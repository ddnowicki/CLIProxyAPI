#!/usr/bin/env bash
# Merge an upstream release tag into the current master checkout without running repository code.
#
#   merge.sh <tag> <default|theirs|ours>
#
# The tag must already be fetched as refs/tags/<tag> and HEAD must be a clean master checkout.
# Deterministic for a given (master, tag, strategy): the publish job recomputes the merge and compares tree hashes.
#   - .github/workflows is always restored to master's content (upstream workflows never enter the fork)
#   - .github/upstream-version records the tag
#   - "theirs"/"ours" resolve only conflicting hunks (git merge -X); non-conflicting changes from both sides stay
# Exit codes: 0 merged and committed, 3 unresolved conflicts (merge left in progress), 2 usage error.
set -euo pipefail

tag="${1:-}"
strategy="${2:-}"
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "usage: merge.sh <vX.Y.Z> <default|theirs|ours>" >&2; exit 2; }
case "$strategy" in
  default) options=() ;;
  theirs) options=(-X theirs) ;;
  ours) options=(-X ours) ;;
  *) echo "unknown strategy: $strategy" >&2; exit 2 ;;
esac

git() { command git -c core.hooksPath=/dev/null -c user.name='cliproxy-autoupdate' -c user.email='cliproxy-autoupdate@users.noreply.github.com' "$@"; }

git diff --quiet && git diff --cached --quiet || { echo "working tree is not clean" >&2; exit 2; }
base="$(git rev-parse HEAD)"
upstream="$(git rev-parse "refs/tags/$tag^{commit}")"

set +e
git merge --no-ff --no-commit "${options[@]}" "$upstream" >/dev/null 2>&1
set -e
# A merge that is already up to date leaves no MERGE_HEAD; the version record below still changes.

git rm -r -q -f --ignore-unmatch -- .github/workflows
git checkout "$base" -- .github/workflows
printf '%s\n' "$tag" > .github/upstream-version
git add -- .github/upstream-version

conflicts="$(git diff --name-only --diff-filter=U)"
if [[ -n "$conflicts" ]]; then
  printf '%s\n' "$conflicts"
  exit 3
fi

if git rev-parse -q --verify MERGE_HEAD >/dev/null; then
  git commit -q --no-edit -m "chore: merge upstream ${tag}" -m "Conflict strategy: ${strategy}"
else
  git commit -q -m "chore: record upstream ${tag}" -m "The release is already contained in master."
fi
git merge-base --is-ancestor "$upstream" HEAD || { echo "merge did not include ${tag}" >&2; exit 2; }
git diff --quiet "$base" HEAD -- .github/workflows || { echo "workflow tree drifted from master" >&2; exit 2; }
