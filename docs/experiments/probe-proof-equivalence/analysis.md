# Does the probe-based reserve-size proof meet every requirement the current proof does?

Issue: [#248](https://github.com/crtahlin/wasp/issues/248). Umbrella:
[#234](https://github.com/crtahlin/wasp/issues/234). Follows the reserve-size-independent
sampling study ([#235](https://github.com/crtahlin/wasp/issues/235)), the probe-sample cost
experiment ([#241](https://github.com/crtahlin/wasp/issues/241)), and the accuracy
calibration ([#245](https://github.com/crtahlin/wasp/issues/245)). Simulation:
[`main.go`](main.go).

**Verdict: no. The probe-based proof matches the current proof on accuracy, on cost it is far
cheaper, and on coordination between honest nodes it is equal, but it fails the one requirement
that matters most, soundness. A node that stores only half of its share, spaced evenly across
the address space, passes the probe proof almost always, while an honest smaller reserve is
correctly rejected. The current order-statistic proof cannot be gamed this way because a node
does not choose where its chunks land in the transform. So the probe scheme is not a drop-in
replacement. The deeper reason is a tension the study makes explicit: the current proof is
expensive because it must read the whole reserve, which is exactly what makes it unforgeable;
the probe proof is cheap because it samples a few points, which is exactly what lets even
spacing fool it. A sound probe proof may not be a cheap one. This is Swarm protocol analysis,
not a fork change.**

Terms, used once and then reused. Reserve: the chunks a node is paid to store. Proof of
resources: the check, run every redistribution round, that a node still holds its share.
Transformed address: an anchor-keyed hash of a chunk, uniform over the address space, where
the anchor is a per-round random value fixed only after every node has committed. Order
statistic: the k-th smallest value in a sample. Soundness, also unforgeability: a node cannot
pass without actually holding its reserve. Schelling game: one where players who cannot
communicate still coordinate on the same answer.

## The question

The current proof estimates a node's reserve size from the density of the smallest sixteen
transformed addresses and checks it against a threshold (Swarm "Future-proof Storage",
Appendix C). It costs a full pass over the reserve, two to four million chunks. The probe-based
alternative sketched in [#235](https://github.com/crtahlin/wasp/issues/235) estimates the same
density from k nearest-neighbour gaps at k anchor-derived probe points, in work proportional to
k rather than to the reserve size.

Two of its properties are already settled. Accuracy is equal: both estimators are Erlang with
the same shape k, so they separate an honest reserve from a slacker equally well
([#245](https://github.com/crtahlin/wasp/issues/245)). Cost is far lower: O(k) against O(reserve
size), hundreds of times cheaper ([#241](https://github.com/crtahlin/wasp/issues/241)). Every
one of the three prior studies then names the same unresolved question, whether the probe scheme
is sound and verifiable, not merely accurate and cheap. This study answers it, requirement by
requirement.

## The nine requirements

The current proof satisfies nine distinct requirements (grounded in the redistribution-game code
and the specification):

1. Accuracy of the reserve-size estimate.
2. Unforgeability, or soundness: a node cannot pass without holding its reserve.
3. Unpredictable challenge: the anchor is unknown until after commitment.
4. Cheap on-chain verifiability: the contract checks a compact proof without recomputing the
   sample.
5. Schelling coordination: honest nodes in a neighborhood converge on the same commitment.
6. Uniformity of the transformed addresses.
7. Determinism given the reserve and the anchor.
8. Sybil and dilution resistance, and freshness.
9. Neighborhood-depth honesty.

## Requirement by requirement

**Table: how the probe-based proof stands against each requirement the current proof meets**

| Requirement | Current proof | Probe proof | Verdict |
|---|---|---|---|
| Accuracy | smallest-k order statistic, Erlang(k, N+1) | k nearest-gap sum, Erlang(k, N) | equal ([#245](https://github.com/crtahlin/wasp/issues/245)) |
| Soundness | the smallest k are defined only over the whole set, and the addresses are content-bound, so density cannot be faked | a node that spaces its chunks evenly produces small gaps everywhere and passes while storing about half its share | **not met** (this study) |
| Unpredictable challenge | anchor is the post-commit reveal | probes derive from the same post-commit anchor | preserved |
| On-chain verifiability | compact inclusion proofs of the smallest-k over the committed set | needs a new proof format, and the soundness fix would change it again | not met as is |
| Schelling coordination | honest nodes hold the same chunks, compute the same smallest-k | honest nodes hold the same chunks near each probe, compute the same nearest set | equal (this study) |
| Uniformity | anchor-keyed hash | same anchor-keyed hash | inherited |
| Determinism | deterministic given reserve and anchor | deterministic given reserve and anchor | inherited |
| Sybil, dilution, freshness | valid paid stamps, not newer than consensus time | same eligibility filters | inherited |
| Neighborhood-depth honesty | counts only within committed depth | probes derive within committed depth | inherited |

Eight of nine are equal, better, or inherited. The ninth, soundness, fails, and it is the one a
proof of resources exists to provide.

## Soundness, in depth

### Why unpredictability looked like enough

The hopeful argument in the prior studies was this. The k probe points come from the anchor,
which is fixed only after every node has committed, so a node cannot know in advance where the
probes will land. A chunk can only sit near a probe if the node actually holds a chunk there.
So the nearest-gap sum should measure true held density, and a node holding fewer chunks should
show larger gaps and be rejected.

The flaw is that the probe is unpredictable but the defense against it does not have to be. A
node does not need to know where the probes will land if its chunks are close to every possible
probe. Even spacing achieves exactly that: m chunks placed evenly are within 1 over 2m of any
point, so every probe, wherever it lands, finds a held chunk close by. The gaps are small
everywhere, not because the node is dense, but because it is evenly spread.

The arithmetic is direct. For m evenly-spaced chunks the forward distance from a random probe is
uniform on the interval from zero to 1 over m, with mean 1 over 2m, so the sum of k of them has
mean k over 2m. For an honest reserve of n random chunks each gap is exponential with rate n,
so the sum has mean k over n. The two means are equal when m equals n over 2. An adversary with
half as many chunks, spaced evenly, produces the same estimator as an honest node with the full
reserve.

### The simulation confirms it

[`main.go`](main.go) models transformed addresses and probes as points on the unit circle. It
sets the acceptance threshold so an honest reserve of size n passes 95 percent of the time, then
measures how often a slacker holding a fraction of n passes, for two placements: random (an
ordinary smaller reserve) and even (the adversary's best).

**Table: pass rate of a node holding a fraction of the honest reserve, probe proof, k=16,
threshold set for 95 percent honest recall**

| Holdings, m/n | Random placement | Even placement |
|---|---|---|
| 0.30 | 0.002 | 0.169 |
| 0.40 | 0.026 | 0.853 |
| 0.50 | 0.118 | 0.999 |
| 0.60 | 0.315 | 1.000 |
| 0.70 | 0.546 | 1.000 |
| 0.80 | 0.752 | 1.000 |
| 0.90 | 0.873 | 1.000 |
| 1.00 | 0.950 | 1.000 |

Random placement behaves as a proof of resources should: a node holding half its share passes
only 12 percent of the time, and the pass rate rises smoothly toward the honest 95 percent only
as holdings approach the full reserve. Even placement breaks it: a node holding half its share,
spaced evenly, passes 99.9 percent of the time, and one holding 40 percent passes 85 percent of
the time. This matches the arithmetic: the break is at about half, and it is sharp because even
spacing has very low variance.

So the probe proof, as a bare nearest-gap estimator, is not sound. It admits a node that stores
about half of what it should, or less as k shrinks.

### Why the current proof resists this

The current proof is not gameable by even spacing because a node does not choose the positions
it is measured on. The statistic is the smallest of the anchor-keyed hashes of the chunks the
node holds. The hash of a chunk is fixed by the chunk's content and the anchor; the node cannot
place it. To make its smallest-k small, a node must actually hold many chunks, because the
smallest-k of a small set are not small. The measurement is over the whole held set, and the
positions are content-bound. Even spacing is simply not a move that is available.

This is the tension the study set out to test and found real. The current proof is expensive
because it reads the whole reserve, and reading the whole reserve is exactly what makes it
unforgeable. The probe proof is cheap because it reads k points, and reading only k points is
exactly what lets a node arrange those points to be always close. The cost and the soundness are
two sides of one coin.

### What would restore soundness

Three directions, each of which appears to give back the cost the probe scheme was meant to save:

- Prove the nearest, not merely a near chunk. If the node had to prove that no closer chunk
  exists, even spacing would not help, because the proof would bind to true local density. But a
  proof of non-existence over the reserve is a proof about the whole set, which is the cost the
  probe scheme avoided.
- Bind chunk selection to the anchor. If which chunks a node may count were themselves derived
  from the post-commit anchor, a node could not pre-arrange an even set. This is close to what
  the current proof already does by hashing with the anchor, and it tends back toward measuring
  the whole set.
- Raise k until even spacing is as expensive as honest storage. Even spacing of m chunks needs m
  real, paid, held chunks; the attack is cheaper than honest only because m can be about half of
  n. Raising the required density narrows the gap but does not close it, and it raises the cost.

None of these is free, and each points the same way: toward reading more of the reserve.

## Coordination

The one remaining requirement this study could settle by measurement is Schelling coordination,
whether two honest nodes whose reserves differ slightly still commit to the same proof. The
simulation gives two honest nodes a shared set of probes and a reserve that differs by a small
fraction, and measures how often their commitments match, for the probe scheme (the set of
nearest held chunks) and the current scheme (the smallest-k).

**Table: agreement rate between two honest nodes whose reserves differ by a fraction, k=16**

| Reserve difference | Probe proof | Current proof |
|---|---|---|
| 0.1 percent | 0.972 | 0.970 |
| 0.5 percent | 0.852 | 0.852 |
| 1 percent | 0.728 | 0.713 |
| 2 percent | 0.531 | 0.523 |
| 5 percent | 0.211 | 0.213 |

The two are equal to within noise at every difference. The probe proof coordinates exactly as
well as the current one; the Schelling game has the same shared truth to converge on. This
requirement is met.

## On-chain verifiability

This study does not design the on-chain verifier, and the soundness result makes that secondary:
there is no point specifying a cheap verifier for a proof that is not sound. Two things are worth
recording. First, the probe proof needs a new proof format regardless, because the commitment is
no longer the smallest-k of a single sample but a held chunk at each of k anchor-derived probes,
each with its stamp and inclusion proof, so the contract cost is k inclusion checks rather than
the current fixed challenge. Second, any fix for soundness above changes the format again, most
likely toward proving something about the whole reserve, which is the opposite of cheap. So on
verifiability the honest position is: not equivalent as it stands, and not worth settling until
soundness is.

## Overall verdict

The probe-based proof is equal to the current proof on accuracy, better on cost, equal on
coordination, and inherits uniformity, determinism, sybil resistance, and neighborhood-depth
honesty by reusing the same anchor, transform, and eligibility filters. On eight of the nine
requirements it is equivalent or better.

It fails the ninth. It is not sound: a node that stores about half of its share, spaced evenly,
passes almost always, an attack the current proof does not admit because its measurement is over
the whole content-bound reserve. A proof of resources that a node can pass while storing half is
not a usable proof, whatever its accuracy or cost. So the probe scheme is not a drop-in
replacement for the current one.

The study's honest conclusion is stronger than "needs more work". The cheapness and the
unforgeability are in direct tension: the current proof is dear precisely because it reads the
whole reserve, and that is precisely what makes it hard to forge; the probe proof is cheap
precisely because it does not, and that is precisely what makes it forgeable by even spacing. A
sublinear proof of resources is not ruled out, but it cannot be this one unmodified, and the
modifications that would make it sound appear to give back the cost it was meant to save.

## Open problems

- Is there a sublinear proof that binds to true local density rather than to the presence of a
  near chunk, without a proof over the whole reserve? This is the real open question, and it is
  a question in protocol design and cryptography, not a fork change.
- What is the exact break as a function of k? The simulation shows it near half of the reserve at
  k=16; a smaller k breaks at a larger fraction, a larger k at a smaller fraction but at higher
  cost. The trade curve between k, the break, and the cost is worth deriving.
- The whole matter is Swarm protocol, shared by every node and the on-chain contract. The
  ProbeSample endpoint remains a benchmark only; it is not wired into the redistribution game.

## References

- Accuracy equivalence: [#245](https://github.com/crtahlin/wasp/issues/245),
  `docs/experiments/probe-sample-k/`.
- Cost: [#241](https://github.com/crtahlin/wasp/issues/241),
  `docs/experiments/probe-sample-cost/`.
- The proposal and the requirements: [#235](https://github.com/crtahlin/wasp/issues/235),
  `docs/experiments/reserve-size-independent-sampling/`.
- The current proof: Swarm "Future-proof Storage", Appendix C and sections 3.4 and Appendix B.

Generated with help of AI.
