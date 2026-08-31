# Is there a database more suitable than Pebble for the index store?

Issue: [#185](https://github.com/crtahlin/wasp/issues/185) · Related: [#15](https://github.com/crtahlin/wasp/issues/15)
(Pebble evaluation), [#114](https://github.com/crtahlin/wasp/issues/114)
(goleveldb race), [#176](https://github.com/crtahlin/wasp/issues/176) (write stall)

## Why this survey exists

[#15](https://github.com/crtahlin/wasp/issues/15) evaluated Pebble specifically
and reached "do not adopt yet". Before standing up a second machine to test
Pebble under a real reserve, the question worth answering first is the one that
evaluation did not: **is Pebble even the right alternative, or is there a
database better suited to this workload?**

goleveldb, the incumbent, is the reason to look. Its last substantive commit was
July 2022; as of this survey it has 86 open issues and 25 open pull requests, and
the `compTriggerWait` race ([#114](https://github.com/crtahlin/wasp/issues/114))
is present and unfixed in the pinned version. An unmaintained storage engine
under a node that stores other people's data is the problem; the race and the
write stall ([#176](https://github.com/crtahlin/wasp/issues/176)) are symptoms.

## The constraint that eliminates most candidates

The build sets `CGO_ENABLED=0` on both the `binary` and `build` Make targets, and
the release cross-compiles to linux, windows and darwin across amd64, 386, arm64
and armv7. **The engine must be pure Go.** This removes, up front, the databases
usually reached for as "better":

- **RocksDB** (and the `gorocksdb`/`grocksdb` bindings) — CGO.
- **LMDB** — CGO.
- **SQLite-based** stores (including most "embedded SQL" options) — CGO, unless
  using a pure-Go SQLite transpile whose performance and maturity do not suit a
  storage-engine hot path.

So the field is pure-Go stores only, which is a small and well-known set.

## What the index store actually demands

The index store is not a general database; it has a specific shape, and the
right engine is the one that fits it, not the one that wins a generic benchmark.

- **Ordered iteration with prefix bounds.** The reserve iterates bins and ranges
  by key prefix. This is load-bearing, and it is where Pebble was slower in #15.
  A store without efficient ordered range scans is disqualified outright.
- **Concurrent batched writes.** Each synced chunk is roughly five to six index
  writes inside one transaction, and there are many syncing peers at once
  (about two goroutines per peer per bin). The engine must take concurrent
  writers well; a single-writer design serialises the whole sync path.
- **Concurrent point reads under that write load.** `ReserveHas` runs on the hot
  pullsync path at the same time.
- **Small keys and small values.** The chunk payload lives in sharky, not here.
  The index store holds small index entries. This matters: it rules *out* the one
  design that most distinguishes an alternative (see Badger below).
- **Deletes.** Eviction removes entries.
- **Scale.** A full reserve is millions of chunks; the index is tens of
  gigabytes and grows with the reserve-capacity work ([#17](https://github.com/crtahlin/wasp/issues/17)).
- **Survives write-heavy historical sync.** The compaction behaviour under a
  catch-up sync is the whole pain point, and the axis the A/B must measure.

## The candidates, and why most fall away

Surveyed against the pure-Go filter and the demands above (landscape cross-checked
against the curated `awesome-go-storage` list, August 2026):

| Store | Architecture | Verdict |
|---|---|---|
| **goleveldb** | LSM | The incumbent and the control. Unmaintained since July 2022. |
| **Pebble** | LSM (RocksDB-derived) | **Primary candidate.** See below. |
| **Badger** | LSM + value log | Rejected for this workload. See below. |
| **bbolt** | B+tree | Read-optimised wildcard; likely wrong for write-heavy sync. See below. |
| bitcask | hash index + WAL | Rejected: hash index, no efficient ordered range scans. |
| pogreb | hash index | Rejected: read-heavy append-only, hash-indexed, not ordered. |
| buntdb | in-memory + AOF | Rejected: in-memory model, not for a tens-of-GB reserve. |
| rosedb, LotusDB, nutsdb | young LSM / hybrid | Rejected for now: not proven at this scale, and each would need a store adapter written from scratch. Betting a bench machine and a new adapter on unproven code is the wrong risk. |

### Pebble — the primary candidate

Actively maintained (it is CockroachDB's production storage engine), pure Go, an
LSM that mirrors RocksDB's structure. It already has a conformant `pebblestore`
here that passes the same `storagetest.TestStore` / `TestBatchedStore` suites
goleveldb does, so the build cost is low: parity work (metrics, options, the
write-stall health signal), not a new store.

The #15 microbenchmarks are the reason to test it on a real node rather than
adopt or reject it now: much faster on writes (5–8x on sequential and batched),
but slower on reads (1.16–3.47x) and 1.19x slower on prefix iteration. Two of
those are exactly the index store's hot paths, and microbenchmarks on a laptop do
not capture compaction behaviour under a filling reserve — which is the setting
where goleveldb actually hurts. That is what the real-reserve A/B is for.

### Badger — rejected for this workload

Badger's distinguishing design is key/value separation (WiscKey): values live in
a separate log, keys in the LSM. That reduces write amplification **for large
values**, by keeping big payloads out of the tree. The index store's values are
small — the large payloads are already elsewhere, in sharky. So Badger's one real
advantage does not apply here, while its costs do: value-log garbage collection,
a larger memory footprint, and more operational moving parts. It would also need
a store adapter written and made to pass the conformance suite. Rejected: it does
not fit the shape of this workload, and there is no evidence it would beat Pebble
on it to justify the build cost.

### bbolt — the read-optimised wildcard, deferred

bbolt is a B+tree, very actively maintained (etcd depends on it), with excellent
read and ordered-iteration performance — the very axes Pebble lost on in #15. But
it allows only one read-write transaction at a time, so it serialises the whole
concurrent write path, and a B+tree suffers heavy random-write amplification. For
a write-heavy sync workload with many concurrent writers, that is very likely
disqualifying. It is worth naming as the read-optimised contrast, and it could be
a secondary A/B arm if the write penalty turns out smaller than expected, but it
needs a from-scratch adapter and the prior is against it. Not the primary.

## Recommendation

**Test Pebble against goleveldb under a real reserve. Keep goleveldb as the
control and the default.** The honest finding of the survey is that the pure-Go
constraint, plus a write-heavy workload that also needs prefix iteration and
concurrent writers, leaves Pebble as the best-available maintained alternative —
and the only one with an existing conformant adapter. The alternatives were not
dismissed by assumption:

- Badger's headline feature is for large values the index store does not hold.
- bbolt's single-writer B+tree is the wrong shape for concurrent write-heavy sync.
- The younger stores are unproven at this scale and would each cost a new adapter.

So this is not "Pebble by default"; it is "Pebble because the survey eliminated
the alternatives on their merits." If the A/B confirms Pebble is not better under
a real reserve, the conclusion is not "try the next database" — it is that the
pure-Go field has no clearly better option today, and the effort should go to the
goleveldb-contention work ([#23](https://github.com/crtahlin/wasp/issues/23),
[#28](https://github.com/crtahlin/wasp/issues/28),
[#29](https://github.com/crtahlin/wasp/issues/29)) instead.

## Shortlist for the A/B

1. **goleveldb** — control, current default, bench-1's existing reserve.
2. **Pebble** — primary treatment, on bench-2 with a fresh reserve.
3. *(optional, deferred)* **bbolt** — a read-optimised third arm, only if the
   from-scratch adapter is judged worth it after the first comparison.

## What would change this recommendation

- A pure-Go LSM reaching production maturity and adoption (rosedb, LotusDB, or a
  newcomer) with evidence on a write-heavy, small-value, prefix-scan workload.
- Evidence that Badger's non-value-log path is competitive for small values.
- A decision to relax `CGO_ENABLED=0`, which would reopen RocksDB and LMDB — but
  that trades the static build and five-platform cross-compilation for it, which
  is a much larger change than swapping a pure-Go engine.

Sources for the landscape and maintenance status: the curated
[awesome-go-storage](https://github.com/gostor/awesome-go-storage) list, the
[goleveldb commit history](https://github.com/syndtr/goleveldb/commits/master)
(last substantive commit July 2022), and the #15 Pebble evaluation in this
repository.
