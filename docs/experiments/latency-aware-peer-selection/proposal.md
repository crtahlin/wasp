# Latency-aware peer selection for retrieval

Status: proposal, 2026-11-18. Nothing here is implemented. Issues: [#841](https://github.com/crtahlin/wasp/issues/841) (selection, W1) and [#842](https://github.com/crtahlin/wasp/issues/842) (pruning, W2). The usual order follows: merged spec, code with tests, review, merge. Measurement setup, metrics and success criteria are for the implementer and the spec; section W3 only records why the European benches cannot show the effect. The wire surface is not touched (rule 6): every change is in how this node chooses among peers it is already connected to, and which peers it keeps.

## Why

Swarm's storers sit in Europe: on 2026-11-16, 97.5% of the 4,347 reachable full nodes were in Germany, Finland, the Netherlands and France. A node in another region fetches almost every chunk across an intercontinental link. Running more full nodes in that region does not change this for the other nodes there, because a Bee node chooses the peer it asks by address distance only. This proposal makes a node prefer a lower-latency peer when that costs no routing progress, so that full nodes in a region start serving each other from their caches. No gateway, no pinning, no new protocol.

Terms used below:

- **Proximity order (PO)** between two 32-byte addresses is the number of leading bits they share. Higher is closer.
- A **bin** holds this node's connected peers with one proximity order to this node. Bin 0 holds the peers that differ in the first bit, so half the network is eligible for bin 0.
- A **shallow bin** is a bin below the node's depth. The node keeps only some of the eligible peers there: it stops dialling at `defaultSaturationPeers = 8` and prunes above `defaultOverSaturationPeers = 18` (`pkg/topology/kademlia/kademlia.go:65-66`).
- The **closest peer** for a chunk is the connected peer with the highest proximity order to the chunk address, with ties broken by the full XOR distance (`Kad.ClosestPeer`, `kademlia.go`; `peer.Closer`).
- A **forwarder** is a peer that does not hold a chunk and asks the next closer peer on the requester's behalf. With `cache-retrieval` on (the default), a forwarder stores every chunk it forwards in its cache (`pkg/retrieval/retrieval.go`, "cache the request last").
- **Latency EWMA** is the smoothed response time of a peer that kademlia's metrics collector already records (`im.PeerLatency`, `kademlia.go:1611`) and shows in the topology API as `latencyEWMA`. Today nothing reads it to make a decision.
- A peer is **healthy** when the `salud` service decides so. Every cycle `salud` requests a status snapshot from every connected full peer and measures how long the reply takes. A peer is unhealthy when any of these holds (`pkg/salud/salud.go:221-236`): its committed depth is more than 2 below the network radius; its reply took longer than the 40th percentile of all replies (`DefaultDurPercentile = 0.4`, so the slowest 60% of peers are unhealthy by this rule alone); it reports fewer connected peers than the 80th-percentile threshold; or its batch commitment differs from the majority. A failed status request also marks the peer unhealthy.
- A peer is **reachable** when libp2p's reachability check says it can be dialled from the public internet (`ReachabilityStatusPublic`). A peer behind NAT or a firewall is "private": it can only connect outward. It still serves and forwards chunks on the connection it opened to us.

## What salud already does with latency

The `salud` service already filters peers by response time, so latency is not new to the client. Every cycle (10 s after start, then doubling up to every 5 minutes) it requests a status snapshot from every connected full peer and marks the slowest 60% unhealthy by the duration rule alone, besides the depth, connection-count and batch-commitment rules listed under the terms above. Retrieval prefers healthy peers, so a remote node already sends most first attempts to the faster 40% of its peers. The final choice within that set is still by address distance, which is why Table 1's last row below is slow: the European storer and the Mumbai neighbour are both healthy, and the European one is closer by address. The spec must state how the new rule and the salud rule interact, for example whether a healthy set that is all regional turns the preference into "regional only".

## What the stock client does

1. **Choosing whom to ask.** `retrieval.closestPeer` (`pkg/retrieval/retrieval.go:388-398`) asks kademlia for the closest peer that is reachable and healthy; if none, reachable; if none, any. Within the filter the choice is strictly by address distance. Latency is not consulted.
2. **Retrying.** The requesting node starts a second request to the next closest peer after 1 s if the first has not answered (`preemptiveInterval`, `retrieval.go:126`), and a forwarder does not retry preemptively.
3. **Keeping connections.** Shallow bins fill with whichever eligible peers the node happened to learn about and dial. When a bin is over-saturated, `pruneOversaturatedBins` (`kademlia.go:~850-890`) drops, in order: an unhealthy peer, then a private (unreachable) peer, then a random peer from the balanced slot. Latency is not consulted. Static peers are never pruned (wasp #291).

Measured consequences (mumbai-1, Contabo, India, six stock Bee 2.8.3 full nodes, 17 and 18 Nov 2026, scripts in the swarm-regional-nodes project):

**Table 1. First byte for 5 to 200 KB objects, client on the same machine as the node**

| Path | Median first byte |
|---|---|
| Node in Mumbai, content not held (cold) | 1.4 to 2.2 s |
| Node in Mumbai, content in its cache (warm) | 0.017 to 0.043 s |
| Public gateway in Europe, from Mumbai | 1.0 s |
| Node B in Mumbai, right after connected node A in Mumbai cached the same content | 2.2 s (same as cold) |

**Table 2. Ranged download of one file (8 MB and 32 MB), same setup**

| Path | Throughput |
|---|---|
| Node in Mumbai, cold | 0.32 to 0.38 MB/s |
| Node in Mumbai, warm | 17 to 23 MB/s |
| Public gateway in Europe, from Mumbai | 1.7 to 2.6 MB/s |

The last row of Table 1 is the point: two full nodes in the same data centre, directly connected, do not help each other, because node B asks its address-closest peer, which is in Europe, even though node A holds every chunk 0.2 ms away.

A regional node is asked by a regional neighbour only when it is that neighbour's closest connected peer for the chunk. With 8 to 18 peers kept per shallow bin out of about 2,000 eligible, a Mumbai node with 50 Mumbai peers available would still hold about two of them by chance, and would send an estimated 1 to 2% of its requests their way (estimate, not measured).

## Hypothesis

If a node prefers the lowest-latency peer among the peers that are equally close to a chunk, and keeps lower-latency peers when it prunes a full bin, then after a warm-up period a regional node will route a large share of its requests through regional peers, and those peers' caches will serve repeat requests at local speed. Cold requests cost the same as today, because the regional forwarder still has to reach Europe; the added hop is intra-region.

Two things make this cheap to try: the latency EWMA already exists per peer, and forwarders already cache what they forward.

## Proposed change

### W1. Selection: lowest latency among equally close peers

In `Kad.ClosestPeer`, or in a new method used only by retrieval, change the rule from "the peer with the smallest XOR distance" to:

1. Compute the candidate set: connected peers that pass the caller's filter and whose proximity order to the chunk equals the highest proximity order any candidate reaches. These peers are equally close in bin terms; forwarding to any of them makes the same routing progress, and the "closer than self" check that stops forwarding loops is unchanged, because proximity order to the chunk is what that check compares.
2. Among them, choose the peer with the lowest latency EWMA. A peer with no latency record yet counts as the stock choice (smallest XOR distance) so that a fresh node behaves exactly as today.
3. Preemptive retries and the skip list work as before: the next attempt goes to the best remaining candidate by the same rule.

Only retrieval uses the new rule. Push-sync keeps strict address distance, because an upload must reach the chunk's neighbourhood, and the saving on the last hop is not worth a change in where chunks land.

Setting: `retrieval-prefer-low-latency`, boolean, **default true** (operator decision, 2026-11-18). With `false` the node runs the stock rule, and the mutation check for the whole change is that `false` reproduces the stock measurements.

Possible later extension, not part of W1: allow a candidate one proximity order farther when its latency is lower by a margin. Measure W1 first; a slack setting is permanent surface and may not be needed.

### W2. Pruning: drop the slowest, not a random peer

In `pruneOversaturatedBins`, after the existing unhealthy-first and private-second steps, replace the random pick with the peer with the highest latency EWMA in the balanced slot. The balanced slot structure stays, so address-space coverage inside the bin is unchanged; only which peer in the slot goes. Static peers stay excluded (wasp #291). Peers without a latency record are pruned before peers with one, as today's random pick would eventually do.

This is the part that lets regional peers accumulate: today a Mumbai peer that happens to be connected is as likely to be pruned as any other.

Same setting as W1, or a second one if measurement shows the two parts should be switched separately. Rule 8 says measure first; the measurement in W3 runs the four combinations.

### W3. Measurement: left to the implementer

How to measure, on what setup, and what counts as success are for the implementer and the spec (rule 8: measure first, expose second). One constraint is known now: bench-1 and bench-2 share one European host, so they cannot show the effect; the measurement needs a requester with peers at two latency levels, for example a wasp node in a region with stock full nodes of the same region among its peers. The case to look at is the one that is 2.2 s today: content fetched through a regional node different from the one that cached it. The option set to `false` must reproduce the stock figures.

### W4. Risks to check in the spec

- **Clustering.** If every node in a region prefers regional peers, shallow bins could become regional, and a region that loses its link to Europe would also lose routing diversity. The balanced-slot rule bounds this: pruning changes which peer fills a slot, not how many slots exist per address range. The spec should state how many of the 8 to 18 peers per bin can end up regional and whether a floor of non-regional peers is needed.
- **Health interaction.** `salud` already marks the slowest 60% of peers unhealthy by status-reply duration, so from a remote region the healthy set is biased toward faster peers already. W1 changes the choice within the healthy set; the spec should confirm that the two rules do not fight (for example, every regional peer being healthy and every European one not, which would turn W1 into "regional only").
- **Cache size.** The benefit is bounded by the regional peers' `cache-capacity` (default 1,000,000 chunks, about 4 GB). The swarm-regional-nodes project raises it on its nodes; no change here.
- **Accounting.** A forwarder pays the next hop and charges the requester as today; no change to prices or settlement.

## Relation to other wasp work

- [content-providers/passive-sharing.md](../content-providers/passive-sharing.md): relies on forwarders caching what they forward. This proposal increases how often a regional forwarder is used, so the two are complementary.
- [static-peer-pruning](../static-peer-pruning/spec.md) (#291): W2 keeps the static-peer exclusion.
- [kademlia-saturation-scaling](../kademlia-saturation-scaling/analysis.md) (#32): that finding argues against raising saturation limits; this proposal does not raise them. It changes which peers fill the existing slots.
- Upstream: the change is local to the node and compatible with stock peers. If W3 shows the effect, offer it upstream as an enhancement; it is not a defect.
