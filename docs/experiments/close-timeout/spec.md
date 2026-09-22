# Close should not give up before the store is closed

Issue: [#428](https://github.com/crtahlin/wasp/issues/428).
Type: fix.

## Problem

`DB.Close` gives up before the store is closed whenever a background drain takes
longer than three seconds. The store is then left unclosed and the next start
replays the write-ahead log.

One qualification, since the first draft overstated it: the unclean-shutdown
**marker** is goleveldb's, written and cleared in `pkg/storage/leveldbstore`.
`pkg/storage/pebblestore` has no marker, and pebble is wasp's default index
engine for a new data directory. The write-ahead replay applies to both; the
marker half applies only to goleveldb.

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

A caller that sets `ShutdownTimeout` above five seconds does not have the
problem. The default does. No node can set it, see below.

## The change

Make `Close` wait for the close it is named for.

> **Corrected at implementation, twice over.** The first draft of this section
> said the drains should take `ShutdownTimeout` instead of their own five
> seconds. Review showed that **narrows the #399 window from five seconds to
> three**: the store is closed once the drains return *or give up*, so a shorter
> drain pulls the store out from under live reserve work sooner, which is the
> pebble segfault #399 exists to prevent. That trade was never stated, and the
> draft's claim that the close is "never concurrently with a live scan"
> contradicts its own "returned or given up" in the same sentence.
>
> The draft also framed `ShutdownTimeout` as the number an operator sets. **It
> is not a node setting at all**: it appears nowhere in `cmd/`, and
> `docs/DIFFERENCES.md` says so. On a real node the whole operator framing was
> fiction.

What is implemented instead:

- The drains keep **their own window**, `drainTimeout`, still five seconds, so
  #399's exposure is unchanged. `ShutdownTimeout` may only **extend** it, never
  shorten it.
- `Close` **waits for the close** rather than racing it. That is the whole fix:
  the defect was returning early, not the length of the drain.
- The store's own `Close` gets **no timer at all**. A first implementation gave
  it a five second grace and CI found a real store on Windows takes longer than
  that, so `Close` reported the store as unclosed while it was still closing
  and the test could not delete its files. Bounding this step and returning
  early is the defect itself with a different number. The drains above are
  bounded, so this is the last step and nothing races it; a pathological store
  close blocks shutdown, which is visible, and `cmd/bee` still exits on a
  second interrupt. That is better than reporting either outcome while the
  store is open, and it leaves exactly one number in the change.
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
have returned or given up. Note that "given up" means the work may still be
running, so this is not a guarantee that no scan is live; it is the same
guarantee #399 shipped, neither stronger nor weaker, and the drain window that
bounds it is unchanged.

### Why not simply raise the default

Raising `defaultShutdownTimeout` above five seconds is smaller and was
considered. It is rejected because it leaves the outer bound **racing** the
drains rather than following them, which is the actual shape of the defect: the
problem was never the numbers alone but that `Close` returned while the close
was still pending. Making `Close` wait fixes it for every value of both.

## What it costs

**Shutdown can take longer than it does today, by design.** Today `Close`
returns after three seconds and the process exits with the store open; after
this it waits for the store to be closed. That is the point, and it is bounded:
the drains cannot exceed the drain window, and the store's own close is then
waited for rather than raced.

Nothing changes for a clean shutdown, where the drains return immediately, and
that is pinned by a test rather than asserted.

`ShutdownTimeout` is a Go API option, not a node setting, so no operator can
change any of this without recompiling. The earlier draft's "an operator who
sets four seconds" was wrong on that point as well as on the trade.

## Protocol impact

None. A local shutdown path with no wire component.

## Tests

In `pkg/storer`, mutation checked: revert the change and confirm the test fails.

- **A drain slower than the window still closes the store.** Holds
  `db.inFlight` past the window and asserts the store was closed before `Close`
  returned. This is the defect; it fails against the old `Close`.
- **The store is closed AFTER the drains**, which is #399's property. Review of
  the first implementation found nothing in the repository pinned it: a `Close`
  that closed the store first passed every test in the package. The closer now
  records whether work was in flight at the moment it ran.
- **A cache drain that times out force-closes the limiter.** Also found by
  review: deleting that call left every test green.
- **A clean shutdown is prompt**, so the fix is not paid on every shutdown.
- **`Close` stays idempotent.** `quitOnce` guards the quit channel and
  `TriggerQuit` shares it, so a second `Close` must not panic.

Each of the five has a mutation that kills it and no other.

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
default and its meaning, and may now extend the drain window but never shorten
it. Rollback is reverting the merge commit.

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
