# Evaluate replacing goleveldb with Pebble

Issue: [#15](https://github.com/crtahlin/wasp/issues/15)

## Why now

[#114](https://github.com/crtahlin/wasp/issues/114) settled a question this issue
had only asserted. goleveldb's `compTriggerWait` closes a channel the compaction
goroutine may still send on; the send is caught by a bare `recover()` inside the
library, so it is not a crash, but it is a data race the detector flags, and it
makes that path untestable under `-race`.

More to the point, it is **not fixed and will not be**. The pin is
`v1.0.1-0.20210819022825-2ae1ddf74ef7`, from August 2021. The current `HEAD` of
`syndtr/goleveldb` is from July 2022. `compTriggerWait` is byte-identical between
the two — checked, not assumed. There is nothing to upgrade to.

An unmaintained storage engine underneath a node that stores other people's data
is the problem; the race is one symptom of it.

## What this spike answers

Two questions, and deliberately no others.

1. **Does Pebble satisfy the interface this codebase actually uses?** Not "is
   Pebble a key-value store" — whether `storage.Store` and `storage.BatchStore`
   can be implemented over it without changing the interface, and whether the
   result passes the same conformance suite `leveldbstore` passes.
2. **What does the same workload measure on both?** Using the benchmarks that
   already exist, run against both implementations on the same machine.

The output is a decision with numbers behind it, not a migration.

## What it does not answer, and must not pretend to

- **No on-disk migration.** Existing nodes hold a goleveldb index store. Moving
  them is a separate and larger problem, and nothing here should be read as
  progress on it.
- **No production wiring.** The spike does not make Pebble selectable by
  operators. A store that has never run on a node has not been evaluated for
  running on a node.
- **No claim about the reserve at scale.** The benchmarks are synthetic and run
  against an empty store. bench-1 holds 4,064,993 chunks, and the behaviour that
  matters most — compaction under sustained ingest — is exactly what a
  microbenchmark does not reproduce. This project has already been misled once by
  a microbenchmark: [#8](https://github.com/crtahlin/wasp/issues/8) improved
  Sharky tenfold in a harness and moved nothing on a node.

## Method

`pkg/storage/pebblestore`, implementing the same surface `leveldbstore` does:
`Get`, `Has`, `GetSize`, `Iterate`, `Count`, `Put`, `Delete`, `Close`, `Batch`.

Conformance is not a new test suite. It is the existing shared one, which is what
makes the comparison meaningful:

```go
storagetest.TestStore(t, store)
storagetest.TestBatchedStore(t, store)
```

If those pass unmodified, Pebble satisfies what this codebase asks of a store. If
they need changes, the changes themselves are the finding, and each one has to be
written down rather than absorbed.

Measurement uses the benchmarks already written against that interface:

```go
storagetest.BenchmarkStore(b, store)
storagetest.BenchmarkBatchedStore(b, store)
```

Both engines, same machine, same run, reported side by side. Three runs minimum
before any ratio is quoted, per `docs/agent-playbooks/test-bench.md`.

Pebble is **already in the dependency graph** at `v1.1.5`, reached indirectly
through go-ethereum, so the spike adds no new module and no new transitive
dependencies. That is worth stating because "adds a dependency" would otherwise
be a reasonable objection, and here it is not one.

## Decision criteria

Stated before the numbers exist, so the numbers cannot be read to fit.

- **Adopt as a goal** if the conformance suite passes unmodified and the
  benchmarks show Pebble is not slower on the operations bee actually performs
  most: batched writes and prefix iteration.
- **Park with the evidence** if it passes but is slower, or if it needs interface
  changes that would ripple into callers. Being maintained is worth something,
  but not any amount of throughput.
- **Reject** if the interface cannot be satisfied without changing
  `storage.Store`, since that would touch every store in the tree.

A tie goes to Pebble, on maintenance alone. That is a judgement stated in
advance, not one discovered afterwards.

## Protocol impact

None. The index store is entirely local; no wire format, no chunk format, no
consensus surface. Nothing here can be observed by a peer.

## Rollout and rollback

Nothing is rolled out. The spike lands as a package with tests and a results
document, wired to nothing. Rollback is deleting the package.
