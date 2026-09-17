# Extending a larger payment threshold to one peer at a time

Issue: [#327](https://github.com/crtahlin/wasp/issues/327). Analysis:
[per-peer-credit.md](per-peer-credit.md), merged as the argument this spec
implements.

Code references are to `main` at `a8772819`, base `upstream/v2.8.2`.
`pkg/accounting` and `pkg/pricing` are byte-identical to that base, verified with
`git diff upstream/v2.8.2 a8772819 -- pkg/accounting pkg/pricing`, which is
empty. So every line cited in those two packages is also upstream's.

**Depends on [#324](https://github.com/crtahlin/wasp/issues/324)**, in review as
[#329](https://github.com/crtahlin/wasp/pull/329). That change adds the
`bee_retrieval_preferred_overdrafts` counter this spec uses as its falsifier,
and it supplies every overdraft figure quoted below. **No such counter exists at
`a8772819`**, and this spec cannot be implemented or measured before #329
merges.

## Terms

- **Payment threshold granted to a peer**: `paymentThresholdForPeer`, how much
  that peer may owe this node before this node stops serving it. Announced to
  the peer.
- **Payment threshold received from a peer**: `paymentThreshold`, the number that
  peer announced to us, which is what gates **our** requests to it.
- **Block**: one measurement session on the bench, with the node states and peer
  counts it happened to have. Figures from different blocks are not comparable.

## Problem

A provider serves a few per cent of a download and then runs out of credit with
the requester.

**Which field gates it, exactly**, because the two above are easy to confuse and
an earlier draft of this spec confused them. The refusal happens on the
**requester**, inside `PrepareCredit`, against the requester's copy of what the
provider announced:

```go
// pkg/accounting/accounting.go:313-322
timeElapsedInSeconds := min((a.timeNow().UnixMilli()-accountingPeer.refreshTimestampMilliseconds)/1000, 1)
refreshDue := new(big.Int).Mul(big.NewInt(timeElapsedInSeconds), a.refreshRate)
overdraftLimit := new(big.Int).Add(accountingPeer.paymentThreshold, refreshDue)
if increasedExpectedDebt.Cmp(overdraftLimit) > 0 {
    a.metrics.AccountingBlocksCount.Inc()
    return nil, ErrOverdraft
}
```

So the quantity to raise is what the **provider announces**, and the provider's
own `paymentThresholdForPeer` matters locally only through the `disconnectLimit`
derived from it. That makes `AnnouncePaymentThreshold` the mechanism of this
change rather than a step in it, which Design 3 takes seriously.

Note also `min(elapsed, 1)`: the refresh term is capped at one second, so the
limit is the announced threshold plus at most one refresh rate. It does not
accumulate over a download. An earlier note in this work said it did, and that
was wrong.

Two measurements, both recorded, say this limit is what binds.

**Raising the threshold raises what a provider delivers.** From
[results.md](results.md) Table 6, three runs per step except the last, which has
two:

| Threshold announced | Chunks from the provider | Share of the download |
|---|---|---|
| 13,500,000, the default | 176 (164-235) | 4.3% |
| 27,000,000 | 453 (407-486) | 11.1% |
| 54,000,000 | 1,261 and 1,405 | 31% and 34% |

Read those shares with the caution results.md attaches to them. Credit available
over a download is a fixed amount plus a part that grows with how long the
download lasts, so counts from different blocks are not comparable and a slower
block flatters the same threshold. The direction is what this establishes.
**That same arithmetic is a trap for this spec's own measurement**, and
Measurement says how it is avoided.

**And credit is what stops the sole-source case.** With the #324 fix in the
requester, so with a single refusal no longer removing the only holder of a
chunk for good, content only the provider holds still truncates at the shipped
lookahead buffer. Three runs, and the overdraft figures are from the counter
#329 adds:

| Runs | Bytes of 4,194,304 | Time to truncation | Rate | Overdrafts |
|---|---|---|---|---|
| 3 of 3 truncated | 1,310,720, so 31.2% | 1.34 to 1.42 s | 924,411 to 976,922 B/s | 311 to 429 |

Those times are times to truncation, not completion. A completing download of
that file in the same block took 6.8 to 9.6 s.

The same build with `Swarm-Lookahead-Buffer-Size: 0`, which reads one chunk at a
time instead of prefetching, completes 6 runs of 6. So the prefetch breaks it
through credit: many chunks in flight means many refused at once, and each
refused chunk is then sought from peers that do not hold it.

**The node-wide setting is not the answer.** `payment-threshold` is one number
for the whole node, so a provider raising it announces that credit to every peer
it has, including peers downloading nothing it announced. That is why the ledger
row for [#290](https://github.com/crtahlin/wasp/issues/290) records a raised
threshold as "not a setting to recommend". It is also capped: a node refuses to
start when its configured `payment-threshold` exceeds `maxPaymentThreshold`, 24
times the refresh rate or 108,000,000 (`pkg/node/node.go:241`, `:806`).

## Hypothesis

Announcing the larger threshold only to a peer that is asking this node as a
provider, and only while it is asking, removes the refusals that truncate a
sole-source download, at a fraction of the credit a node-wide raise would
announce.

**Falsifiable.** If `bee_retrieval_preferred_overdrafts` on the requester does
not fall, the announced threshold was not the constraint in that run and nothing
else in this document follows.

## Design

### 1. What the trigger can and cannot be

A requester asking a preferred peer attaches the `wasp-local-only` header to
every attempt (`pkg/retrieval/preferred.go:28`, sent at `:240`). So a peer
asking this node *as a provider* is distinguishable per request, with no new
message and no chunk-to-content index.

**But that header is not proof the peer is downloading content this node
announced, and this spec does not pretend otherwise.** The handler answers a
local-only request from `s.storer.Lookup().Get`
(`pkg/retrieval/retrieval.go:554`), which reads the whole chunk store, reserve
and cache included. Any peer can obtain a hit by asking for any chunk this node
happens to hold, and for an announced reference the node has published which
chunks those are.

**Gating on the content instead is not cheap with today's indexes**, and that is
worth stating so it is not rediscovered. Announcing a reference requires it to
be pinned first (`pkg/api/providers.go:249-259`), so "pinned" is the right set.
But pinning is indexed by collection: `pinChunkItem` is namespaced by the
collection's UUID and keyed by the chunk address
(`pkg/storer/internal/pinning/pinning.go:420-430`), and `HasPin` takes a root
reference, not a chunk (`:202`). Asking "is this chunk pinned" therefore means
one index read per pinned collection, on the hit path of every retrieval. That
is not viable.

### 2. What the operator is actually agreeing to

An earlier draft of this spec said a total credit budget makes the unverifiable
trigger acceptable, because "the worst case is a fixed number the operator
chose". **That was false and the reason matters.**

`Connect` resets the peer's ledger. It writes zero to the balance key
(`pkg/accounting/accounting.go:1435`) and zeroes `shadowReservedBalance`
(`:1427`) and `ghostBalance` (`:1428`), and it re-initialises
`paymentThresholdForPeer` and `disconnectLimit` from the node-wide values
(`:1431`, `:1433`). `Disconnect` blocklists the peer for `blocklistUntil(peer, 1)`
seconds, which is `(latentDebt + paymentThreshold) / refreshRate`, about 12
seconds at a 54,000,000 grant.

So a peer can take the grant, consume it, drop the connection, wait about twelve
seconds, reconnect with the debt erased, and take it again. **The cumulative
cost is therefore not bounded by any budget.** It is bounded by bandwidth and by
how long the operator leaves the setting on.

**So the setting is not "extra credit you expect to be paid". It is bandwidth you
are choosing to give away**, to peers that ask you as a provider, and the
documentation must say exactly that. This is not as bad as it sounds for the
feature's purpose: an operator who turns on content providing has already
decided to serve content to strangers at their own cost. It is bad for any
description of the feature as bounded risk, and that description is withdrawn.

What the budget does still do is bound **concurrent** exposure, which is what
protects the node from having many peers in deep debt at once. That is worth
having and it is what the budget is for. It is not a cumulative bound and the
spec must not imply one.

The four things that do bound the surface:

1. **The operator opts in.** The threshold is a new setting whose default
   disables the behaviour. Nothing changes for a node that does not set it.
2. **`providers-enable` must be on, and it is off by default**
   (`cmd/bee/cmd/cmd.go:415`, honoured at `pkg/retrieval/retrieval.go:171` and
   `:558`).
3. **The concurrent budget**, below.
4. **The existing local-only rate limits**, 100 misses a second per peer and
   1,000 a second in total (`pkg/retrieval/retrieval.go:119-120`). These bound
   the miss path only, not the hit path, so they are listed for completeness
   rather than as a real bound on this change.

**The hardening is a separate issue, deliberately.** The requester already knows
which root reference it is downloading, since the provider hint is attached per
content key (`pkg/api/providers.go:129-134`). It could send that root in a second
header, and the provider could check it with one `HasPin` read. That narrows the
trigger to content this node actually hosts. It is not in this spec because it
adds a header for a benefit that is only worth having if the measurement below
shows the feature is worth hardening, and because it does not fix the reconnect
hole above either.

### 3. Raising it, and why the announce is the whole of it

`paymentThresholdForPeer` and `disconnectLimit` are per-peer fields, initialised
from the node-wide values on connect (`:1415-1433`). They already diverge per
peer through the growth path, which recomputes `disconnectLimit` from the new
threshold and announces the result (`:657-662`).

**How often that growth path fires, stated correctly**, because an earlier draft
had it as "one refresh rate per payment sent" and that is wrong in both
direction and frequency. `notifyPaymentThresholdUpgrade` is called from
`NotifyPaymentReceived` (`:1014-1016`) and `NotifyRefreshmentReceived`
(`:1181-1183`), so it is driven by what the **peer** pays **us**, never by what
we send. And it fires only once `totalDebtRepay` passes `thresholdGrowAt`, which
starts at `thresholdGrowStep = refreshRate * 100`, that is 450,000,000
(`:236`, `:617`), and grows from there. So it is roughly once per 450,000,000
units of cumulative repayment from that peer: rare in general, and reachable
precisely for the high-volume peers this feature targets.

So the raise is: reserve budget, set the two fields, announce, and be ready to
undo all three. Six details a naive version gets wrong.

**(a) The announce must not happen under the peer lock.** Upstream's growth path
calls `AnnouncePaymentThreshold` while holding `accountingPeer.lock` (`:662`),
and that opens a fresh stream with a five-second timeout
(`pkg/pricing/pricing.go:128-131`). Meanwhile `PrepareDebit` takes the same lock
with `TryLock(ctx)` and returns the error immediately on failure (`:1213-1216`),
and the retrieval handler turns that into a failed delivery
(`pkg/retrieval/retrieval.go:574-576`). Under the lookahead prefetch, which is
many concurrent requests from one peer and is this spec's own scenario, copying
that pattern would make chunk deliveries fail and **reproduce the truncation
this change exists to remove.** The announce is therefore made asynchronously,
outside the lock. This is the single thing most likely to turn this change into
a regression, and it is why "one header test before `PrepareDebit`" understates
the work.

**(b) A failed announce must roll the raise back.** Upstream's only handling of
an announce error is to log it and keep the raised local value (`:663-665`).
Copied as-is, a failed announce leaves this node with a raised `disconnectLimit`
and a consumed budget slot, and the requester with no extra credit: all of the
cost and none of the benefit. On failure, the delta is subtracted again and the
budget released.

**(c) Store the granted extra as a delta, never a remembered absolute.** The
growth path in (a) can fire while a raise is active, and it does
`accountingPeer.paymentThresholdForPeer = new(big.Int).Add(...)` on the
**raised** value (`:657`). Restoring a remembered absolute on decay would discard
every growth increment that fired during the raise, leaving the peer below where
it would have been had it never used the feature, so using the feature would make
a peer more likely to be blocklisted later. Subtract the delta instead.

**(d) Beware the pointer.** `:657` **reassigns** the pointer, while `Connect`
(`:1431`) and `NotifyPaymentThreshold` (`:998`) mutate in place with `.Set()`. A
delta or a remembered value held as a `*big.Int` will alias one writer and
detach from the other. Hold values, not pointers, and re-read under the lock.

**(e) `disconnectLimit` moves with the threshold when raising, and not when
lowering.** It is derived from the threshold (`:659`) and the two are raised
together everywhere they are raised today. The asymmetry on the way down is
deliberate and is section 4. An earlier draft said "always", which contradicted
its own section 4.

**(f) Budget admission needs its own mutex, and no I/O under it.** `Accounting`
has exactly one node-level mutex, `accountingPeersMu` (`:155`), whose documented
job is the peer map. No existing path holds it and a peer lock at the same time:
`getAccountingPeer` releases it before returning (`:602-603`) and
`PeerAccounting` copies the map and releases it (`:713-716`) before taking any
peer lock. Reusing it would invert that order and overload it. This change adds a
dedicated budget mutex, or an atomic counter, with the rule that it is never held
across a peer lock or across I/O.

### 4. Lowering it again, which is the part most likely to go wrong

Nothing in the current code lowers `paymentThresholdForPeer`. Lowering is
peer-visible and it is where this design can hurt a peer it meant to help, so it
happens in two steps.

**Step one, on idle.** A peer that has made no local-only hit for
`providerCreditIdle` is no longer downloading from this node as a provider. Its
`paymentThresholdForPeer` has the delta subtracted and the new value is
announced, so the peer stops accruing more before paying.

**A stock peer accepts the decrease, with one condition that must be named.**
`NotifyPaymentThreshold` stores whatever arrives (`:992-1001`). The pricing
handler validates the announced value against a minimum, and a value below it is
not merely rejected: it returns `p2p.NewDisconnectError(ErrThresholdTooLow)`
(`pkg/pricing/pricing.go:100-103`). So lowering is safe only while the announced
value stays at or above the **receiver's** `minPaymentThreshold`, which is the
receiver's configuration and unknown to us. In practice the restored value is at
least our own node-wide `payment-threshold`, which `node.go:802` forces above
9,000,000, so the risk is small. It is stated because the safety comes from that
constraint and not from the store being permissive.

**Step two, on settlement, and gated on ghost balance as well as balance.**
`disconnectLimit` stays at the raised value until **both** the peer's balance and
its ghost balance are back within the pre-raise limit.

Two reasons, and the second was missing from an earlier draft.

- `debitAction.Apply()` records the debit and **then** blocklists a peer whose
  new balance reaches `disconnectLimit` plus at most one second of the refresh
  allowance (`:1321`, `:1343-1353`, tested with `>=`). Note this is `Apply`, not
  `PrepareDebit`, and the chunk is served before the block: an earlier draft said
  `PrepareDebit` and said the next chunk would be refused, and both were wrong.
- `ghostBalance` is a **second** gate on the same field and it has no refresh
  tolerance and a strict comparison: `debitAction.Cleanup()` adds the price to it
  on every prepared-but-unapplied debit and blocklists when it exceeds
  `disconnectLimit` (`:1371-1374`). Settlement does not reduce it; only `Connect`
  resets it (`:1428`). Prepared-but-unapplied debits are exactly what the
  lookahead prefetch produces in volume, so a decay gated on balance alone would
  blocklist the peer on the next cancelled request.

**Step three, on disconnect.** The budget slot is released when the peer
disconnects, whether or not it settled. This is not a nicety. Without it, a peer
that takes a slot and leaves holds it until restart, and a handful of throwaway
identities exhaust the budget permanently, after which every legitimate provider
download is served at the default and gets the 4.3% behaviour. **The operator
agreed to a credit cost, not to a feature that switches itself off**, and an
earlier draft of this spec called the slot leak "the correct behaviour". It is
not.

**Two further rules against the same denial.** At most one slot per peer, and
when the budget is full the oldest **idle** holder is decayed to make room rather
than the new request being refused. A raise is refused only when every holder is
active.

**`providerCreditIdle` is a compiled-in constant**, 60 seconds, not a setting.
Rule 8 wants measurement before surface area, and 60 seconds is chosen only so
that it cannot fire inside a download: a completing 4 MiB sole-source download
took 6.8 to 9.6 s in the block above. One interaction to note rather than
resolve: 60 seconds is shorter than `demoteFor`, 10 minutes
(`pkg/retrieval/preferred.go:40`), so a provider demoted after
`demoteAfterMisses` misses (`:38`) will always decay and need re-raising, each
cycle costing an announce round trip.

### 5. Light peers are excluded

`Connect` deliberately gives a light peer `lightPaymentThreshold` and
`lightDisconnectLimit` (`:1420-1422`), and `lightFactor` is 10
(`pkg/node/node.go:233`), so a light peer's threshold is 1,350,000 at the
default and it settles at `lightRefreshRate`, 450,000 a second.

Granting a light peer the same value as a full peer would be 10 to 40 times the
exposure upstream intends for it, and it would hold its budget slot ten times as
long because it clears debt ten times slower. **Light peers therefore do not get
the raise in this version.** Scaling the grant by `lightFactor` instead is the
obvious alternative and it is left out because nothing has measured a light
requester downloading from a provider. Stated so it is a decision rather than an
oversight.

### 6. Where the code goes

One exported method on `*Accounting` in `pkg/accounting`, because the state it
touches lives on `accountingPeer` and nowhere else. The retrieval handler reaches
it through a small fork-authored interface declared in `pkg/retrieval`, rather
than by widening upstream's `accounting.Interface`, so the next upstream sync
sees an added file and an added method and not a changed interface.

The hit path needs the header read that does not exist today: the handler tests
the local-only header only inside the `storage.ErrNotFound` branch
(`pkg/retrieval/retrieval.go:556-560`) and on a hit goes straight to pricing at
`:572`. That read is cheap. The work is everything in section 3.

## What this risks

- **Bandwidth given away, without a cumulative bound.** Section 2. This is the
  honest statement of the cost and it is what the option's documentation must
  say.
- **A peer blocklisted by a mishandled decay.** Section 4, and the ghost-balance
  gate is the part an earlier draft missed. The test plan covers both.
- **Deliveries failing because of a lock held across a stream.** Section 3(a).
  This would be a regression in exactly the case the feature targets.
- **The feature denied by budget exhaustion.** Section 4, step three and the two
  rules after it.
- **Nothing for stock requesters.** A stock Bee node never sends the local-only
  header, so it never gets the raise. Note that a stock node would *accept* a
  larger announced threshold, since the pricing handler imposes no maximum; not
  doing it is a choice, not a limitation.
- **Above 108,000,000 is not attempted.** Per-peer values can exceed
  `maxPaymentThreshold`, because that check runs only against the node's own
  configured value at startup and the growth path already drifts past it without
  re-checking. This spec refuses to go above it: nothing above it has been
  measured, and crossing a limit upstream chose needs a better reason than
  "possible". The setting is validated against it.

## Protocol impact

**None.** No wire format, protocol identifier, handshake or message change. The
threshold is announced with the existing `AnnouncePaymentThreshold` message,
carrying a value the receiving side already accepts without an upper bound, and
which a stock node could itself be configured to send. No new stream header.
`.github/protocol-freeze.lock` fingerprints `protocolName` and `protocolVersion`
for `pkg/pricing`, neither of which changes, so `make protocol-freeze` must pass
with the fingerprint unchanged and the `protocol-change` label is not needed.

## Configuration

| Setting | Default | Meaning |
|---|---|---|
| `providers-payment-threshold` | `0`, disabled | The threshold announced to a full-node peer making local-only hits. Validated as either 0, or at least the node-wide `payment-threshold` and at most `maxPaymentThreshold`. |
| `providers-credit-budget` | `0` | The most extra credit, summed over all peers holding a raise, that may be outstanding **at one time**. Extra means the announced value minus the value it replaced. Required to be non-zero when the threshold above is set. |

**What raising `providers-payment-threshold` costs:** more of this node's
bandwidth served to each admitted peer before that peer has to pay, and, per
section 2, with no bound on how often the same peer can take it again by
reconnecting. What lowering it costs: the provider delivers a smaller share of
each download, which is the 4.3% in the Problem table.

**What raising `providers-credit-budget` costs:** more peers hold a raise at
once, so more of this node's unsecured credit is outstanding simultaneously. What
lowering it costs: provider downloads beyond the budget are served at the
ordinary threshold, so they get the 4.3% behaviour.

**Who pays.** Mostly this operator, but not only: step one of the decay also
lowers the peer's `earlyPayment` to a percentage of the new value (`:999`,
`payment-early-percent` defaulting to 50), which brings forward a settlement at
a moment this node chose, and the cheque-side cost of a settlement is recorded in
[#300](https://github.com/crtahlin/wasp/issues/300) and
[#304](https://github.com/crtahlin/wasp/issues/304). An earlier draft claimed no
other node pays anything, which was not established.

**On rule 8.** Two settings in one issue, where the rule asks for one issue per
setting. The claim made here, rather than assumed: `providers-payment-threshold`
is the tuning dial and the measurement that justifies it is in this document, so
it is the rule's normal case with the measurement attached. `providers-credit-budget`
is a safety bound whose absence is itself the failure mode, so it cannot
sensibly ship later than the thing it bounds; that is the exception being
claimed, and it is claimed explicitly rather than by silence.
`providerCreditIdle` stays a constant, which is the rule applied rather than
excepted.

## Measurement

On the bench, following [test-bench.md](../../agent-playbooks/test-bench.md).
Every arm in one block, interleaved, at least three runs each (rule 7).

**First, what not to measure, because an earlier draft of this spec got this
wrong.** From the requester's side a node-wide raise and a per-peer raise are the
same experiment: in both the provider announces the same number to this
requester, and whether other peers also got it cannot affect delivery here. So
"the per-peer share lands within the spread of the node-wide share" is close to
true by construction, and gating acceptance on it would be gating on the arm with
the least information.

Worse, it can reject the better outcome. Credit available over a download is a
fixed amount plus a part that grows with the download's duration, so a **faster**
arm receives less grown credit and delivers a **smaller** share. An arm that wins
on time can therefore lose on share. That is arithmetic, not block variance, and
one session does not fix it.

So the arms are:

1. **Control.** The change built but the setting unset, sole-source content at
   the shipped lookahead buffer. This is the in-session baseline for arm 2, and
   it exists because comparing against the 3-of-3 truncations in Problem would
   span two sessions and two builds.
2. **Per-peer raise**, sole-source at the shipped lookahead buffer. **The primary
   arm.**
3. **Node-wide raise** to the same value, sole-source, same buffer. Expected to
   be indistinguishable from arm 2 on delivery; run to confirm that the
   restriction costs nothing, and to expose the parts that genuinely differ,
   which are announce latency, budget admission and the decay.
4. **Non-regression**, content the network also holds, hinted, per-peer raise
   against the setting unset.

Recorded per run: bytes and SHA-256, the `curl` exit code where 18 means
truncated, total time, time to first byte, chunks delivered by the provider,
`bee_retrieval_preferred_overdrafts` and
`bee_accounting_accounting_blocks_count` on the requester, and on the provider
the balance owed by the requester at the end plus the new budget metrics. Rates
in bytes per second beside every timing.

Delivered share is reported **beside** total time, never gated on, and reported
against the elapsed credit window as well as raw, so the duration confound
above is visible rather than silent.

**Pre-registered predictions.**

- **Primary: arm 2 completes with a matching SHA-256 where arm 1 truncates, and
  `bee_retrieval_preferred_overdrafts` falls by at least a factor of ten from
  arm 1's 311 to 429.** If the overdraft count does not fall, the announced
  threshold was not the constraint and nothing else in this document follows.
- Arm 3 is indistinguishable from arm 2 on bytes and within the spread on total
  time. A large gap either way means something other than the announced value
  differs between them, and the design is not understood.
- Arm 4 does not regress beyond the spread of its own control.
- **Time to first byte falls slightly with the raise, and the prediction carries
  a tolerance rather than claiming no movement.** results.md Table 6's hinted
  rows are 0.46 s (0.43-0.48) at 13,500,000, 0.31 s (0.31-0.58) at 27,000,000
  and 0.308 s at 54,000,000, so about 1.5x between the ends. Predicted: arm 2's
  median at or below arm 1's, and no more than 0.10 s above it. An earlier draft
  predicted no movement at all, which that table already contradicts.
- No arm blocklists a peer, and the provider's `disconnects_overdraw_count` and
  `disconnects_ghost_overdraw_count` stay at zero.

**A negative result** is arm 2 still truncating, or the overdraft counter not
falling, or any arm blocklisting a peer. Any of those closes #327 with the
evidence.

## Acceptance

**Accept** if arm 2 completes with a matching SHA-256 where arm 1 truncates,
**and** the requester's overdraft counter falls by at least a factor of ten,
**and** arm 4 does not regress beyond its control's spread, **and** no arm
blocklists a peer.

**Reject** if arm 2 truncates, or the overdraft counter does not fall, or arm 4
regresses, or any peer is blocklisted.

Note what is deliberately **not** in the acceptance rule: the delivered share.
It is reported and it is not a gate, for the reason in Measurement.

## Test plan

Unit tests in `package accounting_test` and `package retrieval_test`:

- a local-only hit raises the peer's announced threshold and `disconnectLimit`
  together, and announces the new value;
- **the announce is not made while the peer lock is held**, asserted so that a
  concurrent `PrepareDebit` for the same peer still succeeds while an announce is
  in flight;
- **a failed announce rolls the raise back and releases the budget**;
- an ordinary hit, with no local-only header, raises nothing;
- a local-only **miss** raises nothing, so the miss path cannot buy credit;
- a light peer is not raised;
- the raise is refused once every budget holder is active, and the peer is then
  served at the ordinary threshold rather than refused service;
- when the budget is full and a holder is idle, the oldest idle holder is decayed
  and the new peer admitted;
- at most one slot per peer, however many local-only hits it makes;
- two concurrent first requests from different peers cannot both take the last
  slot of budget;
- **the growth path firing during an active raise does not corrupt the delta**:
  after decay the peer's threshold equals what it would have been had the raise
  never happened, including every growth increment that landed in between;
- after `providerCreditIdle` with no local-only hit, the threshold is lowered and
  announced, while `disconnectLimit` is not;
- **a peer in debt above the pre-raise limit is not blocklisted by the decay**,
  and `disconnectLimit` is released only once its balance is within that limit;
- **the same for ghost balance**: a peer whose ghost balance sits between the
  pre-raise and raised limits is not blocklisted by the decay;
- **the budget is released on disconnect**, settled or not;
- the setting is refused at startup when it is below the node-wide threshold,
  above `maxPaymentThreshold`, or set with a zero budget.

The pre-raise limit against which step two gates is the `disconnectLimit` derived
from the pre-raise threshold, not the threshold itself. With
`payment-tolerance-percent` defaulting to 25 the two differ by a quarter
(`percentOf`, `:1527-1529`), so the tests pin the number.

Plus the mixed-version check against a stock v2.8.2 node, which must accept both
the raise and the later lowering and blocklist nobody.

## Upstream portability

**No `affects-upstream` marker.** The per-peer threshold machinery is upstream's
and works as intended; what is new is driving it from a fork-authored signal that
upstream has no equivalent of. `pkg/retrieval/preferred.go` does not exist
upstream and `providers-enable` is a fork setting. This is a fork feature, not a
Bee defect, so rule 11 does not apply.

Two upstream behaviours this change works around rather than fixes, recorded
because a reader will wonder: the announce made under the peer lock (`:662`) and
the announce error that is logged while the raised value is kept (`:663-665`).
Both are reasonable for a path that fires once per 450,000,000 units of
repayment, and neither is a defect at that frequency. They only matter because
this change would fire the same code far more often. If that judgment is wrong,
it becomes its own issue.

## Rollout and rollback

- Off unless `providers-payment-threshold` is set, and it cannot be set usefully
  without `providers-enable`, which is also off by default.
- Rollback is unsetting the option. No migration, no on-disk format change, no
  stored state: raises live in memory on the accounting record and are
  re-initialised from the node-wide values on reconnect (`:1415-1433`).
- The budget counter is cleared at startup and on every disconnect, so a restart
  cannot leave it consumed.
- A node restarted mid-download loses the raises, and the peer's next request is
  served at the ordinary threshold. That is the same as any other reconnect and
  needs no special handling.

---

Generated with help of AI.
