# Pooled Claude cooldown reporting

## Scope

This patch is based on CLIProxyAPI v8.0.53, commit `01b24f1f95dc9b309765212c52b25b25e005ff01`.

It makes terminal local Claude HTTP 429 errors advertise the earliest known future availability of an eligible credential, rather than the last failed credential's retry hint. It does not reset upstream quotas, enable overage, change account credentials, or modify Claude Code or T3.

The new terminal aggregation applies only to single-provider `claude` requests. Home-managed scheduling, custom plugin schedulers, request-scoped refusals, cancellations, mixed-provider routes, and other providers keep their existing terminal-error behavior.

## Why a core patch

In this release, successful-response and stream-payload plugin interceptors are not called for final selection errors. Scheduler plugins receive available candidates, not the complete model cooldown state. Runtime auth plugin summaries do not expose all per-model cooldowns. A plugin cannot implement both correct pool availability and final-error rewriting through the existing interfaces without replacing or duplicating core routing.

## Behavior

1. Evaluate the active global and requested-model blocks for each credential. A known credential-wide block cannot hide a longer model block. Disabled, terminal-auth-failed, expired, or indefinitely blocked state must not become a finite availability promise.
2. At the terminal Claude execution boundary, inspect the full request-eligible pool. Ignore the finished request's `tried` set and retry budget, but retain explicit credential pinning, credential policy, model support, aliases, executor registration and weighted-selector exclusions.
3. Resolve execution-model alternatives without advancing alias rotation. Choose the earliest known deadline across eligible alternatives and credentials. If any eligible candidate is already available, leave the original error unchanged rather than claiming the pool is cooling down.
4. Preserve the upstream diagnostic as a cause, but use the pool deadline for the client retry hint. Preserve upstream headers for opt-in header passthrough; only trusted retry metadata is exposed through the safe-header path.
5. Put `(reset after Ns)` in `error.message` before upstream diagnostics, and use the same deadline for the `Retry-After` header. Whole seconds round upward. T3 ignores zero-duration resets.

A deadline is the earliest **locally known opportunity to retry**, not a guarantee of upstream acceptance. Accounts with unknown recovery times may recover before it. Concurrent requests, new upstream restrictions and credential refresh can change availability.

## Client compatibility

The installed T3 version `0.1.7-nightly.20261103.4138` recognizes `(reset after Ns)` in a Claude Code terminal `API Error:` result with `api_error_status: 429`. Its existing usage-limit recovery worker can schedule continuation when auto-resume is enabled.

A local synthetic HTTP 429 smoke test against installed Claude Code `2.1.318` produced:

```json
{
  "type": "result",
  "subtype": "success",
  "is_error": true,
  "terminal_reason": "api_error",
  "api_error_status": 429,
  "result": "API Error: Request rejected (429) · Synthetic pool cooling down (reset after 120s)"
}
```

The smoke test used dummy authentication, an isolated temporary profile, no tools/plugins/MCP/hooks, no session persistence, and one localhost request. No real model was called. The smoke harness is workstation-specific and is not included in this repository.

T3 computes a relative reset from receipt of the terminal result, so transport and internal CLI processing can shift its planned retry slightly later than the proxy deadline. A full live T3 UI/recovery integration test has not been performed.

Once an SSE stream has already been committed, its HTTP status and headers cannot be rewritten. This patch preserves existing streaming framing and supports cooldown metadata in terminal stream-bootstrap errors; it does not change pre-first-token keepalive policy.

## Verification

```sh
go test -count=1 ./sdk/cliproxy/auth ./sdk/api/handlers/...
go test -count=1 -run TestPluginErrorCarriesRetryAfterIntoCooldownAndHTTPHeader ./internal/pluginhost
go test -race -count=1 -run 'Test(PoolCooldown|ModelCooldownErrorClientReset|TerminalPoolCooldown|ClaudePoolCooldown)' ./sdk/cliproxy/auth ./sdk/api/handlers/claude
go build -buildvcs=false -o cli-proxy-api ./cmd/server
```

The unfiltered repository suite fails in two subcases of `TestToolSearchDisabledMatchesDevGolden` (`named_declarations` and `named_declarations_session`) in `internal/translator/claude/openai/responses`. Both failures were reproduced in an untouched checkout of the release commit; their fixtures were not modified. Run all other tests explicitly with:

```sh
go test -count=1 -skip '^TestToolSearchDisabledMatchesDevGolden$' ./...
```

The final verification run passed all other repository tests with that explicit skip, passed the targeted regression tests with `-race`, and successfully built `./cmd/server`, using Go 1.26.10 in an isolated Linux container. The skipped test still fails on the untouched release baseline.

`-buildvcs=false` is used for checks performed inside the Docker build environment because the Windows checkout is bind-mounted into Linux; it does not bypass compilation, tests, hooks, or signing.

## Build and deployment preparation

Build the fork with the existing Dockerfile and a distinct local tag:

```sh
docker build \
  --build-arg VERSION=v8.0.53-pool-reset \
  --build-arg COMMIT="$(git rev-parse HEAD)" \
  --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t cliproxyapi:v8.0.53-pool-reset .
```

Publishing a registry image and changing production IaC are separate deployment steps. After approval, publish an immutable image, obtain its registry digest, and update only the image reference in the deployment repository. Preserve the previous digest for rollback. Do not copy or commit runtime configuration or auth files into the fork or image.
