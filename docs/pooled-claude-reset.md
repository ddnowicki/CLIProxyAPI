# Pooled Claude cooldown reporting

## Scope

This fork is synchronized with CLIProxyAPI v8.0.75, commit `c19787b2`. The original fork patch targeted v8.0.53; the implementation now builds on upstream terminal pool cooldown reporting rather than maintaining a second terminal aggregation path.

It makes terminal local Claude HTTP 429 errors advertise the earliest known future availability of an eligible credential, rather than the last failed credential's retry hint. It does not reset upstream quotas, enable overage, change account credentials, or modify Claude Code or T3.

Upstream terminal aggregation also covers other providers and mixed-provider routes. This sync retains that upstream scope and preserves provider diagnostics. Request-scoped refusals and cancelled requests are not reclassified as pool cooldowns. Shared selection and cooldown helpers affect Claude, Codex and other providers, so their regression suites must continue to pass. In particular, `credential_quota` without a recovery time remains blocked until its state is updated, rather than being treated as a known model-only reset.

## Why a core patch

In this release, successful-response and stream-payload plugin interceptors are not called for final selection errors. Scheduler plugins receive available candidates, not the complete model cooldown state. Runtime auth plugin summaries do not expose all per-model cooldowns. A plugin cannot implement both correct pool availability and final-error rewriting through the existing interfaces without replacing or duplicating core routing.

## Behavior

1. Evaluate the active global and requested-model blocks for each credential. A known credential-wide block cannot hide a longer model block. Disabled, terminal-auth-failed, expired, or indefinitely blocked state must not become a finite availability promise.
2. At the terminal execution boundary, inspect the full request-eligible pool. Ignore the finished request's `tried` set and retry budget, but retain explicit credential pinning, credential policy, model support, aliases, executor registration and weighted-selector exclusions.
3. Resolve execution-model alternatives without advancing alias rotation. Choose the earliest known deadline across eligible alternatives and credentials. If any eligible candidate is already available, leave the original error unchanged rather than claiming the pool is cooling down.
4. Preserve the upstream error text, body and status through upstream `poolCooldownTerminalError`, but use the pool deadline for the client retry hint. Only upstream `Request-Id` and `X-Request-Id` headers may survive in a cooldown error for opt-in diagnostic passthrough; connection-scoped headers are excluded. Cookies, credentials, internal headers, CORS, framing and account-specific quota headers are not copied. Only locally generated retry metadata is exposed through the safe-header path.
5. In the Claude API handler, append `(reset after Ns)` to `error.message` exactly once and use the same deadline for the `Retry-After` header. Keep integer seconds for T3 compatibility even for delays longer than a minute. Whole seconds round upward. T3 ignores zero-duration resets.

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

`TestToolSearchDisabledMatchesDevGolden` also fails on an untouched archive of v8.0.75 (`c19787b2`) in the Linux Go 1.26.11 validation environment. The mismatch involves CRLF versus LF in raw tool declarations. The existing exclusion remains limited to that exact test; the rest of its translator package is tested. This is a validation limitation, not a passing golden comparison.

The v8.0.75 baseline also has a stale plugin integration assertion: a decoded 7-second retry hint is expected unchanged after terminal pool aggregation, although the effective quota cooldown has a 10-second floor. This fork retains the 7-second decoding assertion and updates the terminal-header and stored-cooldown assertions to that effective floor. No runtime retry behavior was changed to satisfy the test.

`-buildvcs=false` is used for checks performed inside the Docker build environment because the Windows checkout is bind-mounted into Linux; it does not bypass compilation, tests, hooks, or signing.

## Security and account-policy limitations

The patch does not establish permission to collect or intermediate Claude.ai OAuth credentials. Anthropic's Claude Code legal/compliance documentation distinguishes a user signing in to the unmodified Claude Code binary from a third-party platform collecting, storing or intermediating Claude.ai credentials. Assess the actual account plan and applicable terms, or obtain explicit authorization, before using a subscription OAuth bridge. API/provider credentials under an appropriate agreement are a different integration path.

Auto-resume can increase unattended requests and quota consumption. It is not evidence of account-policy compliance and is not a guarantee against suspension. No numerical suspension probability or claim about private abuse-detection heuristics is supported by this review. Full T3 recovery, concurrency, retry-budget and spending-limit integration tests remain required before enabling unattended operation. A randomized positive retry delay can spread load; it is not a method for hiding automated access.

Accounts belonging to different people or organizations should not be combined without an explicit data-processing and authorization boundary. Changing credentials can change which account or organization processes a conversation, and may lose cache reuse. Those concerns exist in the existing pooling architecture, not just this patch.

Keep production deployment on hold until account-policy authorization and the operational conditions above are resolved. This fork does not implement a scheduler concurrency limit or bounded T3 auto-resume policy. Building the fork must not include runtime secrets: `.dockerignore` excludes root and nested `.env` files and their dot-suffixed variants, in addition to the existing `auths` and `config.yaml` exclusions. Do not put secrets in differently named source/configuration files and assume this exclusion will find them.

References checked on 2026-11-05:

- https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use
- https://www.anthropic.com/legal/consumer-terms
- https://www.anthropic.com/news/2026-usage-policy-update

The published Usage Policy update takes effect on 2026-11-12; it is not treated here as already effective on 2026-11-05. Consumer Terms can be regional; apply the version governing the actual account.

## Build and deployment preparation

Build the fork with the existing Dockerfile and a distinct local tag:

```sh
docker build \
  --build-arg VERSION=v8.0.75-pool-reset \
  --build-arg COMMIT="$(git rev-parse HEAD)" \
  --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t cliproxyapi:v8.0.75-pool-reset .
```

Publishing a registry image and changing production IaC are separate deployment steps. After approval, publish an immutable image, obtain its registry digest, and update only the image reference in the deployment repository. Preserve the previous digest for rollback. Do not copy or commit runtime configuration or auth files into the fork or image.
