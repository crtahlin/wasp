# The gate terms at the moment of refusal

Issue: [#343](https://github.com/crtahlin/wasp/issues/343). Instrument:
[#353](https://github.com/crtahlin/wasp/issues/353), merged as
[`5e527a22`](https://github.com/crtahlin/wasp/commit/5e527a22). Harness
`t16.sh`, outside this repository. Follows
[overdraft-terms.md](overdraft-terms.md), which established that polling could
not answer this.

**Why this needed an instrument.** The mechanism measured here was proposed on
#343, then retracted on the grounds that the code forbids it, which was wrong,
then restated as possible and unmeasured. Four designs were withdrawn before it,
each derived by reading the code. `overdraft-terms.md` carries that history in
full.

This document went through six drafts and six reviews. The measurement never
changed and every figure below reproduces from the raw capture. What kept
failing was the running commentary about which draft had said what, so that
commentary is gone: the claims that were wrong are collected once, under
**Claims withdrawn** at the end, rather than annotated throughout.

## Terms

- **The gate**: `increasedExpectedDebt > paymentThreshold + refreshDue`
  (`pkg/accounting/accounting.go:363`), where
  `increasedExpectedDebt = max(-balance, 0) + reservedBalance + price + surplusBalance`.
- **The sum**: `max(-balance, 0) + reservedBalance`, the gated quantity without
  the price of the request being decided. It is the part that persists between
  requests.
- **Floor and ceiling**: `refreshDue` is `0` or one `refreshRate` of 4,500,000,
  so the limit is **13,500,000** or **18,000,000**. The integer division at
  `:356` holds it at 0 for the first second; the `min(..., 1)` cap on the same
  line stops it growing after that.
- **Margin**: `expected_debt - overdraft_limit`. It includes the price.
- **SWAP block**: both nodes run `swap-enable: true`.
- **Sole-source content**: content only the provider holds, its postage batch
  having expired, so the network answers 404 for it.

## Conditions

Taken **2026-09-18**, 12:53 to 13:03 UTC. Requester `0.1.3-353-5e527a22`;
provider `0.1.3-f005605d`, grant asserted zero at run time. SWAP block, both
nodes full. Sole-source content, 4,194,304 bytes, hinted at the provider,
`Swarm-Cache: false`. Runs 92.7 to 94.1 s apart, **balances not reset between
runs**.

**Neither arm used the shipped lookahead default.** The harness sent
`Swarm-Lookahead-Buffer-Size: 0` and `524288`. For a 4,194,304-byte file the
shipped value is `smallFileBufferSize = 262,144` (`pkg/api/bzz.go:51,59-64`);
524,288 is `largeFileBufferSize`, which this file is too small to select. No run
here exercises the default, and the same mislabelling reached
[overdraft-terms.md](overdraft-terms.md), corrected there.

**Delivered bytes**, which matter because they are the acceptance criterion for
any remedy:

| Arm | Run 1 | Run 2 | Run 3 |
|---|---|---|---|
| lookahead 0 | 196,608 | 196,608 | 131,072 |
| lookahead 524,288 | **0** | **0** | **0** |

All six `curl` exit 18. The arm producing 96.6 per cent of the refusals
delivered nothing.

**The data.** 2,381 refusal lines, redacted, at
`bee-experimental-infra/cp290/t16-refusals-full.txt`, outside this repository
per rule 10. Every refusal figure comes from that file. The delivered bytes
above and the 1,282-line capped count come from `t16-gate-terms.txt`, the
harness output, in the same place. The provider build is from its `/health` at
deploy time and is in neither file.

## Three peers, not one

| Peer | Refusals | At ceiling | `refresh_timestamp_ms` = 0 | `settled_balance` | `reserved` max |
|---|---|---|---|---|---|
| A | 2,322 | 71 | 0 | -620,000 to -13,480,000 | **12,860,000** |
| B | 28 | **28** | **28** | 0 in every row | 17,980,000 |
| C | 31 | **31** | **31** | 0 in every row | 17,880,000 |

**Peer A is the provider relationship this work is about**, which the data shows
rather than assumes: it is the only peer with any debt, and the only one present
in the lookahead-0 runs. Peers B and C have no debt at all, no refreshment on
record, and a reserved balance near 18,000,000. They are concurrency overruns
against peers this node owes nothing, and all 59 of their refusals are at the
ceiling.

A zero `refresh_timestamp_ms` does not prove no refreshment was attempted: every
error path in `pseudosettle`, the time-based settlement protocol, passes
`timestamp = 0`, so a failed one leaves the same value. What follows either
way is that their elapsed term is pinned at the cap,
which is why they sit permanently at the ceiling.

**Everything below is peer A unless stated.**

## The answer

**The term that moves is the limit.** Across the whole capture 2,251 of 2,381
refusals (94.5 per cent) occur with `refreshDue` at zero, so the limit is
13,500,000 rather than 18,000,000, and every one of those would have passed at
the ceiling: their largest `expected_debt` is 13,800,000.

`refreshDue` is zero because the elapsed term is integer-divided at `:356`, so
it is 0 for the first 1,000 ms after `refreshTimestampMilliseconds` is written,
that is 0 through 999 inclusive, then steps to the full rate. The data agrees:
the last floor refusal is at +999.7 ms and the first ceiling one at +1,017.8 ms.

### Per run, peer A only

| Run | Arm | Refusals | At ceiling | `settled` median | `reserved` above 4.5M | Largest margin |
|---|---|---|---|---|---|---|
| 1 | lookahead 0 | 18 | 0 | -13,210,000 | 0% | 20,000 |
| 2 | lookahead 0 | 50 | 0 | -13,370,000 | 0% | 290,000 |
| 3 | lookahead 0 | 14 | 0 | -12,680,000 | 0% | 280,000 |
| 4 | lookahead 524,288 | 751 | 21 | -13,450,000 | 14.9% | 270,000 |
| 5 | lookahead 524,288 | 744 | 27 | -13,230,000 | 15.1% | 50,000 |
| 6 | lookahead 524,288 | 745 | 23 | -13,480,000 | 15.3% | 300,000 |

Peers B and C appear **only in the dense runs** (12 and 15, 8 and 8, 8 and 8),
so pooling inflates that arm's ceiling share about 1.8 times, 5.65 per cent
against peer A's own 3.17 per cent, and moves run 5's largest margin from 50,000
to 280,000.

Peer A overall: **3.06 per cent at the ceiling, 96.83 per cent at the floor in
the dense arm.** The lookahead-0 runs are 82 refusals, all peer A, all at the
floor.

### Timing

In run 1 all 18 refusals landed **433 to 928 ms** after
`refreshTimestampMilliseconds`, inside one integer second. Peer A's ceiling
refusals begin at **+1,017.8 ms**, range 1,017.8 to 1,063.1 ms, which is when
the elapsed term first reaches 1.

## Why no refusal misses by much

**Not one of the 2,381 refusals missed by as much as its own price** (prices
range 270,000 to 320,000). That follows from the gate's own construction:

A request is admitted only when `sum + price <= limit`, and admission grows
`reservedBalance` by exactly that price (`:407`). So an admitted request leaves
`sum <= limit`, and any later refusal has

```
margin = sum + price - limit  <=  price
```

Measured: `max(sum - limit_in_force) = -20,000`. The sum never reaches the limit
in force at all.

The step from the gate's own quantity to `sum` drops `surplusBalance`, so it
also assumes that term stays zero. It does here: **`surplus_balance` reads 0 in
all 2,381 rows**. `NotifyPaymentReceived` can raise it, and this is a SWAP block
where cheques arrive, so it is a precondition rather than a constant.

**The induction holds only while two further things do not happen**, neither of
which fires here:

- `NotifyRefreshmentReceived` (`:1253-1287`) lowers the balance without lowering
  `reservedBalance`, and its comment says it may "potentially put us into debt".
  That raises the sum with no admission.
- `NotifyPaymentThreshold` (`:1074-1083`) sets `paymentThreshold` to whatever
  the peer announces, which can lower the limit.

Neither is a race, since both take the same lock. **`payment_threshold` reads
13,500,000 in all 2,381 rows**, and peer A is a peer this node owes throughout.
That constancy also closes a confounder
[overdraft-terms.md](overdraft-terms.md) flagged as unasserted.

## The bound survives the limit stepping down

The limit falls by 4,500,000 when the elapsed term resets, and a reservation
made at the ceiling is not obviously cancelled by that. The three step-downs in
the capture, all peer A:

| Run | `settled` before to after | `reserved` before to after | sum before to after | margin |
|---|---|---|---|---|
| 4 | -13,450,000 to -8,950,000 | 4,480,000 to 4,480,000 | 17,930,000 to 13,430,000 | 250,000 both sides |
| 5 | -13,230,000 to -8,730,000 | 4,480,000 to 4,480,000 | 17,710,000 to 13,210,000 | 30,000 both sides |
| 6 | -13,480,000 to -9,620,000 | 4,480,000 to 3,840,000 | 17,960,000 to 13,460,000 | 280,000 both sides |

The before and after rows are 8.1 ms, 0.1 ms and 5.0 ms apart. In runs 4 and 5
the reserved balance is **bit-identical** across the step-down, so the
reservation plainly survives it. Run 6 is ambiguous, two unknowns and one
equation: its 640,000 of reservation was applied if the credit was 4,500,000, or
cancelled if the credit was 3,860,000, and
`-13,480,000 + 4,500,000 - 640,000 = -9,620,000` fits the first.

What is directly measured is that **the sum falls by exactly 4,500,000 at each
step-down**, matching `refreshRate`, leaving the margin unchanged on both sides.
That is what a refreshment crediting exactly the allowance its timestamp reset
costs would produce, the case [overdraft-terms.md](overdraft-terms.md) writes as
`amount - refreshRate = 0`. It is consistent with such a credit having been
applied. It is not proof that one was, because `refreshTimestampMilliseconds` is
written unconditionally at `:1176`, above every check, and the
below-expectation branch at `:1220` returns without crediting (`:1222-1226`). A
recent value proves an attempt reached that line, nothing more.

## The debt and the reservation trade off

Within peer A the two components swing across their whole range and are
anti-correlated: where `reservedBalance` exceeds 8,000,000 (n=321) the settled
balance median is **-620,000**; where it is zero the median is **-13,450,000**.

## What this does not license

The sum sits against whatever limit is in force, so **raising the limit may let
the sum rise to meet it** and deliver no more bytes. Whether that happens is
what [#359](https://github.com/crtahlin/wasp/issues/359) exists to test, with
delivered bytes as the acceptance criterion.

[per-peer-threshold-results.md](per-peer-threshold-results.md) is the nearest
evidence and is **inconclusive**: its one matched pair, both runs starting from
a zero balance, delivered 2,293,760 bytes against 360,448, 6.4 times more at the
raised threshold. That is a large effect at n=1, against rule 7. The same
document records that it truncated in both arms, which answers completion rather
than delivered bytes.

## A sampling error corrected inside this run

The harness capped **each run's** journal at 400 lines. Three runs were under
that; the three dense runs were cut at 400 of 760 to 778, giving 1,282 lines.
Because peer A's ceiling refusals only begin past +1 s, they fell beyond the cap
in every run, at the 706th, 689th and 662nd line of their runs. So the capped
sample contained **zero** ceiling refusals and suggested a single universal
mechanism. The cap is removed.

## A correction to overdraft-terms.md

That document gave 12,860,000 as its headline `reservedBalance` peak, from runs
spanning 12,780,000 to 12,880,000, and inferred concurrency was the likely
driver. Peer A's peak here is 12,860,000, inside that spread.

But the peak and the refusal do not coincide: in 1,773 of peer A's 2,322
refusals, 76 per cent, the reserved balance is **zero**. A peak sampled at 50 ms
is not the value the gate saw, which is what polling could not show and why the
instrument was worth building.

## Claims withdrawn

Each of these appeared in a draft of this document and is wrong. Collected here
rather than marked through the text, because the marking was itself the largest
source of error across five reviews.

- **That the settled debt is a steady state pinned near the threshold.** The
  quantity that sits against the limit is the sum. The debt swings across its
  whole range and trades off against the reservation.
- **That the sub-one-price margin is a measured invariant rather than a
  consequence of the gate's construction.** It follows from the admission rule,
  as derived above.
- **That reservations do not survive the step-down.** In two of three they are
  bit-identical across it.
- **That the ceiling refusals are concurrency alone.** At peer A's ceiling the median
  settled balance is -13,450,000 against a median reserved balance of 4,480,000,
  so the debt carries about 75 per cent of the load.
- **That a refreshment completed**, asserted from a recent
  `refresh_timestamp_ms`. The timestamp is written unconditionally.
- **That #327 delivered 416, 347 and 127 chunks against 43 and 57.** Those chunk
  counts appear nowhere in the repository; 43 and 57 trace to the #313 and #324
  diagnostics, a different measurement.
- **That overdraft-terms.md's 12,860,000 peak was confirmed here and
  exceeded, at 17,980,000.** That maximum is peer B's. Peer A's is exactly
  12,860,000, so it is confirmed and not exceeded.
- **Two timing claims**: 0.925 s quoted as exact, which was the seventeenth of
  eighteen refusals; and ceiling refusals beginning at +0.95 s, which was
  measured from each run's first refusal rather than from the refresh timestamp.
- **Two mislabelled conditions**: an arm called the shipped lookahead default
  that is twice it, and a three-peer capture reported as one relationship.

Generated with help of AI.
