# Separate I/O from hashing in sampler phase 2

Issue: [#9](https://github.com/crtahlin/wasp/issues/9)

## Problem

`ReserveSample` (`pkg/storer/sample.go`) runs a single pool of
`max(4, runtime.NumCPU())` workers. Each worker does both halves of the job in
the same goroutine:

```go
chunk, err := db.ChunkStore().Get(ctx, chItem.Address)   // LevelDB lookup + sharky read
...
taddr, err := transformedAddress(hasher, chunk, chItem.ChunkType)   // CPU
```

Two consequences. Each worker spends most of its time blocked on disk with an
idle CPU. And the sampler offers only `NumCPU` concurrent reads to the layer
below, which on bench-1 is 8.

## Why this is the other half of #8

#8 removed Sharky's per-shard read serialisation. Measured in the harness, Sharky
went from flat at ~400,000 ops/s regardless of concurrency to scaling with it,
reaching 87–100% of raw `pread`:

| concurrency | before | after |
|---|---|---|
| 4 | 546,334 | ~2,790,000 |
| 8 | 391,738 | ~3,740,000 |
| 32 | 383,950 | ~3,940,000 |

**That ceiling is now unreachable from the sampler**, which offers 8 concurrent
reads. Neither change delivers a node-level improvement alone: #8 raised a
ceiling nothing reaches, and #9 without #8 would raise offered concurrency into a
ceiling that was flat from 4 upward.

## Design

Split the single pool into two, joined by a channel:

- **Readers** — a pool sized independently of `NumCPU`, doing only
  `ChunkStore().Get()` and forwarding the chunk.
- **Hashers** — a pool sized to `runtime.NumCPU()`, doing only
  `transformedAddress()` and sample insertion.

Sample selection takes the 16 smallest transformed addresses and does not depend
on processing order, so decoupling the stages is safe.

## Configuration (rule 8)

The reader pool size is a new tunable and is exposed as a config option from the
outset rather than hardcoded.

| | |
|---|---|
| flag | `sampler-read-concurrency` |
| default | `runtime.NumCPU()` — preserves today's offered concurrency |
| raising it costs | more concurrent disk operations; on a slow or contended disk this queues rather than helps, and competes with pullsync writes |
| lowering it costs | the sampler under-drives the storage layer and sampling takes longer, risking missing a redistribution round |
| costs other nodes | nothing directly — this is local I/O, not peer-facing |

Defaulting to `NumCPU` means **behaviour is unchanged unless an operator sets
it**, so this change is measurable as a pure A/B on one binary. Given the 2.23x
disk-time variance recorded in #13, being able to flip a setting rather than
rebuild is what makes the experiment tractable at all.

The hasher pool stays at `NumCPU` and is deliberately **not** exposed: it is
CPU-bound work on a known core count, so there is no operator judgement to make.
Rule 8 says expose what measurably matters, not everything.

## Protocol impact

**None.** `pkg/storer` internals; no wire surface, no on-disk format, no
migration. Sample output must be bit-identical for the same inputs, which the
existing `TestReserveSampler` covers.

## Measurement

**Harness cannot answer this one** — `beebench` exercises Sharky directly and has
no sampler. So this is measured on the node, which means contending with the
2.23x disk-time variance from #13.

Method:

1. Three runs at each of several `sampler-read-concurrency` values (default,
   4x, 8x), via `/rchash` using the node's own overlay as anchor1.
2. Compare `ChunkLoadDuration` per chunk iterated, not totals — `TotalIterated`
   varies between runs.
3. Matched conditions: same peer count, same storage radius, same interval after
   restart. See `docs/agent-playbooks/test-bench.md`.

**Success**: `ChunkLoadDuration` per chunk falls as read concurrency rises, and
wall-clock sample time falls with it.

**A negative result** looks like: raising read concurrency does not reduce disk
time per chunk, because the bottleneck is the LevelDB `retrievalIdx` lookup
rather than the Sharky read. `ChunkStore().Get()` does both, and #8 only fixed
the second. That outcome would redirect effort to #28 and #12 — and it is a
genuinely likely result, so it should not be treated as failure.

## Rollout and rollback

Config-only surface. The default preserves current behaviour; reverting the
commit removes the option entirely. No migration, no persisted state.

## Upstream portability

The pool split is self-contained in `pkg/storer/sample.go`. Offering it upstream
is more plausible than most of this backlog **because it is a config option with
their current value as default** — it changes nothing for existing operators
unless they opt in, which is the form Ethersphere is most likely to accept.

Generated with help of AI.
