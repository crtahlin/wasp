# Splitting the refresh rate into what a node expects and what it grants

Issue: [#444](https://github.com/crtahlin/wasp/issues/444).
Type: experiment.

**This spec does not propose a configuration option.** Rule 8 asks for a
measurement before a dial is exposed, and the measurement below may well say the
dial is not worth having. What it proposes is a temporary, bench-only change and
the runs that decide the question.

## The mechanism, verified in the code

`refreshRate` (`pkg/node/node.go:238`, 4,500,000) serves two opposite roles.

**Creditor, granting.** `peerAllowance`
(`pkg/settlement/pseudosettle/pseudosettle.go:155-179`) returns
`min(elapsed * refreshRate, peerDebt)`. A refreshment is allowed at most once per
whole second per peer, since `currentTime == lastTime.Timestamp` returns
`ErrSettlementTooSoon`.

**Debtor, expecting.** `NotifyRefreshmentSent`
(`pkg/accounting/accounting.go:1253`) computes
`expectedAllowance = allegedInterval * refreshRate`, caps it at what it
attempted, and blocklists a peer that forgives less.

The roles already live in different packages. `accounting.NewAccounting`
receives the expectation rate and `pseudosettle.New` the grant rate
(`pkg/node/node.go:1263`, `:1299`), and inside `pseudosettle` the rate is used
only by `peerAllowance`. **Only the wiring is shared**, which is what makes the
split a small change rather than a refactor.

### Raising the grant is safe, lowering it is not

The debtor's upper bound is not the rate:

```go
// pkg/settlement/pseudosettle/pseudosettle.go:316-321
	acceptedAmount := new(big.Int).SetBytes(paymentAck.Amount)
	if acceptedAmount.Cmp(amount) > 0 {
		err = fmt.Errorf("pseudosettle: peer %v: %w", peer, ErrRefreshmentAboveExpected)
```

`amount` is what the debtor asked to clear. A faster grant moves the
acknowledgement up toward that number and cannot exceed it, because
`peerAllowance` is capped at `peerDebt`. The lower bound blocklists only a
creditor that forgives less than expected, which a faster grant satisfies more
easily.

The asymmetry runs the other way as well. **A grant rate below 4,500,000 gets
this node blocklisted by every stock peer.** If an option ever ships it needs a
floor, not merely a sensible default.

## Why the original motivation is withdrawn

Two figures that motivated this direction no longer hold, and saying so is the
point of writing the spec before the code.

**"The per-peer rate is about 264 KB/s, and the per-second refresh budget
accounts for essentially all of it."** The 264 KB/s is `retrieval-rate.md`'s
**lookahead buffer 0** arm. At the default buffer, sole-source downloads from one
provider now run at 2.19 to 3.28 MB/s
([flight-exit-results.md](flight-exit-results.md)), above the 1 MB/s that was set
as the target of this work.

**"Refreshments are the binding constraint."** Measured, three runs, one 4 MiB
sole-source hinted download each, harness `cp290/t444-settle-split.sh`:

| run | rate | debt incurred | cleared by cheques | cleared by refreshments | credit refusals |
|---|---|---|---|---|---|
| 1 | 1.91 MB/s | 320,090,000 | 260,870,000, 81%, 4 sent | 42,750,000, **13%**, 2 sent | 196 |
| 2 | 1.38 MB/s | 320,740,000 | 316,260,000, 99%, 6 sent | 13,500,000, **4%**, 2 sent | 145 |
| 3 | 2.32 MB/s | 319,930,000 | 195,360,000, 61%, 3 sent | 18,000,000, **6%**, 2 sent | 146 |

`bee_accounting_disconnects_overdraw_count` moved by zero on every run.

**Refreshments carry 4 to 13 per cent of the clearing.** Raising the grant rate
moves that minority. Cheques do the rest, and
`providers-payment-threshold` is already 108,000,000 on the bench provider
([#327](https://github.com/crtahlin/wasp/issues/327)), so the threshold headroom
this was meant to unlock is in place.

## The one reason it may still be worth doing

Credit is refused 145 to 196 times per download of about 1,035 chunks, so
roughly one chunk in six is refused at least once. Each refusal costs that chunk
a retry. If a faster grant removes most of those refusals, the download could
finish sooner even though the quantity of debt cleared barely moves.

That is a mechanism, not a result, and it is what the runs below test.

## The measurement is probably underpowered, and that is the hard part

The three baseline runs span **1.38 to 2.32 MB/s, a ratio of 1.69**, on
identical code, identical content size and a node whose peer count did not
change. Run-to-run variance is therefore wider than most effects worth having.

Three runs per arm, which is rule 7's floor, cannot resolve anything smaller
than the spread. So this experiment commits in advance to:

- **Nine runs per arm**, interleaved, alternating arms rather than running one
  block then the other, so that any drift in node state falls on both.
- **Reporting the full spread, not a mean alone.** If the two arms' ranges
  overlap, the answer is no effect.
- **A pre-registered decision rule**: the option ships only if the slowest run of
  the raised arm beats the fastest run of the baseline arm. Anything less is
  recorded as `neutral` and no dial is added. This is deliberately strict,
  because rule 8 says a dial that turns out not to matter is worse than no dial,
  and because a 1.69 spread will otherwise produce a convincing-looking mean
  difference from noise.

`gate-terms-measured.md:204-209` applies: **delivered bytes and wall-clock rate
are the acceptance criteria, never refusal counts.** The refusal count is a
diagnostic here and may not be used to claim success.

## The change to be measured

Bench-only, on an experiment branch, not for `main` unless the measurement
justifies it:

- give `pseudosettle.New` its own rate, separate from the one
  `accounting.NewAccounting` receives (`pkg/node/node.go:1299` and `:1263`);
- set it from an environment variable or a hard-coded bench value, so no
  configuration surface is added before the answer is known;
- leave the expectation rate, and every quantity derived from `refreshRate` in
  `pkg/accounting` (`thresholdGrowStep`, `thresholdGrowChange`,
  `minimumPayment`, and the light-node divisions) exactly as they are. Those are
  debtor-side and must not move.

The raised value should match the threshold the provider already announces,
108,000,000, so the grant can clear a full threshold of debt in one second.

## Arms

1. **Baseline**, current build, provider grant rate 4,500,000. Nine runs.
2. **Raised**, provider grant rate 108,000,000, expectation untouched. Nine runs,
   interleaved with arm 1.
3. **Compatibility**, on every run of both arms: neither node's blocklist gains
   an entry, and `bee_accounting_disconnects_overdraw_count` stays at zero. The
   requester in this pair runs the same build, so this does not prove stock
   compatibility on its own; the code argument above carries that, and this arm
   is the check that the argument is not wrong.

Recorded per run: delivered bytes, checksum, wall-clock rate, the cheque and
refreshment split, `bee_accounting_accounting_blocks_count`, and both
blocklists.

## Protocol impact

None. No wire message changes, no new field, and no version bump. The grant is
carried in the existing `PaymentAck` and the only change is the number in it,
which the protocol already allows to be anything up to what the debtor asked
for.

## Rollout and rollback

Nothing ships to `main` from this spec. The experiment branch is not merged
unless the decision rule above is met, in which case a separate issue and spec
cover the configuration option, its floor, and its documentation.

## Upstream portability

Not applicable. `refreshRate` serving both roles is upstream's design and is not
a defect there: upstream has no notion of a provider that wants to clear a
customer's debt faster. **No `affects-upstream` marker.**

Generated with help of AI.
