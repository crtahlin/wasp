# Raising what one peer may owe another: three options

Phase 1 measured a provider delivering about 4% of a download
([results.md](results.md)). This document explains why that number is what it
is, and compares three ways to change it. It is analysis, not a spec. Each
option that survives needs its own issue and its own spec before any code
(rule 2).

All code references are to `main` at `6eaaa651`, base `upstream/v2.8.2`.

## Why 4%

**Two separate payments are involved and they are easy to confuse.**

- **Postage**, paid by an uploader so that nodes keep chunks. When it expires,
  nodes delete the chunks. Nothing in this document concerns postage.
- **Bandwidth accounting**, owed by a downloader to whichever peer serves it a
  chunk, settled with cheques. That is what this document is about.

A requester may owe one peer at most the payment threshold that peer announced,
plus at most one second of refreshment
(`pkg/accounting/accounting.go:313-316`). At shipped defaults that is
13,500,000 + 4,500,000 = **18,000,000 units**. Past it, `PrepareCredit` returns
`ErrOverdraft` (`:320-322`) and the request goes to normal retrieval instead.

Refreshment refills that allowance at 4,500,000 units a second. At the roughly
305,000 units a chunk measured in phase 1 (6,400,000 per cheque, about 21
chunks), one peer can deliver:

- an opening burst of about **59 chunks**, from the 18,000,000 window;
- then about **15 chunks a second**, from refreshment.

So over a download lasting T seconds, one peer supplies roughly `59 + 15T`
chunks, whoever it is and however hard we prefer it. The phase 1 spec states the
same rate from the same constants ([spec.md](spec.md), Hypothesis: "about
4,500,000 / 320,000 about 14 chunks per second to a full node").

For content A, 4,129 chunks downloaded in about 5.5 s, that predicts about 141
chunks. Phase 1 measured **163 (160-177)** over three runs. The prediction is
low by about 13%, which is the right order but not a fit, so treat it as the
mechanism rather than as a calibrated model.

**This is the whole result.** The provider's share is set by the refresh rate
times the download duration. It is not set by how the preference is implemented,
which is why phase 1 found no speed gain and why the erasure-coding question
turned out not to matter
([#299](https://github.com/crtahlin/wasp/issues/299)). Nothing that changes
which peer is asked first can raise a ceiling that a different mechanism holds
down.

## Option A: pay in advance

**The idea.** Before or during a download, the requester sends the provider a
cheque covering work not yet done, so the allowance never runs out.

**What already exists.** A payment arriving when the payer owes nothing is not
rejected. `NotifyPaymentReceived` books the whole amount as **surplus balance**
when the receiver's balance with that peer is zero or negative
(`accounting.go:1026-1041`). So the receiving half needs no change, and no wire
change: a cheque is a cheque.

**What limits it, and this is the part that decides the option.** On the paying
side, `getIncreasedExpectedDebt` floors the debt at zero:

```go
currentDebt := new(big.Int).Neg(currentBalance)
if currentDebt.Cmp(big.NewInt(0)) < 0 {
    currentDebt.SetInt64(0)
}
```

(`accounting.go:251-254`). **Being in credit buys no headroom.** Paying
10,000,000 ahead when you owe nothing leaves the ceiling exactly where it was,
because the debt was already zero. Prepayment does not widen the window; it only
returns you to the start of it.

So the gain is not "a bigger allowance", it is "a faster reset". Instead of
waiting one second for 4,500,000 units of refreshment, the requester pays and
immediately has the full threshold available again. Phase 1 measured about 0.3 s
from a cheque being sent to the payment being registered, so the cycle would be
about 44 chunks, one threshold's worth, per 0.3 s rather than 15 chunks per
second. That is roughly a **tenfold** rate increase, derived from the code and
from one measured latency, not measured end to end.

**What has to be built.** There is no path to pay proactively. `Pay` is reached
only as `payFunction` from `settle` (`accounting.go:509`, assigned `:1518`), and
`settle` requires existing debt of at least one refresh rate (`:458`) and an
originated debt of at least `minimumPayment` (`:483`). A node that owes nothing
cannot currently send a cheque at all. That is the implementation.

**What it risks.**

- **Money for undelivered content.** You pay before you are served. If the
  provider takes the cheque and stops, the cheque is still valid and still
  cashable by them.
- **Running ahead of the peer's view.** The requester reduces its own debt when
  the cheque is written, but the provider only does so when it has received and
  checked it. A requester that treats the allowance as refilled too early can
  cross the provider's disconnect limit, which is 125% of the threshold it
  announced (`payment-tolerance-percent` default 25, `cmd.go:386`,
  `accounting.go:659`).

## Option B: the provider announces a larger threshold

**The idea.** The limit a requester obeys is not its own choice. It is the
threshold the *other side* announced. So a provider that wants to serve its
announced content can simply tell requesters they may owe it more.

**Why this is the cheapest of the three.** The mechanism is already there and
already on the wire.

- `NotifyPaymentThreshold` sets the value the peer sent, with **no maximum and
  no validation** (`accounting.go:992-1001`).
- The pricing handler checks only a **minimum**, `minPaymentThreshold`, which is
  `2 * refreshRate` = 9,000,000 (`pkg/pricing/pricing.go:100-102`,
  `pkg/node/node.go:240`). Anything above it is accepted.
- It travels on the existing `AnnouncePaymentThreshold` message
  (`pricing.go:125-148`), so there is **no wire change** and nothing for the
  protocol freeze.

**The consequence worth noticing: it works on stock requesters.** A stock Bee
node accepts the announcement and raises what it is willing to owe. So a wasp
provider would serve more to downloaders that are not running wasp at all. None
of the other options here has that property, and it is unusual for a fork change
to help unmodified peers.

**Size.** To cover a whole 4 MiB file, 1,033 chunks, in a single window the
provider would announce about 1,033 x 305,000 = **315,000,000 units**, against
the 13,500,000 default. That is a large multiple, and it is the honest number
for the availability case, where the provider is the only source and refreshment
at 15 chunks a second is not a fallback but the whole budget.

**What it risks, and who carries it.**

- **The provider carries all of it.** It serves up to the announced amount
  before being paid, and its own disconnect limit scales from what it announced
  (`accounting.go:659`), so it has deliberately widened its own exposure. If the
  requester never settles, that is the provider's loss.
- **It should not be a global setting.** Announcing 315,000,000 to every peer
  would let anyone draw that much. It wants to be per-peer and tied to a peer
  that is actually downloading content this node announced, which is information
  the provider has.
- **A requester pays no more in total.** Debt accrues only from chunks actually
  received, so a larger threshold defers settlement rather than increasing what
  is owed. There is no extraction risk for the requester in accepting a large
  announcement.

## Option C: rely on the growth that already happens

**The idea.** Thresholds already rise as a peer proves it pays.

**Why it is not a lever.** The growth exists
(`accounting.go:636-665`, `:1014-1016`) but is far too slow to matter inside a
download. The threshold rises by one refresh rate, 4,500,000, each time the
peer's cumulative repayment passes a checkpoint, and the first checkpoint is
`refreshRate * 100` = **450,000,000 units** (`:236`). At about 305,000 a chunk,
a requester must repay for roughly **1,475 chunks** to earn about **15 chunks**
of extra window.

Doubling the default window would take three such checkpoints, about 4,400
chunks repaid, roughly 18 MiB from that one peer. It is a mechanism for a
long-standing relationship between two nodes, not something a download can use.
Recorded here so nobody proposes it as an answer.

## Comparison

| | A: prepay | B: provider announces more | C: automatic growth |
|---|---|---|---|
| Widens the window | No, resets it faster | **Yes** | Yes, far too slowly |
| Wire change | None | None | None |
| Helps stock requesters | No | **Yes** | No |
| Code to write | New proactive payment path | Per-peer announcement policy | None |
| Who takes the risk | Requester, pays first | **Provider**, serves first | Nobody |
| Useful inside one download | Yes | Yes | No |

**Recommendation: B first, A second, C not at all.**

B is a smaller change, needs no new payment machinery, carries no risk for the
requester, and is the only one that helps downloaders running stock Bee. A is a
genuine follow-on rather than a competitor: once B has widened the window, A
refills it faster, and the two multiply rather than overlap. They should still
be separate issues and measured separately, because bundling #301 and #302 is
already a recorded mistake in this fork.

## What none of them fixes

**The download still gives up.** In the expired-content case
([#313](https://github.com/crtahlin/wasp/issues/313)) the requester asked the
provider for 45 chunks, was served 43, asked ordinary peers for others that no
longer exist, and abandoned the download with no bytes transferred. A wider
window means the provider *could* serve more; it does not make the requester ask
it for the rest rather than give up.

So for availability, B is probably necessary and clearly not sufficient, and
#313 has to be settled alongside it. For speed, phase 1 already showed a
provider contributing 3% to 5% with overlapping spreads, so a wider window is
worth measuring but should not be expected to produce a fast download on its
own.

## What would have to be measured

Any of these is accepted only on a measurement (rule 7), at least three runs per
condition reported with the spread. The figures above are derived from constants
and from one phase 1 run; none of them is a measurement of the option itself.

- Chunks delivered by the provider and its share of the download, against the
  phase 1 baseline in the same session rather than against the numbers quoted
  here.
- Total download time, which is what the feature is for, and which a wider
  window is not guaranteed to improve.
- `bee_accounting_accounting_blocks_count`, to confirm the overdraft gate was
  what bound before the change and is no longer binding after it. If it was
  never moving, none of this is the constraint and the measurement should stop
  there.
- For B, what the provider is left owed at the end of a download, since that is
  the cost it accepted.

---

Generated with help of AI.
