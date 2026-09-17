# Cheque acceptance cost: results

Measured on 2026-09-16 on the two node bench, against the method in
[`spec.md`](spec.md). The change under test is
[`a89a3a83`](https://github.com/crtahlin/wasp/commit/a89a3a83), which takes the
chain calls off the path that accepts a cheque
([#301](https://github.com/crtahlin/wasp/issues/301),
[#302](https://github.com/crtahlin/wasp/issues/302)).

## Result

**The change does what it claims, and the spec's acceptance rule cannot be
evaluated as written.** Three findings of different strength, which should not
be blurred together.

1. **The mechanism is proven.** The patched node avoids about 93% of the chain
   reads it used to make, 87% to 95% per run, 153 avoided against 12 made across
   six runs. With each chequebook chain call slowed to about 0.55 s, a stock
   node took a median of 1.788 s to accept a cheque, against 0.362 s unslowed.
   That rise of about 1.4 s is three delayed calls, which is what
   [#300](https://github.com/crtahlin/wasp/issues/300) says a stock node makes.
2. **When chain calls are slow, the change is worth about 42%** of the time it
   takes to accept a cheque, 1.788 s against 1.045 s. The samples are thin, and
   the sizes are given with every figure below rather than buried.
3. **When chain calls are fast, the accept time does not move beyond the
   spread.** Nor does the rate at which cheques are accepted from the single
   paying peer this bench had, which is not the spec's node-wide figure. The
   floor did move: across 39 cheques a stock node never accepted one in under
   0.184 s, and a patched one did it in 0.031 s in five runs out of six.

### What a chain call actually costs, and where the first draft went wrong

An earlier draft of this document claimed the three calls "cannot have been on
that path" during the main comparison, and blamed a drifting endpoint. **Both
claims were wrong, and the data in this document refutes them.**

- The calls were on the path. Slowing each one by 0.5 s raised the accept time
  by about 1.4 s against 1.5 s injected. Two calls would add about 1.0 s to the
  rise and four about 2.0 s, so the count is three. The source agrees: in
  `upstream/v2.8.2:pkg/settlement/swap/chequebook/chequestore.go` the issuer,
  balance and paid out reads are made one after another, on every cheque, with
  nothing conditional about them.
- The endpoint's median did not drift during the comparison. It was probed 36
  times per build, and the median was 0.099 s for one and 0.107 s for the other.
  Single probes did vary, from 0.093 s to 0.283 s, so the steadiness is in the
  medians, not in every call.
- What was wrong is the probe, not the node. The probe makes a separate request
  per call, so each pays for setting up a connection. The node most likely
  reuses one connection instead, which is how its chain client is built, but
  that was inferred here and not measured; the check is to probe once with a
  reused connection and once without, or to count the node's connections to the
  endpoint. **The probe overstates what the node pays.** A lower bound comes
  from the floors in this document: 0.184 s for stock against 0.031 s for
  patched is 0.153 s for three calls, so **at least about 0.05 s each at the
  fastest moment observed**. Both floors are best cases, so the usual cost lies
  between that and the 0.10 s the probe reports, and this data does not say
  where.

The lesson for the next run of this experiment is to time the node's own calls,
through its own counters, rather than a separate probe standing beside it.

### The spec's rule, quoted in full

> **A negative result** is condition 4 showing no shortening of the accept time
> beyond the spread, or no rise in cheques accepted per second node-wide. Either
> would mean the chain calls are not what they appear to cost, and the change
> would be reverted rather than kept.

Taking that clause by clause:

- **The first figure was measured and did not move.** On a fast endpoint the
  accept time did not shorten beyond the spread.
- **The second figure was never measured at all.** The spec's node-wide figure
  is "with three peers paying the provider at once". That never happened, for
  the reason under "What is still not measured". Table 1's cheques per second is
  the rate from a single paying peer, and using it for the spec's node-wide
  figure would be substituting one quantity for another. A figure that was not
  measured did not fail to move.
- **The consequence clause does not hold up.** The rule says either outcome
  would mean the chain calls "are not what they appear to cost". That is true,
  but not in the direction the rule assumes. The calls are not free and are not
  negligible; they cost at least about 0.05 s each on this endpoint, somewhere
  between that and the 0.10 s the probe suggested, and about 0.55 s each on a
  slow one, where removing them is worth 42%. The rule reads "not what they
  appear to cost" as "cheap, so this does not matter". The measurement says
  "cheaper here than assumed, dearer elsewhere, and the benefit tracks it".

So one of the two conditions failed, one was never evaluated, and the inference
the rule draws from failure does not follow. **The rule cannot be applied as
written.**

**Recommendation: keep the change**, on findings 1 and 2 together with the
absence of any measured harm, and **amend the criterion** so that it states what
a chain call costs at the time of measurement and judges the change against
that. Amending a criterion is not the same as meeting it, and this document does
not claim the original was met. The operator decided on 2026-09-16 to keep it,
on resource use rather than on the gain the spec claimed. Two things were
accepted: a 30 second window in which a drained chequebook can pass, itself a
widening of a gap that stays open until a cheque is cashed, and, permanently, a
wrong but well-formed cached issuer that would block that chequebook for good
where upstream recovers on its own. The criterion was amended in the spec at the
same time and marked as amended after the measurement, and the node-wide gain
the spec claimed remains unproven and open as
[#312](https://github.com/crtahlin/wasp/issues/312). The ledger records this as
having no measurable effect in normal operation rather than as validated,
because that is what the main comparison showed.

## Setup

- **P**: the node that receives cheques, on bench-1. **Q**, the node that pays,
  on bench-2. About 30 ms added between them, as in [content
  providers](../content-providers/measurement.md).
- Stock build and patched build differ only by `a89a3a83`. Both were confirmed
  by the strings they contain, because a version stamp can be set by a build
  flag, and Go's own `vcs.revision` reported the wrong commit when building from
  a linked git worktree. The patched build was confirmed again at run time by
  the two counters it adds, which the stock build does not expose.
- Each run uploads a fresh 16 MiB file on P without erasure coding, then Q
  downloads it with a hint to P, with Q's accounting, swap and pseudosettle
  logging at debug.
- **The figure** is the time from Q's "sending cheque message to peer" to Q's
  "registering payment sent", filtered to P. It is a round trip and includes the
  network, so it is an upper bound on what the change can affect.

### Where this departs from the spec

- **Conditions 3 and 4 are not separable.** #301 and #302 landed in one commit,
  so the build under test carries both. Only condition 4 exists here.
- **P side timing was not possible.** The only debug line on the cheque path is
  on the sending side. A receiving node logs nothing around handling a cheque,
  so the receiver's own work cannot be timed without adding a log line to the
  node, which needs its own issue and merged spec.
- **The node-wide figure was never exercised.** See "What is still not measured"
  below.

## The main comparison, on the normal endpoint

Six cycles, alternating the two builds within each cycle. Every arm was
restarted, then given the same wait of at least 300 s and at least 100 peers.
Every arm probed the chain endpoint three times before and three times after its
run.

**Table 1: accepting a cheque, stock against patched, normal endpoint**

| | Stock | Patched |
|---|---|---|
| Runs | 6 | 6 |
| Cheques timed | 39 | 56 |
| Median | 0.362 s | 0.304 s |
| Range over all cheques | 0.184 to 1.493 s | 0.031 to 1.858 s |
| Per run medians | 0.228, 0.236, 0.323, 0.464, 0.697, 0.865 | 0.117, 0.273, 0.282, 0.302, 0.420, 0.867 |
| Per run minima | 0.184, 0.191, 0.192, 0.209, 0.216, 0.216 | 0.031, 0.031, 0.031, 0.031, 0.031, 0.207 |
| Cheques accepted per second, per run | 0.57, 0.67, 0.86, 0.86, 1.00, 1.11 (median 0.86) | 0.71, 0.75, 0.89, 1.17, 1.18, 1.29 (median 1.03) |
| Chunks served per second, per run | 31.1, 31.5, 35.1, 37.9, 38.6, 40.2 (median 36.5) | 31.6, 32.8, 34.7, 37.6, 38.5, 43.9 (median 36.2) |
| Chain call probe, 36 per build | 0.099 s median, 0.093 to 0.162 | 0.107 s median, 0.095 to 0.283 |

**What this shows:**
- **The medians overlap and so does the throughput.** Neither difference
  survives six runs. Cycles 1 to 3 favoured the patched build and cycles 4 to 6
  did not. The cheques per second ranges, 0.57 to 1.11 against 0.71 to 1.29,
  overlap across most of their width.
- **The floor does not overlap.** Across 39 cheques a stock node never went
  below 0.184 s. A patched one reached 0.031 s in five runs out of six. A stock
  node cannot go below the cost of its chain calls; a patched one can, and
  repeatedly does. This is the one place in the normal-endpoint data where the
  two builds are cleanly distinguishable.
- **The nodes were doing the same work.** Chunks served per second are the same
  to within noise, so the difference is not that one build was busier.

## The stress test

The main comparison could not resolve the effect because a chain call was cheap.
Rather than wait for a slow endpoint, the bench made one: an endpoint that
delays only the calls naming a paying node's chequebook, by 0.5 s, and forwards
everything else at full speed. A stock node then pays about 1.65 s of chain work
for each cheque. A patched node does not make those calls on the hot path.

**This is a stress test.** It answers whether removing these calls matters when
they are the bottleneck. It does not say how much the change helps a node whose
endpoint is fast. During cycles 1 to 3 the endpoint delayed 24 calls and passed
676 through untouched, so the node's other chain work was not affected; the
counter was not captured for cycles 4 to 6.

**Table 2: accepting a cheque, chequebook chain calls slowed to about 0.55 s**

| | Stock | Patched |
|---|---|---|
| Runs with a timed cheque | 5 of 6 | 4 of 6 |
| Cheques timed | 12 | 9 |
| Median | 1.788 s | 1.045 s |
| Per run medians, with sample size | 0.663 (n=1), 1.799 (n=1), 1.783 (n=2), 1.785 (n=4), 1.798 (n=4) | 1.236 (n=1), 0.615 (n=2), 0.626 (n=3), 1.045 (n=3) |
| Range over all cheques | 0.663 to 2.089 s | 0.031 to 1.236 s |

**What this shows, and what it does not:**
- **Three calls, confirmed.** Stock's eleven cheques outside run 1 span 1.771 to
  2.089 s. Two delayed calls would put the total accept time near 1.1 s and four
  near 2.2 s, so the count is three. This is the strongest thing in the
  document, and it agrees with reading the source.
- **The two builds separate, on thin samples.** Patched's slowest run median,
  1.236 s, is below stock's fastest run of more than one cheque, 1.783 s. That
  1.236 s figure is a **single cheque**, so the separation should be read as
  consistent rather than established: patched's other slow values, 1.200, 1.183
  and 1.180, sit in the same place, which is what makes it consistent.
- **Two of six patched runs produced no timed cheque at all**: and in one run a
  cheque was sent whose payment never registered within the window.
- **The patched build does not reach its floor here** because it still reads the
  chequebook's balance and paid out total once per 30 s, which is two delayed
  calls. Its residual cost is that reading, not per cheque work.
- **Throughput did not separate.** Both arms completed only a few cheques per
  run, so neither was demand limited, and no claim about cheques per second can
  be made from this series.

## A fast endpoint, without any code change

Before the code was tested, the spec asked for a condition that makes the chain
calls cheap without changing the node: a proxy in front of the endpoint that
answers the repeated chequebook calls from a short lived cache.

Both arms of this pair were measured in blocks rather than alternating, which is
the design fault recorded under "Corrections". Both arms are the **same build**,
which removes one half of that fault, a drifting bench between one build and the
other. It does not remove the other half: between the two arms the endpoint was
torn down, the node's configuration restored, two binaries installed, the node
restarted three times and another series run, and the control arm was measured
on a node restarted minutes earlier, which is the slower state. That confound
pushes the same way as the reported difference, so this pair does not clear it.
Read Table 3 as a pointer, not as a result.

**Table 3: accepting a cheque, stock build, caching endpoint**

| | Stock, normal endpoint | Stock, caching endpoint |
|---|---|---|
| Cheques timed | 26 | 34 |
| Median | 0.252 s | 0.213 s |
| Range | 0.182 to 0.861 s | 0.113 to 0.882 s |

Calls through the caching endpoint took 0.053 to 0.055 s against about 0.10 s
direct, by the same probe, though not inflated by the same amount: a cached call
is answered on the loopback interface and pays almost nothing to set up, while
the direct call pays in full, so the caching endpoint's real advantage is
smaller than those two numbers suggest. The result points the same way as the
code change and is smaller, which is expected: the cache served only one of the
calls made per cheque, so it understates what removing all of them is worth.

## What is still not measured

- **Contention, at a realistic load.** This is the gap that matters most,
  because the node-wide mutex is the whole point of #300, and it is the spec's
  second acceptance figure. A second paying peer was built for it: a light node
  on bench-1, with its own chequebook, pulling from P throughout each measured
  run. It worked, and it was not enough. Total cheque traffic reached about 1
  per second, of which the second peer contributed about 0.2, against a stock
  ceiling of roughly 3.3 per second, so nothing ever queued. Saturating that
  ceiling honestly would need on the order of twenty paying peers. The stress
  test above lowers the ceiling instead of raising the load, which tests the
  mechanism but not the contention.
- **What a receiving node spends on its own.** Not available without adding a
  log line or a histogram to the cheque path.
- **Cheques accepted per second under saturation.** Both arms completed only a
  few cheques per run, so neither was demand limited.

## Corrections to the method

Recorded because each one produced a wrong number first, and a reader should
know which numbers were discarded. They are listed whichever way the error
pointed.

- **Running the two builds in blocks rather than alternating them was wrong.**
  The first attempt measured all of one build, then all of the other. It
  reported the patched build at 0.662 s against 0.252 s for stock, which reads
  as a large regression. Repeating the patched block gave 0.424 s, straddling
  stock, so the regression did not reproduce. The bench drifts between blocks:
  download times moved between 5.6 s and 13.4 s, and the time to settle after a
  restart between 60 s and 300 s. Alternating the builds inside each cycle
  removed it. **The blocked stock-against-patched numbers are discarded**, which
  does not extend to Table 3 in the same way, where both arms are the same
  build; the part of the fault that does still apply there is set out in that
  section.
- **An early stress figure of about 2x was superseded and is not the result.**
  The first three stress cycles gave stock 1.794 s against patched 0.903 s.
  Three more cycles moved it to 1.788 s against 1.045 s, about 42%. The larger
  figure flattered the change and is recorded here for that reason.
- **The first caching endpoint was slower than no endpoint at all.** It opened a
  fresh connection for every call it forwarded, so most of the node's chain
  traffic paid to set one up, and accepting a cheque came out at 0.409 s, worse
  than stock. It also matched only calls made to the chequebook, missing the
  balance call, which is made on another contract with the chequebook as an
  argument. Both were fixed, with pooled connections and a wider match. **Those
  numbers are discarded.**
- **Timings were once computed without filtering to the peer.** The diagnostic
  that replaced the first one captured the whole log rather than the lines
  naming P, so cheques to every other peer were paired with each other. It
  produced medians of 0.000 s. Filtering restored the real figures.
- **An earlier claim that other peers' cheque traffic caused the drift was
  wrong.** It was checked afterwards: in eleven of the twelve uncontended runs
  the number of cheques P accepted equals the number Q sent, and in the twelfth
  P counted one fewer, most likely because the counter was read just before the
  last cheque landed. In no run did P count more than Q sent, which is what
  rules out another payer.
- **The first contention series is void**: not merely insufficient. Besides the
  load being too small, P served Q far less in the stock arms than the patched
  ones, 0 to 169 chunks against 307 to 327, because a faster download simply
  takes more chunks from other peers. Stock produced one timed cheque across
  three runs, so there was nothing to compare.
- **An earlier draft blamed a drifting endpoint for the missing effect**: and
  claimed the three calls were not on the path. Both were wrong, and are
  corrected under "What a chain call actually costs" above.

## Raw data

Accepting a cheque, in seconds, filtered to P.

| Series | Build | Endpoint | Runs | Cheques | Median | Minimum | Maximum |
|---|---|---|---|---|---|---|---|
| Interleaved | stock | normal | 6 | 39 | 0.362 | 0.184 | 1.493 |
| Interleaved | patched | normal | 6 | 56 | 0.304 | 0.031 | 1.858 |
| Stress | stock | chequebook calls at 0.55 s | 5 | 12 | 1.788 | 0.663 | 2.089 |
| Stress | patched | chequebook calls at 0.55 s | 4 | 9 | 1.045 | 0.031 | 1.236 |
| Caching | stock | cached chequebook calls | 3 | 34 | 0.213 | 0.113 | 0.882 |
| Baseline | stock | normal | 3 | 26 | 0.252 | 0.182 | 0.861 |

The per run logs are kept on the bench, so every figure here can be recomputed
without running anything again.

---

Generated with help of AI.
