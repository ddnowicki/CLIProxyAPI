# Routing account pools after the upstream update

This research compares the deployed fork commit `294bc7ec` with upstream
`c19787b2` (November 16, 2026). Production routing remains unchanged. An image
digest update and routing configuration change belong in a separate oopsie
rollout after the updated fork passes its checks.

## What the update provides

Upstream [d2045150](https://github.com/router-for-me/CLIProxyAPI/commit/d20451504f473d303a877a8f4edef195f988da4e)
adds weighted routing compensation for sticky sessions. Requests that reuse a
session binding, inherit a parent binding, match an LCP binding, or explicitly
pin a credential consume weighted credits without moving existing bindings.
New assignments can therefore favor credentials that have served less traffic.
Follow-up commits `e84d4836` and `9d7d012b` improve pinned-request accounting and
fairness when eligible pools change.

See `config.example.yaml` under `routing`,
`sdk/cliproxy/auth/selector_weighted.go`, and
`sdk/cliproxy/auth/selector_weighted_affinity_test.go`.

The deterministic upstream test
`TestWeightedSessionAffinityCompensatesStickyRequests` illustrates the behavior:
with weights A:B = 3:1, four requests remain bound to B, then twelve fresh
selections go to A to compensate, while B's existing parent binding remains
unchanged. This is a controlled selection test, not a prediction of production
token consumption or account allowance.

## Recommended first configuration

For healthy accounts on equivalent plans, start with equal weights. OAuth auth
files default to weight 1 when no top-level numeric `weight` field is present.
Different provider pools are selected separately. Keep peer credentials at the
same priority; different priority tiers do not share the same weighted budget.

```yaml
routing:
  strategy: weighted-round-robin
  session-affinity: true
  session-affinity-ttl: 1h
  session-affinity-subagents: true
  session-affinity-rebind-on: quota
```

This is a routing-section excerpt. Preserve the deployment's remaining
configuration. A longer affinity TTL is not required for this experiment.

Before enabling it:

- Repair or disable credentials with unresolved authentication or availability
  problems. A failed usage lookup alone does not prove that generation quota is
  exhausted, but an unverified account is not a dependable reserve.
- Confirm clients send stable session identifiers. Upstream documents explicit
  `X-Session-ID` support for clients whose compaction replaces the full history;
  otherwise compaction can create a fresh binding.
- Verify real parent/subagent identifiers reach the proxy. Inheritance depends
  on recognizable relationships and eligible credentials; it is not universal
  across every client and model transition.
- Schedule selector-changing configuration updates at a quiet point: selector
  replacement or process restart resets process-local accounting and can lose
  affinity state.

Retain `rebind-on: quota` for the initial canary. The new `never` option refuses
failover for an established session and returns its error instead. It trades
availability for strict credential continuity; it is not a balancing strategy.
See [issue #7348](https://github.com/router-for-me/CLIProxyAPI/issues/7348).

## What weighted routing does not provide

The standalone weighted selector charges local selections, not output tokens,
cache writes, reasoning work, or percentage of a subscription allowance. A
large request and a small request each count as one selection. Credits are
scoped to eligible provider/model/priority/transport pools, bounded to roughly
32 pool rounds, and process-local. The selector can compensate through future
assignments but cannot rebalance a pool consisting only of existing long-lived
sessions without breaking their bindings.

Equal weights improve request distribution; they do not guarantee equal 5-hour
or weekly percentages. Static unequal weights express an operator's chosen
ratio, not a live measurement of remaining quota or time until reset.

## Quota-aware routing is separate work

The supported built-in strategies remain `round-robin`, `weighted-round-robin`,
and `fill-first`. `sdk/cliproxy/auth/quota_signals.go` records passive response
header observations, explicitly without changing cooldown or scheduling state.
Those observations are not inputs to the built-in weighted selector.

Upstream [issue #5782](https://github.com/router-for-me/CLIProxyAPI/issues/5782)
requests Codex selection based on remaining allowance. It was closed as not
planned; the maintainer's September response says routing should not be coupled
to remaining quota. Current passive observation support should not be confused
with the adaptive selection requested there.

[PR #4674](https://github.com/router-for-me/CLIProxyAPI/pull/4674) remains open
and unmerged at the research date. It proposes Claude-specific quota-aware
routing and a usage poller, along with separate retry and subagent identity
changes. It is not shipped Codex support and should not be bundled into an
upstream synchronization without a separate review.

A later adaptive policy would need comparable, fresh, model-relevant 5-hour and
weekly observations, reset deadlines, explicit treatment of stale or missing
data, and preserved session bindings. A scheduler plugin is an extension point,
not a ready-made solution: the current scheduler candidate conversion does not
include the host's quota snapshots. Avoid adding request-path polling or a
high-frequency usage poller to approximate this feature.

## Canary and rollout evidence

Keep the workload comparable before increasing concurrency. Record per account:

- New bindings, affinity hits, parent inheritance, and account changes with their
  reasons; distinguish request count from completed-stream success.
- Upstream and final 429/401/403/5xx responses, cooldown deadlines, latency, and
  stream cancellation/error outcomes.
- Input, output, cache-read and cache-write token totals where available.
- 5-hour and weekly utilization snapshots with observation timestamps, reset
  times, model scope, and source. Prefer passive upstream response headers.
  When occasional explicit usage refreshes are needed, bound their frequency
  and concurrency and back off on rate limiting. Retain observation age; never
  display a stale percentage as a fresh reading or interpret missing data as 0%.

Compare request distribution and allowance consumption over more than one
window. An idle credential may retain an old snapshot; mark it unknown/stale
rather than claiming the deployment has already demonstrated balanced quota.
Keep the prior image digest and configuration available for rollback.

This approach preserves binding behavior while improving assignment fairness.
It does not establish that provider enforcement or ban risk is unchanged, and
it does not require changing fingerprints or headers to conceal usage.
