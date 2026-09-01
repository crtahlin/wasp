# Make the index-store engine selectable, and A/B Pebble under a real reserve

Issue: [#185](https://github.com/crtahlin/wasp/issues/185) · Survey:
[`survey.md`](survey.md) · Related: [#15](https://github.com/crtahlin/wasp/issues/15)

## What this builds

An operator-selectable index-store engine — goleveldb (default) or Pebble — and
the observability parity needed to compare them fairly on real nodes. goleveldb
stays the default and only value; Pebble is opt-in. Per rule 8 this is experiment
surface, not a migration: no engine becomes the default without the A/B verdict.

The survey (`survey.md`) settled *which* alternative to test and why. This spec is
the *how*.

## What is already in place (do not rebuild)

- `pkg/storage/pebblestore` implements `storage.Store` and `storage.BatchStore`
  and passes the same `storagetest.TestStore` / `TestBatchedStore` conformance
  suites `leveldbstore` passes. `go.mod` already carries
  `github.com/cockroachdb/pebble`.
- Everything below `transaction.NewStorage` runs on interfaces. The `DB` struct,
  `PinIntegrity`, `sharkyRecovery`, `migration.Migrate` and the whole
  `transaction` package are already engine-agnostic and need no changes.
- Metrics are wired by a duck-typed `m.Collector` assertion
  (`transaction.go:148`), so any store implementing `Metrics() []prometheus.Collector`
  is picked up automatically.

## The concrete changes

All the goleveldb coupling is confined to `pkg/storer/storer.go`.

**1. The engine selector.** A `--storage-engine=leveldb|pebble` flag
(`cmd/bee/cmd/cmd.go`, `start.go`), threaded through `node.Options` and
`storer.Options` exactly as the `db-*` options are. Default `leveldb`.

**2. `initStore` returns the interface and branches.** `initStore`
(`storer.go`) becomes `(storage.Store, error)` and switches on the engine,
calling `leveldbstore.New` or `pebblestore.New`. Their signatures differ —
`leveldbstore.New` returns `(*Store, bool, error)` (the `bool` is the
unclean-shutdown flag), `pebblestore.New` returns `(*Store, error)` — so the
switch reconciles them and, for the leveldb arm, keeps today's dirty-flag
handling.

**3. Per-engine options builder.** `indexStoreOptions` stays the goleveldb
builder. A sibling maps the same `Ldb*` knobs to `pebble.Options`:

| storer knob | goleveldb | Pebble |
|---|---|---|
| block cache | `BlockCacheCapacity` | `Cache` (sized `*pebble.Cache`) |
| write buffer | `WriteBuffer` | `MemTableSize` |
| compaction start | `CompactionL0Trigger` | `L0CompactionThreshold` |
| write pause | `WriteL0PauseTrigger` | `L0StopWritesThreshold` |
| open files | `OpenFilesCacheCapacity` | `MaxOpenFiles` |
| filter | `filter.NewBloomFilter(64)` | per-level `FilterPolicy` |

goleveldb's slowdown trigger (`WriteL0SlowdownTrigger`) has no clean 1:1 in
Pebble's stall model; it is documented as leveldb-only rather than forced onto a
Pebble field.

**4. Engine-neutral store health.** The 15-second stats goroutine and the
write-pause log line (added in [#180](https://github.com/crtahlin/wasp/issues/180),
`storer.go`) read `store.DB().Stats()` — goleveldb-specific, the *only* hard
coupling. Introduce a small interface both stores satisfy, e.g.

```go
type indexStoreHealth interface {
    Level0FileCount() int
    WriteStalled() bool
    WriteStallCount() uint64
}
```

goleveldb fills it from `leveldb.DBStats` (`LevelTablesCounts[0]`, `WritePaused`,
`WriteDelayCount`); Pebble from `db.Metrics()` (`Levels[0].NumFiles`,
`WriteStallCount`/`WriteStallDuration`). The polling goroutine, the
`writePauseEdge` log line, and the metric run off the interface, so a stall is
visible on both engines — which the A/B depends on.

**5. Pebble metrics parity.** `pebblestore` exports nothing today, so a Pebble
node would be observability-blind. Add `pebblestore/metrics.go`: a `Metrics()
[]prometheus.Collector` and a collector reading `db.Metrics()`, exposing series
parallel to leveldbstore's (level file counts, write stalls, compaction). The
`m.Collector` assertion then wires it with no storer change.

**6. Datadir engine-marker guard.** No format marker exists, and the two formats
are mutually unreadable. At `initStore`, write the engine name into the datadir on
first init, and on start refuse to open a datadir created by a different engine
with a clear error naming both. Prevents silent corruption from a flipped flag.

## Tests

- `pebblestore` already passes the conformance suite; keep it green.
- A unit test on the `initStore` switch, mirroring the `indexStoreOptions` tests
  (`pkg/storer/indexstore_options_test.go`): each engine value constructs the
  right store; an unknown value errors.
- The Pebble options builder maps each knob to the right `pebble.Options` field,
  tested the same way (`0`-means-default handling documented per engine).
- The datadir marker guard: opening a leveldb datadir as pebble, and the reverse,
  fails with the named error. Tested with two temp dirs.
- Store-health parity: the interface returns sane values for both engines against
  a freshly opened store (the stall itself is unreachable on fast disk, so the
  test exercises the read path, not a real stall).
- Whole-repo suite green; `make lint`, `make build`, `make check-whitespace`.

## The A/B (real-reserve comparison)

Documented so it is reproducible.

1. **Control:** bench-1, unchanged, goleveldb, its existing ~4M-chunk reserve.
2. **Treatment:** bench-2, a bench-1-class box (`docs/agent-playbooks/bench-vm-spec.md`),
   fresh datadir, `--storage-engine=pebble`, otherwise identical config (postage,
   `nat-addr` = its own public IP, puller limits).
3. Let both fill, then compare under matched state (rule 7 — three runs, spread):
   time-to-full, sync throughput, read and write method-call latencies (the storer
   metrics), **write-stall behaviour** (the neutral health metrics), CPU / disk
   I/O / memory, and on-disk bytes for the same reserve.
4. Record in `results.md`; reach a verdict that changes [#15](https://github.com/crtahlin/wasp/issues/15)'s
   or confirms it with real-node evidence. If Pebble is not better under a real
   reserve, the conclusion is that the pure-Go field has no better option today and
   the effort goes to the goleveldb-contention work ([#23](https://github.com/crtahlin/wasp/issues/23),
   [#28](https://github.com/crtahlin/wasp/issues/28), [#29](https://github.com/crtahlin/wasp/issues/29)).

## Development and rollout

Built and verified on the dev machine first: a locally-built binary runs with
`--storage-engine=pebble` against a throwaway datadir, syncs, and emits Pebble
store-health metrics. Only then deployed to bench-2. The dependency
(`cockroachdb/pebble`) moves from indirect to direct in `go.mod` when it enters
the build; noted per the rule that dependency changes are deliberate.

## Using it: selecting an engine and switching

The engine is a property of the data directory, recorded in a `.storage-engine`
marker inside `localstore/indexstore/`. The two on-disk formats are mutually
unreadable, so a directory is bound to the engine that created it. This section
is the operator-facing how-to.

### Starting a new node on Pebble

Pass the flag once, on a fresh data directory:

```bash
bee start --storage-engine pebble
```

That creates the index store in Pebble format and writes the marker. From then
on the directory is a Pebble node.

### Running it afterwards

Do not pass the flag again. An empty `--storage-engine` resolves to the marker,
so a plain `bee start`, and every `bee db …` command, reopens the directory on
whatever engine created it. The node is otherwise identical to a goleveldb node:
same APIs, same sync. The only outward difference is the storage metrics, which
come out under `bee_pebble_*` instead of `bee_leveldb_*`; the write-stall log
line works the same on both.

### You cannot switch an existing directory's engine in place

There is no toggle and no conversion. Pointing the wrong engine at an existing
directory — for example `--storage-engine leveldb` at a Pebble node — is refused
at start with a clear error, rather than failing obscurely or corrupting. That
refusal is deliberate: it protects the reserve.

### Switching a node's engine while keeping its identity

You do **not** need a whole new data directory. A node's identity lives outside
the reserve, in three separate places under the data directory:

- `keys/` — the node's private keys: libp2p identity, swarm overlay key, wallet.
- `statestore/` — addressbook, postage batches, accounting, chequebook, overlay.
- `localstore/` — the reserve: chunk data in sharky plus the index store. **Only
  this is engine-specific, and only this re-syncs.**

So to move a node to the other engine, wipe only the reserve and keep the rest.
`bee db nuke` is built for exactly this:

```bash
# stop bee first
bee db nuke --data-dir <dir>          # wipes the reserve, keeps keys and overlay
bee start --storage-engine pebble     # fresh localstore, bound to pebble
```

By default `nuke` removes the `localstore` (reserve) and the kademlia peer cache
and clears sync-progress state. It does **not** touch `keys/`, and it keeps the
overlay. After this the node is the same node — same overlay address, libp2p
identity, wallet, chequebook, postage batches and addressbook — and it re-syncs
its reserve from scratch on the new engine. Because the overlay is unchanged, it
re-syncs the same neighbourhood it was already responsible for.

Two cautions:

- **Do not pass `--forget-overlay` to `nuke`.** That flag deliberately discards
  the overlay and deploys a new chequebook on next boot, which is the opposite of
  keeping the identity.
- The re-sync costs time and bandwidth to refill the reserve; the identity
  survives instantly, the stored chunks are rebuilt over hours.

### Scope

Only the reserve's index store is the leveldb-or-pebble choice. The `statestore`
stays on goleveldb regardless, so a "Pebble node" is a Pebble reserve index with
a goleveldb statestore and sharky underneath. That is intentional; the statestore
is small and is not what the comparison is about.

A node running an older build that upgrades to this one is unaffected: its index
directory has data but no marker, which is read as goleveldb, and the marker is
written as `leveldb` on first open. No node changes engine unless someone
deliberately starts a fresh reserve on Pebble.
