# Separate the expired-batch sweep from the reserve count

Issue: [#28](https://github.com/crtahlin/wasp/issues/28)

## What is already done, and what is left

Half of #28 landed in [#121](https://github.com/crtahlin/wasp/pull/121):
`batchstore.Exists` is asked once per batch rather than once per chunk. On
bench-1 that is 426 lookups per wake-up instead of 4,064,293, each of which took
a read lock and a store `Get`.

This spec is for the other half. The issue frames it as "start the iteration at
`radius` instead of filtering from bin 0", and notes that the expired-batch check
in the same loop makes that not a one-line change. That framing is right that
there is a problem and wrong about which part of it costs anything, so this spec
starts by measuring.

## What starting at radius is worth

`countWithinRadius` iterates from bin 0 and counts only chunks at or above the
storage radius, so the chunks below radius are read and discarded. The question
is how many that is.

**Table 1 — Reserve composition on bench-1, from `/status` on 2026-08-26**

| Quantity | Chunks | Share of reserve |
|---|---|---|
| Reserve size | 4,065,042 | 100% |
| At or above storage radius | 3,646,069 | 89.7% |
| Below storage radius | 418,973 | **10.3%** |

Storage radius 9, committed depth 9.

So starting the iteration at `radius` would remove **10.3%** of the scan on this
node right now. That is a real saving and it is not the problem. The remaining
89.7% is read either way, and it is that 89.7%, every 15 minutes, that the issue
describes as "a long scan holding up other reserve operations".

The share is not fixed: it is whatever has accumulated below radius since the
radius last moved, and it is zero immediately after eviction catches up. Treating
10.3% as a constant would be wrong. Treating it as the order of magnitude is not.

**The lever is the cadence of the scan, not where it starts.** The rest of this
spec is about which parts of the scan need to run every wake-up at all.

## What the scan is actually for

The loop does two unrelated jobs:

```go
err := db.reserve.IterateChunksItems(0, func(ci *reserve.ChunkBinItem) (bool, error) {
	if ci.Bin >= radius {
		count++            // job 1: how full is the reserve within radius
	}
	if !exists(ci.BatchID) {
		missing++          // job 2: does this chunk's batch still exist
		evictBatches[string(ci.BatchID)] = true
	}
	return false, nil
})
```

Job 1 feeds the radius decision made immediately afterwards, and is written to
`/status` as `reserveSizeWithinRadius`. It has to be current, because the node
lowers its radius on the strength of it.

Job 2 is **not the expiry path.** Batch expiry already has its own mechanism, and
tracing it end to end matters here, because if job 2 were the primary path then
its cadence would not be negotiable.

`batchstore.cleanup` handles a batch whose value has fallen below the cumulative
payout:

```go
err = s.evictFn(b.ID)                       // -> storer.EvictBatch
...
err = s.store.Delete(batchKey(b.ID))        // only then does Exists() go false
```

`storer.EvictBatch` writes an `expiredBatchItem` and triggers the `batchExpiry`
event; the reserve worker picks that up and runs `evictExpiredBatches`. The
ordering is what matters: **the `expiredBatchItem` is durable before the batch
key is deleted**, so on the normal path `Exists` can never report false for a
batch the storer has not already been told about.

That makes job 2 a reconciliation net, not a primary path. It catches divergence
between two stores that share no transaction — a partially completed eviction
whose `expiredBatchItem` was already deleted, a batchstore rebuilt from a
different chain start block, chunks left by a crash between two writes. On a
healthy node it finds nothing, and it pays a full reserve scan every 15 minutes
to find it.

`ReserveMissingBatch` already reports what it finds. On a node where that gauge
has been flat at zero, every one of those scans found nothing.

## Why job 2 cannot simply start at radius

This is the point the issue's comment thread reached, and it stands: the same
loop does both jobs, so starting at `radius` would stop job 2 ever seeing chunks
below radius. Those chunks would never be evicted when their batch expired,
because nothing else looks at them.

That is a change in eviction behaviour dressed as a scan optimisation, and it is
why the cheap version of this issue was not done.

## Design

Split the loop into two, and give each the cadence its job needs.

1. **Count, every wake-up.** `countChunksWithinRadius()` already exists — it was
   added in [#127](https://github.com/crtahlin/wasp/pull/127) to give
   `reserveSizeWithinRadius` one definition — and it iterates from the storage
   radius. The wake-up path uses it and gets the 10.3% for free, because it no
   longer has to see the chunks below radius.

2. **Reconcile, on its own interval.** A separate sweep iterates from bin 0,
   asks `batchstore.Exists` once per batch, and evicts what it finds. It keeps
   the whole of the current behaviour, including chunks below radius.

Configuration, per rule 8 in `AGENTS.md`:

| Setting | Default | Meaning |
|---|---|---|
| `--reserve-batch-sweep-interval` | `0` | How often the reconciliation sweep runs. `0` runs it on every reserve wake-up, which is the current behaviour. |

**The default is the current behaviour**, so nothing changes on merge, and an
operator who wants the sweep hourly or daily can have it. The interval is worth
setting only once the sweep is known to be finding nothing, which
`ReserveMissingBatch` already answers per node.

### What a longer interval costs

A chunk whose batch has vanished, on a node where the normal expiry path did not
run, survives until the next sweep. It is not served incorrectly — retrieval does
not consult the batch — but it occupies reserve capacity that a chunk with a
living batch could have used, and it is skipped during sampling when its stamp
fails to load. Both are bounded by the sweep interval, and both apply only to the
divergence case that the sweep exists to repair.

The failure this must not introduce is a node that has stopped reconciling
entirely because the interval was set to something absurd. The sweep should
therefore log at info when it evicts anything, and `ReserveMissingBatch` should
keep being set on every sweep rather than only when it is non-zero, so a stale
gauge is distinguishable from a zero one.

## Measurement

The claim is that the wake-up scan gets cheaper without the radius decision
getting worse.

- **Scan duration at the wake-up cadence, before and after.** There is no metric
  for this today; one is needed, and it is the reason the issue's "long scan
  holding up other reserve operations" has never been quantified. Add a
  histogram around the count and record it before changing anything.
- **`reserveSizeWithinRadius` unchanged.** The two paths must agree; #127 exists
  because they once did not. A test asserts the count is identical whether the
  sweep ran in the same pass or not.
- **`ReserveMissingBatch` unchanged over a full sweep interval.** Separating the
  jobs must not change what reconciliation finds, only when it looks.

A negative result would be that the count-only scan is not measurably cheaper
than the combined one — that the 10.3% is inside the noise and the cost was
somewhere else entirely, most likely the iteration itself rather than what the
callback does. That would redirect the issue toward maintaining the count
incrementally, which is the option the issue raises and this spec does not take,
because a derived counter that drifts is worse than a slow scan: the radius
decision is made on it.

## Protocol impact

None. Both jobs are local bookkeeping. The radius the node ends up at is
unchanged, and radius is a local decision that peers observe rather than agree
on.

## Rollout and rollback

Rollback is setting `--reserve-batch-sweep-interval` to `0`, which is also the
default and restores the single combined pass. No persisted state changes.
