# A chunk should be able to use a provider that connects after its flight starts

Issue: [#435](https://github.com/crtahlin/wasp/issues/435).
Type: fix.

## Problem

Measured, in
[`preferred-cold-start.md`](preferred-cold-start.md): on the first hinted
download after a disconnect, **the download's own chunk is asked of more than
thirty ordinary peers and never once of the provider**, which is connected in
Kademlia from 0.38 s of a 2.34 s request and holding the content throughout.
The answer is 404.

A chunk's preferred candidate list is built once, before the retry loop, and
never rebuilt inside it:

```go
// pkg/retrieval/retrieval.go:232
candidates := s.preferredCandidates(preferredPeers, chunkAddr, s.errSkip.ChunkPeers(chunkAddr))
```

`preferredCandidates` keeps only peers already connected in Kademlia
(`preferred.go:202`, `:222-228`). The content chunk's flight starts at about
t=0, before the dial the same request began has landed, so its list is empty
and stays empty for that chunk's whole retry budget. For sole-source content
that chunk is the root, and without the root there is nothing else to fetch,
which is why the answer is 404 rather than a truncated body.

`Discover` has the same gap by construction: it adds providers to the preferred
set part way through a download, and a chunk already in flight cannot see them,
because `preferredPeers` is a snapshot taken before the flight
(`retrieval.go:201-202`).

**Two explanations for this issue have already been withdrawn**, and the
measurement that settles it also settles them: the dial is not the problem,
since the provider is eligible for 84% of the request; and #438's fix, which
stops an in-flight delivery being discarded, does not change the symptom,
because the delivery is never requested.

## The change

Rebuild the candidate list when it runs out, from the **live** preferred set,
and never offer the same peer twice in one flight.

```go
// when len(candidates) == 0, before falling through to ordinary selection
if preferredSet != nil {
    fresh := s.preferredCandidates(
        preferredSet.Peers(),                    // live, so Discover is visible
        chunkAddr,
        append(skip.ChunkPeers(chunkAddr), s.errSkip.ChunkPeers(chunkAddr)...),
    )
    for _, p := range fresh {
        if _, seen := offered[p.ByteString()]; !seen {
            offered[p.ByteString()] = struct{}{}
            candidates = append(candidates, p)
        }
    }
}
```

`offered` is a per-flight set seeded with the initial candidates.

Reading `preferredSet.Peers()` rather than the `preferredPeers` snapshot is
what closes the discovery gap, and it is the reason the rebuild is not simply a
retry of the same slice. Checked rather than assumed: `PreferredSet` guards
`peers` with a mutex, `Add` appends to it, and `Peers` builds a fresh slice
from it on every call (`preferred.go:55-103`), so a provider appended after the
flight started is visible. `Peers` also drops peers currently demoted for
repeated misses, which the snapshot path never re-evaluated either.

The combined skip is the same expression ordinary selection already uses a few
lines below (`retrieval.go:369`), so this is not a new idea about what to
exclude, only the same one applied to the preferred list, which line 232 never
did.

### Why this terminates, which is where the previous design failed

A [previous design for this issue](https://github.com/crtahlin/wasp/issues/435)
was withdrawn before any code because it would have livelocked: a preferred
miss returns through the `res.preferred` arm **before** both `errorsLeft--` and
`s.errSkip.Add`, so it spends no error budget and is recorded in neither skip
list, and a rebuild would re-offer the peer it had just tried, for ever, with
no exit but the request context.

Two independent things stop that here.

**The per-flight skip list already records every preferred peer that is
dispatched.** `retrievePreferred` calls `skip.Forever(chunkAddr, peer)` before
it dispatches (`preferred.go:253`), and `Forever` is `Add` with a maximum
duration (`pkg/skippeers/skippeers.go:59-61`), so `skip.ChunkPeers(chunkAddr)`
returns it for the rest of the flight. The withdrawn design did not see this
because line 232 passes only `s.errSkip`; the peer was recorded all along, in
the list the candidate builder was not reading.

**`offered` makes it monotone anyway, and that is the load-bearing half.**
Relying on the skip list alone would not be enough: a peer refused credit is
added with `overDraftRefresh` rather than for ever (`preferred.go:245`), so it
becomes re-offerable once that expires, and a peer dropped past its
`providerWait` window would then be re-offered and dropped again on a cycle. It
would be throttled by `overDraftRefresh` rather than spinning, but it would
keep a preferred candidate present indefinitely, and that has a cost described
below. `offered` removes the question: **at most one entry per peer in the
preferred set ever enters the loop**, so the number of rebuilds that add
anything is bounded by the size of that set, which for a hint is a handful.

A third guard comes free and is worth naming because it is easy to remove by
accident: `Peers` already excludes peers demoted for repeated misses, so a
provider that keeps failing leaves the rebuild's input on its own.

This does give up one behaviour deliberately: a peer refused credit and later
dropped past `providerWait` is not offered again in the same flight. That is
the #324 and #392 retention window ending, which is what it is for.

## What it costs

**The ordinary peer walk gets wider, and other operators pay for it.** The
error budget is suspended while a preferred candidate is present
(`retrieval.go:461-466`), so a rebuild that finds a candidate extends the
window in which a chunk keeps being asked of ordinary peers.
`docs/DIFFERENCES.md` already records roughly a fourfold rise in outbound
retrieval requests for a chunk on a 150-peer node for the neighbouring guard,
and [`flight-exit-results.md`](flight-exit-results.md) measured the addition
from #438 at a ratio of 0.97, which is to say none, because the provider serves
essentially every chunk on its first attempt. Neither of those measured a
rebuild, so **this cost has to be measured here and not inherited.**

The bound is what makes it arguable rather than open ended: the extra candidates
are at most the preferred set, once each.

**A request carrying no hint is untouched.** `preferredSet` is nil unless the
request carried `Wasp-Providers` or discovery found something, so a forwarder
and an ordinary download run exactly as before, and a stock Bee peer sees no
difference.

## Rejected: wait for the dial before retrieval starts

This is the issue's own first suggestion, and it is rejected on the
measurement rather than on taste.

It would fix the measured case, because the dial lands at 0.38 s. But the
provider was **already connected for 84% of that request**, so the time was
never missing; what was missing was a second look. A wait puts a fixed cost on
every hinted request to buy something the request already had. It also does
nothing for discovery, where the provider appears part way through a download
by design, and it has to decide what an unreachable hint costs, which a rebuild
never has to ask because it only ever adds peers that are already connected.

## Protocol impact

None. Requester-side selection, no wire component, no change to
`.github/protocol-freeze.lock`.

## Observability

One counter, `PreferredRebuilds`, incremented when a rebuild adds at least one
candidate. Without it an operator cannot tell this path from the first build,
and the existing counters cannot: `PreferredCandidatesSelected` is once per
flight, and `PreferredAttempts` is capped by credit.

The measurement that produced this issue needed exactly such a counter and did
not have one, which is why two explanations survived as long as they did.

## Tests

In `pkg/retrieval`, mutation checked.

- **A provider that becomes connected after the flight starts serves the
  chunk.** This is the defect; it fails today. The test connects the peer after
  the first ordinary sweep has begun.
- **A provider added to the preferred set after the flight starts is used**,
  which is the discovery half and which a rebuild from the snapshot would not
  satisfy. It fails if the rebuild reads `preferredPeers` instead of
  `preferredSet.Peers()`.
- **A peer already dispatched in this flight is not offered again**, driven by
  a provider that misses. Without `offered` and without the skip list this is
  the livelock; the test asserts a bounded number of attempts, not just that
  the call returns.
- **A peer refused credit and dropped past `providerWait` is not re-offered**,
  so the retention window still ends.
- **A request with no preferred set behaves exactly as before**, asserted on
  the outbound request count, so ordinary retrieval is untouched.
- **A reference nobody holds still fails quickly**, from a fresh relationship
  and a warm one, which is the guard against trading a 404 for a hang.

Mutations that must each break a named test: remove the rebuild; rebuild from
the snapshot rather than the live set; drop the `offered` check; drop the
per-flight `skip` from the combined skip; rebuild unconditionally rather than
only when the list is empty. A mutation that fails to compile proves nothing
and is redone.

## Measurement

Rule 7 applies: bench, provider and requester, three runs per condition, spread
reported, node state matched, arms interleaved.

**The acceptance criterion is delivered bytes and a matching checksum, never a
counter.** `gate-terms-measured.md:204-209` records that widening a window can
let the debt rise to meet it and deliver no more bytes, and this issue is
itself a case where counters pointed away from the defect twice.

| arm | question | reject if |
|---|---|---|
| 1 | Does the first hinted download after a disconnect complete? 4 MiB sole-source, redundancy NONE, three runs | any run returns 404 |
| 2 | Is a warm hinted download unchanged? Same content, provider already connected | rate outside the control's spread |
| 3 | Is an unhinted download unchanged? Network-held 16 MiB | rate outside the control's spread |
| 4 | How much wider is the ordinary walk? `peer_request_count` over `request_attempts_count`, hinted, both builds | recorded, not a reject; a large rise reopens the cap question |
| 5 | Does a reference nobody holds still fail fast? Fresh and warm relationship | any run hangs past the control |

Arm 1 is the one that fails today, 404 in every one of the fifteen runs
recorded in `preferred-cold-start.md`.

## Rollout and rollback

No configuration, no migration, no on-disk change. Rollback is reverting the
merge commit.

## Upstream portability

**Not applicable.** `preferredCandidates`, `PreferredSet` and the whole
preferred path are this fork's own and have no upstream counterpart, so the
issue carries no `affects-upstream` label.

## Files

- `pkg/retrieval/retrieval.go`, the retry loop and the per-flight `offered` set.
- `pkg/retrieval/metrics.go`, `PreferredRebuilds`.
- `pkg/retrieval/preferred.go` if `preferredCandidates` needs the combined skip
  rather than the caller assembling it.
- `docs/DIFFERENCES.md`: a hinted download uses a provider that connects after
  it starts, where before it did not.
- `docs/experiments/INDEX.md`: the ledger row.

Generated with help of AI.
