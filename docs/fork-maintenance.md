# Fork maintenance

The maintained deployment branch is `master`. It tracks the latest stable upstream release (recorded in `.github/upstream-version`) and retains the fork cooldown-compatibility and header-security behavior. Upstream now provides the terminal pool-reset implementation; the fork keeps its additional regression coverage and header restrictions. The original `main` branch remains an upstream snapshot and is not the deployment source.

## Tested images

`fork-build` runs repository tests, the targeted race regressions and a server build before publishing `ghcr.io/ddnowicki/cliproxyapi`. Only pushes or manual runs on `master` can publish; pull requests cannot. Images have an immutable `sha-<commit>` tag and a moving `master` tag. Production must use `:master@sha256:<digest>` so a subsequent build cannot change the running version until the IaC digest is updated.

The documented upstream translator golden test is excluded by name, not by excluding its package. No credentials are added to Docker build arguments or contexts. The server configuration and OAuth credentials stay on the host in the existing mounted directories.

The v8.0.75 synchronization was validated on Linux with Go 1.26.11: the full test suite passed with that single named golden exclusion, the targeted cooldown/security/weighted-affinity race regressions passed, and the server build passed. The golden failure was reproduced on the untouched upstream commit as documented in `pooled-claude-reset.md`. Routing recommendations and their quota-accounting limitations are documented in `fork-routing.md`.

## Renovate

The root `renovate.json` opts this fork into Renovate processing on `master`. Renovate proposes Go module, Docker base-image and GitHub Actions updates, with a three-day minimum release age and no automatic merge. The Renovate GitHub App must be installed for this repository (or for all owner repositories).

The last `fork-build` job on `master` (`deploy`, after `test`, `image-smoke` and the GHCR push) commits the new `:master` digest to `projects/cliproxy/vars.yml` in ddnowicki/oopsie; that push runs the guarded oopsie deploy (smoke test on the real accounts, automatic rollback). The job checks out and runs no fork code; its App token is limited to oopsie contents.

## Upstream application releases

`sync-upstream` checks the latest stable upstream release every 15 minutes and is fully unattended:

1. **detect** compares `releases/latest` with `.github/upstream-version`. One update is in flight at a time: an open `autoupdate` pull request (or a `needs-human` one for the same release) makes newer releases wait. A `needs-human` pull request for an older release does not: the newer release is tried and that pull request is closed as superseded, because upstream often fixes a broken release within hours.
2. **trial** (no secrets, runs candidate code) merges the tag with `.github/autoupdate/merge.sh`, first plainly, then — only when there are conflicts — with `-X theirs` (upstream side of each conflicting hunk) and `-X ours` (fork side). Every merge restores `.github/workflows` to master's content, so upstream workflows never enter the fork, and records the tag in `.github/upstream-version`. The first strategy whose merge passes `.github/autoupdate/verify.sh` (the full suite, the fork race regressions and the server build) wins. A test that fails in the merge is tolerated only when the same test also fails on the pristine upstream tag (upstream occasionally tags red releases); build or setup failures and fork-only test failures are never tolerated, and each tolerated test is reported as a workflow warning. The fork contract tests (`fork_pool_cooldown_test.go`, `code_handlers_fork_pool_cooldown_test.go`) are what makes automatic conflict resolution safe: a strategy that drops a fork patch fails them.
3. **publish** (GitHub App token, runs no repository code) recomputes the winning merge, checks that its tree is the tested tree and that the workflow tree equals master, pushes `upstream/<tag>` and opens a pull request labelled `autoupdate` with auto-merge enabled. The required checks (`test`, `image-smoke`) run again on the pull request.
4. When no strategy works, the pull request carries the plain merge (conflict markers committed as-is) and the label `needs-human`, without auto-merge. Resolve it on that branch, or let the next upstream release supersede it.

The serwerownia watcher (oopsie `projects/cliproxy`) reads this repository's public API and alerts through Grafana/Discord on a `needs-human` pull request, an update pull request open for more than three hours, a failing `sync-upstream` run, or a release not deployed within 48 hours.

`image-smoke` boots the candidate image as production does (UID 1001, `HOME=/data`, mounted auth dir) and probes `/healthz` and `/v1/models`, then starts the current `:master` image on the auth dir the candidate initialised, so a store-format change that would block a rollback fails before merge.

The upstream workflows (`release.yaml`, `docker-image.yml`, the PR guards) are deleted in the fork; only `fork-build` and `sync-upstream` exist. Merging an upstream history that touched workflow files requires the App's `workflows` permission; the tree pushed never contains upstream workflow content.

### One-time setup

- A GitHub App with Contents, Pull requests and Workflows read/write, installed on this repository and on ddnowicki/oopsie (each job mints a token for one repository: this one in `sync-upstream`, oopsie contents only in `fork-build`'s `deploy`); its client ID in the Actions variable `AUTOUPDATE_APP_CLIENT_ID`, its private key in the secret `AUTOUPDATE_APP_PRIVATE_KEY`.
- Repository settings: allow auto-merge, delete head branches on merge.
- A ruleset on `master`: pull request required, required status checks `test` and `image-smoke` (GitHub Actions), no force push, no deletion; the owner may bypass.

## Rollback

Production pins `:master@sha256:<digest>`. The oopsie deploy keeps the last good digest on the host, rolls back automatically when the post-deploy smoke test or the one-hour watchdog fails, quarantines the bad digest and never restores the auth directory (single-use refresh tokens and the Codex refresh journal make an old copy unsafe). Manual rollback: revert the digest bump in oopsie.

Account-policy and unattended-operation limitations from `pooled-claude-reset.md` remain applicable. An owner-approved deployment is not evidence of authorization by Anthropic.
