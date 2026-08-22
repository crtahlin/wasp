# Concurrent reads in Sharky

Issue: [#8](https://github.com/crtahlin/wasp/issues/8)

## Problem

`shard.process()` (`pkg/sharky/shard.go:99`) is one goroutine per shard running a
`select` loop that performs blocking file I/O inline. Reads and writes share that
loop, so each shard has **one outstanding I/O operation at a time** and a read can
queue behind a write on the same shard.

Measured on bench-1 (AMD Ryzen 7 5700G, 4.4 GB corpus, page-cache resident, so
this isolates software overhead from disk):

| concurrency | sharky ops/s | direct pread ops/s |
|---|---|---|
| 1 | 408,190 | 745,274 |
| 4 | 546,334 | 2,816,761 |
| 16 | 362,836 | 4,715,493 |
| 256 | 438,739 | 4,474,478 |

Sharky peaks at concurrency **4** and is then flat to 256. `pread` over the same
files scales 6.3x. Under 8 concurrent writers Sharky collapses to 80,000–122,000
ops/s while `pread` holds 2.6M — the worst case being exactly the condition the
sampler runs in.

Sampling is where this bites. With SIMD hashing enabled, disk is **78–89%** of
reserve-sample time (three matched runs, #13).

## Hypothesis

The per-shard goroutine exists to serialise **free-slot allocation for writes**,
not to make reads safe. Reads can bypass the actor entirely.

Two facts support this:

- `shard.read` does nothing but `sh.file.ReadAt(r.buf, sh.offset(r.slot))`.
  `ReadAt` compiles to `pread`, which neither uses nor mutates the shared file
  offset and is safe for concurrent use on the same descriptor.
- The shard data file is a plain `*os.File` (`dirFS.Open` → `os.OpenFile`), and
  the only `Seek` call in the package is in `slots.go`, on the **slots file** —
  a different descriptor. Nothing repositions the shard file.

## Design

`Store.Read` calls `sh.read(...)` directly from the calling goroutine rather than
handing work to the actor over `sh.reads` and awaiting `sh.errc`.

Writes are unchanged: they keep the actor, because slot allocation genuinely must
be serialised.

Deliberately **not** in scope:

- Replacing the write actor with a mutex or atomic free list. Possible later, but
  it changes allocation behaviour and this change should not.
- Changing `sharkyNoOfShards` (#31). The data says shard count is not the binding
  constraint — Sharky saturates at concurrency 4, far below the 32 shards it
  already has — and that change carries an on-disk migration.

## Protocol impact

**None.** No on-disk format change, so no migration. No wire change; nothing in
`.github/protocol-freeze.lock` is touched. `Location` encoding is untouched, so
existing `retrievalIdx` entries stay valid.

The risk is not protocol, it is **data**: a read returning wrong bytes would
corrupt chunks silently. Mitigated by `pread` semantics above and by the existing
`pkg/sharky` test suite, which must pass unchanged.

## Constraints from the existing code

- `pkg/sharky/main_test.go` runs `goleak.VerifyTestMain`. The actor must still
  terminate cleanly when it handles only writes; a shutdown path that waits on
  `sh.reads` will hang or leak.
- `Store.Read` contains a `select` that **deliberately ignores context
  cancellation**, commented as required to avoid a deadlock (upstream issue
  #2932): the result must be drained from `errc` or the shard stalls. Removing
  reads from that protocol should eliminate the hazard rather than move it, but
  that must be demonstrated by the existing tests rather than assumed.
- `Store.Read` currently returns `ErrQuitting` on shutdown. That behaviour must
  survive, since callers distinguish it.

## Measurement

**In the harness first, because the node cannot resolve this.**

Three matched runs on bench-1 showed disk time per chunk varies **2.23x**
(113.69–253.72 µs) with identical code and identical peer count, while CPU time
varies 1%. A 2x improvement to the disk half would sit *inside* that noise band,
so a node-level before-and-after cannot demonstrate this change.

1. **Primary**: `beebench` `TestReadConcurrencyWarm` and `TestReadConcurrencyContended`,
   which produced the table above. Success is Sharky scaling with concurrency
   instead of flattening at 4. Target: within a small factor of the `pread`
   column rather than 9% of it.
2. **Secondary**: `TestBigCorpusConcurrency`, device-bound, for a figure that
   reflects real disk rather than software overhead. Expect a **smaller**
   improvement here — the briefing's own warm figures (3.0–5.0x) overstated its
   device-bound result (1.79x), and the same will apply.
3. **Node-level**: only as a sanity check that nothing regressed, not as proof.
   Requires the disk quiesced — see #23, which is a measurement prerequisite as
   well as an optimization.

**A negative result** looks like: the harness shows no improvement because the
bottleneck is elsewhere in the read path — the LevelDB lookup preceding the
Sharky read, for instance. That would redirect effort to `retrievalIdx` rather
than invalidating the approach.

**This change alone will not speed up sampling.** The sampler offers only
`NumCPU` concurrent reads (#9), which is below Sharky's *current* ceiling of 4.
Raising the ceiling without raising the offered concurrency measures nothing.
The two must land together for a node-level effect; they are separable only in
the harness, which drives concurrency directly.

## Rollout and rollback

No configuration, no migration, no persisted state. Reverting the commit fully
restores previous behaviour.

## Upstream portability

Self-contained within `pkg/sharky`, no format change, and it addresses a
limitation upstream can measure for themselves with the same harness. A strong
upstream candidate if it works — but it touches core storage in a project that is
conservative there for good reason, so it should carry the harness numbers and
real-node evidence when offered.

Generated with help of AI.
