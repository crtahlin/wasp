# Windowed proof cost, measured on the bench nodes

This is the Phase C measurement for #273: the real cost of the windowed proof
against the classic whole-reserve scan, on live nodes, on both storage engines.
The soundness was settled in simulation (#271); this measures the wall-clock
saving that the N/f figure only predicts.

## Method

Two nodes: bench-1, the goleveldb node, and bench-2, the pebble node, each
holding a reserve of about 3.77 million chunks within radius at storage radius 9.
Both ran the **same binary**, wasp 0.1.3 at commit `093d93b5`, in both modes, so
within a node the only difference between the arms is the `reserve-proof-mode`
setting; the within-node comparison isolates the mode.

The sampler was driven with `GET /rchash/{depth}/{anchor1}/{anchor2}`, the node's
own overlay as the anchor, which runs the reserve sample without the node being
staked. Each arm: one warm run discarded, then five measured runs, reported with
mean and median.

**The settle gate matters, and it is engine-specific.** Pebble's reserve-sample
cost is dominated by its per-chunk index lookup, which is sensitive to how deep
Pebble's level 0 is (see the note on level 0 and compaction below). A node
measured too soon after a restart, while it is still catching up sync and its
level 0 is deep, reports a sample several times slower than its settled cost. A
first pass at this measurement made exactly that mistake and reported Pebble at
141 s, about three times its real cost. This measurement gates the Pebble runs
on the store being genuinely settled: `pullsyncRate` at 0 for a sustained period
and the level-0 file count (the `pebble_level_files{level="0"}` metric) confirmed
at 0 before the runs, not just a recovered peer count.

## Results

**Table: classic versus windowed reserve sample, same `093d93b5` binary, five
measured runs each, reserve about 3.77M chunks at radius 9, stores confirmed
settled**

| Node | Engine | Mode | Chunks read+hashed | Runs (s) | Mean | Median |
|------|--------|------|--------------------|----------|------|--------|
| bench-1 | goleveldb | classic | 3,777,884 | 53.6, 118.8, 52.3, 52.1, 75.4 | 70.4 | 53.6 |
| bench-1 | goleveldb | windowed | 910 | 1.57, 1.45, 1.47, 1.53, 1.46 | 1.50 | 1.47 |
| bench-2 | pebble | classic | ~3,774,000 | 33.4, 33.1, 31.3, 34.2, 87.3 | 43.9 | 33.4 |
| bench-2 | pebble | windowed | 949 | 1.60, 1.58, 1.58, 1.61, 1.58 | 1.59 | 1.58 |

Within each node, config the only difference:

- **goleveldb: about 36x faster on the median** (53.6 s to 1.47 s), 47x on the
  mean, reading about 4,150 times fewer chunks (3,777,884 against 910).
- **pebble: about 21x faster on the median** (33.4 s to 1.58 s), 28x on the mean,
  reading about 3,980 times fewer chunks (about 3,774,000 against 949).

The windowed sample is **deterministic and steady**: runs varied about one
percent, and the same anchor gives the same window and so the same chunks and the
same sample hash. And it produces a **valid proof**: `/rchash` returned a hash, so
the windowed sample yielded a full sixteen-item inclusion proof in the existing
on-chain format, the forward-compatible format from Phase B.

## Pebble is faster than goleveldb, and that lowers its multiple

The settled Pebble full scan, 33 s median, is **faster than goleveldb's** 54 s,
matching the storage-engine-eval result that Pebble at its shallow level-0 setting
reads the reserve sample faster than goleveldb. That is why Pebble's windowed
speedup, 21x, is *smaller* than goleveldb's 36x: the windowed proof lands at about
the same place on both engines, around 1.5 seconds, so the faster the classic
scan it is compared against, the smaller the multiple. Both engines end at the
same useful result, the windowed proof removes almost all of the sample's cost;
Pebble simply had less cost to remove.

(The earlier draft of this note reported Pebble at 141 s and a bogus 81x, from
measuring before the store settled. The number here, gated on a shallow level 0,
is the real one.)

## Why the speedup is tens, not thousands

The chunk count fell by about 4,000 times but wall-clock by 21 to 47. The reason
is that the windowed sampler, as built, still walks the whole neighbourhood index
in phase one to decide which chunks fall in the window; only the phase-two
load-and-hash is limited to the roughly 900 chunks in the window. So the windowed
time, about 1.5 seconds, is essentially the cost of that index walk, and the
load-and-hash of 900 chunks is negligible beside it. The classic time is that same
walk plus loading and hashing all 3.77 million chunks.

The measured speedup is therefore the saving from not loading and hashing the
reserve. The remaining one-and-a-half-second floor is the index walk, which the
windowed proof does not need: the window is a contiguous address range, so a
future change could seek straight to it in the index, the way the probe-sample
benchmark (#241) does, and skip the walk. That follow-up is tracked in #277. The
measured figures are a lower bound on what the approach can reach.

## Note: level 0 and compaction, briefly

Pebble and goleveldb are log-structured stores. New writes are flushed to disk as
small sorted files at level 0, and level-0 files can overlap in key range, so a
read may have to check several of them and merge the results. Compaction is the
background job that merges those overlapping files and pushes them down into
deeper, non-overlapping levels where a read checks one file. A shallow level 0
(few files) means reads are fast; a deep level 0 (a write burst outran compaction)
means reads are slow. The reserve sample does millions of index lookups, so it is
acutely sensitive to level-0 depth: right after a restart, sync catch-up piles up
level 0 and the sample crawls until compaction drains it. That is the state the
first Pebble measurement was taken in, and the settle gate above is what avoids it.

## What this does and does not show

It shows the windowed proof is cheap, deterministic and valid on a real reserve on
both engines. It does not change the soundness finding (#271, simulation) or the
on-chain constraint: neither node was staked, both won nothing, and on the live
network a windowed proof is still rejected by the current contract. The
measurement used `/rchash`, which runs the sampler without the redistribution game.

Reproducible by deploying a `093d93b5` or later build to a node with a full
reserve, confirming the store is settled (sync idle, level 0 shallow), running
`/rchash` in each `reserve-proof-mode`, and reading `TotalIterated` and
`durationSeconds`. Both nodes were returned to their prior builds and
configuration after the run.

Generated with help of AI.
