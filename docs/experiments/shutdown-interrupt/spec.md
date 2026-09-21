# Spec: stop the evict and unreserve loops on the quit signal

Issue: [#407](https://github.com/crtahlin/wasp/issues/407). Type: fix. Area: storage.
Affects upstream: to be decided by the reproducing test, as [#291](https://github.com/crtahlin/wasp/issues/291)
was. See "Upstream" below.

## Problem

[#399](https://github.com/crtahlin/wasp/issues/399) fixed a shutdown crash by ordering
the store close after the reserve worker drains, and by making the within-radius scan
return promptly on the quit signal. The reserve worker has two other operations that use
the store and got neither treatment: `evictExpiredBatches`, on the batch-expiry trigger,
and `unreserve`, on the over-capacity trigger.

**The drain gives up rather than waiting.** `Close` runs
`syncutil.WaitWithTimeout(&db.inFlight, 5*time.Second)`, which returns false on timeout,
and the caller's `defer close(bgReserveWorkersClosed)` fires either way
(`pkg/storer/storer.go:992-998`). So the closer goroutine proceeds to
`db.dbCloser.Close()` **while the operation is still running**:

```go
go func() {
	defer close(bgReserveWorkersClosed)
	if !syncutil.WaitWithTimeout(&db.inFlight, 5*time.Second) {
		db.logger.Warning("db shutting down with running goroutines")
	}
}()
...
go func() {
	defer close(closerDone)
	<-bgReserveWorkersClosed
	<-bgCacheWorkersClosed
	err = db.dbCloser.Close()
}()
```

That is the same read-after-close #399 removed for the scan, reachable whenever an evict
or an unreserve runs longer than five seconds. The warning line
`db shutting down with running goroutines` is the only sign it happened.

## What is and is not established

**Read from the code, not reproduced.** The crash observed in #399 was the scan path,
through `countWithinRadius`. No crash has been seen in `evictExpiredBatches` or
`unreserve`. The bound is real: both finish well inside five seconds in the common case,
so this is the rare path where one does not.

What would settle it: a test that holds one of those operations past the drain timeout
and calls `Close` concurrently, and shows the store being used after it is closed. That
test is the deliverable here, exactly as it was for #291, and it decides the
`affects-upstream` label. If it does not reproduce, the reading was wrong and this spec
is withdrawn rather than patched.

## The change

Give both loops the prompt exit the scan already has: check the quit signal at a safe
point between chunks or between batches and stop with `ErrDBQuit`, so the drain finishes
quickly instead of timing out.

**The alternative, and why it is not taken here.** #399 mentioned making the store
reject new operations once closing has begun, which would make this whole class
impossible rather than fixing each caller. That is the better end state and it is a
larger change: every caller of the store would need to handle a new failure mode, and
the ones that currently assume success would have to be found. It should have its own
issue rather than arriving inside this one. This change makes the two known loops safe
and does not preclude it.

## A second defect found while specifying this, filed separately

The drain waits **five** seconds, and `Close` waits `defaultShutdownTimeout`, which is
**three** (`pkg/storer/storer.go:255`). So whenever a drain takes longer than three
seconds, `Close` returns `storer closed with bg goroutines running after 3s` **before
`dbCloser.Close()` has run at all**, and the actual close then races process exit. The
store can be left unclosed, which means the unclean-shutdown marker stays and the next
start replays the write-ahead log.

That is a different defect from this one, in the same function, and fixing either does
not fix the other. Filed as [#428](https://github.com/crtahlin/wasp/issues/428) rather
than folded in.

## Verification

- A test that makes an evict or an unreserve block past the drain timeout, calls `Close`
  concurrently, and asserts the store is not used after it is closed. On unmodified code
  this must fail; that failure is the evidence.
- A test that the quit signal actually shortens the drain: with the fix, `Close` returns
  well inside the timeout while one of those operations is in flight.
- Normal operation is unaffected: an evict or unreserve that is not racing a shutdown
  still completes and still evicts what it was going to evict. Without this, a fix that
  made the loops exit too eagerly would pass the first two tests.
- Under `-race`, since the claim is about concurrent use of the store.
- Mutations over the whole package with `-run .`: removing each quit check must fail the
  first test; a mutation that fails to compile is reported as a build failure rather than
  as caught.

Per rule 7 this is code rather than a bench measurement, so the discipline is the
mutation check rather than three runs on a node.

## Scope

`pkg/storer` and its tests. No configuration, no wire change. What a node does on
shutdown changes in a way an operator can observe, through the warning line and through
whether the next start replays the log, so `docs/DIFFERENCES.md` gains a row per rule 13.

## Upstream

`pkg/storer/storer.go` **is** modified in this fork, by #399 among others, so a
file-level diff proves nothing and the question has to be asked of upstream's own copy:
whether its evict and unreserve loops check a quit signal, and whether its `Close` has
the same drain-then-close ordering at all. That check belongs in the implementation,
with the answer recorded, and the label goes on only if the reproducing test reproduces
against unmodified upstream code. Per rule 11 the label is a marker for a later human
decision and nothing more.
