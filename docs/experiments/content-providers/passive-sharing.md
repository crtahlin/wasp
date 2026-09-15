# Content providers: passive sharing of cached content

Issue: [#290](https://github.com/crtahlin/wasp/issues/290). Builds on
[spec.md](spec.md), whose phase 1 merged as `5608aa53`. Related:
[#282](https://github.com/crtahlin/wasp/issues/282) (light-node cache serving),
[#296](https://github.com/crtahlin/wasp/issues/296) (a draft SWIP, a Swarm
Improvement Proposal, for the advertising mechanism, kept in this repository).

Status: analysis for review. It chooses a direction. Each part below still gets its
own spec change, merged before any code (rule 2), and none starts before the phase 1
measurement has run (see Order of work).

All code references are to wasp `main` at `86119b10`, whose base is bee `v2.8.2`.

## Terms

The terms of [spec.md](spec.md) apply. In addition:

- **Reference**: the address of the root chunk of a file's chunk tree. It is what a
  user passes to `/bzz` or `/bytes`.
- **Relaying node**: a node that forwards a retrieval request for a peer. The spec
  calls such a request a forwarded request.
- **First hop**: the peer that a requester sends a chunk request to.
- **Honeypot**: a node that offers content in order to learn who asks for it.
- **Bloom filter**: a compact bit array built from a set of addresses. It answers
  "possibly in the set" or "certainly not in the set". The share of wrong
  "possibly" answers is its false-positive rate.
- **Summary**: a Bloom filter of the chunk addresses a node holds, sent to its
  connected peers (part C).
- **Hot set**: the chunks a node accessed most recently.
- **User agent**: the free-text name and version a libp2p node announces to its
  peers when they connect.
- **Stream negotiation**: the step at the start of every libp2p stream where both
  sides agree on the protocol. A peer that does not know the protocol refuses it
  there.
- **Time to first byte**: the time from a download request until its first byte
  arrives.

## Why phase 1 is not enough

Phase 1 lets an operator announce content that they pinned, with records that need a
postage stamp, and lets a requester that finds such a record contact the provider
directly. A review of phase 1 raised three objections to it as the general
mechanism:

1. **It needs someone to choose.** An operator must pin and announce each
   reference. Content a node holds only in its cache is never shared, because
   nobody would think to announce it.
2. **The requester must trust the provider.** A requester that contacts a provider
   directly tells it its overlay, its network address and what it wants. A node
   that announces popular content could be a honeypot.
3. **Light nodes have no stamps.** They cannot write a record, and in phase 1 they
   cannot announce at all.

A fourth limit is technical. A node knows the reference only for content fetched
through its own API. For a chunk it only relayed, it cannot know the file: a chunk
never points to its parent. Announcing every chunk instead would be far too much. A
1 GiB file is 262,144 data chunks, plus the chunks of its tree.

## What Swarm already does

These findings decide which gaps are worth closing. Each was checked in the code.

**1. Relaying nodes already share their cache.**
- A node caches every chunk it relays, `pkg/retrieval/retrieval.go:593`, unless the
  setting `cache-retrieval` is off. It is on by default
  (`cmd/bee/cmd/cmd.go:414`).
- A node answers from everything it holds, cache and pins included
  (`retrieval.go:554`, `pkg/storer/internal/cache/cache.go:128-147`).
- So full nodes already share cached, unpinned content, with no one choosing, along
  the paths that requests take. Popular content is cached at every node on those
  paths. The gaps are: copies that are not on the path, content whose stamp has
  expired, and the privacy of the requester.

**2. Light nodes reach the network only through full nodes.**
- A node that dials a light node drops the connection after the handshake, with
  `ErrDialLightNode` (`pkg/p2p/libp2p/libp2p.go:1167-1170`). So a light node only
  ever has connections that it opened itself, and only to full nodes.
- A full node accepts at most 100 light peers by default (`cmd/bee/cmd/cmd.go:403`)
  and keeps them in a separate list (`libp2p.go:703-724`).
- Every byte a light node receives or uploads crosses a link between it and a full
  node. The busiest link in a live event is the upload from a full node to its own
  light peers. As an illustration, 100 viewers of a 5 Mbit/s stream need up to
  500 Mbit/s from that one node, if all of them fetch through it. No light node can
  take any of that load, because light nodes have no connections to each other.
- A light node's cache can only save its full peer a fetch from further away. The
  full peer usually already has the chunk: it cached it when it relayed it
  (finding 1).

**3. Stock nodes store only stamped chunks.**
- Anything a light node or any other node writes into Swarm needs a stamp, or the
  nodes of the chunk's neighborhood refuse it.
- A record kept without a stamp at some address would therefore work only if the
  node responsible for that address runs wasp. While wasp is rare, that almost
  never holds.
- So stamp-free discovery can only happen between peers that are already connected
  to each other.

**4. The p2p layer allows everything below, with limits.**
- A full node can open a stream to an inbound light peer: `NewStream` has no light
  node check (`libp2p.go:1335-1378`). Pricing and hive already do this
  (`pkg/pricing/pricing.go:131`, `pkg/topology/kademlia/kademlia.go:1221`).
- A protocol in a new package is not part of the protocol-freeze fingerprint
  (`scripts/protocol-freeze.sh:39-77` lists everything that is fingerprinted).
- Stock nodes ignore stream header keys they do not know
  (`pkg/p2p/libp2p/headers.go:34-63`).
- Light nodes refuse every retrieval stream today and blocklist the peer that opens
  one (`pkg/node/node.go:1373`, `pkg/p2p/specwrapper.go:25-31`).
- A light node that serves a full node for pay is blocklisted once the full node's
  debt to it grows past what it allows.
  - A light node grants a refresh of 450,000 units per second
    (`node.go:232-234`, `node.go:1219-1221`, `node.go:1240`).
  - The full node checks the refresh it receives against the full-node rate,
    4,500,000, for every peer (`pkg/accounting/accounting.go:1131-1142`). The debit
    side already uses the light rate for light peers (`accounting.go:741-743`,
    `accounting.go:1337-1339`).
  - Stock Bee never meets this case, since full nodes never owe light nodes.
  - Light-node serving therefore needs one of two things: serving free of charge on
    both sides, or a wasp full node that checks light peers against the light rate.

## The mechanism

Three parts. Each works without the others, and each falls back to normal retrieval.

### A. Ask through a relaying peer, not the provider

This answers objection 2.

**Today (phase 1).** A requester that knows a provider, from a hint or a lookup,
opens its own retrieval stream to it with the header `wasp-local-only`. The provider
learns who the requester is and what it downloads.

**Proposed.** The requester does not contact the provider. It sends its request to a
connected full peer and adds a header naming at most 2 provider overlays. A wasp
relaying node that receives such a request:

1. takes the named providers that are connected full nodes, leaving out the peer
   that sent the request;
2. asks them first with the local-only header, one after the other, with the
   existing 500 ms wait (`localOnlyHeaders` and `retrievePreferred`,
   `pkg/retrieval/preferred.go:175` and `:217`);
3. on a miss, forwards the request as usual;
4. never passes the provider header on, so it acts only one hop away from the
   requester and cannot create loops.

This reverses a phase 1 rule: "Forwarded requests never carry a preferred set"
(spec.md, section 2). Part A's spec change amends that section.

**Code that changes**, for the spec:
- `preferredCandidates` (`preferred.go:183`) receives only the `errSkip` list today.
  It must also leave out the sending peer.
- The preferred path runs only for requests the node starts itself
  (`retrieval.go:171`).
- Relayed requests share one singleflight key per chunk (`retrieval.go:177-183`), so
  a request that names providers would be merged with one that does not. The key
  must include the fingerprint of the named providers, as it does for origin
  requests.
- `retrievePreferred` passes `true` as the origin flag to the credit
  (`preferred.go:224`). It must pass the real flag.

**Choosing the first hop.** A stock peer ignores the header and forwards normally.
A requester therefore sends a request that names providers to the connected peer
closest to the chunk among those that run wasp, and to its usual closest peer when it
has none. It recognizes wasp peers by their user agent, which contains `wasp/`
(`libp2p.go:1586-1590`). The function that reads it, `peerUserAgent`
(`libp2p.go:1495`), is private and used only for logging (`libp2p.go:765`, `:1241`),
so part A needs a new accessor.

**Accounting.** The relaying node charges the requester its own price for the chunk
(`retrieval.go:572`) and pays the provider the provider's price (`retrieval.go:459`).
A price falls as the node gets closer to the chunk (`pkg/pricer/pricer.go:34-36`).
- Normal forwarding goes to a peer closer to the chunk, so a relaying node earns more
  than it pays.
- A named provider may be further from the chunk than the relaying node, and then the
  relaying node loses money on that chunk. For a random chunk, the provider is the
  closer of the two about half of the time.
- A slow provider attempt is not canceled (`preferred.go:34-36`), so the relaying
  node may pay both the provider and the next hop while it is paid once.
- The requester starts a parallel attempt after 1 s (`retrieval.go:146`). If the
  relaying node's provider attempts take that long, the requester may pay twice as
  well.
- Rules to choose between in the spec:
  - ask only providers whose price for the chunk is no higher than the relaying
    node's own;
  - or accept a loss, capped per requesting peer and period;
  - in both cases, keep the relaying node's provider attempts well under 1 s,
    for example one provider and 500 ms.

**Who learns what:**

| Party | Phase 1, direct | Part A, relayed |
|---|---|---|
| Provider | the requester's overlay, network address and requests | the relaying node, and that the requester is the relaying node itself or one of its direct peers |
| Relaying node, full requester | not involved | that its peer started these requests, because the header is never passed on and so marks the origin; and that they are for content this provider holds |
| Relaying node, light requester | not involved | nothing new about the origin, since every request from a light peer is that peer's own; that these requests are for content this provider holds |
| Other nodes | nothing | nothing |

So part A does not remove the leak of the origin; it moves it. In phase 1 the origin
is revealed to a node chosen because of the content, which may be a honeypot. In
part A it is revealed to a peer the requester is already connected to, chosen by the
requester.

**Remaining risks**, for the spec:
- **Concentration.** Every request that names a provider goes to the few connected
  peers that run wasp. They see a larger share of the requester's downloads than
  normal routing would give them.
- **A false user agent.** Any node can put `wasp/` in its user agent, so a honeypot
  can offer itself as the relaying node. Spreading these requests over all connected
  wasp peers, by closeness to each chunk, limits what one of them sees. It does not
  stop a node that holds many connections to the requester.
- **An option against both.** In the approach known as Crowds (Reiter and Rubin,
  1998), each relaying node passes a request to another willing peer with some
  probability before acting on it. The first hop then cannot tell whether its peer
  started the request. This adds hops and needs a hop limit. It is listed as an open
  question.
- **Shared miss limit.** The provider's limit on free misses per peer
  (spec.md, section 3) counts all of one relaying node's requesters together.

**Contacting the provider directly** stays available as a separate choice of the
requester, for when no wasp peer is connected. It is not the default.

**Open for the spec:** whether a relaying node dials a named provider that is not
connected. Only already connected providers is safe, but it will rarely match.
Dialing from the address book, with `forceConnection=false` and a limit per
requesting peer, keeps a peer from making the node dial arbitrary addresses.

### B. Automatic announcements by nodes that have stamps

This answers objection 1 for nodes that can pay for records.

A full node with a new setting, off by default, announces without anyone choosing:
- the references that its own API served in full, through `/bzz` or `/bytes`, at
  least N times in the current window, and that it still holds in full;
- its pins, as in phase 1.

**Details.**
- **Holding in full** is checked by walking the reference's chunk tree against the
  local store only, once per candidate per window. Candidates above a size cap are
  skipped.
- **Stopping** needs no action. A reference the node no longer holds is not written
  for the next window, and its last record ends with its window, within 24 hours.
- **Caps** limit the references and the records written per window, so the stamp
  cost is bounded. Each reference costs about 2 records and 2 index writes a day,
  as in phase 1.
- **The batch** comes from configuration, since no operator is present to pass one.
- **The format does not change:** the record SOC and the index slots of phase 1.

**Who this suits:** gateways, publishers and applications that run their own full
node. They are the nodes that know references and have stamps. Relaying nodes know
no references, and light nodes have no stamps, so neither announces.

**Privacy.** An announcement reveals that this node's API served the reference often.
For a gateway with many users that is aggregate popularity. For a personal node it
reveals what its owner watches. This is why the setting is separate and off by
default. It changes the spec's rule that a provider announces pinned content only
(spec.md, section 3), for the nodes that choose it.

**Light nodes as consumers.** Reading records needs no stamp: a lookup is ordinary
retrieval. A light node can therefore find providers and use them through part A.

### C. Chunk summaries between connected peers

This works without stamps and without knowing any reference. It is the "summary
cache" of cooperating web caches (Fan, Cao, Almeida and Broder, 2000), applied to
Swarm's request path.

**How it relates to what the spec rejected.** The spec rejected a protocol that asks
connected wasp peers "do you have K", because "it adds nothing that the local-only
header does not, and it cannot reach beyond connected peers" (spec.md, section 1b).
- The second reason applies to part C as well. Summaries reach only connected peers.
- The first does not. With the local-only header alone, finding a copy among
  connected peers takes one probe per chunk per peer. A summary tells the node which
  peer to probe, at most one probe per miss, and it needs no K.

**Content.**
- Chunks the node relayed for others, and its pins. Not the reserve: normal routing
  already finds reserve chunks, because they are stored in their own neighborhood.
- **Not chunks of the node's own downloads.** Downloads through the node's API enter
  its cache by default (`pkg/storer/netstore.go:107-125`). If they were summarized,
  any connected wasp peer could test whether this node downloaded a known reference.
  Across a whole file at a 1% false-positive rate, the answer is close to certain.
  This is the spec's reason never to announce the cache (spec.md, section 3). The
  cache does not record where a chunk came from today, so the spec must add that
  marker, or a node that downloads through its API must not send summaries.
- The hot set first. The cache order index is keyed by access time
  (`pkg/storer/internal/cache/cache.go:371-392`), so the most recently used chunks
  can be taken up to a cap.
- As an illustration, 100,000 addresses at a 1% false-positive rate take about
  120 KB. 1,000,000, the default cache capacity, take about 1.2 MB.

**Format.**
- A bit array. The sender sizes it so that at most half of its bits are set at the
  cap.
- Bit positions come from the trailing bytes of each address. The leading bytes
  cluster, because a node holds mostly chunks near its own address.
- This needs no new dependency; `go.mod` has no Bloom filter library.

**Exchange.**
- A new protocol in its own package.
- A node sends its summary to each connected wasp full peer when they connect and
  then every T minutes.
- A stock peer refuses the protocol in stream negotiation, which surfaces as an
  `IncompatibleStreamError` (`libp2p.go:1393-1398`). The sender remembers it and does
  not try again for that connection.

**Use.** On a local miss, whether the request is its own or relayed, and before
normal peer selection, a node asks at most one connected peer whose summary claims
the chunk. It uses the local-only header and the phase 1 rules: 500 ms wait,
demotion after repeated misses, and no `errSkip` entry.

**Protection.** A receiver computes the false-positive rate that a summary's share of
set bits implies, and rejects the summary when that rate is above 5%. With 7 bit
positions per address, that is a summary with more than about 65% of its bits set.
A summary that claims everything would otherwise attract all of a peer's misses.
Stale entries cost only a local-only miss.

**Privacy.**
- A summary shows connected wasp peers which chunks the node relayed for others and
  which it pinned. A peer on a request path already sees only the requests routed
  to it, so this is new information, and sending summaries is a setting of its own,
  off by default.
- The requester side learns nothing new, since the node asks its own peers, as in
  normal routing.

**Light nodes** could take part. That would mean:
- accepting a retrieval stream only when it carries the local-only header and comes
  from a full peer, instead of blocklisting (`node.go:1373`);
- serving free of charge, or a wasp full node checking light peers against the light
  rate (finding 4).

By finding 2 the gain is small. Light-node providers stay in phase 4 of the spec,
gated on measurement, as the spec already says.

## User stories

**1. A light viewer who never contacts the provider.**
- Ana watches a live event in a desktop application that runs a light node.
- Her node reads the event's provider records, published by the broadcaster's full
  node, and sends her requests, naming that provider, to her connected wasp full
  peers.
- Each of them fetches from the provider in one hop. The provider sees those full
  nodes, never Ana.
- If none of her peers runs wasp, she gets exactly today's behavior.

**2. A gateway that shares its cache without an administrator.**
- A community gateway serves a popular site to many users through its API.
- Once the site has been requested often and the gateway holds it in full, the
  gateway announces it on its own.
- No one pinned it. When it leaves the cache, the announcement ends within a day.

**3. Content whose stamp expired.**
- A dataset's stamp expired and its neighborhood deleted it, but a wasp node still
  holds it, pinned or relayed.
- A requester finds it through a record, if that node announces (part B), or
  through a summary, if it is connected to that node (part C).
- Otherwise the request fails, as it does today.

## Options considered and not chosen

- **Stamp-free records kept at the reference's neighborhood.** They work only where
  that neighborhood runs wasp (finding 3). Without stamps, anyone could also write
  false entries into them.
- **Requests as announcements.** A requester marks its request "I will hold this",
  and the node that serves the root chunk lists it as a provider. This reveals each
  viewer to every node on the path, and the serving node is usually stock.
- **Light nodes as the main source.** They cannot relieve their full peers
  (finding 2).
- **A full node that pays stamps for its light peers' records.** It costs the full
  node stamps for others' choices, invites spam, and publishes what each light node
  holds.
- **Direct contact with providers as the default.** This exposes the requester to
  honeypots (objection 2). It is kept as an explicit choice.

## Protocol impact

- **A:** a new stream header key on retrieval requests. Stock nodes ignore it
  (finding 4), and a wasp relaying node never forwards it.
- **B:** no wire change. Only more records in the phase 1 format.
- **C:**
  - a new protocol ID in a new package, outside the frozen surface. Stock peers
    refuse it in stream negotiation;
  - light nodes, only if they take part, accept a retrieval stream that carries the
    local-only header from a full peer. A stock full node never sends that header,
    so its behavior toward a light node is unchanged.

`.github/protocol-freeze.lock` does not change for any part. Each part's spec change
carries its own protocol impact section and a mixed-version test like the one for
phase 1 (`mixed-version.md`).

The new names that other nodes see should not contain "wasp", so that other clients
can adopt them. This depends on the rename of the phase 1 names
(`wasp-local-only`, `wasp-providers-v1`, `wasp-provider-index-v1`), still to be
decided, and is a precondition for the SWIP draft in #296.

## Measurement

**First, the phase 1 measurement** (spec.md, Measurement). The spec says a negative
result on speed stops work on phase 2 onwards, and keeps the mechanism only for
availability. Passive sharing is filed under phase 2, so it waits for that result.
If speed shows no gain but availability holds, parts A and B are still worth
building for availability: A is how a requester would use a provider without
revealing itself to it, and B is how content that nobody pinned gets announced.

**Before part C is specified.** Its value depends on how often a connected peer
holds a chunk that the node lacks. That cannot be measured on the network while few
nodes run wasp, so the decision rests on a simulation:
- a kademlia topology taken from a bench node's peer list. The list contains real
  overlays, so it stays out of the repository (rule 10);
- requests drawn from a Zipf popularity model, where a few items receive most of the
  requests;
- default cache sizes, with on-path caching modeled, because finding 1 is what
  part C competes with;
- the result: the share of misses a connected peer's summary could have answered,
  with 0%, 10% and 50% of nodes running wasp, and separately for light peers.

A simulation informs the decision; it is not a performance claim.

**After parts A and B are built.** On the bench, following
`docs/agent-playbooks/test-bench.md`:
- time to first byte and total download time;
- with and without a wasp relaying node and a provider;
- three runs per condition, reported with the spread.

## Order of work

1. The phase 1 measurement (task already open under #290).
2. Part A: a spec change, then code.
3. Part B: a spec change, then code.
4. Part C: the simulation first, then a spec only if the result is useful.
5. Light nodes serving: phase 4, or not at all.

**Open questions for the specs:**
- the header names (see Protocol impact);
- the accounting rule for part A: providers no dearer than the relaying node, or a
  capped loss;
- whether part A passes requests on with some probability, as in Crowds;
- whether a relaying node dials a named provider that is not connected;
- N, the size cap and the reference cap for part B;
- how the cache marks relayed chunks, the summary cap and the period for part C.

Generated with help of AI.
