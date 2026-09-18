# Why a sole-source provider download truncates

Issue: [#343](https://github.com/crtahlin/wasp/issues/343), step 1 of
[retrieval-rate.md](retrieval-rate.md).

Measured 2026-09-18 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester. Harnesses `cp290/t12b.sh` through `t12e.sh`,
outside this repository. Sole-source content from local ingest
([#326](https://github.com/crtahlin/wasp/issues/326)), 4,194,304 bytes at
redundancy level NONE, lookahead buffer 0.

## The mechanism

1. The provider is refused credit for a chunk, because the requester's debt to
   it is at the threshold it announced.
2. [#324](https://github.com/crtahlin/wasp/issues/324) keeps the provider for a
   later attempt instead of dropping it, **up to `maxOverdraftReadmits`, which
   is 8** (`retrieval.go:159`, `:280-296`).
3. On the refusal after that, the `default:` arm runs and
   `candidates = candidates[1:]` (`:299`). **The only node holding the chunk is
   now gone from that chunk's candidate list.**
4. The chunk falls to ordinary selection. The content is sole-source, so no
   ordinary peer can serve it.
5. It spends its error budget, `maxOriginErrors = 32` raised by up to
   `maxMultiplexForwards = 2` (`:155`, `:160`, `:356`), on about 34 peers that
   do not have it, and returns `storage: not found`.
6. `joiner.ReadAt` is all or nothing (`joiner.go:215-223`), so that one chunk
   fails its whole read unit and the download stops there.

The truncation points are exact multiples of the read unit, which is step 6
visible from outside.

## The evidence

### Credit refusals exhaust the readmit bound, and the count of that is the count of failures

Two runs, both truncating, with the counters read either side:

| | Run 1 | Run 3 |
|---|---|---|
| Delivered | 1,507,328 | 1,671,168 |
| `preferred_overdrafts` | +56 | +42 |
| `preferred_readmits` | +52 | +38 |
| **Overdrafts not readmitted** | **4** | **4** |
| `preferred_misses` | +2 | +2 |

`PreferredOverdrafts` counts every credit refusal on the preferred path
(`preferred.go:232`). `PreferredReadmits` counts only those where
`readmits[peer] < maxOverdraftReadmits`, meaning the peer was kept
(`retrieval.go:295`). **The difference is the number of refusals that dropped
the provider from a chunk**, because the only other route to the `default:` arm
is an error that is not an overdraft, and those are not counted as overdrafts.

Four in each run, and the same runs lose one or two chunks to
`storage: not found`, with a failing read unit cancelling others in flight.

### The provider is absent from the chunks that fail, and serves everything else

From a separate pass over three runs:

| Run | Delivered | Failed chunks | Provider among their attempts | Attempts per failed chunk | Chunks retrieved | From the provider |
|---|---|---|---|---|---|---|
| 1 | 1,736,704 | 1 | 1 | 34 | 478 | 478 |
| 2 | 1,146,880 | 2 | 0 | 34 | 324 | 324 |
| 3 | 917,504 | 2 | 0 | 34 | 261 | 261 |

A failing chunk, with the peers written as letters because they are addresses of
real nodes and rule 10 keeps those out of this repository:

```
failed to get chunk -> peer A
failed to get chunk -> peer B
failed to get chunk -> peer C
   ... thirty-four attempts, each to a different peer, none the provider ...
retrieval failed [storage: not found]
```

That excerpt is from a separate single-chunk run rather than from the three in
the table.

**The 478, 324 and 261 figures are close to tautological** and are reported for
what they are: the content is sole-source, so nothing else could have served it.
They confirm the setup was sound, not that the provider was healthy.

**The per-chunk attempt count is 34 where it can be read directly**, in the
single-chunk run and in run 1, which had one failed chunk. Runs 2 and 3 average
two chunks, so 34.0 there is a mean and an earlier draft of this document called
it quantised, which the data does not support.

### What is ruled out

**Demotion after repeated misses is ruled out, from data recorded before this
question was asked.** `PreferredSet.miss` drops a peer for ten minutes after
`demoteAfterMisses`, 16, consecutive misses (`preferred.go:38-41`, `:115-137`),
and the set lives for one HTTP request, so it would end a download. Across all
eleven runs of the size sweep, completing and truncating alike,
`preferred_misses` rose by **1 or 2**, never near 16. It never fires.

## Corrections to the earlier draft of this document

- **It said "the download does not stop for want of credit".** That is wrong.
  Credit is the first step of the chain above. What is true is narrower: at the
  moment the chunk fails, the failure is an exhausted retry budget against peers
  that cannot help, and adding credit at that point would not be reached.
- **It treated the zero count of the credit-wait log line as evidence about
  credit.** It is not. That branch requires every peer to be skipped, so zero is
  near enough guaranteed here, and credit refusal is routed around it with no
  log line at all (`retrieval.go:280-296`, `:360-365`). The zero count is
  consistent with no credit pressure and with heavy credit pressure alike, and
  the counters above are what distinguish them.
- **It said no instrumentation existed for the drop and that a new log line was
  needed.** Seven counters already existed and answered it without any code
  change.
- **It listed three candidate mechanisms and missed two**, including the
  candidate consumption that turns out to be the answer.
- **It cited the wait branch at `retrieval.go:331`.** It is at `:330`.
- **It did not reconcile 34 against `maxOriginErrors = 32`.** The two extra come
  from `maxMultiplexForwards`.
- **It quoted four peer overlay addresses**, against rule 10, now redacted.

## What this does not settle

- **The three runs in the table fall monotonically**, 1,736,704 then 1,146,880
  then 917,504, ten seconds apart with no reset. That is accumulating node state
  across runs, which rule 7 exists to catch, and they are not interchangeable
  samples.
- **Two runs for the counter evidence, not three.** The third was lost to a
  transient failure of an `ssh` invocation, the same fault that has eaten a
  whole arm twice in this project.
- **The gap of 4 is not tied chunk by chunk to the failures.** The counters are
  node-wide and the failures are per download; that they agree in magnitude is
  strong but it is not a per-chunk match.
- **Why the provider is refused in the first place** is the balance sitting at
  the announced threshold, which [#327](https://github.com/crtahlin/wasp/issues/327)
  raises and which its measurement shows is not enough on its own.
- **Nothing here explains the rate** at buffer 0 with no credit pressure. That
  remains the other half of #343.

## What follows

The bound is the lever. `maxOverdraftReadmits = 8` is what decides whether a
chunk keeps its only holder, and the same number governs both cases the
requester cannot tell apart: content the network also holds, where giving up on
the provider quickly is right, and sole-source content, where it is fatal.

That the requester cannot tell them apart is the same difficulty
[#324](https://github.com/crtahlin/wasp/issues/324) recorded. What is new is
that the boundary now has a number on it and a counter that shows when it is
crossed.

No design is proposed here. The measurement that should come before one is
whether a larger bound completes these downloads without the 3x regression on
widely-held content that the first attempt at #324 produced.

## Upstream portability

The readmit bound and the preferred path are **fork code**, from #324 and #290.
Unmodified upstream drops the candidate on the first refusal, with no readmit at
all, so upstream is worse in this case rather than better, and this fork already
improved it once.

`maxOriginErrors`, `maxMultiplexForwards`, the error budget loop and the all or
nothing `joiner.ReadAt` are unmodified upstream code, checked against
`upstream/v2.8.2`.

**No `affects-upstream` marker.** The chain runs through fork-authored code, and
the upstream parts of it behave as designed rather than in error.

---

Generated with help of AI.
