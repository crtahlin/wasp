# Sort sampler reads by physical position

Issue: [#11](https://github.com/crtahlin/wasp/issues/11)

## Problem

`ReserveSample` (`pkg/storer/sample.go`) iterates the `chunkBin` index in bin
order. Bin order is derived from the chunk address, which is a hash, so it has no
relationship to where the chunk data physically sits in the sharky shard files.
Phase 2 therefore issues its reads in an order uncorrelated with the disk layout.

Every read is a seek to an unrelated position. On a device where seek cost is a
large part of read cost, that is the dominant term, and it is not a term the
reader count from [#9](https://github.com/crtahlin/wasp/issues/9) can reduce:
more concurrent random reads is still random reads.

## What the issue assumed, and what is actually here

The issue says:

> Phase 1 already reads the index. Have it emit `(address, Location)` pairs [...]
> This also removes a redundant `retrievalIdx` lookup that phase 2 currently
> performs, since the location is already known from phase 1.

**That is not true of this tree, and the difference changes the design.** Phase 1
iterates `chunkBin`, whose item type is:

```go
type ChunkBinItem struct {
	Bin       uint8
	BinID     uint64
	Address   swarm.Address
	BatchID   []byte
	StampHash []byte
	ChunkType swarm.ChunkType
}
```

There is no `Location` in it. The location lives in `retrievalIdx`
(`chunkstore.RetrievalIndexItem`), and the only place it is read is inside
`chunkstore.Get`, which phase 2 calls. So phase 1 cannot emit locations without
doing the `retrievalIdx` lookup itself.

The consequence: **no lookup is removed by this change.** The `retrievalIdx`
lookup is moved earlier, not eliminated, and in the design chosen below it is
performed twice — once cold to obtain the sort key and once warm inside the
existing `Get`. The issue's claim of a saved lookup should not be carried into
the result.

The original fork's `order_test.go` numbers were produced against a tree where
phase 1 did carry the location. They therefore measure a strictly cheaper change
than the one available here, and they are treated as an upper bound rather than
a prediction.

## Hypothesis

Sampler read cost has a seek component proportional to the distance between
consecutive reads within a shard file. Buffering a window of pending reads,
sorting the window by `(Shard, Slot)`, and issuing them in that order reduces the
mean distance between consecutive reads within a window from roughly `N/3` slots
to roughly `N/W`, for a reserve of `N` chunks and a window of `W`.

If seek cost matters on the device, sample wall clock falls. If the device has no
meaningful seek cost — which is the expected case on the NVMe in bench-1 — the
ordering buys nothing and the extra `retrievalIdx` lookup makes the sampler
slower. **Both outcomes are answers.** A null or negative result on an SSD does
not refute the mechanism; it bounds where the mechanism is worth paying for, and
that is what the default should then be set from.

## Design

Insert an ordering stage between the existing iterate stage and the existing
reader pool:

```
iterate chunkBin  ->  locate + sort window  ->  read pool  ->  hash pool
   (sequential)        (random index reads)     (sharky)      (CPU)
```

The ordering stage:

1. Accumulates up to `W` bin items from phase 1.
2. For each, reads `retrievalIdx` to obtain `Location`.
3. Sorts the window by `(Location.Shard, Location.Slot)`.
4. Emits the window in that order into the existing chunk channel.

The reader pool is unchanged: it still calls `db.ChunkStore().Get(ctx, addr)`.
The location is used **only as a sort key** and is then discarded.

A short final window is sorted and emitted like any other. An item whose
`retrievalIdx` lookup fails is emitted unsorted rather than dropped, so the
ordering stage cannot change which chunks reach the sampler — only the order they
arrive in.

### Why the location is not used to read

The obvious version of this change is to keep the location and read sharky
directly, skipping the second lookup. That version is **incorrect here**, and the
reason is worth recording because it is not obvious.

`chunkStoreTrx.Get` takes a per-address lock for the duration of the lookup and
the read:

```go
func (c *chunkStoreTrx) Get(ctx context.Context, addr swarm.Address) (ch swarm.Chunk, err error) {
	unlock := c.lock(addr)
	defer unlock()
	ch, err = chunkstore.Get(ctx, c.indexStore, c.sharkyTrx, addr)
	return ch, err
}
```

Splitting the lookup from the read opens a window between them that is, by
construction, the whole sort window wide. Within that window the chunk can be
evicted, its sharky slot released, and that slot reused by an unrelated `Put`
from pushsync or pullsync. The read then returns another chunk's bytes, and
`readChunk` labels them with the address that was asked for:

```go
return swarm.NewChunk(rIdx.Address, buf), nil
```

The sampler would hash those bytes, derive a transformed address from them, and
potentially place the result in the sample it commits to a redistribution round.
The failure is silent — nothing checks that the bytes match the address — and it
gets more likely the larger the window is, which is the opposite of the direction
the optimisation wants to move in.

Sampling runs for minutes on a large reserve and overlaps eviction and ordinary
syncing, so this is not a theoretical race. Closing it would need per-chunk
revalidation (`cac.Valid` or `soc.Valid`), which is another BMT hash per chunk on
the stage that is already the CPU-bound one.

Paying one warm LevelDB lookup is the cheaper correct option, so this design pays
it. The lookup that the reader repeats is for a key the ordering stage read
moments earlier, so it is served from the LevelDB block cache or the OS page
cache rather than the device.

## Configuration

Per rule 8 in `AGENTS.md`, this ships as configuration:

| Setting | Default | Meaning |
|---|---|---|
| `--sampler-sort-window` | `0` | Chunks buffered and sorted by physical position before reading. `0` disables ordering. |

**The default is `0`, which is exactly current behaviour.** No node changes what
it does when this merges. That is deliberate: the ordering has not been measured
on any device in this project, and the one device available to measure it on is
the one where the mechanism is least likely to pay. Defaulting it on would be
setting a default from an argument, which is the thing this repository has
repeatedly been wrong about.

The default is revisited in a follow-up once the measurement below exists.

## Measurement

Same method as [#54](https://github.com/crtahlin/wasp/issues/54): repeated
`rchash` runs against a fixed neighbourhood on bench-1, comparing the window off
against several window sizes, reading wall clock from the endpoint and
`ChunkLoadDuration` from `SampleStats`.

Three quantities separate cleanly:

- **Ordering gain** — `ChunkLoadDuration` per chunk iterated, window on against
  window off.
- **Extra lookup cost** — total sample duration at window `W` against window off,
  minus the ordering gain. On an SSD the ordering gain should be near zero, which
  makes that run a direct measurement of the lookup cost on its own.
- **Window sensitivity** — whether the effect grows with `W` as `N/W` predicts,
  or flattens. Flattening at small `W` would mean the device is not seek-bound
  and the mechanism is not the one described here.

Expected on bench-1 (NVMe): little or no gain, and a measurable regression from
the extra lookup. The SSD figures the issue quotes — 1.16x at 1% of slots, 1.11%
at 5%, 1.25x at 20% — came from the cheaper variant described above and should
not be expected to reproduce.

**The mechanical-drive case cannot be measured here.** No such device is
available, and the issue's argument that this could decide whether a sample
completes within a redistribution round on a seek-bound device stays an argument.
It is not evidence, and the default must not be set from it.

## Protocol impact

None, but the reason is not the one the issue gives, and it is worth stating
precisely because the sample is what a redistribution round is judged on.

Phase 3 does not sort a collected slice. It runs an insertion sort into a list
bounded at `SampleSize`, deciding as each item arrives whether it belongs:

```go
if le(item.TransformedAddress, currentMaxAddr) || len(sampleItems) < SampleSize {
```

That gate reads `currentMaxAddr`, which depends on what has arrived so far — so
the *decision* is order-dependent even though the *outcome* is not. The outcome
holds because the running maximum is always greater than or equal to the final
maximum, so any item belonging in the final 16 passes the gate whenever it
arrives. The set of the 16 smallest transformed addresses is the same for every
arrival order.

One case is genuinely order-dependent: two items with an **equal** transformed
address. `insert` then keeps whichever is content-addressed, and if both are, the
later arrival replaces the earlier. That requires a collision in a keyed BMT hash
between two distinct chunks, so it is not a practical concern, but it is the one
place where the reads' order reaches the sample and it should not be described as
impossible.

Checked directly: a test samples the same fixture store with the window off and
at several window sizes and asserts the resulting `Sample.Items` are identical.

## Rollout and rollback

Rollback is setting `--sampler-sort-window` to `0`, which is also the default. No
persisted state changes, no format changes, nothing to migrate.
