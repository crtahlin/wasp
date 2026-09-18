# What ends a truncated provider download

Issue: [#343](https://github.com/crtahlin/wasp/issues/343), step 1 of
[retrieval-rate.md](retrieval-rate.md), which said nothing further should be
specified until this was answered.

Measured 2026-09-18 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester. Harnesses `cp290/t12b.sh`, `t12c.sh` and `t12d.sh`,
outside this repository. Sole-source content from local ingest
([#326](https://github.com/crtahlin/wasp/issues/326)), lookahead buffer 0, so
nothing else on the network could serve it.

**The requester gives up on a chunk after asking about thirty-four peers that do
not hold it, having stopped asking the one peer that does.** One such chunk
fails its read unit, and `joiner.ReadAt` is all or nothing, so the download
truncates there.

## Method

The requester's own debug logging for `node/retrieval` was raised at run time
through the `/loggers` API, so no restart was needed and node state was not
disturbed. Every line the retrieval path emitted during each download was then
read back from the node's journal for exactly that window.

Chunks that ended in `context canceled` are excluded throughout. Those are
consequences of the download having already stopped, not causes, and an earlier
pass of this analysis picked one up by accident and drew the wrong conclusion
from it.

## What the log says

Three runs, one fresh 4,194,304-byte file each.

| Run | Delivered | Chunks that failed | Provider asked for them | Mean attempts per failed chunk | Chunks retrieved | Retrieved from the provider |
|---|---|---|---|---|---|---|
| 1 | 1,736,704 | 1 | 1 | 34.0 | 478 | **478** |
| 2 | 1,146,880 | 2 | 0 | 34.0 | 324 | **324** |
| 3 | 917,504 | 2 | 0 | 34.0 | 261 | **261** |

Four things follow, and the first two are the finding.

**Every chunk that succeeded came from the provider.** 478 of 478, 324 of 324,
261 of 261. The provider is not failing, slow, or out of content. It serves
everything it is asked for.

**The chunks that fail are the ones it is not asked for.** In four of the five
failures across these runs the provider does not appear among the attempts at
all. Here is one such chunk in full, each line an attempt against a different
peer:

```
failed to get chunk -> 09c7917e1f790914
failed to get chunk -> 600495fde6069d02
failed to get chunk -> 16391cd116ed2391
   ... thirty-four in total, none of them the provider ...
failed to get chunk -> 53ba0a913780894a
retrieval failed [storage: not found]
```

**The attempt count is quantised at 34.0 in every run.** `maxOriginErrors` is 32
(`pkg/retrieval/retrieval.go:155`), and `errorsLeft` is decremented once per
failed result (`:408`). Thirty-four attempts ending in `storage: not found` is
that budget being spent, to the chunk.

**The branch that would wait for credit never fires.** `sleeping to refresh
overdraft balance` (`retrieval.go:331`) appears **zero** times across all three
runs, and so does `no peers left`. That branch sits behind
`errors.Is(err, topology.ErrNotFound)`, which means `closestPeer` has no
unskipped peer left. With more than a hundred connected peers and a budget of
32, the budget goes first, every time, in this regime.

## What this settles

- **The download does not stop for want of credit at the moment it stops.** It
  stops because a chunk was asked of peers that cannot answer until its budget
  ran out. Credit may be why the holder left that chunk's candidate list, which
  is the open question below, but the failure itself is an exhausted retry
  budget against the wrong peers.
- **An earlier claim in [#343](https://github.com/crtahlin/wasp/issues/343),
  withdrawn as unverified, is now measured and holds in this regime.** That
  claim was that the credit wait cannot be reached because the error budget runs
  out first. A review correctly said it was asserted rather than checked, and
  that it is reachable in general. It is reachable in general and it was reached
  zero times here.
- **It also explains why the buffer matters without the buffer being the
  cause.** A larger read unit contains more chunks, so it is more likely to
  contain one that has lost its holder, and one is enough.
- **It is consistent with `t7-cold`**, where downloads failed at a balance of
  zero with maximum headroom. A chunk that has stopped asking its only holder
  fails whatever the balance is.

## What this does not settle

**Why the holder leaves a chunk's candidate list is not established.** Three
mechanisms in the code could do it and the logs here do not separate them:

- `maxOverdraftReadmits = 8` (`retrieval.go:159`): after eight credit refusals
  of one chunk the preferred peer is dropped from that chunk's candidates.
- `s.errSkip` (`:122`) is service-wide with a one minute life, so a peer that
  fails a chunk once is excluded from it for a minute.
- `preferredCandidates` (`preferred.go:184`) excludes anything in the skip list
  and anything not reported as a connected full node.

Separating them needs a log line at the point the candidate is dropped, which
does not exist today. That is the next step and it is small.

Also not settled: **one of the five failures did have the provider among its
attempts**, and it still failed. One instance is not enough to say whether that
is a different path or the same one seen a beat earlier.

And nothing here touches **why buffer 0 runs at about 263,000 B/s** when there
is no credit pressure at all. This explains where a download stops, not the rate
it runs at until then.

## What follows

The shape of a fix is now constrained by evidence rather than guesswork, and the
constraint kills the design this project already withdrew once. That design
would have waited for credit at the point the loop gives up. **By then the
provider is not in the candidate list**, so there would be nothing to retry and
the wait would be spent before falling back to the same peers that do not have
the chunk.

What the evidence points at instead is keeping the only holder available to a
chunk that nothing else can serve. That is the same family as
[#313](https://github.com/crtahlin/wasp/issues/313) and
[#324](https://github.com/crtahlin/wasp/issues/324), and #324's readmit path is
already most of it, bounded at eight.

No design is proposed here. The next measurement is the one named above:
instrument the point at which a preferred peer is dropped from a chunk, and read
which of the three mechanisms does it.

## Upstream portability

`maxOriginErrors`, `errSkip`, the error budget loop and the unreachable-in-practice
wait branch are all unmodified upstream code, checked against `upstream/v2.8.2`.
`maxOverdraftReadmits` and `preferredCandidates` are fork code from #324 and
#290.

No `affects-upstream` marker is claimed. The behaviour is reproduced and its
cause within the retrieval loop is now known, but which mechanism drops the
holder is not, and a defect report that cannot name the mechanism is the kind of
weak member rule 11 warns about. It becomes appropriate once the next
measurement lands.

---

Generated with help of AI.
