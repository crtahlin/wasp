# Close should not give up before the store is closed

Issue: [#428](https://github.com/crtahlin/wasp/issues/428).
Type: fix.

## Problem

`DB.Close` gives up before the store is closed whenever a background drain takes
longer than three seconds. The store is then left unclosed, the unclean-shutdown
marker stays, and the next start replays the write-ahead log.

Two timeouts disagree, both in `pkg/storer/storer.go`:

- each drain waits **five** seconds, hardcoded:
  `syncutil.WaitWithTimeout(&db.inFlight, 5*time.Second)` for the reserve
  workers and the same for the cache workers;
- `Close` waits `db.shutdownTimeout`, which defaults to
  `defaultShutdownTimeout`, **three** seconds (`:255`).

The store is closed only after both drains have returned:

```go
	go func() {
		defer close(closerDone)
		<-bgReserveWorkersClosed
		<-bgCacheWorkersClosed
		err = db.dbCloser.Close()
	}()
	...
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
		return fmt.Errorf("storer closed with bg goroutines running after %s", shutdownTimeout)
	}
```

So a drain taking between three and five seconds makes `Close` return at three,
while `dbCloser.Close()` has not run. It runs later, in a goroutine, racing
whatever the caller does next, which on shutdown is process exit.

The error message is misleading too. "storer closed with bg goroutines running"
reads as closed, with something still running. What happened is that it was
**not** closed.

## Why it is reachable

[#399](https://github.com/crtahlin/wasp/issues/399) put the close after the
drains, so a reserve scan could not run against a closing store. That ordering is
what makes the disagreement matter: before it, the close waited for nothing.

[#407](https://github.com/crtahlin/wasp/issues/407) stopped the evict and
unreserve loops ignoring the quit signal, which is one way a drain used to
exceed five seconds. It does not fix this: a drain can still be slow, and this
is about the store not being closed at all rather than about what a slow drain
is doing.

An operator who sets `ShutdownTimeout` above five seconds does not have the
problem. The default configuration does.

## The change

Derive one bound from the other so they cannot drift apart again, which is how
they got into the wrong order.

- The drains take the **shutdown budget** rather than a hardcoded five seconds,
  so `ShutdownTimeout` is the single number an operator reasons about.
- `Close` then waits for the close itself with a small fixed grace on top of
  that budget, because the drains are now self-bounding and the only remaining
  unbounded step is `dbCloser.Close()`.
- The two failures are reported differently: a drain that did not finish is not
  the same as a store that was not closed, and the message says which.

### Why the store is still closed when a drain times out

This is the part worth stating, because it looks like it contradicts #399.

It does not. `syncutil.WaitWithTimeout` returns false on timeout but the drain
goroutine returns either way, so `bgReserveWorkersClosed` and
`bgCacheWorkersClosed` close in both cases and the closer goroutine proceeds.
**Closing after a timed-out drain is today's behavior**, not something this
change introduces; the only difference is that `Close` now waits for it rather
than returning while it happens.

#399's ordering is preserved exactly: the close still happens after both drains
have returned or given up, never concurrently with a live scan.

### Why not simply raise the default

Raising `defaultShutdownTimeout` above five seconds is smaller and was
considered. It is rejected because it leaves two numbers whose relative order
has to be maintained by hand, and the defect is precisely that nobody
maintained it. It would also not fix an operator who sets `ShutdownTimeout` to
four seconds, which is a legitimate thing to do and still loses the store.

## What it costs

**Shutdown can take longer than it does today, by design.** Today `Close`
returns after three seconds and the process exits with the store open; after
this it waits for the store to be closed. That is the point, and it is bounded:
the drains cannot exceed the budget, so the worst case is the budget plus the
grace.

An operator who wants a faster shutdown lowers `ShutdownTimeout`, and now gets
what the name says: the whole of `Close` inside that budget, rather than the
drains taking five seconds regardless.

Nothing changes for a clean shutdown, where the drains return immediately.

## Protocol impact

None. A local shutdown path with no wire component.

## Tests

In `pkg/storer`, mutation checked: revert the change and confirm the test fails.

- **A drain slower than the budget still closes the store.** The test holds
  `db.inFlight` past the budget and asserts `dbCloser.Close()` ran before
  `Close` returned. This is the defect and it must fail today.
- **The error distinguishes the two cases**, so a reader of a log can tell a
  slow drain from an unclosed store.
- **A clean shutdown is unchanged**: with nothing in flight, `Close` returns
  promptly and reports no error.
- **`Close` stays idempotent.** `quitOnce` guards the quit channel and
  `TriggerQuit` shares it, so a second `Close` must not panic.

Testability was checked before this was written rather than assumed: `dbCloser`
is an `io.Closer` field on `DB`, so a test can substitute one that records
whether it ran, and `TriggerQuit` in `export_test.go` already exists for driving
shutdown from a test.

## Measurement

None on the bench. The claim is about ordering within one function, and a unit
test that holds a drain open states it exactly. Rule 7 is for claims about how a
node performs.

## Rollout and rollback

No migration and no on-disk change. `ShutdownTimeout` keeps its name, its
default and its meaning, and gains authority over the drains. Rollback is
reverting the merge commit.

## Upstream portability

**Not applicable, and this one is worth being careful about.** The ordering
here is fork-authored, introduced by #399. Checked rather than assumed: upstream
`v2.8.2` calls `db.dbCloser.Close()` **first**, at its line 657, and only then
waits for the drains. So on a timeout upstream's store has already been closed,
and it does not have this defect.

What upstream has instead is the defect #399 fixed here: closing the store while
a reserve scan may still be iterating it. Trading that for this one was the
right way round, and the change below keeps #399's ordering while removing the
cost it introduced.

**No `affects-upstream` marker**, and the issue should not gain one.

## Files

- `pkg/storer/storer.go`, `Close` and the two drain goroutines.
- `pkg/storer/storer_test.go`, or a new test file alongside it.
- `docs/DIFFERENCES.md`: the #399 row describes this ordering and gains the
  bound that now governs it.

Generated with help of AI.
