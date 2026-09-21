# Spec: close the store only after the reserve worker has stopped using it

Issue: [#399](https://github.com/crtahlin/wasp/issues/399). Type: fix. Area: storage.
Affects upstream: yes (the racy ordering is present unmodified in bee v2.8.2).

## Problem

On shutdown the node can die with a segmentation fault instead of exiting cleanly. A
crash watch on the bench-2 node (pebble engine, build `wasp 0.1.3-d396-5ea54d65`, base
bee v2.8.2) caught it on 2026-09-21 at 12:49:08 CEST: a nil pointer dereference (SIGSEGV,
process exit status 2) during a graceful stop.

The stack trace shows the reserve worker iterating the store while the store is being
closed:

```
pebble/sstable.(*Reader).readBlock  (nil deref)
... pebble iterator Next ...
pebblestore.(*Store).Iterate            pebblestore/store.go:290
reserve.(*Reserve).IterateChunksItems   reserve/reserve.go:589
storer.(*DB).countWithinRadius          storer/reserve.go:161
storer.(*DB).reserveWakeupScan          storer/reserve.go:203
storer.(*DB).reserveWorker              storer/reserve.go:265
```

`DB.Close()` in `pkg/storer/storer.go` starts three goroutines at once: one drains the
reserve worker (`db.inFlight`, up to five seconds), one drains the cache workers, and one
closes the underlying store (`db.dbCloser.Close()`). The store-closing goroutine is not
ordered after the worker drain, so it runs at the same time. When a reserve wake-up scan
is iterating when the store closes, pebble reads a data structure that closing has already
torn down. Pebble requires that no iterators are active when the store is closed;
goleveldb returns an error from an iterator used after its store is released rather than
reading freed memory, so the same race is a logged error on goleveldb and a crash on
pebble. This fork defaults to pebble (issue #185), so it is materially more exposed.

Evidence it is a race, not corruption: it was the only panic in five days; the node was
being restarted often by a separate experiment (about a dozen restarts between 12:19 and
12:56 CEST); there were no corruption or checksum signatures; and the node recovered on
the next start with a clean write-ahead-log replay. The small window, the store close has
to land during an in-flight scan, is why it is rare.

## Hypothesis

Two changes remove the race. First, closing the store only after the reserve-worker and
cache-worker drains have finished means the store is never closed while an iterator is
live, in the common case. Second, making the within-radius scan stop promptly on the
shutdown signal means the drain finishes quickly rather than reaching its five-second
timeout, so the ordered close does not have to wait long and the timeout path, which would
otherwise still race on a large reserve whose scan runs longer than five seconds, is not
reached during a normal shutdown.

The shutdown signal already exists: `Close()` closes the `db.quit` channel as its first
action, and the package already defines an `ErrDBQuit` sentinel and checks `db.quit` in
other reserve paths. The two scan callbacks do not currently check it.

## Design

Two edits, both in `pkg/storer`.

1. `pkg/storer/storer.go`, `DB.Close()`: order the store close after the worker drains.
   The goroutine that calls `db.dbCloser.Close()` first waits for the reserve-worker and
   cache-worker drain signals, then closes the store. The overall `shutdownTimeout` still
   bounds the whole operation, so a stuck worker cannot make shutdown hang: each drain has
   its own five-second `WaitWithTimeout`, and the outer timeout still returns if the total
   runs long.

2. `pkg/storer/reserve.go`, both scan callbacks: `countWithinRadius` and the cheap-path
   `countChunksWithinRadius` check `db.quit` at the top of each iterated element and stop
   the iteration when it is closed, returning the existing `ErrDBQuit` sentinel. This uses
   the same shutdown signal `Close()` raises and needs no new parameter on either method,
   so the `debug.go` caller of the cheap path is unaffected. It turns shutdown into a
   prompt end of the scan instead of running the scan over the whole reserve.

Together: the scan ends quickly on shutdown (edit 2), so the drain completes well within
its timeout, and the store is closed only after that drain (edit 1). The residual timeout
path, the drain not finishing in five seconds, is no longer reachable by an in-flight
reserve scan, because that scan now returns as soon as the context is cancelled.

## Protocol impact

None. This changes shutdown ordering and adds a context check inside a local scan. It
touches no wire format, no constant in `.github/protocol-freeze.lock`, and nothing under
`pkg/p2p/`, `pkg/swarm/`, or `pkg/config/`. No `protocol-change` label.

## Measurement

A regression test in `pkg/storer` that reproduces the race and asserts it is gone: open a
store on the pebble engine, start a long `IterateChunksItems` over it, and call `Close()`
concurrently. Before the fix this panics; after it, `Close()` returns without a panic and
the iteration ends with a context or closed error rather than a segfault. Run it with the
race detector (`make test-race`) so a residual overlap shows up even when it does not
crash.

Field confirmation: the same bench-2 node under the same restart churn should stop
producing the crash. A negative result is any panic on the shutdown path after the change,
or a shutdown that now hangs to the timeout.

## Rollout and rollback

No configuration and no operator action. The change is a correctness fix in the shutdown
path, on by definition. Rollback is reverting the merge commit; there is no state or
on-disk format change to undo.

## Upstream portability

The racy ordering is present unmodified in `upstream/v2.8.2:pkg/storer/storer.go`, so the
fix applies directly upstream. The only fork-side difference in `Close()` is that the
shutdown timeout is configurable here; the ordering change sits on top of that without
depending on it. The context check in `countWithinRadius` is likewise independent of any
fork-specific change. Ethersphere could adopt both edits as-is. This is recorded under the
`affects-upstream` marker on #399, which is a marker for a later human decision only and
does not authorise contacting upstream (fork rule 1).

## Configuration

None. This fix tunes no constant and adds no flag. The existing `shutdownTimeout` option
is unchanged and keeps its current default.

Generated with help of AI.
