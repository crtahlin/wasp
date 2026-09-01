# Results — Pebble vs goleveldb under a real reserve

Issue: [#185](https://github.com/crtahlin/wasp/issues/185) · Spec:
[`spec.md`](spec.md) · Survey: [`survey.md`](survey.md) · Supersedes the
microbenchmark verdict in [#15](https://github.com/crtahlin/wasp/issues/15) once
this completes.

**Status: running.** bench-2 is filling its Pebble reserve; the deciding
numbers land once it holds a reserve comparable to bench-1's. This document
fixes the method and the verdict criteria *before* the numbers arrive, per the
repo's measure-before-you-conclude rule, and records fill progress as it goes.

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

- **Fill / write throughput** — `bee_puller_pullsync_rate` (chunks/s) and
  time-to-full. The headline for the write path: does Pebble ingest a reserve at
  least as fast as goleveldb?
- **Write latency** — `ReservePut` average from
  `bee_localstore_method_calls_duration`.
- **Read latency** — `ReserveGet` / `ReserveHas` average, and the read-heavy
  **reserve sample** duration (`bee_localstore_reserve_sample_duration_seconds`),
  which is the retrieval path the redistribution game exercises.
- **Write-stall behaviour** — whether either engine stalls under sync. See the
  caveat below on comparing this across engines.
- **On-disk size** — the indexstore size for the *same* reserve, goleveldb vs
  Pebble (sharky is engine-independent and should match).
- **Resource use** — bee process RSS and CPU.

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
- serves reads — `ReserveGet` and the reserve sample — at least as fast at a full
  matched reserve, **and**
- shows no write stall that goleveldb avoids, **and**
- uses comparable or less disk for the same reserve, and comparable memory.

If Pebble is not clearly better, the [#15](https://github.com/crtahlin/wasp/issues/15)
"do not adopt" verdict stands with real-node evidence behind it, and the effort
moves to the goleveldb-contention work
([#23](https://github.com/crtahlin/wasp/issues/23),
[#28](https://github.com/crtahlin/wasp/issues/28),
[#29](https://github.com/crtahlin/wasp/issues/29)) — the survey's conclusion that
the pure-Go field has no clearly better option.

## Fill progress

Times approximate, from `ab-snapshot.sh`. bench-1 is full throughout (the
control); the rows track bench-2 filling.

**Table — bench-2 Pebble reserve fill (bench-1 goleveldb steady at ~4.09M / radius 9)**

| elapsed | bench-2 reserve | radius | pullsync /s | pebble indexstore | notes |
|---|---|---|---|---|---|
| ~10 min | 297,127 | 9 | ~314 | 0.33 GB | adopted radius 9 immediately; no stall |

More rows land as it fills. The comparison table is written once bench-2 reaches
a full, matched reserve.
