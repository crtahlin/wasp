# Spec: expose Pebble L0 read-amplification as a metric

Issue: [#286](https://github.com/crtahlin/wasp/issues/286). Prerequisite for a
valid cross-engine L0 comparison in the resource-curve study
([#279](https://github.com/crtahlin/wasp/issues/279)) and the under-load study
([#278](https://github.com/crtahlin/wasp/issues/278)).

## Problem

The Pebble stats collector exposes `pebble_level_files{level="0"}`, the raw SST
file count. Pebble triggers L0 compaction on read-amplification, the number of L0
sublevels (`Metrics().Levels[0].Sublevels`), not the file count. Many
non-overlapping files sit in few sublevels, so file count overstates depth: a
store at 7 L0 files may be at read-amp 2, shallower than goleveldb at 4 files.
Comparing the two engines by file count is the wrong yardstick.

## Design

Add two gauges to the collector in `pkg/storage/pebblestore/metrics.go`, read at
scrape time from the metrics Pebble already computes:

- `pebble_level_sublevels{level}` from `stats.Levels[level].Sublevels` (only L0
  carries sublevels; other levels report 0).
- `pebble_read_amp` from `stats.ReadAmp()`, the whole-database read amplification.

No behaviour change; these are read-only gauges alongside the existing level_files
and compaction metrics.

## Protocol impact

None. Metrics only. No wire, proof, or on-chain surface; not near the
protocol-freeze lock.

## Tests

A collector scrape includes the new gauges with plausible values on a store with
data.

## Docs

Note in the metrics collector comment and the test-bench playbook that Pebble L0
depth is read as sublevels (read-amplification), not file count, and that the
cross-engine comparison uses read-amplification.

## Upstream portability

Self-contained in the Pebble store metrics; ports across an upstream sync without
conflict. Not intended upstream as-is. No `affects-upstream` label.

Generated with help of AI.
