# Splitting the refresh rate into what a node expects and what it grants

Issue: [#444](https://github.com/crtahlin/wasp/issues/444).
Type: experiment.

**This spec proposes no configuration option**, and it may propose nothing at
all. Rule 8 asks for a measurement before a dial is exposed. Review of the first
draft found that the measurement it described could not answer the question and
that two of its citations were wrong. Both are corrected below rather than
quietly removed.

## The mechanism, verified in the code

`refreshRate` (`pkg/node/node.go:238`, 4,500,000) serves two opposite roles.

**Creditor, granting.** `peerAllowance`
(`pkg/settlement/pseudosettle/pseudosettle.go:155-179`) returns
`min(elapsed * rate, peerDebt)`, where the rate is `lightRefreshRate` for a
light peer. On the first refreshment with a peer `lastTime.Timestamp` is zero,
so `elapsed` is the whole Unix epoch and `peerDebt` always binds.

The once-per-second rule is `currentTime == lastTime.Timestamp` returning
`ErrSettlementTooSoon` (`:155-158`). That gates on **distinct Unix seconds**,
not on elapsed time: two refreshments a millisecond apart across a second
boundary both pass. It is not a rate limit, and describing it as one overstates
how tightly refreshments are bounded.

**Debtor, expecting.** `NotifyRefreshmentSent`
(`pkg/accounting/accounting.go:1253`) computes
`expectedAllowance = allegedInterval * refreshRate`, caps it at
`attemptedAmount - refreshReservedBalance` (`:1242`, tighter than the attempted
amount alone), and blocklists a peer that forgives strictly less (`:1260`).

### Raising the grant trips no blocklist

Checked against every candidate, not assumed. The debtor's upper bound is not
the rate:

```go
// pkg/settlement/pseudosettle/pseudosettle.go:316-321
	acceptedAmount := new(big.Int).SetBytes(paymentAck.Amount)
	if acceptedAmount.Cmp(amount) > 0 {
		err = fmt.Errorf("pseudosettle: peer %v: %w", peer, ErrRefreshmentAboveExpected)
```

`amount` is what the debtor asked, and `paymentAmount` is clamped to the
allowance at `:218-220`, so it can never exceed it. The three time-sync errors
(`ErrTimeOutOfSyncAlleged`, `Recent`, `Interval`) compare timestamps only and
carry no amount term, so a larger grant cannot move them. The creditor's own
`debitAction.Apply()` uses `a.refreshRate`, which this split leaves untouched,
and a larger grant only lowers the balance further.

### Lowering it is unsafe, and the reason is sharper than a floor

A debtor blocklists a creditor when `min(interval * 4,500,000, attempted)`
exceeds what was granted. So a lowered grant is harmless when the debtor asks
for less than the lowered rate would give, and gets the creditor blocklisted
whenever the ask is larger, which is the ordinary download case.

The useful statement is therefore not "the option needs a floor". It is that
**4,500,000 is not a local setting at all. It is a value both peers must agree
on, enforced by a blocklist**, so a future option's floor could never be
validated locally.

## The roles are coupled, and the first draft said they were not

The first draft claimed "only the wiring is shared". That is wrong. `settle`
sizes a cheque by **predicting the creditor's grant**, using the debtor's own
rate (`pkg/accounting/accounting.go:575-589`):

```go
					refreshDue := a.settleRefreshDue(balance, now)
					...
					decreasedDebt := new(big.Int).Sub(debt, refreshDue)
					expectedDecreasedDebt := new(big.Int).Sub(decreasedDebt, balance.shadowReservedBalance)
					if paymentAmount.Cmp(expectedDecreasedDebt) > 0 {
						paymentAmount.Set(expectedDecreasedDebt)
					}
```

`settleRefreshDue`'s own comment (`:767-772`) states the assumption: the
uncapped product is "the correct prediction", so "money is not spent on debt
that costs nothing".

Raise only the creditor's grant and that prediction is wrong by the same factor.
The debtor keeps writing cheques for debt the creditor is about to forgive, and
those land at the creditor as surplus. **The cost of this change is that the
customer overpays, in real cheques**, until its own expectation rate is raised
too, which it cannot be without being blocklisted by stock peers. That is a
serious objection to the whole direction and it belongs at the front, not in a
costs section.

## Why the original motivation is withdrawn

**"The per-peer rate is about 264 KB/s and the refresh budget accounts for
essentially all of it."** Withdrawn: that figure is `retrieval-rate.md`'s
**lookahead buffer 0** arm.

The first draft replaced it with "2.19 to 3.28 MB/s", cited to
[flight-exit-results.md](flight-exit-results.md). **That range does not exist in
any document.** The 2.19 is from [dial-race.md](dial-race.md), the 3.28 from
`flight-exit-results.md`, and splicing the low end of one source to the high end
of another is exactly the composite the verification protocol forbids. What the
sources actually say, separately:

- `dial-race.md`: 2.41, 2.42, 2.44, then 2.19, 2.51, 2.96 MB/s.
- `flight-exit-results.md`: 1.80, 2.05, 2.68, 3.28 after, against 2.37, 2.45,
  2.45 before, **and it disclaims the comparison**: "The download rate is not
  compared here, deliberately ... this measurement is not sensitive enough to
  find a small one."

The baseline below then measures 1.38 MB/s on the same bench and content, which
is lower than every figure in that spliced range. No target of 1 MB/s is
recorded anywhere in `docs/`, so the claim that this work had one is dropped.

**"Refreshments are the binding constraint."** Measured, three runs, harness
`cp290/t444-settle-split.sh`:

| run | rate | debt incurred | cheques | refreshments | refusal events |
|---|---|---|---|---|---|
| 1 | 1.91 MB/s | 320,090,000 | 260,870,000, 81%, 4 | 42,750,000, **13%**, 2 | 196 |
| 2 | 1.38 MB/s | 320,740,000 | 316,260,000, 99%, 6 | 13,500,000, **4%**, 2 | 145 |
| 3 | 2.32 MB/s | 319,930,000 | 195,360,000, 61%, 3 | 18,000,000, **6%**, 2 | 146 |

`bee_accounting_disconnects_overdraw_count` moved by zero on every run.

## The check that has to come first

Everything above is worthless if `peerAllowance` is already bound by `peerDebt`
rather than by the rate, because then raising the rate changes nothing.

The baseline hints at both. With two refreshments per run and whole-second
elapsed, a rate-bound total must be an integer multiple of 4,500,000. Run 2 is
exactly 3x and run 3 exactly 4x, consistent with rate-bound. **Run 1 is 9.5x,
which no pair of whole-second intervals can produce**, so at least one of its
refreshments was bound by `peerDebt`.

So the terms alternate, and the size of any effect depends on how often the rate
binds. **The first action of this experiment is to measure that split**, by
logging which term `peerAllowance` returned, before any arm is run. If the rate
binds rarely, the experiment stops there and closes `neutral` at the cost of one
run rather than eighteen.

## The mechanism that might still justify it, stated accurately

The first draft said credit is refused "145 to 196 times per download of about
1,035 chunks, roughly one chunk in six, and each refusal costs a retry". Both
halves are wrong.

`AccountingBlocksCount` counts refusal **events, node-wide, across all peers,
with no chunk identity** (`accounting.go:371`). 196 events is equally consistent
with 196 chunks refused once and 20 chunks refused ten times, and the retrieval
loop re-asks the same chunk deliberately. There is no per-chunk incidence here.

And a refusal does not cost a retry. On the preferred path it falls straight
through to ordinary selection at no cost (`pkg/retrieval/retrieval.go:366-368`).
The only place it costs wall-clock time is the 600 ms `overDraftRefresh` sleep
at `:382-390`, which fires **only when every peer is already skipped**. The cost
is proportional to how often the loop exhausts its peers, not to the refusal
count.

The repository has already measured that refusal counts do not track outcome.
`docs/experiments/INDEX.md`, the #392 row: "refusal counts least of all, since
one completing run took 42,988 refusals and one truncating run took 191".

What survives is narrower: only **two refreshments fired per run** regardless of
run length, so the ceiling on the effect is two grants, each bounded by
`peerDebt`. Refreshments could plausibly rise from 4 to 13 per cent of clearing
to a majority. Whether that makes the download **faster** is the open question,
and it is not established by any of the above.

## Risk this experiment is specifically exposed to

[gate-terms-measured.md](gate-terms-measured.md), under "What this does not
license":

> The sum sits against whatever limit is in force, so **raising the limit may
> let the sum rise to meet it** and deliver no more bytes.

That is this experiment's most likely outcome, and the first draft cited this
passage as if it supported its measurement discipline. The acceptance criterion
it does establish is at `gate-terms-measured.md:54`: **delivered bytes**.

## The change to be measured

Bench-only, on an experiment branch, not for `main` unless the result justifies
it:

- give `pseudosettle.New` a rate separate from the one
  `accounting.NewAccounting` receives (`pkg/node/node.go:1299`, `:1263`);
- **on the provider only**, and settable at runtime rather than compiled in, so
  that arms can be switched without a restart. This matters, see below;
- leave the expectation rate and everything derived from `refreshRate` in
  `pkg/accounting` untouched: `thresholdGrowStep`, `thresholdGrowChange`,
  `minimumPayment`, and the light-node divisions are all debtor-side.

## Arms, and the confound that dictates their design

1. **Baseline**, provider grant 4,500,000.
2. **Raised**, provider grant 108,000,000, matching the threshold it already
   announces under [#327](https://github.com/crtahlin/wasp/issues/327).
3. **Compatibility**, every run: no blocklist entry on either node,
   `disconnects_overdraw_count` zero, and the provider's balance and surplus
   recorded, since granting the full `peerDebt` drives its balance to minus its
   shadow reserve on every refreshment.

**Arms must switch at runtime, not by restart, and the reason is not
convenience.** `NotifyRefreshmentReceived` feeds `totalDebtRepay` and trips
`notifyPaymentThresholdUpgrade` (`accounting.go:723-757`), which ratchets
`paymentThresholdForPeer` upward with no cap. A 24x larger grant crosses that
checkpoint 24x sooner, and only `Connect` resets it (`:1547-1581`). So the
raised arm permanently inflates the requester's credit limit for every later
baseline run: **interleaving does not cancel this, it spreads it**. The first
draft's stated reason for interleaving was exactly backwards.

Restarting between arms is worse: `Connect` also clears the #327 provider grant,
destroying the 108,000,000 threshold this argument leans on.

The honest consequence is that a clean A/B on one long-lived connection may not
be possible at all. The experiment must either measure the ratchet and show it
did not move between arms, or accept that the comparison is one-directional:
baseline first, raised second, with the baseline never repeated afterwards.

## The decision rule

The first draft required the slowest raised run to beat the fastest baseline
run. That is **anti-monotone in the number of runs**: adding runs raises the
baseline maximum and lowers the raised minimum, so nine runs per arm is strictly
harder to pass than three. It commits to more data and then penalises itself for
collecting it, and its one-sided permutation probability under the null is about
2e-5, which is not strictness but near-zero power.

Replaced with a rule fixed before the first run:

- analyse `log(rate)`, since the spread is multiplicative;
- form adjacent baseline and raised pairs and use a one-sided **exact Wilcoxon
  signed-rank** test on the paired differences; nine pairs give a minimum
  one-sided p of 1/512;
- ship only if **both** the median paired difference is at least **+25 per cent**
  of the baseline median **and** one-sided exact p < 0.05;
- report the full spread either way, and record every exclusion (restart, peer
  count change) with its reason.

The effect-size floor is what answers rule 8's concern about a dial that does
not matter. Statistical significance alone would not.

## Protocol impact

No wire message changes and no version bump, so `make protocol-freeze` passes,
and it is run because `pkg/settlement/pseudosettle/pseudosettle.go` is a file the
lock tracks.

That is not the same as "no protocol impact". 4,500,000 is a value two peers
must agree on, enforced by a blocklist, and by the coupling above it is also the
debtor's cheque-sizing input. Diverging from it is a protocol-visible act even
though no byte on the wire changes shape.

## Rollout and rollback

Nothing ships to `main` from this spec. The experiment branch is not merged. If
the decision rule is met, a separate issue and spec cover the option, its floor,
the overpayment cost to the debtor, and its documentation.

## Upstream portability

Not applicable. One rate serving both roles is upstream's design and not a
defect there: upstream has no notion of a provider that wants to clear a
customer's debt faster. **No `affects-upstream` marker.**

Generated with help of AI.
