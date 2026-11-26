# Light-node cache serving, and the live-event streaming case

Issue: [#282](https://github.com/crtahlin/wasp/issues/282).

**Finding: the premise that popular content ends up in many light-node caches holds,
and holds most strongly for live streams. But at stock limits, what overloads full
nodes during a live event is not where the popular chunks are stored. It is how
little bandwidth each connection gives a light node for free, about 6 to 9 KB/s,
which forces every viewer to open 100 or more connections. Serving from light caches
at stock limits does not change that. wasp is its own build, so these limits can be
raised on the serving side. With raised limits, a stream needs about 8 or 9
connections instead of about 100, whether the serving node is a full node run for
the event or a light node that has the stream cached. The open question, and the
first thing to measure, is whether viewers use a higher allowance without changes on
their side.**

All code references are to bee `v2.8.2` unless stated otherwise.

## 1. The issue's claims, checked

Every code claim in the issue holds at the lines it cites, and in wasp `main` at
shifted lines:

- the cache is created with or without a reserve;
- `cache-capacity` defaults to 1,000,000 chunks;
- light nodes wrap retrieval, pushsync and pullsync in `WithBlocklistStreams`
  (`pkg/node/node.go:1198-1200`);
- incoming light nodes go to a separate list that routing never uses;
- full nodes refuse to dial light nodes;
- a full node accepts at most 100 light nodes.

One correction: `cache-retrieval` is not the reason a light node caches. That flag
controls caching of chunks a node forwards for others, which a light node never
does. A light node caches through the download path (`Download(cache)` in
`pkg/storer/netstore.go`), and each download request decides whether to cache.

## 2. Evidence for research questions 1 and 2

**Question 1, is there anything to serve.** An operator's Swarm Desktop light node
running wasp v0.1.3 holds 907,778 cached chunks against its limit of 1,000,000,
about 3.7 GB. All of it is downloaded content. An uploaded chunk is deleted from the
store once it has synced, not moved into the cache (`upload.Report` in
`pkg/storer/internal/upload/uploadstore.go`). This is one node at one moment. It
does not replace the 7-day, overlap and replay measurements the issue asks for.

**Question 2, why the blocklist is there.** It arrived with the light-node work in
2021 ([ethersphere/bee#1242](https://github.com/ethersphere/bee/issues/1242)). The
design comment on that issue gives the reason: "Light node does not forward,
therefore full node does not ask light node for chunks." So the blocklist rests on
an assumption about forwarding. It is neither a defence against abuse nor an
accounting rule. The same comment said a light node should reset such a request
rather than block the asker, because blocking can be abused. The code blocks anyway.
A cache-only answer path removes the forwarding assumption.

Two more facts shape the design:

- A retrieval reply in `v2.8.2` carries only chunk data (`pb.Delivery{Data}`), with
  no postage stamp, so a cached chunk can be served as it is.
- On a local miss, the stock retrieval handler forwards the request into the network
  and charges the asker for it. A light node that reused that handler would become a
  forwarding node. A cache-only path is therefore required, not optional.

## 3. The live-event case

At a live event, video is streamed onto Swarm and viewers watch through nodes of
their own. [weeb-3](https://github.com/lat-murmeldjur/weeb-3) is one example: a
community Swarm client that runs in the browser and behaves as a light node. It reaches full
nodes over WebSocket, because a browser cannot open raw TCP connections or accept
incoming ones. A local load test of weeb-3 measured 2.3 to 2.9 MB of retrieval
traffic per second of video delivered, per viewer.

Two properties matter:

- **The content really is popular.** Every viewer requests the same segments within
  seconds of each other. For a live stream, the issue's premise that many light
  nodes cache the same chunks holds more strongly than for ordinary browsing.
- **The overload is the number of connections, and accounting sets that number.**

### Why each viewer needs so many connections

- A full node lets each light peer use 450,000 accounting units per second for
  free: its refresh rate of 4,500,000 divided by the light factor of 10
  (`pkg/node/node.go:211-213`).
- A chunk costs (32 - proximity) x 10,000 units. Proximity is the number of leading
  address bits that the serving node shares with the chunk. For the closest
  connected peer, a chunk typically costs 240,000 to 270,000 units.
- So one full node serves a light peer about 1.4 to 2.1 chunks per second for free,
  roughly 6 to 9 KB/s.
- SWAP cheques (bee's off-chain payment channel) are off by default
  (`swap-enable: false`), so a light node gets only this free allowance.
- A 5 Mbit/s stream is about 153 chunks per second before any overhead. On the free
  allowance alone, that needs about 73 to 109 full-node peers.
- weeb-3 aims for 200 peers per viewer and uses the same 450,000 rate, hard-coded in
  [`src/accounting.rs`](https://github.com/lat-murmeldjur/weeb-3/blob/main/src/accounting.rs).
- The viewer also asks another peer for every second it waits for an answer
  (`preemptiveInterval`). A node that forwards the request can pass it to up to 2
  peers at once. Each extra request uses allowance at one more peer.

### A ceiling for the whole network

- Each full node accepts 100 light peers by default, and over that limit it evicts
  one at random.
- Swarm's State of the Network reports put reachable full nodes at about 4,000, so
  the network has roughly 400,000 light-peer slots.
- At 100 to 200 slots per viewer, that is about 2,000 to 4,000 concurrent viewers
  across the entire network. Beyond that, full nodes start evicting light peers at
  random.

This is an estimate. It assumes viewers spread evenly and every full node accepts
light peers.

## 4. What this means for the thesis

- **At stock limits, light-node serving does not reduce connections.** A serving
  light node would grant the same free rate, so each connection still carries 6 to
  9 KB/s, and viewers would still need about 100 peers. The only difference is that
  some of those peers could be light nodes.
- **Full nodes already spread popular chunks.** A full node caches every chunk it
  forwards (`cache-retrieval`, default on), so a popular chunk ends up in many full
  nodes' caches along the request paths. At stock limits, light-node serving adds
  serving capacity close to the viewers. It does not add a mechanism that is missing
  today.
- **The limits are ours to set.** In wasp's own build, three values can become
  settings on wasp nodes, and together they decide how much one connection can
  carry:
  - the free rate granted to light peers, 450,000 units per second today;
  - the payment threshold a node offers to light peers, 1,350,000 today
    (`pkg/accounting/accounting.go:218`);
  - `light-node-limit`, which is already a setting.
- **Rate and threshold have to be raised together.** A light peer can owe at most
  about one threshold, and it repays at most about once per second, so the threshold
  also caps throughput per connection. This is inferred from the code, not measured.
  For example, if a serving node granted light peers the full-node rate of 4,500,000
  per second with a matching threshold, one connection would carry about 17 to 19
  chunks per second, roughly 70 KB/s. A 5 Mbit/s stream would then need about 8 or 9
  connections instead of 73 to 109.
- **The key unknown is the viewer's side.** The serving node decides what it offers
  and what it accepts. The viewer decides when, and how much, to repay. bee light
  nodes repay at most about once per second, and weeb-3 derives its repayment
  triggers from its hard-coded 450,000. So a stock viewer may not use a higher
  allowance. weeb-3 would then need a matching change, and bee light viewers need
  checking. This decides options B and C below, so it should be measured first.
- **A serving light node can also be capped.** Since the build is ours, a serving
  light node can limit its upload rate, the number of peers it serves, and which
  content it serves. That keeps a desktop machine from being overrun.
- **So, for live events:** the thesis is right about the content. At stock limits it
  is wrong about the bottleneck. With raised limits on the serving side, and viewers
  able to use them, fewer and wider connections become possible. Serving from light
  caches then adds capacity close to the viewers, at a scale that event nodes alone
  would not cover.

## 5. Options, from least to most change

**Table: ways to take load off full nodes during a live event, compared with bee
v2.8.2 behaviour**

| Option | What changes | Fewer connections per viewer? | Wire protocol change? | Main risk |
|---|---|---|---|---|
| A. Viewers pay | Viewers enable SWAP with a funded chequebook, so bandwidth is no longer limited to the free allowance. | Yes | No | Needs on-chain setup per viewer. Browser clients may not support it. |
| B. Event cache node | A wasp full node run for the event: a large cache (`cache-capacity`) and a raised `light-node-limit`, both existing settings, plus a raised free rate and threshold for light peers, which are new settings. Viewers prefer it as a peer. | Yes, if viewers use the raised allowance | Probably not; to confirm | A single point of failure, and the event operator gives the bandwidth away. |
| C. Light nodes serve over connections they opened (this issue's narrow form) | A cache-only handler without forwarding; the light-node blocklist lifted for retrieval; accounting fixed so that a serving light node is not blocklisted; full nodes ask connected wasp light peers first; serving caps and raised limits on the serving light node. | Only with raised limits on the serving side | Plausibly not; to confirm | Privacy, resources on desktop machines, mixed versions. |
| D. Viewers exchange with each other ("the whole stream over one connection") | Viewer-to-viewer connections: desktops need hole punching or a relay, and browsers need WebRTC, while bee's libp2p has only TCP and WebSocket. A way for viewers to find each other, for example a rendezvous topic per stream over GSOC or PSS, since no chunk-announcement mechanism exists. An accounting mode between viewers, such as exchange in both directions. | Yes | Yes, new protocols | Largest scope and largest attack surface. |

A full node that holds the whole event in its cache and delivers the stream over one
connection is option B. It needs no light-node serving at all, only raised limits and
a way for viewers to prefer that node. A class of event caching nodes would be a
later version of B.

## 6. What could break

- **A light node that serves a full node gets blocklisted by it.** A light node
  accepts repayment at 450,000 per second, while a full node that owes it expects
  about 4,500,000 per second to be accepted. The full node would then blocklist the
  light node with "failed to meet expectation for allowance"
  (`pkg/accounting/accounting.go:1131-1142`). Option C must change how the rate is
  chosen. The code is verified; the outcome is inferred.
- **Mixed versions.** Stock light nodes blocklist any peer that asks, and stock full
  nodes never ask. So option C offloads only between wasp full nodes and wasp light
  nodes, and a wasp full node must not ask stock light nodes. The libp2p user agent
  now carries `wasp/<version>`, which is one way to tell them apart.
- **Free bandwidth is a cost.** Raising the free rate gives away the serving
  operator's bandwidth to anyone who connects, including peers who only take.
  Following rule 8, each new setting must state what raising and lowering it costs.
- **Privacy.** A node that answers from its cache reveals what its operator watched.
  Serving should be limited to content the operator opted into, such as the event
  stream.
- **Resources on the light node.** Home upload bandwidth is small, serving competes
  with the viewer's own playback, and laptops run on battery. Every cache hit costs
  three index writes: it deletes the old access-order entry, writes a new one, and
  rewrites the cache entry (`Cache.Getter`).
- **Abuse.** A light node's identity is cheap to create, and today the blocklist is a
  light node's only defence against unwanted requests. Serving needs rate limits per
  asker.
- **Latency.** A miss at a light node costs one extra round trip. The viewer's own
  1-second retry limits the damage.
- **Churn.** Viewers leave when the event ends, and they all leave at the same
  moment.
- **Not at risk: integrity.** Every chunk is checked against its address, so a light
  node cannot serve corrupted data.

## 7. Suggested changes to the research plan

1. **Add a question before question 1: what limits a viewer, connections or
   bandwidth?** During a real stream, measure a viewer's throughput per connection
   and how many peers it needs.
2. **Measure the raised-limit case first.** On one wasp full node, raise the free
   rate and the threshold for light peers, as new settings per rule 8. Connect a
   stock bee light viewer and a weeb-3 viewer, and measure throughput per
   connection. This decides options B and C.
3. **Keep the replayed hit-rate measurement, but run it against event traffic,**
   where the popular-content premise is strongest.
4. **Specify the serving caps for light nodes:** upload budget, peers served and
   opt-in content, each with the cost of raising and lowering it.
5. **State in the spec's rollout section that the change is two-sided:** the offload
   is limited by how many full and light nodes run it.

Generated with help of AI.
