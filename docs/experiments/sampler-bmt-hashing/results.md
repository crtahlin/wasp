# Results: a synchronous BMT hash for the sampler on non-SIMD nodes

Issue: [#236](https://github.com/crtahlin/wasp/issues/236). Spec:
[`spec.md`](spec.md). Umbrella:
[#234](https://github.com/crtahlin/wasp/issues/234).

**Verdict: on non-SIMD machines the sampler's per-chunk hash is 1.3 to 2.2x
faster and allocates about a third less, with byte-identical output.** The
sampler already runs a pool of chunk-level hashers that saturates the cores, so
the goroutine BMT hasher's extra per-section fan-out was oversubscription. Hashing
sections in the calling goroutine removes it. SIMD nodes are unchanged: they keep
the SIMD hasher, which has no per-section fan-out to remove.

## What changed

- `pkg/bmt/hasher_goroutine.go`: the goroutine hasher gains a `sync` mode that
  hashes each section in the calling goroutine instead of spawning one goroutine
  per section. The BMT root is identical, because the per-node `toggle` assigns
  left and right children regardless of arrival order. The result channel is
  buffered in sync mode so the final in-line section can deliver the root to the
  goroutine that also reads it.
- `pkg/bmt/dispatch_simd.go`, `pkg/bmt/dispatch_other.go`:
  `NewSamplerPrefixHasher` returns the SIMD hasher where SIMD is available and the
  sync goroutine hasher otherwise. Scoped to the sampler: `NewPrefixHasher`, used
  by upload and normal chunk hashing where one hash runs at a time on otherwise
  idle cores, is left on the fan-out, which helps there.
- `pkg/storer/sample.go`: the sampler's hasher pool uses `NewSamplerPrefixHasher`.

No wire, consensus, on-disk, or config surface is touched. The output is
byte-identical, so nothing about how a stock node verifies the sample changes.

## Byte-identical output

`TestSamplerPrefixHasherByteIdentical` (`pkg/bmt/samplerhasher_test.go`) hashes 15
sizes from 0 to 4096 bytes with the fan-out hasher and the sync hasher, reusing one
sync hasher across all sizes to exercise `Reset`, and asserts equal output. It
passes. The existing BMT suite, which checks both hashers against the reference
implementation, still passes, and the storer sample tests pass.

## Measurement

The change is byte-identical and the metric is CPU and allocation, not disk, so a Go
benchmark is the right instrument. `BenchmarkSamplerPattern`
(`pkg/bmt/samplerpattern_bench_test.go`) reproduces the sampler's own parallelism:
GOMAXPROCS parallel workers, each repeatedly hashing a 4 KB chunk with its own
hasher. Run at constrained GOMAXPROCS to model weaker machines. SIMD is forced off so
the comparison is fan-out against sync, the choice a non-SIMD node actually faces.

**Table: 4 KB chunk hash under the sampler's parallel pattern, fan-out versus sync,
Apple Silicon arm64 (a non-SIMD target platform), 3000 iterations per worker**

| GOMAXPROCS | fan-out ns/op | sync ns/op | speedup | fan-out allocs/op | sync allocs/op | fan-out B/op | sync B/op |
|---|---|---|---|---|---|---|---|
| 1 | 40309 | 30996 | 1.30x | 195 | 131 | 5971 | 4435 |
| 2 | 23505 | 16003 | 1.47x | 195 | 131 | 6006 | 4454 |
| 4 | 13937 | 8321 | 1.68x | 195 | 131 | 6077 | 4491 |
| 8 | 13035 | 6027 | 2.16x | 196 | 132 | 6221 | 4567 |

The speedup grows with core count because the fan-out's oversubscription gets worse
the more chunk-level workers are already competing for the cores. Allocations drop by
about a third at every core count, because each avoided goroutine spawn was itself
allocating; the fan-out's per-op allocation and byte counts also drift up with core
count, while the sync hasher's stay flat.

Scaled to a full radius-9 reserve, about 3.79M chunks a round, the allocation drop is
on the order of 240M fewer allocations and several GB less garbage per round on a
non-SIMD node, on top of the 1.3 to 2.2x on the hashing term.

### An isolated benchmark points the other way, and is the wrong model

Hashing a single chunk in isolation, with all other cores idle, the fan-out is faster
at 2 cores and up, because it has spare cores to use. That is not the sampler's
situation: the sampler already fills the cores with chunk-level hashers, so the spare
cores the fan-out would use do not exist. The parallel benchmark above is the model
that matches how the sampler runs; the isolated one overstates the fan-out.

## Scope and what is left

This helps only non-SIMD nodes: all ARM including Raspberry Pi and Apple Silicon, all
Windows, 386, and x86 without AVX2. SIMD nodes, including bench-1 and bench-2, keep
the SIMD hasher and are unchanged, which is why this is measured by benchmark on an
arm64 machine rather than on the amd64 bench nodes.

The sync hasher still allocates about 131 times per chunk, from the per-section keccak
work that both the sync and fan-out paths do. Pooling those keccak hashers to cut the
remaining allocations is a separate change, orthogonal to the goroutine question, and
is left as a follow-up under [#234](https://github.com/crtahlin/wasp/issues/234).

## Upstream

The goroutine-per-section fan-out is upstream's design and is fine for a single hash
on idle cores; it is only redundant for a caller that already saturates the cores, so
this is an optimization for the sampler's access pattern, not an upstream defect. No
`affects-upstream` label. The change ports cleanly: it is confined to `pkg/bmt` and
one line of `pkg/storer/sample.go`, with no wire surface.

Generated with help of AI.
