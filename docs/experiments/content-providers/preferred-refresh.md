# Why the first hinted request fails, and two designs withdrawn

Issue: [#435](https://github.com/crtahlin/wasp/issues/435).
Type: investigation. **No change is proposed yet, deliberately.**

This document was written as a spec for a fix. Review showed the fix would
livelock, and that the mechanism it was built on does not fit the evidence. Both
are recorded here rather than removed, because the measurement is worth keeping
and because the second design was mine and was wrong for reasons worth writing
down.

## What is measured

Five runs, `cp290/t435-dialtime.sh`. Each ingests fresh sole-source content on
the provider, disconnects the provider from the requester, then starts a poller
and issues one hinted download. The poller samples the requester's peer list
every 100 ms and records when the provider appears.

| run | request | provider in peer list |
|---|---|---|
| 1 | 404, 36 bytes, 3.127 s | 0.22 s |
| 2 | 404, 36 bytes, 3.099 s | 0.22 s |
| 3 | 404, 36 bytes, 3.075 s | 0.22 s |
| 4 | 404, 36 bytes, 3.116 s | 0.22 s |
| 5 | 404, 36 bytes, 3.100 s | 0.22 s |

The poller and the request run concurrently in the same run, so this is one
measurement rather than two laid side by side.

**Three limits on that table, all of which matter:**

- **0.22 s is an upper bound on a 100 ms grid**, not a stable value. It is the
  third poll in every run, so the connection landed somewhere in (0.11, 0.22].
  Five identical readings measure the poll interval.
- **The harness records no counters**, so it cannot attribute the connection to
  the hint. Kademlia's own connection loop could have dialed the peer after the
  `DELETE /peers`. `bee_providers_hinted_connects_started` exists and was not
  sampled. In [dial-race.md](dial-race.md) the attribution is supported by
  `bee_providers_connects_dialed` rising by one; here it is not.
- **`/peers` is libp2p's set; candidate selection reads kademlia's.**
  `connectedFullNode` asks `peerSuggester.ClosestPeer`
  (`pkg/retrieval/preferred.go:222-228`). Appearing in `/peers` is not the same
  event as becoming eligible, and the gap between them is not measured.

What the table does establish is that the request had **about 2.9 seconds** left
to run after the provider was connected, and still returned 404. Whatever the
cause, it is not that the download finished before the connection existed.

## Why ordinary selection did not use the provider either

Worth stating, because it is the first thing a reader asks. Once the provider is
connected it is an ordinary peer too, and `allowUpstream` is set for an origin
request, so nothing about proximity excludes it. It was still not used.

`closestPeer` (`pkg/retrieval/retrieval.go:585-597`) asks for
`Select{Reachable: true, Healthy: true}` first and only falls back to
`Select{Reachable: true}`, and then to a bare `Select{}`, when the previous ask
returns `ErrNotFound`. A peer connected a fifth of a second ago is not yet
marked reachable or healthy, and the fallback never runs while other reachable
peers remain. `connectedFullNode` uses a bare `Select{}`, so the preferred path
would see a peer that ordinary selection cannot.

That asymmetry is what makes the preferred path the one that has to work here.

## Design 1, from the issue: wait for the dial. Withdrawn

#435 proposed waiting for the hinted dial before retrieval begins, reasoning
that the download finishes before the dial lands. The measurement above refutes
the reasoning: there were about 2.9 seconds in hand. A wait would have made this
case pass without addressing why a connected provider went unused, and it would
block every hinted request including the common one where the provider is
already connected and the correct wait is zero.

## Design 2, mine: refresh the candidate list. Withdrawn

`candidates` is computed once per flight and never refreshed
(`pkg/retrieval/retrieval.go:232`). The four writes to it inside the loop are
the overdraft rotation (`:340`), the default-arm drop (`:348`) and the
successful-dispatch trim (`:353`). I proposed rebuilding the list when it runs
out, with a set of peers already dropped in this flight so the rebuild could not
re-add them.

Review found four reasons that does not work. All four are verified in the code.

**It livelocks on a hint naming a peer that does not hold the content.** The
dropped set I specified was scoped to the default arm. A peer that is
successfully dispatched is removed at `:353` instead, and a preferred miss
returns through

```go
				if res.preferred {
					// a miss at a preferred peer is not a peer error: it does
					// not use up an allowed error, and it does not skip the
					// peer for other chunks
					retry()
					continue
				}
```

which is reached **before** both `errorsLeft--` and
`s.errSkip.Add(chunkAddr, res.peer, skiplistDur)`. So a preferred miss spends no
error budget and is recorded in neither skip list. The cycle is: dispatch,
list empties, miss, retry, rebuild finds the same peer again, forever. The only
exit is the request context. Today this case fails fast. The change would remove
that, which is the same class of regression that ended
`fix/392-wait-for-credit` ([wait-for-credit.md](wait-for-credit.md)).

**It does not fix discovery, which I claimed it did.** `preferredPeers` is a
snapshot taken before the flight:

```go
// pkg/retrieval/retrieval.go:201-202
		if preferredSet = PreferredPeers(ctx); preferredSet != nil {
			preferredPeers = preferredSet.Peers()
		}
```

`Peers()` builds a fresh slice (`pkg/retrieval/preferred.go:88-98`). `Discover`
appends to the set, not to the snapshot, so rebuilding from `preferredPeers`
cannot see a discovered provider. Closing that gap means re-reading
`preferredSet.Peers()` inside the loop, which also brings the demotion filter
back into play and makes the singleflight key stale, since `flightRoute`
embeds a fingerprint of `preferredPeers`.

**Its stated hazard was wrong.** I wrote that the drop arm "records nothing in a
skip list" and that "`skip` and `s.errSkip` are consulted when the list is
built". Neither is true. `retrievePreferred` writes the peer to `skip` on both
paths, `skip.Add` with a 600 ms window on a credit refusal
(`pkg/retrieval/preferred.go:245`) and `skip.Forever` on dispatch (`:253`). And
line 232 passes only `s.errSkip.ChunkPeers(chunkAddr)`; the flight-local `skip`
is never given to `preferredCandidates`. The drop is recorded, in a list the
builder does not read, which is a different problem with a different fix.

**Its cost section was incomplete in the direction that matters.** The error
budget is suspended while a candidate is present:

```go
				if len(candidates) == 0 {
					errorsLeft--
				}
```

Keeping the list non-empty more often therefore suspends the budget more often,
and `docs/DIFFERENCES.md:157` already records what that costs and says it is
paid by other operators: on a node with 150 peers, roughly a fourfold rise in
outbound retrieval requests for a chunk, each costing the receiving peer a
forward attempt. I wrote only "one topology lookup per preferred peer per
refresh". The claim that a download with no hint "pays one integer comparison"
was also wrong as specified, since `len(candidates) == 0` is true on every
retry and the rebuild would run each time.

## The mechanism is not established

[dial-race.md](dial-race.md) records the failing run moving
`bee_retrieval_preferred_attempts` by **14** with **zero** hits and zero bytes,
in all three trials. Design 2's account says the first chunk is the root, it
never sees a candidate, and nothing else is attempted. That account cannot
produce 14 attempts.

The hint in those runs names **one** overlay, so `preferredCandidates` returns at
most one candidate and `PreferredAttempts` increments once per dispatch
(`pkg/retrieval/preferred.go:254`). Fourteen attempts therefore means at least
**fourteen flights** reached a non-empty candidate list. An earlier draft of
this document said "at least seven chunks", by dividing by
`maxPreferredAttempts`; that is the wrong arithmetic and the anomaly is twice
the size it claimed.

A competing account fits the data better and is not yet tested. The loop's
termination is `for errorsLeft > 0` (`:289`), checked at the top with **no
in-flight guard**, unlike the two other exits, which both test `inflight == 0`.
So a flight can dispatch to a preferred peer, empty its list at `:353`, resume
spending the error budget on ordinary peers because the list is now empty, reach
zero, return `storage.ErrNotFound`, and run `defer close(quit)` while the
preferred delivery is still outstanding. `retrieveChunk` then discards that
delivery on `quit`, so `preferredResult` never runs and `PreferredHits` never
moves. That produces attempts without hits and no bytes, including in the case
where the provider did return the chunk.

If that is the mechanism, rebuilding the candidate list is not sufficient, and
it might appear to work for the wrong reason: a non-empty list suspends the
error budget at the same test, which is also what produces the livelock above.

## What has to happen before any fix

1. **Settle the mechanism.** Instrument the flight exit and find out whether an
   in-flight preferred delivery is being discarded by `close(quit)`. Until that
   is answered, any fix is a guess, and the acceptance test of "delivered bytes
   with a matching checksum" cannot tell the two mechanisms apart.
2. **Re-run the dial measurement with counters**, at a finer poll interval, so
   the connection can be attributed to the hint rather than to kademlia, and so
   the number of the 32 errors already spent by the time the provider connects
   is known rather than inferred from a wall-clock total.
3. Only then design the change, and state its cost in terms of the error-budget
   suspension at `:464`, citing `docs/DIFFERENCES.md:157`.

## Protocol impact

None. Nothing is proposed.

## Upstream portability

Not applicable. `pkg/retrieval/preferred.go`, `pkg/api/providers.go` and
`pkg/providers` do not exist in `upstream/v2.8.2`, checked with `git ls-tree`.
The preferred path is this fork's own, added by
[#290](https://github.com/crtahlin/wasp/issues/290).
**No `affects-upstream` marker.**

Generated with help of AI.
