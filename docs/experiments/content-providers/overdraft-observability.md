# Making an overdraft refusal visible

Issue: [#353](https://github.com/crtahlin/wasp/issues/353). Exists to unblock
one measurement on [#343](https://github.com/crtahlin/wasp/issues/343), recorded
in [retrieval-rate.md](retrieval-rate.md) and
[truncation-cause.md](truncation-cause.md).

Code references are to `main` at `a561b5a5`, base `upstream/v2.8.2`.
`pkg/accounting` is byte-identical to that base, verified with
`git diff upstream/v2.8.2 main -- pkg/accounting`, which is empty. So every line
cited below is also upstream's, **and this change is the first fork
modification to that package.**

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
- **Refresh age**: milliseconds since `refreshTimestampMilliseconds`, which is
  written only when a refreshment **completes**
  (`NotifyRefreshmentSent`, `:1106`).
- **V(2)**: the verbosity that `PUT /loggers/<name>/all` enables and plain
  `debug` does not (`pkg/api/logger.go:61-64`).

## Problem

The refusal is silent:

```go
// pkg/accounting/accounting.go:332-335
if increasedExpectedDebt.Cmp(overdraftLimit) > 0 {
	a.metrics.AccountingBlocksCount.Inc()
	return nil, ErrOverdraft
}
```

`AccountingBlocksCount` carries no peer label and no amounts. No log line is
written, at any level. The callers add nothing: `preferred.go:228-234`
increments a counter and returns, `retrieval.go:360-364` records a skip and
retries. A chunk refused for credit and a chunk that is genuinely gone both
surface as `storage.ErrNotFound`.

The lock-contention path two screens up **does** log, at plain Debug (`:285`),
so a chunk delayed by lock contention can be diagnosed and a chunk refused for
credit cannot. That asymmetry is the whole of this document.

## What has to be measured, and why nothing existing does it

#343 has had four designs withdrawn, each because a remedy was chosen before
the mechanism was measured. The deciding question is which term moves:

```
increasedExpectedDebt  >  paymentThreshold + refreshDue
```

Two of the four terms are already known not to move on a chunk's timescale.
`timeElapsedInSeconds` (`:325`) is integer division capped at 1, so `refreshDue`
takes exactly two values and steps once, at one second; and since
`refreshTimestampMilliseconds` is written only on a completed refreshment, it is
normally already past that cap. `paymentThreshold` changes only when the peer
announces a new one.

That leaves the settled debt and `reservedBalance`, and **the two answers imply
opposite remedies**:

- if `reservedBalance` is the mover, the refusals are self-inflicted by this
  node's own concurrency, spacing retries out is exactly wrong, and more
  attempts beat fewer;
- if a completed refreshment is what releases a chunk, the remedy concerns
  triggering settlement, and because `settle()` is called from inside
  `PrepareCredit` (`:313`), fewer attempts again means fewer chances to start
  one.

Existing observability cannot separate them:

| Quantity | API | Metric | Log |
|---|---|---|---|
| settled balance | `/accounting`, `/balances` | aggregate only | yes, V(2) at `:364`, `:1208`, `:1309` |
| `reservedBalance` | `/accounting` | no | never |
| `shadowReservedBalance` | `/accounting` | no | never |
| `refreshTimestampMilliseconds` | no | no | never |

`/accounting` is a snapshot with no event semantics. Refusals resolve well
inside a second, so a poll cannot be attributed to a particular chunk, and by
the time a poll returns the reserved balance has already moved.
`currentThresholdReceived` is no help either: it folds the refresh term through
the same saturating `min(..., 1)` (`:761-765`), so it carries no timing signal.

## Design

One V(2) Debug line at the refusal site, under the existing `accounting`
logger, naming every term that feeds the comparison:

```go
loggerV2.Debug("credit refused, would overdraw",
	"peer_address", peer,
	"price", bigPrice,
	"expected_debt", increasedExpectedDebt,
	"overdraft_limit", overdraftLimit,
	"payment_threshold", accountingPeer.paymentThreshold,
	"refresh_due", refreshDue,
	"refresh_age_ms", refreshAgeMilliseconds,
	"settled_balance", currentBalance,
	"reserved_balance", accountingPeer.reservedBalance,
	"shadow_reserved_balance", accountingPeer.shadowReservedBalance,
	"settle_triggered", settleTriggered,
)
```

Three fields are doing work that no rearrangement of existing data could do.

**`settle_triggered`** records whether the branch at `:312` fired on this call.
It is the field the #343 question turns on, it is not derivable from anything
else, and it separates "settlement ran and the request was still refused" from
"settlement never ran".

**`refresh_age_ms`** is the true age, not the saturated
`timeElapsedInSeconds`. The saturated value is `1` almost always and says
nothing; the raw age says whether a refreshment has completed recently, which is
the other half of the same question.

**`settled_balance`** needs one supporting change. At `:319` the post-settle
recomputation discards its second return value:

```go
increasedExpectedDebt, _, err = a.getIncreasedExpectedDebt(peer, accountingPeer, bigPrice)
```

That discarded value is the balance the gate then compares against. It has to be
captured, or the logged balance is the pre-settle one and is wrong exactly on
the calls that matter most.

### What is deliberately not in this change

- **No config option.** Rule 8 governs tuning constants that measurably matter;
  a log line is not one. V(2) costs nothing when it is not enabled, and
  `PUT /loggers/accounting/all` raises it at runtime with no restart, which
  matters on a bench node whose peer table takes minutes to warm.
- **No peer label on `AccountingBlocksCount`.** The bench node carries about 120
  peers, which is poor cardinality for a Prometheus label, and the log line
  already carries the peer.
- **No API change and no wire change.** `.github/protocol-freeze.lock` is
  untouched.
- **No behaviour change.** The gate, the comparison and both return values are
  unchanged. This is the property the tests assert.

## Protocol impact

None. No constant in `.github/protocol-freeze.lock` is read or written, no
message type changes, and a peer cannot observe whether this node emits the
line.

## Cost

A `logger.V(2).Register()` call already happens on this path's sibling
(`:348`, `:1017`, `:1254`); the refusal path adds one. When the level is not
enabled the call is a comparison and a return. The line is emitted only on
refusal, which is by construction the rare case, and on the bench a failing
download produces a few hundred over its life.

The one real cost: with `accounting` at `all`, a node under sustained
concurrent load against a slow peer can emit a line per refused request, so
this is a diagnostic level to turn on deliberately and turn off afterwards, not
to leave on. Stated in the operator note added to
[operators.md](operators.md).

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
4. `PrepareCredit` returns the same value and error in every case above,
   compared against the behaviour before the change. This is what makes "no
   behaviour change" a test rather than a claim.
5. On a call where the settle branch at `:312` fires, the logged
   `settled_balance` is the post-settle balance, not the pre-settle one. This
   is the supporting change, and without this test it is unasserted.

**On the bench**, as the first use: raise `accounting` to `all` on the
requester, request sole-source content held by a single provider with the
provider grant off, and confirm refusal lines appear, that their
`expected_debt` and `overdraft_limit` bracket the refusal, and that the per-term
fields are consistent with `expected_debt`. An inconsistency there means the
model in this document is wrong, which is a finding in itself.

### How this could still mislead

The line reports the terms **as the gate saw them**, under
`accountingPeer.lock`. It does not report what changed them, and two refusals
of the same chunk can interleave with other requests to the same peer. So a
rising `reserved_balance` across consecutive lines shows concurrency is present
but does not by itself show the failing chunk is the victim of it. Attributing
cause needs the refusals correlated with the chunk address, which the retrieval
side supplies, not this line.

Recording this because the four withdrawn designs on #343 all failed by
treating a suggestive number as a demonstrated mechanism.

## Reporting

Peer overlay addresses appear in this line at runtime. Rule 10 forbids them in
the repository, so anything published from these logs is redacted to
`peer A`, `peer B` and so on, as
[per-peer-threshold-results.md](per-peer-threshold-results.md) already does
after that rule was breached once.

## Files

- `pkg/accounting/accounting.go`, the log line and the captured balance.
- `pkg/accounting/accounting_test.go`, the five tests.
- `docs/DIFFERENCES.md`, a row, since this is the first fork change to
  `pkg/accounting`.
- `docs/experiments/content-providers/operators.md`, the note on turning the
  level on and off.

Generated with help of AI.
