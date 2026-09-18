# Making an overdraft refusal visible

Issue: [#353](https://github.com/crtahlin/wasp/issues/353). Exists to unblock
one measurement on [#343](https://github.com/crtahlin/wasp/issues/343), recorded
in [retrieval-rate.md](retrieval-rate.md) and
[truncation-cause.md](truncation-cause.md).

**This is revision 2.** Revision 1 was reviewed and refused. Its central claim,
set in bold, was that `pkg/accounting` is byte-identical to `upstream/v2.8.2`
and that this change would be the first fork modification to it. That is false.
It was produced by running

```
git diff upstream/v2.8.2 main -- pkg/accounting
```

in a worktree whose local `main` ref was stale at `de136880` while `origin/main`
was at `a561b5a5`. The command returned empty, and the empty result was reported
as verification.

This is the same failure that produced the four withdrawn designs on #343: a
check that looked like verification and was run against the wrong thing. It is
recorded here rather than quietly corrected, because a document whose purpose is
to make a measurement trustworthy has no business hiding its own retraction.

Five further defects from the same review are corrected below and marked where
they appear.

## What the base actually is

Code references are to `origin/main` at `a561b5a5`, base `upstream/v2.8.2`.

`pkg/accounting` is **not** unmodified. Against that base it carries 782 lines
of fork changes from [#327](https://github.com/crtahlin/wasp/issues/327):

| File | Change |
|---|---|
| `provider.go` | new, 237 lines, the per-peer provider grant |
| `provider_test.go` | new, 466 lines |
| `accounting.go` | 65 lines: `providerGrant` on the peer, `providerThreshold`, `providerBudget` and `providerBudgetUsed` on the service, grant release in `Connect` and `terminate` |
| `metrics.go` | 23 lines: the three provider grant counters |

Two consequences the refused revision missed.

**The gate itself is unmodified upstream code.** The body of `PrepareCredit`
carries no fork change, so the mechanism this document describes is upstream's.
But the **line numbers are not upstream's**: the sites cited below as `:285`,
`:325`, `:332-335` and `:1106` are at `273`, `313`, `321-322` and `1094` in
`upstream/v2.8.2`. A reader following these citations into the upstream tree
lands in the wrong place unless they translate.

**`paymentThreshold` is not static in this fork.** The document previously set
it aside as changing "only when the peer announces a new one", which is true and
misleading. `provider.go:18` is explicit that the threshold a **provider**
announces is what the requester stores as `paymentThreshold`, and
`provider.go:156-157` raises `paymentThresholdForPeer` mid-connection when a
grant is admitted. So with the provider feature on, the term this analysis
treats as fixed moves, on the requester side, which is the side `PrepareCredit`
runs on during a download.

The measurement below therefore **asserts the provider grant is zero** rather
than assuming it, and the harness refuses to record rows otherwise.

## Terms

- **Overdraft refusal**: `PrepareCredit` returning `ErrOverdraft`
  (`pkg/accounting/accounting.go:334`) because the debt a request would create
  exceeds what the peer currently allows.
- **Settled debt**: the stored per-peer balance. Negative means this node owes
  the peer.
- **Reserved balance**: `reservedBalance`, charges for requests this node has
  started and not yet completed. It rises when a request is prepared and falls
  when it is applied or released.
- **Shadow reserved balance**: `shadowReservedBalance`, charges the peer may
  already have applied on its side but that this node has not yet confirmed.
- **Refresh timestamp**: `refreshTimestampMilliseconds`. See below, because it
  does not mean what its name suggests.
- **V(2)**: the verbosity that the `all` level enables and plain `debug` does
  not (`pkg/log/registry.go:118-125`).

## The refresh timestamp does not record a completed refreshment

Revision 1 defined it as "written only when a refreshment completes" and built
a `refresh_age_ms` field on that. Both were wrong.

```go
// pkg/accounting/accounting.go:1097  "called by pseudosettle when refreshment is done or failed"
accountingPeer.refreshTimestampMilliseconds = timestamp   // :1106
if receivedError != nil {                                 // :1109
```

The assignment is **unconditional and sits above the error branch**. Nine of the
ten callers in `pkg/settlement/pseudosettle/pseudosettle.go` (`:276, 285, 303,
310, 319, 327, 334, 340, 350`) pass `timestamp = 0`; only `:358`, the success
path, passes a real time.

So a **failed** refreshment sets the timestamp to zero, and so does a peer that
has never refreshed. An age computed from it would read about 1.79e12
milliseconds, the time since the Unix epoch, in precisely the states worth
distinguishing.

**The field is therefore replaced.** The log line records the raw
`refreshTimestampMilliseconds` and the saturated `timeElapsedInSeconds` that the
gate actually used. Zero is then unambiguous and means "never refreshed, or the
last refreshment failed", and the age is recoverable by subtraction when the
value is not zero.

The conclusion that `refreshDue` is pinned at its cap survives, but by a
different route than revision 1 gave: a zero timestamp saturates the `min` just
as an old one does.

One further caveat. `min((now - ts)/1000, 1)` caps the top and not the bottom,
so a backwards clock step yields a negative `timeElapsedInSeconds`, a negative
`refreshDue`, and an `overdraftLimit` **below** `paymentThreshold`. Unlikely,
but "exactly two values" was stated as fact in revision 1 and is not one.

## Problem

The refusal is silent:

```go
// pkg/accounting/accounting.go:332-335
if increasedExpectedDebt.Cmp(overdraftLimit) > 0 {
	a.metrics.AccountingBlocksCount.Inc()
	return nil, ErrOverdraft
}
```

`AccountingBlocksCount` is an unlabelled counter (`metrics.go:110-115`), with no
peer and no amounts. No log line is written at any level.

The callers add little. `preferred.go:229-235` is:

```go
if err != nil {
	skip.Add(chunkAddr, peer, overDraftRefresh)
	if errors.Is(err, accounting.ErrOverdraft) {
		s.metrics.PreferredOverdrafts.Inc()
	}
	return err
}
```

The `skip.Add` is how a refusal becomes retrieval behaviour, and note it fires
for **every** error and tags all of them with `overDraftRefresh`, whether or not
the error was an overdraft. Only the counter is conditional. Revision 1
described this as "increments a counter and returns", which hid the part that
matters.

`retrieval.go:360-364` likewise records a skip and retries.

So a chunk refused for credit and a chunk that is genuinely gone both surface as
`storage.ErrNotFound`. The lock-contention path at `:285` **does** log, at plain
Debug, so a chunk delayed by lock contention is diagnosable and a chunk refused
for credit is not. That asymmetry is the whole of this document.

## What has to be measured

#343 has had four designs withdrawn, each because a remedy was chosen before the
mechanism was measured. The deciding question is which term moves:

```
increasedExpectedDebt  >  paymentThreshold + refreshDue
```

`refreshDue` is pinned at its cap, for the reasons above. `paymentThreshold`
moves only through a provider grant, which the measurement holds at zero. That
leaves the settled debt and `reservedBalance`, and **the two answers imply
opposite remedies**:

- if `reservedBalance` is the mover, the refusals are self-inflicted by this
  node's own concurrency, spacing retries out is exactly wrong, and more
  attempts beat fewer;
- if a completed refreshment is what releases a chunk, the remedy concerns
  triggering settlement, and because `settle()` is called from inside
  `PrepareCredit` (`:313`), fewer attempts again means fewer chances to start
  one.

### What polling has already established, and why it is not enough

`/accounting` exposes `reservedBalance` and `shadowReservedBalance` per peer, so
the prior question, whether `reservedBalance` moves at all, needs no code. On
the bench, sampling it every 50 ms during downloads that truncate
(`curl` exit 18) gives a peak `reservedBalance` against the provider of about
2.53 to 2.55 million.

Two things follow.

**It moves**, which refutes the pre-registered prediction that disabling the
lookahead buffer would hold it near one chunk price. It does not, because
`joiner.ReadAt` uses an unlimited errgroup, so a single read unit fans out
concurrently whatever the lookahead setting is. Any design resting on
`reservedBalance` being negligible is dead.

**It is not sufficient.** Against a 13,500,000 threshold, 2.55 million is under
a fifth of the gate. `reservedBalance` alone cannot push
`increasedExpectedDebt` over the limit, so the settled debt has to be carrying
most of it, and the split between them is what decides the remedy.

Polling cannot supply that split. It has no event semantics, so no sample can be
attributed to the refusal it coincided with, and a refusal resolving inside one
50 ms sample is invisible. The per-refusal breakdown needs the log line.

## Design

One V(2) Debug line at the refusal site, under the existing `accounting` logger.

```go
loggerV2.Debug("credit refused, would overdraw",
	"peer_address", peer,
	"price", bigPrice,
	"expected_debt", increasedExpectedDebt,
	"overdraft_limit", overdraftLimit,
	"payment_threshold", accountingPeer.paymentThreshold,
	"refresh_due", refreshDue,
	"refresh_timestamp_ms", accountingPeer.refreshTimestampMilliseconds,
	"elapsed_seconds", timeElapsedInSeconds,
	"settled_balance", currentBalance,
	"reserved_balance", accountingPeer.reservedBalance,
	"shadow_reserved_balance", accountingPeer.shadowReservedBalance,
	"settle_triggered", settleTriggered,
)
```

### What this requires that does not exist yet

Revision 1 printed this block as though it dropped in. It does not. Eight of the
names are already in scope at `:332`: `bigPrice` (`:295`),
`increasedExpectedDebt`, `overdraftLimit`, `refreshDue`, `timeElapsedInSeconds`,
`accountingPeer.paymentThreshold`, `.reservedBalance` and
`.shadowReservedBalance`. `currentBalance` is in scope from `:300`. The rest
have to be introduced:

- **`settleTriggered`**, a `bool` declared before `:312` and set inside that
  branch. It does not exist in the tree.
- **`loggerV2`**. `PrepareCredit` has none. The eight existing V(2)
  registrations in this file (`:348, 959, 1017, 1184, 1221, 1254, 1478, 1507`)
  each build one per call, and `logger.V(2)` forces the full `Build()` path
  (clone, join, allocate, flatten, hash with two reflect calls, `sync.Map`
  load) **whether or not the level is enabled**. `PrepareCredit` runs once per
  chunk per peer attempt, which is the hottest accounting path there is, so
  this one is built **once in `NewAccounting`** and stored on the service.
  Revision 1 claimed the disabled cost is "a comparison and a return"; that
  describes `Debug()`, not `V(2)`, and the claim is withdrawn.

### The supporting change, correctly described

At `:319` the post-settle recomputation discards its second return value:

```go
increasedExpectedDebt, _, err = a.getIncreasedExpectedDebt(peer, accountingPeer, bigPrice)
```

Revision 1 said this discarded value "is the balance the gate then compares
against". **That is false.** The gate compares `increasedExpectedDebt` against
`overdraftLimit`; `currentBalance` is not an operand of it, and is read only at
`:312`, before the recomputation.

The real reason to capture it is narrower and still sufficient: without it the
logged `settled_balance` is the **pre-settle** balance, so on exactly the calls
where settlement ran, the line would report a debt that no longer exists. The
capture is behaviour-neutral, because `:319` already assigns with `=` and
nothing reads `currentBalance` after `:312`.

### Not in this change

- **No config option.** Rule 8 governs tuning constants that measurably matter;
  a log line is not one.
- **No peer label on `AccountingBlocksCount`.** The bench requester carried 292
  peers in `/accounting` during the polling run above, which is poor cardinality
  for a Prometheus label, and the log line already carries the peer. (Revision 1
  said "about 120" without a source.)
- **No API change and no wire change.** `.github/protocol-freeze.lock` is
  untouched.
- **No behaviour change.** The gate, the comparison and both return values are
  unchanged. This is asserted by a test, not claimed.

## Raising the level, which is not what it looks like

`PUT /loggers/accounting/all` returns **400**, not 404 as first assumed, and for
two separate reasons.

**The `exp` segment is base64, not a URL path.** The handler declares
`map:"exp,decBase64url"` (`pkg/api/logger.go:115`) and `pkg/api/api.go:330-333`
implements it as `base64.URLEncoding.DecodeString`. The literal `accounting` is
ten bytes and fails padding, so `mapStructure` takes the 400 branch.

**The logger's tree path is `node/accounting`, not `accounting`**
(`accounting.go:238` registers under the root `node` logger).

So the call is:

```
PUT /loggers/bm9kZS9hY2NvdW50aW5n/all      # base64("node/accounting")
```

Verified on the bench requester: reads back `info`, PUT returns 200, reads back
`all`, and `info` restores it. No restart.

**Order matters, and it is the opposite of what revision 1 implied.** All V(2)
registrations in this file are lazy, on traffic-driven paths, and a V(2) logger
is a separate registry entry keyed by its verbosity. So no `node/accounting`
V(2) entry exists at boot, and raising verbosity **before** the first credit has
no effect on an entry registered afterwards. The node must be warm and carrying
traffic first, then the level raised. Revision 1 offered the runtime raise as a
way to avoid waiting for the peer table to warm, which is backwards and would
have wasted a bench run.

Building `loggerV2` once in `NewAccounting`, as the design requires for cost
reasons, also removes this trap, because the entry then exists from startup.

## Protocol impact

None. No constant in `.github/protocol-freeze.lock` is read or written, no
message type changes, and a peer cannot observe whether this node emits the
line.

## Verification

A log line is verified by use, not by a benchmark, so rule 7's three-runs
requirement does not apply to the change itself. It does apply to the
measurement the line exists to enable, which is #343's and is reported
separately.

**Unit tests**, in `pkg/accounting`:

1. A prepared credit that exceeds the limit emits exactly one line at V(2), and
   the emitted `expected_debt` and `overdraft_limit` are the two values actually
   compared.
2. A prepared credit that succeeds emits no such line.
3. With the level below V(2), neither case emits it.
4. `PrepareCredit` returns the same value and error in every case above. This is
   what makes "no behaviour change" a test rather than a claim.
5. On a call where the settle branch at `:312` fires, the logged
   `settled_balance` is the post-settle balance, not the pre-settle one.

These need a harness change the refused revision did not mention. Every
`NewAccounting` call in `accounting_test.go` passes `log.Noop`, and the package
has no log-capture facility. The pattern to copy is
`pkg/retrieval/retrieval_test.go:355`:
`log.NewLogger("test", log.WithSink(buf), log.WithVerbosity(log.VerbosityAll))`.

One trap to avoid: `VerbosityDebug` is 0 and `Debug()` gates on
`verbosity >= l.v` (`pkg/log/logger.go:180`), so a capture logger left at
default verbosity emits nothing and tests 1 and 5 would **pass vacuously**. Test
3 exists partly to catch that, and the tests must assert on captured content,
never only on absence.

**On the bench**, as the first use: warm the requester, then raise
`node/accounting` to `all` on it. The requester is where `PrepareCredit` runs
and therefore where the refusal happens, not the provider. Assert the provider
grant is zero, request sole-source content held by a single provider, and
confirm refusal lines appear, that `expected_debt` and `overdraft_limit` bracket
the refusal, and that the per-term fields are consistent with `expected_debt`.
An inconsistency there means the model in this document is wrong, which is a
finding in itself.

### How this could still mislead

The line reports the terms **as the gate saw them**, under
`accountingPeer.lock`. It does not report what changed them, and two refusals of
the same chunk can interleave with other requests to the same peer. So a rising
`reserved_balance` across consecutive lines shows concurrency is present but
does not by itself show the failing chunk is the victim of it. Attributing cause
needs the refusals correlated with the chunk address, which the retrieval side
supplies, not this line.

Recording this because the four withdrawn designs on #343 all failed by treating
a suggestive number as a demonstrated mechanism, and because this document has
now done the same thing once itself.

## Reporting

Peer overlay addresses appear in this line at runtime. Rule 10 forbids them in
the repository, so anything published from these logs is redacted to `peer A`,
`peer B` and so on, as
[per-peer-threshold-results.md](per-peer-threshold-results.md) already does
after that rule was breached once.

## Files

- `pkg/accounting/accounting.go`, the log line, `settleTriggered`, the captured
  balance, and `loggerV2` built in `NewAccounting`.
- `pkg/accounting/accounting_test.go`, the five tests and the capture logger.
- `docs/DIFFERENCES.md`, a row. Not because this is the first fork change to
  `pkg/accounting`, which it is not, but because it adds a log line that is not
  in Bee.
- `docs/experiments/content-providers/operators.md`, the note on warming first,
  raising the level, and lowering it afterwards.

Generated with help of AI.
