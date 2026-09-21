# Spec: make settle use one definition of the refresh allowance

Issue: [#316](https://github.com/crtahlin/wasp/issues/316). Type: fix. Area: incentives.
Affects upstream: yes (the inline computation in `settle` is unmodified from bee v2.8.2).

## The issue describes upstream, and the fork has moved since

[#316](https://github.com/crtahlin/wasp/issues/316) says the allowance is computed in
five places, four of which cap the elapsed time at one second and one of which does not.
**That is exactly right about upstream and no longer right about this fork**, and the
difference is worth stating rather than patching around, because it changes what the fix
looks like here.

Upstream v2.8.2 still has all five, checked rather than assumed: capped at lines 313,
738, 749 and 1334, and the uncapped one in `settle` at 484. The issue's reading holds
there in full.

Since the issue was written, [#359](https://github.com/crtahlin/wasp/issues/359) gave
this fork a single definition, `Accounting.refreshDue` in `pkg/accounting/accrual.go`,
and moved two of the sites onto it. Today there are four computations:

| Where | How |
|---|---|
| `PrepareCredit`, the credit gate | `a.refreshDue(accountingPeer, now)` |
| The refreshment-received path | `a.refreshDue(accountingPeer, t)` |
| `Debit`, the disconnect limit | inline, `min(elapsed, 1) * refreshRate` |
| **`settle`, the payment amount** | **inline, `elapsed/1000 * refreshRate`, uncapped** |

So the count here is two using the shared definition, one capped inline, and one neither.
The defect the issue names is real and is in the place it says. Only its description of
the neighbours has been overtaken, and only in this fork.

## Problem

`settle` decides how much of a peer's debt a cheque should cover. Before deciding, it
subtracts what it expects refreshment to clear:

```go
timeElapsedInSeconds := (a.timeNow().UnixMilli() - balance.refreshTimestampMilliseconds) / 1000
refreshDue := new(big.Int).Mul(big.NewInt(timeElapsedInSeconds), a.refreshRate)
...
decreasedDebt := new(big.Int).Sub(debt, refreshDue)
expectedDecreasedDebt := new(big.Int).Sub(decreasedDebt, balance.shadowReservedBalance)
if paymentAmount.Cmp(expectedDecreasedDebt) > 0 {
	paymentAmount.Set(expectedDecreasedDebt)
}
if paymentAmount.Cmp(a.minimumPayment) >= 0 {
```

**Refreshment cannot clear that much.** Pseudosettle grants at most one refreshment per
whole second per peer, so between now and the next settlement opportunity it can clear at
most one `refreshRate`. The subtraction assumes it will clear `elapsed` seconds' worth,
which grows without bound as the time since the last refreshment grows.

The consequence runs the wrong way round. **The longer a peer has gone without
refreshing, the smaller the cheque this computes, and past a point there is no cheque at
all**, because the amount falls below `minimumPayment`. A peer whose refreshment has
stalled is exactly the peer that needs a monetary settlement, and this is the code that
decides not to send one.

A peer refreshing normally is unaffected: `elapsed` stays near one second and the term is
near one `refreshRate`, which is what the other three sites compute.

**The clock can also run backwards here.** `elapsed` is a signed subtraction with no
clamp, so a backwards step makes the term negative and *increases* the amount. #359
clamped that inside `refreshDue`; this site never got the clamp because it never used the
helper.

## The change, and the question it has to settle

Replace the inline computation with the shared definition:

```go
refreshDue := a.refreshDue(balance, now)
```

**This is not a pure refactor and the spec must not present it as one.** The two differ
in three ways, and only the first is unambiguously a fix:

1. **The cap.** The helper never returns more than one `refreshRate`. This is the defect.
2. **The clamp.** The helper never returns a negative value.
3. **The accrual mode.** Under `refresh-allowance-accrual: continuous` the helper returns
   a proportion of a `refreshRate` inside the first second, and applies `safeAccrualCap`.

The third is the one to be careful about. `safeAccrualCap` exists to keep a **credit
limit** below what a peer tolerates. In `settle` the quantity is not a limit; it is an
estimate of what refreshment will clear. Reusing the capped value here borrows a bound
that was reasoned about for a different purpose.

**The choice taken: use the helper anyway, and say why.** Under continuous accrual the
helper returns less than or equal to a `refreshRate` inside the first second, which is a
smaller subtraction, which makes the cheque larger rather than smaller. So the third
difference can only move cheques in the safe direction, and the alternative, a second
capped expression inline, would recreate the very thing this issue is about: one quantity
computed two ways.

## What this must not do

It must not increase how often or how much this node pays beyond what the debt justifies.
The subtraction exists so that a cheque does not cover debt that refreshment is about to
clear for free, and removing too much of it would make the node pay for what it could
have had at no cost. Capping the term at one `refreshRate` keeps that protection for the
next second, which is all pseudosettle can deliver.

## Verification

**This changes how much money leaves the node, so it is gated on a measurement and must
not merge on unit tests alone.** Per rule 7, at least three runs per condition with the
spread reported.

- Unit: a peer whose last refreshment is far in the past gets a cheque where it got none,
  and a peer refreshing normally gets the same cheque as before. The second is the guard:
  a fix that changed the common case would be a behaviour change, not a repair.
- Unit: a backwards clock step no longer increases the amount.
- Unit: under continuous accrual the amount is no smaller than under step accrual, which
  is the direction the argument above depends on.
- Bench: sole-source and network-held downloads, three runs each, recording cheques sent,
  `bee_accounting_payment_error_count`, delivered bytes and wall-clock rate, against the
  same build with the inline computation. **Delivered bytes and rate are the acceptance
  criteria, not the cheque count**, for the reason `gate-terms-measured.md` records:
  widening a credit window can let the debt rise to meet it and deliver nothing more.
- Mutations over the whole package with `-run .`: reverting to the inline computation
  must fail the first unit test; removing the clamp must fail the second.

## Scope

`pkg/accounting/accounting.go` and its tests. No configuration, no wire change. What a
node pays and when changes, which an operator can observe through `/settlements` and the
cheque counters, so `docs/DIFFERENCES.md` gains a row per rule 13.

## Upstream

`pkg/accounting/accounting.go` is modified in this fork, so a file-level diff proves
nothing and the defect was checked in upstream's own copy:

```
$ git show upstream/v2.8.2:pkg/accounting/accounting.go | grep -n timeElapsedInSeconds
313:  ... min((a.timeNow().UnixMilli()-accountingPeer.refreshTimestampMilliseconds)/1000, 1)
484:  ... (a.timeNow().UnixMilli() - balance.refreshTimestampMilliseconds) / 1000
738:  ... min(t.Unix()-accountingPeer.refreshReceivedTimestamp, 1)
749:  ... min((t.UnixMilli()-accountingPeer.refreshTimestampMilliseconds)/1000, 1)
1334: ... min(a.timeNow().Unix()-d.accountingPeer.refreshReceivedTimestamp, 1)
```

Line 484 is `settle`, and it is the only one without the cap, exactly as the issue says.
Upstream has no `refreshDue` helper at all; that is fork-authored. **So the fix upstream
is a different change from the fix here**: there it would be adding the cap inline, as
its four neighbours already do, rather than calling a helper that does not exist. The
issue should say so before anyone acts on the label, and a comment recording it is part
of this work. Per rule 11 the label is a marker for a later human decision and nothing
more.
