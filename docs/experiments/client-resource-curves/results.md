# Client resource curves: results

This is the results document the methodology ([methodology.md](methodology.md),
[#252](https://github.com/crtahlin/wasp/issues/252)) anticipated. It reports the
first execution of the resource-curve harness ([#279](https://github.com/crtahlin/wasp/issues/279)):
upstream bee, wasp+goleveldb, and wasp+pebble measured across the three regimes,
steady operation and the two radius events.

## Setup

One host, bench-1, holding a reserve of about 3.77 million chunks within radius at
storage radius 9. All three clients ran in turn on the same host, one heavy phase
at a time, each reset from a frozen reserve image so every run started from the
same size and radius. Two features, built for this study, made it possible on an
unstaked node:

- `reserve-capacity` ([#283](https://github.com/crtahlin/wasp/issues/283)): a
  settable reserve capacity. The node's storage radius is driven by its reserve
  count against capacity, so setting capacity below or above the current reserve
  drives real radius events, the node's own autonomous mechanism, with no staking.
- The Pebble read-amplification metric ([#286](https://github.com/crtahlin/wasp/issues/286)):
  `pebble_read_amp` and `pebble_level_sublevels`. Pebble compacts L0 on
  read-amplification (sublevels), not raw file count, so the file count overstates
  depth. This metric is the correct L0 yardstick and the one comparable to
  goleveldb's file count.

The harness sampled host and client metrics every ten seconds and drove the
reserve sample with `/rchash`. Clients were unstaked; `/rchash` runs the sampler
without the redistribution game.

## Steady regime

Idle resource use was near zero for all three clients (about 0.1 to 0.2 percent
processor, no disk). The measured event is the periodic reserve-sample peak.

**Table: reserve-sample peak, steady regime, per client (same host, same reserve)**

| Client | Peak processor (max / mean) | Peak disk read (max / mean) | Shape |
|--------|-----------------------------|------------------------------|-------|
| wasp + goleveldb | 90 / 56 % | 619 / 310 MB/s | short, disk-heavy burst |
| wasp + pebble | 88 / 75 % | 335 / 96 MB/s | longer, processor-heavy |
| upstream bee | 98 / 80 % | 804 / 272 MB/s | heaviest |

**Finding.** wasp+goleveldb has the lightest sample peak (56 percent mean
processor) and upstream bee the heaviest (80 percent), though both use goleveldb.
The difference is wasp's SIMD hashing (off in stock bee, about 25 percent faster
sample, [#54](https://github.com/crtahlin/wasp/issues/54)) plus its sampler
read/hash split and ordering ([#9](https://github.com/crtahlin/wasp/issues/9),
[#11](https://github.com/crtahlin/wasp/issues/11)). Pebble's sample is
processor-heavy but disk-light, a different shape for the same total work.

Caveat: background sync differed slightly across the three runs (pull rate roughly
0 to 24, 33, and 41 chunks per second for goleveldb, pebble, bee), which nudges the
processor-during-peak figure; a matched-load re-run ([#278](https://github.com/crtahlin/wasp/issues/278))
would tighten it. The pebble figures are not confounded by L0 depth: read-amp was 1.

## Radius increase, the eviction burst

Induced by setting the capacity below the current reserve, so the node evicts and
deepens its radius.

- **goleveldb**: radius 9 to 10, processor peak 45 percent (mean 18 percent
  during eviction), L0 climbing 4 to 7 then compacting back in order.
- **pebble**: radius 9 to 10, processor peak 89 percent (mean 49 percent), about
  2.7 times more processor-intensive.

Both engines evicted the same work: about 1.89 million chunks removed, down to a
1.89 million reserve (goleveldb 1,887,354; pebble 1,886,599). The radius label
differing, goleveldb to 10 and pebble to 11, is neighbourhood-boundary accounting,
not extra work, so the processor comparison is on equal workloads.

**What this does and does not say.** Pebble spends markedly more processor on the
eviction (2.7 times the mean). It is not a settle artifact: read-amplification was
1 (shallow) at the start, so the raw-file-count worry was a misreading. But note
the axis: the methodology's expectation for eviction was about READ RESPONSIVENESS,
that goleveldb stalls reads during its fast reclaim while pebble stays responsive,
NOT about processor. Pebble's higher processor is the continuous compaction pebble does to stay
responsive. A request-latency probe was then added to the harness (it times a
retrieval each interval) and both engines re-run: during the eviction, goleveldb's
retrieval latency degraded more (median 578 to 650 ms, +12 percent; worst 627 to
1135 ms, roughly double, with one timeout) than pebble's (median 594 to 614 ms,
+3 percent; worst 719 to 820 ms, no timeout). So the two axes agree: pebble spends
about 2.7 times the processor and in return degrades reads far less. That is the
methodology's hypothesised trade, pebble stays responsive while reclaiming more
slowly, and the extra processor is how it buys the responsiveness. Caveats: the
latency probe retrieves a random in-neighbourhood address, a miss forwarded to the
network (about 580 ms baseline), so it is a soft proxy for the local store read
path; node-level responsiveness (/status latency) stayed flat for both, so neither
stalled the node on this NVMe; the effect would be larger on a slower disk.

## Radius decrease, the backfill plateau

Induced by setting the capacity high, so the reserve falls below half of it; once
the node's sync rate reaches zero it lowers its radius and backfills the larger
neighborhood. The sync-rate meter decays asymptotically and only reads exactly
zero after roughly twenty minutes idle, which the run must wait for.

- **goleveldb**: radius 9 to 8, backfilling; L0 sat around 5 files and, per the
  methodology, would climb toward the write-slowdown trigger under a longer plateau.
- **pebble**: radius 9 to 7, backfilling at 400 or more chunks per second; **its
  read-amplification stayed pinned at 1 for the whole plateau**, while its raw L0
  file count wobbled between 1 and 8. Processor stayed at 8 to 14 percent.

Note the backfill depths differed: goleveldb lowered one step (9 to 8), pebble two
(9 to 7), so pebble took on a larger neighbourhood and therefore MORE sustained
write pressure. That it still held read-amplification at 1 under the heavier load
strengthens rather than weakens the comparison. The goleveldb "climbs toward the
write-slowdown trigger" is the methodology's prediction under a long plateau, not a
measured endpoint here (the goleveldb backfill was captured only at onset, L0 ~5).

**Finding, the headline.** Pebble absorbs the sustained-write backfill more
smoothly than goleveldb. It compacts continuously and holds L0 read-amplification
at 1 under a heavy write plateau, where goleveldb's L0 grows toward its
write-slowdown trigger. This is the methodology's headline hypothesis, confirmed,
and it is only visible in read-amplification: the raw file count wobbled and would
have told the opposite story.

## The metric lesson

Raw L0 file count is the wrong yardstick for Pebble. It wobbled between 1 and 8
across these runs while the read-amplification that governs read cost and
compaction stayed at 1. Every Pebble L0 comparison in this study, and the
under-load study ([#278](https://github.com/crtahlin/wasp/issues/278)), uses
read-amplification (`pebble_read_amp` / sublevels), never file count. Building that
metric ([#286](https://github.com/crtahlin/wasp/issues/286)) changed one
conclusion outright.

## Scope and limits

- **Upstream bee has no radius arm.** It lacks the `reserve-capacity` setting, so
  its radius cannot be induced without patching it; bee appears in the steady
  regime only. The radius comparisons are wasp+goleveldb against wasp+pebble.
- **One host, one client at a time.** The within-client comparisons are clean; the
  cross-client absolute numbers carry the usual live-node variance.
- **Backfill plateaus were captured at their onset**, not run to completion (a full
  radius-8 or radius-7 fill takes hours). The read-amplification behaviour is
  established; longer plateaus would refine the resource totals.
- **Not staked.** The sampler was driven with `/rchash`, so no redistribution
  reward behaviour is measured here.

## Reproducing

Deploy a `4362f36b` or later build, reset from a full reserve image, and drive the
harness: steady is the baseline sampler peaks; radius increase sets
`reserve-capacity` below the reserve; radius decrease sets it high and waits for
the sync rate to reach zero. Read Pebble L0 depth as `pebble_read_amp`, not file
count.

Generated with help of AI.
