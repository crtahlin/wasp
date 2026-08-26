# Upstream sync

This fork absorbs `ethersphere/bee` continuously. Nothing ever goes the other
way.

## What we track, and why

**Upstream release tags, and only final ones — never `master` HEAD, never
`-rc`.**

Bee is a live network participant. A mid-refactor `master` can carry a bumped
protocol version that no deployed peer speaks yet, which would silently
partition our nodes from the network while every local test still passed. Tags
are also the only upstream commits that have been through the beekeeper
integration suite. Release candidates can be pulled deliberately into a
`sync/upstream/vX.Y.Z-rcN` branch for testing, but they are never merged to
`main` on a schedule.

`.upstream-base` at the repository root holds one line — the upstream tag `main`
currently derives from. It is the single source of truth: the sync workflow
compares against it, the Makefile injects it into the binary, and `bee version`
reports it.

## Merge, never rebase

`main` is published and other operators pull it. Rebasing it would rewrite tens
of thousands of commits on every sync and break every clone. Merge is also what
keeps the **merge base advancing** — which is the whole reason syncs stay cheap.

This is worth being blunt about, because the previous fork got it wrong: if you
squash an upstream sync, `git merge-base main upstream/master` never moves, and
the *next* sync three-way-merges from the original fork point again. That is how
a fork ends up re-resolving the same conflicts forever. **Never squash a sync.**

## The automated path

`.github/workflows/upstream-sync.yml` runs weekly and on demand. It:

1. fetches upstream tags and finds the newest non-RC tag
2. compares it against `.upstream-base`, stopping if there is nothing new
3. creates `sync/upstream/vX.Y.Z` from `main`
4. attempts `git merge --no-ff upstream/vX.Y.Z` — a real two-parent merge
5. updates `.upstream-base` and regenerates `.github/protocol-freeze.lock` in
   the same commit
6. opens a pull request `chore(upstream): sync bee vX.Y.Z`, labelled
   `upstream-sync`
7. on conflict, still pushes the branch, marks the title `[CONFLICTS]`, and
   lists the conflicting paths in the body

## Reviewing a sync pull request

Three things, in order:

1. **The protocol-freeze diff.** `git diff main...HEAD -- .github/protocol-freeze.lock`.
   If upstream moved a protocol minor, read
   `docs/agent-playbooks/protocol-compatibility.md` on what that means for
   dialability before merging.
2. **Our own files.** The ones that carry fork changes and therefore conflict:
   `AGENTS.md` (the prepended block), `README.md`, `.goreleaser.yml`,
   `Makefile`, `version.go`, `.golangci.yml`, `.github/workflows/`. Conflicts
   here are expected and small; take care that resolving them does not silently
   revert a fork change.

3. **Workflows upstream ADDED, and jobs added inside workflows we keep.** This
   one is easy to miss because it does not conflict — a new file merges cleanly
   and then fails on every subsequent run.

   The `v2.8.2` sync showed the harder version: upstream added two **jobs inside
   `go.yml`** rather than new files. `Coverage Report` needs `CODECOV_TOKEN`
   with `fail_ci_if_error: true`, `Trigger Beekeeper` needs `GHA_PAT_BASIC`, and
   it also moved Linux testing onto a `[self-hosted, linux, bee]` runner that
   does not exist here and would queue forever. Read the whole diff of every
   workflow that conflicts, not only the file list.
   Upstream keeps adding workflows that need Ethersphere organisation secrets
   or push into Ethersphere repositories. A real example: the `v2.8.1` →
   `v2.8.2-rc1` sync introduces `swarm-cli-bee-version.yaml`, which dispatches
   into `ethersphere/swarm-cli` on release. Delete anything of that kind in the
   sync pull request and record it in the body.

   ```bash
   git diff --name-status main...HEAD -- .github/workflows/ | grep '^A'
   ```
4. **CI green**, then merge with a merge commit.

## Never `git fetch --tags upstream`

The `upstream` remote is configured to fetch tags into a **separate namespace**,
`refs/tags/upstream/*`, by an extra refspec that `scripts/setup-upstream.sh`
installs. `git fetch --tags` overrides that refspec and drops upstream's tags
into the plain namespace instead.

That is not untidy, it is wrong, and it is wrong silently. The Makefile derives
the fork version with `git describe --tags --match 'v[0-9]*'`, so the moment
upstream's `v2.8.2` lands in `refs/tags/`, a local build reports:

```
wasp 2.8.2-8d9caf63 (upstream bee v2.8.2)
```

The fork claims upstream's version number as its own. Nothing fails, no test
catches it, and a binary built and deployed in that state misidentifies itself
to whoever reads its logs six months later. It happened during the `v2.8.2`
sync.

Use the configured refspec, which needs no flag:

```bash
git fetch upstream                    # tags land in refs/tags/upstream/*
git rev-parse upstream/v2.8.2         # this is how you name the tag
```

If it has already happened, delete only the tags that upstream also owns:

```bash
for t in $(git tag -l 'v[0-9]*'); do
  git rev-parse -q --verify "refs/tags/upstream/$t" >/dev/null && git tag -d "$t"
done
git describe --tags --abbrev=0 --match 'v[0-9]*'   # must be a wasp version
```

Check `git ls-remote --tags origin 'refs/tags/v2*'` as well: if the polluted
tags were pushed, every clone inherits the problem and they have to be deleted
on the remote too.

## Resolving conflicts locally

```bash
git fetch origin && git switch sync/upstream/vX.Y.Z
git reset --hard HEAD~1
git merge --no-ff upstream/vX.Y.Z
# resolve, then:
echo vX.Y.Z > .upstream-base
scripts/protocol-freeze.sh > .github/protocol-freeze.lock
git add -A && git commit -m "chore(upstream): sync ethersphere/bee vX.Y.Z"
git push --force-with-lease
```

## Feature branches after a sync

Two different lifecycles — conflating them is the usual mistake.

**Unmerged `exp/*` branches** get rebased onto `main`, keeping them as clean
patch series so `scripts/export-patch.sh` keeps working:

```bash
git switch exp/<n>-<slug>
git rebase main
```

If the conflict is in code the experiment *replaces* wholesale — a rewritten
hasher, a replaced scheduler — take ours and re-verify against upstream's new
tests. If upstream changed the **interface** the experiment builds on, drop the
commit and re-apply the idea on top rather than hand-merging; a hand-merged
interface change is how subtle behavioural drift enters.

**Merged `exp/*` branches are frozen.** Their content is already in `main`. The
branch pointer and the `exp-*` tag exist only to name the patch series. Do not
keep rebasing them.

## Verifying a sync actually merged properly

```bash
git log --merges -1 --format='%H %P'          # must show TWO parents
git merge-base main upstream/vX.Y.Z            # must equal the upstream tag commit
```

If the first shows one parent, the sync was squashed and must be redone.

## Bot-authored pull requests and CI

A pull request opened by this workflow is authored by `github-actions[bot]`. That is not
by itself a problem: workflows do run on such pull requests here, confirmed on
[#43](https://github.com/crtahlin/wasp/pull/43), where two of them reported.

What went wrong on #43 was path filtering — `go.yml` ignored `**/*.md` on `pull_request`,
and that pull request changed only `CHANGELOG.md`, so `Lint` and `Test` were never
triggered and it merged on zero evidence. An earlier version of this playbook blamed
GitHub holding checks in `action_required` and told you to run `gh api .../approve`. That
was wrong, and the remedy does nothing. Read the check runs for the head SHA before
concluding anything about why a check is missing.

Two things now prevent a recurrence:

1. **`paths-ignore` is gone from the `pull_request` trigger**, so the contexts always
   report; documentation-only pull requests skip the work at step level instead. See #45.
2. **The workflow verifies before opening the pull request.** `make build` runs inside the
   sync job on the clean path, so a merge that resolves but does not compile never becomes
   a pull request.

The verification is deliberately skipped when the merge conflicts. A conflicted sync is
committed with its markers intact so it can be resolved locally, and that tree cannot
build; failing the job there would suppress the `[CONFLICTS]` pull request altogether and
you would never learn the sync needs attention.

Do not work around it by merging with admin rights. The protocol-freeze check is the one
thing proving upstream has not moved the wire surface underneath us, and a sync is
precisely when that matters.

`Test (macos-latest)` and `Test (windows-latest)` are deliberately **not** required. #79
turned out to be slowness rather than a flake — `TestBzzUploadDownloadWithRedundancy` takes
6 s normally, 123 s under `-race`, and 9m42s on a macOS runner, against Go's 10-minute
default — and #97 raises the timeout to fit. Promote both to required once several syncs
and releases have passed with neither failing. The full list of required contexts and the
rule for adding to it are in `release-process.md`.
