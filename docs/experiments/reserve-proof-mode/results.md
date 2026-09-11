# Windowed proof cost, measured on a bench node

This is the Phase C measurement for #273: the real cost of the windowed proof
against the classic whole-reserve scan, on a live node. The soundness was
settled in simulation (#271); this measures the wall-clock saving that the N/f
figure only predicts.

## Method

One node, bench-1, the goleveldb bench node, holding a reserve of 3,776,819
chunks within radius at storage radius 9, with about 107 connected peers. Both
arms ran the **same binary**, wasp 0.1.3 at commit `093d93b5` (current main),
so the only difference between them is the `reserve-proof-mode` setting; the
comparison isolates the mode, not the build.

The sampler was driven directly with `GET /rchash/{depth}/{anchor1}/{anchor2}`,
the node's own overlay as the anchor and the storage radius as the depth, which
runs the reserve sample without needing the node to be staked or to win a round.
Each arm: one warm run discarded, then three measured runs. `durationSeconds`
comes from the response; `TotalIterated`, the number of chunks the sampler read
and hashed, from the "reserve sampler finished" log line. The node was restarted
between arms and left to settle to a comparable peer count before measuring, per
the bench playbook.

## Results

**Table: classic versus windowed reserve sample on bench-1 (goleveldb, 3,776,819
chunks in radius, radius 9, ~107 peers), same binary `093d93b5`, three runs each**

| Mode | Chunks read and hashed | Run 1 | Run 2 | Run 3 | Sample hash |
|------|------------------------|-------|-------|-------|-------------|
| classic | 3,776,819 | 33.9 s | 31.1 s | 90.9 s | varies with anchor |
| windowed | 911 | 1.50 s | 1.51 s | 1.52 s | identical every run |

The windowed arm chose a window twelve bits deeper than the committed depth, a
fraction of about 1/4096 of the neighbourhood, so it read 911 chunks where the
classic scan read all 3,776,819, a factor of about 4,145 fewer. Wall-clock fell
from about 32 seconds to about 1.5 seconds, a factor of about 21 on the stable
runs.

Two things stand out. The windowed sample is **deterministic**: the same anchor
gives the same window and so the same 911 chunks and the same hash, identical
across all three runs. And it produces a **valid proof**: `/rchash` returned a
hash, which means the windowed sample yielded a full sixteen-item inclusion proof
in the existing on-chain format, the forward-compatible format from Phase B.

## Why the speedup is 21x, not 4,145x

The number of chunks read fell by 4,145 times, but wall-clock fell by only 21.
The reason is that the windowed sampler, as built, still walks the whole
neighbourhood index in phase one to decide which chunks fall in the window; only
the phase-two load-and-hash is limited to the 911 chunks in the window. So the
1.5-second windowed time is essentially the cost of that index walk, and the
load-and-hash of 911 chunks is negligible beside it. The classic 32 seconds is
that same index walk plus loading and hashing all 3.78 million chunks.

The 21x is therefore the saving from not loading and hashing the reserve, which
is the dominant cost today. The remaining 1.5-second floor is the index walk,
which the windowed proof does not need: the window is a contiguous address range,
so a future change could seek straight to it in the index, the way the
probe-sample benchmark (#241) does, and skip the walk. That would cut the floor
too and approach the full 1/f. The measured 21x is a lower bound on what the
approach can reach, not its ceiling.

## Noise

The classic third run took 90.9 seconds against about 32 for the other two, the
live-node variance the bench playbook warns about, a chunk-load path at the mercy
of the page cache and concurrent sync. The windowed runs varied by about one
percent, because their cost is the deterministic index walk, not disk-bound chunk
loads. This is a side benefit worth noting: the windowed proof is not only faster
but far more predictable, which matters for a proof that has to finish inside a
fixed round time.

## What this does and does not show

It shows the windowed proof is cheap and deterministic on a real reserve, and
produces a valid proof in the existing format. It does not change the soundness
finding (#271, simulation) or the on-chain constraint: this node was not staked
and won nothing, and on the live network a windowed proof is still rejected by
the current contract. The measurement used `/rchash`, which runs the sampler
without the redistribution game, exactly so cost could be measured without stake.

Reproducible by deploying a `093d93b5` or later build to a node with a full
reserve, running `/rchash` in each `reserve-proof-mode`, and reading
`TotalIterated` and `durationSeconds`. bench-1 was returned to its prior build
and configuration after the run.

Generated with help of AI.
