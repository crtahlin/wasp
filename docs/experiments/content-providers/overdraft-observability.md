# Making an overdraft refusal visible

Issue: [#353](https://github.com/crtahlin/wasp/issues/353). Exists to unblock
one measurement on [#343](https://github.com/crtahlin/wasp/issues/343), recorded
in [retrieval-rate.md](retrieval-rate.md),
[truncation-cause.md](truncation-cause.md) and
[overdraft-terms.md](overdraft-terms.md).

**This is revision 5.** Four reviews refused it, finding thirteen, fourteen,
fourteen and then six. Two of the failures are worth stating at the top rather
than in a footnote, because both are the failure this document exists to
prevent.

(The header said "revision 4, three reviews" when the document merged: the body
was updated for the fourth review and this line was not. It is the same stale
counter that the companion results document eventually solved by deleting its
revision commentary altogether.)

**Revision 3 reinstated three claims that had already been withdrawn** in
[overdraft-terms.md](overdraft-terms.md) on `main`, with the same citations:
that the code forbids a completed refreshment tightening the gate, that the gate
is a single number, and that `shadowReservedBalance` is subtracted only at
`:306`. The cause was mechanical rather than analytical: this document was
drafted while the companion was still being corrected, and was then applied
without reconciling against what had changed in it. Where the two documents
overlap, **the companion is the reference and this one cites it** rather than
restating the argument.

**Revision 1 reported an empty diff as verification.** It claimed in bold that
`pkg/accounting` is byte-identical to `upstream/v2.8.2`. The command was run in
a worktree whose local `main` ref was stale at `de136880` while `origin/main`
was at `a561b5a5`, so it returned nothing, and nothing was read as confirmation.
The package carries 782 inserted lines of fork change. Filed as
[#355](https://github.com/crtahlin/wasp/issues/355), because `AGENTS.md` rule 13
tells the reader to run exactly that command.

**Revision 2 fabricated a code citation.** It told the implementer to copy
`pkg/retrieval/retrieval_test.go:355` as
`log.NewLogger("test", log.WithSink(buf), log.WithVerbosity(log.VerbosityAll))`.
The actual line is `log.NewLogger("test", log.WithSink(buf))`. The invented
argument was the one thing that makes the test work, so the pattern it told the
implementer to copy was the vacuous-pass trap the next paragraph warned about.

Every defect from all three reviews is corrected below and marked where it
appears.

## Problem

An overdraft refusal is invisible.

```go
// pkg/accounting/accounting.go:332-335
if increasedExpectedDebt.Cmp(overdraftLimit) > 0 {
	a.metrics.AccountingBlocksCount.Inc()
	return nil, ErrOverdraft
}
```

`AccountingBlocksCount` is an unlabelled counter (`metrics.go:110-115`), with no
peer and no amounts. No log line is written at any level.

The callers add little. `preferred.go:229-235`:

```go
if err != nil {
	skip.Add(chunkAddr, peer, overDraftRefresh)
	if errors.Is(err, accounting.ErrOverdraft) {
		s.metrics.PreferredOverdrafts.Inc()
	}
	return err
}
```

The `skip.Add` is how a refusal becomes retrieval behaviour, and it fires for
**every** error, tagging all of them `overDraftRefresh` whether or not the error
was an overdraft. Only the counter is conditional. `retrieval.go:360-364`
likewise records a skip and retries.

So a chunk refused for credit and a chunk that is genuinely gone both surface as
`storage.ErrNotFound`: the retrieval loop returns it at `retrieval.go:414` once
the attempt budget is spent, with nothing recording why the budget went. The
lock-contention path at `:285` **does** log, at plain Debug, so a chunk delayed
by lock contention is diagnosable and a chunk refused for credit is not. That
asymmetry is the whole of this document.

## Hypothesis

The gate is

```
increasedExpectedDebt  >  paymentThreshold + refreshDue
```

where, from `getIncreasedExpectedDebt` (`accounting.go:256-279`),

```
increasedExpectedDebt = max(-balance, 0) + reservedBalance + price + surplusBalance
```

`balance` there is the **raw stored balance** (`accounting.go:259`), which
`/accounting` exposes as `consumedBalance` rather than as `balance`
(`:768-769`).

Note `shadowReservedBalance` is **not** in it. The subtraction at `:306` decides
whether `settle()` fires (`:312`); it is also subtracted at `:386` and `:429`,
and it feeds `peerDebt` (`:883`), `peerLatentDebt` (`:911-912`) and
`shadowBalance`
(`:948-952`). The field itself is incremented at `:515` and `:1238`. An earlier
revision said "subtracted only at `:306`", which
[overdraft-terms.md](overdraft-terms.md) already records as wrong.

[overdraft-terms.md](overdraft-terms.md) measured twelve runs and established
that **both** remaining terms move on the same timescale: the settled balance
cycles between about -300,000 and a floor at or just short of the announced
threshold, and `reservedBalance` peaks at 2,530,000 with the lookahead prefetch
off against 12,860,000 with it on.

The hypothesis is therefore **not** that one term is the culprit. It is that
**which term dominates at the instant of refusal decides the remedy, and the two
answers point opposite ways**:

- if `reservedBalance` dominates, the refusals are self-inflicted by this node's
  own concurrency, spacing retries out is wrong, and bounding concurrent
  reservation against one peer is the direction;
- if the settled debt dominates and a completed refreshment is what releases a
  chunk, the remedy concerns triggering settlement, and because `settle()` is
  called from inside `PrepareCredit` (`:313`), fewer attempts means fewer
  chances to start one.

A log line at the refusal, carrying every term under the same lock that guards
the comparison, distinguishes them. Nothing else does.

### Why the existing observability cannot

| Quantity | API | Metric | Log |
|---|---|---|---|
| settled balance (raw, = `consumedBalance`) | `/accounting`, `/balances` | aggregate only | yes, V(2) at `:364`, `:1208`, `:1309` |
| `reservedBalance` | `/accounting` | no | never |
| `shadowReservedBalance` | `/accounting` | no | never |
| `surplusBalance` | `/accounting` (`accounting.go:774`) | no | never |
| `refreshTimestampMilliseconds` | no | no | never |

`/accounting` is a snapshot with no event semantics, so no sample is
attributable to the refusal it sat beside, and a refusal resolving inside one
sampling interval is invisible. [overdraft-terms.md](overdraft-terms.md)
demonstrates this rather than asserting it: it reached the limit of what polling
can say and withdrew a conclusion that had added two values from different
samples.

## What the base actually is

Code references are to commit `443b6246`, base `upstream/v2.8.2`.

`pkg/accounting` is **not** unmodified. Against that base it carries 782
inserted lines (and 9 deleted) from
[#327](https://github.com/crtahlin/wasp/issues/327):

| File | Inserted | Deleted |
|---|---|---|
| `provider.go` | 237 | 0 |
| `provider_test.go` | 466 | 0 |
| `accounting.go` | 56 | 9 |
| `metrics.go` | 23 | 0 |

The `accounting.go` change adds `providerGrant` to the peer record,
`providerThreshold`, `providerBudget`, `providerBudgetMu` and
`providerBudgetUsed` to the service, and releases a grant in `Connect`
(`:1420`) and `Disconnect` (`:1526`). Revision 2 named the second of those
`terminate`, which is a function in `pkg/settlement/pseudosettle`, not here.

Two consequences.

**The gate itself is unmodified upstream code.** The body of `PrepareCredit`
carries no fork change. But the **line numbers are not upstream's**: `:285`,
`:325`, `:332-335` and `:1106` here are `273`, `313`, `320-323` and `1094` in
`upstream/v2.8.2`. The offset is a constant 12 at all four sites, though they
are in two functions: the first three are in `PrepareCredit` and `:1106` is in
`NotifyRefreshmentSent` (`:1097`). Revision 2
gave the third of those as `321-322`, which excludes the `if` and the closing
brace.

**`paymentThreshold` is not static in this fork.** On the **granting** node,
`provider.go:156-157` (inside `applyProviderGrant`, `:133`) raises
`paymentThresholdForPeer`, "individual payment threshold at which the peer is
expected to pay" (`accounting.go:139`), and announces it. On the
**requesting** node that arrives
at `NotifyPaymentThreshold` (`accounting.go:1004-1013`), which is what actually
writes `paymentThreshold`, the term the gate reads. So with the provider feature
on, the term this analysis treats as fixed moves on the requester side, where
`PrepareCredit` runs. Revision 3 cited only the provider-side write for a
requester-side effect.

## Terms, including one that is easy to invert

- **Overdraft refusal**: `PrepareCredit` returning `ErrOverdraft`
  (`accounting.go:334`).
- **`paymentThreshold`**: what the **peer announced**, documented at `:137` as
  "the threshold at which the peer expects us to pay", exposed as
  `thresholdReceived`. It is **not** this node's own `payment-threshold`
  setting. Revision 2 conflated the two and quoted 13,500,000 as though it were
  a local default; it is the value the provider announced, and it was measured.
- **The gate**: `paymentThreshold + refreshDue`, exposed whole as
  `currentThresholdReceived` (`:765`). Because `timeElapsedInSeconds` is
  `min((now - ts)/1000, 1)` (`:325`), `refreshDue` is either 0 or one
  `refreshRate` of 4,500,000 (`pkg/node/node.go:236`), so with the measured
  announcement of 13,500,000 **the gate is either 13,500,000 or 18,000,000, not
  a single number**; 18,000,000 is its ceiling. Revision 2 called 13,500,000
  "the gate" and revision 3 called it 18,000,000. Both are wrong in the same
  way, and which value is in force at a given refusal is exactly what the line
  below records.
- **Reserved balance**: `reservedBalance`, charges for requests started and not
  yet completed.
- **Surplus balance**: `surplusBalance`, credit received from the peer that is
  not treated as debt for settlement, and the fourth term of the gated quantity.
- **Shadow reserved balance**: `shadowReservedBalance`, charges the peer may
  have applied that this node has not confirmed. Not in the gate.
- **V(2)**: the verbosity the `all` level enables and plain `debug` does not
  (`pkg/log/registry.go:118-125`).

## The refresh timestamp does not record a completed refreshment

```go
// accounting.go:1096  "called by pseudosettle when refreshment is done or failed"
accountingPeer.refreshTimestampMilliseconds = timestamp   // :1106
if receivedError != nil {                                 // :1109
```

The assignment is **unconditional and above the error branch**. Nine of the ten
callers in `pkg/settlement/pseudosettle/pseudosettle.go` (`:276, 285, 303, 310,
319, 327, 334, 340, 350`) pass `timestamp = 0`; only `:358` passes a real time.

So a **failed** refreshment sets it to zero, as does a peer that never
refreshed. An age computed from it would read about 1.79e12 milliseconds in
precisely the states worth telling apart. The log line therefore records the raw
timestamp and the saturated `timeElapsedInSeconds`, not an age.

One caveat: `min((now - ts)/1000, 1)` caps the top and not the bottom, so a
backwards clock step yields a negative `refreshDue` and a limit **below**
`paymentThreshold`.

## Design

One V(2) Debug line at the refusal site.

```go
a.loggerV2.Debug("credit refused, would overdraw",
	"peer_address", peer,
	"price", bigPrice,
	"expected_debt", increasedExpectedDebt,
	"overdraft_limit", overdraftLimit,
	"payment_threshold", accountingPeer.paymentThreshold,
	"refresh_due", refreshDue,
	"refresh_timestamp_ms", accountingPeer.refreshTimestampMilliseconds,
	"elapsed_seconds", timeElapsedInSeconds,
	"settled_balance", currentBalance,
	"surplus_balance", surplusBalance,
	"surplus_error", surplusErr,
	"reserved_balance", accountingPeer.reservedBalance,
	"shadow_reserved_balance", accountingPeer.shadowReservedBalance,
	"settle_called", settleCalled,
)
```

### Everything in that block, and where it comes from

Revision 2 printed this as though it dropped in. Its accounting of the names was
incomplete, and revision 3's was miscounted. In full, **fourteen** values.
(Thirteen at the time of writing; `surplus_error` was added during
implementation, so that a logged zero surplus is never mistaken for a read
that failed.)

**Already in scope at `:332`** (eleven): `peer` (the function parameter,
`:281`), `bigPrice` (`:295`), `increasedExpectedDebt`, `overdraftLimit`,
`refreshDue`, `timeElapsedInSeconds`, `accountingPeer.paymentThreshold`,
`accountingPeer.refreshTimestampMilliseconds`, `.reservedBalance`,
`.shadowReservedBalance`, and `currentBalance` from `:300`.

Revision 3 said "ten in scope, two to introduce, twelve in all". That balanced
only because it left `currentBalance` out of the ten and counted `a.loggerV2`,
which is the receiver rather than a logged value, as one of the two.

**To be introduced** (two):

- **`settleCalled`**, a `bool` declared before `:312` and set inside that
  branch. It exists nowhere in the tree. It is the field the #343 question turns
  on and is derivable from nothing else. Named for what it records: `settle()`
  often does nothing, so entering the branch is not the same as a settlement
  starting. An earlier draft called it `settle_triggered`, which claimed more.
- **`surplusBalance`**. It is the fourth term of the gated quantity and is
  **not** in scope: `getIncreasedExpectedDebt` reads it at `:272` and does not
  return it. Without it the Measurement section's consistency check cannot be
  performed, which revision 3 required while omitting the field. Read it on the
  refusal path only, with `a.SurplusBalance(peer)`, so the extra store read
  costs nothing on the path that succeeds. It needs no special error handling:
  it returns zero for a peer with no stored surplus (`:557-564`). On any other
  error, log zero rather than failing the refusal, since a diagnostic must not
  change the outcome it is describing.

And one supporting change that is not a logged value:

- **`a.loggerV2`**, a new field on the service, built **once in
  `NewAccounting`** as `logger.WithName(loggerName).V(2).Register()` beside the
  existing `logger` field (`:238`).

Building it once is not style. The eight existing V(2) registrations in this
file (`:348, 959, 1017, 1184, 1221, 1254, 1478, 1507`) each build one per call,
and `a.logger.V(2).Register()` forces the full `Build()` path (clone, join, allocate,
flatten, then `hash()` with a `fmt.Sprintf` and two `reflect.ValueOf` calls,
then a `sync.Map` load) **whether or not the level is enabled**. `PrepareCredit`
runs once per chunk per peer attempt, the hottest accounting path there is.
Revision 2 claimed the disabled cost is "a comparison and a return"; that
describes `Debug()`, not `V(2)`, and is withdrawn.

### The supporting change

At `:319` the post-settle recomputation discards its second return value:

```go
increasedExpectedDebt, _, err = a.getIncreasedExpectedDebt(peer, accountingPeer, bigPrice)
```

Revision 1 said this value "is the balance the gate then compares against".
**False.** The gate's operands are `increasedExpectedDebt` and
`overdraftLimit`; `currentBalance` is read only at `:312`, before the
recomputation.

Revision 3 then gave a second reason that is also wrong: that the uncaptured
value would be "the pre-settle balance, so the line reports a debt that no
longer exists". **`settle()` is asynchronous.** It dispatches
`go a.refreshFunction(...)` (`:476`) and `go a.payFunction(...)` (`:521`) and
returns `nil` at `:527`, writing no balance. The store write happens later, in
`NotifyRefreshmentSent` (`:1169`) or on the payment-sent path. So the re-read at
`:319` returns the **same** value, for the reason given below, and what
`settle()` changes synchronously is bookkeeping (`refreshOngoing`,
`paymentOngoing`, `shadowReservedBalance`, `refreshReservedBalance`), none of
which is in `settled_balance` or `increasedExpectedDebt`.

The reason that survives is weaker still, and is worth stating as such:
**`settled_balance` should come from the same call as `expected_debt`**, which
is tidy rather than necessary. It is **not** true that a concurrent refreshment
could otherwise fall between them, which an earlier draft claimed:
`accountingPeer.lock` is held for the whole of `PrepareCredit` and every writer
of the balance takes it, so both calls return the same value by construction.
The capture is therefore defensive and **not separately testable**, and
reverting it passes every test. The capture is
behaviour-neutral, since `:319` already assigns with `=` and nothing reads
`currentBalance` after `:312`.

## Protocol impact

None. No constant in `.github/protocol-freeze.lock` is read or written, no
message type changes, and a peer cannot observe whether this node emits the
line. `make protocol-freeze` is unaffected.

## Configuration

**No new configuration option, deliberately.** Rule 8 governs tuning constants
that measurably matter; a log line is not one, and a flag would be permanent
surface area for something `/loggers` already controls at runtime.

The existing control is the `all` verbosity on the `node/accounting` logger. Its
costs, which rule 8 asks for in both directions:

- **Raising it** costs this node only. Under sustained concurrent load against
  a slow peer it emits a line per refused request. For scale,
  [per-peer-threshold.md](per-peer-threshold.md) records 311 to 429 refusals
  per sole-source download at the shipped lookahead buffer. It costs other
  nodes nothing: the line is local and no peer can see it.
- **Leaving it low**, the default, costs the diagnosis. There is no other way to
  tell a credit refusal from a missing chunk.

It is a level to raise deliberately and lower afterwards, not to leave on.

## Rollout and rollback

The change is inert until the level is raised, so there is no rollout step for
an operator who does not want it.

**To turn it on**, and the order matters:

1. Let the node run and carry retrieval traffic **first**.
2. `PUT /loggers/bm9kZS9hY2NvdW50aW5n/all`
3. Read the refusal lines from the journal.
4. `PUT /loggers/bm9kZS9hY2NvdW50aW5n/info` to restore.

`bm9kZS9hY2NvdW50aW5n` is base64 of `node/accounting`. Two things make the
obvious call fail, and revision 1 got both wrong:

- **`PUT /loggers/accounting/all` returns 400**, not 404. The handler declares
  `map:"exp,decBase64url"` (`pkg/api/logger.go:115`) and `pkg/api/api.go:330-333`
  implements it as `base64.URLEncoding.DecodeString`, so the literal
  `accounting`, being ten bytes, fails padding.
- **The logger's tree path is `node/accounting`**, not `accounting`
  (`accounting.go:238` registers under the root `node` logger).

Verified on the bench requester: reads back `info`, the PUT returns 200, reads
back `all`, and `info` restores it. No restart.

**Why step 1 comes first, and why this change removes the need for it.** All
eight V(2) registrations in this file today are lazy, on traffic-driven paths.
`SetVerbosityByExp` reaches `SetVerbosity` (`registry.go:148`) for a pattern
match, and `SetVerbosity` for `all` sets a logger to **its own** `v`
(`:118-125`). There is an exact-key fast path at `:134-138`, which does not
apply here because registry keys embed the verbosity and the values
(`logger.go:261-268`), so a bare name never matches one. So `all` applied when only
the V(0) entry exists sets that entry to 0, a V(2) child later cloned from it
inherits 0 (`logger.go:111`, `c := *b.l`), and `0 >= 2` fails at `:180`.
Revision 2 said an entry registered afterwards is simply unaffected, which is
the wrong mechanism for the right conclusion.

Building `loggerV2` in `NewAccounting` removes the trap **for this line**: its
V(2) entry exists from boot, so `SetVerbosity` clamps it to 2. It does not fix
the other eight, which are still built per call, and three of those (`:364`,
`:1208` and `:1309`) are the only log source for the settled balance. So step 1
stays
required even on a build carrying this change, whenever those lines are wanted
too.

**Rollback** is lowering the level. Removing the change entirely is deleting one
log line, one bool, one struct field and one discarded underscore; nothing
persists on disk and no peer state depends on it.

**One consequence worth naming**: with `loggerV2` built at construction, a
`node/accounting` V(2) row appears in `GET /loggers` from boot, where today it
appears only after traffic. That is a visible difference in the response, so
revision 2's flat "no API change" is qualified rather than repeated.

## Upstream portability

`PrepareCredit` and its gate are **unmodified upstream code**, verified: the
function body is byte-identical to `upstream/v2.8.2` at an offset of 12 lines.
So the silent refusal is upstream's behaviour, not this fork's, and Ethersphere
could adopt this change essentially as written. The only fork-specific part is
the surrounding file's line numbers.

**Not tagged `affects-upstream`.** Rule 11 says to tag defects, not preferences,
and a missing diagnostic is a gap rather than a defect. The code does what it
was written to do.

What would justify the tag later, recorded here so the judgement is not made
twice: if the instrumented measurement shows the refusal path can strand a chunk
that is provably retrievable, then returning `ErrNotFound` for a credit refusal
is a defect in its own right, independent of the logging. That belongs to its
own issue, and the tag belongs there rather than here.

## Measurement

A log line is verified by use, not by a benchmark, so rule 7's three-runs
requirement does not apply to the change itself. It applies to the measurement
the line enables, which is #343's and is reported separately.

**Unit tests**, in `pkg/accounting`:

1. A prepared credit that exceeds the limit emits exactly one line at V(2), and
   the emitted `expected_debt` and `overdraft_limit` are the two values actually
   compared.
2. A prepared credit that succeeds emits no such line.
3. With the level below V(2), neither case emits it.
4. `PrepareCredit` returns the same value and error in every case above. This
   makes "no behaviour change" a test rather than a claim.
5. On a call where the settle branch at `:312` fires, `settled_balance` and
   `expected_debt` are **mutually consistent**, that is both come from the
   recomputation at `:319`. Assert the arithmetic, not a post-settle value:
   `expected_debt == max(-settled_balance, 0) + reserved_balance + price +
   surplus_balance`. An earlier draft asked for "the post-settle balance", which
   is not deterministically satisfiable, because `settle()` only dispatches
   goroutines and writes no balance before returning. Such a test would have
   asserted that an injected goroutine happened to win a race.

These need a harness change. Every `NewAccounting` call in `accounting_test.go`
passes `log.Noop`, and so do the three in `provider_test.go`; the package has no
log-capture facility.

The capture logger must set **both** a sink and a verbosity:

```go
log.NewLogger("test", log.WithSink(buf), log.WithVerbosity(log.VerbosityAll)).Build()
```

`WithVerbosity` is an `Option` (`pkg/log/log.go:243`) and `NewLogger` takes
options (`pkg/log/registry.go:62`). The in-tree precedent for the combination is
`pkg/log/asyncsink_test.go:152`, which passes `VerbosityDebug` where this needs
`VerbosityAll`.

**`pkg/retrieval/retrieval_test.go:355` is not the pattern to copy.** It is
`log.NewLogger("test", log.WithSink(buf))`, a sink with no verbosity, so its
logger stays at the default. `VerbosityDebug` is 0, being the fifth value of
`Level(iota - 4)` (`pkg/log/log.go`), and `Debug()` gates on
`verbosity >= l.v` (`logger.go:180`), so `0 >= 2` is false and such a logger
emits nothing at V(2). Tests 1 and 5 would **pass vacuously**.

Revision 2 cited that line *as though* it already carried
`WithVerbosity(log.VerbosityAll)`. The construct it quoted is valid and is what
this section now prescribes; what was invented was the attribution, and the line
actually cited is the one that breaks the tests. Test 3 exists partly to catch
this, and every test must assert on captured content rather than only on
absence.

**On the bench**, as the first use. The requester is where `PrepareCredit` runs
and therefore where the refusal happens.

Assert before recording anything:

- the requester's `thresholdReceived` for the provider, which is the
  `paymentThreshold` the gate uses. `/accounting` also exposes
  `thresholdGiven` and `currentThresholdGiven` there, which are this node's own
  announcements and are not what the gate reads;
- the provider's grant. `providerGrant` is a field on the **granting** node, so
  this is asserted there, either from its `providers-payment-threshold`
  configuration or from its own `thresholdGiven` for the requester, which is the
  `paymentThresholdForPeer` that `provider.go:157` raises. Revision 2 put this
  assertion on the requester, where it cannot be made.

Then confirm refusal lines appear, that `expected_debt` and `overdraft_limit`
bracket the refusal, and that the per-term fields are consistent with
`expected_debt` by the formula above. An inconsistency means the model in this
document is wrong, which is a finding in itself.

**What a negative result looks like**: refusals occur and the logged terms show
neither the settled debt nor `reservedBalance` dominating, for instance if both
sit far below the limit and the surplus balance or the price carries it. That
would mean the model is incomplete and the next step is a wider line, not a
design.

### The refreshment question this line is meant to settle

**Do not restate this from reading.** It has been argued twice on #343 and
withdrawn twice, in opposite directions, and an earlier draft of this spec
restated the withdrawn version a third time.
[overdraft-terms.md](overdraft-terms.md) carries the reconciled account and is
the reference; what follows is only what the instrument needs.

The position there, in short: a completed refreshment changes the headroom by
`amount - refreshRate`, and `amount` is the **peer-accepted** amount, floored
only by `min(allegedInterval * refreshRate, attemptedAmount -
refreshReservedBalance)` (`:1132`, `:1143-1146`). `allegedInterval` comes from
the peer (`pseudosettle.go:324`) and may be zero, and `refreshReservedBalance`
is raised by ordinary traffic (`:518`, `:1241`), so `amount` **can** fall below
`refreshRate`. The mechanism is possible under those preconditions, and none of
them is exposed on `/accounting`.

Note also that `:1106` writes the timestamp **unconditionally, above every
check**, so the below-expectation return at `:1150-1156` advances the timestamp
without crediting. That path is an instance of the mechanism, not a bar to it.

What the instrument contributes: `refresh_timestamp_ms`, `refresh_due` and
`settle_called` are recorded at the refusal, so the question is answered from
a measurement instead of from another reading of the code.

### How this could still mislead

The line reports the terms **as the gate saw them**, under
`accountingPeer.lock`. It does not report what changed them, and two refusals of
the same chunk interleave with other requests to the same peer. So a rising
`reserved_balance` across consecutive lines shows concurrency is present but not
that the failing chunk is its victim. Attributing cause needs the refusals
correlated with the chunk address, which the retrieval side supplies, not this
line.

Recorded because the four withdrawn designs on #343 all failed by treating a
suggestive number as a demonstrated mechanism, and because this document has now
done the same thing twice itself.

## Reporting

Peer overlay addresses appear in this line at runtime. Rule 10 forbids them in
the repository, so anything published from these logs is redacted to `peer A`,
`peer B` and so on, as
[per-peer-threshold-results.md](per-peer-threshold-results.md) already does
after that rule was breached once. The harness redacts both swarm overlays and
libp2p peer identifiers before writing any file.

## Files

- `pkg/accounting/accounting.go`: the log line, `settleCalled`, the captured
  balance, and the `loggerV2` field built in `NewAccounting`.
- `pkg/accounting/overdraft_logging_test.go`: a new file rather than an
  addition to `accounting_test.go`, carrying a capture logger and seven tests.
  The five below, plus one covering a refusal on a peer already in debt so the
  settle branch is entered and the balance term is not zero, and one asserting
  every field of the line is present.
- `docs/DIFFERENCES.md`: a row, because the change adds a log line and a
  `GET /loggers` row that Bee does not have. Not because it is the first fork
  change to `pkg/accounting`, which it is not.
- `docs/experiments/content-providers/operators.md`: the procedure above.

Generated with help of AI.
