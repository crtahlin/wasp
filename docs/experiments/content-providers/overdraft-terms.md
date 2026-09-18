# Which accounting terms move during a failing download

Issue: [#343](https://github.com/crtahlin/wasp/issues/343). Harnesses `t15.sh`
and `t15i.sh`, outside this repository. Companion to
[truncation-cause.md](truncation-cause.md) and
[retrieval-rate.md](retrieval-rate.md).

**Exploratory.** This measurement answers a prior question and explicitly does
not answer the main one. What it cannot do is stated before the numbers rather
than after.

## Why this run exists

Four designs have been withdrawn on #343, each because a remedy was chosen
before the mechanism was measured. The deciding question is which term of

```
increasedExpectedDebt  >  paymentThreshold + refreshDue
```

moves during a failing chunk's life. The per-refusal answer needs the log line
specified in [overdraft-observability.md](overdraft-observability.md), which is
not implemented yet.

One prior question needs no code. `/accounting` already exposes
`reservedBalance` and `shadowReservedBalance` per peer
(`pkg/api/accounting.go:30-31`), so whether `reservedBalance` moves **at all**
can be sampled today.

## Terms, including one that is easy to invert

- **`paymentThreshold`** is what the **peer announced**, documented at
  `pkg/accounting/accounting.go:137` as "the threshold at which the peer expects
  us to pay", and exposed as `thresholdReceived`. It is **not** this node's own
  `payment-threshold` setting. Confusing the two has already produced one wrong
  analysis in this project, and an earlier draft of this document did it again.
- **The gate** is `paymentThreshold + refreshDue`, exposed whole as
  `currentThresholdReceived` (`accounting.go:765`).
- **`refreshRate`** is 4,500,000 per second (`pkg/node/node.go:236`).
- **What the gate actually compares**, from `getIncreasedExpectedDebt`
  (`accounting.go:256-279`):

  ```
  increasedExpectedDebt = max(-balance, 0) + reservedBalance + price + surplusBalance
  ```

  **`shadowReservedBalance` is not in it.** It is subtracted only at `:306` to
  form the quantity that decides whether `settle()` fires (`:312`). The shadow
  figures below therefore bear on when settlement triggers, not on whether a
  request is refused, and should not be read as pressure on the gate.

Measured on the requester during these runs, the provider announced
`thresholdReceived` = **13,500,000**, so the gate was **18,000,000**. Both are
read values, not assumptions.

## Conditions

Requester and provider on the bench, requester running `0.1.3-324v2-dev`.

The provider grant was asserted zero at run time, read from the provider's
configuration rather than assumed: a grant raises `paymentThresholdForPeer` on
the provider (`pkg/accounting/provider.go:156-157`), which the requester stores
as `paymentThreshold`, so a nonzero grant would move the very term this run
holds fixed. The harness refuses to record rows otherwise.

Sole-source content, 4,194,304 bytes, hinted at the provider,
`Swarm-Cache: false`. `/accounting` sampled every 50 ms on the requester.

Twelve runs: six with the arms in sequence, then six with the arms interleaved.
The second set exists because the first confounded arm order with node warmth,
and the tables below show that mattered.

## Pre-registered predictions

Written before the run, so it can fail:

1. With the lookahead buffer disabled the reader takes one chunk at a time, so
   `reservedBalance` should stay at or below roughly one chunk price.
2. With the default lookahead it prefetches, so if concurrency matters
   `reservedBalance` should reach a large multiple of that.
3. If **both** arms hold `reservedBalance` near zero, concurrency is not the
   mover and the settled debt is.

## Result

**Sequential order (t15): all three of one arm, then all three of the other.**

| Arm | Run | `reservedBalance` peak | `shadowReserved` peak | Balance floor | Body bytes | Elapsed | curl |
|---|---|---|---|---|---|---|---|
| lookahead 0 | 1 | 2,530,000 | 5,660,000 | -13,500,000 | 262,144 | 3.33 s | 18 |
| lookahead 0 | 2 | 2,550,000 | 5,620,000 | -13,480,000 | 557,056 | 9.89 s | 18 |
| lookahead 0 | 3 | 2,530,000 | 8,740,000 | -13,480,000 | 720,896 | 12.79 s | 18 |
| lookahead default | 1 | 12,810,000 | 8,730,000 | -13,490,000 | 0 | 2.47 s | 18 |
| lookahead default | 2 | 12,860,000 | 4,690,000 | -13,490,000 | 0 | 2.54 s | 18 |
| lookahead default | 3 | 12,860,000 | 4,680,000 | -13,480,000 | 0 | 2.55 s | 18 |

**Interleaved order (t15i): arms alternated.**

| Arm | Run | `reservedBalance` peak | `shadowReserved` peak | Balance floor | Body bytes | Elapsed | curl |
|---|---|---|---|---|---|---|---|
| lookahead 0 | 1 | 2,530,000 | 6,240,000 | -13,480,000 | 196,608 | 3.21 s | 18 |
| lookahead default | 1 | 12,880,000 | 4,710,000 | -13,500,000 | 0 | 1.94 s | 18 |
| lookahead 0 | 2 | 2,530,000 | 6,240,000 | -13,500,000 | 196,608 | 2.92 s | 18 |
| lookahead default | 2 | 12,780,000 | 4,680,000 | -13,400,000 | 0 | 1.97 s | 18 |
| lookahead 0 | 3 | 2,530,000 | 6,240,000 | -13,470,000 | 196,608 | 3.00 s | 18 |
| lookahead default | 3 | 12,860,000 | 4,710,000 | -13,480,000 | 0 | 1.92 s | 18 |

All twelve truncated, `curl` exit 18, against a 4,194,304 byte file. The settled
balance reached the announced threshold in every one of the twelve.

## Interleaving changed one column and not the other

**Body bytes in the sequential lookahead-0 arm rise steadily: 262,144, then
557,056, then 720,896.** Interleaved, the same arm gives 196,608 three times.
That trend was an artefact of arm order against a provider that had restarted
shortly before, not a property of the arm. Reporting the sequential figures
alone would have published a spread of nearly 3x that does not exist.

**The reserved peak was unaffected.** Its spread within an arm is 0.8% or less
in the sequential set and 0.0% to 0.8% interleaved. It is much the more stable
observable of the two, and a future run should prefer it.

The interleaved set is the one to cite.

## Prediction 1 is refuted

Disabling the lookahead buffer does not hold `reservedBalance` near one chunk
price. `joiner.ReadAt` declares `var eg errgroup.Group`
(`pkg/file/joiner/joiner.go:215`) with no `SetLimit` anywhere in the file, so a
single read unit fans out concurrently whatever the lookahead setting is.
Disabling the prefetch removes one layer of concurrency, not the concurrency.

Dividing the peak by the measured mean chunk price of about 306,735
(`measurement.md:429-434`) suggests roughly eight chunks in flight rather than
one. **That conversion is an estimate only**: as `measurement.md` says where the
price was taken, the metric counts credit decisions node-wide including relayed
retrievals, not deliveries from one peer, and a node-wide mean cannot be divided
into a per-peer quantity without that caveat. The reserved balance is itself per
peer and measured directly, so the refutation does not rest on the conversion.

Three merged documents asserted the refuted claim and are corrected in the same
change as this one: [overdraft-retry.md](overdraft-retry.md),
[overdraft-retry-results.md](overdraft-retry-results.md) and
[per-peer-threshold.md](per-peer-threshold.md).

## Prediction 2 holds

The default lookahead raises the peak about five times, 12,860,000 against
2,530,000, and that factor is the difference between delivering part of the file
and delivering none of it.

## Prediction 3 does not arise

## The settled debt is not a steady state either

An earlier reading of this data called the settled debt saturated and therefore
constant. The per-sample values say otherwise: within a single lookahead-0 run
the balance ranges from **-300,000 to -13,480,000**. That is the refresh cycle,
consume toward the threshold, refreshment pays down, consume again.

Both terms move, on the same timescale.

## What this run does NOT establish

An earlier draft reconstructed `increasedExpectedDebt` by adding the peak
`reservedBalance` to the deepest balance, and concluded the arithmetic did not
close on the lookahead-0 arm.

**That reasoning is withdrawn.** The two figures come from different samples.
Adding two extremes that never co-occurred reconstructs nothing, and the
conclusion drawn from it was unsupported.

Since both terms move together, only their values at the same instant decide a
refusal, and sampling cannot supply that:

- a refusal resolving inside one interval is invisible, and sampling faster
  perturbs the node;
- `/accounting` has no event semantics, so no sample is attributable to the
  refusal it happened to sit beside.

The per-refusal breakdown has to be taken under the same lock as the
comparison. That is what #353 is for.

## A fifth candidate mechanism, recorded and not acted on

A completed refreshment sets `refreshTimestampMilliseconds` to now, so
`timeElapsedInSeconds` becomes 0, `refreshDue` falls from 4,500,000 to 0, and
the gate tightens from 18,000,000 to 13,500,000 for a second. If the debt has
not been paid down in that same instant, the gate is at its most hostile
immediately after a refreshment succeeds.

It fits this data. So did the four withdrawn designs, at the point they were
written. No design follows from it here.

## Two limits of this measurement

**Sampling.** At 50 ms a refusal resolving faster than one sample is invisible,
so a peak is a lower bound on what the gate actually saw.

**`/accounting` entry counts are not connection counts.** The response carried
292 entries, but `PeerAccounting` iterates `a.accountingPeers`
(`accounting.go:722-730`), which retains a record for every peer seen rather
than every peer currently connected. The connected count for this bench and
build is 113 to 118 (`measurement.md:423`). An earlier draft of the #353 spec
quoted 292 as a peer count and called the sourced figure unsourced; that was
wrong in both directions.

Generated with help of AI.
