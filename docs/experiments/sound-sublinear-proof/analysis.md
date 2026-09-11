# A sound sublinear proof of reserve size

This note answers the question that the probe-proof study (#248) left open: can a
reserve-size proof be made cheap without being forgeable? The probe scheme in #248 was
hundreds of times cheaper than the current whole-reserve scan, but it failed soundness. A
node that stored half of its share, spaced evenly, passed almost every round, because the
node chose where its measured points landed. This note describes a candidate that keeps the
cheapness and closes that hole, states the result from simulation, and gives an honest
account of where it holds and where it does not.

The short version: a windowed order statistic is sound and sublinear. It rejects the
even-spacing slacker that broke the probe scheme, it degrades an attacker's odds in
proportion to how much the attacker actually stores, and it costs a chosen fraction of the
current scheme. There is a floor on how cheap it can be, set by the need to keep enough
chunks under measurement to tell a full reserve from a half one.

## The problem in one paragraph

The current redistribution scheme takes the sixteen smallest transformed addresses over the
whole reserve. It is sound because the transformed address of a chunk is a fresh hash the
node cannot choose: to make its sixteen smallest small, a node must actually hold many
chunks. It is expensive because it reads every chunk, cost of order N. The probe scheme
(#235, #241, #245) replaced the whole-reserve scan with sixteen anchor-derived probe points
and the nearest stored neighbour of each, cost of order k. That made the measurement depend
only on the gaps between stored addresses, and a node controls those gaps: sixteen evenly
spaced points sit in the middle of large empty gaps, so half a reserve laid out evenly looks
denser than a full reserve laid out at random. Cheapness and unforgeability turned out to be
two views of one property. The whole-reserve scan is unforgeable exactly because it looks at
a quantity the node cannot arrange, the transformed addresses; the probe scheme became
cheap exactly by looking at a quantity the node can arrange, the address gaps.

## The candidate: a windowed order statistic

Keep the transformed address, the thing the node cannot choose. Give up only the part of the
whole-reserve scan that makes it expensive, which is scanning the whole reserve.

Define the proof for a round as follows.

1. From the round anchor, derive a window: a contiguous slice of the address space covering a
   fraction 1/f of it, its start pseudo-random in the anchor. The window is unpredictable
   before the anchor is revealed, exactly as the current scheme's transform salt is.
2. Consider only the chunks whose address falls inside the window. For each, compute its
   transformed address, the same content-bound hash the current scheme uses.
3. The proof is the k-th smallest of those transformed addresses. Accept when it is below a
   threshold set so an honest full reserve passes with the target recall.

The cost is the number of chunks in the window, about N/f, because only those chunks are
read and hashed. Choosing f trades cost against the floor discussed below. The soundness
comes from step 2: the measured quantity is still the transformed address, which the node
cannot place, so evenly spacing the stored addresses buys nothing. And because the window is
unpredictable, the node cannot pack its chunks where the measurement will look.

This is the user's "randomly spaced sample" intuition made precise: sample a random region,
and inside it fall back to the honest, unforgeable order statistic.

## Soundness result

The simulation (`main.go`) is scale-free: only the count of a node's chunks in the window
matters, so it works in counts rather than materialising millions of chunks. The k-th
smallest of c transformed addresses is drawn as Erlang(k, c), which is its distribution for k
much smaller than c. Honest reserve n = 4,000,000, k = 16, threshold set for 95 percent
honest recall, 100,000 trials, seed 1.

### Even spacing buys nothing

A slacker holds a fraction m/n of the reserve. Random placement is an ordinary smaller
reserve; even placement is the #248 attack. Under the windowed statistic both give the same
count of chunks in the window, so they must pass at the same rate. They do.

Windowed order statistic, n = 4,000,000, k = 16, 95 percent honest recall.
Pass rate by holdings and window size; "even" is the attack that broke the probe scheme.

| window | m/n  | random pass | even pass |
|--------|------|-------------|-----------|
| 1/50   | 0.30 | 0.0023      | 0.0022    |
| 1/50   | 0.50 | 0.1235      | 0.1265    |
| 1/50   | 0.70 | 0.5512      | 0.5518    |
| 1/50   | 0.90 | 0.8795      | 0.8789    |
| 1/50   | 1.00 | 0.9503      | 0.9502    |
| 1/400  | 0.50 | 0.1243      | 0.1271    |
| 1/400  | 1.00 | 0.9505      | 0.9505    |
| 1/2000 | 0.50 | 0.1270      | 0.1286    |
| 1/2000 | 1.00 | 0.9505      | 0.9514    |

Compare the probe scheme in #248: there the even-spacing half-share passed at 0.999 while the
honest random half was rejected at 0.118. Here the even column tracks the random column at
every row. The attack is gone, and it is gone at every window size down to 1/2000, a
two-thousand-fold cost reduction. A half-share slacker is rejected at about 88 percent
(passes 0.124) against the honest 95, which is the ordinary, wanted behaviour of a
size proof: store less, pass less.

### Concentration degrades in proportion, it does not break

The remaining thing a node can arrange is where in the address space it stores at all. A node
could store at full honest density but only over a fraction cov of the space, holding cov of
a full reserve. Because the window is unpredictable, the window lands inside the covered
region only cov of the time; elsewhere it is empty and the proof fails.

Pass rate of a node storing at full density over a fraction of the space, window 1/400.

| coverage cov | pass rate |
|--------------|-----------|
| 0.25         | 0.2379    |
| 0.50         | 0.4740    |
| 0.75         | 0.7139    |
| 1.00         | 0.9504    |

Pass rate tracks coverage. This is not the probe-scheme failure, where a half-share passed
everything. It is the honest gradient: a node that stores a quarter of the space passes about
a quarter of the rounds. Over many rounds its expected reward is proportional to what it
actually stores, which is the property the redistribution game needs. Concentration is not an
exploit against the windowed statistic; it is just another way to store less and be paid
less. The whole-reserve scan has the same gradient. The difference from #248 is that there
the gradient was defeated by even spacing and here it is not.

## Cost and the floor

The window can shrink, cutting cost, only until it holds too few chunks to tell reserves
apart. Below, C is the honest window count, which is the cost in chunks read, and the figure
is the pass rate of a half-share even-spacing slacker, whose window then holds about C/2
chunks.

Half-share even slacker pass rate versus window count C (the cost), k = 16.

| C (chunks) | slacker pass (m = n/2) | cost versus full scan |
|------------|------------------------|-----------------------|
| 16         | 0.0000                 | about n/250000        |
| 32         | 0.2512                 | about n/125000        |
| 64         | 0.1773                 | about n/62500         |
| 128        | 0.1490                 | about n/31250         |
| 256        | 0.1367                 | about n/15625         |
| 1024       | 0.1270                 | about n/3906          |
| 2000       | 0.1258                 | about n/2000          |

Read this from the bottom up. For large C the slacker settles near 0.126, cleanly separated
from the honest 95. As C falls toward k the separation degrades: at C = 32 the slacker's
window holds about k chunks, barely enough for the order statistic to exist, and its pass
rate climbs to 0.25. The C = 16 row is degenerate rather than good news: the half-share's
window holds about eight chunks, fewer than k = 16, so it is auto-rejected, but so is much of
the honest mass, and honest recall is no longer met. The usable floor is therefore C a
healthy multiple of k, a few hundred chunks, which still buys a cost reduction of four orders
of magnitude at bench scale. The ceiling on the speedup n/C is set by k, not by n: a larger
reserve can use a proportionally smaller window and stay sound.

## Accuracy and calibration

The threshold is calibrated exactly as the current scheme's is, by fixing the honest recall.
The windowed statistic's honest distribution is Erlang(k, C), narrower in relative terms as C
grows, so the same recall gives a sharper accept boundary at larger windows. This is why the
1.00 row sits at 0.95 across every window size in the first table: recall is held fixed by
construction. Estimator variance, which the k-estimation work (#245) quantified for the whole
reserve, applies here with C in place of N; a smaller window is a noisier estimator, another
reason not to push f past the floor.

## Coordination

The windowed statistic inherits the current scheme's coordination property for free, because
it uses the same anchor. Every node in a neighbourhood derives the same window from the same
round anchor and measures the same region, so their proofs are comparable in the same way the
current transformed-address proofs are. The probe study measured probe and current
coordination at parity (0.97 each); the windowed statistic does not change the anchor
mechanism, so that parity carries over. No new coordination machinery is needed.

## On-chain verifiability

A sketch, not a claim. The current scheme's proof is checkable because a verifier can
recompute the transform and confirm the submitted addresses are the k smallest. The windowed
statistic is checkable the same way, with one addition: the verifier recomputes the window
from the anchor and confirms every submitted address lies inside it. That is one range check
per submitted address on top of the existing transform check, so verification stays the same
order as today. The prover cost falls to N/f; the verifier cost is unchanged, since the
verifier already only inspects the k submitted addresses. Working this into the redistribution
contract is out of scope here and belongs in its own issue.

## Verdict

The windowed order statistic is a sound sublinear reserve-size proof. It closes the
even-spacing hole that #248 found in the probe scheme, it degrades a concentrating attacker
in proportion to what the attacker stores rather than letting it pass for free, it keeps the
current scheme's calibration and coordination, and it costs a chosen fraction of the current
scan down to a floor set by k. It answers the question #248 posed, whether cheapness and
unforgeability can be had together, with a qualified yes: together, down to a window of a few
hundred chunks, which at bench scale is a four-orders-of-magnitude saving.

This is a simulation result under an idealised model, not a protocol change and not a proof
of security. Two views of one coin still holds, only the coin is smaller: the scheme is
unforgeable because it measures transformed addresses, and it is only as cheap as the window
is small, and the window can be small only while it still holds enough chunks to measure.

## Open problems

- **A formal bound.** The simulation shows even = random and concentration = proportional.
  A written argument that the windowed transformed-address statistic is invariant to stored
  address placement, and that no placement strategy beats honest density in expected reward,
  would turn the empirical result into a claim.
- **Adaptive multi-window attacks.** This note tested single-window rounds. A node that sees
  many windows over time might learn something exploitable; the anchor's unpredictability
  should prevent it, but it was not tested here.
- **The contract change.** The on-chain sketch needs a real design: how the window is
  derived from the anchor in the contract, the range-check cost in gas, and the migration
  from the current whole-reserve proof.
- **Interaction with radius changes.** When the reserve grows or shrinks, N changes and so
  does the honest window count. The floor argument says f should track N to keep C above the
  floor; a rule for setting f from the observed radius is unwritten.

Generated with help of AI.
