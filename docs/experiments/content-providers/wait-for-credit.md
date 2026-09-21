# Waiting for a provider's credit instead of asking peers that do not have the chunk

> **WITHDRAWN, 2026-09-21. Do not build on this document.** The design inferred
> from a single failed ordinary retrieval that the network did not hold a chunk.
> Nothing on the wire supports that: a failed retrieval arrives as an opaque
> string covering timeouts, dropped streams and the forwarder's own accounting
> refusal alike. The implementation could also livelock, because once the flag
> was set and a candidate remained, ordinary selection became unreachable and
> the error budget could not decrement.
>
> The truncation it was written for was mostly caused by the requester's
> chequebook being out of funds, so every cheque failed and settlement fell back
> to the time allowance alone. Funding it took the same 50 MB download from
> 17m29s to 16.8 s on the same build.
>
> The residue is specified in [provider-retention.md](provider-retention.md).

Issue: [#392](https://github.com/crtahlin/wasp/issues/392). Type: fix.

## Problem

Downloading content that only one node holds fails above about 1 MiB. Measured
on the bench: 32 KiB and 256 KiB complete; 4 MiB truncates at 786,432 to
1,310,720 bytes depending on the run. The provider holds the content, is
connected, and is not blocklisting. The limit is credit.

The same 4 MiB attempt, with the requester's retrieval log at V(1):

| line | count |
|---|---|
| `failed to get chunk` | 4,595 |
| `retrieved chunk` | 378 |
| `sleeping to refresh overdraft balance` | **0** |
| `no peers left` | **0** |

and the preferred counters: 2,565 overdrafts, 2,351 readmits.

Three things follow, and none of them were what the code intends.

**The wait for credit never runs.** `retrieval.go` sleeps for `overDraftRefresh`
only when `closestPeer` returns `topology.ErrNotFound`, meaning every peer has
been skipped. The requester has about 125 peers and only the 32 that actually
failed are added to `errSkip`, so `closestPeer` always has another peer to
return and that branch is unreachable. The count above is zero, not small.

**The error budget is spent on peers that cannot help.** 4,595 ordinary
retrievals failed. Each one decrements `errorsLeft`, which starts at
`maxOriginErrors`, 32, per chunk. For content only the provider holds, every
ordinary attempt is known in advance to fail: the chunk was never uploaded to
the network, so no forwarding path can end anywhere that has it.

**The provider is then dropped from chunks it is the only source for.**
Overdrafts exceed readmits by 214, which is the number of times a chunk hit
`maxOverdraftReadmits`, 8, and removed the provider from its candidate list.
After that the chunk has no source at all.

So a chunk whose only holder is briefly out of credit is abandoned, while the
mechanism written to wait for exactly that never gets a chance to run.

## Hypothesis

An ordinary peer failing is evidence about the content, not just about that
peer: if a forwarding retrieval cannot find the chunk, the network does not have
it. From that point on, trying more ordinary peers cannot succeed, and the only
thing that can is the provider regaining credit.

Waiting at that point should let the download complete at any size, bounded by
the request's own deadline rather than by a per-chunk error count.

## Design

Per chunk, in the singleflight closure that already holds `candidates`,
`readmits` and `errorsLeft`, add one piece of state:

```go
// An ordinary, forwarding retrieval has failed for this chunk, so the
// network does not hold it and only a preferred peer can serve it.
ordinaryFailed := false
```

It is set where an ordinary result fails, next to the existing `errorsLeft--`.

**Change 1: do not spend the error budget while a known holder is waiting for
credit.**

```go
errorsLeft--
```
becomes

```go
if len(candidates) == 0 {
    errorsLeft--
}
```

The budget exists to end a hopeless search. While a verified provider is still a
candidate for this chunk, the search is not hopeless, and counting these
failures ends it early. When the candidate list is empty the budget applies
exactly as before, so content the network does hold is unaffected.

**Change 2: once ordinary selection has failed, wait for the provider rather
than falling through to it again.**

In the overdraft branch, which today keeps the peer and falls through:

```go
case errors.Is(err, accounting.ErrOverdraft) && readmits[...] < maxOverdraftReadmits:
```

add, before it:

```go
case errors.Is(err, accounting.ErrOverdraft) && ordinaryFailed:
    // The network does not have this chunk: an ordinary retrieval already
    // failed for it. More ordinary attempts cannot succeed, so wait for this
    // peer's credit instead of spending the budget and the readmit cap on
    // peers that will not answer. Not counted as a readmit: the cap exists to
    // stop favouring a peer over alternatives, and here there are none.
    select {
    case <-time.After(overDraftRefresh):
        retry()
        continue
    case <-ctx.Done():
        return nil, ctx.Err()
    }
```

The existing readmit branch stays underneath it and keeps today's behaviour for
the case it was written for: the first overdraft on a chunk, before any ordinary
attempt has failed, still falls through to ordinary selection at once. That is
what [#324](https://github.com/crtahlin/wasp/issues/324) measured as three times
faster on content the network also holds, and it is unchanged.

**What bounds the wait.** The request context. A download that cannot make
progress ends when the caller gives up, which is the same bound any slow
download has. There is no new unbounded loop: each iteration sleeps
`overDraftRefresh`, 600 ms, so a stalled chunk costs one wakeup per 600 ms
rather than a spin.

**What is deliberately not changed.** `maxOverdraftReadmits` and
`maxOriginErrors` keep their values. The fix is about when they are consumed,
not how large they are, and raising them would not have reached the wait path
either.

## Protocol impact

None. No wire message, constant or version changes. The change is entirely in
how the requester schedules its own attempts. `make protocol-freeze` is expected
to pass unchanged and the `protocol-change` label is not applied.

## Measurement

Sole-source content ingested with `POST /wasp/ingest`, fetched with an explicit
`Wasp-Providers` hint so discovery is not involved, from a settled balance, with
the provider credit grant on.

**Arm 1, the sizes that fail today.** 4 MiB, three runs. Passes when all three
deliver 4,194,304 bytes with a matching SHA-256.

**Arm 2, the target size.** 50 MB, three runs, same criterion. This is the size
the work was asked for and is the arm that decides the issue.

**Arm 3, no regression on content the network holds.** The same file uploaded
normally with postage and fetched without a hint, three runs before and three
after, comparing wall time. Passes when the change is within the spread of the
control. This is the arm that protects #324's result, and a failure here is a
reject even if arms 1 and 2 pass.

**Arm 4, the budget still ends a hopeless search.** A reference that no node
holds, fetched with no hint. Passes when it fails in a time comparable to today
rather than hanging until the request deadline. Without this arm, change 1 could
turn every missing chunk into a long wait.

**What a negative result looks like.** Arm 1 or 2 still truncating means the
credit arriving during the wait is too slow to finish, and the answer is then
about settlement rate rather than scheduling. Arm 3 regressing means the wait is
being entered on content that does not need it.

## Rollout and rollback

No configuration. Rollback is `git revert` of the merge commit.

## Upstream portability

`maxOverdraftReadmits`, the preferred set and the candidate list are this fork's
(#299, #324), so change 2 has no upstream counterpart. Change 1 touches a line
that exists upstream, but its condition refers to fork state, so it is not
portable on its own and the issue is not tagged `affects-upstream`.

## Configuration

None.

## Files

- `pkg/retrieval/retrieval.go`, the two changes above.
- `pkg/retrieval/retrieval_test.go`, unit cover for: the budget is not spent
  while a candidate remains; the budget is spent when none remains; the wait is
  entered only after an ordinary failure.
- `docs/DIFFERENCES.md`, a row, since this changes what a node does.
