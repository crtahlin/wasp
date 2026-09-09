# Cut per-chunk allocations and goroutine churn in the sampler's BMT hashing

Issue: [#236](https://github.com/crtahlin/wasp/issues/236). Umbrella:
[#234](https://github.com/crtahlin/wasp/issues/234).

## Problem

The reserve sampler computes a keyed BMT hash over every chunk in the
committed-depth neighborhood, about 3.79M chunks per round on a full radius-9
reserve (`transformedAddress`, `pkg/storer/sample.go:511`). It runs a pool of
`workers = max(4, runtime.NumCPU())` hashers, each owning its own
`bmt.NewPrefixHasher(anchor)`.

Which BMT implementation that hasher is depends on the build and CPU
(`pkg/bmt/dispatch_simd.go`, `pkg/bmt/dispatch_other.go`):

- **linux/amd64 with AVX2**: the SIMD hasher (`pkg/bmt/hasher_simd.go`). `Write`
  copies into a preallocated buffer and the per-chunk cost is close to the output
  digest alone. This path is already lean.
- **everything else**: the goroutine hasher (`pkg/bmt/hasher_goroutine.go`). This
  is selected for any build that is not linux/amd64, which is all ARM (including
  Raspberry Pi and Apple Silicon), all Windows, and 386, plus linux/amd64 without
  AVX2.

On the goroutine path, `Write` spawns one goroutine per 64-byte section
(`go h.processSection(i, false)`, `hasher_goroutine.go`), up to about 64 goroutines
per 4 KB chunk. The sampler already parallelizes at the chunk level with its hasher
pool, so this adds a second layer of per-section goroutines on top: on the order of
hundreds of millions of goroutine spawns per round, each with stack, scheduling, and
channel overhead, plus per-hash allocations. The origin storage-layer briefing
measured about 194 allocations and 5.9 KB per chunk on this path, roughly 813M
allocations and 25 GB of garbage per round. That figure predates the SIMD split and
is treated here as a claim to re-measure, not a fact.

Two more per-chunk allocations are visible in the source regardless of hasher:
`transformedAddressCAC` returns `hasher.Sum(nil)`, allocating a fresh output slice,
and `transformedAddressSOC` allocates a new `swarm.NewHasher()` for every
single-owner chunk (`sample.go:534`).

### Why a fast machine hides this

A machine with AVX2 and an NVMe disk runs the lean SIMD path, so it shows none of
this. The goroutine path runs on the weaker hardware, and that is exactly the
hardware where the sampler is already the term that can push a node past the
commit-phase deadline and exclude it from the redistribution game. The aim is to cut
per-round CPU, allocations, and goroutine churn on these machines, independent of
whether a fast machine has wall-clock headroom.

## Hypothesis

Inside the sampler, which already saturates the cores with chunk-level hashers, the
BMT hasher's goroutine-per-section fan-out is not a parallelism gain but
oversubscription: on a few-core machine it adds scheduling and allocation overhead
for no benefit, and on a many-core machine the chunk-level pool already uses the
cores. A synchronous, goroutine-free BMT hash path used by the sampler's hasher pool,
together with pooling the output digest and reusing the per-SOC keccak hasher, will
cut allocations, garbage collection, and CPU on non-SIMD nodes. Because the
transformed address is byte-identical, this is a pure efficiency change.

## Design

1. **Measure first.** Benchmark `transformedAddressCAC` and `transformedAddressSOC`
   on both hasher implementations on the current tree: allocations per op, bytes per
   op, and nanoseconds per op, including a low-GOMAXPROCS run that stands in for a
   weak machine. This confirms or corrects the 194-alloc figure and the
   goroutine-per-section cost before any change.
2. **Synchronous BMT path for the sampler.** Evaluate a goroutine-free hash that
   processes sections in the calling goroutine, compared against the
   `pkg/bmt/reference` implementation for correctness. Scope it so it does not change
   the goroutine hasher used elsewhere (upload, normal chunk hashing): either a
   distinct synchronous hasher the sampler requests, or a bounded fan-out. The global
   hasher's behaviour on other paths must not change without its own measurement.
3. **Pool the small per-chunk allocations.** Reuse the `Sum` output buffer and reuse
   one keccak hasher per sampler hasher goroutine for the SOC path, rather than
   allocating per chunk.

The change is byte-identical in output, so it can be validated against the existing
hasher by hashing the same chunks both ways and comparing.

## Protocol impact

None. The transformed address is byte-identical; only the allocation and goroutine
strategy changes. No wire, consensus, or on-chain verification surface is touched.
This does not go near `.github/protocol-freeze.lock`.

## Measurement

Rule 7: at least three runs per condition, reported with the spread, per chunk.

- **Primary (local, no node needed):** Go allocation and timing benchmarks before and
  after on both hasher paths, at GOMAXPROCS matching a weak machine and at full
  cores. Report allocations per op, bytes per op, and nanoseconds per op. A synthetic
  benchmark is enough here because the change is byte-identical and the metric is
  allocation and CPU, not disk.
- **Confirming (node), if a non-SIMD machine can be provisioned:** wall-clock and
  garbage-collection comparison over real rounds, read from the "reserve sampler
  finished" log or `reserve_sample_duration_seconds`, never the `/rchash` HTTP
  duration, which wraps proof generation and a chain call. Match node state across the
  comparison.
- A negative result is stated in advance and honoured: if the synchronous path does
  not reduce allocations and CPU on the goroutine platform, or regresses timing, the
  change is not made and the finding is recorded.

## Rollout and rollback

Pure code change, no config surface, no on-disk or wire effect, so it is revertable by
a single commit. If measurement shows the synchronous path helps few-core machines but
regresses on a many-core non-SIMD machine, the fallback is a bounded fan-out rather
than fully synchronous, chosen by the measured crossover, not by assumption.

## Upstream portability

Clean. The change is in `pkg/bmt` and `pkg/storer`, no wire surface, and ports across
an upstream sync without conflict. The goroutine-per-section fan-out is upstream's own
design, but it is a design choice for a different access pattern, not a defect, so no
`affects-upstream` label: the sampler's chunk-level parallelism is what makes the
per-section fan-out redundant here, and that is specific to how the sampler uses the
hasher.

## Configuration

None. This is a code efficiency change, not a tuning constant, so it adds no config
option. If measurement surfaces a machine-dependent crossover that must be operator
tunable, that becomes its own issue under rule 8, with a measured default.

Generated with help of AI.
