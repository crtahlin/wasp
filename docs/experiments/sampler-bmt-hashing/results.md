# Results: a synchronous BMT hash for the sampler does not help on a node

Issue: [#236](https://github.com/crtahlin/wasp/issues/236). Spec:
[`spec.md`](spec.md). Umbrella:
[#234](https://github.com/crtahlin/wasp/issues/234).

**Verdict: neutral, the change was not adopted.** A synchronous BMT hash for the
sampler was built and shown byte-identical and correct, and it is 1.3 to 2.2x faster
with about a third fewer allocations in an isolated microbenchmark. On a real node it
does none of that: no wall-clock improvement and no allocation or garbage-collection
reduction, at 8 cores or at 2, with SIMD off. The microbenchmark did not translate,
for an understood reason, so the production change is not worth its surface and was
dropped. This document records the finding so it is not re-derived.

## What was tried, and why

The sampler computes a keyed BMT hash over every chunk in the committed-depth
neighborhood, about 3.79M chunks per round. On machines without AVX2 (all ARM
including Raspberry Pi and Apple Silicon, all Windows, 386, and x86 without AVX2) the
goroutine BMT hasher spawns one goroutine per 64-byte section, up to about 64 per
chunk. The sampler already runs a pool of chunk-level hashers that saturates the
cores, so the idea was that this per-section fan-out is oversubscription, and hashing
sections in the calling goroutine instead would be faster and lighter on those
machines. A `sync` mode was added to the goroutine hasher and wired into the sampler
through a `NewSamplerPrefixHasher` that keeps the SIMD hasher on SIMD platforms.

The change is byte-identical (the per-node toggle assigns left and right children
regardless of arrival order), verified by test and by an independent review with a
20,000-iteration randomized reuse stress test under the race detector.

## The isolated microbenchmark did look like a win

A benchmark reproducing the sampler's parallel pattern, GOMAXPROCS workers each
hashing a 4 KB chunk in a tight loop with its own hasher, SIMD off, on arm64:

| GOMAXPROCS | fan-out ns/op | sync ns/op | speedup | fan-out allocs/op | sync allocs/op |
|---|---|---|---|---|---|
| 1 | 40309 | 30996 | 1.30x | 195 | 131 |
| 2 | 23505 | 16003 | 1.47x | 195 | 131 |
| 4 | 13937 | 8321 | 1.68x | 195 | 131 |
| 8 | 13035 | 6027 | 2.16x | 196 | 132 |

Faster at every core count and about a third fewer allocations. This is what made the
change look worth shipping.

## On a node it does nothing

Measured on bench-1, a full radius-9 leveldb reserve (~3.79M chunks), SIMD forced off
so the goroutine path runs, three `rchash` runs per condition. The old (fan-out) and
new (sync) binaries were compared at 8 cores and, restricting the process to 2 CPUs
with a systemd `CPUAffinity` that `runtime.NumCPU` honours, at 2 cores.

**Table: full reserve sample wall time, bench-1, SIMD off**

| Condition | 8 cores | 2 cores |
|---|---|---|
| old, fan-out | 62.2s (62.0 / 62.0 / 62.4) | 191 / 187 / 224s |
| new, sync | 62.4s (62.5 / 61.7 / 62.9) | 194 / 190 / 191s |
| new, SIMD on (control) | 31.5s | n/a |

**Table: per-sample allocation and garbage-collection load, bench-1, SIMD off,
mallocs and GC-cycle counters before and after three samples, divided by three**

| Condition | mallocs / sample | alloc bytes / sample | GC cycles / sample |
|---|---|---|---|
| old, fan-out, 8 cores | 991 M | 55.9 GB | 375 |
| new, sync, 8 cores | 1011 M | 57.1 GB | 370 |
| old, fan-out, 2 cores | 996 M | n/a | 363 |
| new, sync, 2 cores | 1036 M | n/a | 377 |

No wall-clock improvement at either core count. No allocation reduction: the sync run
allocates about 2 to 4 percent more, not a third less, which is within noise. No GC
reduction. The SIMD-on control confirms the SIMD path is untouched (31.5s is the normal
SIMD range) and that SIMD is itself the roughly 2x lever here, independent of this
change.

## Why the microbenchmark did not translate

Two reasons, both about the difference between a tight loop and a pipeline.

- **The oversubscription is masked.** The microbenchmark hashes in a tight loop, so the
  per-section goroutines are always runnable and the scheduling penalty bites in full.
  The sampler's hashers are fed chunks by a separate pool of disk readers over a
  channel, so they spend time waiting for chunks rather than hashing continuously. The
  fan-out's extra goroutines are not always runnable, and the penalty hides behind the
  read wait. This held at 2 cores as well as 8.
- **The allocation difference was a tight-loop artifact.** The 64-allocation gap in the
  microbenchmark came from goroutine-related heap churn that a sustained flat-out spawn
  loop forces. On a steadily running node the scheduler's goroutine pool stays warm, so
  the fan-out does not heap-allocate per goroutine, and both paths allocate essentially
  the same. On the node, hashing is only about a third of the roughly 56 GB a sample
  allocates; the rest is chunk loading, index lookups, and stamp handling, which this
  change does not touch. So even a real hasher-level saving would be a minority of the
  total.

## Decision

Not adopted. The change is correct and safe but earns nothing measurable on real
hardware, and shipping code that does not matter is worse than not shipping it. The
production edits were dropped; only this record and the spec remain. This mirrors the
sharky-concurrent-reads result ([#8](https://github.com/crtahlin/wasp/issues/8)),
where a large harness gain did not survive the node.

A genuinely weak machine also has a slow disk and slow cores together, which bench-1
with restricted CPUs does not reproduce, so a difference there cannot be ruled out
absolutely; but nothing in these measurements, including the 2-core runs, suggests one.
The more promising direction under the umbrella is the reserve-size-independent
sampling study ([#235](https://github.com/crtahlin/wasp/issues/235)), which attacks the
cost at its root rather than shaving the hasher.

Generated with help of AI.
