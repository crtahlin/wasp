# The gate terms at the moment of refusal

Issue: [#343](https://github.com/crtahlin/wasp/issues/343). Instrument:
[#353](https://github.com/crtahlin/wasp/issues/353), merged as
[`5e527a22`](https://github.com/crtahlin/wasp/commit/5e527a22). Harness
`t16.sh`, outside this repository. Follows
[overdraft-terms.md](overdraft-terms.md), which established that polling could
not answer this.

**This is revision 3.** Two reviews refused it. Revision 1 named the wrong
invariant and re-asserted a claim the companion had withdrawn. Revision 2
corrected the arithmetic but replaced one wrong causal claim with another, and
left two experimental conditions mislabelled. Both wrong versions are stated
where they occur.

## Terms

- **The gate**: `increasedExpectedDebt > paymentThreshold + refreshDue`
  (`pkg/accounting/accounting.go:363`), where
  `increasedExpectedDebt = max(-balance, 0) + reservedBalance + price + surplusBalance`.
- **The sum**: `max(-balance, 0) + reservedBalance`, that is the gated quantity
  without the price of the request being decided. Used throughout because it is
  the part that persists between requests.
- **Floor and ceiling**: `refreshDue` is `0` or one `refreshRate`, from integer
  division at `:356`, so the limit is **13,500,000** or **18,000,000**.
- **Margin**: `expected_debt - overdraft_limit`, how far a refused request
  exceeded the limit it was measured against. It includes the price.
- **SWAP block**: both nodes run `swap-enable: true`.
- **Sole-source content**: content only the provider holds, its postage batch
  having expired, so the network answers 404 for it.

## Conditions

Taken **2026-09-18**, 12:53 to 13:03 UTC. Requester `0.1.3-353-5e527a22`;
provider `0.1.3-f005605d`, grant asserted zero at run time. SWAP block, both
nodes full. Sole-source content, 4,194,304 bytes, hinted at the provider,
`Swarm-Cache: false`. Runs 93 to 94 s apart, **balances not reset between
runs**.

**Neither arm used the shipped lookahead default.** The harness sent
`Swarm-Lookahead-Buffer-Size: 0` and `524288`. For a 4,194,304-byte file the
shipped value is `smallFileBufferSize = 262,144` (`pkg/api/bzz.go:51,59-64`);
524,288 is `largeFileBufferSize`, which this file is too small to select.
Revisions 1 and 2 called the second arm "the shipped buffer". It is not, and no
run here exercises the default.

**Delivered bytes**, which matter because they are the acceptance criterion for
any remedy:

| Arm | Run 1 | Run 2 | Run 3 |
|---|---|---|---|
| lookahead 0 | 196,608 | 196,608 | 131,072 |
| lookahead 524,288 | **0** | **0** | **0** |

All six `curl` exit 18. The arm producing 96.6 per cent of the refusals
delivered nothing at all, which revision 2 omitted.

**The data.** 2,381 refusal lines, redacted, at
`bee-experimental-infra/cp290/t16-refusals-full.txt`, outside this repository
per rule 10. Every figure below is computed from that file.

## Three peers, not one

The capture pools **three** peers, which revisions 1 and 2 did not disclose and
which changes what the aggregate means:

| Peer | Refusals | At ceiling | `refresh_timestamp_ms` = 0 | `settled_balance` | `reserved` max |
|---|---|---|---|---|---|
| A | 2,322 | 71 | 0 | 620,000 to 13,480,000 | **12,860,000** |
| B | 28 | **28** | **28** | 0 in every row | 17,980,000 |
| C | 31 | **31** | **31** | 0 in every row | 17,880,000 |

**Peer A is the provider relationship this work is about.** Peers B and C are a
different phenomenon entirely: no debt at all, no refreshment ever attempted, and
a reserved balance near 18,000,000. They are pure concurrency overruns against
peers this node owes nothing, and all 59 of their refusals are at the ceiling.

Everything below is **peer A** unless stated. Revision 2's invariant table
quoted 17,980,000 as the reserved maximum, which is peer B's; peer A's is
12,860,000, exactly the figure [overdraft-terms.md](overdraft-terms.md)
reported. Revision 2 said that peak was "confirmed here, and exceeded". It is
confirmed and **not** exceeded.

## The answer to the question as asked

**The term that moves is the limit.** Across the whole set 2,251 of 2,381
refusals (94.5 per cent) occur with `refreshDue` at zero, so the limit is
13,500,000 rather than 18,000,000, and every one of those would have passed at
the ceiling.

`refreshDue` is zero because the elapsed term is integer-divided at `:356`, so
it is 0 for the first 999 ms after `refreshTimestampMilliseconds` is written and
then steps to the full rate.

### Per run, since the arms differ by an order of magnitude

| Run | Arm | Refusals | At ceiling | `settled` median | `reserved` above 4.5M | Largest margin |
|---|---|---|---|---|---|---|
| 1 | lookahead 0 | 18 | 0 | -13,210,000 | 0% | 20,000 |
| 2 | lookahead 0 | 50 | 0 | -13,370,000 | 0% | 290,000 |
| 3 | lookahead 0 | 14 | 0 | -12,680,000 | 0% | 280,000 |
| 4 | lookahead 524,288 | 778 | 48 | -13,450,000 | 17.9% | 300,000 |
| 5 | lookahead 524,288 | 760 | 43 | -13,230,000 | 16.8% | 280,000 |
| 6 | lookahead 524,288 | 761 | 39 | -13,480,000 | 17.1% | 300,000 |

The lookahead-0 runs are 82 refusals, 3.4 per cent of the total, and **all at
the floor**. The dense arm is 94.35 per cent at the floor. A pooled figure
describes mainly the dense arm.

### Timing

In run 1 all 18 refusals landed **433 to 928 ms** after
`refreshTimestampMilliseconds`, inside one integer second. Revision 1 quoted
0.925 s as "exact"; that is the seventeenth of eighteen, and is 926 ms.

**Ceiling refusals begin at +1.018 s** measured from that same timestamp, range
1.018 to 1.063 s, which is when the elapsed term first reaches 1. Revision 2
said +0.95 s, which was measured from the first refusal of each run, a different
origin.

That applies to peer A. For peers B and C the elapsed term has been capped at 1
since boot, because no refreshment has ever occurred, so 59 of the 130 ceiling
refusals are not a timing effect at all.

## Why no refusal misses by much, and what is actually load bearing

**Not one of the 2,381 refusals missed by as much as its own price** (prices
range 270,000 to 320,000). Revision 1 called this a tautology, revision 2 called
it a measured invariant. **Revision 1 was closer to right**, and the exact
statement is:

A request is admitted only when `sum + price <= limit`, and on admission
`reservedBalance` grows by exactly that price (`:407`). So an admitted request
leaves `sum <= limit`, and therefore any later refusal has

```
margin = sum + price - limit  <=  price
```

Measured: `max(sum - limit_in_force) = -20,000`. **The sum never reaches the
limit in force at all.** Revision 2's "the sum never exceeds the floor limit by
more than 300,000" conflated that with the margin, which includes the price.

**What is not a construction is that the bound survives the limit stepping
down.** The limit falls by 4,500,000 when the elapsed term resets, and a
reservation made at the ceiling is not cancelled by that. Revision 2 asserted
"reservations do not survive the step-down"; the data shows the opposite. The
three step-downs in the capture, all peer A:

| Run | `settled` before to after | `reserved` before to after | sum before to after | margin |
|---|---|---|---|---|
| 4 | -13,450,000 to -8,950,000 | 4,480,000 to 4,480,000 | 17,930,000 to 13,430,000 | 250,000 both sides |
| 5 | -13,230,000 to -8,730,000 | 4,480,000 to 4,480,000 | 17,710,000 to 13,210,000 | 30,000 both sides |
| 6 | -13,480,000 to -9,620,000 | 4,480,000 to 3,840,000 | 17,960,000 to 13,460,000 | 280,000 both sides |

The reserved balance is **bit-identical** across two of the three. What holds
the invariant is that the refreshment credit and the allowance loss are the
**same event and equal in size**: the sum falls by exactly 4,500,000, matching
`refreshRate`, and the margin is unchanged on both sides.

That is the `amount - refreshRate = 0` case, measured. It is also the reason a
step-down does not strand a request: the credit pays for the allowance it costs.

## The debt and the reservation trade off

Within peer A the two components swing across their whole range and are
anti-correlated: where `reservedBalance` exceeds 8,000,000 (n=321) the settled
balance median is **-620,000**; where it is zero the median is
**-13,450,000**. Revision 1 called the settled debt a steady state, which
[overdraft-terms.md](overdraft-terms.md) had already withdrawn under the heading
"The settled debt is not a steady state either".

## What the timestamp does not prove

`refreshTimestampMilliseconds` is written unconditionally at `:1176`, above
every check, and the below-expectation path at `:1222-1226` advances it and
returns **without crediting**. A recent value proves an attempt reached that
line, not that anything was credited. Revision 1 said "the refreshment
completed"; withdrawn.

The step-down table above is the nearest thing to evidence either way: the sum
falls by exactly `refreshRate` at each one, which is consistent with a credit of
that size having been applied.

## What still does not license a remedy

The sum sits against whatever limit is in force, so **raising the limit may let
the sum rise to meet it** and deliver no more bytes. Whether that happens is
exactly what [#359](https://github.com/crtahlin/wasp/issues/359) exists to test,
with delivered bytes as the acceptance criterion.

Revision 2 cited #327 as evidence pointing the other way, quoting chunk counts
of 416, 347 and 127 against 43 and 57. **Those numbers are not in
[per-peer-threshold-results.md](per-peer-threshold-results.md)**, which reports
bytes and no chunk counts; 43 and 57 come from the #313 and #324 diagnostics, a
different measurement. The citation is withdrawn, and that source's own
conclusion points the other way: every run truncated in both arms.

## A sampling error found and corrected inside this run

The harness capped **each run's** journal at 400 lines. Three runs were under
that; the three dense runs were cut at 400 of 760 to 778, giving 1,282 lines.
Because peer A's ceiling refusals only begin past +1 s, they fell beyond the cap
in every run (first at index 705, 688 and 661), so the capped sample contained
**zero** ceiling refusals and suggested a single universal mechanism. The cap is
removed.

## A correction to overdraft-terms.md

That document measured `reservedBalance` peaking at 12,860,000 and inferred
concurrency was the likely driver. Peer A's peak here is **exactly 12,860,000**,
confirming it. But the peak and the refusal do not coincide: in 1,773 of 2,381
refusals the reserved balance is **zero**. A peak sampled at 50 ms is not the
value the gate saw.

## History of this question

Proposed on #343, then retracted on the grounds that the code forbids it, which
was wrong, then restated as possible and unmeasured, and measured here at 94.5
per cent of refusals. Four designs were withdrawn before this, each derived by
reading the code. Revisions 1 and 2 of this document then made the same class of
error three more times between them, which is why their wrong versions are left
standing above rather than replaced.

Generated with help of AI.
