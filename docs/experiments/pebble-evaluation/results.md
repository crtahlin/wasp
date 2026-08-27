# Results — Pebble evaluated against goleveldb

Issue: [#15](https://github.com/crtahlin/wasp/issues/15) ·
Spec: [`spec.md`](spec.md)

## Verdict: park with the evidence

The criterion written before the numbers existed was "passes the conformance
suite unmodified **and** is not slower on batched writes and prefix iteration".

The suite passes. Prefix iteration is now measurable for the first time and
Pebble is **1.19x slower** at it. Batched writes are split: 8.61x faster in one
benchmark, 1.61x slower in the other.

So the criterion is not met, and the answer is park. Honouring a criterion
written in advance is the entire reason for writing one down, and an 8.61x gain
on batched writes is exactly the sort of number that would otherwise be used to
argue past it.

That is a genuine trade rather than a dismissal: an engine that writes several
times faster and reads modestly slower may well suit a node that ingests
continuously and reads its reserve rarely. Deciding that needs a workload shaped
like a node's, which is the third item under "what would change the verdict"
below — not a microbenchmark.

## Conformance: passes, after two adaptations in the store

`storagetest.TestStore` and `storagetest.TestBatchedStore` pass **unmodified**.
Neither test file was touched, which was the bar.

Two adaptations were needed in `pebblestore` itself, and both are the kind of
difference a port gets wrong by copying rather than by thinking.

**1. Close is not idempotent in Pebble.** `pebble.DB.Close` panics with
`pebble: closed` on a second call; goleveldb returns an error. The shared suite
closes the store itself, so any caller with a deferred `Close` alongside an
explicit one crashes. `Store.Close` guards with a `sync.Once`.

**2. `PrefixAtStart` cannot be ported literally.** `leveldbstore` seeks and then
steps back with `iter.Prev()`, which reads as "start one before the target". It
is not: the `Prev` compensates for its loop calling `Next()` before the first
read, and the net effect is to start *at* the target. Copying the two calls
yields the element before it. The conformance suite caught this
(`iterate_subset_prefix`), which is the argument for using the shared suite
rather than writing a new one.

## Benchmarks

**The numbers first published here were wrong, and are corrected below.** What
they measured is recorded in the next section rather than deleted, because the
way they were wrong is the useful part.

Apple M5 Pro, `-benchtime 200ms -count 3`, median of three. Both engines on
disk, a fresh store per sub-benchmark, 100,000-entry datasets.

**Table — Store operations, goleveldb against Pebble, like for like**

| operation | goleveldb ns | Pebble ns | verdict |
|---|---|---|---|
| `WriteSequential` | 7,637 | 1,499 | **5.09x faster** |
| `WriteInBatches` | 836 | 97 | **8.61x faster** |
| `ReadRandom` | 5,100 | 5,906 | 1.16x slower |
| `IterateSequential` (per entry) | 19.0 ms | 22.7 ms | 1.19x slower |
| `WriteInFixedSizeBatches` | 1,110 | 1,789 | 1.61x slower |
| `ReadSequential` | 1,533 | 5,312 | 3.47x slower |
| `ReadRandomMissing` | 232 | 6,249 | *see below — measures nothing useful* |

Pebble is substantially faster at writing and slower at reading. That is a
coherent result for an LSM tuned differently, and unlike the first attempt it is
a comparison of two engines rather than of memory against disk.

### `ReadRandomMissing` is excluded from the verdict

It does not measure what its name says, in either engine. Hit keys are formatted
`"1%015d"` and missing keys `"0%015d"`, so **every missing key sits below the
entire stored keyspace**. A lookup is rejected by a table-level key-range check
before any bloom filter or data block is consulted.

That is why configuring a bloom filter changes nothing: 4,750 ns with it against
4,748 ns without, over three runs each, on a 100,000-entry dataset. The filter
is never reached. The `DefaultOptions` bloom filter is retained on general
grounds, and the code says plainly that this harness cannot test it.

A node looking up a chunk it does not hold looks up an address *inside* its
keyspace. The 27x gap is the cost of two different range-rejection paths and
says nothing about that case. Filed separately.

## What the first version of this document got wrong

Two independent defects, both of which made the original table meaningless.

**The datasets held one key.** `b.N` reads as **1** before `b.Loop()` runs, and
every generator was sized from `b.N`. Verified directly:

```
b.N before the loop = 1, iterations actually run = 126247629
```

So `WriteSequential` was reporting hundreds of thousands of iterations of
overwriting *the same key* as distinct writes. Nothing failed; the number simply
meant something other than its name.

**The two engines were not both on disk.** `leveldbstore`'s benchmark store was
built with `New("", ...)` — goleveldb's in-memory backend — while
`pebblestore`'s used `b.TempDir()`. Every published figure compared memory
against disk. On the corrected harness the same leveldb `ReadRandom` moves from
226 ns in memory to 9,198 ns on disk: a 40x difference, which was the dominant
term in the original table.

Both are fixed — the first in [#159](https://github.com/crtahlin/wasp/pull/159),
the second here.

The earlier conclusion that the level-0 file count explained the missing-key gap
was reasoning on top of these broken numbers. The 98-files-in-L0 observation was
real, but it was not the explanation, and the actual explanation above is
simpler.

## Two defects in the shared benchmark harness

Both affect `leveldbstore` exactly as much as `pebblestore`. Neither was
introduced here; both were found by trying to use the harness for its purpose.

**1. Seven benchmarks fail outright on Go 1.26.** `B.Loop called with timer
stopped`. The harness calls `b.Loop()` twice in one benchmark — once in
`populate` as setup, once in the measured phase — which was valid under the
`for i := 0; i < b.N; i++` idiom and is not under `b.Loop()`. Broken:
`ReadSequential`, `ReadReverse`, `ReadRedHot`, `DeleteRandom`,
`DeleteSequential`, `WriteRandom`, `DeleteInBatches`,
`DeleteInFixedSizeBatches`.

**2. Sub-benchmarks share one store, so the selection changes the results.**
Running a different subset leaves each benchmark facing a differently populated
store. An early Pebble run measured `ReadRandomMissing` at 141 ns; the same code
under a wider selection measured 1,531 ns. **The 141 ns figure was an artefact
and is recorded here only so nobody rediscovers it and believes it.**

The second defect is the more dangerous one, because it produces plausible
numbers rather than failures.

## What this does not establish

Everything the spec said it would not, and one thing more.

- **Nothing about the reserve at scale.** The workload is synthetic, against an
  empty store, on a laptop. bench-1 holds 4,064,993 chunks.
- **Nothing about compaction under sustained ingest**, which is the behaviour
  that matters most and the one a microbenchmark cannot reproduce.
- **Nothing about prefix iteration**, one of the two operations the spec named
  as deciding. `IterateSequential` and `IterateReverse` run but report no
  `ns/op`, so there is nothing to compare.

## Cost, stated plainly

Importing Pebble promotes it to a direct requirement and adds eleven modules to
`go.mod`'s indirect list that nothing in the build previously needed. It is in
`go.sum` already, so no new source becomes reachable — but reachable and built
are different, and the spec's first draft got that wrong.

## What would change the verdict

In order of what it would cost:

1. **Done** — the harness is fixed ([#146](https://github.com/crtahlin/wasp/issues/146),
   [#159](https://github.com/crtahlin/wasp/pull/159)) and the numbers above are
   the result.
2. **Done** — prefix iteration now reports per-entry cost, and Pebble is 1.19x
   slower. That is the half of the criterion that decides this.
3. **Run both engines under a real reserve**, which is the only setting where
   compaction behaviour appears at all.

The maintenance argument for Pebble is unchanged and remains the strongest point
in its favour: goleveldb has had no commit since July 2022 and the race in
`compTriggerWait` is unfixed at its `HEAD`. That argument does not need a
benchmark, and it is why this is parked rather than closed.
