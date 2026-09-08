# Deriving chunk placement from the address: analysis

Issue: [#16](https://github.com/crtahlin/wasp/issues/16).

**Conclusion: not worth its risk, and its own precondition is now met. Iceboxed.**
The headline gain, cutting a chunk read from two operations to one, is largely
illusory because the operation it removes is a cached lookup, not a disk seek. The
real cost the index imposes is write amplification and memory, and removing it that
way brings serious correctness problems and a disk over-allocation that can cost
more than it saves. This is the largest on-disk change of the storage track and
needs a migration; the cheaper targets it was gated behind have landed, so the
marginal gain does not justify it.

## How placement works today

A stored chunk carries a `RetrievalIndexItem` in the index store, keyed by the
32-byte address:

    {Address 32B, Timestamp 8B, Location 7B, RefCnt 4B} = 51 bytes

`Location` is `{shard uint8, slot uint32, length uint16}`. Sharky chooses a shard
dynamically: writes go onto a shared channel and whichever shard has a free slot
takes the write, by backpressure. Placement is therefore unpredictable, which is why
the address-to-location index is mandatory. A read is: look up the index by address,
get the `Location`, then a positional read from the shard file. Two operations.

## The hypothesis, and why the headline overstates the gain

Addresses are uniform hashes, so a shard and slot could be derived from the address,
dropping the index and reducing a read to one operation. The overstatement is in
treating both operations as I/O. The retrieval index is small and hot: about 51
bytes per chunk, roughly 200 MB for a 4M-chunk reserve, which sits in the block
cache. The lookup it removes is usually a memory hit, not a disk seek. The genuine
cost the index imposes is write amplification (it is one of about six index writes
per chunk, see [#30](https://github.com/crtahlin/wasp/issues/30)) and memory, not
read latency.

## The open problems, worked through

- **Collision.** Hashing an address into a fixed number of slots is a hash table. To
  keep collisions rare the slot array must be much larger than the chunk count, a low
  load factor, which over-allocates disk with empty slots. That over-allocation can
  cost more than the 200 MB index it replaces. Without it, collisions force either
  probing, which restores multiple reads and defeats the one-read premise, or
  chaining, which restores an index.
- **Verification needs a read.** To detect a collision the slot must store the
  address so a read can confirm it holds this chunk. That turns a cheap cached
  existence check into a disk read, a regression on the very common "do I already
  have this chunk" path.
- **Reference counting.** `RefCnt` is not derivable from the address; a chunk shared
  across reserve, cache, and pins has a count above one. A persisted, crash-consistent
  per-slot refcount structure is required, so the "much smaller occupancy structure"
  is refcount-per-slot, not a bitmap.
- **Eviction ordering stays.** The reserve and cache still need their own ordering
  indexes (`batchRadius`, `chunkBin`, `cacheOrderIndex`). Removing the retrieval
  index removes none of them.
- **Single-owner and content-addressed chunks can share an address.** They can land
  on the same address with different content; address-derived placement puts them in
  one slot with no clean way to hold both.

## Why now is the point to stop

[#16](https://github.com/crtahlin/wasp/issues/16) set its own condition: do not start
before the cheaper targets are measured, and if they deliver the projected 2 to 2.5x,
this may not be worth its risk. That condition is met. Pebble as the default engine
already roughly halved the index footprint per within-radius chunk
([#185](https://github.com/crtahlin/wasp/issues/185)), the write amplification is
characterized ([#30](https://github.com/crtahlin/wasp/issues/30)), and reserve
capacity doubling is validated on real hardware
([#17](https://github.com/crtahlin/wasp/issues/17)). The marginal further gain from
address-derived placement does not justify the largest on-disk change of the track,
a migration, and the correctness risk above.

Kept on record, iceboxed. Revisit only if a measured, specific need appears that the
cheaper levers cannot meet.

Generated with help of AI.
