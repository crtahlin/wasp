# Bee storage layer: optimization briefing

A handoff document recording what the storage layer does today, which performance
claims were verified by measurement, which were refuted, and where the remaining
opportunities are.

> **Provenance and verification**
>
> Written against `ethersphere/bee` commit `3e157a04` (2026-01-03).
>
> Every anchor in this document was re-checked against this fork's base,
> **upstream `v2.8.1`**, on 2026-08-21. All of the constructs it describes still
> exist and all of the constants it cites are unchanged. Line numbers have
> drifted by up to five in `pkg/storer/sample.go` and
> `pkg/storage/leveldbstore/store.go`; every other reference is exact. The two
> claims that carry the top-priority recommendation were confirmed verbatim:
> `shard.process()` is still a single goroutine handling reads and writes in one
> `select` loop with blocking I/O inline, and `Store.Read` still contains the
> deliberate context-ignoring select with its issue #2932 comment.
>
> The measurements were taken on an Apple M5 Pro with NVMe and APFS. Absolute
> throughput does not transfer to a Linux production node on ext4. Ratios between
> two code paths measured on the same machine transfer more reliably. No
> mechanical drive was tested.

Generated with help of AI.

---

## 1. Architecture as it exists today

Bee stores chunks in two separate systems joined by an application-level
transaction layer.

**Sharky** (`pkg/sharky/`) holds chunk payloads in 32 fixed-size shard files.

| Property | Value | Source |
|---|---|---|
| Shard count | 32 | `pkg/storer/storer.go:204` |
| Slot size | 4201 bytes (`SocMaxChunkSize`) | `pkg/swarm/swarm.go:33` |
| Typical stored chunk | 4104 bytes (`ChunkWithSpanSize`) | `pkg/swarm/swarm.go:30` |
| Default reserve capacity | 4,194,304 chunks (`1 << 22`) | `pkg/storer/storer.go:251` |

A full reserve therefore occupies approximately 17 GB of chunk data.

**LevelDB** (`goleveldb`, via `pkg/storage/leveldbstore/`) holds all indexes. About
15 namespaces share a single database instance, including `retrievalIdx`,
`batchRadius`, `chunkBin`, `stampIndex`, `chunkstamp`, `cacheEntry`,
`cacheOrderIndex`, `UploadItem`, and `pinCollectionItem`. Configuration is set at
`pkg/storer/storer.go:266-272`, with a 32 MB block cache and a 32 MB write buffer
by default (`pkg/storer/storer.go:246-247`).

Reading one chunk requires two I/O operations: a LevelDB lookup of
`RetrievalIndexItem` to obtain a 7-byte `Location{shard, slot, length}`, followed
by a positional read from the corresponding shard file.

The presence of `pkg/storer/recover.go`, `compact.go`, `validate.go`, and a
migration framework indicates that keeping the two stores consistent without a
shared write-ahead log has required ongoing repair tooling.

---

## 2. Verified findings

### 2.1 Sharky serializes I/O per shard

`shard.process()` (`pkg/sharky/shard.go:99`) is a single goroutine per shard. It
runs a `select` loop and calls blocking `file.ReadAt` and `file.WriteAt` inline.
Each shard therefore has one outstanding I/O operation at a time, giving the node
a maximum of 32 concurrent operations. Reads and writes share this queue, so a
read waits for any write already in progress on the same shard.

`Store.Read` (`pkg/sharky/store.go:115`) contains a `select` on the result channel
that deliberately ignores context cancellation, with a comment explaining that
draining the channel is required to avoid a deadlock (issue #2932). Any change to
this channel protocol must account for that history.

### 2.2 The sampler couples I/O and hashing in the same goroutine

`ReserveSample` (`pkg/storer/sample.go`) runs `max(4, runtime.NumCPU())` workers.
Each worker calls `db.ChunkStore().Get()`, which performs a LevelDB lookup and a
Sharky read, then computes the transformed address with `transformedAddress()` in
the same goroutine.

Two consequences follow. First, each worker spends most of its time blocked on
disk. Second, the sampler offers only `NumCPU` concurrent read requests, which is
below the level at which the Sharky change in section 2.1 begins to matter.

Phase 1 iterates `chunkBin` in bin order, which is uncorrelated with physical
position on disk, so phase 2 issues random reads.

### 2.3 Index size against block cache

A LevelDB database populated with 4,194,304 `retrievalIdx` entries occupies
**213 MB on disk**, measured directly. The default block cache is 32 MB.

---

## 3. Measurements

Test environment: Apple M5 Pro, 15 cores, 48 GB RAM, Apple NVMe SSD, macOS with
APFS, Go 1.26.4.

### 3.1 Sharky compared to direct pread, device-bound

Corpus of 67 GB, which exceeds RAM, so reads reach the storage device.

**Table — Sharky against direct pread, 67 GB corpus, device-bound, Apple M5 Pro NVMe**

| Goroutines | Sharky (reads/s) | Direct pread (reads/s) | Ratio |
|---|---|---|---|
| 8 | 70,554 | 83,427 | 1.18 |
| 32 | 124,718 | 198,131 | 1.59 |
| 64 | 129,220 | 230,763 | 1.79 |
| 128 | 131,470 | 235,007 | 1.79 |
| 256 | 133,723 | 228,500 | 1.71 |

Sharky's throughput stops increasing at approximately 130,000 reads per second
while the device sustains approximately 230,000. Sharky achieves about 57% of
available device throughput.

With eight concurrent writers active, Sharky falls to 38,000-112,000 reads per
second and direct pread sustains 78,000-226,000, a ratio of 2.0 to 2.3.

On a corpus small enough to remain in page cache, the ratio is 3.0 without write
load and 4.0-5.0 with it. That configuration measures software overhead only and
overstates the benefit available on a real node.

### 3.2 Physical-order reads

**Table — Bin-order against physical-order reads, 67 GB corpus, 64 readers, NVMe**

| Fraction of slots read | Reads | Unsorted (reads/s) | Sorted (reads/s) | Ratio |
|---|---|---|---|---|
| 1% | 160,000 | 222,591 | 257,951 | 1.16 |
| 5% | 800,000 | 224,104 | 248,356 | 1.11 |
| 20% | 3,200,000 | 226,672 | 282,985 | 1.25 |

### 3.3 LevelDB block cache size

**Table — LevelDB block cache sweep, 4.19M retrievalIdx entries, 32 readers, NVMe**

| Block cache | Lookups/s | Ratio to 32 MB |
|---|---|---|
| 32 MB (default) | 247,089 | 1.00 |
| 512 MB | 289,825 | 1.17 |
| 2 GB | 301,964 | 1.22 |

Treat this as a lower bound. The 213 MB index remained in operating system page
cache during the test, so a block cache miss became a page cache hit rather than a
disk read. On a node where the index competes with 17 GB of chunk data for memory,
the improvement should be larger by an unknown amount.

### 3.4 Micro-benchmarks

```
BenchmarkKey_Current-15               45.17 ns/op    224 B/op   2 allocs/op
BenchmarkKey_Binary-15                 3.84 ns/op      0 B/op   0 allocs/op
BenchmarkItem_Unmarshal-15            26.16 ns/op     96 B/op   3 allocs/op
BenchmarkTransformedAddressCAC-15    26119 ns/op   5924 B/op  194 allocs/op
```

---

## 4. Claims that did not survive measurement

Recorded so the same estimates are not repeated.

**Table — Pre-measurement estimates against measured results, storage layer**

| Claim | Estimated | Measured | Status |
|---|---|---|---|
| BMT transform cost per chunk | ~40 µs | 26 µs | Confirmed, correct magnitude |
| `retrievalIdx` size on disk | ~220 MB | 213 MB | Confirmed |
| Ordered reads, SSD | 1.2-1.5x | 1.11-1.25x | Confirmed at lower bound |
| Sharky concurrency change | 3-5x | 1.8x idle, 2.3x under write load | Overstated |
| Block cache 32 MB to 512 MB | 1.4-1.8x | 1.17x lower bound | Overstated |
| Fixed-width binary keys | 1.05-1.15x | ~0.1% | Withdrawn |
| Sampler, end to end, SSD | 5-8x | 2-2.5x | Overstated |

The binary key encoding change cannot produce a measurable improvement. Key
construction costs 45 nanoseconds against roughly 34 microseconds of total work per
chunk, which is 0.13% of the total. **Do not pursue it for performance reasons.**

The realistic total improvement available in the storage layer on fast hardware is
approximately 2 to 2.5 times, not the 5 to 8 times estimated before measurement.

---

## 5. Prioritized optimization targets

Each of these is tracked as an issue. See `docs/ROADMAP.md`.

### 5.1 Allow concurrent reads in Sharky

Expected 1.8x idle, 2.3x under write load. Highest value per unit of effort.

`os.File.ReadAt` compiles to `pread` on Unix systems. It does not use or modify the
shared file offset and is safe for concurrent use. The per-shard goroutine exists to
serialize free-slot allocation, not to make reads safe. Reads can therefore call
`ReadAt` directly from the calling goroutine and bypass the actor entirely.

Writes still require serialized slot allocation, but that can use a mutex or atomic
free list rather than a goroutine and channel.

Constraints:
- `pkg/sharky/main_test.go` runs `goleak.VerifyTestMain`, so leaked goroutines fail
  the test suite.
- The deadlock described at `pkg/sharky/store.go:115` (issue #2932) arose from this
  channel protocol. Removing reads from the protocol should eliminate the hazard,
  but verify with the existing tests in `pkg/sharky/`.
- No on-disk format change, so no migration is required.

### 5.2 Separate I/O from hashing in sampler phase 2

Required for 5.1 to produce a benefit. Fifteen worker goroutines cannot supply
enough concurrent read requests to exceed Sharky's current ceiling.

Restructure `pkg/storer/sample.go` so that a large pool of goroutines issues reads
and a pool sized to `runtime.NumCPU()` computes transformed addresses.

### 5.3 Pool BMT allocations

`transformedAddressCAC` performs 194 heap allocations and allocates 5,924 bytes per
chunk. Across a full reserve, one sampling round performs approximately 813 million
allocations and produces roughly 25 GB of garbage. Measured throughput is 157 MB/s,
below what Keccak implementations typically achieve on ARM64.

Reuse the BMT tree buffers, for example through `sync.Pool`. Contained within
`pkg/bmt`. **Identified by measurement rather than by reading the code**, and not in
the original list of proposals.

### 5.4 Sort sampler reads by physical position

Expected 11% to 25% on SSD, plausibly much larger on mechanical drives but
unverified.

Phase 1 already reads the index. Have it emit `(address, Location)` pairs, buffer
them, sort by `(shard, slot)`, then read in that order. Sample selection chooses the
16 items with the smallest transformed address and does not depend on input order,
so reordering is safe.

This also removes the redundant `retrievalIdx` lookup that phase 2 currently
performs.

### 5.5 Increase the LevelDB block cache

At least 1.17x, available without code changes. The flags already exist at
`cmd/bee/cmd/cmd.go`: `db-block-cache-capacity`, `db-write-buffer-size`,
`db-open-files-limit`, `db-disable-seeks-compaction`.

Consider raising the compiled-in default at `pkg/storer/storer.go:246`.

### 5.6 Separate the LevelDB instance by write pattern

Not measured. Roughly 15 namespaces with very different churn rates share one
database. Cache entries are rewritten constantly, pin collections are nearly static,
and reserve indexes churn on eviction. Mixing them means cache activity triggers
compactions that rewrite cold reserve keys.

Quantify the write amplification before committing to this.

### 5.7 Replace goleveldb with Pebble

Not measured. Pebble shares goleveldb's design lineage, is written in Go without
cgo, and performs compaction concurrently. Expected benefit is in tail latency
rather than mean throughput, so measure p99 rather than averages.

### 5.8 Derive placement from the chunk address

Not measured, high effort, high risk. Recorded for completeness.

Sharky currently chooses a shard by backpressure through a shared write channel, so
placement is unpredictable and the 32-byte address to location index is mandatory.
Chunk addresses are already uniformly distributed hashes, so a shard and slot could
be derived from the address instead, replacing the index with a smaller occupancy
structure. That would reduce a chunk read from two I/O operations to one.

Open problems: eviction, reference counting, collision handling, and the fact that
single-owner and content-addressed chunks can share an address.

---

## 6. Changes that require a migration

Anything that alters on-disk layout needs a migration step. The framework is at
`pkg/storer/migration/`, with `all_steps.go` listing the sequence. Items 5.1
through 5.5 are format-neutral; items 5.6, 5.7, and 5.8 are not.

One idea considered and not pursued: inlining postage stamps into the chunk blob.
A content-addressed chunk occupies 4104 bytes in a 4201-byte slot, leaving 97 bytes
unused, and the stamp minus its batch ID is 81 bytes, so it would fit. However, a
maximum-size single-owner chunk fills the slot completely, so a spill path or a
wider slot would be required. The benefit to sampling is small because
`chunkstamp.LoadWithBatchID` is only called for candidates that can still enter the
sample, which becomes rare once the sample fills. The benefit would be to sync
paths and disk usage instead.

---

## 7. Reproducing the measurements

A standalone Go module containing the benchmarks accompanies the original briefing
in a `beebench/` directory. **It has not yet been imported into this repository.**
Until it is, none of the numbers above can be re-derived here. Importing it is
tracked as its own issue and should come before any of section 5 is implemented,
since every one of those targets needs a before-and-after measurement.

Expected contents:

| File | Contents |
|---|---|
| `harness_test.go` | Corpus construction, shard file access, timed concurrent runner |
| `concurrency_test.go` | Sharky against direct pread, page-cache resident |
| `bigconc_test.go` | Sharky against direct pread, device-bound, 67 GB corpus |
| `order_test.go` | Random compared to physical-order reads |
| `writers_test.go` | Background write load generator |
| `ldb_test.go` | LevelDB block cache sweep |
| `micro_test.go` | Key encoding, item unmarshalling, BMT transform |

Two methodological points that cost time and should not be repeated:

1. Reading the same locations in two consecutive passes makes the second pass
   page-cache resident regardless of flags. Use disjoint location sets, or warm
   deliberately before measuring.
2. `F_NOCACHE` on macOS did not bypass the page cache for the unaligned reads used
   here. Building a corpus larger than RAM is a more reliable way to force reads to
   reach the device. Slot size 4201 is not a multiple of the 4096-byte page size, so
   every chunk read spans two pages.

---

## 8. Unmeasured questions

1. **Behaviour on mechanical drives.** The physical-ordering change should help far
   more when seek time dominates. This is the difference between a node completing a
   sample within a redistribution round and failing to. Untested.
2. **Real page cache pressure.** On a production node the index competes with 17 GB
   of chunk data. Section 3.3 understates the block cache benefit by an unknown
   margin.
3. **Production balance of disk against CPU.** The sampler logs a `SampleStats`
   structure at info level on every round. The ratio of `ChunkLoadDuration` to
   `TaddrDuration` in those log lines shows directly whether a given node is limited
   by disk or by CPU. Collecting these from real nodes is the cheapest available
   source of production data and requires no code change.
4. **Write path throughput.** All measurements here concern reads. The ingest path
   through pushsync was not benchmarked.
5. **Write amplification across the shared LevelDB instance.** Needed before
   committing to item 5.6.

---

## 9. Why this matters

For ordinary retrieval traffic the storage layer is not the limiting factor.
Network round-trip time and peer routing dominate. These changes do not make Bee
noticeably faster from a user's perspective.

They matter for one specific function: completing the reserve sample within a
redistribution round. That determines whether a node earns rewards. The practical
effect of this work is to widen the range of hardware that can run a full node
profitably, which is an argument about decentralization rather than about
performance.
