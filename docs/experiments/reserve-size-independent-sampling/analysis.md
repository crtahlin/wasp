# A reserve-size-independent sample: analysis

Issue: [#235](https://github.com/crtahlin/wasp/issues/235). Umbrella:
[#234](https://github.com/crtahlin/wasp/issues/234).

**Conclusion: the goal is real, but not reachable from the fork. The sampler's cost is
linear in the number of chunks within radius, which genuinely varies about twofold, so
cutting it would help weak nodes. But the linear cost is the proof of storage, not an
accident of implementation: the redistribution game proves how much a node stores by
taking the smallest 16 of the anchor-keyed hashes of its reserve, an order statistic
that is only defined over the whole set. A sublinear replacement is conceivable, and
this note sketches one, but it is a different proof shared by every node and the on
chain contract, with its own security and accuracy work to do. A node that sampled
differently would fall out of consensus with its neighbourhood and fail the proof. The
fork cannot make this change; it is recorded here as a direction for the Swarm protocol,
not iceboxed as wrong.** This corrects an earlier draft of this study that called the
cost fixed and claimed the contract would reject a different algorithm; both were wrong,
as set out below.

## What the sample is, and what it costs

Each redistribution round a full node computes a reserve sample. It iterates the chunks
within its committed depth of the round anchor, computes for each a transformed address
(a BMT hash of the chunk data keyed with the anchor, `transformedAddress` in
`pkg/storer/sample.go`), and keeps the 16 with the smallest transformed addresses
(`SampleSize = 16`). It commits a hash of those 16 on chain, and if selected, proves a
few of them.

The cost is one transformed-address hash per chunk within radius, so it is
O(reserveWithinRadius). That count is not fixed. It is the number of chunks the node
holds within its committed depth, which ranges from zero up to about four million and
sits most often between two and four million, a roughly twofold swing depending on how
full the neighbourhood is. It is independent of the node's total capacity under reserve
doubling, because a doubled node still samples a single neighbourhood
([#17](https://github.com/crtahlin/wasp/issues/17)), but it is not independent of how
full that neighbourhood is. This is what makes the sample the term that can push a weak
node past the commit deadline, and why reducing it is a genuine goal rather than a
phantom one.

## Why the cost is the proof, not an accident

The reason the sample touches every chunk is that it is the proof of how much the node
stores. The Swarm storage-incentives specification ("Future-proof Storage", v4.20)
calls this the proof of resources and derives it in Appendix C as density-based size
estimation.

The transformed addresses are hashes, so they are uniform over the 256-bit address
space. Take the smallest 16 of them. The value of the largest of those 16, the last
element, is an order statistic: for n uniform values its distribution is Erlang with
shape 16 and rate n+1 (Appendix C, equation for E(x,k,n+1)). The denser the sampled set,
the smaller that last element tends to be. So a ceiling on the last element is a floor
on n. The contract checks the last element against a calibrated threshold u; the
specification tunes u for k = 16 to a recall error of about 0.098 and a precision error
of about 0.072 (Appendix C, Table 3), trading the two off, and notes that a larger k
sharpens the estimate (Figure 19).

This is the point that decides the whole question. To prove size this way the node must
find the 16 smallest transformed addresses, and the smallest 16 of a set are only
defined once the whole set has been seen. There is no way to compute them from a
subsample. The linear pass is not the node being slow; it is the node doing the
measurement that proves it is not slacking on storage.

## It is a Schelling game, verified statistically

The contract does not know a node's true reserve and does not recompute the "correct"
sample. It cannot. The guarantee comes from three things together, none of which is an
exact check of the algorithm:

- **Density.** The last-element threshold above bounds the reserve size from below.
- **Neighbourhood consensus.** All honest nodes in a neighbourhood hold the same chunks
  and so compute the same commitment. The round is a Schelling game around that
  commitment (specification section 3.4); a node whose commitment does not agree with
  its neighbours is the odd one out.
- **An unpredictable post-commit challenge.** The round anchor is the exclusive-or of
  the obfuscation nonces revealed after everyone has committed (specification Appendix
  B), so it is not known while the sample is being built. A second anchor drawn after
  the reveal selects which of the 16 items, and which segment within a chunk, must be
  proved. A node cannot answer that for chunks it does not hold.

Because the contract never learns the true sample, the security rests on every honest
node running the same rule. That is the fact that governs whether the rule can be
changed.

## A sublinear alternative, worked through

The linear cost is intrinsic to this construction, the order statistic, but not to
proof of resources in general. A different size proof could be sublinear. The most
natural one:

Derive k probe addresses from the round anchor. For each probe, the node proves it
holds a reserve chunk within some XOR distance d of it: the chunk data, its postage
stamp, and an inclusion proof. The distance to the nearest held chunk at a random probe
is a measure of local density, so k probes estimate the reserve size in O(k)
address-sorted index lookups rather than one pass over the whole reserve. The retrieval
index is already keyed by address, so the nearest held chunk to a probe is a cheap
lookup.

This keeps the properties that make the current design work. The probes are
unpredictable until the round, because the anchor is post-commit, so a node cannot
pre-position chunks to sit near them. To answer k unpredictable probes with close held
chunks a node must hold density everywhere, which is to say it must hold the whole
reserve. And two honest nodes with the same reserve return the same nearest chunks, so a
Schelling consensus still forms. So the linear cost is not strictly fundamental.

What is fundamental is that this is a different proof, and the open problems are real:

- **It needs its own accuracy calibration.** The order-statistic estimator has a clean,
  provable error analysis (Appendix C). A probe estimator has a different distribution
  and would need the same recall-and-precision treatment before anyone could trust the
  size it reports, including the choice of k and d.
- **Proving "a chunk within d" is not proving "the nearest".** The node presents a close
  chunk it happens to hold; it does not prove no closer one exists, which would need a
  non-membership proof over the reserve. That is sound for a size floor, but the gap has
  to be analysed for ways to game the estimate, which the order-statistic version does
  not have because the last element is a hard maximum over the committed set.
- **It needs a new on-chain verifier and proof format.** The commitment, the inclusion
  proof structure, and the contract's checks all change. This is the redistribution
  contract, shared by every node.
- **The anchor entropy and collusion resistance must be re-established.** The
  specification is careful about a colluding neighbourhood influencing anchor selection
  (section 3.4); a new selection rule has to re-argue that.

## Fork-local? No

The sampling algorithm, its calibration, and the game around it are shared with every
stock Bee node and with the deployed redistribution contract. A node that sampled by a
different rule would compute a different commitment, fall out of Schelling consensus
with its neighbourhood, and produce a size proof the contract's calibration does not
expect. It would not quietly get a faster sample; it would lose the game. So this cannot
be a fork-local optimisation, however it is implemented. It is exactly the kind of
change the fork rules say to stop on: a protocol change that affects a running node's
stake (rule 4). The wire-freeze rule 6 governs the peer-to-peer surface rather than the
contract, but the conclusion is the same from the other direction: the change is
consensus with other nodes, not a local matter.

## Where this leaves it

The goal is legitimate and the linear cost is not a law of nature, but the fork has no
route to it. Two things bound its urgency in the meantime. The doubling and
committed-depth mechanism already holds the sample to one neighbourhood rather than the
whole of a large node's reserve. And the fork's actual aim, widening the range of
hardware that can play the game profitably, is served by the implementation-level
performance of producing the same sample, which the earlier work found to be near its
measured ceiling ([#236](https://github.com/crtahlin/wasp/issues/236),
[#8](https://github.com/crtahlin/wasp/issues/8)).

A sublinear proof of resources is a real idea and the probe-based sketch above is a
plausible starting point, but it is a change to the Swarm protocol with its own security
and statistics to settle, not an engineering task for a downstream client. Recorded here
as a direction for upstream. Revisit for the fork only if the protocol adopts one.

Generated with help of AI.
