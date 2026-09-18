# Extending a larger payment threshold to one peer at a time

Issue: [#327](https://github.com/crtahlin/wasp/issues/327). Analysis:
[per-peer-credit.md](per-peer-credit.md), merged as the argument this spec
implements.

Code references are to `7bd5da14`, base `upstream/v2.8.2`.

> **Note on the verification below, from
> [#355](https://github.com/crtahlin/wasp/issues/355).** This document said
> `pkg/accounting`, `pkg/pricing` and `pkg/settlement/pseudosettle` were
> byte-identical to the base, "verified with `git diff upstream/v2.8.2 main --`
> against those three paths, which is empty".
>
> **That was true at `7bd5da14`**, the commit this document pins, and it is still
> true there: the diff at that commit is empty. The defect is that the command
> names `main`, a **moving** ref, rather than the pinned commit. Re-run today it
> gives a different answer, because those paths now carry 1,600 inserted lines
> and 11 deleted against the base: `pkg/accounting` 1,196 and 11, `pkg/pricing`
> 404, and `pkg/settlement/pseudosettle` still unchanged. Most of that is #327's
> own implementation and #353, all of which landed after this document was
> written.
>
> So **read every line number below against `7bd5da14`, not against current
> `main`.** `PrepareCredit` in particular has both moved and gained fork code
> since, so its citations here do not point where they did. The verification
> command, restated so it stays true:
>
> ```bash
> git diff upstream/v2.8.2 7bd5da14 -- pkg/accounting pkg/pricing pkg/settlement/pseudosettle
> ```

**Depends on [#324](https://github.com/crtahlin/wasp/issues/324)**, merged as
[`558c93a5`](https://github.com/crtahlin/wasp/commit/558c93a5), which adds the
`bee_retrieval_preferred_overdrafts` counter this spec uses as its falsifier and
supplies every overdraft figure quoted below.

**Interacts with [#333](https://github.com/crtahlin/wasp/issues/333)**, found
while writing this document and tagged `affects-upstream`. It is not a blocker,
and Design 3 says how this change stays correct while it stands.

**This is revision 5.** Four adversarial reviews found forty-nine defects
between them. Revision 3 removed the lowering step, which was the source of most
of them. Revision 4 replaced the cost model with a measurement, and revision 5
replaces that measurement's evidence, because the fourth review showed it
compared a node-wide quantity against a per-peer one. Earlier claims that were
wrong are marked where they appear rather than quietly removed.

## Terms

- **Payment threshold granted to a peer**: `paymentThresholdForPeer`, how much
  that peer may owe this node before this node stops serving it. Announced to
  the peer.
- **Payment threshold received from a peer**: `paymentThreshold`, the number that
  peer announced to us, which is what gates **our** requests to it.
- **Latent debt**: `peerLatentDebt`, a peer's balance plus its shadow reserve
  plus its ghost balance, floored at zero
  (`pkg/accounting/accounting.go:899-905`). It is what the blocklist duration is
  computed from.
- **Lookahead buffer**: the read-ahead the API performs while streaming a
  download, set per request with `Swarm-Lookahead-Buffer-Size`. At its shipped
  default it prefetches, putting many chunks in flight at once; set to 0 it
  puts fewer in flight, but **not one**. At 0 the handler passes the reader
  straight to `http.ServeContent` (`pkg/api/bzz.go:824-828`), whose `io.Copy`
  buffer is 32 KiB, so the read unit is **8 chunks**; at the shipped
  `smallFileBufferSize` of 262,144 (`bzz.go:51`) it is **64**. And
  `joiner.ReadAt` uses an errgroup with no limit, so a read unit fans out
  however large it is. On a separate bench session, so not comparable run for
  run with the figures below, the peak reserved balance was 2,530,000 to
  2,550,000 with it off against 12,780,000 to 12,880,000 with it on. See
  [overdraft-terms.md](overdraft-terms.md).
  **The whole result below turns on which of the two is in play.**
- **Sole-source content**: content that only the provider holds, because its
  postage batch expired and the network answers 404 for it. The provider still
  serves it because it is pinned, which needs no stamp.
- **Hinted**: a download carrying `Wasp-Providers` naming the provider, so the
  requester tries it first.
- **Block**: one measurement session on the bench, with the node states and peer
  counts it happened to have. Figures from different blocks are not comparable.

## Problem

A provider serves a few per cent of a download and then runs out of credit with
the requester.

**Which field gates it, exactly**, because the two above are easy to confuse and
an earlier draft of this spec confused them. The refusal happens on the
**requester**, inside `PrepareCredit`, against the requester's copy of what the
provider announced:

```go
// pkg/accounting/accounting.go:313-322
timeElapsedInSeconds := min((a.timeNow().UnixMilli()-accountingPeer.refreshTimestampMilliseconds)/1000, 1)
refreshDue := new(big.Int).Mul(big.NewInt(timeElapsedInSeconds), a.refreshRate)
overdraftLimit := new(big.Int).Add(accountingPeer.paymentThreshold, refreshDue)
if increasedExpectedDebt.Cmp(overdraftLimit) > 0 {
    a.metrics.AccountingBlocksCount.Inc()
    return nil, ErrOverdraft
}
```

So the quantity to raise is what the **provider announces**, and the provider's
own `paymentThresholdForPeer` matters locally only through the `disconnectLimit`
derived from it. That makes `AnnouncePaymentThreshold` the mechanism of this
change rather than a step in it, which Design 3 takes seriously.

Note `min(elapsed, 1)`: the refresh term is capped at one second, so the limit is
the announced threshold plus at most one refresh rate. It does not accumulate
over a download. An earlier note in this work said it did, and that was wrong.

Two measurements, both recorded, say this limit is what binds.

**Raising the threshold raises what a provider delivers.** From
[results.md](results.md) Table 6, three runs per step except the last, which has
two:

| Threshold announced | Chunks from the provider | Share of the download |
|---|---|---|
| 13,500,000, the default | 176 (164-235) | 4.3% |
| 27,000,000 | 453 (407-486) | 11.1% |
| 54,000,000 | 1,261 and 1,405 | 31% and 34% |

Read those shares with the caution results.md attaches to them. Credit available
over a download is a fixed amount plus a part that grows with how long the
download lasts, so counts from different blocks are not comparable and a slower
block flatters the same threshold. The direction is what this establishes.
**That same arithmetic is a trap for this spec's own measurement**, and
Measurement says how it is avoided.

**And credit is what stops the sole-source case.** With #324 merged, so with a
single refusal no longer removing the only holder of a chunk for good, content
only the provider holds still truncates at the shipped lookahead buffer. Three
runs, from
[overdraft-retry-results.md](overdraft-retry-results.md):

| Runs | Bytes of 4,194,304 | Time to truncation | Rate | Overdrafts |
|---|---|---|---|---|
| 3 of 3 truncated | 1,310,720, so 31.2% | 1.34 to 1.42 s | 924,411 to 976,922 B/s | 311 to 429 |

Those are times to truncation, not completion. A completing download of that
file with the prefetch off took 15.9 s in the same block.

With `Swarm-Lookahead-Buffer-Size: 0` the same build completes 6 runs of 6. So
the prefetch breaks it through credit: many chunks in flight means many refused
at once, and each refused chunk is then sought from peers that do not hold it.

**The node-wide setting is not the answer.** `payment-threshold` is one number
for the whole node, so a provider raising it announces that credit to every peer
it has, including peers downloading nothing it announced. That is why the ledger
row for [#290](https://github.com/crtahlin/wasp/issues/290) records a raised
threshold as "not a setting to recommend". It is also capped: a node refuses to
start when its configured `payment-threshold` exceeds `maxPaymentThreshold`, 24
times the refresh rate or 108,000,000 (`pkg/node/node.go:241`, `:806`).

## Hypothesis

Announcing the larger threshold only to a peer that is asking this node as a
provider removes the refusals that truncate a sole-source download, at a
fraction of the credit a node-wide raise would announce.

**Falsifiable.** If the requester's overdrafts per preferred attempt do not
fall, the announced threshold was not the constraint in that run and nothing
else in this document follows.

## Design

### 1. What the trigger can and cannot be

A requester asking a preferred peer attaches the `wasp-local-only` header to
every attempt (`pkg/retrieval/preferred.go:28`, sent at `:240`). So a peer
asking this node *as a provider* is distinguishable per request, with no new
message and no chunk-to-content index.

**But that header is not proof the peer is downloading content this node
announced, and this spec does not pretend otherwise.** The handler answers a
local-only request from `s.storer.Lookup().Get`
(`pkg/retrieval/retrieval.go:589`), which reads the whole chunk store, reserve
and cache included. Any peer can obtain a hit by asking for any chunk this node
happens to hold, and for an announced reference the node has published which
chunks those are.

**Gating on the content instead is not cheap with today's indexes**, and that is
worth stating so it is not rediscovered. Announcing a reference requires it to
be pinned first (`pkg/api/providers.go:249-259`), so "pinned" is the right set.
But pinning is indexed by collection: `pinChunkItem` is namespaced by the
collection's UUID and keyed by the chunk address
(`pkg/storer/internal/pinning/pinning.go:420-430`), and `HasPin` takes a root
reference, not a chunk (`:202`). Asking "is this chunk pinned" therefore means
one index read per pinned collection, on the hit path of every retrieval. That
is not viable.

### 2. What the operator is agreeing to, now measured rather than reasoned

This section has been wrong twice in opposite directions, and the third attempt
replaced reasoning about constants with reasoning about a regime nobody had
checked. So it is now written from measurement.

**The two earlier errors, kept so they are not repeated.** The first draft said
a total credit budget makes the worst case "a fixed number the operator chose".
False: `Connect` resets the peer ledger, writing zero to the balance key
(`pkg/accounting/accounting.go:1435`) and zeroing `shadowReservedBalance`
(`:1427`) and `ghostBalance` (`:1428`), so a peer can take a grant, disconnect,
reconnect with the debt erased and take it again. The second draft concluded
from that that the setting gives away unbounded bandwidth. Also false, and for a
reason the third draft got only half right.

**The price is measured now, not estimated.** Every analysis in this work has
used 310,000, derived from the pricer's formula
`(MaxPO - proximity(peer, chunk) + 1) x 10,000` (`pkg/pricer/pricer.go:34-35`)
with an expected proximity of 1. `bee_retrieval_chunk_price` is a summary
(`pkg/retrieval/metrics.go:26`) observed in `prepareCredit`
(`pkg/retrieval/retrieval.go:495`) that had never been read. Over three
sole-source runs the mean is **306,735**, **306,454** and **309,141** across 245,
2,403 and 1,769 credit decisions. The estimate was right to about 1%. Note it
counts credit decisions node-wide, including relayed retrievals, not deliveries
from one peer.

**And the threshold recycles, which is provable per peer from Table 6.** An
earlier version of this section argued it from
`bee_swap_total_sent` and `bee_pseudosettle_total_sent_pseudosettlements`.
**That argument was wrong and is withdrawn**: both are unlabelled counters
incremented for any peer (`pkg/settlement/swap/swap.go:155`,
`pkg/settlement/pseudosettle/pseudosettle.go:355`), so they are node-wide, while
the free allowance they were compared against is per peer, computed from a
per-peer timestamp (`pseudosettle.go:155-168`). Against 117 peers the node-wide
free ceiling is about 527,000,000 a second, so the comparison supported the
opposite of the conclusion drawn from it.

The right evidence was already published. Table 6's "chunks from P" is one
peer's deliveries, so it is per peer by construction. At the measured price, and
against the whole credit that peer could supply without a cheque, which is the
window plus every unit of free allowance the run could produce:

| Threshold, run time | Chunks from P | Debt to P | Window + all free allowance | Ratio |
|---|---|---|---|---|
| 13,500,000, 6.80 s | 176 | 53,985,360 | 48,600,000 | 1.11x |
| 27,000,000, 8.10 s | 453 | 138,950,955 | 67,950,000 | 2.04x |
| 54,000,000, 9.46 s | 1,261 | 386,792,835 | 101,070,000 | **3.83x** |
| 54,000,000, 9.57 s | 1,405 | 430,962,675 | 101,565,000 | **4.24x** |

**At the value this spec proposes to use, nearly four times more credit was
consumed with one peer than the window and the free allowance together could
supply.** Cheques cleared the rest, during the download. So the payment
threshold behaves as an in-flight window that recycles as settlement clears it,
not as a stock of free credit that has to last.

**So what a larger threshold buys is a wider window, not free bandwidth.** The
sustained rate is set by how fast debt clears, and on this bench debt clears
mostly by cheque, which is payment. **The extra credit is repaid.** What the
operator is accepting is the risk that a particular peer does not repay it,
bounded per connection by the grant and in aggregate by the budget.

That is a better case for the feature than either earlier draft made, and it is
the first one resting on measurement.

**Three things it does not say.**

- **It is not a claim about other deployments.** A node with no chequebook
  settles only by pseudosettle, at 4,500,000 units a second, and there the
  threshold is much closer to a cumulative ceiling. The measurement below records
  the bench's settlement configuration for exactly this reason, and the result
  should not be read as a property of the protocol.
- **Reconnect still erases the ledger**, so a peer can take the burst again after
  the blocklist expires, `(latent debt + node-wide payment threshold) / refresh
  rate` seconds, about 15 at a 54,000,000 grant with the defaults (`:1388`).
  Note it is the node-wide value in that formula and not the grant.
- **The free allowance has to be asked for.** Forgiveness happens in
  pseudosettle's handler, on a payment the debtor initiates, reached from
  `settle` (`:464`), and it is capped at the debt rather than banked
  (`pkg/settlement/pseudosettle/pseudosettle.go:168`, `:174-178`). An earlier
  draft said a peer receives it "by doing nothing", which is not so: any peer
  that settles receives it, and every stock Bee does.

The four things that bound the surface:

1. **The operator opts in.** The threshold is a new setting whose default
   disables the behaviour.
2. **`providers-enable` must be on, and it is off by default**
   (`cmd/bee/cmd/cmd.go:415`, honoured at `pkg/retrieval/retrieval.go:175` and
   `:593`).
3. **The budget**, which bounds how much extra credit is granted across all
   connections holding a raise at one time.
4. **Reconnects are rate limited** by the accounting blocklist above.

The existing local-only rate limits, 100 misses a second per peer and 1,000 a
second in total (`pkg/retrieval/retrieval.go:119-120`), bound the **miss** path
only, so they are not a bound on this change.

**The hardening is a separate issue, deliberately.** The requester already knows
which root reference it is downloading, since the provider hint is attached per
content key (`pkg/api/providers.go:129-134`). It could send that root in a second
header, and the provider could check it with one `HasPin` read. It is not here
because it adds a header for a benefit only worth having once the measurement
shows the feature is worth hardening.


### 3. Raising it, and why the announce is the whole of it

`paymentThresholdForPeer` and `disconnectLimit` are per-peer fields, initialised
from the node-wide values on connect (`:1415-1433`). They already diverge per
peer through the growth path (`:657-662`), which recomputes `disconnectLimit`
from the new threshold and announces the result.

**How often that growth path fires**, because two earlier drafts of this spec had
it wrong and the second error is now its own issue.

It is called from `NotifyPaymentReceived` (`:1014-1016`) and
`NotifyRefreshmentReceived` (`:1181-1183`), so it is driven by what the **peer**
pays **us**, never by what we send. The first draft said "one refresh rate per
payment sent", which is wrong in direction. The second said "roughly once per
450,000,000 units of cumulative repayment", which is right only until a peer
reconnects: `Connect` rewinds `thresholdGrowAt` to `thresholdGrowStep`
(`:1432`) without re-zeroing `totalDebtRepay`, which is written once at record
creation (`:613`) and whose record is never deleted from the peer map. A
returning peer with a long history therefore fires the upgrade on **every**
settlement until the checkpoint catches up. That is
[#333](https://github.com/crtahlin/wasp/issues/333).

**So this spec may not assume the growth path is rare**, and must be correct
alongside a burst of upstream announces to the same peer. Design 3(a) is what
makes that true.

Six details a naive version gets wrong.

**(a) The announce must not happen under the peer lock, and there must be one in
flight per peer.** Upstream's growth path calls `AnnouncePaymentThreshold` while
holding `accountingPeer.lock` (`:662`), and that opens a fresh stream with a
five-second timeout (`pkg/pricing/pricing.go:128-131`). `PrepareDebit` takes the
same lock with `TryLock(ctx)` (`:1213-1216`), and **`TryLock` blocks**: it is a
select on acquiring the lock or the caller's context expiring (`:113-120`). An
earlier draft said it returns immediately on failure, which was wrong. So
announcing under the lock **stalls** concurrent deliveries to that peer for up
to five seconds, and fails them only when the requester gives up first, its
context being `RetrieveChunkTimeout`, 30 seconds
(`pkg/retrieval/retrieval.go:145`, applied to the handler at `:544`).

That is a serialised stall rather than a failure, which is less severe than the
earlier draft claimed, and still unacceptable under the prefetch, which is many
concurrent requests from one peer and is this spec's own scenario.

**Ordering is the harder half.** The message carries an **absolute** value with
no sequence number, the receiver stores whatever arrives (`:998`), and a fresh
stream is opened per call (`pricing.go:131`), so two announces to one peer can
be delivered in either order and the peer keeps the last one processed, for
good, with no later correction. Rule 6 forbids adding a sequence number to the
wire, so ordering is the sender's job:

- the value to announce is decided under the peer lock and stamped with a
  per-peer generation counter;
- at most one announce is in flight per peer, later values replacing an
  unsent earlier one rather than queueing behind it;
- an announce whose generation is stale when it is about to be sent is dropped.

**There is a third announcer, outside `pkg/accounting` entirely.**
`pricing.Service.init` announces the node-wide threshold on connect, from the
pricing protocol's own connect handlers, registered as both `ConnectIn` and
`ConnectOut` (`pkg/pricing/pricing.go:73-74`, announcing at `:116`). Its
13,500,000 and a raise's 54,000,000 are concurrent, absolute and unacknowledged,
on separate fresh streams. **If the connect announce lands second the peer keeps
13,500,000 for the life of the connection**, while this node has consumed a
budget slot and raised its `disconnectLimit` for nothing: all of the cost, none
of the benefit, and silent.

So out-of-order delivery still matters even though raises are monotonic, because
a stale **lower** value landing last is permanent. All three announcers go
through the queue, which is more than "one exported method", and section 6 says
so.

**(b) A failed announce changes nothing.** `AnnouncePaymentThreshold` does not
wait for a reply: it writes the message and closes the stream
(`pricing.go:143-149`), with no reply and no acknowledgement. **So an error
never distinguishes "the peer did not get it" from "the peer got it and the
transport failed afterwards".** Lowering `disconnectLimit` in the second case
would blocklist the peer for debt it was invited to take on, which is precisely
what this design exists to avoid. An earlier draft said "the delta is subtracted
again and the budget released", which contradicted its own section 4.

So the rollback is asymmetric, and it is safe in the direction that matters:
this node stops counting the grant and frees the slot, while the tolerance that
protects the peer stays where it is until the connection ends.

**But the rollback must not write to `paymentThresholdForPeer` either**, and an
earlier draft said it should. The growth path recomputes `disconnectLimit` from
whatever that field holds (`:659`), and #333 means it can fire on every
settlement after a reconnect. So a rollback that lowered the field would have
the very next growth firing silently pull `disconnectLimit` down with it, which
is the invited-debt blocklist this design exists to avoid, reached by a route
section 4 does not cover. It would also break monotonicity: the next growth
announce would carry a value below one the peer may already hold, absolute and
uncorrectable.

**So a failed announce changes nothing at all.** An earlier draft released the
budget slot, which contradicted the budget's own definition as the sum of
granted deltas across connections currently holding a grant: after an announce
error the node cannot know whether the peer holds the grant, and 3(b) has
already resolved that same ambiguity conservatively for both upstream fields.
Resolving it the same way for the budget removes the question of what the
per-peer slot marker becomes, which had no good answer either. The error is
logged; the grant stays counted until the connection ends.

**(c) Announced values are clamped to at least the node-wide
`payment-threshold`.** That value is itself forced to at least
`minPaymentThreshold` at startup (`node.go:802`), and `minPaymentThreshold` is a
compiled constant, `2 * refreshRate` = 9,000,000, or `2 * lightRefreshRate` for
a receiver in light mode (`node.go:240`, `:1182-1187`, passed to pricing at
`:1201`). An earlier draft called it "the receiver's configuration and unknown
to us", which was wrong: for any stock receiver it is known and it is 9,000,000.
A value below it is answered with `p2p.NewDisconnectError(ErrThresholdTooLow)`
(`pricing.go:100-103`), a disconnect rather than a rejection, so the clamp is
what keeps every announce safe against a stock peer.

**(d) Store the granted extra as a delta, never a remembered absolute.** The
growth path reassigns `accountingPeer.paymentThresholdForPeer` with
`new(big.Int).Add(...)` on whatever value is there (`:657`), while `Connect`
(`:1431`) and `NotifyPaymentThreshold` (`:998`) mutate in place with `.Set()`. A
value held as a `*big.Int` will alias one writer and detach from the other. Hold
values, not pointers, and re-read under the lock.

**(e) `disconnectLimit` is recomputed, not remembered.** Wherever it changes it
is `percentOf(100+a.paymentTolerance, paymentThresholdForPeer)`, matching `:659`
and `:1527-1529`. Nothing stores a previous absolute.

**(f) Budget admission needs its own mutex, and no I/O under it.** `Accounting`
has exactly one node-level mutex, `accountingPeersMu` (`:155`), whose documented
job is the peer map. No existing path holds it and a peer lock at once:
`getAccountingPeer` releases it before returning (`:602-603`) and
`PeerAccounting` copies the map and releases it (`:713-716`) before taking any
peer lock. Reusing it would invert that order. This change adds a dedicated
budget mutex, or an atomic counter, never held across a peer lock or across I/O.

### 4. The grant lasts for the connection, and is never lowered

**Both earlier drafts had a decay step, and it was the source of nearly every
defect the two reviews found.** Out-of-order announces mattered only because a
decay could overtake a raise. The rollback was ambiguous only because it had to
choose whether to lower. The budget disagreed with `disconnectLimit` only
because eviction lowered one and not the other. And the decay's own gate was
unsatisfiable: it waited for ghost balance to come back within the pre-raise
limit, while ghost balance is only ever added to (`:1371`) and is zeroed only by
`Connect` (`:1428`), so for a peer under the prefetch it can never come back.

**Removing the decay removes all four, and costs little, for a reason section 2
establishes.** Pseudosettle already caps the sustained rate at the refresh rate
whatever this node grants, so a grant that persists does not give away more per
second; it holds a larger burst ceiling open for one peer for one connection.
And the exposure is per-connection anyway, because `Connect` resets the ledger.

So:

- **A raise is granted once per connection**, on the first local-only hit from a
  peer that qualifies, and it stays until the connection ends.
- **A second local-only hit changes nothing**, so at most one slot per peer
  follows from the rule rather than needing to be enforced separately.
- **The budget slot is released on disconnect**, in `Disconnect` (`:1496`), and
  the per-peer state this change adds is cleared in `Connect`. **Neither is
  reliable without care**, and an earlier draft assumed both were.
  `accounting.Disconnect` has one caller, pseudosettle's `terminate`
  (`pkg/settlement/pseudosettle/pseudosettle.go:125`), registered as both
  `DisconnectIn` and `DisconnectOut`, so an explicitly initiated disconnect runs
  it twice; today that is harmless only because the body sits behind
  `if accountingPeer.connected` (`:1502`), so a release placed outside that
  guard would free the slot twice. And `Connect` and `Disconnect` are both
  dispatched with `go` and no ordering (`pseudosettle.go:115`, `:125`), so on a
  fast reconnect a stale `Disconnect` can run after the new `Connect` and free a
  slot the new connection is holding. **Keying the release to the same
  `connected` transition that guards `:1502` fixes the double run and does not
  fix the race**, and an earlier draft claimed a connection epoch would. It
  cannot: `Accounting.Disconnect` takes only an address, and its caller has only
  a `p2p.Peer`, which libp2p builds without a connection identity
  (`pkg/p2p/libp2p/libp2p.go:1257`), so there is nothing to compare an epoch
  against. **The race is therefore not fixed and the cost is stated instead**:
  on a fast reconnect a stale teardown can free a slot the new connection holds,
  so the budget can drift low until restart. The same interleaving already
  blocklists the new connection in unmodified upstream code, which is a
  candidate for `affects-upstream` once someone verifies it rather than reasons
  it, and this change does not make it worse.
- **There is no idle timeout and no eviction**, so `providerCreditIdle` is gone
  along with the argument about how long a download takes.

**What this gives up.** A peer that makes one local-only hit and then downloads
nothing holds its slot until it disconnects. With the budget full, a later
provider download is served at the ordinary threshold and gets the 4.3%
behaviour. That is a real cost and it is the price of not having a decay whose
own release gate cannot fire.

It is bounded by connection count rather than by a timer, which is a bound the
node already has, and a peer occupying a connection is a pre-existing condition
this feature does not create. An attacker who wants to deny the feature must
hold connections open, and while doing so is receiving the free refresh
allowance either way.

### 5. Light peers are excluded, and `fullNode` comes from the request

`Connect` gives a light peer `lightPaymentThreshold` and `lightDisconnectLimit`
(`:1420-1422`), and `lightFactor` is 10 (`pkg/node/node.go:233`), so a light
peer's threshold is 1,350,000 at the default and it settles at
`lightRefreshRate`, 450,000 a second. The same grant would be 10 to 40 times the
exposure upstream intends for it, and it would clear the debt ten times slower.

**Light peers therefore do not get the raise in this version**, and the decision
reads `p.FullNode` from the `p2p.Peer` the handler already has
(`pkg/p2p/p2p.go:192-196`), not `accountingPeer.fullNode`. That field is set by
`Connect` (`:1426`), which is invoked as
`go s.accounting.Connect(p.Address, p.FullNode)`
(`pkg/settlement/pseudosettle/pseudosettle.go:115`), so it races the first
request on another stream and may not be set yet.

For the same reason a raise is refused while `!accountingPeer.connected`
(`:1220-1222`) rather than assuming the record is ready.

Scaling the grant by `lightFactor` instead is the obvious alternative and is
left out because nothing has measured a light requester downloading from a
provider.

### 6. Where the code goes, and what it touches

- **`pkg/accounting`**: one exported method to grant a raise, the per-peer delta
  and slot, the budget counter, clearing the new state inside `Connect` and
  releasing the slot inside `Disconnect`, and the per-peer announce queue that
  upstream's growth path is also routed through.
- **`pkg/retrieval`**: the header read on the hit path, which does not exist
  today. The handler tests the local-only header only inside the
  `storage.ErrNotFound` branch (`:591-595`) and on a hit goes straight to
  pricing at `:607`. The read itself is cheap. The service reaches accounting
  through a small fork-authored interface declared here, rather than by widening
  upstream's `accounting.Interface`.

**This edits upstream functions, it does not only add to them.** `Connect`,
`Disconnect` and the announce path all change. An earlier draft said the upstream
interface would be untouched and implied the same of the functions, and the
second is no longer true. It is stated so the next upstream sync expects it.

## What this risks

- **A larger burst per provider-requesting peer**, repeatable across reconnects,
  on top of the sustained allowance every stock node already gives. Section 2.
- **A slot held for a whole connection by a peer that stops downloading.**
  Section 4, accepted deliberately.
- **Deliveries stalling behind an announce held under the peer lock.** Section
  3(a). This would be a regression in exactly the case the feature targets, and
  #333 makes the announce burst larger than upstream's normal cadence.
- **An announce protocol with no acknowledgement and no ordering.** Rule 6
  forbids fixing that on the wire, so every correctness argument here reduces to
  sender-side discipline, and only the tests hold it in place. This is the
  residual risk of the whole design and it is stated rather than buried.
- **Nothing for stock requesters.** A stock Bee node never sends the local-only
  header, so it never gets the raise. It would *accept* a larger announced
  threshold; not doing it is a choice.
- **Above 108,000,000 is not attempted.** Per-peer values can exceed
  `maxPaymentThreshold`, since that check runs only against the node's own
  configured value at startup. This spec refuses to: nothing above it has been
  measured. The setting is validated against it.

## Protocol impact

**None.** No wire format, protocol identifier, handshake or message change. The
threshold is announced with the existing `AnnouncePaymentThreshold` message,
carrying a value the receiving side already accepts without an upper bound and
which a stock node could itself be configured to send. No new stream header.
`.github/protocol-freeze.lock` fingerprints `protocolName` and `protocolVersion`
for `pkg/pricing`, neither of which changes, so `make protocol-freeze` must pass
with the fingerprint unchanged and the `protocol-change` label is not needed.

## Configuration

| Setting | Default | Meaning |
|---|---|---|
| `providers-payment-threshold` | `0`, disabled | The threshold announced to a full-node peer making local-only hits. Validated as either 0, or at least the node-wide `payment-threshold` and at most `maxPaymentThreshold`. |
| `providers-credit-budget` | `0` | **The sum of granted deltas across connections currently holding a grant**, where a delta is the announced value minus the value it replaced. Validated as at least one grant's worth, so a budget too small to admit anybody is refused rather than silently disabling the feature. |

**The budget counts what has been granted, not what is outstanding**, and an
earlier draft's wording implied the second. A peer that takes a grant and uses
none of it still holds its slot. That is the conservative direction, and saying
it plainly is better than a word that suggests the node measures exposure it
does not measure.

**What raising `providers-payment-threshold` costs:** a larger burst of this
node's bandwidth served to each admitted peer before it has to pay or wait, on
top of the sustained refresh allowance it already gets. What lowering it costs:
the provider delivers a smaller share of each download, which is the 4.3% in the
Problem table.

**What raising `providers-credit-budget` costs:** more connections hold a raise
at once, so more unsecured credit is outstanding simultaneously. What lowering
it costs: provider downloads beyond the budget get the 4.3% behaviour. A budget
large enough to admit every peer is the node-wide raise reached by a longer
route, and
the documentation says so.

**Who pays.** Mostly this operator, but not only: an announced threshold also
sets the peer's `earlyPayment` to a percentage of it (`:999`,
`payment-early-percent` defaulting to 50 at `cmd/bee/cmd/cmd.go:387`), which
changes when that peer settles, and the cheque-side cost of a settlement is
recorded in [#300](https://github.com/crtahlin/wasp/issues/300) and
[#304](https://github.com/crtahlin/wasp/issues/304). An earlier draft claimed no
other node pays anything, which was not established.

**On rule 8.** Two settings in one issue, where the rule asks for one issue per
setting. `providers-payment-threshold` is the tuning dial and the measurement
that justifies it is in this document, which is the rule's normal case.
`providers-credit-budget` is a safety bound whose absence is itself the failure
mode, so it cannot sensibly ship later than the thing it bounds; that is the
exception, claimed explicitly rather than by silence, and it rests on the budget
genuinely bounding concurrent exposure, which section 4's removal of eviction is
what makes true.

## Measurement

On the bench, following [test-bench.md](../../agent-playbooks/test-bench.md).
Every arm in one block, interleaved, at least three runs each (rule 7).
`providers-enable` is on for every arm on both nodes, since it gates both the
requester's preferred path (`retrieval.go:175`) and the provider's local-only
answer (`:593`), and an arm without it measures something else.

**First, what not to measure, because an earlier draft got this wrong.** From
the requester's side a node-wide raise and a per-peer raise are the same
experiment: in both the provider announces the same number to this requester,
and whether other peers also got it cannot affect delivery here. So "the
per-peer share lands within the node-wide spread" is close to true by
construction.

Worse, it can reject the better outcome. Credit available over a download is a
fixed amount plus a part that grows with duration, so a **faster** arm receives
less grown credit and delivers a **smaller** share. An arm that wins on time can
lose on share. That is arithmetic, not block variance.

The arms:

**The value under test is 54,000,000**, matching the top row of the Problem
table, with `providers-credit-budget` set to one grant's worth, that is
54,000,000 minus the node-wide 13,500,000, or 40,500,000. The content is the
4,194,304-byte sole-source file. An earlier draft never wrote any of these down,
so "the same value" had no antecedent and no arm could be run from it.

**The bench settles with cheques**, which the measurement records because the
result is not portable without it: a node with no chequebook settles only by
pseudosettle and the threshold is much closer to a cumulative ceiling there.

**Every run records the balance with the provider before and after.** The
correction to the #324 results turned on arms that started from different
balances, and the cold case below exists because of it.

1. **Control.** The change built, `providers-payment-threshold` unset.
   Sole-source content at the shipped lookahead buffer. **The in-session
   baseline**, which exists because comparing against the three truncations in
   Problem would span two blocks.
2. **Per-peer raise**, sole-source, shipped buffer. **The primary arm.**
3. **Node-wide raise** to the same value, sole-source, shipped buffer. Expected
   to be indistinguishable from arm 2 on delivery; run to confirm the
   restriction costs nothing and to expose what genuinely differs, which is
   announce latency and budget admission.
4. **Non-regression**, content the network also holds, hinted, shipped buffer,
   per-peer raise set. **4a** is the same with it unset. The quantity that must
   not regress is **total time**, reported as a median with the full range, and
   the rule is that arm 4's median must not exceed arm 4a's range. Three runs
   each, interleaved with the rest.

The provider is restarted only when a provider-side setting changes, which is
between arms 2, 3 and the control, and every arm is run after the same settling
period, so page-cache state is matched across the comparison as rule 7 requires.

The section 2 price measurement, and this measurement's rows, are written into
[measurement.md](measurement.md) with their block, date and peer count, so a
later reader can tell which block a figure came from. Recorded per run: bytes
and SHA-256, the `curl` exit code where 18 means truncated, total time, time to
first byte, chunks delivered by the provider,
`bee_retrieval_preferred_attempts`, `bee_retrieval_preferred_overdrafts` and
`bee_accounting_accounting_blocks_count` on the requester as **before-and-after
scrape differences**, and on the provider the balance owed by the requester at
the end plus the budget metrics this change adds, which are named in the
implementation and listed in the results. Rates in bytes per second beside every
timing. Nothing else downloads on the requester during a run.

**The falsifier is normalised, because the raw counter is not usable.**
`bee_retrieval_preferred_overdrafts` is a cumulative node-wide counter
incremented once per preferred attempt refused, and a peer readmitted after
`overDraftRefresh` can increment it again for the same chunk. Its magnitude
therefore scales with how long a run lasts, and arm 2 is predicted to complete
where arm 1 truncates at 1.34 to 1.42 s, so arm 2 has several times longer to
accumulate. **The raw count can rise while the change works.** So the quantity
is **overdrafts per preferred attempt**, and it is compared against arm 1's
in-session value, not against the 311 to 429 from another block, which are
quoted only as an order of magnitude.

5. **Cold**, run as arms 1 and 2 again rather than as new builds: the change
   built, with the setting unset and then set, on the same sole-source content at
   the shipped lookahead buffer. Each cycle restarts the node, settles for 15
   minutes and until it has at least 100 peers as the Table 6 sweep did,
   confirms the balance with the provider reads zero, then runs **once**. Three
   cycles each, which is three runs per condition under rule 7. Today, with the
   setting unset, the node delivers one **read unit**, the 262,144-byte unit the
   API reads in, on one cycle of three, and stock delivers nothing on three of
   three. **This is the arm with the most room to move**, because a node that has
   settled nothing has no accumulated headroom at all.

**Pre-registered predictions.**

- **Primary: arm 2 completes with a matching SHA-256 where arm 1 truncates, and
  the refusal rate falls to at most a tenth of arm 1's.** The rate is
  `overdrafts / (overdrafts + attempts)`, bounded in 0 to 1. It is **not**
  overdrafts per attempt, which an earlier draft used: the two counters are
  incremented at disjoint sites, `PreferredOverdrafts` only when `prepareCredit`
  refuses with `ErrOverdraft` and `PreferredAttempts` only when it succeeds, so
  that ratio's denominator moves with the effect under test. The denominator is
  therefore decisions that resolved as a grant or an overdraft, not all
  decisions: `prepareCredit` can fail for other reasons and then neither counter
  moves. Relayed retrievals do not contaminate it, since they never touch the
  preferred path. If the rate does not fall, the announced threshold was not the
  constraint and nothing else here follows.
- **Arm 2 completes in under 30 s.** Section 2 has already excluded the
  cumulative-ceiling model on this bench, so this confirms rather than
  discriminates, and is worth stating because no earlier draft predicted arm 2's
  total time at all. The window model predicts a time of the order of the 15.9 s
  prefetch-off completion, so 30 s carries about a factor of two of headroom.
  The ceiling model, at the value under test, predicts
  `1,033 x 306,735 = 316,857,255` units against `54,000,000 + t x 4,500,000`,
  which is **58 s**. An earlier draft said 71 s, which is the figure for the
  13,500,000 control rather than for arm 2.
- **Arm 5 with the setting set delivers more than zero on at least two cycles
  of three**, against one of three with it unset.
- Arm 3 is indistinguishable from arm 2 on bytes and within the spread on total
  time. A large gap either way means something other than the announced value
  differs between them, and the design is not understood.
- Arm 4's median total time does not exceed arm 4a's range.
- **Time to first byte falls slightly, with a tolerance rather than a claim of
  no movement.** results.md Table 6's hinted rows are 0.46 s (0.43-0.48) at
  13,500,000, 0.31 s (0.31-0.58) at 27,000,000 and 0.308 s at 54,000,000, about
  1.5x between the ends. Predicted: arm 2's median at or below arm 1's, and no
  more than 0.10 s above it. An earlier draft predicted no movement, which that
  table already contradicts.
- No arm blocklists a peer, and the provider's `disconnects_overdraw_count` and
  `disconnects_ghost_overdraw_count` stay at zero.

**A negative result** is arm 2 still truncating, or the normalised overdraft
ratio not falling, or any arm blocklisting a peer. Any of those closes #327 with
the evidence.

## Acceptance

**Accept** if arm 2 completes with a matching SHA-256 where arm 1 truncates,
**and** overdrafts per preferred attempt fall to at most a tenth of arm 1's,
**and** arm 4's median total time does not exceed arm 4a's range, **and** no arm
blocklists a peer.

**Reject** if arm 2 truncates, or the normalised overdraft ratio does not fall,
or arm 4 regresses, or any peer is blocklisted.

Deliberately **not** in the acceptance rule: the delivered share. It is reported
and it is not a gate, for the reason in Measurement.

## Test plan

Unit tests in `package accounting_test` and `package retrieval_test`:

- a local-only hit from a full-node peer raises its announced threshold and
  `disconnectLimit` together, and announces the new value;
- **the announce is not made while the peer lock is held**, asserted by showing a
  concurrent `PrepareDebit` for the same peer completes promptly while an
  announce is in flight;
- **at most one announce is in flight per peer, and a stale one is dropped**: two
  raises decided in quick succession result in the later value reaching the peer,
  never the earlier;
- **upstream's growth path goes through the same queue**, so a growth announce
  and a raise announce to one peer cannot be reordered;
- **a failed announce changes nothing**: `paymentThresholdForPeer`,
  `disconnectLimit` and the budget are all untouched, and the error is logged;
- every announced value is at least the node-wide `payment-threshold`;
- an ordinary hit, with no local-only header, raises nothing;
- a local-only **miss** raises nothing, so the miss path cannot buy credit;
- a light peer is not raised, decided from `p.FullNode` and not from the
  accounting record;
- a peer whose accounting record is not yet connected is not raised;
- a second local-only hit from an already-raised peer changes nothing;
- the raise is refused when the budget is exhausted, and the peer is then served
  at the ordinary threshold rather than refused service;
- two concurrent first requests from different peers cannot both take the last
  slot of budget;
- **the budget slot is released on disconnect**, settled or not, and the new
  per-peer state is cleared on connect, so a reconnecting peer cannot carry a
  stale delta;
- **the growth path firing during an active raise does not corrupt the delta**,
  including the repeated firing #333 produces after a reconnect;
- the setting is refused at startup when it is below the node-wide threshold,
  above `maxPaymentThreshold`, or set with a budget smaller than one grant.

Plus the mixed-version check against a stock v2.8.2 node, which must accept the
raise and blocklist nobody.

## Upstream portability

**No `affects-upstream` marker on this change.** The per-peer threshold machinery
is upstream's and works as intended; what is new is driving it from a
fork-authored signal upstream has no equivalent of.
`pkg/retrieval/preferred.go` does not exist upstream and `providers-enable` is a
fork setting.

Two upstream behaviours this change works around rather than fixes: the announce
made under the peer lock (`:662`) and the announce error logged while the raised
value is kept (`:663-665`). A third, the growth checkpoint rewound on connect
while its counter is not, **is** tagged and is
[#333](https://github.com/crtahlin/wasp/issues/333). An earlier draft argued the
first two were harmless because the path fires rarely; #333 is the reason that
argument no longer holds, and it is why 3(a) queues rather than merely moves the
announce.

## Rollout and rollback

- Off unless `providers-payment-threshold` is set, and it cannot be set usefully
  without `providers-enable`, which is also off by default.
- Rollback is unsetting the option. No migration and no on-disk format change.
- **No stored state, with the qualification an earlier draft omitted.** The
  grant lives in memory on the accounting record. Upstream's two fields are
  re-initialised by `Connect` (`:1431`, `:1433`), and the delta and slot this
  change adds are cleared there too, because `Connect` knows nothing about them
  otherwise. The budget counter is cleared at startup and on every disconnect,
  so a restart cannot leave it consumed.
- A node restarted mid-download loses the raise, and the peer's next request is
  served at the ordinary threshold. That is the same as any other reconnect.

---

Generated with help of AI.
