# beebench

Benchmarks for the Bee storage layer. A standalone Go module that measures the
code in this repository without modifying it, through a `replace` directive
pointing at the parent module.

It exists so that storage-layer changes are accepted on evidence rather than on
the code looking faster. See
[`docs/analysis/storage-layer-briefing-2026-01.md`](../docs/analysis/storage-layer-briefing-2026-01.md)
for what has already been measured, including the claims that did **not**
survive measurement.

## Running

```bash
cd beebench

# Micro-benchmarks: seconds, no corpus needed.
go test -run '^$' -bench 'BenchmarkKey|BenchmarkItem|BenchmarkTransformed' -benchtime 3s -count 3

# Corpus-based benchmarks: build a corpus first. These are slow and large.
go test -run '^$' -bench BenchmarkSharkyVsPread -benchtime 10x
```

**Use `-count 3` or more and report the spread, not a single figure.** A single
run of anything I/O-adjacent measures the machine's mood.

## Files

| File | Measures |
|---|---|
| `harness_test.go` | Corpus construction, shard file access, timed concurrent runner |
| `concurrency_test.go` | Sharky against direct `pread`, page-cache resident |
| `bigconc_test.go` | Sharky against direct `pread`, device-bound, large corpus |
| `order_test.go` | Random against physical-order reads |
| `writers_test.go` | Background write load generator |
| `ldb_test.go` | LevelDB block cache sweep |
| `micro_test.go` | Key encoding, item unmarshalling, BMT transform |

## Two traps that cost the original author time

1. **Reading the same locations twice** makes the second pass page-cache
   resident regardless of flags. Use disjoint location sets, or warm
   deliberately before measuring.
2. **`F_NOCACHE` on macOS does not bypass the page cache** for the unaligned
   reads used here. Building a corpus larger than RAM is the reliable way to
   force reads to reach the device. Slot size 4201 is not a multiple of the
   4096-byte page size, so every chunk read spans two pages.

## What these numbers are and are not

Measurements to date were taken on an Apple M5 Pro with NVMe on APFS.
Production nodes run Linux, usually on ext4, often on slower storage.

**Absolute throughput does not transfer.** Ratios between two code paths
measured on the same machine transfer more reliably, and that is the only form
in which results should be quoted. No mechanical drive has been tested, which
matters most for the physical-ordering work.

## Upstream drift

This harness was written against upstream commit `3e157a04` (2026-01-03) and
adapted when imported here at `v2.8.1`. Two upstream API changes had to be
absorbed:

- `leveldbstore.New` gained a third return value reporting whether the previous
  shutdown was unclean.
- `bmt.NewHasher(func() hash.Hash {...})` became `bmt.NewPrefixHasher(anchor)`,
  and `Hash` became `Sum`. Upstream also added SIMD dispatch
  (`pkg/bmt/dispatch_simd.go`).

Expect more of this after each upstream sync. When a benchmark stops compiling,
the fix is to mirror what the production code now does — a benchmark measuring
a shape the code no longer has is worse than no benchmark.

## Reproduced at v2.8.1

**Table — Micro-benchmarks re-run on this fork's v2.8.1 base, Apple M5 Pro, 3s × 3**

| Benchmark | Briefing (`3e157a04`) | Measured at `v2.8.1` |
|---|---|---|
| `Key_Current` | 45.17 ns/op, 224 B, 2 allocs | 47.1–48.2 ns/op, 224 B, 2 allocs |
| `TransformedAddressCAC` | 26,119 ns/op, 5,924 B, 194 allocs | 27,161–27,766 ns/op, 5,952 B, 195 allocs |
| `TransformedAddressCAC` throughput | 157 MB/s | 148–151 MB/s |

The briefing's figures reproduce within a few percent, so its conclusions hold
on this base. In particular the BMT allocation problem is unchanged — 195
allocations per chunk — despite upstream adding SIMD dispatch in the meantime.
