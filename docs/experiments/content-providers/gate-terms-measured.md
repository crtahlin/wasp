# The gate terms at the moment of refusal

Issue: [#343](https://github.com/crtahlin/wasp/issues/343). Instrument:
[#353](https://github.com/crtahlin/wasp/issues/353), merged as
[`5e527a22`](https://github.com/crtahlin/wasp/commit/5e527a22). Harness
`t16.sh`, outside this repository. Follows
[overdraft-terms.md](overdraft-terms.md), which established that polling could
not answer this.

**This is revision 2.** A review of revision 1 found the analysis had named the
wrong invariant and had dismissed its own strongest result as a tautology. Both
are corrected below and the wrong versions are stated, because one of them
re-asserted a claim the companion document had already withdrawn.

## Terms

- **The gate**: `increasedExpectedDebt > paymentThreshold + refreshDue`
  (`pkg/accounting/accounting.go:352`), where
  `increasedExpectedDebt = max(-balance, 0) + reservedBalance + price + surplusBalance`.
- **The floor and the ceiling**: `refreshDue` is `0` or one `refreshRate`
  (integer division at `:344`), so the limit on this bench is **13,500,000** or
  **18,000,000**. Those are the floor and the ceiling.
- **Margin**: `expected_debt - overdraft_limit`, how far a refused request
  exceeded the limit it was measured against.
- **Log field names** (`refresh_due`, `reserved_balance`, `settled_balance`) are
  the code's fields (`refreshDue`, `reservedBalance`, and the raw stored
  balance) as emitted by #353's line.
- **SWAP block**: both nodes run `swap-enable: true`, so debt can clear by
  cheque as well as by refreshment.
- **Sole-source content**: content only the provider holds, its postage batch
  having expired, so the network answers 404 for it.

## Conditions

Taken **2026-09-18**, 12:53 to 13:03 UTC. Requester `0.1.3-353-5e527a22`, the
merge commit exactly; provider `0.1.3-f005605d`, grant asserted zero at run
time. SWAP block, both nodes full. Sole-source content, 4,194,304 bytes, hinted
at the provider, `Swarm-Cache: false`.

Six runs, three at `Swarm-Lookahead-Buffer-Size: 0` and three at the shipped
buffer, spaced 93 s apart. **Balances were not reset between runs**, so debt and
allowance carry across, as in [overdraft-terms.md](overdraft-terms.md).

Delivered bytes: 196,608, 196,608 and **131,072** at lookahead 0, all with
`curl` exit 18. Revision 1 said all three returned 196,608, which the harness
output contradicts. All three are exact multiples of the 32,768 read unit.

`node/accounting` was raised to `all` per run and restored afterwards. The V(2)
entry was confirmed present in `GET /loggers` immediately after boot, before any
retrieval traffic.

**The data.** 2,381 refusal lines, redacted, at
`bee-experimental-infra/cp290/t16-refusals-full.txt`, outside this repository
per rule 10. Every figure below is computed from that file. Revision 1 quoted
aggregates from a live journal query whose output was not preserved, which made
its headline numbers unreproducible.

## What the gate saw

Every one of the 2,381 lines satisfies
`expected_debt == max(-settled_balance,0) + reserved_balance + price + surplus_balance`
and `overdraft_limit == payment_threshold + refresh_due`. That checks the log
line is wired to the right variables. It does **not** check the accounting,
since all the fields come from one call inside one held lock, and revision 1
overstated it as "the instrument checking itself".

### Per run, since the arms differ by more than an order of magnitude

| Run | Arm | Refusals | At the ceiling | `settled_balance` median | `reserved` above 4.5M | Largest margin |
|---|---|---|---|---|---|---|
| 1 | lookahead 0 | 18 | 0 | -13,210,000 | 0% | 20,000 |
| 2 | lookahead 0 | 50 | 0 | -13,370,000 | 0% | 290,000 |
| 3 | lookahead 0 | 14 | 0 | -12,680,000 | 0% | 280,000 |
| 4 | shipped buffer | 778 | 48 (6.2%) | -13,450,000 | 18% | 300,000 |
| 5 | shipped buffer | 760 | 43 (5.7%) | -13,230,000 | 17% | 280,000 |
| 6 | shipped buffer | 761 | 39 (5.1%) | -13,480,000 | 17% | 300,000 |

The three lookahead-0 runs contribute 82 refusals, 3.4 per cent of the total, so
a pooled figure is effectively a statement about the shipped-buffer arm.
Reported per arm accordingly:

- **Lookahead 0: every refusal is at the floor, in all three runs.**
- **Shipped buffer: 94.3 per cent at the floor**, 5.1 to 6.2 per cent at the
  ceiling.

## The answer to the question as asked

**The term that moves is the limit.** Across the whole set, 2,251 of 2,381
refusals (94.5 per cent) occur with `refreshDue` at zero, so the gate is at
13,500,000 rather than 18,000,000. Those refusals would not have occurred at the
ceiling.

`refreshDue` is zero because the elapsed term is integer-divided
(`min((now - ts)/1000, 1)` at `:344`), so it is 0 for the first 999 ms after
`refreshTimestampMilliseconds` is written, then steps to the full rate.

In run 1, all 18 refusals landed between **0.433 s and 0.928 s** after that
timestamp, every one inside the same integer second. Revision 1 quoted 0.925 s
as "exact", which is the seventeenth of the eighteen and was chosen without
reason.

**The ceiling refusals are the other side of the same clock.** They appear only
in the dense arm and only after **+0.95 to +0.96 s**, which is when the elapsed
term first reaches 1. That timing also explains the sampling error below: a
per-run cap of 400 lines stopped before that point in runs of 760 to 778.

They are also not concurrency alone, which revision 1 claimed. At the ceiling
the median settled balance is 13,230,000 and the median reserved balance
4,480,000, so the debt is **75 per cent** of the load.

## What the timestamp does and does not prove

Revision 1 said "the refreshment completed". Nothing here shows that.
`refreshTimestampMilliseconds` is written unconditionally at `:1106`, above
every check, and the below-expectation path at `:1150-1156` advances it and
returns **without crediting**, as [overdraft-terms.md](overdraft-terms.md)
records. A recent value proves an attempt reached that line, not that anything
was credited.

This run does not distinguish the two, and the distinction matters: a timestamp
advanced without a credit means the allowance was surrendered for nothing, which
is a more serious finding than the one revision 1 asserted. Left open.

## The invariant is the sum, not the debt

Revision 1 called the settled debt a steady state pinned near the threshold.
**That is wrong, and it re-asserted a claim
[overdraft-terms.md](overdraft-terms.md) had already withdrawn** under the
heading "The settled debt is not a steady state either".

What the data shows:

| Quantity | min | max | spread |
|---|---|---|---|
| `settled_balance` absolute | 0 | 13,480,000 | 13,480,000 |
| `reserved_balance` | 0 | 17,980,000 | 17,980,000 |
| **`max(-balance,0) + reserved_balance`** | **13,200,000** | **17,980,000** | **4,780,000** |

The **sum** is what sits against the limit, and its whole range is bounded by
the two limits the gate takes. Its components swing far more widely than it
does, and they are anti-correlated:

- where `reserved_balance` exceeds 8,000,000 (n=380), the settled balance median
  is **-620,000**;
- where `reserved_balance` is zero (n=1,773), it is **-13,450,000**.

So debt and reservation trade off against a roughly fixed total. A request
reserves, the reservation converts to debt, and the sum stays where it was.

## The sub-one-price margin is a finding, not a tautology

**Not one of the 2,381 refusals missed by as much as its own chunk price.**
Prices in the set range 270,000 to 320,000, so this is stated per refusal rather
than against a single price, which revision 1 did not do.

Revision 1 then dismissed this as true "by construction" at any limit. **That is
false, and this document's own mechanism is the counterexample.** The limit
*steps down* by 4,500,000 when the elapsed term resets. A request admitted while
the limit was 18,000,000 can leave the sum as high as 17,980,000; measured
against the floor a moment later, that would miss by about 4,480,000, roughly
**fourteen chunk prices**. Concurrency against a large debt could do the same.

Neither ever happens. The sum never exceeds the floor limit by more than
300,000. That is a measured invariant: **reservations do not survive the
step-down**, and whatever releases them does so before the tighter limit is
applied. Revision 1 discarded its strongest result.

## What still does not license a remedy

The re-equilibration caution stands, applied to the correct quantity. The
**sum** sits against whatever limit is in force. Raising the limit may simply
let the sum rise to meet it and deliver no more bytes.

Evidence points both ways, which is why it needs its own measurement:

- [#327](https://github.com/crtahlin/wasp/issues/327)'s raised per-peer
  threshold delivered 416, 347 and 127 chunks against 43 and 57 at the default,
  so raising the limit was not fully absorbed there;
- but that arm still truncated, and the sum still reached the threshold.

Acceptance for any such change must be **delivered bytes**, not refusal count. A
change that halves refusals while delivering the same bytes has moved the steady
state and achieved nothing. Filed as
[#359](https://github.com/crtahlin/wasp/issues/359).

## A sampling error found and corrected inside this run

The harness piped each run's journal through `head -400`. Three runs were under
that and untouched; the three dense runs were cut at 400 of 760 to 778, giving
1,282 lines rather than 2,381.

| | capped | uncapped |
|---|---|---|
| refusals | 1,282 | 2,381 |
| at the ceiling | **0** | **130** |
| `reserved_balance` max | 12,860,000 | 17,980,000 |

Because ceiling refusals only begin at +0.95 s, the cap excluded the entire
second regime. An interim reading of the capped data concluded "one mechanism,
universal", which the full set refutes. The cap is removed.

This also settles a prediction registered before the second arm ran, that the
shipped buffer would show a distinct ceiling regime driven by
`reserved_balance`. On the capped sample it looked refuted; on the full set it
is right in existence and wrong in weight, at about 5.7 per cent of that arm
rather than as the dominant mode, and with the debt still carrying 75 per cent
of the load.

## A correction to overdraft-terms.md

That document measured `reservedBalance` peaking at 12,860,000 with the prefetch
on and inferred concurrency was the likely driver. The peak is confirmed here,
and exceeded, at 17,980,000. But **the peak and the refusal do not coincide**:
in 1,773 of 2,381 refusals the reserved balance is exactly **zero**.

A peak sampled at 50 ms is not the value the gate saw. That is the specific
thing polling could not show, and why the instrument was worth building.

## History of this question, since the pattern is the finding

The mechanism now measured was proposed on #343, then **retracted on the grounds
that the code forbids it**, which was itself wrong, then restated as possible
under stated preconditions and explicitly not measured, and is measured here at
94.5 per cent of refusals.

Four designs were withdrawn before this, each derived by reading the code
carefully. The first instinct was closer to right than the confident refutation
that followed it. What the discipline bought was not the answer but the refusal
to act on any of the three versions before this run existed. Revision 1 of this
document then made the same class of error twice more, which is why its wrong
versions are left standing above rather than quietly replaced.

Generated with help of AI.
