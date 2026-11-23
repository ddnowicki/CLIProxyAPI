#!/usr/bin/env bash
# The fork's test gate, shared by fork-build (required check) and sync-upstream (strategy trials).
# The golden test is excluded by name because it also fails on untouched upstream (CRLF vs LF, see docs/pooled-claude-reset.md).
set -euo pipefail

go test -count=1 -skip '^TestToolSearchDisabledMatchesDevGolden$' ./...
go test -race -count=1 -run 'Test(PoolCooldown|ModelCooldownErrorClientReset|TerminalPoolCooldown|ClaudePoolCooldown|SharedCooldown|WeightedSessionAffinity|WeightedRoundRobin|Pinned.*Weighted|Execute.*Pool|Execute.*429|AnnotatePoolCooldown)' ./sdk/cliproxy/auth ./sdk/api/handlers/claude
go build -buildvcs=false -o "${RUNNER_TEMP:-/tmp}/cli-proxy-api" ./cmd/server
