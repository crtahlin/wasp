# Results: Pebble vs goleveldb under a real reserve

Issue: [#185](https://github.com/crtahlin/wasp/issues/185) · Spec:
[`spec.md`](spec.md) · Survey: [`survey.md`](survey.md) · Supersedes the
microbenchmark verdict in [#15](https://github.com/crtahlin/wasp/issues/15) once
this completes.

**Status: steady-state and reserve-sample comparison done (2026-09-01), including
a tuning retest; write throughput is the one axis still open.** bench-2 filled to
a full radius-9 reserve, matched to bench-1 on the within-radius count. The at-rest
axes (disk, CPU, memory) and the heaviest read a storer performs (the
redistribution reserve sample, measured with `rchash`) are recorded below. The
headline reversed on retest: at Pebble's default tuning the sample ran 4.6x slower
than goleveldb, but that was goleveldb's compaction habits surfacing through
Pebble's default level-0 trigger. Lowering `db-compaction-l0-trigger` to 4 takes
the sample from 245 s to 44 s, faster than goleveldb's 52 s, at lower memory and no
extra disk. Tuned this way Pebble matches or beats goleveldb on every axis measured
so far except an immaterial `ReserveHas` sliver; only write throughput remains
unmeasured. This document fixed the method and the verdict criteria *before* the
numbers arrived, per the repo's measure-before-you-conclude rule.

## The two nodes

Two VMs on one Proxmox host, so hardware and network are shared and matched.

| | bench-1 (control) | bench-2 (treatment) |
|---|---|---|
| engine | goleveldb | **Pebble** (`--storage-engine pebble`) |
| CPU | AMD Ryzen 7 5700G, 8 vCPU | same |
| RAM | 16 GB | 16 GB |
| disk | larger | ~50 GB (bounds the run to a default-capacity reserve) |
| config | mainnet full node, `swap-enable: false`, 64 MB block cache / write buffer | identical except engine and p2p port |
| reserve | full, ~4.09M chunks, radius 9 | filling from zero |

Because the disk is ~50 GB, the comparison is at **default reserve capacity**,
which bench-1 also runs, so it is matched. It does not exercise the
very-large-reserve regime where the index outgrows RAM; that is noted, not
hidden.

## What is measured, and how

Captured from both nodes at once by `ab-snapshot.sh` (in the operator's private
infra directory; it reads `/status`, `/topology`, `/metrics`, process stats, and
on-disk sizes). The deciding axes:

- **Fill / write throughput**: `bee_puller_pullsync_rate` (chunks/s) and
  time-to-full. The headline for the write path: does Pebble ingest a reserve at
  least as fast as goleveldb?
- **Write latency**: `ReservePut` average from
  `bee_localstore_method_calls_duration`.
- **Read latency**: `ReserveGet` / `ReserveHas` average, and the read-heavy
  **reserve sample** duration (`bee_localstore_reserve_sample_duration_seconds`),
  which is the retrieval path the redistribution game exercises.
- **Write-stall behaviour**: whether either engine stalls under sync. See the
  caveat below on comparing this across engines.
- **On-disk size**: the indexstore size for the *same* reserve, goleveldb vs
  Pebble (sharky is engine-independent and should match).
- **Resource use**: bee process RSS and CPU.

## Caveats that decide whether a number means anything

1. **Compare only at matched reserve state.** Read latency, CPU, sample time and
   RSS are only comparable once *both* nodes hold a full, radius-9 reserve. While
   bench-2 fills, its small reserve makes its reads look artificially fast (they
   fit in cache) and its CPU artificially low. Early snapshots are fill-progress,
   not verdicts.
2. **Raw level-0 file counts are not comparable across engines.** goleveldb gates
   writes on the level-0 *file* count; Pebble gates on the level-0 *sublevel*
   count and organises L0 into sublevels, so its raw file count runs higher for
   the same pressure and is not a stall by itself. A Pebble `level_files{0}` of
   40+ during heavy sync is normal, not a stalled node. Judge stalls by each
   engine's own gate and by whether writes actually stop, not by comparing the
   two file counts.
3. **Three runs, report the spread** (rule 7) for any latency figure, taken with
   matched peer count and a fixed interval after any restart.
4. **Fast disk hides the extreme case.** At default reserve the index (a few GB)
   fits in 16 GB RAM, so the index-read-from-disk axis is lightly exercised. The
   write/compaction path under sync and the 17 GB sharky read path (which exceeds
   RAM) are exercised fully.

## The verdict criteria, fixed in advance

Adopt Pebble only if, under a real reserve, it is **at least as good as goleveldb
on writes and reads and does not stall, at comparable or smaller disk footprint**.
Concretely:

- fills the reserve at least as fast (write/sync throughput within noise or
  better), **and**
- serves reads, `ReserveGet` and the reserve sample, at least as fast at a full
  matched reserve, **and**
- shows no write stall that goleveldb avoids, **and**
- uses comparable or less disk for the same reserve, and comparable memory.

If Pebble is not clearly better, the [#15](https://github.com/crtahlin/wasp/issues/15)
"do not adopt" verdict stands with real-node evidence behind it, and the effort
moves to the goleveldb-contention work
([#23](https://github.com/crtahlin/wasp/issues/23),
[#28](https://github.com/crtahlin/wasp/issues/28),
[#29](https://github.com/crtahlin/wasp/issues/29)), the survey's conclusion that
the pure-Go field has no clearly better option.

## Matched state at measurement

Both nodes at radius 9, pull-sync zero, sampled three times three minutes apart
on 2026-09-01. The comparison uses `reserveSizeWithinRadius`, not the raw
`reserveSize`: a node also caches chunks outside its radius, so the raw figure is
not the matched quantity. On the within-radius count the two nodes agree to
within about three percent, which is ordinary non-uniformity between two
different radius-9 neighbourhoods plus batch churn, not one node being unfilled.

**Table: matched reserve state, bench-1 (goleveldb) vs bench-2 (Pebble), 2026-09-01**

| | bench-1 goleveldb | bench-2 Pebble |
|---|---|---|
| storage radius | 9 | 9 |
| reserve within radius | 3,674,563 | 3,791,570 |
| total reserve (incl. cache) | 4,090,799 | 3,791,662 |
| pull-sync rate | 0 /s | 0 /s |
| connected peers | 121 | 128 |

## Results

Three samples each, three minutes apart. Latency figures are **windowed**: the
average over the sampling window, computed from the change in the metric's
cumulative sum and count between the first and last sample, so a node's fill
history does not bias its at-rest read. Disk, CPU and memory are read directly.

### On-disk footprint: the clean, decisive axis

**Table: index and chunk storage on disk for a full radius-9 reserve, 2026-09-01**

| store | bench-1 goleveldb | bench-2 Pebble |
|---|---|---|
| indexstore | 3.49 GB | 1.91 GB |
| indexstore, per within-radius chunk | 951 bytes | 504 bytes |
| sharky (chunk bytes) | 17.66 GB | 15.96 GB |

The index is the engine-selectable part; sharky is the same fixed-size blob store
on both and scales with the total reserve, so its 17.66 vs 15.96 GB simply tracks
the 4.09M vs 3.79M total chunk counts. Normalised per chunk, **Pebble's index is
about half the size of goleveldb's** (504 vs 951 bytes per within-radius chunk).
This is the headline result and it is not sensitive to the small reserve-count
difference.

### Resource use at steady state

**Table: bee process CPU and memory at rest, three samples, 2026-09-01**

| metric | bench-1 goleveldb | bench-2 Pebble |
|---|---|---|
| CPU (3 samples) | 279%, 279%, 279% | 127%, 124%, 122% |
| RSS (3 samples) | 504, 507, 505 MB | 475, 475, 475 MB |

Memory is comparable, Pebble slightly lower. CPU is the surprise: **goleveldb
holds a stable 279% (about 2.8 cores) while completely at rest**, against Pebble's
~124%. A node with pull-sync at zero is doing no sync work, so this is background
store activity, consistent with goleveldb compacting continuously under a
multi-million-chunk index, which is the pressure this whole investigation started
from ([#176](https://github.com/crtahlin/wasp/issues/176)). bench-1 does hold
about 8% more total chunks, which cannot account for 2.3x the CPU; the difference
is the engine. This warrants its own focused confirmation before it is leaned on.

### Read latency the traffic exercised

**Table: windowed read latency at full reserve, 2026-09-01**

| operation | bench-1 goleveldb | bench-2 Pebble | window sample size |
|---|---|---|---|
| ReserveHas | 0.024 ms | 0.044 ms | ~600 ops each |

goleveldb is about 1.9x faster on `ReserveHas`, but both are far below a tenth of
a millisecond, so the absolute difference is roughly 20 microseconds per call,
operationally irrelevant. This is the one read path organic protocol traffic
drove during the window.

### Reserve-sample read, the redistribution path (rchash)

The `/rchash` endpoint computes the reserve commitment sample the redistribution
game runs once per round to prove reserve. It iterates the whole within-radius
reserve, computes a transformed address for the sampled chunks, and returns the
sample hash with its inclusion proofs. It is the heaviest read a storer performs,
and it is the read path [#15](https://github.com/crtahlin/wasp/issues/15) flagged
as Pebble's weakness. It needs no injected data, so it drives that read directly
without perturbing either reserve. The two VMs share a host, so runs were taken
strictly one node at a time, three each, alternating, with a 60-second settle gap.
Each run used depth 9 and the node's own overlay as the anchor, so it sampled that
node's entire reserve. Every run returned a valid sample, and each node's hash was
identical across its three runs, so the timings compare the same deterministic
work.

**Table: rchash reserve-sample duration at a full radius-9 reserve, 2026-09-01**

| | bench-1 goleveldb | bench-2 Pebble |
|---|---|---|
| run 1 / 2 / 3 (s) | 79.0 / 29.8 / 51.8 | 257.3 / 239.3 / 245.6 |
| median | 51.8 s | 245.6 s |
| min, max | 29.8, 79.0 s | 239.3, 257.3 s |

**goleveldb computes the sample about 4.6x faster** on the median (51.8 vs 245.6
seconds). goleveldb's spread is wide, its fastest run a warm page cache and its
slowest a colder one; Pebble is tight at about 245 seconds regardless, so its cost
is structural rather than a cache miss. The shared host penalised Pebble, not
goleveldb: Pebble sampled while goleveldb was spending its ~2.8 idle cores
compacting, so the true gap on a quiet host is if anything smaller, but not by the
factor that separates them.

This looks like a tuning problem, not an inherent Pebble limit, and the metrics
point at a cause. At steady state, with **zero writes**, Pebble was sitting at
**19 level-0 files with 382 MB of uncompacted debt**, while goleveldb held level 0
at 5. Level-0 files overlap, so an iterator merges across all of them on every
step; a reserve scan across 19 overlapping L0 files plus the lower levels does far
more work per chunk than the same scan on a well-compacted tree. And the block
cache is 64 MB against a 1.9 GB index, so almost nothing stays hot between the
per-round samples, which is exactly the warm-cache advantage goleveldb's fast run
shows. Both levers are already exposed configuration
([rule 8](../../../AGENTS.md)): raising `db-block-cache-capacity` well above 64 MB,
and keeping level 0 shallow at rest (a lower `db-compaction-l0-trigger`, or a
periodic compaction so a settled reserve does not sit at 19 L0 files). This was a
hypothesis, so it was tested, below.

### Tuning retest: the gap was the tuning, and it is level 0

The two levers were applied to bench-2 and the sample re-run, then the cache
lever was reverted on its own to see which one mattered. Each change was followed
by a restart, a wait for level 0 and warm-up to settle, and three fresh rchash
runs at the same depth and anchor under the same shared-host conditions. The
reserve was unchanged throughout (about 3.79M within radius), and every run
returned the same sample hash, so all three configurations did the same work.

**Table: rchash reserve-sample duration and memory across Pebble tunings, 2026-09-01**

| Pebble configuration | level 0 files | median duration | bee RSS |
|---|---|---|---|
| default: 64 MB cache, `db-compaction-l0-trigger` 8 | 19 | 245.6 s | ~475 MB |
| 1 GiB cache, `db-compaction-l0-trigger` 4 | 0 | 49.8 s | ~2.8 GB |
| 64 MB cache, `db-compaction-l0-trigger` 4 | 0 | 43.8 s | ~364 MB |
| goleveldb (bench-1), for reference | 5 | 51.8 s | ~505 MB |

Three things fall out of this cleanly:

- **Level 0 was the whole fix.** Lowering the L0 compaction trigger and restarting
  took level 0 from 19 files to 0, and that alone took the sample from 245.6 s to
  the mid-40s. The block cache did not matter for this read: at 64 MB the sample is
  43.8 s, at 1 GiB it is 49.8 s, the same within noise. A reserve scan reads each
  block once, so a bigger cache has almost nothing to reuse; what it was fighting
  was the merge across 19 overlapping L0 files, and once those are gone the cache
  is beside the point.
- **The fix is memory-cheap, and then some.** With the cache left at its 64 MB
  default, bee's resident memory is about 364 MB, *below* goleveldb's ~505 MB. The
  1 GiB cache only added ~2.4 GB of resident memory for no read benefit, so it
  should not be used for this.
- **Tuned this way, Pebble beats goleveldb on the sample.** 43.8 s against 51.8 s
  on the median, and far steadier: Pebble's three runs span 43.6 to 50.8 s where
  goleveldb's span 29.8 to 79.0 s with page-cache warmth.

The effective configuration is a single knob, `db-compaction-l0-trigger: 4`, at
the default cache. bench-2 is left on it. One caveat is carried forward honestly:
the 19 L0 files were fill leftovers that the restart cleared, and the default
trigger of 8 had plainly not kept them down during the fill. The lower trigger
should keep level 0 shallow as new files form, but that it holds through
steady-state operation, rather than only right after a flush, is stated on the
trigger's semantics, not yet shown over time. A longer level-0 watch is the small
remaining check on this axis.

### What still needs a driven-load run

The reserve-sample read is measured above. Two axes remain open at steady state:

- **Write throughput and `ReservePut` latency**: the axis
  [#15](https://github.com/crtahlin/wasp/issues/15) cared most about (its
  microbenchmark put Pebble 5–8x faster on writes). A fair read needs a matched
  write burst against both engines, or a fresh side-by-side fill race, not the
  historical fill of one node.
- **`ReserveGet` under ordinary retrieval load**: organic traffic drove no
  `ReserveGet` in the window, so per-call retrieval latency at a full reserve is
  still open, distinct from the bulk sample read above.

Both need generated load, driven with care against bench-1, the stable control
holding a real reserve.

## Verdict, against the criteria fixed above

Measured against the pre-registered criteria, with Pebble tuned to hold level 0
shallow (`db-compaction-l0-trigger: 4`, default cache):

- **disk footprint**: Pebble about half the index bytes per chunk. **Pebble wins.**
- **idle CPU**: goleveldb burns about 2.8 cores compacting at rest; Pebble about
  half. **Pebble wins.**
- **memory**: with the effective tuning (64 MB cache) Pebble's resident memory is
  ~364 MB against goleveldb's ~505 MB. **Pebble wins, as long as the block cache
  is left modest.**
- **write stall**: neither node stalled; bench-2 filled to full without a stall.
  **No stall on either.**
- **light read (`ReserveHas`)**: goleveldb faster by about 20 microseconds,
  immaterial. **Marginal goleveldb edge, not material.**
- **heavy read (reserve sample)**: Pebble 43.8 s against goleveldb 51.8 s once
  level 0 is kept shallow, and steadier. **Pebble wins.** A 4.6x goleveldb win
  appears only at Pebble's default L0 tuning, which the retest showed to be a fixable
  artefact, not the engine's floor.
- **write throughput**: **still not measured**;
  [#15](https://github.com/crtahlin/wasp/issues/15)'s microbenchmark favoured
  Pebble 5–8x.

This reverses the pre-tuning reading. With one configuration change that costs
nothing and no extra memory, Pebble matches or beats goleveldb on every axis
measured so far except an immaterial `ReserveHas` sliver: half the index disk,
half the idle CPU, lower resident memory, no stall, and a faster and steadier
redistribution sample. The [#15](https://github.com/crtahlin/wasp/issues/15)
microbenchmark verdict was formed before any of this could be tuned against a real
reserve, and it no longer describes the node in front of us: the reserve-sample
slowness it warned about was goleveldb's compaction habits hiding in Pebble's
default L0 trigger, not an inherent read penalty.

It is still short of a final adopt decision, for two concrete and now-narrow
reasons, both pointing toward Pebble rather than away:

- **Write throughput is unmeasured.** It is the one axis
  [#15](https://github.com/crtahlin/wasp/issues/15) put firmly in Pebble's favour,
  so measuring it is more likely to strengthen the case than weaken it, but it
  should be measured, not assumed. This is the write-load harness named earlier.
- **The level-0 fix must be shown to hold over time.** The 19 files were cleared by
  a restart; the lower trigger should keep them down as new files form, but a
  longer steady-state watch is needed before relying on it in production.

The idle-CPU finding also stands on its own regardless of the engine choice:
goleveldb burning about 2.8 cores to compact a settled reserve at rest is the
pressure ([#176](https://github.com/crtahlin/wasp/issues/176)) that began this
work, and it is worth fixing on the goleveldb path too
([#23](https://github.com/crtahlin/wasp/issues/23),
[#28](https://github.com/crtahlin/wasp/issues/28),
[#29](https://github.com/crtahlin/wasp/issues/29)).
