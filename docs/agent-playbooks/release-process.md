# Release process

Releases are cut from `main`, versioned on this fork's own semver line, and
built by goreleaser into the same shape of artifacts upstream ships.

## Version identity

This fork does **not** mirror upstream's version numbers. It has its own line
starting at `v0.1.0`, and reports the upstream base separately:

```
$ bee version
0.2.0-a1b2c3d4
$ bee version --verbose        # or the startup log line
bee-experimental 0.2.0-a1b2c3d4 (upstream bee v2.8.1)
```

The reasoning: a tag like `v2.8.1-exp.1` would sort *below* `v2.8.1` in semver,
so goreleaser would mark every build a prerelease, no `latest` Docker tag would
publish, and `apt` would refuse the upgrade. It would also read like an official
Ethersphere release candidate, which it must not.

The upstream base travels in `.upstream-base` → the `upstreamBase` ldflag →
`bee version` and the libp2p user agent.

## Why the changelog is git-cliff and not release-please

release-please cannot work in a repository that merges an upstream. It fetches
history through GitHub's GraphQL `Commit.history`, which has **no first-parent
option**, and stops only when it reaches the previous release's commit. So every
upstream commit newer than our last release would land in our changelog and
drive our version bump — including any upstream `feat!:` forcing a major bump on
us. The pollution is bounded by date rather than ancestry, so its size varies
unpredictably from release to release.

git-cliff exposes a `merge_commit` boolean to its commit parsers, so `cliff.toml`
reduces `main` to exactly its first-parent spine:

```toml
{ field = "merge_commit", pattern = "^false$", skip = true }
```

This is the mechanical reason merge subjects must be conventional-commit lines:
**the merge subject is the changelog entry.** Parser order in `cliff.toml` is
load-bearing — git-cliff takes the first match.

## Cutting a release

1. Confirm `main` is green and the ledger is current.
2. Preview what will ship:

   ```bash
   git-cliff --unreleased
   git-cliff --bumped-version
   ```

   Read the preview. It must contain only fork changes — one entry per feature
   merge, plus `chore(upstream)` entries for syncs. **If you see upstream commit
   subjects in there, stop**: the merge-commit discipline has been broken
   somewhere and the changelog is lying.

3. Run the **Prepare release** workflow (`workflow_dispatch`). Leave
   `dry_run` on first: it prints the computed version and the exact changelog
   without committing anything. Re-run with `dry_run` off to commit
   `CHANGELOG.md`, tag, and push — pushing the tag is what triggers the
   goreleaser build in `release.yaml`.

   The workflow carries a tripwire for the one failure mode that silently
   corrupts a release: if any entry starts with `Merge ` or
   `Pull from upstream`, upstream commits have reached the first-parent spine
   (something was squash-merged) and the job fails rather than publishing a
   changelog that misattributes upstream's work to this fork.

## What gets built

Five build targets (linux amd64/386/arm64/armv7, a upx-compressed linux-slim,
windows, darwin amd64 and arm64), plus:

- Docker images to `ghcr.io/crtahlin/bee-experimental`, authenticated with the
  workflow's own `GITHUB_TOKEN` so no long-lived registry credential exists
- `.deb` and `.rpm` packages

Everything upstream publishes to Ethersphere-owned destinations — Docker Hub,
Quay, Gemfury, the Homebrew tap, the Scoop bucket, and the GPG signing that
needs their keys — is removed from `.goreleaser.yml` rather than repointed. The
`stable` Docker tags are gone too: `stable` implies a support commitment this
fork does not make.

The `linux-slim` build runs `upx` in a post hook. Upstream never installs it,
so `release.yaml` does so explicitly — assuming it is present on the runner is
how that build breaks.

The packages are **drop-in replacements** for upstream's `bee` package: the
binary stays at `/usr/bin/bee`, the systemd unit and `/etc/bee/bee.yaml` paths
are unchanged, and the package declares `Conflicts`, `Replaces`, and `Provides`
against `bee` so it cannot be co-installed and can take over the files. An
`epoch: "1"` is set so that `1:0.2.0` outranks upstream's `0:2.8.1` and `apt`
will actually perform the upgrade — without it our lower version number would
look like a downgrade.

## After a release

Announce what an operator needs to know: which upstream base it carries, which
experiments are in it, and what the rollback is. Every release note repeats the
experimental-software warning — people find releases without reading the README.
