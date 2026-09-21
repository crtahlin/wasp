# Spec: reset the repayment counter when a peer reconnects

Issue: [#333](https://github.com/crtahlin/wasp/issues/333). Type: fix. Area: incentives.
Affects upstream: yes, checked rather than assumed. See "Upstream" below.

## Problem

`Connect` rewinds the checkpoint that decides when a peer's payment threshold grows, but
not the counter that checkpoint is compared against. A returning peer with a long
payment history then triggers the growth path on every repayment instead of once.

Two fields in `pkg/accounting/accounting.go` work as a pair:

- `totalDebtRepay` (`:148`), commented "since being connected, amount of cumulative debt
  settled by the peer";
- `thresholdGrowAt` (`:149`), the cumulative figure at which the next threshold upgrade
  is due.

A **payment threshold** is how much debt this node lets a peer run up before it expects
payment. The growth mechanism raises it for a peer that has proved it settles, so a
reliable peer is trusted with more.

`Connect` resets the checkpoint:

```go
// :1539
accountingPeer.thresholdGrowAt.Set(thresholdGrowStep)
```

It never resets `totalDebtRepay`. That field is set to zero once, when the per-peer
record is created (`:706`), and nothing else writes zero to it: the only other writes are
the two additions at `:1109` and `:1276`. The record itself is never removed. `Disconnect`
marks the peer not connected and blocklists it, and there is no `delete` against
`a.accountingPeers` anywhere in the file, so the record and its cumulative figure live for
the lifetime of the process.

The comment on the field is therefore not what the code does. "Since being connected" is
the intent; the value is since the process first saw the peer.

## What it costs

`thresholdGrowStep` is `refreshRate * linearCheckpointStep`, 4,500,000 x 100 =
**450,000,000**.

Take a peer whose cumulative repayment has reached 10,000,000,000, roughly 37 minutes of
settling at the full refresh rate. It disconnects and reconnects. `Connect` puts
`thresholdGrowAt` back to 450,000,000 while `totalDebtRepay` stays at 10,000,000,000, so
the test `totalDebtRepay > thresholdGrowAt` (`:1111`, `:1278`) is **already true** before
the peer has repaid anything on this connection.

`notifyPaymentThresholdUpgrade` then fires on the peer's first repayment after
reconnecting and on every repayment after that. Each firing advances the checkpoint and
raises that peer's threshold by one refresh rate.

**Corrected after review.** An earlier version of this spec said "about 22 consecutive
upgrades", reasoning that the checkpoint closes the gap 450,000,000 at a time and
10,000,000,000 divided by 450,000,000 is about 22. That is wrong, and it is wrong for the
reason it explicitly dismissed. The checkpoint steps by 450,000,000 only while it is
below `thresholdGrowChange`, which is `refreshRate * 1800` = **8,100,000,000**. It
reaches exactly that on the eighteenth upgrade, and `notifyPaymentThresholdUpgrade` then
**doubles** it to 16,200,000,000, which is past 10,000,000,000, so the run stops there.

The measured answer, driven against the shipped constants, is **18 upgrades**, raising
the threshold by 81,000,000. Since pseudosettle grants at most one refreshment per whole
second per peer, one a second is the ceiling rather than the rate.

The overshoot matters as much as the burst. After it, the checkpoint sits at
16,200,000,000 against a counter near 10,000,000,000, so the same peer must repay another
6,200,000,000, about 23 minutes at the full refresh rate, before it earns anything at
all. A peer that never disconnected needs 450,000,000, about 100 seconds. So the defect
is a burst of unearned upgrades followed by a long stall, which is both a better
description and more clearly wrong than a steady over-grant.

At the shipped default `payment-threshold` of 13,500,000 the run ends at 94,500,000,
which is **below** `maxPaymentThreshold` of 108,000,000. It passes that limit only for a
configured base above 27,000,000.

The peer is told about each upgrade, so the announcements are real traffic, not just an
internal number.

## The change

Reset the counter where the checkpoint is reset:

```go
accountingPeer.totalDebtRepay.Set(zero)
accountingPeer.thresholdGrowAt.Set(thresholdGrowStep)
```

## Why resetting the counter, rather than keeping the checkpoint

The two possible repairs are opposites: reset both, so the peer starts fresh, or reset
neither, so the peer keeps what it earned. **`Connect` itself settles which one this
design intends**, and the evidence is the block the line sits in (`:1532-1540`):

```go
accountingPeer.connected = true
accountingPeer.fullNode = fullNode
accountingPeer.shadowReservedBalance.Set(zero)
accountingPeer.ghostBalance.Set(zero)
accountingPeer.reservedBalance.Set(zero)
accountingPeer.refreshReservedBalance.Set(zero)
accountingPeer.paymentThresholdForPeer.Set(paymentThreshold)
accountingPeer.thresholdGrowAt.Set(thresholdGrowStep)
accountingPeer.disconnectLimit.Set(disconnectLimit)
```

`paymentThresholdForPeer` goes back to the **base** threshold, which is the earned
growth being deliberately discarded, and the persisted balance and surplus balance are
written to zero just below. Every other piece of per-connection state starts again. So
keeping `totalDebtRepay` is the outlier, and zeroing it makes the field match both its
own comment and the nine lines around it. Nothing new is decided here; the existing
decision is simply applied to the one field that missed it.

## What this does not do

It does not change how fast a threshold grows for a peer that stays connected, does not
change `thresholdGrowStep`, and does not touch the light-node variants beyond what
`Connect` already selects. It does not remove the per-peer record or change its
lifetime, which stays a separate question. Nothing on the wire changes: the upgrade
notification is the same message, sent the same way, and only the occasions on which it
is sent change.

## Upstream

`pkg/accounting/accounting.go` **is** modified in this fork, so the file being different
proves nothing either way and the defect had to be checked in upstream's own copy. It is
there, unmodified:

```
$ git show upstream/v2.8.2:pkg/accounting/accounting.go | grep -n totalDebtRepay
148:  totalDebtRepay  *big.Int // since being connected, amount of cumulative debt settled by the peer
613:      totalDebtRepay: big.NewInt(0),
1012:  accountingPeer.totalDebtRepay = new(big.Int).Add(accountingPeer.totalDebtRepay, amount)
1014:  if accountingPeer.totalDebtRepay.Cmp(accountingPeer.thresholdGrowAt) > 0 {
1179:  accountingPeer.totalDebtRepay = new(big.Int).Add(accountingPeer.totalDebtRepay, amount)
1181:  if accountingPeer.totalDebtRepay.Cmp(accountingPeer.thresholdGrowAt) > 0 {
```

Zero is written once, at creation on line 613, and every other write is an addition.
Upstream's `Connect` (lines 1408 to 1436) carries the same nine-line reset block and the
same `thresholdGrowAt.Set(thresholdGrowStep)` on line 1432, with no reset of the counter.

Per rule 11 the label is a marker for a later human decision and nothing more.

## Verification

- A unit test that drives a peer's `totalDebtRepay` above `thresholdGrowStep`,
  disconnects and reconnects it, then makes **one** repayment, and asserts exactly one
  threshold upgrade rather than a run of them. On unmodified code that test sees the run.
- The same for a **light peer**. Added after review found that guarding the reset with
  `if fullNode` passed the whole package, so an entire arm of the fix was covered by
  nothing. A light peer uses `lightThresholdGrowStep` and reconnects more often rather
  than less.
- A second test that a peer which has **not** passed the checkpoint still gets its
  upgrade at the right point after a reconnect, so the fix did not simply switch growth
  off.
- A third that a peer which stays connected is unaffected, since that is the path the
  mechanism was written for and the one this must not disturb.
- The existing `pkg/accounting` tests keep passing.
- A mutation: removing the new line must fail the first test, and zeroing
  `thresholdGrowAt` instead of `totalDebtRepay` must fail it too.

Mutations are run over the whole package with `-run .`, never with a name filter. A
filter has twice produced a false "not caught" result in this repository.

## Scope

`pkg/accounting/accounting.go` and `pkg/accounting/accounting_test.go`. No configuration,
no wire change. What a node does after a peer reconnects changes in a way an operator can
observe through the threshold and through the upgrade announcements, so
`docs/DIFFERENCES.md` gains a row per rule 13.
