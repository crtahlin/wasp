# Kademlia saturation limits do not gate high-capacity reserve fill

Issue: [#32](https://github.com/crtahlin/wasp/issues/32).

**Finding: raising the saturation peer limits does not speed up filling a large
reserve, and would only add shallow-bin connections at a cost to the network. The
fill is bound by the global sync rate, not by the number of peers.** This is
concluded from the reserve-doubling measurement
([#17](https://github.com/crtahlin/wasp/issues/17)) and the live topology of a
high-capacity node, without running a saturation change on the public network,
because that change consumes other nodes' connection slots and the observed
evidence already settles the question.

## The hypothesis

`defaultSaturationPeers = 8` and `defaultOverSaturationPeers = 18`
(`pkg/topology/kademlia/kademlia.go`) bound peers per bin. The hypothesis in
[#32](https://github.com/crtahlin/wasp/issues/32) is that a node with a much larger
reserve needs more parallel sync sources, so raising these limits would fill it
faster.

## Why the sync rate binds first

Reserve fill is inbound pull-sync, and two rate limits cap it:

- per-peer inbound: `DefaultMaxChunksPerSecond = 250` (`pkg/pullsync/pullsync.go`)
- global inbound across all peers: `DefaultMaxChunksPerSecond = 1000`
  (`pkg/puller/puller.go`)

So the global cap is reached by **four** peers syncing at the per-peer rate
(4 x 250 = 1000). The doubling measurement confirmed it: bench-1 filled its d=1 and
d=2 reserves at about 1000 chunks per second, sitting on the global cap the whole
time. Adding sync sources beyond the fourth does nothing, because the global rate is
already saturated.

## The node already has far more sync sources than it can use

bench-1, holding a d=2 reserve at storage radius 7 (depth 7), was connected as
follows. Its neighborhood bins, which are the ones that carry reserve chunks, were
connected to every peer available in them:

| Bin | Connected | Population |
|---|---|---|
| 7 | 14 | 14 |
| 8 | 8 | 8 |
| 9 | 3 | 3 |
| 10 | 2 | 2 |

That is about 25 neighborhood sync peers, against the four needed to saturate the
global rate. The neighborhood bins are limited by how many peers exist in them
(population), not by the saturation cap, so raising the cap cannot add sync sources
there: there are none left to add. Only the shallow bins (0 to 2, populations in the
hundreds) are held below their population by the cap, and those are forwarding and
retrieval peers, not reserve-sync sources.

## Conclusion, and what actually helps

Raising the saturation limits fails the hypothesis on both counts: the sync sources
that matter are already fully connected, and even if they were not, the global rate
would still bind at four peers. What it would do is add connections in the shallow,
populous bins, which is a cost to those peers and to the network's connection budget
for no fill benefit. On the issue's own measure, treating the neighborhood rather
than the node as the unit, this is the change with the widest network impact and the
least local return.

The lever for faster fill is the global sync rate, exposed as `--puller-rate-limit`
([#26](https://github.com/crtahlin/wasp/issues/26)), not the peer count. Raising the
rate has its own network cost, because it draws more bandwidth from each peer, and
that trade is measured under [#26](https://github.com/crtahlin/wasp/issues/26). The
saturation limits are the wrong knob for this problem.

## On not running a live test

[#32](https://github.com/crtahlin/wasp/issues/32) asks for a measurement, and the
honest reason none was run is that it is not worth its cost. Confirming a result the
observation already predicts, that more peers do not speed a rate-capped fill, would
mean raising this node's share of the public network's connections, which is exactly
the impact the issue flags. The evidence from an existing high-capacity node and the
rate limits is conclusive without it.

Generated with help of AI.
