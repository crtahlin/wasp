# Accruing the refresh allowance continuously

Issue: [#359](https://github.com/crtahlin/wasp/issues/359). Evidence:
[gate-terms-measured.md](gate-terms-measured.md), merged as
[`03160761`](https://github.com/crtahlin/wasp/commit/03160761). Instrument:
[#353](https://github.com/crtahlin/wasp/issues/353).

Code references are to `origin/main` at `1b89c35a`, base `upstream/v2.8.2`.
`pkg/accounting` is **not** unmodified: it carries 1,196 inserted and 11 deleted
lines against that base, from #327 and #353. The gate itself is unmodified
upstream code; `git diff` the function before relying on any line number here.

## Problem

`refreshDue`, the allowance this node grants itself above the threshold a peer
announced, is computed from an integer division:

```go
// pkg/accounting/accounting.go:356
timeElapsedInSeconds := min((a.timeNow().UnixMilli()-accountingPeer.refreshTimestampMilliseconds)/1000, 1)
refreshDue := new(big.Int).Mul(big.NewInt(timeElapsedInSeconds), a.refreshRate)
```

So it is 0 for the first 1,000 ms after the refresh timestamp is written, then
steps to a full `refreshRate`. The allowance is granted as a cliff, although
`refreshRate` is documented as a rate, "accounting units refreshed per second"
(`pkg/node/node.go:236`).

**Measured**: of 2,381 refusals read at the gate under its own lock, **2,251
(94.5 per cent) occurred with `refreshDue` at zero**, and every one of them
would have passed at the ceiling. The largest `expected_debt` among them is
13,800,000 against a ceiling of 18,000,000.

## Hypothesis

**Not** that removing the cliff delivers more bytes. That is the open question,
and the measurement is explicit that it cannot answer it: the gated sum sits
against whatever limit is in force, so raising the limit may simply let the sum
rise to meet it.

The hypothesis is narrower and testable: **if the cliff is what refuses those
2,251 requests, then accruing the allowance continuously moves them, and the
question is whether the bytes follow.** Three outcomes are possible and all are
reportable:

1. refusals fall and delivered bytes rise, so the cliff was costing throughput;
2. refusals fall and delivered bytes do not move, so the sum re-equilibrated and
   the change achieves nothing;
3. refusals do not fall, so the model in `gate-terms-measured.md` is wrong.

Outcome 2 is the one this spec expects to have to report, and it is why the
acceptance criterion below is bytes rather than refusal count.

## The constraint that shapes the design

**The step is not an accident. It keeps both sides of the connection in
lockstep, and one side accruing continuously breaks that.**

This node's spending limit is `paymentThreshold + refreshDue` (`:359`). The peer
deciding whether to keep serving us uses `disconnectLimit + refreshDue`
(`:1425`, enforced at `:1427`), where `disconnectLimit = percentOf(100 + paymentTolerance,
paymentThresholdForPeer)` (`:741`) and `payment-tolerance-percent` defaults to
25 (`cmd/bee/cmd/cmd.go:390`). Both sides compute their elapsed term from the
same refreshment event with the same integer step, ours from
`refreshTimestampMilliseconds` (`:356`) and theirs from
`refreshReceivedTimestamp` (`:1416`).

So with a 13,500,000 threshold the two move together and the margin is constant:

| elapsed | we allow ourselves | a stock peer tolerates | margin |
|---|---|---|---|
| under 1 s | 13,500,000 | 16,875,000 | 3,375,000 |
| 1 s or more | 18,000,000 | 21,375,000 | 3,375,000 |

The margin is exactly `paymentTolerance` of the threshold, at every instant.

Accrue continuously on our side alone and that invariant goes:

| elapsed | we would allow | stock peer tolerates | |
|---|---|---|---|
| 0.50 s | 15,750,000 | 16,875,000 | within |
| 0.75 s | 16,875,000 | 16,875,000 | exactly at the limit |
| 0.90 s | 17,550,000 | 16,875,000 | **over by 675,000** |
| 0.99 s | 17,955,000 | 16,875,000 | **over by 1,080,000** |

**Crossover at 0.750 of a second.** For the last 250 ms of every second a
continuously-accruing node can push a stock peer past its disconnect limit,
which blocklists us (`:1427-1431`). That is not a wire change, but it is a
behaviour change visible to an unmodified peer, and it is the kind of thing rule
6 exists for.

This also explains the cliff: the step is what makes both sides agree on the
allowance at every instant.

## Design

**Accrue continuously, but never past what a stock peer tolerates.**

```go
elapsedMillis := a.timeNow().UnixMilli() - accountingPeer.refreshTimestampMilliseconds
if elapsedMillis < 0 {
        elapsedMillis = 0          // clock stepped backwards
}
if elapsedMillis > 1000 {
        elapsedMillis = 1000       // the existing one second cap
}
refreshDue := new(big.Int).Mul(a.refreshRate, big.NewInt(elapsedMillis))
refreshDue.Div(refreshDue, big.NewInt(1000))

// Never accrue past what a peer running stock Bee will tolerate before it
// blocklists us. Its limit is its own disconnect threshold plus its own
// refreshDue, and its refreshDue is still a step, so inside the first second
// the only headroom we can rely on is its tolerance.
if cap := a.safeAccrualCap(accountingPeer); refreshDue.Cmp(cap) > 0 {
        refreshDue.Set(cap)
}
```

`safeAccrualCap` is `percentOf(a.paymentTolerance, accountingPeer.paymentThreshold)`,
that is the peer's own stated tolerance applied to the threshold it announced.
We do not know the peer's `payment-tolerance-percent`, so this uses ours as a
proxy, which is the shipped default on both sides. **This is the weakest point
in the design and the measurement must check it**, see below.

With the defaults that caps accrual at 3,375,000 of the 4,500,000, reached at
0.75 s, and the limit then holds flat until the step would have fired anyway.
So the change buys the first three quarters of each second and gives up the
last quarter, which is precisely the part that is unsafe.

### Considered and dropped

**Not resetting the timestamp when a refreshment credits less than the
allowance its reset costs.** The measurement shows the sum falling by exactly
`refreshRate` at all three observed step-downs, so no shortfall was observed and
the case this would protect against did not occur. Recorded because it is not
refuted, only unmotivated.

**Changing both sides symmetrically.** That restores the invariant exactly and
is the clean fix, but it only helps between two wasp nodes, and a wasp node
talking to stock Bee is the normal case. It would also need the mixed-version
test from [mixed-version.md](mixed-version.md) rerun. Out of scope here, and
worth its own issue if the capped form measures well.

## Configuration

`refresh-allowance-accrual`, a string, default **`step`**, which is exactly
current behaviour. The other value is `continuous`.

- **Setting it to `continuous`** costs this node nothing directly and may cost
  it a blocklisting if the safe cap is wrong for a given peer, since exceeding a
  peer's disconnect limit is what triggers one. It costs **other nodes** a
  larger unsecured debt outstanding inside the first second of each refresh
  cycle, up to the tolerance they already permit. It does not let this node owe
  more than the ceiling it can already reach today.
- **Leaving it at `step`** costs the 94.5 per cent of refusals measured at the
  floor, if those refusals turn out to cost bytes. Whether they do is what this
  experiment measures.

Per rule 8 the default is the current value, so a node that does not set it
behaves exactly as it does today.

## Protocol impact

**No frozen surface is touched.** `.github/protocol-freeze.lock` fingerprints
protocol versions, the handshake, chunk format and network ID; none is read or
written here. No message type changes and no field is added to any protobuf.
`make protocol-freeze` is unaffected.

**But it is peer-visible behaviour**, and that is stated here rather than
hidden behind the absence of a wire change. A node with this on sends more
requests inside the first second of a refresh cycle than stock Bee would. The
safe cap above is what keeps that inside the peer's existing tolerance. If the
cap is wrong the peer blocklists us, which is why the measurement makes
blocklisting a reject condition rather than a metric.

## Measurement

Requester and provider on the bench, sole-source content, both lookahead
settings, three runs per arm interleaved. Arms: `step` (control) against
`continuous`.

**Acceptance is delivered bytes.** A change that halves refusals while
delivering the same bytes has moved the steady state and achieved nothing, and
must be reported as a negative result.

Recorded per run:

- delivered bytes and `curl` exit, the primary observable;
- refusals from #353's log line, with `refresh_due`, so the distribution across
  floor and ceiling can be compared with the 94.5 per cent baseline;
- **the provider's `/blocklist` before and after**, and its
  `AccountingDisconnectsReconnectCount`. Any blocklisting of the requester by
  the provider is a **reject condition**, not a data point;
- the peer's `thresholdGiven` and `currentThresholdGiven`, to confirm the
  tolerance assumption the safe cap rests on.

**What a negative result looks like**: refusals fall substantially, delivered
bytes do not move outside the spread of the control arm. That is outcome 2 and
it is the expected one. It would mean the cliff is real, measured and not worth
removing, and that is a publishable result that closes #359 rather than a
failure.

**What would invalidate the run**: any blocklist event; a provider grant other
than zero; the two arms running against different node states, which
[overdraft-terms.md](overdraft-terms.md) showed can move body bytes threefold on
its own.

## Rollout and rollback

Off by default. An operator turns it on with
`refresh-allowance-accrual: continuous` and a restart, and back off by removing
the line and restarting. Nothing persists on disk, no migration, and no peer
state depends on it.

A node that is blocklisted while it is on recovers by the normal blocklist
expiry; turning the setting back off prevents recurrence.

## Upstream portability

The gate is unmodified upstream code, so the change applies to Bee directly and
the cap keeps it compatible with unmodified peers. #359 carries
`affects-upstream` because the behaviour is measured and reproduced rather than
reasoned about, with both readings of the defect question recorded there for a
human to settle.

Related and already tagged: [#316](https://github.com/crtahlin/wasp/issues/316),
where the same quantity is computed a second way in `settle` **without** the one
second cap. If this lands, the two computations should be made to agree, and
that is a reason to keep them in one helper rather than two expressions.

## Files

- `pkg/accounting/accounting.go`: the elapsed term, the safe cap helper, and the
  service field holding the setting.
- `pkg/accounting/accounting_test.go` or a new file: the accrual at several
  elapsed values, the cap binding at 0.75 s with default tolerance, the
  backwards-clock clamp, and that `step` reproduces current behaviour exactly.
- `cmd/bee/cmd/cmd.go` and `pkg/node/node.go`: the setting.
- `docs/DIFFERENCES.md`: a row, since this changes what a node does.
- `docs/experiments/content-providers/`: the results document.

Generated with help of AI.
