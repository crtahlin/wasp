# Results: Pebble vs goleveldb under a real reserve

Issue: [#185](https://github.com/crtahlin/wasp/issues/185) · Spec:
[`spec.md`](spec.md) · Survey: [`survey.md`](survey.md) · Supersedes the
microbenchmark verdict in [#15](https://github.com/crtahlin/wasp/issues/15) once
this completes.

**Status: steady-state comparison done (2026-09-01); the write and
heavy-read axes still need a driven-load run.** bench-2 has filled to a full
radius-9 reserve, matched to bench-1 on the within-radius count, and both nodes
were at rest (pull-sync zero) when measured. The at-rest axes, on-disk
footprint, CPU, memory, and the read path that organic traffic exercises, are
measured below. The write-throughput and retrieval-read axes could not be read
from idle nodes and are named as the remaining step. This document fixed the
method and the verdict criteria *before* the numbers arrived, per the repo's
measure-before-you-conclude rule.

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

### What idle nodes could not show

At rest, `ReserveGet` and `ReservePut` each saw zero operations in the window, and
no reserve sample ran, so none of the three could be measured from organic
traffic. Their lifetime cumulative averages are dominated by each node's history,
bench-2 spent its whole life filling, and are not comparable, so they are
deliberately not reported here. Measuring them needs generated load:

- **Write throughput and `ReservePut` latency**: the axis
  [#15](https://github.com/crtahlin/wasp/issues/15) cared most about (its
  microbenchmark put Pebble 5–8x faster on writes). A fair read needs a matched
  write burst against both engines, or a fresh side-by-side fill race, not the
  historical fill of one node.
- **`ReserveGet` under load and the reserve-sample read path**: the axis
  [#15](https://github.com/crtahlin/wasp/issues/15) flagged as Pebble's weakness
  (reads 1.16–3.47x slower). This needs a driven retrieval workload against
  locally held chunks.

Neither can be read from nodes sitting at pull-sync zero, and driving that load
against bench-1, the stable control holding a real reserve, should be done with
care not to perturb it. A small read-load harness is the next step.

## Verdict so far, against the criteria fixed above

Measured against the pre-registered criteria:

- **disk footprint**: Pebble clearly smaller, about half the index bytes per
  chunk. **Pebble wins.**
- **memory**: comparable. **Tie.**
- **write stall**: neither node stalled; bench-2 adopted radius 9 immediately and
  filled to full without a stall. **No stall on either.**
- **read latency (Has, the path traffic drove)**: goleveldb marginally faster by
  ~20 microseconds, operationally irrelevant. **Marginal goleveldb edge, not
  material.**
- **write throughput and Get-under-load**: **not yet measured**; needs the load
  harness above.

The at-rest picture favours Pebble on the two axes that separate the engines here,
it uses roughly half the index disk and, strikingly, less than half the CPU at
rest, while matching on memory and giving up only an irrelevant sliver of
`ReserveHas` latency. That is a materially stronger position than the
[#15](https://github.com/crtahlin/wasp/issues/15) microbenchmark implied.

It is **not yet a decision.** The pre-registered criteria require Pebble to be at
least as good on writes, and the write path is exactly what idle nodes cannot
show. The honest state is: the static and at-rest axes favour Pebble, clearly on
disk and CPU; the write-throughput and heavy-read axes remain open and are the
gate on adopting or holding to [#15](https://github.com/crtahlin/wasp/issues/15)'s
verdict. The next step is the load harness, not a conclusion.
