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
