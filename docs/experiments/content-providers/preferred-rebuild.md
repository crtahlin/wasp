# A chunk should be able to use a provider that connects after its flight starts

Issue: [#435](https://github.com/crtahlin/wasp/issues/435).
Type: fix.

## Problem

[`preferred-refresh.md`](preferred-refresh.md) recorded the previous design's
withdrawal and said no change was proposed yet, and that the mechanism was not
established. **This spec supersedes it on both counts**, with the measurement
in [`preferred-cold-start.md`](preferred-cold-start.md).

Measured there: on the first hinted
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
// when len(candidates) == 0, before falling through to ordinary selection.
// peers is the live set; an empty one is the common case on a
// providers-enabled node and must not cost a rebuild. See the guard note.
if peers := preferredSet.Peers(); len(peers) > 0 {
    fresh := s.preferredCandidates(
        peers,                                   // live, so Discover is visible
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

**The nil test is load bearing**, and an earlier revision of this spec said the
opposite, that the enclosing `origin && s.providers.Load()` block already
establishes it. That block does not enclose the retry loop. `withProviders`
returns early when providers are disabled (`pkg/api/providers.go:71-74`), and
`POST /pins`, `GET /soc` and the access-control paths reach `RetrieveChunk` as
origin without ever calling it, so a set really can be absent here and removing
the test would dereference nil.

Reading `preferredSet.Peers()` rather than the `preferredPeers` snapshot is
what closes the discovery gap, and it is the reason the rebuild is not simply a
retry of the same slice. Checked rather than assumed: `PreferredSet` guards
`peers` with a mutex, `Add` appends to it, and `Peers` builds a fresh slice
from it on every call (`preferred.go:55-103`), so a provider appended after the
flight started is visible. `Peers` also drops peers currently demoted for
repeated misses, which the snapshot path never re-evaluated either.

The combined skip is the same expression ordinary selection already uses a few
lines below (`retrieval.go:445`), so this is not a new idea about what to
exclude, only the same one applied to the preferred list, which line 232 never
did.

**The singleflight key is not recomputed, and that is deliberate.**
`flightRoute` embeds a fingerprint of `preferredPeers` taken before the flight
(`retrieval.go:206-211`), so reading the live set inside the loop makes the key
describe a set the flight no longer has. `preferred-refresh.md` raised this
against the earlier design and it is answered here rather than left: the key's
job is to join concurrent requests for the same chunk with the same hint, and
joining a request that started with a narrower set to one that has since
discovered more is the outcome that serves the caller. Recomputing the key
mid-flight would instead split a flight from itself. The implementation must
not move the fingerprint, and a test should pin that two requests with the same
hint still share one flight after a rebuild.

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

**`offered` makes it monotone**: at most one entry per peer in the preferred
set ever enters the loop, so the number of rebuilds that add anything is
bounded by the size of that set.

**How big that set can get is worth stating exactly, because an earlier
revision of this spec got it wrong.** It said "at most 8 for an explicit hint
and about 16 once discovery has run", as though those were alternatives with a
small ceiling. They are not:

- An explicit hint is capped, at `maxProviderHints = 8`
  (`pkg/api/providers.go:31`).
- A discovered set is **not** capped. `Discover` runs once per download, adds
  up to `lookupCandidates = 16` (`pkg/providers/providers.go:35`), and
  `PreferredSet.Add` appends anything not already present with no size limit.
  The set is **shared by every download of the same content key** while it
  lives (`providerSet`, with a time to live), so successive downloads whose
  lookups return different providers accumulate into one set.
- A hinted download can also discover, so the two add rather than exclude.

So the honest bound is the size of the live preferred set, which is 8 for a
hint-only download and, for a discovery-backed one, however many distinct
providers discovery has returned for that key within the set's lifetime. That
is small in every case measured so far and it is not a fixed ceiling. If the
walk-widening arm shows it matters, the cap belongs on the set rather than on
the rebuild, because a set that large is a problem for the first build too.

**These two overlap, and the mutation matrix is what says how.** Removing
either one alone changes no test; removing both fails the termination test.
An earlier revision called `offered` "the load-bearing half" and then, after
the first matrix, called the two "independent", and **neither is right**.
`offered` strictly dominates: `skip.Forever` covers dispatched peers, and
`offered` covers those and the credit-refused peer whose `overDraftRefresh`
expires.

**And removing both does not produce a livelock**, which an earlier revision
also claimed. Measured, it fails on the attempt count in well under a second,
because a third guard stops it: `PreferredSet` demotes a peer after
`demoteAfterMisses = 16` misses and `Peers` then omits it. So the flight always
ends; what the two guards buy is that it ends after one attempt rather than
sixteen.

`offered` does cover one case the skip list does not, and **that case has no
test**, which is recorded here rather than left to be found later. A peer
refused credit is added with `overDraftRefresh` rather than for ever
(`preferred.go:245`), so once that expires the skip list would let it back, and
a peer already dropped past its `providerWait` window would then be re-offered
and dropped again on a cycle, throttled by `overDraftRefresh` rather than
spinning. Covering it needs a test that drives a credit refusal and then
advances past `overDraftRefresh`, which the accounting mock in this package
does not make convenient. Until such a test exists, `offered` is justified by
reading the code and not by a failing test, and it is kept because the hazard
it covers is real even though the cheaper guard happens to mask it today.

A third guard comes free and is worth naming because it is easy to remove by
accident: `Peers` already excludes peers demoted for repeated misses, so a
provider that keeps failing leaves the rebuild's input on its own.

This does give up one behaviour deliberately: a peer refused credit and later
dropped past `providerWait` is not offered again in the same flight. That is
the #324 and #392 retention window ending, which is what it is for.

## What it costs

**The ordinary peer walk gets wider, and other operators pay for it.** The
error budget is suspended while a preferred candidate is present
(`retrieval.go:557`), so a rebuild that finds a candidate extends the
window in which a chunk keeps being asked of ordinary peers.
`docs/DIFFERENCES.md` already records roughly a fourfold rise in outbound
retrieval requests for a chunk on a 150-peer node for the neighbouring guard,
and [`flight-exit-results.md`](flight-exit-results.md) measured the addition
from #438 at a ratio of 0.97, which is to say none, because the provider serves
essentially every chunk on its first attempt. Neither of those measured a
rebuild, so **this cost has to be measured here and not inherited.**

The bound is what makes it arguable rather than open ended: the extra candidates
are at most the preferred set, once each.

**The guard is about the set's contents, not its presence**, and an earlier
revision of this spec had that wrong. It said `preferredSet` is nil unless the
request carried `Wasp-Providers` or discovery found something. It is not:
`withProviders` builds one unconditionally (`pkg/api/providers.go:93-98`) and
attaches it to every origin download, so on a providers-enabled node the set is
present and usually empty. A guard on `preferredSet != nil` would therefore run
the rebuild on every retry iteration of every unhinted download.

What that costs is small but must be stated rather than assumed: one mutex
acquisition and one zero-length allocation per iteration for an empty set, and
for a non-empty one a `connectedFullNode` call per peer, which is a Kademlia
lookup. With `maxOriginErrors = 32` and the preemptive ticker that is tens of
lookups per chunk in the worst case. The rebuild therefore runs only when the
set actually has peers.

**A forwarder is untouched** whatever the guard: `origin` is false for it, so
`preferredPeers` is never populated and no preferred path runs. A stock Bee
peer sees no difference either way.

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

The mutation matrix, run rather than predicted, with the survivors reported:

| mutation | result |
|---|---|
| remove the rebuild | fails both defect tests |
| rebuild from the snapshot rather than the live set | fails the discovery test |
| drop the `offered` check | **survives** |
| drop the per-flight `skip` from the combined skip | **survives** |
| drop **both** of the above | fails the termination test |
| drop the per-chunk cap from the entry test | **survives** |
| drop the per-chunk cap from the append loop | **survives** |
| drop **both** cap tests | fails the cap test |
| drop the `PreferredRebuilds` increment | fails the counter test |
| rebuild unconditionally rather than only when the list is empty | **survives** |

Four survivors, in two pairs plus one. Each pair is two guards that overlap, so
removing either alone leaves the other; the paired mutation is the
discriminating one. The last, rebuilding unconditionally, is an equivalent
mutant for behaviour: it appends the same peers earlier and changes only cost,
which no functional test can see.

A mutation that fails to compile proves nothing and is redone.

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
| 4 | How much wider is the ordinary walk? See the note below on what this can and cannot measure | recorded, not a reject; a large rise reopens the cap question |
| 5 | Does a reference nobody holds still fail fast? Fresh and warm relationship | any run hangs past the control |

Arm 1 is the one that fails today, 404 in every failing run recorded in
`preferred-cold-start.md`.

**Arm 4 needs a metric this repository does not yet have.**
`peer_request_count` is incremented at the top of the retry branch
(`retrieval.go:319`), before the candidates block, so it counts **loop
iterations** and not outbound requests to ordinary peers. A preferred dispatch
and a dropped candidate each consume one, so the ratio moves under this change
for reasons that are not a wider walk. `flight-exit-results.md` used the same
ratio, in a build with no rebuild, where that confound did not arise. So arm 4
either counts ordinary dispatches separately, which is a counter the
implementation should add next to `PreferredRebuilds`, or it is reported with
that caveat and treated as an upper bound.

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
