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

**Table — Experiments merged into wasp `main`, with outcome**

| Issue | Slug | Type | Branch | Merge | Upstream base at merge | Status | Result |
|---|---|---|---|---|---|---|---|
| [#33](https://github.com/crtahlin/wasp/issues/33) | [chequebook-chain-disabled](chequebook-chain-disabled/spec.md) | fix | `fix/33-chequebook-chain-disabled` | [`bac497c5`](https://github.com/crtahlin/wasp/commit/bac497c5) | v2.8.1 | merged | 405 rather than 500 when the chain is disabled on the available-balance path. Not a performance change, so nothing to measure; the test fails against the unfixed handler. Offer upstream. |
| [#69](https://github.com/crtahlin/wasp/issues/69) | green-tea-gc | fix | `fix/69-disable-greenteagc` | [`9702319c`](https://github.com/crtahlin/wasp/commit/9702319c) | v2.8.1 | validated | Go 1.26's default Green Tea collector corrupts the heap under wasp's allocation profile. `GOEXPERIMENT=nogreenteagc` is set in the Makefile with `:=`, so an inherited environment variable cannot silently defeat it, and CI asserts the setting is recorded in the binary. Note it is a **build-time** variable: setting it in a systemd unit does nothing, which invalidated an entire earlier measurement matrix. Affects upstream. |
| [#8](https://github.com/crtahlin/wasp/issues/8) | [sharky-concurrent-reads](sharky-concurrent-reads/spec.md) | optimization | `exp/8-sharky-concurrent-reads` | [`a583a345`](https://github.com/crtahlin/wasp/commit/a583a345), reverted by [`5392fed0`](https://github.com/crtahlin/wasp/commit/5392fed0) | v2.8.1 | reverted | In the harness, removing the per-shard read actor took Sharky from flat at ~400,000 ops/s to ~3,940,000 at concurrency 32. It was then reverted on the belief that it crashed a live node. **That was wrong** — the crashes were SIMD memory corruption (#92), and the revert was made on false evidence. Re-measured end to end after the SIMD fix: **no measurable node-level change** (1.2% faster on one comparison, 1.4% slower on another, both inside the noise). Not reinstated, because a 10x harness gain that moves nothing on a node is not yet worth the risk. #8 stays open pending #9, which the spec argues is the half that would make the ceiling reachable. |
| [#92](https://github.com/crtahlin/wasp/issues/92) | simd-scratch-stack | fix | `fix/92-scratch-stack` | [`8fe665dd`](https://github.com/crtahlin/wasp/commit/8fe665dd) | v2.8.1 | validated | The XKCP SIMD blob ran on the goroutine stack, which Go may move or grow and the collector scans. It corrupted memory and killed nodes within ~12 minutes under load. Established by A/B/A on a live node: 12 min to crash, 117 min clean, 12 min to crash. The stub now runs the blob on a pooled scratch stack, in pure Go with `CGO_ENABLED=0`. Result: **7.6 hours clean** (457 minutes, 113 peers, 305 `rchash` runs). `rchash` fast-cluster median 92.0s with SIMD off against 69.0s with it on and fixed, over 243 runs — **1.33x**, not the 3.16x the microbenchmark had suggested. Affects upstream. |

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
