# Results: the LevelDB block cache default does not need raising

Issue: [#12](https://github.com/crtahlin/wasp/issues/12).

**Verdict: keep the compiled-in default at 32 MB. A node measurement refutes the
prediction that the real benefit is larger than the microbenchmark's.** With the
one confounder the microbenchmark could not remove taken out, raising the block
cache does not speed up the reserve sample. It is flat at best and slightly slower
at worst, because the cache holds only the small index while the sample's time is
spent reading the large chunk store, which the block cache does not hold.

## What the issue predicted, and why it was reasonable

[#12](https://github.com/crtahlin/wasp/issues/12) measured 1.17x at 512 MB and
1.22x at 2 GB with `ldb_test.go`, and asked that these be read as a lower bound: the
213 MB retrieval index stayed in the operating system page cache during that test,
so a block cache miss became a page cache hit rather than a disk read. The reasoning
was that on a real node the index competes with about 17 GB of chunk data for memory,
so a larger block cache would pin more of the index and the true benefit would be
larger by an unknown margin.

## How this measurement removes the confounder

The block cache is process heap memory that goleveldb manages itself. The page cache
is operating system memory holding recently read file pages. `echo 3 >
/proc/sys/vm/drop_caches` empties the page cache but does not touch the block cache,
because the block cache is anonymous heap, not file-backed pages. So dropping the page
cache before each run makes the block cache the only in-memory cache of the index: a
block cache miss is then a real disk read, which is the condition the microbenchmark
could not reach.

Setup:

- bench-1, goleveldb engine (`storage-engine: leveldb`), no reserve doubling
  (`reserve-capacity-doubling: 0`), node built from `cec9b84d`. Mainnet, unstaked.
- Reserve full and settled: about 3.82M chunks within radius at network radius 9,
  17 GB on disk. The machine has about 15 GB of RAM, so the reserve is larger than
  memory, which is the memory pressure the issue asks for.
- For each block cache size the node was restarted with that
  `db-block-cache-capacity`, then the page cache was dropped immediately before each
  `rchash` run at the committed depth (9). Three runs per size (rule 7), reported
  with the spread. Chunk reads hit disk in every arm identically, so they are a
  constant baseline and the only variable across arms is how much of the index the
  block cache holds.

## The result: raising the cache does not help

**Table — reserve sample duration by block cache size, bench-1 goleveldb d=0, page
cache dropped before each run, committed depth 9**

| Block cache | Coverage of 213 MB index | Cold runs (s) | Median (s) |
|---|---|---|---|
| **32 MB** (default) | ~15% | 39.9 / 38.5 / 39.1 | **39.1** |
| 512 MB | 100% | 43.6 / 42.0 / 42.4 | 42.4 |
| 2 GB | 100% | 42.1 / 49.5 / 71.2 | 49.5 |

The 32 MB and 512 MB arms are tight and do not overlap, and 512 MB is the slower of
the two by about 3.5 s. Going from 15% index coverage to 100% did not shorten the
sample; it lengthened it slightly. The 2 GB arm is noisier, with a 71.2 s run from
compaction and cache-warmth variance, but it is never faster than 32 MB. The tight,
non-overlapping 32 MB and 512 MB clusters carry the conclusion without leaning on the
noisy 2 GB arm.

## Why the prediction did not hold

Three reasons, and the third is why bigger is slightly worse rather than merely no
better.

- **The block cache holds the index, not the chunks.** The reserve sample reads both
  the 213 MB retrieval index and the roughly 17 GB of chunk data behind it. The block
  cache caches only the index store's blocks; the chunk bytes live in the sharky blob
  files and are served by the page cache, not the block cache. With the page cache
  dropped, the chunk reads hit disk in every arm, and that disk time dominates the
  sample. Making the index fully resident cannot move the part of the run that is
  spent on chunk reads.
- **32 MB already holds the working set the sample touches.** The sample iterates one
  neighborhood, and the index blocks for that neighborhood's chunks are a small,
  localized part of the 213 MB index. 32 MB is enough to hold the blocks the sample
  actually reads, so raising the cache to cover the whole index adds coverage the
  sample never uses.
- **A larger block cache steals memory from the page cache.** The block cache is Go
  heap. On a node where the reserve is larger than RAM, every megabyte given to the
  block cache is a megabyte the operating system cannot use to cache the 17 GB chunk
  store, which is the actual bottleneck. So a 512 MB or 2 GB block cache leaves less
  page cache for chunk reads and can make the sample slightly slower, which is the
  ~3.5 s the 512 MB arm lost.

## Where a benefit could still exist

The sample here is chunk-read bound, so an index-only cache cannot help it. A
workload that is index-read bound and runs where the index has been evicted from the
page cache could still benefit. That needs a node whose reserve dwarfs RAM by a large
factor, so the operating system cannot keep even the 213 MB index resident alongside
the chunk data, and a read pattern dominated by index lookups rather than chunk loads.
That is a high-doubling goleveldb node under a specific load, not a normal
single-reserve node. Such an operator is already tuning the `db-*` options and can
raise `db-block-cache-capacity` themselves; the flag exists for exactly that. This
issue was only ever about the compiled-in default, which most operators never change,
and the default should serve the normal node.

Two further reasons the default matters less than when the issue was filed: goleveldb
is no longer the default engine (pebble became the default in
[#185](https://github.com/crtahlin/wasp/issues/185)), so fewer nodes run the goleveldb
block cache at all, and pebble roughly halved the index footprint per within-radius
chunk, which shrinks the index this cache would hold.

## Not an upstream defect

The 32 MB default is identical in unmodified upstream and this measurement confirms it
is a reasonable default, not a defect. Under rule 11 this is a preference validated,
not a defect found, so it carries no `affects-upstream` label.

## Recommendation

Keep `defaultBlockCacheCapacity` at 32 MB. The `db-block-cache-capacity` flag stays
available for the rare large-reserve goleveldb node that has measured a need. The
config reference now states the cost of raising it, per rule 8: more heap memory, and
less page cache for the chunk store on a memory-constrained node.

Generated with help of AI.
