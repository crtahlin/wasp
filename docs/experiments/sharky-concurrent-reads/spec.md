# Concurrent reads in Sharky

Issue: [#8](https://github.com/crtahlin/wasp/issues/8)

Status: parked, pending [#9](https://github.com/crtahlin/wasp/issues/9). See the
history section for why. This document is the optimization's design and its
corrected record, not a merged change.

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
files scales 6.3x.

## Hypothesis

The per-shard goroutine exists to serialise **free-slot allocation for writes**,
not to make reads safe. Reads can bypass the actor entirely.

- `shard.read` does nothing but `sh.file.ReadAt(r.buf, sh.offset(r.slot))`.
  `ReadAt` compiles to `pread`, which neither uses nor mutates the shared file
  offset and is safe for concurrent use on the same descriptor.
- The shard data file is a plain `*os.File`, and nothing repositions it.

## History, and the correction that matters

This has a misattribution in its record, and the correction has to travel with the
design so it is not repeated a third time.

**First attempt.** The change was implemented and merged ([#63](https://github.com/crtahlin/wasp/issues/63))
and then reverted ([#66](https://github.com/crtahlin/wasp/issues/66)) on the belief
that it crashed bench-1 with Go runtime heap corruption
(`fatal error: s.allocCount != s.nelems`).

**That diagnosis was wrong.** The crash was **SIMD memory corruption**
([#92](https://github.com/crtahlin/wasp/issues/92)): the SIMD assembly stub ran
foreign code on the goroutine stack and passed Go pointers to it, corrupting memory
that then failed wherever it was next touched — "nine distinct runtime assertions
across seven subsystems," the allocator among them. The `allocCount` assertion the
revert pinned on this change was one of those, and the corruption is layout
sensitive, which is why it was easy to blame the nearest concurrency change. After
the SIMD fix, the concurrent-reads change was re-measured end to end and showed
**no measurable node-level change** (1.2% either side of the baseline, inside the
noise).

**A second attempt was made and reverted too.** A re-approach added a per-shard
`RWMutex` read-and-close guard, presented as the fix for a use-after-close that had
crashed the node. That premise inherited the same misattribution: the node crash
was SIMD, not a read/close race. On the real `*os.File` backend, Go's own file
descriptor accounting already serialises `Close` against an in-flight `ReadAt` and
returns a clean error after close, so there is no use-after-close to fix there. The
guard was defensive and backend-generic, not a crash fix, and the change was
reverted to restore this parked state rather than keep it on a false justification.

## Why it stays parked

The harness gain is real and large, but it **does not translate to a node**, for a
reason the design cannot remove on its own: the sampler offers only `NumCPU`
concurrent reads ([#9](https://github.com/crtahlin/wasp/issues/9)), which is at or
below Sharky's current ceiling. Raising the ceiling while the caller offers no more
concurrency measures nothing. The two must land and be measured together, or the
result is a false negative. Until [#9](https://github.com/crtahlin/wasp/issues/9)
provides the offered concurrency and a matched before-and-after on the bench shows a
node-level effect, this is a 10x microbenchmark gain that moves nothing real, and it
is not worth adding read/write concurrency to core storage for that.

## The genuine correctness item, if this is ever reinstated

One real defect surfaces only once reads are concurrent, and any re-attempt must
carry it: the in-memory backend used by the ephemeral store
(`afero.NewMemMapFs()`) implements `ReadAt` by seeking a shared offset, so it is not
safe for concurrent use, unlike a real `*os.File`. Concurrent reads over it race and
can return wrong bytes; the race detector reports it in the `pkg/storer` sampler
tests. It needs a serialising wrapper on that backend. It is only needed because of
concurrent reads, so it belongs with them and not before.

## Protocol impact

None. No on-disk format change, no wire change, nothing in
`.github/protocol-freeze.lock`.

## If reinstated: the gates

1. Land with [#9](https://github.com/crtahlin/wasp/issues/9) and show a node-level
   before-and-after on the bench, three matched runs per condition, or it stays out.
2. Include the in-memory-backend wrapper above.
3. A real-node run remains sensible defence, but note that it can only show the code
   is stable, not that the guard earns its place — the unguarded post-SIMD code was
   stable too. The node-level *measurement* is the gate, not the absence of a crash.

Generated with help of AI.
