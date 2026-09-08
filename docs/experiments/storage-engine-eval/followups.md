# Re-checking the Pebble follow-ups

Issue: [#198](https://github.com/crtahlin/wasp/issues/198). Follows the
storage-engine comparison in this directory
([results.md](results.md), [#185](https://github.com/crtahlin/wasp/issues/185)).

**Outcome: of the three follow-ups, only the level-0 one was a real question, and it
is answered yes. Re-checking it on the real Pebble node surfaced a larger problem:
the reserve-sample read is bound by disk speed and page-cache warmth, not by level-0
depth or the engine, which means the read-axis verdict in
[#185](https://github.com/crtahlin/wasp/issues/185) is not soundly established. That
is filed separately as [#231](https://github.com/crtahlin/wasp/issues/231). The other
two follow-ups are not the questions to spend time on now, and the conditions that
would bring them back are recorded.** The issue itself asked for exactly this check,
warning that the obvious metric had already been the wrong one once.

## Follow-up 2: level 0 stays shallow over long uptime (confirmed)

The `db-compaction-l0-trigger: 4` setting that made the sample read fast in
[#185](https://github.com/crtahlin/wasp/issues/185) was verified right after a
restart. The question was whether level 0 stays shallow through long steady-state
operation.

It does. bench-2 (Pebble, no doubling, `trigger: 4`) after about 21 hours of uptime,
reserve full and settled:

| Pebble level | files | size |
|---|---|---|
| 0 | 6 | 9.6 MB |
| 4 | 11 | 37.8 MB |
| 5 | 45 | 249 MB |
| 6 | 142 | 1.48 GB |

Level 0 held at 6 files across four snapshots over two minutes, under the stop-writes
threshold of 12, with 47 MB of compaction debt. Compaction is keeping up, not falling
behind. The fix holds beyond a fresh restart.

## The larger finding: the sample read is bound by disk and cache, not level 0

The read this tuning was meant to protect is much slower over long uptime than the
[#185](https://github.com/crtahlin/wasp/issues/185) figure, and level 0 depth is not
why. bench-2's sample went from 44 s (measured 2026-09-01 right after fill) to
170 to 217 s at steady state, while level 0 stayed at 6. Two verified confounds
explain it, neither of them the engine:

- **The 44 s was a warm-page-cache number.** Right after a fill the roughly
  15 to 17 GB reserve was resident in 16 GB of RAM. A week later it is not, so the
  sample reads cold. Warming did not help: a cold run and the next two warm runs were
  all 170 to 217 s, because the reserve is larger than RAM. This is the same
  chunk-read-bound behaviour found in [#12](https://github.com/crtahlin/wasp/issues/12).
- **The engines were compared across nodes with different disks.** Cold read of one
  sharky shard with the page cache dropped: bench-1 (goleveldb, virtio) 880 MB/s;
  bench-2 (Pebble, emulated SATA) 259 MB/s, 3.4x slower, and the sample does random
  reads where the emulated disk is penalised more. bench-1 goleveldb samples cold in
  about 39 s, bench-2 Pebble in about 182 s, but that gap is disk and cache, not a
  demonstrated engine difference.

There is an operational point the warm figure hid: a steady-state cold sample of
170 to 217 s sits at or over the roughly 190 s redistribution commit window
([#17](https://github.com/crtahlin/wasp/issues/17)), so a node whose reserve exceeds
its RAM on a slow disk may not sample in time to play the round. This is
engine-independent and is why sample time must be measured cold and at steady state.
The read-axis retest, same-disk and cold, is [#231](https://github.com/crtahlin/wasp/issues/231).

## Follow-up 1: compaction nudge after a mass eviction (real, not a risk now)

Pebble reclaims freed disk slowly after a mass eviction. This is a real mechanism but
not a risk in normal operation. Mass evictions on a no-doubling node are gradual, from
batch expiry and radius steps, not sudden. bench-2 sits at 65% disk with 17 GB free,
so a slow background reclaim does not outpace to disk-full. It was not tested by
forcing an eviction, because that is invasive on a live mainnet node (rule 4). Revisit
only if a node is seen filling disk because reclamation falls behind evictions,
which needs evictions that are both large and frequent together with thin disk
headroom.

## Follow-up 3: write-stall tail under saturation (not a regime a live node reaches)

Pebble showed occasional multi-second commit stalls when the write path was
saturated. A live node does not reach that regime. The global inbound rate is capped
at 1000 chunks per second ([#32](https://github.com/crtahlin/wasp/issues/32)), a few
hundred KB/s of index writes. bench-2 over 21 hours flushed and compacted about
465 MB, an average near 6 KB/s to Pebble, orders of magnitude below saturation. The
stalls were a synthetic microbench force-feeding writes far above any network rate.
This is not a question a node on a real connection reaches, as the issue anticipated.

## Disposition

Follow-up 2 is confirmed and the other two are not worth active work now, so
[#198](https://github.com/crtahlin/wasp/issues/198) is closed. The substantive thread
it uncovered is the read-axis retest, [#231](https://github.com/crtahlin/wasp/issues/231).

Generated with help of AI.
