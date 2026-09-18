# How bandwidth is paid for

Every chunk a node serves is charged for. This document says how that works,
because the mechanism is spread across several packages, most of it is
undocumented, and getting the direction of a single field backwards inverts the
meaning of everything downstream.

Written for `main` at `527f32b2`, base `upstream/v2.8.2`. Everything here is
**unmodified upstream behaviour** unless a line says otherwise; the fork's own
changes are gathered at the end. Every constant was read from the source rather
than recalled, and the measured figures come from this fork's bench and are
marked as measured.

## The unit and the price

There is no token movement per chunk. Nodes keep a running **balance** with each
peer in an internal accounting unit, and settle it in larger, rarer payments.

A chunk's price depends on how close the serving peer is to the chunk:

```go
// pkg/pricer/pricer.go:34
func (pricer *FixedPricer) PeerPrice(peer, chunk swarm.Address) uint64 {
	return uint64(swarm.MaxPO-swarm.Proximity(peer.Bytes(), chunk.Bytes())+1) * pricer.poPrice
}
```

With `MaxPO = 31` (`pkg/swarm/swarm.go:27`) and `basePrice = 10,000`
(`pkg/node/node.go:239`), the price is `(32 - proximity) * 10,000`. Proximity
here counts shared leading bits, so a larger number means closer. A peer sharing
no prefix with the chunk **charges** 320,000; one in the chunk's own
neighbourhood charges far less.

**Payment is hop by hop, and the spread between the two hops is the incentive.**
A requester is charged on the proximity of **the peer it asks**
(`pkg/retrieval/retrieval.go:499`, `PeerPrice(peer, chunk)`), while a node
serving a request charges on **its own** proximity (`:620`, `Price(chunk)`,
which is `PeerPrice(ownOverlay, chunk)`). A forwarder therefore pays less than
it collects, and keeps 10,000 units for every proximity order it gains on the
chunk. That margin, not the absolute price, is what pays for relaying.

A requester never owes the node that finally stores the chunk. It owes the peer
it asked, which owes the peer it asked, and so on.

**Pushsync is priced the same way** (`pkg/pushsync/pushsync.go:263`, `:683`).
Everything below is described for retrieval, and applies to both.

**Measured on this fork's bench**
([measurement.md](experiments/content-providers/measurement.md)): three readings
of the mean price per credit decision gave 306,735, 306,454 and 309,141, so
about 307,000. That implies a mean proximity near 1, which is what uniformly
distributed chunk addresses give. The counter behind it is node-wide and counts
credit decisions rather than deliveries from one peer, so it is a price
estimate, not a per-peer charge.

## The two thresholds, and which is which

This is the part that is easy to invert. Each peer relationship has **two**
limits, and they are not symmetric.

| Field | Meaning | Set by |
|---|---|---|
| `paymentThresholdForPeer` | **What we announce to the peer.** How much debt we will let them run up with us before we expect payment. | our own configuration, then growth |
| `paymentThreshold` | **What the peer announced to us.** How much we may spend with them before they refuse us. | `NotifyPaymentThreshold`, from their announcement |

So a node's own `payment-threshold` setting is **how much credit it extends to
others**, not how much it may spend. What a node may spend is whatever each peer
chose to extend to it. The API reports the second as `thresholdReceived` and the
first as `thresholdGiven` (`pkg/api/accounting.go`).

Defaults and bounds, all in `pkg/node/node.go:235-246` and
`cmd/bee/cmd/cmd.go:389-391`:

| Name | Value | What it is |
|---|---|---|
| `refreshRate` | 4,500,000 per second | the free allowance rate, below |
| `payment-threshold` | 13,500,000 | default credit extended to each peer, three seconds of refresh |
| `minPaymentThreshold` | 9,000,000 | `2 * refreshRate`, the least a full node will accept from a peer and the least it will configure for itself (`node.go:806-808`) |
| `maxPaymentThreshold` | 108,000,000 | `24 * refreshRate`, the most a node accepts **as its own configuration** |
| `payment-tolerance-percent` | 25 | disconnect at 125% of what we extended |
| `payment-early-percent` | 50 | settle when debt reaches 50% of what the peer extended |
| light node factor | 10 | light nodes get a tenth of the thresholds and refresh rate |

**`maxPaymentThreshold` is not a ceiling on growth.** It bounds what a node will
accept as its own configured value (`node.go:810-811`) and what the fork's
provider setting may be set to. Nothing checks an incoming announcement against
it, and the growth path below walks straight past it.

## The ledger

**The sign convention first, because it is the most confusing thing here.** A
**credit** action is us spending with a peer, for a request we made. A **debit**
action is us charging a peer for one they made. A balance that is negative means
we owe them.

Per peer, in `accountingPeer` (`pkg/accounting/accounting.go:131-151`):

- **balance**: what is owed, negative when we owe them.
- **reservedBalance**: price of **our** requests in flight, committed against
  our limit but not yet credited.
- **shadowReservedBalance**: the same for requests **they** have made of us,
  which they may already count against their own limit before we debit them.
- **ghostBalance**: charges we prepared for a peer's request and never applied,
  because the delivery failed or was abandoned (`:1383`, in
  `debitAction.Cleanup`). Not unpaid service: nothing was delivered. It is a
  second disconnect gate with no refresh tolerance, and it only ever rises until
  `Connect` clears it (`:1450`).
- **surplusBalance**: overpayment received, which offsets future debt but is
  **added** to expected debt in the gate below, because it is value we have
  already been given.

The quantity actually gated is not the balance but `increasedExpectedDebt`
(`:256-279`):

```text
max(-balance, 0) + reservedBalance + price + surplusBalance
```

Two things follow that the balance alone does not show. The debt term is
**clamped at zero**, so a peer being in debt to us buys no spending headroom at
all. And only `reservedBalance` is counted; `shadowReservedBalance` is
**subtracted** to form the separate figure the early-settlement test uses.

## Two ways debt is cleared

**Refresh, also called pseudosettle, is free and time based.** A peer allows
`refreshRate` units of debt to be forgiven per second of elapsed time
(`pkg/settlement/pseudosettle/pseudosettle.go:168`), capped at the debt actually
outstanding (`:175-179`).
It costs the payer nothing. It is the mechanism that lets small, steady traffic
run indefinitely without any payment at all.

**Cheques carry real value.** The swap protocol sends a signed cheque backed by
a chequebook contract, which the recipient can cash on chain. This is what
settles debt that refresh cannot keep up with.

**Cheques settle only debt this node originated.** `settle` pays
`-originatedBalance` (`accounting.go:493`), and a credit action returns without
touching that figure when the request was not originated here (`:387-393`). So
debt from **forwarding** other nodes' requests is cleared by refresh alone, and
that traffic genuinely is refresh-bound even where a node's own requests are not.
A cheque is also only issued above `minimumPayment`, `refreshRate / 5` or
900,000 (`:245`).

**No claim is made here about which of the two dominates in practice.** An
earlier version of this document compared node-wide settlement counters against
the per-peer allowance and concluded the bench was cheque-dominated. That
comparison is invalid, and this project had already recorded why: a node-wide
counter cannot be compared with a per-peer allowance
([measurement.md](experiments/content-providers/measurement.md)). The figures
are removed rather than restated.

## The three gates

In order of severity, all in `PrepareCredit`
(`pkg/accounting/accounting.go:281-336`) except the last. Note the capital: a
lowercase `prepareCredit` is a different function, in
`pkg/retrieval/retrieval.go:497`.

**1. Early settlement.** When expected debt **less what the peer may already
have counted** reaches `earlyPayment`, and the balance is actually negative, the
node settles before it has to (`:312`). `earlyPayment` is
`100 - payment-early-percent` of what the peer extended, so 50% by default
(`:1011`). Paying early avoids blocking a later request that arrives while the
balance sits near the limit.

**2. Overdraft refusal.** The hard gate:

Simplified from `pkg/accounting/accounting.go:325-335`, which uses `big.Int`:

```text
timeElapsedInSeconds = min((now - refreshTimestampMilliseconds) / 1000, 1)
refreshDue           = timeElapsedInSeconds * refreshRate
overdraftLimit       = paymentThreshold + refreshDue
refuse when increasedExpectedDebt > overdraftLimit
```

Three things follow that surprise people.

**The elapsed term is integer division, then capped at one.** It is 0 below one
second and 1 at or above it, so `refreshDue` takes exactly two values, 0 or one
refresh rate. Nothing accrues at 200 ms or 600 ms. Four designs in this project
were withdrawn for assuming it ramps.

**The clock is not the request's.** `refreshTimestampMilliseconds` is written
only when a refreshment completes (`:1106`), so the term is usually already past
one second and pinned at its cap. A completed refreshment resets it, which drops
`refreshDue` to 0 and **tightens** the limit for the following second; the debt
reduction is what helps, not this term.

**The limit is the threshold the peer announced**, so a node cannot raise its own
spending limit by editing its own configuration.

**3. Disconnection.** A peer whose debt to us passes `disconnectLimit`, 125% of
what we extended, plus the same refresh term (`:1355`), is disconnected and
blocklisted for
`(max(latentDebt, refreshRate) + paymentThreshold) * multiplier / refreshRate`
seconds (`:1390-1409`). The `paymentThreshold` in that formula is the node's own
configured value, not either per-peer field. `latentDebt` includes
`ghostBalance` (`:912`). Refusing service is the normal case; disconnection is
for a peer that got past the refusal.

## Thresholds grow with history

A relationship that settles reliably is extended more credit over time
(`notifyPaymentThresholdUpgrade`, `accounting.go:640-678`). Each time cumulative
settled debt passes a checkpoint, the node raises what it extends to that peer
**by one refresh rate** and announces the new value.

Checkpoints start at `thresholdGrowStep = refreshRate * 100 = 450,000,000` and
step by that amount, until they pass a limit after which they double
(`:651-660`). So the early growth is linear in settled volume and then slows.

Three consequences worth knowing:

- **There is no upper cap.** The growth path adds a refresh rate without bound,
  and `NotifyPaymentThreshold` stores whatever a peer announces
  (`:1004-1013`). There is a **lower** bound: an announcement below
  `minPaymentThreshold` is rejected and the peer disconnected
  (`pkg/pricing/pricing.go:102-105`). **Observed on the bench**, with
  `cp290/t12e.sh` and `cp290/t13.sh`: a pair that had traded for days announced
  94,500,000, and then 112,500,000, from a configured 13,500,000.
- **`Connect` resets the announced value** to the configured one (`:1453`), and
  also zeroes the balance and the surplus balance (`:1465`, `:1470`), so unpaid
  debt does not survive a reconnect either. The allowance is **not** fully
  re-earned from scratch: `thresholdGrowAt` is rewound but the cumulative
  settled total is not, so a returning peer re-fires the upgrade on its next
  settlement. That asymmetry is
  [#333](https://github.com/crtahlin/wasp/issues/333).
- **So a long-lived relationship behaves quite differently from a fresh one**,
  and a measurement that does not say which it was is hard to interpret.

## What this means for anyone measuring

- **Record the balance and the announced threshold with every run.** Two results
  in this project had to be withdrawn for not having them.
- **A node's own `payment-threshold` does not control what it can spend.** To
  change what a requester may spend, change what the *provider* announces.
- **Refusal is per request, not per download.** A download is many chunks, and
  what matters is whether an individual chunk can be paid for at the moment it
  is asked.
- **Free allowance is small next to real traffic.** 4,500,000 per second is
  about 14 chunks per second at the measured price. A download moving faster
  than that is running on settlement, not on refresh.

## Where this fork differs

| Change | What it does | Record |
|---|---|---|
| `providers-payment-threshold` and `providers-credit-budget` | A provider announces a larger threshold to a peer downloading content it announced, granted once per connection, never lowered, bounded by a node-wide budget. Measured: it takes a fresh peer to the configured value at once, and does not by itself make a large sole-source download complete. | [#327](https://github.com/crtahlin/wasp/issues/327), [results](experiments/content-providers/per-peer-threshold-results.md) |
| Preferred peers keep their place after a credit refusal | A named provider refused credit for a chunk is kept for up to eight further attempts rather than dropped at once. **Upstream has no preferred path at all**; on its ordinary path it skips a refused peer for 600 ms and retries, so dropping at once was this fork's own earlier behaviour, not upstream's. | [#324](https://github.com/crtahlin/wasp/issues/324) |
| Per-path credit metrics | `bee_retrieval_preferred_overdrafts`, `_readmits`, `_misses`, and the provider grant counters. Upstream already counts refusals node-wide as `bee_accounting_accounting_blocks_count`; these add which path they happened on, which is otherwise invisible because credit refusal is silent in the logs. | [#324](https://github.com/crtahlin/wasp/issues/324), [#327](https://github.com/crtahlin/wasp/issues/327) |
| Threshold announcements serialised per peer | Concurrent announcements to one peer are coalesced, so a grant and a growth step cannot race. | [#327](https://github.com/crtahlin/wasp/issues/327) |
| Chequebook liquidity cached for 30 s | Reading it once per cheque is what limits how fast debt clears with a peer. | [#301](https://github.com/crtahlin/wasp/issues/301), [#302](https://github.com/crtahlin/wasp/issues/302) |

A consequence of all this, measured rather than reasoned: a sole-source download
truncates when one chunk exhausts its eight readmissions, loses the only peer
holding it, and then spends its retry budget on peers that cannot help. See
[truncation-cause.md](experiments/content-providers/truncation-cause.md).

---

Generated with help of AI.
