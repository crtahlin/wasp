# Content providers: passive sharing of cached content

Issue: [#290](https://github.com/crtahlin/wasp/issues/290). Builds on
[spec.md](spec.md), whose phase 1 merged as `5608aa53`. Related:
[#282](https://github.com/crtahlin/wasp/issues/282) (light-node cache serving).

Status: analysis for review. It chooses a direction. Each part below still gets its
own spec change, merged before any code (rule 2).

All code references are to wasp `main` at `86119b10`, whose base is bee `v2.8.2`.

## Terms

The terms of [spec.md](spec.md) apply. In addition:

- **Relaying node**: a node that forwards a retrieval request for a peer. The spec
  calls such a request a forwarded request.
- **First hop**: the peer that a requester sends a chunk request to.
- **Honeypot**: a node that offers content in order to learn who asks for it.
- **Bloom filter**: a compact bit array built from a set of addresses. It answers
  "possibly in the set" or "certainly not in the set". The share of wrong
  "possibly" answers is its false-positive rate.
- **Hot set**: the chunks a node accessed most recently.

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

A fourth limit is technical. A node knows the reference, the root of a file's chunk
tree, only for content fetched through its own API. For a chunk it only relayed, it
cannot know the file: a chunk never points to its parent. Announcing every chunk
instead would be far too much. A 1 GB file is about 262,000 chunks.

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

**2. Light nodes sit behind their full peers.**
- A node that dials a light node drops the connection after the handshake, with
  `ErrDialLightNode` (`pkg/p2p/libp2p/libp2p.go:1167-1170`). So a light node only
  ever has connections that it opened itself, and only to full nodes.
- A full node accepts at most 100 light peers by default, and keeps them in a
  separate list (`libp2p.go:703-724`).
- Every byte a light node uploads therefore goes to a full node it is connected to.
  The busiest link in a live event is the upload from a full node to its own light
  peers. As an illustration, 100 viewers of a 5 Mbit/s stream need 500 Mbit/s from
  that one node. No light node can take any of that load, because light nodes have
  no connections to each other.
- A light node's cache can only save its full peer a fetch from further away. The
  full peer usually already has the chunk: it cached it when it relayed it
  (finding 1).

**3. Stock nodes store only stamped chunks.**
- Anything a light node or any other node writes into Swarm needs a stamp, or the
  nodes of the chunk's neighbourhood refuse it.
- A record kept without a stamp at some address would therefore work only if the
  node responsible for that address runs wasp. While wasp is rare, that almost
  never holds.
- So stamp-free discovery can only happen between peers that are already connected
  to each other.

**4. The p2p layer allows everything below.**
- A full node can open a stream to an inbound light peer: `NewStream` has no light
  node check (`libp2p.go:1335-1378`). Pricing and hive already do this.
- A protocol in a new package is not part of the protocol-freeze fingerprint
  (`scripts/protocol-freeze.sh:43-56` lists the fingerprinted files).
- Stock nodes ignore stream header keys they do not know
  (`pkg/p2p/libp2p/headers.go:34-63`).
- Light nodes refuse every retrieval stream today and blocklist the peer that opens
  one (`pkg/node/node.go:1373`, `pkg/p2p/specwrapper.go:25-31`).
- A light node serving a full node for pay would be blocklisted. The full node
  expects a refresh allowance at its own rate, 4,500,000 units per second, and the
  light node grants only 450,000 (`node.go:232-234`,
  `pkg/accounting/accounting.go:1130-1142`). Light-node serving works only if both
  sides skip accounting.

## The mechanism

Three parts. Each works without the others, and each falls back to normal retrieval.

### A. Ask through your usual peer, not the provider

This answers objection 2.

**Today (phase 1).** A requester that knows a provider, from a hint or a lookup,
opens its own retrieval stream to it with the header `wasp-local-only`. The provider
learns who the requester is and what it downloads.

**Proposed.** The requester does not contact the provider. It sends its request to a
connected full peer, as it always does, and adds a header naming at most 2 provider
overlays. A wasp relaying node that receives such a request:

1. takes the named providers that are connected full nodes, leaving out the peer
   that sent the request (`preferredCandidates`, `pkg/retrieval/preferred.go:183`);
2. asks them first with the local-only header, one after the other, with the
   existing 500 ms wait (`localOnlyHeaders` and `retrievePreferred`,
   `preferred.go:175` and `:217`);
3. on a miss, forwards the request as usual;
4. never passes the provider header on, so it acts only one hop away from the
   requester and cannot create loops.

**Choosing the first hop.** A stock peer ignores the header and forwards normally.
A requester therefore sends a request that names providers to its closest connected
peer that runs wasp, and to its usual closest peer when it has none. It recognises
wasp peers by the user agent that libp2p exchanges, which contains `wasp/`
(`libp2p.go:1586-1587`, read by `peerUserAgent` at `libp2p.go:1495`).

**Accounting** stays ordinary. The requester pays the relaying node its usual price.
The relaying node pays the provider. `retrievePreferred` must pass the real origin
flag to the credit instead of always `true`.

**Who learns what:**

| Party | Phase 1, direct | Part A, relayed |
|---|---|---|
| Provider | the requester's overlay, network address and requests | only the relaying node, which also relays for others |
| Relaying node | nothing new | that these requests go with this provider. For a light requester it already sees every request, since light nodes never relay. |
| Other nodes | nothing | nothing |

A honeypot provider therefore learns only which relaying nodes ask, as any node on a
retrieval path does today.

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

**How it differs from what the spec rejected.** The spec rejected a protocol that
asks connected peers "do you have K" (spec.md, section 1b). Such a query needs the
reference and costs a message per question. A summary is sent without being asked,
once per period, and is then used locally for any chunk.

**Content.**
- The cache and the pins, not the reserve. Normal routing already finds reserve
  chunks, because they sit in their own neighbourhood.
- The hot set first. The cache order index is keyed by access time
  (`pkg/storer/internal/cache/cache.go:371-392`), so the most recently used chunks
  can be taken up to a cap.
- As an illustration, 100,000 addresses at a 1% false-positive rate take about
  120 KB. 1,000,000, the default cache capacity, take about 1.2 MB.

**Format.** A bit array. Chunk addresses are already uniform hashes, so the bit
positions can be taken from slices of the address bytes. This needs no new
dependency; `go.mod` has no Bloom filter library.

**Exchange.**
- A new protocol in its own package.
- A node sends its summary to each connected wasp full peer when they connect and
  then every T minutes.
- A stock peer fails the stream negotiation, which surfaces as an
  `IncompatibleStreamError` (`libp2p.go:1393-1398`). The sender remembers it and does
  not try again for that connection.

**Use.** On a local miss, whether the request is its own or relayed, and before
normal peer selection, a node asks at most one connected peer whose summary claims
the chunk. It uses the local-only header and the phase 1 rules: 500 ms wait,
demotion after repeated misses, and no `errSkip` entry.

**Protection.** A summary with more than half its bits set is rejected, because a
summary that claims everything would draw in all of a peer's misses. Stale entries
cost only a local-only miss.

**Privacy.** A summary shows connected wasp peers roughly what the node has recently
used. Those peers already see many of its requests. The requester side learns nothing
new, since the node asks its own peers, as in normal routing. Sending summaries is a
setting of its own, off by default.

**Light nodes** could take part. That would mean:
- accepting a retrieval stream only when it carries the local-only header and comes
  from a full peer, instead of blocklisting (`node.go:1373`);
- serving free of charge on both sides (finding 4).

By finding 2 the gain is small. It is included only if the simulation below shows
that it matters.

## User stories

**1. A light viewer who trusts no one new.**
- Ana watches a live event in a desktop application that runs a light node.
- Her node reads the event's provider records, published by the broadcaster's full
  node, and sends her requests, naming that provider, to her connected wasp full
  peer F.
- F fetches from the provider in one hop. The provider sees F, never Ana.
- If none of her peers runs wasp, she gets exactly today's behaviour.

**2. A gateway that shares its cache without an administrator.**
- A community gateway serves a popular site to many users through its API.
- Once the site has been requested often and the gateway holds it in full, the
  gateway announces it on its own.
- No one pinned it. When it leaves the cache, the announcement ends within a day.

**3. Content whose stamp expired.**
- A dataset's stamp expired and its neighbourhood deleted it, but a wasp node still
  holds it, pinned or cached.
- A requester finds it through a record, if that node announces (part B), or
  through a summary, if it is connected to that node (part C).
- Otherwise the request fails, as it does today.

## Options considered and not chosen

- **Stamp-free records kept at the reference's neighbourhood.** They work only where
  that neighbourhood runs wasp (finding 3). Without stamps, anyone can also fill
  them.
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
    decline it in stream negotiation;
  - light nodes, only if they take part, accept a retrieval stream that carries the
    local-only header from a full peer. A stock full node never sends that header,
    so its behaviour towards a light node is unchanged.

`.github/protocol-freeze.lock` does not change for any part. Each part's spec change
carries its own protocol impact section and a mixed-version test like the one for
phase 1 (`mixed-version.md`).

The new names that other nodes see should not contain "wasp", so that other clients
can adopt them. This depends on the rename of the phase 1 names
(`wasp-local-only`, `wasp-providers-v1`, `wasp-provider-index-v1`), still to be
decided.

## Measurement

**Before part C is specified.** Its value depends on how often a connected peer
holds a chunk that the node lacks. That cannot be measured on the network while few
nodes run wasp, so the decision rests on a simulation:
- a kademlia topology taken from a bench node's peer list;
- requests drawn from a Zipf popularity model, where a few items receive most of the
  requests;
- default cache sizes, with on-path caching modelled, because finding 1 is what
  part C competes with;
- the result: the share of misses a connected peer's summary could have answered,
  with 0%, 10% and 50% of nodes running wasp, and separately for light peers.

A simulation informs the decision; it is not a performance claim.

**After parts A and B are built.** On the bench, following
`docs/agent-playbooks/test-bench.md`:
- time to first byte and total download time;
- with and without a wasp relaying node and a provider;
- three runs per condition, reported with the spread.

This joins the phase 1 measurement that is still to run.

## Order of work

1. Part A: a spec change, then code on top of phase 1.
2. Part B: a spec change, then code.
3. Part C: the simulation first, then a spec only if the result is useful.
4. Light nodes serving: last, or not at all.

**Open questions for the specs:**
- the header names (see Protocol impact);
- whether a relaying node dials a named provider that is not connected;
- N, the size cap and the reference cap for part B;
- the summary cap and period for part C.

Generated with help of AI.
