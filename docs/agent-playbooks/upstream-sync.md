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

3. **Workflows upstream ADDED.** This one is easy to miss because it does not
   conflict — a new file merges cleanly and then fails on every subsequent run.
   Upstream keeps adding workflows that need Ethersphere organisation secrets
   or push into Ethersphere repositories. A real example: the `v2.8.1` →
   `v2.8.2-rc1` sync introduces `swarm-cli-bee-version.yaml`, which dispatches
   into `ethersphere/swarm-cli` on release. Delete anything of that kind in the
   sync pull request and record it in the body.

   ```bash
   git diff --name-status main...HEAD -- .github/workflows/ | grep '^A'
   ```
4. **CI green**, then merge with a merge commit.

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
