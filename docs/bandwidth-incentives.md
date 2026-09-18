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
(`pkg/node/node.go:239`), the price is `(32 - proximity) * 10,000`. A peer with
no shared prefix pays 320,000; a peer in the chunk's own neighbourhood pays far
less. **The closer a peer is to the content, the cheaper it is to ask.** That is
the incentive to fetch from the right part of the network rather than from
anyone.

**Measured on this fork's bench:** three independent readings of the mean price
paid per chunk gave 306,735, 306,454 and 309,141, so about 307,000 in practice.
That implies a mean proximity near 1, which is what uniformly distributed chunk
addresses give.

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
| `minPaymentThreshold` | 9,000,000 | `2 * refreshRate`, the least a full node will accept from a peer |
| `maxPaymentThreshold` | 108,000,000 | `24 * refreshRate`, the most a node accepts **as its own configuration** |
| `payment-tolerance-percent` | 25 | disconnect at 125% of what we extended |
| `payment-early-percent` | 50 | settle when debt reaches 50% of what the peer extended |
| light node factor | 10 | light nodes get a tenth of the thresholds and refresh rate |

**`maxPaymentThreshold` is not a ceiling on growth.** It bounds what a node will
accept as its own configured value (`node.go:810-811`) and what the fork's
provider setting may be set to. Nothing checks an incoming announcement against
it, and the growth path below walks straight past it.

## The ledger

Per peer, in `accountingPeer` (`pkg/accounting/accounting.go:130-160`):

- **balance**: what is owed. Negative means we owe them.
- **reservedBalance**: price of our requests in flight, already committed
  against our limit but not yet debited.
- **shadowReservedBalance**: the same for requests they have made of us, which
  they may already count as debt even though we have not credited it yet.
- **ghostBalance**: debt from requests we served to a peer that never paid and
  which we could not refuse. A second gate with no refresh tolerance.
- **surplusBalance**: overpayment received, applied to future debt.

The quantity actually gated is not the balance but `increasedExpectedDebt`: the
balance, plus everything reserved, plus surplus, plus the price of the request
being considered. A node can therefore be refused while its settled balance
still looks comfortable.

## Two ways debt is cleared

**Refresh, also called pseudosettle, is free and time based.** A peer allows
`refreshRate` units of debt to be forgiven per second of elapsed time, capped at
the debt actually outstanding (`pkg/settlement/pseudosettle/pseudosettle.go:144-165`).
It costs the payer nothing. It is the mechanism that lets small, steady traffic
run indefinitely without any payment at all.

**Cheques carry real value.** The swap protocol sends a signed cheque backed by
a chequebook contract, which the recipient can cash on chain. This is what
settles debt that refresh cannot keep up with.

**Measured on this fork's bench**, these are not close to equal: over single
downloads, pseudosettle moved 4,630,000 to 23,250,000 units while cheques moved
13,150,000 to 40,230,000. The bench is cheque-dominated, not refresh-bound, and
an analysis that assumes the free allowance is the whole story will be wrong
about what limits throughput.

## The three gates

In order of severity, all in `prepareCredit`
(`pkg/accounting/accounting.go:290-336`) except the last.

**1. Early settlement.** When expected debt reaches `earlyPayment`, which is
`100 - payment-early-percent` of what the peer extended, so 50% by default
(`:1011`), the node settles before it has to. This is deliberate: paying early
avoids blocking a later request that arrives while the balance sits near the
limit.

**2. Overdraft refusal.** The hard gate:

```go
// pkg/accounting/accounting.go:325-335
timeElapsedInSeconds := min((now-refreshTimestampMilliseconds)/1000, 1)
refreshDue := timeElapsedInSeconds * refreshRate
overdraftLimit := paymentThreshold + refreshDue
if increasedExpectedDebt > overdraftLimit {
	return nil, ErrOverdraft
}
```

Two things follow that surprise people. The elapsed term is **capped at one
second**, so waiting longer than a second buys no more headroom; the most this
adds is one refresh rate. And the limit is the threshold **the peer announced**,
so a node cannot raise its own spending limit by editing its own configuration.

**3. Disconnection.** A peer whose debt to us passes `disconnectLimit`, 125% of
what we extended, is disconnected and blocklisted for
`(latentDebt + paymentThreshold) / refreshRate` seconds. Refusing service is the
normal case; disconnection is for a peer that got past the refusal.

## Thresholds grow with history

A relationship that settles reliably is extended more credit over time
(`notifyPaymentThresholdUpgrade`, `accounting.go:640-678`). Each time cumulative
settled debt passes a checkpoint, the node raises what it extends to that peer
**by one refresh rate** and announces the new value.

Checkpoints start at `thresholdGrowStep = refreshRate * 100 = 450,000,000` and
step by that amount, until they pass a limit after which they double
(`:651-660`). So the early growth is linear in settled volume and then slows.

Three consequences worth knowing:

- **There is no cap.** The growth path adds a refresh rate with no upper bound,
  and `NotifyPaymentThreshold` stores whatever a peer announces without checking
  it (`:1004-1013`). **Measured:** a bench pair that had traded for days was
  announcing 94,500,000, and then 112,500,000, from a configured 13,500,000.
- **`Connect` resets it** to the configured value (`:1453`). A reconnect throws
  away the accumulated allowance, and it has to be re-earned.
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
| Preferred peers keep their place after a credit refusal | A named provider refused credit for a chunk is kept for up to eight further attempts rather than dropped at once, which upstream does. | [#324](https://github.com/crtahlin/wasp/issues/324) |
| Metrics | `bee_retrieval_preferred_overdrafts`, `_readmits`, `_misses`, and the provider grant counters. These are the only practical way to see credit refusal, which is otherwise silent in the logs. | [#324](https://github.com/crtahlin/wasp/issues/324), [#327](https://github.com/crtahlin/wasp/issues/327) |

A consequence of all this, measured rather than reasoned: a sole-source download
truncates when one chunk exhausts its eight readmissions, loses the only peer
holding it, and then spends its retry budget on peers that cannot help. See
[truncation-cause.md](experiments/content-providers/truncation-cause.md).

---

Generated with help of AI.
