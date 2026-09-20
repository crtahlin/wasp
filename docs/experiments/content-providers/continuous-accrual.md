# Accruing the refresh allowance continuously

Issue: [#359](https://github.com/crtahlin/wasp/issues/359). Evidence:
[gate-terms-measured.md](gate-terms-measured.md), merged as
[`03160761`](https://github.com/crtahlin/wasp/commit/03160761). Instrument:
[#353](https://github.com/crtahlin/wasp/issues/353).

Code references are to commit
[`f1c9c7e6`](https://github.com/crtahlin/wasp/commit/f1c9c7e6), base
`upstream/v2.8.2`. `pkg/accounting` is **not** unmodified: it carries 1,196
inserted and 11 deleted lines against that base, from #327 and #353. The gate
itself is unmodified upstream code; `git diff` the function against that commit
before relying on any line number here.

## Terms

- **The gate** is the overdraft check in `PrepareCredit`
  (`pkg/accounting/accounting.go:363`). It refuses a chunk request when this
  node's projected debt to a peer would exceed the limit it allows itself.
- **`refreshDue`** is the allowance the gate grants above the threshold a peer
  announced, meant to represent debt the peer has forgiven by the passage of
  time since the last refreshment.
- **A refreshment** is a payment in time rather than money, made by the
  `pseudosettle` protocol. It clears debt without a cheque.
- **The floor** is a limit computed with `refreshDue` at zero, 13,500,000 at the
  shipped default. **The ceiling** is the same limit with a full `refreshRate`
  added, 18,000,000. The gate produces one or the other and nothing between.
- **The sum** is `increasedExpectedDebt`, the quantity compared against the
  limit: `max(-balance, 0) + reservedBalance + price + surplusBalance`.
- **The lookahead buffer** is the prefetch size the download path reads ahead
  with, set per request by `Swarm-Lookahead-Buffer-Size`. The shipped value for
  a file under 10 MB is `smallFileBufferSize = 262,144` (`pkg/api/bzz.go:51`).
- **Sole-source content** is content held by exactly one reachable node, so a
  refusal against that node cannot be served by anyone else.
- **A provider grant** is the larger payment threshold a provider announces to a
  requester under [#327](https://github.com/crtahlin/wasp/issues/327). It is off
  by default and must be zero for the arms below, or it moves the limit under
  the measurement.

## Problem

`refreshDue` is computed from an integer division:

```go
// pkg/accounting/accounting.go:356
timeElapsedInSeconds := min((a.timeNow().UnixMilli()-accountingPeer.refreshTimestampMilliseconds)/1000, 1)
refreshDue := new(big.Int).Mul(big.NewInt(timeElapsedInSeconds), a.refreshRate)
```

So it is 0 for the first 1,000 ms after the refresh timestamp is written, then
steps to a full `refreshRate`. The allowance is granted as a cliff, although
`refreshRate` is documented as a rate, "accounting units refreshed per second"
(`pkg/node/node.go:236`).

**Measured**, on the provider relationship this work is about: of peer A's 2,322
refusals, **2,251 (96.9 per cent) occurred with `refreshDue` at zero**, and every
one of them would have passed at the ceiling. The largest sum among them is
13,800,000 against a ceiling of 18,000,000.

The figure quoted when #359 was filed was 94.5 per cent, which is the same 2,251
over the whole capture of 2,381, pooling peer A with two peers this node owes
nothing and has never refreshed. That figure is not withdrawn and is correct as
a whole-capture statistic; what `gate-terms-measured.md:267` withdraws is the
**framing**, reporting a three-peer capture as one relationship. Since every one
of the other two peers' 59 refusals is at the ceiling, pooling can only dilute
the figure, so the per-peer denominator is used here throughout.

Two nearby numbers in that document are different populations and should not be
matched against 96.9: `gate-terms-measured.md:123` gives 3.06 per cent at the
ceiling, which is peer A overall (71 of 2,322), and 96.83 per cent at the floor,
which is the dense arm alone (2,169 of 2,240).

## Hypothesis

**Not** that removing the cliff delivers more bytes. That is the open question,
and the measurement is explicit that it cannot answer it: the sum sits against
whatever limit is in force, so raising the limit may simply let the sum rise to
meet it.

The hypothesis is narrower and testable, and the mechanism it has to move is
already isolated in [truncation-cause.md](truncation-cause.md): a download stops
because one chunk's refusals **exhaust the readmit bound**. The #324 path keeps a
refused provider for up to `maxOverdraftReadmits`, which is 8
(`pkg/retrieval/retrieval.go:159`); on the refusal after that,
`candidates = candidates[1:]` (`:299`) drops the only holder, the chunk spends
its error budget on peers that never had it, and `joiner.ReadAt` is all or
nothing (`joiner.go:215-223`), so the whole read unit fails with it.

That gives a graded observable this spec uses instead of a refusal count:
**overdrafts not readmitted**, the difference between `preferred_overdrafts`
(`preferred.go:232`) and `preferred_readmits` (`retrieval.go:295`). It was
**4 in each of two truncating runs**, matching the number of failed read units
exactly (`truncation-cause.md:36-46`), and **0 in the one run that completed**,
where 438 of 438 refusals were readmitted (`retrieval-rate.md:74-78`).

**So the hypothesis is: continuous accrual lowers the refusals landing on any
one chunk below the readmit bound, so overdrafts not readmitted goes to zero and
the read unit survives.** Four outcomes are possible and all are reportable:

1. overdrafts not readmitted falls to zero and the file completes, so the cliff
   was costing throughput;
2. it falls, the truncation point moves later, and the file still does not
   complete, so the cliff was part of the cause and not all of it;
3. it does not fall although floor refusals do, so the sum re-equilibrated
   against the higher limit and the change achieves nothing;
4. floor refusals do not fall, so the model in `gate-terms-measured.md` is wrong.

Outcome 3 is the one this spec expects to have to report. Outcome 2 is a partial
result that an earlier draft of this spec would have rejected as a failure, which
is why acceptance below is graded rather than a single pass or fail.

## The constraint that shapes the design

**The step is not an accident. It holds this node inside a margin a stock peer
already permits, and one side accruing continuously can spend that margin.**

This node's spending limit is `paymentThreshold + refreshDue` (`:359`). The peer
deciding whether to keep serving us uses `disconnectLimit + refreshDue`
(`:1425`, enforced at `:1427`), where
`disconnectLimit = percentOf(100 + paymentTolerance, paymentThresholdForPeer)`
and `payment-tolerance-percent` defaults to 25 (`cmd/bee/cmd/cmd.go:390`). That
limit is assigned in three places which an implementation must keep consistent:
on connect from the node-wide value (`:1525`, from `:243` or `:257` for a light
peer), on a threshold upgrade (`:741`), and on a provider grant
(`pkg/accounting/provider.go:161`).

### The two sides do not step at the same instant

An earlier draft of this document said both sides compute the elapsed term "with
the same integer step". That is wrong, and the correction matters because the
whole design rests on the margin between them.

- **Our side** (`:356`) measures milliseconds on our own clock, from
  `refreshTimestampMilliseconds`, which we stamp when we read the payment
  acknowledgement (`pkg/settlement/pseudosettle/pseudosettle.go:314`). It steps
  at exactly 1,000 ms of real elapsed time.
- **The peer's side** (`:1416`) measures **whole Unix seconds** on its own clock,
  from `refreshReceivedTimestamp`, which it stamps when it computes the allowance
  (`pseudosettle.go:155`), before the acknowledgement is sent. A difference of
  two whole-second counts becomes 1 the moment the wall clock crosses the next
  integer second, which is anywhere in `(0, 1]` of real elapsed time.

So the peer's step fires **at or before** ours, never after. The margin between
the two limits is therefore 3,375,000 in the worst case and wider the rest of
the time, rather than a constant 3,375,000 at every instant. The error was in
our favour: the worst case is the one the design must survive, and it is the one
the old table described.

With a 13,500,000 threshold, and taking that worst case throughout:

| elapsed | we allow ourselves | a stock peer tolerates, worst case | margin |
|---|---|---|---|
| under 1 s | 13,500,000 | 16,875,000 | 3,375,000 |
| 1 s or more | 18,000,000 | 21,375,000 | 3,375,000 |

The worst-case margin is exactly `payment-tolerance-percent` of the threshold.

### Accruing continuously on our side alone spends that margin

| elapsed | we would allow | peer tolerates, worst case | |
|---|---|---|---|
| 0.50 s | 15,750,000 | 16,875,000 | within |
| 0.75 s | 16,875,000 | 16,875,000 | **already blocklists** |
| 0.90 s | 17,550,000 | 16,875,000 | over by 675,000 |
| 0.99 s | 17,955,000 | 16,875,000 | over by 1,080,000 |

The 0.75 s row is a blocklist, not a boundary: the peer's check is
`nextBalance.Cmp(disconnectLimit) >= 0` (`:1427`), greater than **or equal**,
while ours is `> 0` (`:363`). We admit a debt equal to our limit; the peer
disconnects at a balance equal to its own. So the unsafe region is **at and
after 0.750 of a second**, not the last 250 ms.

Crossing it blocklists us (`:1427-1435`). That is not a wire change, but it is a
behaviour change visible to an unmodified peer, and it is the kind of thing rule
6 exists for.

### What the tables do and do not compare

The two columns are **limits, not measured quantities**. The number our gate
compares is the sum defined above, which includes reserved and surplus balances;
the number the peer compares is `nextBalance`, only what it has actually
debited. Ours is systematically the larger, so reaching our own limit does not
by itself put the peer's number at its limit. The real risk is therefore lower
than the table implies and the cap below is conservative. It is written this way
deliberately: the safe bound is the one that holds when the two numbers coincide.

### The crossover is a property of the threshold, not a constant

0.750 is `tolerance headroom / refreshRate` at the shipped 13,500,000. At the
minimum accepted threshold of 9,000,000 (`minPaymentThreshold = 2 * refreshRate`,
`pkg/node/node.go:244`) it is 0.500. And the headroom is
`0.25 x threshold`, so it reaches a full `refreshRate` at a threshold of
18,000,000: **at or above that the cap can never bind and continuous accrual is
already safe unaided.**

The threshold that matters here is `accountingPeer.paymentThreshold`, the one the
**peer announced to us**, which is what `:359` and the cap both read. It grows on
the **provider's** node, by that node's own copy of `:739`, as **we** repay it.
(`:739` on our node raises what we announce to a peer that has repaid us, which
is the mirror of this and not the term in play.) `docs/bandwidth-incentives.md:222`
records an announced threshold reaching 94,500,000, so a mature relationship sits
in the region where the cap does nothing and a fresh one sits in the region where
it does. The measurement must say which regime each arm ran in.

## Design

**Accrue continuously inside the first second, capped at what a stock peer
tolerates. Leave the behaviour at and after one second exactly as it is.**

```go
elapsedMillis := a.timeNow().UnixMilli() - accountingPeer.refreshTimestampMilliseconds
if elapsedMillis < 0 {
        elapsedMillis = 0          // clock stepped back a second or more
}

var refreshDue *big.Int
if a.accrual != accrualContinuous || elapsedMillis >= 1000 {
        // The step arm, and the continuous arm at or after one second:
        // a full allowance, uncapped, exactly as today.
        refreshDue = new(big.Int).Set(a.refreshRate)
        if elapsedMillis < 1000 {
                refreshDue.SetInt64(0)
        }
} else {
        refreshDue = new(big.Int).Mul(a.refreshRate, big.NewInt(elapsedMillis))
        refreshDue.Div(refreshDue, big.NewInt(1000))

        // Never accrue past what a peer running stock Bee tolerates. Inside the
        // first second we must assume its own step has not fired, so the only
        // headroom we can rely on is its tolerance. Strictly below, because its
        // check disconnects at equality where ours admits it. Never negative:
        // a peer tolerance of zero is legal and would otherwise take the limit
        // BELOW the announced threshold, which is worse than today.
        if cap := a.safeAccrualCap(accountingPeer); cap.Sign() > 0 && refreshDue.Cmp(cap) > 0 {
                refreshDue.Set(cap)
        } else if cap.Sign() <= 0 {
                refreshDue.SetInt64(0)
        }
}
```

`safeAccrualCap` is `percentOf(a.paymentTolerance, accountingPeer.paymentThreshold)`
minus one unit: **our own** tolerance applied to the threshold **the peer
announced**. The peer announces a threshold and nothing else; its tolerance is
not on the wire, so this uses ours as a proxy. **That is the weakest point in
the design and the measurement checks it rather than assuming it**, see below.

At the shipped defaults the cap is 3,374,999 and the capped limit is
**16,874,999**, one unit below the 16,875,000 at which a stock peer's `>=`
disconnects. Stating it as 16,875,000, which an earlier draft did twice, asserts
the exact value the minus one exists to avoid.

**Why the cap must not apply at and after one second.** It would otherwise hold
the limit at 16,874,999 forever instead of letting it reach 18,000,000, making
the node permanently worse. That is not hypothetical: `gate-terms-measured.md`
records peers B and C, 59 refusals, **every one at the ceiling with
`refresh_timestamp_ms` at zero**. A zero timestamp makes the elapsed term
enormous, so those peers sit at the cap permanently, and an unconditional cap
would lower their limit by 1,125,001 and refuse more, not less. A first draft of
this design had exactly that defect.

**Why the cap must never go negative.** `payment-tolerance-percent: 0` is legal
(`pkg/node/node.go:814-815` rejects only negative values). With it the cap is
`0 - 1`, and since `refreshDue` starts at zero the comparison would set it to
minus one and put the limit one unit **below** the announced threshold for the
whole sub-second window. That is the only reachable input on which the new code
would be worse than the current code, and the sign check above is what removes
it.

Note also why a zero timestamp arises: `:1176` writes
`refreshTimestampMilliseconds` above every check, and every error path in
`pseudosettle` passes `timestamp = 0`, so a **failed** refreshment leaves the
field at zero. The branch above treats that case as one second or more elapsed,
which is what the current code already does.

**Light peers.** The credit path has no light-node branch, unlike the debit path
(`:1419-1422`). It does not need one: `a.refreshRate` is already the rate
enforced for this node's type (`pkg/node/node.go:1224-1229`), and the cap scales
with the threshold the peer announced, which a light peer sets lower. The
asymmetry is called out here because it is the first thing an implementer will
suspect.

**The backwards-clock clamp is new behaviour, not a restatement, and only past a
full second.** Go truncates integer division toward zero, so a backwards step of
1 to 999 ms already gives an elapsed term of zero and today's code is unaffected.
Only a step of **a second or more** makes the term negative and puts the limit
below the announced threshold. That is a defect on its own, so the clamp is
applied in **both** arms, and `step` is then a control that reproduces current
behaviour in every case except that one. The test for it must use a step of at
least 1,000 ms, or it passes against the old code as well and pins nothing.

> **Correction, added during implementation: the "only past a full second"
> claim is true of the `step` arm only, and it understates the clamp.**
>
> The continuous arm multiplies by the elapsed milliseconds **before** dividing
> by 1,000, so any negative elapsed value produces a negative allowance
> directly. Measured with the clamp removed: a backwards step of **1 ms** gives
> -4,500, 10 ms gives -45,000, and 999 ms gives -4,495,500. The 1,000 ms floor
> comes from integer division truncating toward zero, which only the step arm
> relies on.
>
> That matters for which failure is reachable rather than only for the test.
> Small backwards corrections, the ordinary result of an NTP adjustment, are
> common; full-second steps are not. So the case this paragraph described as
> harmless is the one the continuous arm is actually exposed to.
>
> The test requirement therefore splits: the **step** arm must use at least
> 1,000 ms or it pins nothing, and the **continuous** arm should use 1 ms,
> because that is both the tighter assertion and the realistic input. A further
> consequence found at the same time: in the step arm the zero comes from the
> sub-second branch rather than from the clamp, so a step-only test can never
> fail for the clamp's absence at all.

### Considered and dropped

**Not resetting the timestamp when a refreshment credits less than the allowance
its reset costs.** The measurement shows the sum falling by exactly `refreshRate`
at all three observed step-downs, so no shortfall was observed and the case this
would protect against did not occur. Recorded because it is not refuted, only
unmotivated.

**Changing both sides symmetrically.** That removes the asymmetry entirely and is
the clean fix, but it only helps between two wasp nodes, and a wasp node talking
to stock Bee is the normal case. It would also need the mixed-version test from
[mixed-version.md](mixed-version.md) rerun. Out of scope here, and worth its own
issue if the capped form measures well.

## Configuration

`refresh-allowance-accrual`, a string, default **`step`**, which is current
behaviour. The other value is `continuous`.

- **Setting it to `continuous`** costs this node nothing directly and may cost it
  a blocklisting if the safe cap is wrong for a given peer, since exceeding a
  peer's disconnect limit is what triggers one. It costs **other nodes** a larger
  unsecured debt outstanding inside the first second of each refresh cycle, up to
  the tolerance they already permit. It does not let this node owe more than the
  ceiling it can already reach today.
- **Leaving it at `step`** costs the 96.9 per cent of this peer's refusals
  measured at the floor, if those refusals turn out to cost bytes. Whether they
  do is what this experiment measures.

Per rule 8 the default is the current value, so a node that does not set it
behaves as it does today.

**The option ships on outcome 1 or 2, and not otherwise.** Rule 8 says a dial
that turns out not to matter is worse than no dial. Outcome 1 or 2 means the
readmit exhaustion this targets actually moved. On outcome 3 or 4 the change is
reverted and the measurement is kept as the record of why, rather than leaving a
permanent setting that moves a counter and nothing else.

## What this risks

- **Blocklisting by a peer running a lower tolerance than ours.**
  `payment-tolerance-percent: 0` is legal; `pkg/node/node.go:814-815` rejects
  only negative values. Against such a peer its disconnect limit equals the
  threshold it announced and **any** accrual is unsafe. The shipped default on
  both sides is what makes the proxy work, and that is an assumption, not a
  bound. This is the reason blocklisting is a reject condition below.
- **Being measured in the wrong regime.** A fresh peer pair exercises the capped
  regime, any mature relationship the uncapped one. See the crossover section.
- **Measuring an arm with no credit pressure in it.** At lookahead 0 the
  unmodified node completed **3 of 3 with zero overdrafts**
  (`retrieval-rate.md:41-46`). There is nothing there for this change to relieve,
  so that arm can only show it does no harm. Treating it as a treatment arm would
  manufacture a null result.
- **Comparing against a baseline taken under different node state.**
  `gate-terms-measured.md:44-45` did not reset balances between runs; this spec
  does. Its delivered-byte figures are therefore not a control for these arms and
  are not used as one.

## Protocol impact

**No frozen surface is touched.** `.github/protocol-freeze.lock` fingerprints
protocol names and versions, handshake protobuf fields, chain and network IDs,
and `pkg/swarm` constants; none is read or written here. No message type changes
and no field is added to any protobuf. `pkg/accounting` is not among the
directories rule 6 names. `make protocol-freeze` is unaffected and no
`protocol-change` label is required.

**But it is peer-visible behaviour**, and that is stated here rather than hidden
behind the absence of a wire change. A node with this on can hold more
outstanding debt inside the first second of a refresh cycle than stock Bee would.
The cap is what keeps that inside the peer's existing tolerance.

## Measurement

Requester and provider on the bench, sole-source content of 4,194,304 bytes,
provider grant asserted zero, balances reset between runs. Arms: `step`
(control) against `continuous`, at two lookahead sizes, **three runs each**, for
a budget of **12 runs**, interleaved so node state cannot separate the arms.

**The two lookahead sizes do different jobs, and only one is a treatment arm.**

- **262,144**, the shipped default for this file size (`pkg/api/bzz.go:51`), is
  **the arm under test**. It is where the credit pressure is: the unmodified
  node completed 1 of 3 there with 609, 240 and 438 refusals per run
  (`retrieval-rate.md:41-46,70-78`).
- **0** is a **no-harm check, not a treatment arm**. The unmodified node
  completed 3 of 3 there with **zero overdrafts**, so there is no refusal for
  this change to remove and the only thing it can show is that nothing regresses.

`gate-terms-measured.md` used 0 and 524,288, and neither was the default for its
file, a mislabelling that had already reached one earlier document. The 524,288
arm is not reused here. Nor are that document's delivered-byte figures used as a
baseline at all, because it did not reset balances between runs and this spec
does, so the two are not the same condition.

Recorded per run, with the spread reported **per condition** and not only for
the control:

- **`preferred_overdrafts` minus `preferred_readmits`, the overdrafts not
  readmitted. This is the primary observable**, because it is the quantity the
  mechanism in `truncation-cause.md` turns on, and it was exactly 4 in each of
  two truncating runs and 0 in the one that completed;
- delivered bytes, `curl` exit code, and the SHA-256 of the body. `curl` exit 18
  means truncation, which is informative here rather than noise: it says a read
  unit failed, and `truncation-cause.md` establishes why;
- `preferred_misses`, which rose by only 1 or 2 per download in every run so far,
  so a larger rise means something other than this change is at work;
- refusals from #353's log line, with `refresh_due`, so the split between floor
  and ceiling can be compared **within peer A** against its own 96.9 per cent;
- **the provider's `/blocklist` before and after**, naming the requester. That
  is the deciding evidence for the safety gate, because
  `bee_accounting_disconnects_overdraw_count`
  (`AccountingDisconnectsOverdrawCount`, `:1429`) is an unlabelled counter
  (`pkg/accounting/metrics.go:23`) that any peer on the provider can move.
  Record the counter too, as corroboration.

  Two related counters, since an earlier draft of this spec got this wrong in
  both directions. `AccountingDisconnectsOverdrawCount` is the **specific**
  counter for this hazard. `AccountingDisconnectsReconnectCount` (`:1615`) then
  rises **as a consequence**, not instead: the blocklist returns
  `p2p.NewBlockPeerError` (`:1431-1435`), libp2p's `Blocklist` disconnects the
  peer (`pkg/p2p/libp2p/libp2p.go:1017`), `pseudosettle` is registered for that
  disconnect and calls back into accounting (`pseudosettle.go:118-125`), and the
  peer is still marked connected at `:1608`. Its help text says "early attempt to
  reconnect" (`metrics.go:107`), which is upstream's wording and describes only
  one of the ways it moves. So a rise in it corroborates and does not
  contradict. `AccountingDisconnectsGhostOverdrawCount` (`:1455`) and
  `AccountingDisconnectsEnforceRefreshCount` (`:1193`) are different events;
- the provider's `thresholdGiven` and `currentThresholdGiven`, to check the
  tolerance the cap assumes. `currentThresholdGiven` is
  `disconnectLimit + refreshDue` (`:829`), so the tolerance is recoverable as
  `100 * (currentThresholdGiven / thresholdGiven - 1)` **only while the
  provider's own `refreshDue` is zero**. Sample repeatedly across a run and take
  the minimum; a single sample at an arbitrary moment returns a wrong tolerance.
  **The minimum can still be wrong**, because the provider's term at `:820` has
  no zero clamp either, so a peer whose `refreshReceivedTimestamp` is zero sits
  permanently at a full `refreshRate` and no sample ever reaches zero. That state
  is real on this bench, which is what peers B and C are. So discard the
  estimate unless a refreshment from the requester is on record, and since both
  nodes here are ours, read the provider's configured tolerance directly and use
  the ratio only as a cross-check;
- which regime each arm ran in, from `thresholdGiven`: capped below 18,000,000,
  uncapped at or above it.

**A sanity check to run before the arms.** The capped limit at the shipped
threshold is 16,874,999 and the largest sum among peer A's floor refusals is
13,800,000. The cap must clear that measured maximum, or the design cannot move
those refusals at all and there is nothing to measure.

**What a negative result looks like.** Floor refusals fall substantially,
overdrafts not readmitted stays at its baseline of about 4 per run, and delivered
bytes stay inside the spread of the `step` arm. That is outcome 3: the sum
re-equilibrated against the higher limit. It closes #359 as a measured negative
rather than a failure, and it is the outcome this spec expects.

## Acceptance

All of this is at the shipped lookahead default of 262,144, the arm under test.
The lookahead-0 arm decides nothing; it only has to not regress.

**The safety gate comes first and overrides everything below.** If the provider's
`/blocklist` names the requester in any run, the change is **rejected** whatever
else it did. That is the hazard the cap exists to prevent, and a change that
delivers the whole file by getting itself blocklisted has failed.

Then, on the primary observable, **overdrafts not readmitted**, whose baseline
is about 4 per truncating run:

| Result | Bucket | Disposition |
|---|---|---|
| goes to 0 and the file completes with a matching SHA-256 | outcome 1 | accept and ship |
| falls materially and the truncation point moves later, but no complete file | outcome 2 | accept as a partial result and ship, recording that the cliff was part of the cause and not all of it |
| does not fall, although floor refusals do | outcome 3 | reject, revert, publish as a measured negative |
| floor refusals do not fall either | outcome 4 | reject, revert; the model in `gate-terms-measured.md` is what is wrong, not the design |

**Why this is graded and not a single pass.** An earlier draft required a
complete file in 2 of 3 runs "where the `step` arm delivers a complete file in
none". Prior measurement falsifies that in advance: the unmodified node already
completes 1 of 3 at this lookahead size and 3 of 3 at lookahead 0
(`retrieval-rate.md:41-46`). A criterion the control can satisfy on its own
decides nothing, and the same draft would have thrown away outcome 2, which is a
real partial win. It also mislabelled a rise in delivered bytes without
completion as "achieves nothing", when going from 196,608 bytes to most of the
file plainly is not nothing.

Delivered bytes are reported with the spread per condition but are **not** the
gate, because a truncation point is quantised to the read unit and moves in steps
rather than smoothly.

**What invalidates a run** rather than deciding it: a provider grant other than
zero; the two arms running against different node states, which
[overdraft-terms.md](overdraft-terms.md) showed can move body bytes threefold on
its own; or a peer count that changes between arms.

## Rollout and rollback

Off by default. An operator turns it on with
`refresh-allowance-accrual: continuous` and a restart, and back off by removing
the line and restarting. Nothing persists on disk, no migration, and no peer
state depends on it.

A node that is blocklisted while it is on recovers by the normal blocklist
expiry; turning the setting back off prevents recurrence.

## Upstream portability

The gate is unmodified upstream code, verified against `upstream/v2.8.2`, where
`:356`, `:359` and `:363` appear as `:313`, `:316` and `:320`, and `:1416` and
`:1427` as `:1334` and `:1345`. `refreshRate` is unmodified at upstream
`node.go:211`. So the change applies to Bee directly and the cap keeps it
compatible with unmodified peers. #359 carries `affects-upstream` because the
behaviour is measured and reproduced rather than reasoned about, with both
readings of the defect question recorded there for a human to settle.

Related and already tagged: [#316](https://github.com/crtahlin/wasp/issues/316),
where the same quantity is computed a second way in `settle` (`:566`) **without**
the one second cap. If this lands, the two computations should be made to agree,
and that is a reason to put the elapsed term in one helper rather than two
expressions.

## Files and test plan

- `pkg/accounting/accounting.go`: the elapsed term in one helper, the safe cap
  helper, and the service field holding the setting.
- `pkg/accounting/accounting_test.go`, `package accounting_test`:
  - `TestRefreshDueStepUnchanged`, that `step` reproduces the current two values
    at 0, 999 and 1,000 ms;
  - `TestRefreshDueContinuousAccrual`, the value at several elapsed points
    inside the first second;
  - `TestRefreshDueCapBindsBelowTolerance`, that the cap binds from 0.750 s at
    the shipped threshold and from 0.500 s at 9,000,000, and that the capped
    value is strictly below the peer's disconnect limit;
  - `TestRefreshDueZeroToleranceNeverLowersTheLimit`, the one input on which the
    naive form is worse than the current code;
  - `TestRefreshDueCapDoesNotBindAtOrAfterOneSecond`, that the limit still
    reaches 18,000,000, and that a zero `refreshTimestampMilliseconds` takes
    that branch;
  - `TestRefreshDueCapAbsentAboveEighteenMillion`, that the cap never binds at a
    grown threshold;
  - `TestRefreshDueBackwardsClockClamped`, in both arms, with a step of at
    least 1,000 ms; a smaller step passes against the current code too and
    pins nothing.
- `cmd/bee/cmd/cmd.go` and `pkg/node/node.go`: the setting.
- `docs/DIFFERENCES.md`: a row, since this changes what a node does.
- `docs/experiments/content-providers/`: the results document.

Generated with help of AI.
