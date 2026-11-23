#!/usr/bin/env bash
# The fork's test gate, shared by fork-build (required check) and sync-upstream (strategy trials).
# The golden test is excluded by name because it also fails on untouched upstream (CRLF vs LF, see docs/pooled-claude-reset.md).
#
# Upstream sometimes tags a release whose own tests are red. A failing test is tolerated only when the very
# same test also fails on the pristine upstream tag named in .github/upstream-version; anything else
# (a new failure that also fails an isolated re-run, a build/setup failure, a package that fails outside a test)
# fails the gate.
set -euo pipefail

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
skip='^TestToolSearchDisabledMatchesDevGolden$'

# run_tests <dir> <json-out> [go test args...]: prints package results and failing test output, never fails.
run_tests() {
  local dir="$1" out="$2"
  shift 2
  (cd "$dir" && go test -count=1 -json "$@" >"$out" 2>"$out.stderr") || true
  cat "$out.stderr" >&2
  jq -rj 'select(.Action=="output" and .Test==null) | .Output' "$out"
}

# failed_tests <json>: "<package> <TopLevelTest>" for every failing test.
failed_tests() {
  jq -r 'select(.Action=="fail" and .Test!=null) | "\(.Package) \(.Test | split("/")[0])"' "$1" | sort -u
}

run_tests . "$work/fork.json" -skip "$skip" ./...
failed_tests "$work/fork.json" >"$work/fork.fail"
jq -r 'select(.Action=="fail" and .Test==null) | .Package' "$work/fork.json" | sort -u >"$work/fork.pkgfail"
cut -d' ' -f1 "$work/fork.fail" | sort -u >"$work/fork.testpkgs"

hard="$(comm -23 "$work/fork.pkgfail" "$work/fork.testpkgs")"
if [ -n "$hard" ] || grep -q '"FailedBuild"' "$work/fork.json"; then
  echo "::error::packages failed outside a test (build/setup failure): ${hard:-see build output}"
  exit 1
fi

if [ -s "$work/fork.fail" ]; then
  echo "Failing tests:"
  sed 's/^/  /' "$work/fork.fail"
  failing="$(jq -Rsc 'split("\n") | map(select(length > 0) | split(" "))' "$work/fork.fail")"
  jq -rj --argjson f "$failing" \
    'select(.Action=="output" and .Test!=null and ([.Package, (.Test | split("/")[0])] | IN($f[]))) | .Output' \
    "$work/fork.json" | grep -v '^=== \(RUN\|PAUSE\|CONT\|NAME\)' || true

  tag="$(tr -d '\r\n' <.github/upstream-version)"
  echo "Checking whether these also fail on pristine upstream ${tag}..."
  git fetch -q --depth=1 --no-tags https://github.com/router-for-me/CLIProxyAPI.git "refs/tags/${tag}:refs/tags/${tag}"
  git worktree add -q --detach "$work/upstream" "refs/tags/${tag}"
  trap 'git worktree remove --force "$work/upstream" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

  run_regex="^($(cut -d' ' -f2 "$work/fork.fail" | sort -u | paste -sd'|'))\$"
  mapfile -t pkgs < <(cut -d' ' -f1 "$work/fork.fail" | sort -u)
  run_tests "$work/upstream" "$work/upstream.json" -run "$run_regex" "${pkgs[@]}" >/dev/null
  failed_tests "$work/upstream.json" >"$work/upstream.fail"

  comm -23 "$work/fork.fail" "$work/upstream.fail" >"$work/new.fail"
  new=""
  if [ -s "$work/new.fail" ]; then
    # Timing-sensitive upstream tests (websocket, cooldown) occasionally fail under the full parallel suite.
    # A fork-only failure gets one isolated re-run; only a test that fails again blocks the gate.
    echo "Re-running fork-only failures once in isolation..."
    rerun_regex="^($(cut -d' ' -f2 "$work/new.fail" | sort -u | paste -sd'|'))\$"
    mapfile -t rerun_pkgs < <(cut -d' ' -f1 "$work/new.fail" | sort -u)
    run_tests . "$work/rerun.json" -run "$rerun_regex" "${rerun_pkgs[@]}"
    failed_tests "$work/rerun.json" >"$work/rerun.fail"
    new="$(comm -12 "$work/new.fail" "$work/rerun.fail")"
    if grep -q '"FailedBuild"' "$work/rerun.json"; then new="$(cat "$work/new.fail")"; fi
    comm -23 "$work/new.fail" "$work/rerun.fail" | while read -r pkg test; do
      echo "::warning::flaky: ${pkg} ${test} failed in the full suite and passed when re-run"
    done
  fi
  if [ -n "$new" ]; then
    echo "::error::tests failing only in the fork (not on upstream ${tag}):"
    echo "$new"
    exit 1
  fi
  comm -12 "$work/fork.fail" "$work/upstream.fail" | while read -r pkg test; do
    echo "::warning::tolerated: ${pkg} ${test} also fails on pristine upstream ${tag}"
  done
fi

go test -race -count=1 -run 'Test(PoolCooldown|ModelCooldownErrorClientReset|TerminalPoolCooldown|ClaudePoolCooldown|SharedCooldown|WeightedSessionAffinity|WeightedRoundRobin|Pinned.*Weighted|Execute.*Pool|Execute.*429|AnnotatePoolCooldown)' ./sdk/cliproxy/auth ./sdk/api/handlers/claude
go build -buildvcs=false -o "${RUNNER_TEMP:-/tmp}/cli-proxy-api" ./cmd/server
