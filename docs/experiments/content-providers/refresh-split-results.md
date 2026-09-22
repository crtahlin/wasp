# Measured: a faster grant buys no bytes, and costs the provider income

Issue: [#444](https://github.com/crtahlin/wasp/issues/444). Spec:
[refresh-split.md](refresh-split.md).

**Outcome: neutral. No configuration option ships, and no code reaches `main`.**

Measured 2026-09-22 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester. Harnesses `cp290/t444-settle-split.sh`,
`cp290/t444-binding.sh`, `cp290/t444-overpay.sh` and `cp290/t444-arm.sh`,
outside this repository. Sole-source content per run, 4,194,304 bytes at
redundancy NONE through `POST /wasp/ingest`, downloaded with a hint naming the
provider.

Only the provider changed. It ran an experiment build in which the rate it
**grants** as creditor is settable, while the rate it **expects** as debtor
stays at 4,500,000. The requester ran unmodified `main` throughout, so exactly
one node differs between the arms and the requester is, for this purpose, a
stock peer.

The override is visible in the provider's log rather than inferred:

```
"msg"="wasp #444 experiment: pseudosettle grant rate overridden"
"grant_rate"="108000000" "expectation_rate"="4500000"
```

## The first check: does the rate ever bind?

The spec put this first, because raising a term that never binds does nothing.
`peerAllowance` returns `min(elapsed * rate, peerDebt)`, so it was instrumented
to count which term won.

| run | rate-bound | debt-bound |
|---|---|---|
| 1 | 0 | 1 |
| 2 | 2 | 0 |
| 3 | 2 | 0 |

The rate bound 4 of 5 grants, so the experiment was worth running. The pattern
is that the **first** refreshment after an idle period is debt-bound, because
elapsed is large and the debt is then the smaller term, while refreshments
during sustained traffic are rate-bound.

This also confirmed, independently, a reading taken from the baseline before the
instrumentation existed: two of three baseline runs cleared exactly 3x and 4x
the rate, and the third totalled 9.5x across two refreshments, which no pair of
whole-second intervals can produce.

## The result

Four downloads per arm, each arm begun from a fresh restart so the payment
threshold ratchet started in the same place. One raised run returned 404 and is
excluded: that is [#435](https://github.com/crtahlin/wasp/issues/435), the
unconnected-provider case after a restart, and unrelated to settlement.

| | baseline | raised, 24x grant |
|---|---|---|
| cleared by refreshments | 18.1M | **74.5M**, 4.11x |
| cleared by cheques | 286.6M | **217.8M**, 0.76x |
| `surplusBalance` at the provider | 0 | **0** |
| rate per run | 1.70, 1.73, 1.76, 1.97 MB/s | 1.73, 1.83, 1.82 MB/s |
| mean rate | **1.79 MB/s** | **1.79 MB/s** |

**The change does what it says.** Refreshments clear four times more.

**It delivers no additional bytes per second.** The two arms' ranges overlap
completely and their means are identical to three significant figures. That is
exactly the outcome [gate-terms-measured.md](gate-terms-measured.md) warns about
under "What this does not license": raising a limit lets the cleared sum rise to
meet it and delivers no more bytes.

## An objection of mine that the measurement refuted

The spec argued, at some length and at the front, that raising only the
creditor's grant would make the **debtor overpay**. `settle` sizes a cheque by
predicting the creditor's grant from the debtor's own rate
(`pkg/accounting/accounting.go:575-589`), and `settleRefreshDue`'s comment
states that assumption outright, so raising one side alone falsifies the
prediction by the same factor.

**Measured, it does not happen.** `surplusBalance` on the provider's view of the
requester stayed at zero across every run of both arms.

The argument was wrong for a reason worth keeping. `settle` caps the payment at
the **current** balance, which already reflects whatever the creditor forgave
earlier. Under-predicting the next forgiveness cannot push a payment past debt
that actually exists, so the prediction error is real but bounded by a quantity
that is itself shrinking. A mis-prediction of a subtraction is not the same as
an overpayment.

## The cost that is real

Cheques fell to 0.76 of baseline. **The provider forgives about a quarter more
of what it is owed, per download served.** The debtor gains exactly that amount.

That is forgone income for the operator running the provider, not a hazard to
the protocol, and it is the thing to put in front of anyone who would turn such
a dial up. It is also a larger and better grounded objection than the
overpayment argument it replaces.

## What this does not establish

**The sample is three completed runs per arm, against the nine the spec asks
for**, and the spec's decision rule wanted a paired exact test on `log(rate)`
with a +25 per cent effect-size floor. That rule is not satisfied here, and no
firm negative is claimed at this sample size. What can be said is that the arms
are indistinguishable and that the distance to a +25 per cent floor is not a
sampling question: the observed difference is zero, not small.

A single bench pair is also not a network. The requester ran stock `main` and
never blocklisted the provider, and `disconnects_overdraw_count` stayed at zero
throughout, which is consistent with the code argument that a faster grant trips
no check. It does not prove compatibility with the wider network, and the spec
said so before the runs.

## Disposition

Closed `neutral`. No option ships, per rule 8: a dial that turns out not to
matter is worse than no dial. The experiment branch is not merged. The bench is
restored to the shipped grant rate.

What survives for a later reader is the code analysis in
[refresh-split.md](refresh-split.md): the two roles are real and separable, the
grant may be raised without tripping any peer-side check, and it may **not** be
lowered, because 4,500,000 is a value two peers must agree on rather than a
local setting.
