# Extending credit to a peer downloading announced content

This is the follow-up that [results.md](results.md) asks for. The payment
threshold sweep showed that raising a provider's threshold raises how much it
delivers, and then said what the follow-up must settle: "a provider that raises
its threshold extends more unsecured credit to **every** peer. That is the cost
to the provider, and the follow-up must say so."

This document takes that cost seriously and proposes extending the credit
per peer instead of node-wide. It is analysis and a proposal, not a spec. It
needs its own issue and a merged spec before any code (rule 2).

Code references are to `6eaaa651`, base `upstream/v2.8.2`. `pkg/accounting` and
`pkg/pricing` are byte-identical to that base **at that commit**, so every line
cited is also upstream's. They are not byte-identical on current `main`, and
`PrepareCredit` has since both moved and gained fork code, so read the line
numbers below against `6eaaa651`:

```bash
git diff upstream/v2.8.2 6eaaa651 -- pkg/accounting pkg/pricing
```

## What is already measured

**This is not an untested idea.** It was measured twice, both times outside the
fixed method, so both are pointers rather than results under rule 7.

The sweep, three runs per step except where noted, from
[results.md](results.md) Table 6:

| Threshold | Chunks from provider | Share | Chunks a second |
|---|---|---|---|
| 13,500,000 (default) | 176 (164-235) | 4.3% | about 26 |
| 27,000,000 | 453 (407-486) | 11.1% | about 52 |
| 54,000,000 | 1,261 and 1,405 | 31% and 34% | 133 to 147 |

The 54,000,000 step has **two** runs of condition 3, not three. An earlier
session at 108,000,000, the maximum a node accepts, gave 869 (787-888), about
21%, at roughly 178 chunks a second (Table 5; the rate is from the caution
paragraph under Table 6).

**Read the shares with the caution the results file attaches to them.** Counts
from different blocks are not comparable, because credit available over a
download is roughly the threshold plus a part that grows with how long the
download lasts, so a slower download lets a provider deliver more at the same
threshold. The blocks got slower as the campaign ran, which is why 54,000,000
shows a larger share than the earlier 108,000,000 session. Dividing by duration
does not repair this either, because delivery is a fixed amount plus a rate
rather than a pure rate. The chunks-a-second column is there to show the size of
the effect, not as a quantity that should be equal across blocks. The sweep does
not establish how the share behaves between 54,000,000 and the maximum.

**On speed, two different things were measured and they should not be merged.**

- **Time to first byte is the steady effect and does not depend on the
  threshold.** With a hint it was 0.308 s at 54,000,000 and 0.307 to 0.312 s at
  108,000,000, against controls in the same blocks ranging 0.44 s to 1.21 s. The
  hint saves the search for a source, which is a fixed cost.
- **Total time is not settled by the sweep.** Condition 3 beat condition 2 in
  every block, but the control spreads are wide: at 54,000,000 the three
  controls ran 8.16 s, 10.79 s and 13.39 s. The one session that did clear the
  spread is Table 5, at the maximum threshold, where total time was 4.86 s
  against 5.31 s, about 8% less.

**Why it works**, with the weakest evidence in this document. The rate one
provider may serve one requester is the free allowance, 4,500,000 units a
second, plus what cheques clear, which results.md puts at about 5,000,000 to
6,000,000 units a second, roughly 34 chunks a second at the provider's price.
**That figure is one diagnostic download, taken in the 108,000,000 session with
debug logging that the results file says slowed it, so it is a diagnosis rather
than a measurement under rule 7 and it is not a default-threshold figure.** Take
from it only the shape: raising the threshold raises both the opening window and
what a single cheque can clear, which is what the sweep's 26, 52 and 133 to 147
chunks a second show.

## The problem with the obvious version

`payment-threshold` is one number for the whole node. A provider that raises it
to serve announced content is extending that credit to **every** peer it has,
including peers that are not downloading anything it announced and peers it has
never heard of. At 108,000,000 units with the 123 peers the requester had in the
Table 5 runs, and assuming every one of them is a full node, the worst case is
roughly 13,000,000,000 units of unsecured credit outstanding, against about
under 2,000,000,000 at the default. That is the requester's peer count standing
in for the provider's exposure; the provider's own count for the sweep is given
only as at least 100 peers, so treat it as an order of magnitude.

That is the cost the results file names, and it is the reason the ledger row for
[#290](https://github.com/crtahlin/wasp/issues/290) records a raised threshold
as "not a setting to recommend".

There is a second reason not to reach for the node-wide setting. A node refuses
to start when its configured `payment-threshold` exceeds `maxPaymentThreshold`,
24 times the refresh rate or 108,000,000 (`pkg/node/node.go:241`, `:806`). So
the node-wide route is capped at the value that has already been measured, and
cannot go further without changing a check that upstream put there deliberately.

## The proposal

**Raise the threshold only for a peer that is actually downloading content this
node announced, and only while it is doing so.**

Three things make this small. Two already exist; the third is nearly there.

**1. The threshold is already per-peer state.** `paymentThresholdForPeer` and
`disconnectLimit` live on the per-peer record, are initialized from the
node-wide values on connect (`pkg/accounting/accounting.go:1415-1433`), and
already diverge per peer over time through `notifyPaymentThresholdUpgrade`
(`:657-659`). Nothing needs restructuring; the per-peer value simply gets set
from a different input.

**2. Announcing it needs no wire change.** `AnnouncePaymentThreshold`
(`pkg/pricing/pricing.go:125-150`) is an existing message that this node already
sends whenever the growth path fires. Raising a peer's threshold means sending
that message with a larger number.

**3. The information the trigger needs is already on the wire, though one small
read is missing.** A requester asking a preferred peer attaches the
`wasp-local-only` header to every attempt (`pkg/retrieval/preferred.go:176`,
sent at `:240`), so a peer asking this node *as a provider* is already
distinguishable per request, with no chunk-to-content index and no new message.

**But the handler reads that header only on a local miss.** The read at
`pkg/retrieval/retrieval.go:558` sits inside the `storage.ErrNotFound` branch
(`:556-560`). On a hit, which is the case where the provider actually serves and
credit is consumed, the handler goes straight to pricing (`:572`) without
looking at the headers. So this item is the one piece of the three that does
**not** already exist: it needs a header read on the hit path. That is a small
change, and the spec should not describe it as free.

**It works on stock requesters, which is unusual for a change here.** A stock
Bee node accepts an announced threshold with no maximum: the pricing handler
validates only a minimum of 9,000,000, computed as 2 x refreshRate at
`pkg/node/node.go:1182`, or 2 x lightRefreshRate at `:1186`, and passed to
pricing at `:1201`; the same value appears as the `minPaymentThreshold` constant
at `:240`, which guards the node's own config (`pricing.go:100-102`), and
`NotifyPaymentThreshold` stores whatever arrives (`accounting.go:992-1001`).
Both files are byte-identical to `upstream/v2.8.2`, so this is upstream behavior
and not something our fork arranged. A wasp provider would therefore serve more
to downloaders running unmodified Bee. Those downloaders do not send the
local-only header, so they would need a different trigger, or would be left out;
the spec has to choose, and leaving them out is the safer default.

## What the spec must settle

- **The trigger.** The local-only header identifies a provider request, but it
  arrives with the *first* chunk request, which is also the point at which the
  node has extended no credit yet. Whether one such request is enough to raise
  a peer's threshold, or whether it should take several, is a judgment about
  how cheaply an arbitrary peer can obtain the larger credit by sending a header
  any wasp node will accept. **This is the main abuse question and the spec must
  answer it, not defer it.** Two things already bound the surface and should be
  stated rather than rediscovered: the header is honored only when
  `providers-enable` is on, and it is off by default (`cmd/bee/cmd/cmd.go:415`)
  (`pkg/retrieval/retrieval.go:124-129`), and local-only misses are already rate
  limited to 100 a second per peer and 1,000 a second in total (`:118-120`).
  Neither limits the hit path, which is what this change would put credit
  behind.
- **How much.** The sweep covers 13,500,000 to 54,000,000 with a separate point
  at 108,000,000. Going above 108,000,000 per peer is possible, since the cap is
  enforced only on the node's own configured value at startup and the growth
  path already raises `paymentThresholdForPeer` past it without re-checking. But
  doing so deliberately crosses a limit upstream chose, and the spec must say
  why that is acceptable rather than doing it silently.
- **When it goes back down.** The raise should not be permanent. Nothing in the
  current code lowers `paymentThresholdForPeer`; the growth path only ever
  raises it (`:657`). Lowering an announced threshold is a peer-visible change
  that the mixed-version test must cover, since a stock peer will simply adopt
  the lower number and may already be in debt above it.
- **The disconnect limit must move with it.** `disconnectLimit` is derived from
  the threshold (`:659`), and the two are set together everywhere they are set
  today. A per-peer raise that forgets this would disconnect exactly the peers
  it meant to help.
- **The total exposure.** Per-peer credit bounds the risk per peer but not in
  aggregate. The spec needs a ceiling on how many peers may hold the raised
  threshold at once, and what happens when it is reached.

## What to measure, and against what

The sweep already establishes the direction, so this change does not need to
re-establish it. What is unmeasured is whether restricting the raise to provider
requests keeps the benefit.

- **The provider's share and chunks a second**, against a node-wide raise to the
  same value in the same session, not against the numbers quoted above. That
  comparison is the whole point: if per-peer gives materially less than
  node-wide, the restriction is costing the benefit.
- **Total download time**, which the sweep did not settle. This is the figure
  the feature exists for and it should be treated as the primary one, with the
  control spread reported beside it. Time to first byte should be reported too
  but not credited to this change, since it is the hint rather than the
  threshold that moves it.
- **What the provider is left owed** at the end of a download, and across all
  peers at once, since that is the cost being accepted.
- **`bee_accounting_accounting_blocks_count` on the requester**, to confirm the
  overdraft gate was binding before and is not after. If it never moved, the
  threshold was not the constraint in that run and the rest does not follow.
- **A stock requester arm**, if the spec chooses to include stock downloaders.
  The claim that they would accept a larger announced threshold is read from
  upstream code and has never been run, so it should not be carried into a
  result without a condition that tests it.
- At least three runs per condition with the spread, interleaved, inside the
  fixed method rather than outside it. Both existing measurements are
  exploratory, and this is the one that should not be.

## What this does not fix, and a correction

**Correcting an earlier draft of this section.** It said that in the
expired-content case ([#313](https://github.com/crtahlin/wasp/issues/313))
"credit was never the limit", on the evidence of one diagnostic download whose
balance moved 40,000 units against an 18,000,000 window. That reading was wrong.
The run it rested on was spaced far enough apart for debt to settle between
requests, so the condition under test was absent from the very measurement used
to rule it out. Credit is the trigger, and
[#324](https://github.com/crtahlin/wasp/issues/324) is where that was
established: `retrieval.go` consumed the preferred candidate before
`prepareCredit` ran, so a refusal lasting `overDraftRefresh`, 600 ms, removed the
only holder of a chunk permanently. The chunk then fell to peers that never had
it, spent its 32 origin retries and returned `storage.ErrNotFound`.

**The fix for that is necessary and it is not sufficient, which is this
document's case.** Measured on the bench with the #324 fix in place, three runs,
sole-source content at the shipped lookahead buffer, that is with no header
overrides as a real client sends:

| Runs | Bytes of 4,194,304 | Time | Rate | Overdrafts |
|---|---|---|---|---|
| 3 of 3 | 1,310,720, so 31.2% | 1.34 to 1.42 s | 924,411 to 976,922 B/s | 311 to 429 |

All three truncate, with `curl` exit 18. The same build with
`Swarm-Lookahead-Buffer-Size: 0`, which turns the prefetch off and puts one
chunk in flight at a time, completes six runs of six at about 264,000 B/s.

So the prefetch is what breaks it, through credit. With many chunks in flight at
once, many overdraft at once; the #324 fix keeps the peer but tries ordinary
selection immediately rather than waiting, because waiting cost about 3x on
content the network also holds; and for content only this peer holds, ordinary
selection is where the chunk is lost. **Removing the permanent drop does not
remove the overdraft.** A limit set by a credit window is not removed by
changing which peer is asked next, and a per-peer window large enough that
provider requests do not overdraft at all is the direct answer to the
measurement above.

That makes the two changes complementary rather than alternatives, and it is
also a measurable prediction this proposal can be judged against: with the
per-peer raise in place, the sole-source download at the shipped buffer should
complete, and `bee_retrieval_preferred_overdrafts` should fall towards zero. If
the overdraft count does not fall, the threshold was not the constraint and the
rest of this document does not follow.

## The alternative, and why it is second

A requester can pay in advance instead, which moves the risk from the provider
to the requester. The mechanism is partly there: a payment arriving when the
payer owes nothing is booked as surplus (`accounting.go:1026-1041`), and the
balance is reduced without a floor on each credit (`:350`), so prepaying X buys
about X divided by the chunk price in extra chunks before the gate binds.

It is second because it needs machinery that does not exist. `Pay` is reached
only as `payFunction` from `settle` (`:509`, assigned `:1518`), and `settle`
requires existing debt of at least one refresh rate (`:458`) and an originated
debt of at least `minimumPayment` (`:483`), so a node that owes nothing cannot
send a cheque at all. A proactive path must also mirror the shadow reserve
bookkeeping that `settle` and `NotifyPaymentSent` do together in the shadow and
refresh reserve bookkeeping at `:503`, `:506` and `:957`, or the reserve goes
negative.

The two are not competitors. Once a provider has extended credit, prepayment
refills it faster, and they would multiply. They should be separate issues and
measured separately, because bundling
[#301 and #302](../cheque-acceptance-cost/results.md) is already a recorded
mistake in this fork.

## Related open issues

The rate at which a cheque clears debt is the other half of this, and it is
already filed: [#300](https://github.com/crtahlin/wasp/issues/300) on the chain
calls per received cheque, [#303](https://github.com/crtahlin/wasp/issues/303)
on one payment in flight per peer,
[#304](https://github.com/crtahlin/wasp/issues/304) on each cheque clearing only
a slice of the debt, and [#316](https://github.com/crtahlin/wasp/issues/316) on
`refreshDue` being computed without the one second cap in `settle`. The spec for
#303 and #304 predicts both do nothing at the measured rate
([accounting-gates](../accounting-gates/spec.md)). Whatever is proposed here
must not duplicate them.

---

Generated with help of AI.
