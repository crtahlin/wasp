# Results — Pebble evaluated against goleveldb

Issue: [#15](https://github.com/crtahlin/wasp/issues/15) ·
Spec: [`spec.md`](spec.md)

## Verdict: park with the evidence

Against the criteria written before the numbers existed, the deciding one was
"passes the conformance suite unmodified **and** is not slower on batched writes
and prefix iteration".

The suite passes. The rest cannot be answered with the harness this repository
has, and the numbers that do exist point in both directions at once. Parking is
what the spec says to do in that case, and the evidence is below so the next
attempt starts from it rather than from scratch.

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

Apple M5 Pro, `-benchtime 300ms -count 3`, median of three. Both engines run the
**same benchmark selection**, which matters — see the harness defects below.

**Table — Store operations, goleveldb against Pebble, same selection and machine**

| operation | goleveldb ns/op | Pebble ns/op | ratio |
|---|---|---|---|
| `WriteSequential` | 369.2 | 976.4 | **2.6x slower** |
| `ReadRandom` | 226.1 | 219.3 | 1.03x faster |
| `ReadRandomMissing` | 199.9 | 1531 | **7.7x slower** |
| `WriteInBatches` | 749.4 | 87.8 | **8.5x faster** |
| `WriteInFixedSizeBatches` | 243.8 | 927.5 | **3.8x slower** |

Two batched-write benchmarks disagreeing by more than an order of magnitude in
opposite directions is the clearest signal here, and it is a signal about the
benchmarks rather than about the engines.

### The missing-key result is about level 0, not about Pebble

`ReadRandomMissing` at 7.7x looked like a missing bloom filter. Pebble sets
filters per level and applies none unless asked, so a zero `pebble.Options` has
none at all.

Adding one changed nothing: **1,531 ns with the filter against 1,539 ns
without**, same selection, same machine.

The reason is that the benchmark never builds a level for a filter to help with.
One million entries leave **98 files in level 0 and none below it**. Level-0
files overlap, so a point lookup consults many of them whatever their filters
say. goleveldb compacts level 0 far sooner — its trigger is 4 files — so its
misses are cheap.

So the number measures *level-0 depth during sustained ingest with no idle
time*, which is a benchmark artefact here and is precisely the behaviour
[#24](https://github.com/crtahlin/wasp/issues/24) found is not reachable on a
real node at real ingest rates. It should not be read as "Pebble is slow at
missing keys".

The filter is kept in `DefaultOptions` on reasoning — a settled reserve has data
below level 0, where it does pay — and the code says plainly that this is an
argument the harness cannot test.

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

1. **Fix the harness.** Until seven benchmarks run, no comparison here is more
   than indicative. Filed separately; it is an upstream defect.
2. **Measure prefix iteration**, since it is half the deciding criterion and is
   currently unmeasurable.
3. **Run both engines under a real reserve**, which is the only setting where
   compaction behaviour appears at all.

The maintenance argument for Pebble is unchanged and remains the strongest point
in its favour: goleveldb has had no commit since July 2022 and the race in
`compTriggerWait` is unfixed at its `HEAD`. That argument does not need a
benchmark, and it is why this is parked rather than closed.
