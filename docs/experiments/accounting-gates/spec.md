# Accounting gates: how fast debt clears with one peer

Issues: [#303](https://github.com/crtahlin/wasp/issues/303) (more than one
payment in flight per peer) and
[#304](https://github.com/crtahlin/wasp/issues/304) (each cheque clears only a
slice of the debt). One spec, because they are the same code path, the same
measurement and the same risk. **Two commits** when they land, for the reason
under "Land them separately".

## Terms

- **Credit window.** What a requester may owe one peer before it must stop:
  that peer's payment threshold plus at most one second of refresh. At shipped
  defaults, 13,500,000 + 4,500,000 = 18,000,000 accounting units
  (`cmd/bee/cmd/cmd.go:385`, `pkg/node/node.go:232`). A peer's announced
  threshold rises over a connection's life
  (`notifyPaymentThresholdUpgrade`, `accounting.go:657`), so this is the value
  early on, not for ever.
- **Early payment threshold.** What actually triggers settlement:
  `percentOf(100 - payment-early-percent, paymentThreshold)`
  (`accounting.go:619`, `:999`). At shipped defaults, 50% of 13,500,000 =
  **6,750,000**. Settlement starts when the debt, reduced by what the other side
  may already have credited, reaches this. Three sites test it.
  `PrepareCredit` at `:300` is the first and by far the most frequent, running
  once before every chunk request, and it compares with `>=`. The two that run
  after a credit completes, `:375` and `:418`, compare with `>`.
- **Refreshment.** The free, time-based allowance, at most 4,500,000 units a
  second, once a second.

## Problem

Two rules in `pkg/accounting/accounting.go` govern how fast debt clears with one
peer.

**One payment at a time (#303).** `settle` starts a payment only when
`paymentOngoing` is false, and clears the flag when the payment is reported sent
(`:468-510`, `:955`).

**Each cheque pays the debt minus the allowance about to arrive (#304).** The
amount is the debt, minus what refreshment is about to cover, minus what has
already been promised (`:484-497`).

**Where the figures come from, and how far they carry.** One diagnostic download
during [#290](https://github.com/crtahlin/wasp/issues/290), with three loggers
at debug: nine cheques in 10.6 s, one about every 1.2 s, about 6,400,000 units
each, roughly 21 chunks, about 0.3 s from a cheque being sent to the payment
being registered, and nine free allowance refreshes, one a second
([results](../content-providers/results.md), "How fast the credit window
reopens"). **That is one run, and the results file says its timing is not
comparable with the tables beside it because the logging itself slowed the
download.** Under rule 7 it is a diagnosis, not a measurement. Every figure
below is derived from it and inherits that weakness, which is the first reason
the conditions in the Measurement section start by reproducing it.

**The arithmetic that matters, and that the issues did not do.** Settlement
triggers at the early payment threshold, 6,750,000, not at the 18,000,000 credit
window. The measured cheque of 6,400,000 is **95% of the trigger**, so there is
no gap of 11,600,000 for #304 to close.

**The term #304 removes was zero in every measured cheque.** `refreshDue` is
`(elapsed_ms / 1000) * refreshRate` with integer division and, uniquely at this
site, no cap (`:484-485`). It can therefore only be 0, 4,500,000, 9,000,000 and
so on. `refreshTimestampMilliseconds` is set only when a refreshment completes
(`:1094`), and the run recorded a refreshment every second, so a full second
without one never elapsed. Had the term been 4,500,000, the subtraction at
`:492` would have left a cheque of about 1,900,000 rather than 6,400,000. **So
#304 would not have changed a single one of the nine measured cheques.**

**What #304 would change, in the regime where the term is not zero.** Two
different things, and the issue names only the first:

- **A larger cheque.** At a debt of 6,400,000 with the term at 4,500,000, the
  cheque is 1,900,000; removing the subtraction makes it 6,400,000. So the
  issue's claim is right, but only once a full second has passed with no
  completed refreshment, which the measured run never reached.
- **A settlement that happens at all, in a narrow band.** `minimumPayment` is
  `refreshRate / 5` = 900,000 (`:42`, `:233`). The check at `:500` fails when
  the debt less the term falls below it, and `settle` is only entered at all
  when the shadow balance reaches one refresh rate (`:458`). So the band in
  which a settlement is suppressed outright is a debt between 4,500,000 and
  5,400,000. Above that band a smaller cheque still leaves; below it `:458`
  stops `settle` outright, with or without #304. Both figures assume the shadow
  reserve is zero and that all the debt is originated, which is what the
  measured run looked like.

Only `:500` can suppress. The earlier check at `:483` tests the originated debt
one line before `refreshDue` is computed at `:484`, so the term cannot reach it.

**Neither gate explains the measured spacing, and this is the finding that
matters most.** The 1.2 s is the time to rebuild the tested quantity to the
trigger, and rebuilding it means outrunning refreshment as well as reaching
6,750,000. `NotifyRefreshmentSent` adds the refreshed amount to the balance
(`:1155-1157`) and never touches `shadowReservedBalance`, so refreshment reduces
exactly the quantity the trigger tests. At about 4,500,000 a second that is
roughly 5,400,000 cleared for free inside each 1.2 s cycle, so gross credit with
the peer must be about 6,750,000 + 5,400,000 = 12,150,000 a cycle, **roughly 40
chunks at about 305,000 units each**, not the 22 that 6,750,000 alone would
suggest. No chunk count for that download was recorded, so this is derived from
the accounting figures rather than confirmed against delivery.

- **`paymentOngoing` does not delay the next cheque.** `settle` adds the payment
  to `shadowReservedBalance` before starting it (`:501-503`), and the trigger
  tests the debt *less* that reserve (`:294-300`). When `NotifyPaymentSent`
  lands it subtracts the amount from the shadow reserve (`:957`) and adds it to
  the balance (`:975-979`), so the quantity the trigger tests is the same before
  and after the report. The next cheque needs new debt either way. The report
  took about 0.3 s against a 1.2 s spacing, so it was never the constraint.
- **`refreshDue` does not delay it either**, because it was zero throughout, as
  above.

**So both gates are predicted to do nothing at the rate measured**, and the spec
is written to find the regime in which each would bind rather than to confirm a
gain. #303 binds only where debt rebuilds faster than a payment is reported,
which at these figures needs about four times the rate at which the tested
quantity climbs, that is net of refreshment, and about 2.7 times the gross rate
of credit with the peer. The two differ because refreshment clears a fixed
4,500,000 a second however fast traffic arrives. #304
binds only where a full second passes with no completed refreshment.

**The dominant lever already exists.** `payment-early-percent` sets the trigger
directly (`cmd/bee/cmd/cmd.go:387`, default 50). Lowering it to 10 puts the
trigger at 12,150,000, nearly doubling the cheque, with no code at all. That it
equals the gross credit per cycle derived above is a coincidence of the
defaults, not a relation. Any case
for #304 has to be made against that dial, not against the status quo.

## What has changed since these issues were written

Both issues argued from a cheque costing about 0.3 s in chain calls on the
receiving side. [#301 and #302](../cheque-acceptance-cost/results.md) have since
removed most of that cost, which is why this spec's framing is narrow.

- **About 93% of the chain reads the node used to make are gone**, 87% to 95%
  per run, 153 avoided against 12 made across six runs. That is of the reads the
  node made, not per cheque: on the steady path a cheque costs none, against two
  every 30 seconds.
- **On a fast endpoint the accept time did not change measurably.** On an
  endpoint deliberately slowed by 0.5 s per chequebook call it fell about 42%,
  1.788 s against 1.045 s, with n=12 against n=9 across 5 and 4 runs. **So how
  much either change is worth depends on what a chain call costs at the time**,
  which is why this spec asks for that figure beside every result.
- **The per-cheque floor is the prior number that matters most for #303.** A
  stock receiver never accepted a cheque in under 0.184 s across 39 cheques; a
  patched one reached 0.031 s across 56. That floor is the serialised cost at
  the receiver, and it is what bounds a pipeline.

## Hypothesis

**The headline prediction is that both changes do nothing at the rate
measured.** That is derived in the Problem section, not assumed, and it is what
the conditions below are built to test. Each hypothesis is therefore stated as
the regime in which the change would bind, so that a null result says something
rather than nothing.

1. **#303 binds only where debt rebuilds faster than a payment is reported.**
   Removing `paymentOngoing` cannot make clearing unbounded, because the trigger
   already subtracts `shadowReservedBalance`, which the in-flight payment raises
   (`:501-503`, `:294-300`), and the report takes that amount off the reserve
   (`:957`) and adds it to the balance (`:975-979`), reducing the debt by the
   same amount and so leaving the tested quantity unchanged. So a second cheque
   needs one early payment threshold of **new** debt whether or not the flag
   exists. The flag can only bind when that new debt arrives before the report
   does. Measured: 1.2 s to rebuild against about 0.3 s to report, so it did not
   bind. Starting to bind needs about four times the rate at which the tested
   quantity climbs, net of refreshment, which is about 2.7 times the gross rate
   of credit with the peer, since refreshment clears a fixed 4,500,000 a second
   however fast traffic arrives. **Predicted effect on this bench: none.** Note
   that the regime where #303 starts to bind is also where the overdraft block
   must start firing, since by the condition 3 arithmetic below there are only
   22 to 37 chunks of headroom at the default. So a run that shows #303 helping
   should show blocks rising too, and Acceptance treats that as cost moved
   rather than removed.
2. **#304 binds only where a full second passes with no completed refreshment.**
   The term is zero otherwise, and it was zero in all nine measured cheques, so
   #304 would have changed none of them. Where the term is 4,500,000 it makes
   the cheque larger, and in the narrow band of debt between 4,500,000 and
   5,400,000 it converts a suppressed settlement into a cheque. **Predicted
   effect on this bench: none**, unless the load is shaped to starve the
   refreshment.
3. **Underlying both:** that the accounting gate binds at all on this bench.
   `bee_accounting_accounting_blocks_count`
   (`pkg/accounting/metrics.go:105-108`) counts every `ErrOverdraft`
   (`:320-322`). **If it does not move, neither change can help and the rest of
   the measurement is moot.** That is the first thing to record.

A null result on 1 and 2 is the expected outcome and closes both issues, so long
as it records the debt rate, the refreshment interval and the chain call cost
that applied, because those are the three quantities that decide whether the
regime was ever entered.

## Design

### 1. More than one payment in flight (#303)

Cheques are cumulative, and a receiver credits the difference between the new
cumulative payout and the last one it accepted, so it is self-correcting. That
is what makes pipelining conceivable. Three things in the current code make it
unsafe as it stands, and the implementation must address all three.

**a. `chequebook.Issue` is not safe for concurrent use per beneficiary.** It
reads the last cheque, adds the amount and writes the result with no lock over
the sequence (`pkg/settlement/swap/chequebook/chequebook.go:190-249`; `s.lock`
covers only the reserved total around `:163-178` and `:241`). Two concurrent
calls for the same beneficiary both read the same cumulative payout and both
send the same next value. The receiver then rejects one as not increasing, or
accepts the higher and credits less than the sender records through two
`NotifyPaymentSent` calls, and the persisted `lastIssuedCheque` can be left
lower than what was sent, which makes every later cheque to that peer
non-increasing and stops payment to it permanently.

**It is not reachable today, and #303 is what would make it reachable.** `Issue`
has one caller, `swap.Pay` (`swap.go:146`), reached only as `payFunction` from
`settle` (`accounting.go:509`, assigned `:1518`); there is no API path. `settle`
gates on `paymentOngoing`, so one peer has one payment in flight. Two peers
cannot share a beneficiary and so cannot get around that gate: `Handshake` calls
`MigratePeer` rather than adding a second mapping (`swap.go:252-262`), and
`MigratePeer` deletes the old peer's entry (`addressbook.go:62-93`). Removing
`paymentOngoing` creates the first concurrent path, which is why this is a
blocking design item here rather than a defect to be fixed elsewhere.

The same code is in `upstream/v2.8.2`, but upstream does not make the call
either, so it is a latent hazard and **not** an `affects-upstream` defect
(rule 11: leave untagged what is reasoned from reading the code rather than
reproduced). Its issue records what evidence would justify the tag later.

**Allocation of the cumulative payout must become atomic per beneficiary.** The
spec does not pick the mechanism, but it must be one of:

- a per-beneficiary lock held across allocate, send and persist, which
  serialises the send and so wins nothing; or
- allocate and persist the payout under a lock, then send outside it, which
  reverses the ordering the code deliberately chose at `:227` ("actually send
  the check before saving to avoid double payment") and needs its own reasoning
  about a crash between persist and send; or
- allocate under a lock into an in-memory pending value, persist on
  confirmation, and reconcile at startup.

Whichever is chosen, the implementation must state why a crash at each point
leaves the node able to pay again.

**b. The bound is `reserveTotalIssued`, not the shadow reserve.** An earlier
draft of this spec said unconfirmed promises should be bounded by the shadow
reserve. That is wrong. `shadowReservedBalance` is a correction term meaning
"the other side may already see my debt as lower", and it is raised both by
payments in flight (`:503`) and by `PrepareDebit` when this node *serves* that
peer (`:1226`), so using it would let a node pipeline more cheques merely
because it is serving that peer. The solvency bound already exists and is
concurrency-safe: `reserveTotalIssued` checks the amount against the available
balance less what is already reserved, under the chequebook's lock, and returns
`ErrOutOfFunds` (`chequebook.go:163-178`). That is the limit to use.

**c. The sender cannot know what was accepted, and must not need to.** There is
no acknowledgement in the protocol: `EmitCheque` writes the message and returns
(`pkg/settlement/swap/swapprotocol/swapprotocol.go:241-267`), the handler reads,
credits and returns nothing (`:112-158`), and the stream is closed with the
error discarded (`_ = stream.FullClose()`, `:189-195`).
`swap.Pay` reports the payment sent on a successful write (`swap.go:146-153`)
and `TotalSent` returns the last *issued* cumulative payout (`swap.go:164-183`).
An earlier draft required the sender to fall back to "the highest accepted
cumulative payout"; **that is not observable without adding a response message,
which would be a protocol change under rule 6 and is out of scope here.** The
design must therefore work with "sent" as the only thing the sender knows, and
rely on the receiver crediting the difference from its own last accepted value.

**d. Out-of-order arrival, and why it is a smaller problem than it looks.** Each
cheque takes a new stream, so reordering is possible once payments overlap.

**In value it is self-correcting.** Once (a) makes allocation atomic, payouts
are strictly increasing, and a receiver that has already accepted a higher one
has by the cumulative rule already credited everything the lower one carried. No
machinery is needed for that.

**In signalling it is nearly unreachable, by (c).** The worry would be
`NotifyPaymentSent` with an error setting `lastSettlementFailureTimestamp`
(`:960`), after which `settle` starts no payment for `failedSettlementInterval`,
ten seconds (`:43`, `:469-471`), longer than the download being measured. But a
receiver's rejection reaches the sender only as a stream reset, and by (c) the
sender discards that: `swap.Pay`'s deferred `NotifyPaymentSent` fires on a
failed *write*, not on what the receiver decided. So a reordering rejection
essentially cannot trigger the suppression.

The implementation must therefore do two things, neither of them the obvious
one. It must confirm that this still holds once several payments are in flight,
since that is the assumption everything above rests on. And if it ever makes the
sender aware of a rejection, it must distinguish reordering from real failure
before writing the failure timestamp. What it must not do is build machinery
against a path that is currently unreachable.

**e. How many may be in flight.** The spec requires a stated limit and a reason
for it, not an unbounded pipeline. A small fixed number is acceptable if
justified.

**f. Refreshment boundaries.** `settle` adds a payment to
`refreshReservedBalance` when a refreshment is ongoing (`:505-506`), and
`NotifyRefreshmentSent` computes its allowance check against that value and then
resets it to zero unconditionally (`:1120-1123`), blocklisting the peer if the
allowance falls short (`:1138-1143`). That bookkeeping is single-shot. With
several payments spanning a refreshment it is unanalysed, and it can reach a
blocklist, so the implementation must analyse it and the test plan must cover
it.

### 2. Larger cheques (#304)

**Measure the existing dial before writing any code.** Per the arithmetic in the
Problem section, `payment-early-percent` moves cheque size far more than the
`refreshDue` term does, and it ships today. The sequence is:

1. Sweep `payment-early-percent` on a stock build: 50 (default), 25, 10.
2. Only if the `refreshDue` term still matters once that dial is where it should
   be, implement #304.

If #304 is implemented, it pays the debt without subtracting the allowance that
has not yet arrived. **The cost is real:** the allowance would have cleared that
part for nothing, so the payer spends BZZ slightly earlier than it must. That
cost must be measured, not merely acknowledged: see the acceptance rule.

**A defect noticed while reading this path, reported separately as
[#316](https://github.com/crtahlin/wasp/issues/316).** Five sites compute an
elapsed time and multiply it by a refresh rate. Four cap the elapsed value at
one second, `:313`, `:738`, `:749` and `:1334`. The fifth, `:484`, does not.
Uncapped, a refreshment that has not completed for ten seconds can drive the
expected decrease negative and suppress the cheque entirely. Checked against
`upstream/v2.8.2`, the release named in `.upstream-base`: the same five sites
are at the same line numbers, with the same four-to-one split, so the asymmetry
is present and unmodified upstream. That makes it an `affects-upstream` defect
under rule 11 (a quantity computed two different ways). #304 would erase it as a
side effect rather than fix it deliberately, and the implementation must say
whether that is intended.

### Land them separately

**#303 and #304 must land as two commits, each measurable alone.** #301 and #302
were bundled into one commit, which left
[their results](../cheque-acceptance-cost/results.md) unable to say which change
did what, recorded there as a deviation. Not to be repeated.

## What this risks

- **Accounting divergence if (a) is got wrong.** The sender credits itself for
  payments the receiver never accepted. This is the reason (a) is a blocking
  design item rather than an implementation detail.
- **Settlement suppressed for ten seconds**, but only if an implementation makes
  the sender aware of a rejection and treats it as a failure. On the current
  code that path is unreachable, per (d), so this is a risk the implementation
  would introduce rather than one it inherits.
- **A blocklist** if the refreshment bookkeeping in (f) is wrong.
- **The payer spends more, sooner**, by design, for #304.
- **It raises the exposure #301 and #302 created.** Those reuse a chequebook's
  balance for 30 seconds, so clearing debt faster means more value accepted
  inside that window. [#311](https://github.com/crtahlin/wasp/issues/311) exists
  to revisit the constant. It is **not a prerequisite**: it is explicitly
  blocked on these landing, and its first step is to recompute the exposure at
  the rate these changes actually produce, which does not exist until they have
  run. It is resolved in the same pull request as whichever change is kept,
  using that rate. If the measurement gives the null result this spec expects,
  the rate does not change and neither does the exposure.
- **Probably no effect on the availability case, but the cited run cannot say
  so.** A requester abandoned a sole-source provider after about 77 chunk
  requests ([#313](https://github.com/crtahlin/wasp/issues/313)). The results
  file is explicit that the run cannot separate the abort from the credit
  window: "Both can be true, and this run cannot tell them apart", and "once is
  not measured: treat these figures as a diagnosis to be confirmed"
  ([results](../content-providers/results.md)). If the window did limit the
  provider, a faster clearing rate would change the outcome. So these changes
  must not be presented as fixing #313, and equally must not be presented as
  irrelevant to it until #313 is measured properly.
- **They do not widen the credit window**, which is what sets a provider's
  share: the payment threshold sweep moved the share in proportion to the
  threshold. They change how fast the window refills.

## Protocol impact

None. The cheque format, the protocol ID and the handshake are unchanged; this
alters only when and how much a sender pays. `make protocol-freeze` must pass
with the fingerprint unchanged.

**Explicitly out of scope:** adding an acknowledgement so a sender learns what a
receiver accepted. That is a protocol change, needs the `protocol-change` label
and its own spec section under rule 6, and this spec is designed not to require
it (see Design (c)).

Three peer-visible differences must be shown harmless against a stock v2.8.2
node rather than assumed: several cheques in flight where stock sends one, and
the two things #304 does in its binding regime per the Problem section, a cheque
larger than stock would have sent and a cheque arriving where stock would have
sent none. A stock receiver checks the signature, the beneficiary and that the
payout increases, so the first should not matter.

**The other two reach a check this spec must not skip.** A stock receiver also
rejects a cheque whose cumulative payout exceeds the chequebook's on-chain
balance less what it has already paid out, `ErrBouncingCheque`
(`pkg/settlement/swap/chequebook/chequestore.go:211-217`), and one whose value
after deduction is under a single accounting credit, `ErrChequeValueTooLow`
(`:184-190`). The liquidity check is exactly what a larger or earlier cheque
interacts with, and it is the same quantity
[#311](https://github.com/crtahlin/wasp/issues/311) is about. The mixed-version
test must cover it rather than the increasing-payout check alone.

## Measurement

On the bench, following [test-bench.md](../../agent-playbooks/test-bench.md) and
the #290 harness: provider and requester on separate machines, about 30 ms added
between them, fresh 16 MiB files without erasure coding.

**This method is written before any runs, and each rule below exists because the
[cheque acceptance experiment](../cheque-acceptance-cost/results.md) (#301,
#302) got it wrong first.**

- **Does the gate bind at all?** Record `bee_accounting_accounting_blocks_count`
  before and after every run. If it does not move, stop: neither change can
  help.
- **Interleave, never block.** Alternate arms within each cycle. Blocked arms
  reported the patched build at 0.662 s against 0.252 s for stock, an apparent
  regression of about 0.41 s; repeating the patched block gave 0.424 s,
  straddling stock, so it did not reproduce.
- **Identical treatment per arm.** Same restart, same minimum warm-up, same
  minimum peer count. Arms there were compared across warm-ups of 60 to 300 s.
- **Measure a chain call from the node's own counters**, not a separate probe. A
  probe that opened a connection per call reported 0.10 s, while the floors give
  a lower bound of about 0.05 s. The results file says the usual cost lies
  between the two and that its data does not say where, so "roughly twofold" is
  the widest reading of that gap and not a measured ratio.
- **Write the distinguishing counters into the results file**, not merely
  collect them: `/settlements/{peer}` and `/timesettlements` before and after,
  the exchange rate in force, preferred attempts, hits and misses, retrieval
  request count, chunks delivered by the provider, the accounting balance with
  the provider, and cheque count and spacing from the debug log. The #290
  harness records none of the settlement figures today and must be extended
  first. The content B failure was diagnosed wrongly because collected counters
  were never written down.
- **State the sample size beside every figure**, including runs that produced
  one cheque.
- At least three runs per condition, reported with the spread (rule 7).

**Conditions**, interleaved:

1. **Stock**, `payment-early-percent` at its default of 50.
2. **Stock at `payment-early-percent` 25**, and **3. at 10.** No code: this is
   the dial that moves cheque size most, and it must be measured before #304 is
   justified. **Predicted, and falsifiable:** these conditions should *raise*
   `accounting_blocks_count`, not lower it. The overdraft limit is
   `paymentThreshold + refreshDue` with the cap, so 13,500,000 or 18,000,000
   (`:313-316`). At the default the trigger sits 6,750,000 or 11,250,000 below
   it; at 10 the trigger is 12,150,000, leaving only 1,350,000 or 5,850,000. At
   the rate measured in #290, about 305,000 units a chunk (6,400,000 for roughly
   21 chunks), that is four to nineteen chunks of headroom rather than
   twenty-two to thirty-seven. Bigger cheques are bought with a narrower margin
   before requests block. The two comparisons are not on identical quantities,
   since the trigger subtracts `shadowReservedBalance` and the block does not,
   so the figures hold when that term is zero. If blocks do not rise, this
   reasoning is wrong and the rest of the arithmetic here should be re-checked.
4. **#303 alone.**
5. **#304 alone**, only if conditions 2 and 3 leave a case for it.
6. **Both**, on the same condition.

**Which conditions run on both endpoints.** Conditions 1, 4, 5 and 6 run **on a
fast endpoint and on a deliberately slowed one**, using the delaying endpoint
built for the cheque experiment: they are the code comparison, and a null result
on a fast endpoint says nothing about a slow one. Treating the two as the same
is the error that made the earlier acceptance rule unusable. Conditions 2 and 3
run on the fast endpoint only. Their purpose is to locate the dial rather than
to judge a code change, and the dial acts on the sending side while the endpoint
cost falls on the receiving side. If the sweep shows the dial matters, it is
repeated on the slowed endpoint before #304 is decided.

At three runs each the planned set is 4 x 2 x 3 = 24 runs plus 2 x 3 = 6, so 30,
against the 36 a full cross would need. Two things move that number and neither
is a contradiction of it: conditions 5 and 6 may not run at all, which takes it
down to as few as 18, and the sweep repeated on the slowed endpoint would add 6,
which takes it to 36. So 30 is the plan, 18 the floor and 36 the ceiling.

**Primary figure: total debt cleared per second with the provider, with the
split between cheques and refreshment shown.** Not settlement totals alone: #304
does not clear more debt, it moves clearing from the free allowance into paid
cheques, so a figure taken from cheques alone rises by substitution with nothing
gained.

**Secondary:** the provider's share of a download, cheque count and spacing, and
total download time.

**Not measured here:** anything node-wide across many peers. That needs the load
harness in [#312](https://github.com/crtahlin/wasp/issues/312). A criterion that
cannot be evaluated on this bench must not be written into a spec, which is what
made the earlier rule inapplicable.

## Acceptance

**Accept** a change if total debt cleared per second with one peer rises beyond
the spread, with the chain call cost at the time stated alongside, **and** BZZ
spent per MiB retrieved rises by no more than 10% over condition 1 measured in
the same interleaved session.

The denominator is condition 1, not any figure carried over from #290. The split
between cheques and refreshment was never recorded there and the harness does
not record it today, which is why the Measurement section requires extending it
first. **The 10% is a chosen bound, not a derived one.** It exists because #304
converts free allowance into paid cheques by construction, so "and nothing
measurably worse" would otherwise be unfalsifiable for it, and it is set where
it is because a bound narrower than condition 1's own spread could not be
applied. If that spread turns out wider than 10%, the bound becomes the spread
and the results say so.

**`accounting_blocks_count` is an acceptance input for both changes, not only a
check before starting.** Report it per condition beside the primary figure. If
it did not move in condition 1, neither change is accepted on this bench
whatever else the figures show, because the gate they act on never bound. A
change that raises the clearing rate while also raising blocks has moved the
cost rather than removed it, and is reported that way rather than accepted on
the rate alone.

**A negative result** is no rise beyond the spread. The change is then reverted
rather than kept, and the result is recorded together with the chain call cost
that applied, because a null result on a fast endpoint does not establish one on
a slow endpoint.

**Expect no effect at all**, for both changes, by the reasoning in the
Hypothesis: neither gate was what set the measured spacing. A null result is
therefore the predicted outcome rather than a disappointment, and it closes both
issues provided it records the debt rate, the refreshment interval and the chain
call cost that applied. A non-null result is the interesting one, and it means
the run entered a regime the Problem section says it should not have, which must
be identified before the change is credited for it.

## Test plan

Unit tests in `package accounting_test` and `package chequebook_test`:

- concurrent `Issue` calls for the same beneficiary produce strictly increasing
  cumulative payouts, and the persisted last issued cheque is never lower than
  one that was sent;
- a crash between allocation, send and persist leaves the node able to pay
  again, tested at each point;
- the total promised to one peer never exceeds what `reserveTotalIssued` allows,
  and `ErrOutOfFunds` is returned rather than a cheque the chequebook cannot
  honour;
- cheques confirmed out of order leave the same final balance as in order, **and
  settlement is not suppressed**;
- a payment in flight across a refreshment boundary leaves
  `refreshReservedBalance` consistent and does not blocklist the peer;
- with #304, the amount paid is
  `min(originated debt, debt - shadowReservedBalance)`. Not "the debt": `:481`
  still caps at the originated balance and `:493` still subtracts the shadow
  reserve, and only the `refreshDue` term goes away.

Plus the mixed-version test against a stock v2.8.2 node, showing it accepts
pipelined cheques, cheques larger than stock would have sent, and cheques it
would not otherwise have been sent at all, and blocklists nobody. The larger
case is the one `ErrBouncingCheque` rejects, so it must not be left out.

## Configuration

No new setting is proposed. `payment-early-percent` already exposes the dominant
lever and its default is measured as conditions 1 to 3 above. Per rule 8, a new
setting is proposed only if the measurement shows a value that matters and that
the existing dial does not already reach, and it would then ship with the
current behaviour as its default.

## Upstream portability

Both changes touch code that is unmodified in upstream bee, but **neither change
carries the `affects-upstream` marker.** Rule 11 says to tag defects and not
preferences, and #303 and #304 are optimizations this fork wants, not things
upstream got wrong. Two problems found while reading the path get their own
issues, separately from whether either change is ever built, and only one of
them is tagged. The marker authorises nothing further (rule 1).

- **The uncapped `refreshDue` at `:484`. Tagged,
  [#316](https://github.com/crtahlin/wasp/issues/316).** Verified in
  `upstream/v2.8.2` against the capped `:313`, `:738`, `:749` and `:1334`: the
  same five sites, the same line numbers, the same four-to-one split. This is
  rule 11's own example of a defect, a quantity computed two different ways, and
  it is reached on every `settle`.
- **The `chequebook.Issue` race in Design (a). Not tagged,
  [#317](https://github.com/crtahlin/wasp/issues/317).** The code is the same
  upstream, but no caller there or here can produce two overlapping calls for
  one beneficiary, for the reasons set out in Design (a). It is a latent hazard
  rather than a defect anyone is suffering, and rule 11 says to leave untagged
  what is reasoned from reading the code rather than reproduced. That issue
  records what evidence would justify the tag later.
- The absence of any acknowledgement that a cheque was accepted is an upstream
  protocol property, not a defect, and is out of scope.

**What adoption would take, which is the question this section is really for.**
Both files are byte-identical to `upstream/v2.8.2`, so either change is a clean
patch against that release with no fork-specific dependency: no wasp type, no
wasp config key, no wire change. The work that would not travel is the evidence.
Both changes are justified only inside a regime this spec predicts the bench
does not reach, so anyone adopting them would need their own measurement of the
debt rate and the refreshment interval on their own traffic. The two defect
issues travel independently of either change and are the more useful half.

## Rollout and rollback

- Nothing to turn on: both take effect when the node runs the build.
- Rollback is a revert of either commit independently, which is the other reason
  they land separately.
- No migration and no on-disk format change. Design (a) may change *when* the
  last issued cheque is persisted; if so, the implementation must state what an
  older binary reading that record would see.

---

Generated with help of AI.
