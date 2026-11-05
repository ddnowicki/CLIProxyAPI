# Fork maintenance

The maintained deployment branch is `master`. It starts from the tested upstream v8.0.53 source and retains the pooled Claude reset and header-security commits. The original `main` branch remains an upstream snapshot and is not the deployment source.

## Tested images

`fork-build` runs repository tests, the targeted race regressions and a server build before publishing `ghcr.io/ddnowicki/cliproxyapi`. Only pushes or manual runs on `master` can publish; pull requests cannot. Images have an immutable `sha-<commit>` tag and a moving `master` tag. Production must use `:master@sha256:<digest>` so a subsequent build cannot change the running version until the IaC digest is updated.

The documented upstream translator golden test is excluded by name, not by excluding its package. No credentials are added to Docker build arguments or contexts. The server configuration and OAuth credentials stay on the host in the existing mounted directories.

## Renovate

The root `renovate.json` opts this fork into Renovate processing on `master`. Renovate proposes Go module, Docker base-image and GitHub Actions updates, with a three-day minimum release age and no automatic merge. The Renovate GitHub App must be installed for this repository (or for all owner repositories).

The deployment repository separately tracks the `master` image digest. This creates a reviewed IaC change instead of silently replacing a deployed container.

## Upstream application releases

Renovate updates dependencies, not the fork's source history. `sync-upstream` separately checks the latest stable upstream release daily at 04:17 UTC, merges it with the fork changes into an `upstream/<tag>` branch, and runs the same tests and build. A conflict or failing check stops the update; local fixes are never discarded. Successful candidates become pull requests to `master` and require manual approval. Merging an approved candidate runs the tested-image pipeline, after which Renovate can propose the new deployment digest.

The record in `.github/upstream-version` is updated only together with a tested source merge. The initial deployment does not include unrelated newer upstream changes.

The upstream-update workflow needs repository permission for GitHub Actions to create pull requests. Its write token is supplied only to the release-query and publish steps, not to the steps executing candidate code. Source pulls and image publication are constrained to the expected upstream and fork repositories.

## Rollback

Keep the previous image digest in the IaC commit history. Reverting only that image-reference change and converging the `cliproxy` stack restores the previous binary while preserving the existing auth and configuration volumes. Upstream synchronization and Renovate do not deploy on their own.

Account-policy and unattended-operation limitations from `pooled-claude-reset.md` remain applicable. An owner-approved deployment is not evidence of authorization by Anthropic.
