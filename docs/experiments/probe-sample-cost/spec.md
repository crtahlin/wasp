# Measure the probe-based sublinear reserve sample cost at various k

Issue: [#241](https://github.com/crtahlin/wasp/issues/241). Umbrella:
[#234](https://github.com/crtahlin/wasp/issues/234). Follows the reserve-size-independent
sampling study ([#235](https://github.com/crtahlin/wasp/issues/235)).

## Problem

The reserve sampler costs O(reserve-within-radius), one loaded-and-hashed chunk per chunk
the node holds within committed depth, which is 2M to 4M chunks and about 40 to 180
seconds on the bench. [#235](https://github.com/crtahlin/wasp/issues/235) established that
this linear cost is the density-based proof of resources, and sketched a probe-based
alternative that would be O(k): derive k probe addresses from an anchor, and for each find
and hash the nearest held chunk. The nearest-neighbour distance at k random probes
estimates local density, hence reserve size, without a full pass. The estimate that this
is 100 to 1000x faster is analytical and untested. This experiment measures the real cost
curve.

## Hypothesis

The probe computation costs about O(k) and is far cheaper than the full sample for small
k, but each probe is a random index seek and a random chunk read, which is more expensive
than one sequential per-chunk read in the full sample. So the real speedup is N/k tempered
by that random-versus-sequential ratio, and cold behaviour (page cache dropped) will
differ from warm. There may be a k beyond which the probe approach loses its advantage.
The measurement finds the numbers and the crossover; it neither assumes nor confirms the
estimate.

## Design

Measurement-only. Nothing here is wired into the redistribution game; changing the
sampling rule is a consensus change a fork cannot make (#235). This is a benchmark like
`/rchash` that runs the alternative on the node's real reserve.

- **`(*DB).ProbeSample(ctx, anchor []byte, committedDepth uint8, k int) (ProbeStats, error)`**
  in `pkg/storer/probesample.go`. For i in 1..k:
  - Derive probe_i inside the node's neighbourhood: the node overlay's top
    `committedDepth` bits, the rest from `keccak(anchor || i)`, so the probe lands among
    stored chunks.
  - Seek the retrieval index to probe_i, lock-free, through
    `db.Storage().IndexStore().Iterate(storage.Query{Factory: new RetrievalIndexItem,
    Prefix: string(probe[:]), PrefixAtStart: true}, fn)`, taking the immediate successor
    (the minimal faithful probe; an optional short window with an explicit XOR-nearest
    pick can refine it, at a small extra read cost, and is recorded separately if used).
  - Load that chunk with `db.ChunkStore().Get(ctx, winner.Address)`.
  - Compute its transformed address with `bmt.NewPrefixHasher(anchor)` and
    `transformedAddress`, the same per-chunk work the real sampler does, so per-unit cost
    is comparable.
  - Accumulate `ProbeStats`: total duration, k, seeks, LocateDuration, ChunkLoadDuration,
    TaddrDuration, ChunkLoadFailed.
- **`/probesample/{depth}/{anchor}/{k}` endpoint** in `pkg/api/probesample.go`, registered
  in `pkg/api/router.go` next to `/rchash`. It brackets the single call with
  `runtime.ReadMemStats` to capture Mallocs, TotalAlloc, and NumGC deltas, records
  `ReserveSizeWithinRadius`, `StorageRadius`, and `CommittedDepth`, logs a
  "probe sample finished" line with the full stats, and returns them as JSON. `ProbeSample`
  and an index-store accessor are added to the api `Storer` interface (`pkg/api/api.go`);
  `localStore` (the concrete `*storer.DB`) already satisfies them.
- A unit test that `ProbeSample` returns k results on a seeded reserve and their
  transformed addresses match the real hasher for the same chunks.

## Protocol impact

None. This adds a read-only benchmark endpoint and a storer method. It is not wired into
the redistribution agent, changes no commitment, proof, wire, or on-chain surface, and
does not go near `.github/protocol-freeze.lock`. A node's game behaviour is unchanged.

## Measurement

Rule 7: at least three runs per condition, reported with the spread, matched node state.

- Baseline: `/rchash` (the full sample) on the same node and condition.
- Probe: `/probesample` at k in {16, 64, 256, 1024, 4096, 16384}.
- Conditions: warm, and cold with the OS page cache dropped before each run, since the
  probe does random reads and cold is the honest weak-machine case. bench-1 (leveldb) and
  a confirming run on bench-2 (pebble); note the engine.
- Read from the "probe sample finished" log line, not the HTTP duration alone.
- Record per run: wall time, chunks touched, the seek/load/hash split, allocations, bytes,
  GC cycles, and N = reserveSizeWithinRadius. Report per-probe cost, speedup versus the
  full sample at each k, whether cost scales as O(k), the random-read penalty per probe,
  and the crossover.

A negative or tempered result is stated in advance and honoured: if random-read cost erases
the advantage past some k, or cold behaviour undercuts it, that is the finding.

## Configuration

None. This is a benchmark endpoint, not a tuning constant, so it adds no config option.

## Upstream portability

Not applicable as a production change. The code is a self-contained storer method and a
benchmark endpoint in `pkg/storer` and `pkg/api`, with no wire surface; it ports across an
upstream sync without conflict but is not intended for upstream, since the algorithm it
measures would be a Swarm protocol change, not a client change (#235). No
`affects-upstream` label.

Generated with help of AI.
