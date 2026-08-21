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
| — | — | — | — | — | — | — | *No experiments merged yet. `main` is an unmodified upstream v2.8.1.* |

Status values: `merged` (in `main`, not yet validated on a node), `validated`
(measured on the bench, effect confirmed), `neutral` (no measurable effect,
kept or reverted — say which), `reverted` (backed out, with the reason),
`superseded` (upstream or a later experiment replaced it).
