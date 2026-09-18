# Which accounting terms move during a failing download

Issue: [#343](https://github.com/crtahlin/wasp/issues/343). Harnesses `t15.sh`
and `t15i.sh`, outside this repository. Companion to
[truncation-cause.md](truncation-cause.md) and
[retrieval-rate.md](retrieval-rate.md).

**Exploratory.** This measurement answers a prior question and explicitly does
not answer the main one. What it cannot do is stated before the numbers.

**This is revision 3.** Revision 1 was reviewed and found to carry seventeen
defects. Revision 2 fixed them and, in doing so, replaced one wrong claim about
the refreshment mechanism with a different wrong claim in the opposite
direction, which a second review caught. Both are kept and labelled in the
section concerned, because a claim that has now been wrong in both directions is
more informative than either version of it.

## Why this run exists

Four designs have been withdrawn on #343, each because a remedy was chosen
before the mechanism was measured. The deciding question is which term of

```
increasedExpectedDebt  >  paymentThreshold + refreshDue
```

moves during a failing chunk's life. The per-refusal answer needs a log line at
the refusal itself, specified in
[#353](https://github.com/crtahlin/wasp/issues/353) and not yet merged.

One prior question needs no code. `/accounting` already exposes
`reservedBalance` and `shadowReservedBalance` per peer
(`pkg/api/accounting.go:30-31`), so whether `reservedBalance` moves **at all**
can be sampled today.

## Terms, including two that are easy to invert

- **`paymentThreshold`** is what the **peer announced**, documented at
  `pkg/accounting/accounting.go:137` as "the threshold at which the peer expects
  us to pay", exposed as `thresholdReceived`. It is **not** this node's own
  `payment-threshold` setting.
- **The gate** is `paymentThreshold + refreshDue`, exposed whole as
  `currentThresholdReceived` (`accounting.go:765`). Because
  `timeElapsedInSeconds` is `min((now - ts)/1000, 1)` (`:325`), `refreshDue` is
  either 0 or one `refreshRate`, so **the gate is either 13,500,000 or
  18,000,000 on this bench, not a single number.** 18,000,000 is its ceiling.
  Revision 1 called 18,000,000 "the gate" and claimed it was read rather than
  derived. `currentThresholdReceived` was read as 18,000,000 **after** the runs;
  it was not sampled during them, so which of the two values was in force at any
  refusal is not known here.
- **What the gate compares**, from `getIncreasedExpectedDebt`
  (`accounting.go:256-279`):

  ```
  increasedExpectedDebt = max(-balance, 0) + reservedBalance + price + surplusBalance
  ```

  `shadowReservedBalance` is **not** in it. It is subtracted at `:306` to decide
  whether `settle()` fires, and also at `:386` and `:429`, and it feeds
  `peerDebt` (`:883`), `peerLatentDebt` (`:911-912`) and `shadowBalance`
  (`:948-952`). Revision 1 said "subtracted only at `:306`", which is wrong
  even though the conclusion it supported is right.
- **`balance` in that formula is the raw stored balance** (`:259`). The
  `/accounting` field named `balance` is the stored balance **minus** surplus
  (`:768`); the raw value is exposed as `consumedBalance` (`:769`). On this bench
  `surplusBalance` read 0 after the runs, so the two coincide and the surplus
  term is zero, but that was checked afterwards rather than sampled throughout.

## Correction: the arm called "lookahead default" is not the default

From [gate-terms-measured.md](gate-terms-measured.md). Both harnesses sent
`Swarm-Lookahead-Buffer-Size: 524288`, which is `largeFileBufferSize`
(`pkg/api/bzz.go:52`). A 4,194,304 byte file is below the 10,000,000 threshold
in `lookaheadBufferSize` (`:59-64`) and therefore selects
`smallFileBufferSize`, **262,144**. So the arm labelled "lookahead default"
throughout this document, in both result tables and in the sentence beginning
"The default lookahead raises the peak", is a **doubled** buffer and not the
shipped one. No run in this document or in the #353 measurement exercises the
default.

The comparison itself stands, since it is between 0 and 524,288 in both. **The
read-unit numbers derived from the wrong label do not.** 524,288 is
**128 chunks**, not the 64 this document computes for 262,144 at the line
beginning "At the shipped `smallFileBufferSize`". So:

- the read units in the measured arms differ by a factor of **16**, not 8;
- and with the langos double-buffering this document invokes, in-flight leaves
  reach up to **32 times** buffer 0's, not 16.

Both figures in the paragraph headed "A discrepancy this raises and does not
settle" are therefore understated twofold, and the 64-chunk unit described there
belongs to a buffer no run used. The direction of that paragraph survives and
strengthens: the gap between read-unit ratio and measured peak ratio is wider
than stated, not narrower.

The wrong label also reaches prediction 2 and the heading "Prediction 2 holds",
not only the two result tables and the sentence beginning "The default lookahead
raises the peak".

## Conditions

Taken **2026-09-18**, the sequential set between about 10:08 and 10:18 UTC and
the interleaved set between about 10:21 and 10:31 UTC.

- **Block**: SWAP. Both nodes have `swap-enable: true`, so debt can be cleared
  by cheque as well as by refreshment. Figures here are not comparable with
  pseudosettle-block figures elsewhere in this directory.
- **Both nodes are full nodes**, so no light-node division applies.
- **Requester** `0.1.3-324v2-dev`, which carries #324's retry-on-overdraft
  change. That alters how often a refused chunk is re-attempted, and therefore
  the request pattern the reserved balance is produced by. It is this fork's
  behaviour, not upstream's.
- **Provider** `0.1.3-f005605d`, with the provider grant asserted zero at run
  time, read from its configuration rather than assumed: a grant raises
  `paymentThresholdForPeer` on the provider (`provider.go:156-157`), which the
  requester stores as `paymentThreshold`, so a nonzero grant would move the term
  this run holds fixed. The harness refuses to record rows otherwise.
- **The provider was restarted shortly before the sequential set** and not
  again, so run for run the interleaved set met a provider that had been up
  about 13 minutes longer.
- **The same content in all twelve runs**, one sole-source reference,
  4,194,304 bytes, hinted at the provider, `Swarm-Cache: false`.
- **Balances were not reset between runs**, which are spaced 90 s apart. Debt
  and refresh allowance carry across runs, and the within-run balance range
  below depends on that.
- `/accounting` polled every 50 ms on the requester, with **the provider's entry
  isolated by overlay** from the `peerData` map. Every figure below is that one
  peer's, not a node-wide aggregate.
- **Only four fields were recorded** from each response: `balance`,
  `reservedBalance`, `shadowReservedBalance` and `thresholdReceived`.
  `currentThresholdReceived` is in the same response and was **not** recorded,
  which is why `refreshDue` at any instant is unknown below. That is a gap in
  the harness, not in the endpoint.
- `thresholdReceived` read 13,500,000 whenever it was checked, but it is
  mutable: `NotifyPaymentThreshold` (`accounting.go:1010`) sets it whenever the
  peer announces, and the growth path (`:663-669`) makes peers do that. It was
  not asserted constant across the runs.

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

All twelve truncated, `curl` exit 18, against a 4,194,304 byte file.

**The balance floor reached the announced 13,500,000 exactly in three of the
twelve runs, and came within 0.07 to 0.74 per cent of it in the other nine.**
Revision 1 said "reached the announced threshold in every one of the twelve",
which the table above contradicts.

## Interleaving changed one column and not the other

Body bytes in the sequential lookahead-0 arm rise 262,144, then 557,056, then
720,896. Interleaved, the same arm gives 196,608 three times.

The provider had restarted shortly before the sequential set, so arm order and
provider uptime are confounded there and separated by interleaving. **That the
trend disappears when the arms alternate is measured; that provider warmth
caused it is inference**, and no counter was recorded that would show warmth
directly.

The reserved peak was unaffected: spread within an arm is 0.79% and 0.39%
sequential, 0.0% and 0.78% interleaved. It is much the more stable observable,
and a future run should prefer it. The interleaved set is the one to cite.

## Prediction 1 is refuted, and the exact figure is available

Disabling the lookahead buffer does not hold `reservedBalance` near one chunk
price. It sits about eight times higher.

The read unit says why. [retrieval-rate.md](retrieval-rate.md) already records
it at `:86`, "at buffer 0 a read unit is 8 leaves", so this is a citation rather
than a new derivation. The path: with `Swarm-Lookahead-Buffer-Size: 0` the
handler skips langos and passes the reader straight to `http.ServeContent`
(`pkg/api/bzz.go:824-828`), and the copy buffer is 32 KiB, which is **8 chunks**
of 4,096. At the shipped `smallFileBufferSize = 8 * 32 * 1024 = 262,144`
(`bzz.go:51`) the unit is **64 chunks**. `joiner.ReadAt` declares
`var eg errgroup.Group` (`pkg/file/joiner/joiner.go:215`) with no `SetLimit`
anywhere in the file, so a read unit fans out concurrently however large it is.

**The 32 KiB depends on a wrapper, which is worth stating because it could
change.** `http.ServeContent` copies with `io.CopyN`, and net/http's own
`(*response).ReadFrom` path would prepend a 512-byte content-sniff read and
misalign every subsequent read. It does not apply here: every API response is
wrapped by `responseWriter` (`pkg/api/metrics.go:136-142`), which embeds only
`UpgradedResponseWriter` and so does not satisfy `io.ReaderFrom`. The copy
therefore takes `io.Copy`'s plain 32 KiB path and stays chunk aligned.

The measurement agrees independently: **every lookahead-0 body-byte figure is an
exact multiple of 32,768** (262,144, 557,056, 720,896, 196,608). That is the
strongest evidence for the read unit here and does not depend on reading the
standard library at all.

So turning the prefetch off reduces the read unit eightfold; it does not reduce
it to one chunk.

**A discrepancy this raises and does not settle.** The read units differ by a
factor of 8, the measured peaks by 5.08. The gap is **wider** than that, not
narrower: `retrieval-rate.md:93-96` records that langos fetches the next buffer
while the current one is read, so at the shipped buffer the leaves in flight can
be up to twice the per-unit figure, putting the expected ratio as high as 16. A
peak is also a lower bound at 50 ms sampling, and the default-lookahead runs are
the shorter ones, so their peaks are the more likely to be understated. None of
that is measured here and the gap is left open.

For scale only: the measured chunk price is 306,735 in run 1 of three, with
306,454 and 309,141 in the others (`measurement.md:435-437`), about 307,443
across the three. Note 2,530,000 divided by that is 8.23 rather than 8, which is
expected since the per-chunk price varies with proximity, and is a reason the
read unit rather than the price is the basis used above. Revision 1 cited
`measurement.md:429-434`, which holds caveat text and a table header but not the
number, and presented run 1 as "the measured mean".

## Prediction 2 holds

The default lookahead raises the peak from 2,530,000 to 12,860,000, a factor of
5.08, and that is the difference between delivering part of the file and
delivering none of it.

## Prediction 3 does not arise

It was conditional on both arms holding `reservedBalance` near zero. Neither
does, so the condition is not met and the prediction is neither confirmed nor
refuted.

## The settled debt is not a steady state either

An earlier reading of this data called the settled debt saturated and therefore
constant. The per-sample values say otherwise. In the interleaved lookahead-0
runs the balance ranges from about -300,000 to the floor listed above, within a
single run. That is the refresh cycle, consume toward the threshold, refreshment
pays down, consume again, and it depends on the carry-over noted under
Conditions.

Both terms move, on the same timescale.

## What this run does NOT establish

An earlier draft reconstructed `increasedExpectedDebt` by adding the peak
`reservedBalance` to the deepest balance, and concluded the arithmetic did not
close on the lookahead-0 arm.

**That reasoning is withdrawn.** The two figures come from different samples.
Adding two extremes that never co-occurred reconstructs nothing.

Since both terms move together, only their values at the same instant decide a
refusal, and sampling cannot supply that:

- a refusal resolving inside one interval is invisible, and sampling faster
  perturbs the node;
- `/accounting` has no event semantics, so no sample is attributable to the
  refusal it happened to sit beside;
- `refreshDue` at the binding instant was not sampled at all, so it is not known
  whether the gate was at 13,500,000 or 18,000,000 when any refusal occurred.

The per-refusal breakdown has to be taken under the same lock as the
comparison. That is what #353 is for.

## A candidate mechanism: possible, conditional, and not measured

This section has now been written three times and been wrong twice, in opposite
directions. Both wrong versions are stated before the current one, because the
oscillation is more informative than any of the three.

**Revision 1 claimed** a completed refreshment tightens the gate: it sets
`refreshTimestampMilliseconds` to now, so `refreshDue` drops from `refreshRate`
to zero, while the debt falls by the credited `amount`. Writing headroom as
limit minus debt, the change is `amount - refreshRate`.

**Revision 2 claimed the code forbids that**, on the grounds that a refreshment
is only attempted at or above one `refreshRate` (`:470`) and that one accepted
below expectation is rejected before crediting (`:1150-1156`). **That refutation
was unsound and is withdrawn.**

What is actually true:

- `:470` and `:473` bound the **attempted** amount and the **local** elapsed
  time. What credits the balance at `:1167` is `amount`, the amount the peer
  accepted.
- Its only floor is
  `expectedAllowance = min(allegedInterval * refreshRate, attemptedAmount - refreshReservedBalance)`
  (`:1132`, `:1143-1146`). `allegedInterval` is
  `paymentAck.Timestamp - lastTime.Timestamp` (`pseudosettle.go:324`), taken
  from **the peer's** timestamps. Negative is rejected; **zero is not**, and at
  zero the floor is zero, so an `amount` of zero passes `:1150` with no error
  and no blocklist.
- `refreshReservedBalance` (incremented at `:518` and `:1241`) lowers
  `checkAllowance` by the same route.
- And `:1106` writes the timestamp **unconditionally**, above every check. So on
  the below-expectation path at `:1150-1156` the timestamp has already advanced
  when the function returns without crediting. Revision 2 cited that path as
  forbidding the mechanism; it is an instance of it. The peer is blocklisted
  there (`:1154`), which limits the consequence rather than preventing it.

So `amount` can be less than `refreshRate`, and when it is, headroom shrinks.
**The mechanism is possible.**

What does work against it, and revision 2 missed: on every **error** path
pseudosettle passes `timestamp = 0` (`pseudosettle.go:276, 285, 303, 310, 319,
327, 334, 340, 350`), so `:1106` sets the timestamp to zero,
`min((now - 0)/1000, 1)` saturates at 1, and the gate sits at its **ceiling**.

Said carefully, because this section has twice been wrong about direction: a
refreshment is only attempted once `:473` has found more than 999 ms elapsed, so
`refreshDue` was **already** at `refreshRate` beforehand. A failure keeps it
there. So a failed refreshment **leaves the gate at its ceiling** rather than
tightening it; it is looser only by comparison with the successful outcome, not
looser than before. And nothing resets `refreshTimestampMilliseconds` except
`:1106`, so the ceiling then persists until a refreshment succeeds.

That counterweight is also less useful than it looks: an error other than
`p2p.ErrPeerNotFound` blocklists the peer (`:1122-1125`), as the
below-expectation path does, so both of the paths that leave the gate at its
ceiling also end the connection.

**None of the conditions that decide this were measured here.** `allegedInterval`,
`refreshReservedBalance` and the accepted `amount` are not exposed on
`/accounting` and were not sampled. So this stays a possibility with stated
preconditions, and no design follows from it.

That is four withdrawn designs on this issue plus two withdrawn analyses of this
one mechanism, in opposite directions, every one derived by reading code
carefully. The lesson is not about any single fact: this gate has enough
interacting terms that plausibility is worth nothing here, and only a
measurement at the point of refusal will do.

## Limits of this measurement

**Sampling.** At 50 ms a refusal resolving faster than one sample is invisible,
so every peak is a lower bound.

**`/accounting` entry counts are not connection counts.** The response carried
292 entries, but `PeerAccounting` iterates `a.accountingPeers`
(`accounting.go:722-730`), which retains a record for every peer seen rather
than every peer connected. A connected count was **not** recorded for these
runs. `measurement.md:424` gives 113 to 118 for this bench and build, but from a
session dated 2026-09-17, so it describes a different set of runs.

Generated with help of AI.
