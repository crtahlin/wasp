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
wasp 0.2.0-a1b2c3d4 (upstream bee v2.8.1)
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

- Docker images to `ghcr.io/crtahlin/wasp`, authenticated with the
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

## Release pull requests and CI

### Which checks are required on `main`

`Protocol freeze`, `Lint PR Title`, `Lint` and `Test (ubuntu-latest)`.

`Test (macos-latest)` and `Test (windows-latest)` are deliberately **not** required. Both
run on every pull request and should be read, but neither gates a merge: the macOS runner
is roughly 25x slower than a local machine on the redundancy tests, which is what made #79
look like a hang when it was slowness. #97 raises the per-package timeout to fit. Promote
both to required once a run of syncs and releases has gone through without either flaking.

Requiring a check that a workflow can skip is what caused #45 — see below. Before adding
any context here, confirm it reports on **every** pull request, including one that touches
only Markdown.

The first release pull request ([#43](https://github.com/crtahlin/wasp/pull/43)) merged
with no build or test having run, and put a lint failure on `main` — a `CHANGELOG.md`
ending in a blank line, which `make check-whitespace` rejects.

The cause was path filtering, not anything to do with the pull request being
bot-authored. `go.yml` carried `paths-ignore: '**/*.md'` on its `pull_request` trigger,
and a release pull request changes exactly one file: `CHANGELOG.md`. So `Lint` and `Test`
were never triggered. Two other workflows did run on that same pull request, on the same
`pull_request` event, which is how we know bot authorship was not the obstacle —
`assign-author` even reported a failure, and the merge went through anyway because nothing
was required at the time.

Do not repeat the diagnosis that GitHub held the checks in `action_required` pending
approval. It did not happen here, and `gh api .../approve` — previously recommended in
this playbook — fixes nothing. Check what actually ran before theorising:

```bash
SHA=$(gh pr view <n> --repo crtahlin/wasp --json headRefOid -q .headRefOid)
gh api "repos/crtahlin/wasp/commits/$SHA/check-runs" --jq '.check_runs[] | "\(.name) \(.conclusion)"'
```

Three things now prevent a recurrence:

1. **`paths-ignore` is gone from the `pull_request` trigger** in `go.yml`, so `Lint` and
   `Test` always report. Documentation-only pull requests skip the expensive work through
   a step-level condition instead, which still reports the context. A required check that
   never reports blocks a pull request forever, so the two cannot be combined. See #45.
2. **The workflow verifies before pushing.** `make check-whitespace` and the
   protocol-freeze check run against the commit that will be proposed — after the
   changelog is written, not before it. On failure nothing is pushed and no pull request
   exists.
3. **`main` requires status checks**, so no pull request merges on zero evidence.
