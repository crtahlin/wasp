# Storage scaling bottleneck analysis

The source document behind most of the reserve-capacity-scaling issues in this
backlog, imported so that those issues cite something a reader here can open.

> **Provenance and verification**
>
> Written against `crtahlin/bee` commit `aba763ff` (2026-01-22), on branch
> `fix/2tb-all-fixes`. Imported unmodified on 2026-08-26; the text below is the
> original.
>
> Every constant it cites was re-checked against this fork's base, **upstream
> `v2.8.1`**, and all were unchanged there.
>
> **Four have since changed in wasp, and the document does not know it.** Their
> *values* are untouched — each became an option with its previous value as the
> default — but a reader following the document to the code will not find the
> constant it names:
>
> | document says | wasp now | issue |
> |---|---|---|
> | `handleMaxChunksPerSecond = 250` | `pullsync.DefaultMaxChunksPerSecond`, flag `pullsync-max-chunks-per-second` | [#25](https://github.com/crtahlin/wasp/issues/25) |
> | `maxChunksPerSecond = 1000` | `puller.DefaultMaxChunksPerSecond`, flag `puller-max-chunks-per-second` | [#26](https://github.com/crtahlin/wasp/issues/26) |
> | `recalcPeersDur = 5m` | `puller.DefaultRecalcPeersDur`, flag `pullsync-recalc-interval` | [#59](https://github.com/crtahlin/wasp/issues/59) |
> | `reserveWakeUpDuration = 15m` | `DefaultReserveWakeUpDuration`, flag `reserve-wakeup-duration` | [#58](https://github.com/crtahlin/wasp/issues/58) |
>
> `maxAllowedDoubling` (1), `sharkyNoOfShards` (32) and the LevelDB defaults are
> unchanged.
>
> **Two of its arguments have since been measured and did not survive.** The
> document treats the LevelDB block cache as materially undersized; measurement
> put the gain from a 64x larger cache at 1.17x, because the OS page cache
> already holds the index — see [#12](https://github.com/crtahlin/wasp/issues/12).
> It also reasons about raising `maxAllowedDoubling`; that turns out to change
> pushsync receipt tolerance for every node and to make stock nodes reject
> receipts from a high-doubling node, so it cannot be raised unilaterally — see
> [#17](https://github.com/crtahlin/wasp/issues/17).
>
> Read it as the reasoning that generated the backlog, not as a current
> description of the code or a set of conclusions that still stand.

---

# Storage Scaling Bottleneck Analysis

**Date**: 2026-01-21
**Context**: Analysis of performance bottlenecks when increasing Bee node storage capacity via `--reserve-capacity-doubling`

## Overview

This document analyzes the bottlenecks encountered when scaling Bee node storage capacity. The `--reserve-capacity-doubling` option allows increasing storage from the default ~17 GB to ~34 GB (with value `1`), but several architectural constraints limit the effectiveness of this scaling.

### Storage Capacity Configuration

```
--reserve-capacity-doubling 0  →  4,194,304 chunks (~17 GB)
--reserve-capacity-doubling 1  →  8,388,608 chunks (~34 GB)
```

The option is currently capped at `maxAllowedDoubling = 1` in `pkg/node/node.go:195`.

---

## Critical Bottlenecks

### 1. PullSync Rate Limiting (HIGHEST IMPACT)

**Severity**: Critical
**Location**: `pkg/pullsync/pullsync.go:53-54`

```go
handleMaxChunksPerSecond = 250
handleRequestsLimitRate = time.Second / 250  // 4ms per chunk
```

**Problem**: Each peer can only send 250 chunks/second to the node. Even with more storage capacity, the reserve cannot be filled faster.

**Additional constraint in Puller** (`pkg/puller/puller.go:42`):
```go
maxChunksPerSecond = 1000  // ~4 MB/s total inbound sync rate
```

**Impact**: Doubling storage capacity results in 2x longer sync time. The network cannot fill the additional capacity any faster.

**Calculation**:
- 250 chunks/sec/peer × 8 peers = 2000 chunks/sec theoretical max
- 4M chunks @ 2000/sec = ~33 minutes to fill base capacity
- 8M chunks = ~66 minutes minimum

---

### 2. LevelDB Configuration Undersized

**Severity**: High
**Location**: `pkg/storer/storer.go:244-249`

```go
defaultOpenFilesLimit         = 256
defaultBlockCacheCapacity     = 32 * 1024 * 1024  // 32 MB
defaultWriteBufferSize        = 32 * 1024 * 1024  // 32 MB
```

**Problem**: With 8M+ chunks, 32MB cache provides <1% coverage. Index lookups become disk-bound, causing significant I/O overhead.

**Impact**:
- Increased disk I/O during chunk lookups
- Higher latency for reserve operations
- Potential write stalls during high sync activity

**Recommended values for doubled storage**:
```yaml
db-open-files-limit: 512
db-block-cache-capacity: 67108864     # 64 MB
db-write-buffer-size: 67108864        # 64 MB
```

---

### 3. Reserve Lock Contention

**Severity**: High
**Location**: `pkg/storer/internal/reserve/reserve.go:103-126`

```go
r.multx.Lock(string(chunk.Stamp().BatchID()))  // batchID lock
r.multx.Lock(strconv.Itoa(int(bin)))           // bin lock
```

**Problem**: Every chunk Put operation acquires TWO locks sequentially. With high sync rates, this serializes operations and creates contention.

**Impact**:
- Reduced throughput during parallel sync operations
- Increased latency per chunk storage operation
- Bottleneck scales with number of concurrent sync streams

---

### 4. Full Table Scans (O(n) Operations)

**Severity**: High
**Location**: `pkg/storer/reserve.go:87-118`

```go
func (db *DB) countWithinRadius(ctx context.Context) (int, error) {
    err := db.reserve.IterateChunksItems(0, func(ci *reserve.ChunkBinItem) (bool, error) {
        if ci.Bin >= radius { count++ }
        // Also checks batch existence for each chunk
    })
}
```

**Problem**: Every 15 minutes (`reserveWakeUpDuration = 15 * time.Minute`), the node scans ALL chunks to:
1. Count chunks within radius
2. Check for invalid/expired batches

**Impact**: With 8M chunks, this scan takes significantly longer, blocking other reserve operations during execution.

**Additional scan locations**:
- Compaction (`pkg/storer/compact.go`) - full scan per shard
- Reserve reset (`pkg/storer/internal/reserve/reserve.go:501-587`)
- Neighborhood stats calculation

---

### 5. Index Multiplicity (Write Amplification)

**Severity**: Medium
**Location**: `pkg/storer/internal/reserve/reserve.go:244-260`

Each chunk Put requires 5+ separate index updates:

1. `BatchRadiusItem` - batch/bin tracking
2. `ChunkBinItem` - bin-level indexing
3. `ChunkStampIndex` - stamp metadata
4. `StampIndex` - stamp collision detection
5. `ChunkStore.Put()` - actual chunk data

**Problem**: 5x write amplification per chunk stored.

**Impact**:
- Increased LevelDB write load
- More frequent compaction triggers
- Higher disk I/O during sync

---

### 6. Sharky Shard Contention

**Severity**: Medium
**Location**: `pkg/storer/storer.go:204`

```go
sharkyNoOfShards = 32
```

**Problem**: Only 32 shards for chunk data storage. With high concurrent sync rates, shards become a contention point.

**Impact**:
- Write operations may queue waiting for shard availability
- Read prioritization can starve writes under load

---

### 7. Peer Saturation Limits

**Severity**: Medium
**Location**: `pkg/topology/kademlia/kademlia.go`

```go
defaultSaturationPeers     = 8   // target peers per bin
defaultOverSaturationPeers = 18  // max peers per bin
```

**Problem**: With only 8-18 peers per bin, parallelism for sync operations is inherently limited.

**Impact**:
- Cannot scale sync throughput by adding more peer connections
- Limited redundancy for chunk retrieval

---

## Network-Specific Constraints

| Component | Limit | Impact on Doubled Storage |
|-----------|-------|---------------------------|
| PullSync rate | 250 chunks/s/peer | 2x sync time |
| Puller global rate | 1000 chunks/s | ~4 MB/s max inbound |
| MaxPage (batch size) | 250 chunks | Memory/latency tradeoff |
| Stream limits | 5000 incoming | Parallelization ceiling |
| Peers per bin | 8-18 | Limited sync sources |

---

## What Happens When Capacity Is Doubled

1. **Sync Phase**: Takes ~2x longer to fill reserve (rate-limited)
2. **Steady State**: More chunks to iterate = slower radius calculations
3. **Eviction**: When full, evicting takes longer (more batches to check)
4. **Database**: LevelDB performance degrades with larger dataset
5. **Memory**: No significant increase needed (indexes are on disk)

---

## Recommendations Summary

### Configuration Changes (bee.yaml)

```yaml
# Increase for doubled storage capacity
db-open-files-limit: 512
db-block-cache-capacity: 67108864     # 64 MB
db-write-buffer-size: 67108864        # 64 MB
cache-capacity: 2000000               # 2M entries (up from 1M)
```

### Code Changes Required

| Change | File | Current | Proposed |
|--------|------|---------|----------|
| PullSync rate | `pkg/pullsync/pullsync.go:53` | 250 | 500+ or configurable |
| Puller rate | `pkg/puller/puller.go:42` | 1000 | 2000+ or configurable |
| Wake-up duration | `pkg/node/node.go:192` | 15 min | 30 min |
| Shard count | `pkg/storer/storer.go:204` | 32 | 64 for doubled capacity |
| Max doubling | `pkg/node/node.go:195` | 1 | Higher if needed |

---

## Key Files Reference

| File | Purpose |
|------|---------|
| `cmd/bee/cmd/cmd.go:239-242` | Default DB config values |
| `pkg/pullsync/pullsync.go:53` | `handleMaxChunksPerSecond` |
| `pkg/puller/puller.go:42` | `maxChunksPerSecond` |
| `pkg/node/node.go:192-195` | `reserveWakeUpDuration`, `maxAllowedDoubling` |
| `pkg/storer/storer.go:204` | `sharkyNoOfShards` |
| `pkg/storer/storer.go:251` | `DefaultReserveCapacity` |
| `pkg/storer/internal/reserve/reserve.go` | Reserve locking and Put logic |
| `pkg/storer/reserve.go` | Reserve worker and radius management |
| `pkg/topology/kademlia/kademlia.go` | Peer saturation constants |

---

## Conclusion

The primary bottleneck when scaling storage is **sync rate limiting**, not database or storage capacity. The architecture prioritizes network fairness and stability over maximum per-node throughput, which is appropriate for a decentralized system but limits individual node scaling.

For effective storage scaling beyond 2x, multiple code changes would be required to increase sync rates, reduce lock contention, and optimize O(n) operations.
