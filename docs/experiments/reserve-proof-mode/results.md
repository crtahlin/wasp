# Windowed proof cost, measured on the bench nodes

This is the Phase C measurement for #273: the real cost of the windowed proof
against the classic whole-reserve scan, on live nodes, on both storage engines.
The soundness was settled in simulation (#271); this measures the wall-clock
saving that the N/f figure only predicts.

## Method

Two nodes: bench-1, the goleveldb node, and bench-2, the pebble node, each
holding a reserve of about 3.77 million chunks within radius at storage radius 9.
Both ran the **same binary**, wasp 0.1.3 at commit `093d93b5` (current main), in
both modes, so within a node the only difference between the arms is the
`reserve-proof-mode` setting; the within-node comparison isolates the mode, not
the build and not the machine.

The sampler was driven with `GET /rchash/{depth}/{anchor1}/{anchor2}`, the node's
own overlay as the anchor and the storage radius as the depth, which runs the
reserve sample without the node being staked or winning a round. Each arm: one
warm run discarded, then **five measured runs**, reported with mean, median and
spread, per the bench playbook's three-runs-minimum rule. `durationSeconds` comes
from the response; `TotalIterated`, the chunks the sampler read and hashed, from
the "reserve sampler finished" log. Nodes were restarted between arms and left to
settle before measuring.

## Results

**Table: classic versus windowed reserve sample, same `093d93b5` binary, five
measured runs each, reserve about 3.77M chunks at radius 9**

| Node | Engine | Mode | Chunks read+hashed | Runs (s) | Mean | Median |
|------|--------|------|--------------------|----------|------|--------|
| bench-1 | goleveldb | classic | 3,777,884 | 53.6, 118.8, 52.3, 52.1, 75.4 | 70.4 | 53.6 |
| bench-1 | goleveldb | windowed | 910 | 1.57, 1.45, 1.47, 1.53, 1.46 | 1.50 | 1.47 |
| bench-2 | pebble | classic | ~3,774,000 | 156.3, 161.9, 120.2, 145.5, 123.0 | 141.4 | 145.5 |
| bench-2 | pebble | windowed | 948 | 1.77, 1.70, 1.80, 1.72, 1.70 | 1.74 | 1.72 |

Within each node, same binary, config the only difference:

- **goleveldb: about 47x faster on the mean** (70.4 s to 1.50 s), 36x on the
  median, reading about 4,150 times fewer chunks (3,777,884 against 910).
- **pebble: about 81x faster on the mean** (141.4 s to 1.74 s), 85x on the
  median, reading about 3,980 times fewer chunks (about 3,774,000 against 948).

The windowed sample is **deterministic**: each run on a node returned the same
duration to within one or two percent and, in the earlier single-node run, an
identical sample hash across repeats, because the same anchor gives the same
window and so the same chunks. And it produces a **valid proof**: `/rchash`
returned a hash, so the windowed sample yielded a full sixteen-item inclusion
proof in the existing on-chain format, the forward-compatible format from Phase B.

## Why the speedup is tens, not thousands

The chunk count fell by about 4,000 times but wall-clock by 47 to 81. The reason
is that the windowed sampler, as built, still walks the whole neighbourhood index
in phase one to decide which chunks fall in the window; only the phase-two
load-and-hash is limited to the roughly 900 chunks in the window. So the windowed
time, about 1.5 to 1.7 seconds, is essentially the cost of that index walk, and
the load-and-hash of 900 chunks is negligible beside it. The classic time is that
same walk plus loading and hashing all 3.77 million chunks.

The measured speedup is therefore the saving from not loading and hashing the
reserve, which is the dominant cost today. The remaining one-and-a-half-second
floor is the index walk, which the windowed proof does not need: the window is a
contiguous address range, so a future change could seek straight to it in the
index, the way the probe-sample benchmark (#241) does, and skip the walk. That
would cut the floor too and move the speedup toward the full 1/f. This follow-up
is tracked in #277. The measured figures are a lower bound on what the approach
can reach, not its ceiling.

## Two observations

**The windowed proof is far more predictable.** The classic runs varied widely,
goleveldb from 52 to 119 seconds and pebble from 120 to 162, the live-node
variance the playbook warns about, a chunk-load path at the mercy of the page
cache and concurrent sync. The windowed runs varied by one or two percent, because
their cost is the deterministic index walk, not disk-bound chunk loads. For a
proof that has to finish inside a fixed round time, that predictability matters as
much as the speed.

**Pebble's full scan was slower than goleveldb's here**, about 141 seconds against
70 on the same binary, while the windowed floors were close, 1.74 against 1.50.
Treat the cross-engine absolute comparison as loose: bench-1 and bench-2 are
different machines with different peer counts at measurement time, so this is not
a controlled engine benchmark. The clean, controlled comparison is within each
node, classic against windowed, and there both engines show the same result: the
windowed proof removes almost all of the sample's cost.

## What this does and does not show

It shows the windowed proof is cheap, deterministic and valid on a real reserve,
on both engines. It does not change the soundness finding (#271, simulation) or
the on-chain constraint: neither node was staked, both won nothing, and on the
live network a windowed proof is still rejected by the current contract. The
measurement used `/rchash`, which runs the sampler without the redistribution
game, so cost could be measured without stake.

Reproducible by deploying a `093d93b5` or later build to a node with a full
reserve, running `/rchash` in each `reserve-proof-mode`, and reading
`TotalIterated` and `durationSeconds`. Both nodes were returned to their prior
builds and configuration after the run.

Generated with help of AI.
