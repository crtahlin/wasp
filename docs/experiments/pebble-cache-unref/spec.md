# Spec: release our reference to the pebble block cache after Open

Issue: [#815](https://github.com/crtahlin/wasp/issues/815). Type: fix. Area: storage.
Affects upstream: no (the pebble index store is wasp-only).

## Problem

`pebbleIndexStoreOptions` in `pkg/storer/storer.go` creates the block cache with
`pebble.NewCache` and puts it in the options passed to `pebblestore.New`, which
calls `pebble.Open`. A new cache starts with one reference, owned by whoever created
it. `pebble.Open` takes its own reference (`opts.Cache.Ref()`, pebble v1.1.5
`open.go:163`) and drops it on `Close`, or on an open error. Our creator reference
is never dropped. The comment above the function says this is on purpose:

> its one retained reference is released when the process exits, which for a store
> opened once per node lifetime is not worth threading a closer through for.

That is true for a running node, which opens the index store once. It is not true
for code that opens the store more than once in one process: the storer and storage
tests, and test helpers that reopen a store. After each `Close` the cache is still
referenced, so it is never freed.

How much that costs depends on the build:

- With `CGO_ENABLED=0` (all release builds today) the cache values are Go memory.
  They are freed only when the garbage collector finds nothing points to them; the
  leaked reference keeps the cache object itself, which is small.
- With `CGO_ENABLED=1` pebble allocates cache values and memtable buffers with C
  `calloc` (`internal/manual`, `internal/cache/value_normal.go`). C memory is freed
  only when the reference count reaches zero, so every open leaks the cache's C
  memory for the rest of the process.

This was found in the cgo gate of [#810](https://github.com/crtahlin/wasp/issues/810),
which audits pebble buffer use before any cgo build ships: with pebble's
`invariants` build tag (reference and leak checks), the `pkg/storer` tests stop on
this reference. #810 plans cgo Linux builds, so the leak becomes real memory then.

## Hypothesis

Dropping our reference once `pebble.Open` has returned, on the success path and on
the error path, leaves pebble as the only owner. The cache is then freed when the
store closes, and the `invariants` build passes. A running node is not affected: it
holds the store open until exit either way.

## Design

The owner of the reference is the code that called `pebble.NewCache`, so the release
goes there, not into `pebblestore.New` (which also receives caller options it did not
create, for example from benchmarks).

In `pkg/storer/storer.go`, where the pebble index store is opened:

```go
o := pebbleIndexStoreOptions(opts)
store, err := pebblestore.New(indexDir, o)
if o.Cache != nil {
	o.Cache.Unref() // pebble.Open holds its own reference; drop ours
}
```

The release runs on both paths. On success pebble keeps its reference until `Close`.
On error pebble has already dropped its own (`open.go:187-197`), so ours is the last
one and the cache is freed at once.

The comment on `pebbleIndexStoreOptions` is rewritten to say that the caller releases
the creator reference after `Open`. The cache size, the options and every default are
unchanged.

The two benchmark tools in `docs/experiments/storage-engine-eval/` also call
`pebble.NewCache`. They open once per process and are not part of the node; they are
left as they are.

## Protocol impact

None. No message, encoding or on-disk format changes. The index store contents are
untouched.

## Measurement

- A test opens and closes the pebble index store through the storer path in a loop,
  and runs with `CGO_ENABLED=1 -tags invariants`. Without the change pebble's
  reference checks fail; with it the test passes. Mutation check: removing the
  `Unref` must make the test fail.
- `pkg/storer/...` and `pkg/storage/...` pass with `CGO_ENABLED=0` and
  `CGO_ENABLED=1`, with and without `-race`, and with `-tags invariants`.
- A negative result would be a test that still trips the reference checks with the
  change in place, which would mean another holder of the cache exists.

No bench run is needed: a running node opens the store once, so its behaviour and
memory do not change.

## Rollout and rollback

A code change with no flag. It ships in the next build. Rollback is the previous
build.

## Upstream portability

Not applicable. Bee uses goleveldb for the index store; the pebble engine is
wasp-only.

## Configuration

None.

Generated with help of AI.
