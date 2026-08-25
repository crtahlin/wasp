# Results — restore headroom between L0 compaction and write pause

Issue: [#24](https://github.com/crtahlin/wasp/issues/24) ·
Spec: [`spec.md`](spec.md) ·
Harness: `beebench/l0depth_test.go`

## Outcome: the hypothesis is falsified

The spec proposed returning `CompactionL0Trigger` from 8 to goleveldb's default
of 4, on the hypothesis that compaction starting four files later is what lets
level 0 reach the pause trigger.

**It is not.** Under sustained write load heavy enough to reach the pause
trigger, the two settings are indistinguishable.

**Table — Level-0 depth under sustained writes, by `CompactionL0Trigger`**

Synthetic, `beebench`, 2,000,000-entry pre-populated index store, 8 concurrent
writers, 300 s per arm, options otherwise identical to `pkg/storer`.

| trigger | peak depth | samples ≥8 | samples ≥12 | writes paused | write delays | delay seconds | writes |
|---|---|---|---|---|---|---|---|
| **8** (shipped) | 12 | 2262 | 1573 | **yes** | 25311 | 175.18 | 145,076,000 |
| **4** (proposed) | 12 | 2239 | 1622 | **yes** | 24963 | 179.35 | 144,866,000 |

Every measure lands within a couple of percent. Both arms reached the pause
trigger and blocked writes.

## What this does and does not establish

**The stall reproduces.** Both arms hit `WriteL0PauseTrigger` and paused. The
mechanism described in the spec is real and is reachable under load, which was
not previously demonstrated anywhere — the field report was operational.

**The trigger is not the lever.** Starting compaction at 4 rather than 8 does not
keep level 0 below 12 when compaction cannot keep up. Once the write rate exceeds
compaction throughput, level 0 grows to the pause trigger regardless of when
compaction began; starting earlier only means it was already running and still
losing.

**It says nothing about a real node.** This exercises the index store directly.
Whether a Swarm node under pullsync ingest reaches this write rate is a separate
question, unanswerable on bench-1 today: it holds no funds, so no postage batch
can be bought and nothing can be uploaded, and its reserve was static over a
sixty-second window, so pullsync is not ingesting. Its live level-0 depth reads 0.

## A smaller run pointed the other way, and was wrong

An earlier run — 100,000 entries, 4 writers, 15 s — showed trigger 4 with a lower
peak (10 against 11), 42% fewer samples at or above the slowdown threshold, and
*more* completed writes. That looked like a clean confirmation and was reported
as promising.

It did not survive scale. Neither arm reached the pause trigger in that run, so
it was measuring behaviour in the region where compaction still keeps up — where
the trigger does shift things slightly and nothing is at stake. The regime the
issue is about only appears once compaction is losing, and there the difference
vanishes.

Recorded because the mistake is the reusable part: a difference measured outside
the failure regime does not predict behaviour inside it.

## The stall needs 16x a saturated 1 Gbit/s link

Reproducing it is not the same as it being reachable. The unlimited run drove
483,587 index writes per second, which is 1.98 GB/s of chunk-equivalent ingest.
Sweeping the rate downward, with the shipped trigger, 120 s per point:

**Table — Level-0 depth against sustained write rate, `CompactionL0Trigger=8`**

| target writes/s | achieved | peak depth | paused | GB/s equivalent |
|---|---|---|---|---|
| 2,000 | 2,000 | 2 | no | 0.01 |
| 10,000 | 10,000 | 3 | no | 0.04 |
| 30,000 | 30,000 | **6** | no | 0.12 |
| 100,000 | 100,000 | 8 | no | 0.41 |
| 300,000 | 300,000 | 9 | no | 1.23 |
| unlimited | 483,587 | **12** | **yes** | 1.98 |

A saturated 1 Gbit/s link carries about 30,500 chunks/sec. At that rate level 0
peaks at 6 — below the compaction trigger of 8, never mind the pause at 12. Even
at ten times line rate it reaches only 9.

So on this hardware the failure needs more inbound bandwidth than a node can
have, and no amount of postage funding changes that. The batch itself is cheap
— about 0.56 BZZ per day at depth 22 — which was never the constraint.

### What that does and does not license

It does **not** license "the stall cannot happen". The binding ratio is write
rate against *compaction throughput*, and this ran on fast NVMe with the machine
otherwise idle. Four things a real node does, none of them present here, cut
compaction throughput:

| factor | effect |
|---|---|
| slower disk (SATA SSD, or spinning) | 10-100x less compaction throughput |
| sharky chunk writes alongside index writes | competes for the same device |
| reserve sampling reads | seeks steal from compaction — this is #23 |
| a smaller `WriteBuffer` than the 64 MB used here | more L0 files per unit of data |

On a node whose effective throughput is 20x lower, depth 12 would arrive around
24,000 writes/sec, which is inside 1 Gbit/s. The honest statement is that the
threshold is **disk-and-contention dependent**, and that this machine sits far on
the safe side of it.

That is itself a finding: whatever produced the original field report was
probably not write rate alone, but write rate on slower storage while sampling
competed for the same spindle.

## Where this redirects the work

The spec anticipated this outcome and said how to read it, which is the only
reason it can be reported as a result rather than a surprise:

> A negative result looks like L0 depth reaching 12 in both arms. That means
> compaction throughput is the constraint, and the answer is #23 and #29, not this
> constant.

So the p0 should move toward reducing write pressure and contention rather than
retuning the threshold:

- [#23](https://github.com/crtahlin/wasp/issues/23) — pause pullsync during
  sampling, to stop the two competing for the same store
- [#29](https://github.com/crtahlin/wasp/issues/29) — Reserve Put acquiring two
  locks per chunk

Reverting the trigger remains defensible on the narrower ground that 4/8/12 is
the threshold triple goleveldb is tuned and tested for, and that wasp's 8/8/12
has no recorded rationale — the setting arrived in an undocumented perf-tuning
commit. But it should not be described as fixing the stall, because it does not.

## Related findings

Building the harness and its unit tests surfaced three defects, all filed:

- [#114](https://github.com/crtahlin/wasp/issues/114) — goleveldb data-races on
  close when a writer is paused
- [#115](https://github.com/crtahlin/wasp/issues/115) — `Store.Close` writes, so a
  stalled store cannot shut down cleanly
- and, on #115, that `CompactRange` blocks on the writer lock the paused writer
  holds — so there is no in-process recovery from the stall at all

That last one matters for the direction above: if the stall cannot be recovered
from, keeping the store away from the pause trigger is the only defence, and that
means controlling write pressure.
