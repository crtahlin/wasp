# Experiment ledger

Every feature, optimization, and fork-local fix that has landed on `main`, and
what came of it. This is the human-readable index; the authoritative record is
the git history itself:

```bash
git log --first-parent main          # every experiment, one line each
git diff <merge>^1 <merge>           # exactly what one experiment changed
git log <merge>^1..<merge>^2         # how it was built
scripts/export-patch.sh <slug>       # a clean patch series against current upstream
```

Add a row when an experiment merges. Update `Result` once it has run on a real
node — including when the answer is "no measurable effect" or "made it worse".
A negative result that is recorded is worth more than one that is quietly
abandoned, because it stops the next person repeating it.

**Table — Experiments merged into bee-experimental `main`, with outcome**

| Issue | Slug | Type | Branch | Merge | Upstream base at merge | Status | Result |
|---|---|---|---|---|---|---|---|
| [#33](https://github.com/crtahlin/bee-experimental/issues/33) | [chequebook-chain-disabled](chequebook-chain-disabled/spec.md) | fix | `fix/33-chequebook-chain-disabled` | [`bac497c5`](https://github.com/crtahlin/bee-experimental/commit/bac497c5) | v2.8.1 | merged | 405 rather than 500 when the chain is disabled on the available-balance path. Not a performance change, so nothing to measure; the test fails against the unfixed handler. Offer upstream. |

Status values: `merged` (in `main`, not yet validated on a node), `validated`
(measured on the bench, effect confirmed), `neutral` (no measurable effect,
kept or reverted — say which), `reverted` (backed out, with the reason),
`superseded` (upstream or a later experiment replaced it).

## Worked example

For the first entry above, each claim in this table is checkable:

```bash
git diff bac497c5^1 bac497c5      # exactly what the experiment changed: 2 files, 60 lines
git log bac497c5^1..bac497c5^2    # how it was built
scripts/export-patch.sh chequebook-chain-disabled
```

The last of those replays the experiment onto **current** upstream master and
writes a patch series to `dist/patches/`. It worked against upstream `a93fb7a7`,
which is already newer than this fork's `v2.8.1` base — which is the point: the
series is reconstructed from the merge, not stored, so it does not go stale.
