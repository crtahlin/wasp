# Content providers: let a node that holds content be found and asked first

Issue: [#290](https://github.com/crtahlin/wasp/issues/290). Related:
[#282](https://github.com/crtahlin/wasp/issues/282) (light-node cache serving),
[#291](https://github.com/crtahlin/wasp/issues/291) (static peers and pruning).

Status: spec for review. No code lands until this is merged.

All code references are to wasp `main` at `8e53d493`, whose base is bee `v2.8.2`.

## Terms

- **Provider**: a node that holds some content and says so.
- **Requester**: a node downloading that content.
- **Content key K**: what a provider announces. For `/bzz` and `/bytes` it is the
  reference as given, 32 bytes, or 64 for an encrypted reference. For a feed it is
  `keccak256("wasp-feed-v1" || owner || topic)`; feeds are handled in phase 3.
- **SOC**: a single owner chunk, a chunk whose address is derived from an owner
  key and an id, and which carries the owner's signature.
- **Overlay**: a node's Swarm address. **Underlay**: its network address, as a
  libp2p multiaddr.

## Problem

A node that holds some content in full is asked for it only when it happens to lie
on a request's route:

- A requester sends each chunk request to its connected peer closest to the chunk
  address (`closestPeer`, `pkg/retrieval/retrieval.go:388-419`; the origin may
  choose any peer, line 406).
- A node that holds a whole file or a whole live stream therefore answers about one
  chunk in N for a requester with N peers.
- No other node can know that it holds the content at all.

Serving is not the gap:
- The retrieval handler answers from `Lookup()` (`retrieval.go:467`).
- `Lookup()` reads any chunk in the chunk store, whether reserve, pinned or cache
  (`pkg/storer/internal/cache/cache.go:136`).

Pinning already keeps content without a postage stamp:
- `POST /pins/{ref}` walks the reference and stores each chunk with no stamp
  (`pkg/api/pin.go:61-125`, `pkg/storer/internal/pinning/pinning.go:89-122`).
- Pinned chunks are served to peers like any other.

What is missing is a way for a node to say it holds content, and a way for other
nodes to use that.

The idea is the one once called **global pinning**: content that a node pins is
available to, and discoverable by, the whole network, with no stamp paid for the
content. Only the idea is taken here, not any earlier implementation.

## Hypothesis

If a provider can announce that it holds content K, and requesters try it first for
the chunks of K while keeping normal retrieval as the fallback for every chunk it
does not answer:

1. A download of content that the network also holds gets faster, or at least no
   slower, when a provider is close in network terms and holds all of it.
2. Content that the network no longer holds, for example after its batch expired,
   stays retrievable from a provider that pinned it. No stamp is needed for the
   content itself.
3. A live event stream can be carried mostly by a few nodes set up for it, instead of
   being spread by address across the network.
4. Stock Bee nodes see nothing new. A wrong or stale hint costs a requester one fast
   miss per chunk, and after that ordinary retrieval.

Accounting limits hypotheses 1 and 3. Without SWAP, the sustained rate one provider
can give one requester is about the refresh rate divided by the chunk price:
- about 4,500,000 / 320,000 ≈ 14 chunks per second to a full node;
- about 1.4 chunks per second to a light node (`pkg/node/node.go:230-232`,
  `pkg/pricer/pricer.go:35`).

Beyond that, `PrepareCredit` returns an overdraft and the request moves on to normal
retrieval, which is the intended fallback. These figures are read from the code; the
measurement will state the real ones.

## Design

The design has three parts:
- how a requester learns about providers;
- how it prefers them and falls back;
- how a provider answers.

Only wasp changes. Everything is off by default.

### 1. Learning about providers

A requester builds a **preferred set** for each download from two sources.

#### 1a. Explicit hint

A request header on `GET /bzz/...`, `GET /bytes/{ref}`, `GET /chunks/{addr}` and
`GET /feeds/{owner}/{topic}`:

```
Wasp-Providers: <entry>[,<entry>...]
```

Each entry is either a hex overlay or a full multiaddr.
- A multiaddr is dialled as `POST /connect/{multi-address}` does:
  `p2p.Connect`, then `Connected(ctx, peer, true)` (`pkg/api/peer.go:34-42`). The
  overlay is learnt from the handshake.
- An overlay that is not connected is dialled from the address book
  (`pkg/addressbook/addressbook.go:124-139`) if its underlays are known. Otherwise
  it is ignored.
- At most 8 entries are used.

The header is honoured on every wasp node, without any setting: it applies to one
request only, and the caller chose it. A stock node ignores the unknown header.

This is the source for the event case: the player knows which node carries the
stream.

#### 1b. Discovery by content key

Discovery has two layers, so that a record can be trusted even though anyone can
write to the index:

- a record the provider alone can write, which says "this node holds K";
- an open index that lists candidate providers.

**Provider record (authoritative).**
- A SOC owned by the provider's node key, the same key that signs its handshake.
- `id = keccak256("wasp-providers-v1" || K)`.
- The wrapped chunk's payload is JSON:

  ```json
  {
    "v": 1,
    "key": "<hex K>",
    "address": { "overlay": "...", "underlays": ["..."], "signature": "...",
                 "nonce": "...", "timestamp": 0, "chequebook": "..." },
    "capability": "full",
    "expires": 1789500000
  }
  ```

  - `address` is the node's signed `bzz.Address` in its existing JSON form
    (`pkg/bzz/address.go:200`).
  - `capability` is `full`, `partial` or `withdrawn`.
  - `expires` is a Unix time in seconds.

A reader accepts a record only if all of these hold:
1. The SOC is valid. Retrieval already checks this (`retrieval.go:360-364`).
2. `bzz.ParseAddress` verifies `address` for this node's network ID
   (`pkg/bzz/address.go:84`), and the Ethereum address it recovers equals the SOC
   owner. This binds the underlays to the provider's key, so a record cannot point
   traffic at someone else's machine.
3. `key` equals K, `v` is 1, and `capability` is not `withdrawn`.
4. `expires` is in the future and at most 7 days away. A record cannot be replayed
   for longer than its owner allowed.

Only the provider can write its record. A refresh signs a new version at the same
address and stamps it from the same batch:
- The stamper reuses the stamp index it stored for that chunk address and only
  moves the timestamp forward (`pkg/postage/stamper.go:50-55`).
- Every node's reserve then replaces the older version the same way
  (`pkg/storer/internal/reserve/reserve.go:138-214`), and pullsync refuses older
  copies (`pkg/pullsync/pullsync.go:394`). All nodes converge on the newest record.

**Pointer index (open, best effort).** A reader who knows only K cannot compute a
provider's record address, because the owner is part of it. The pointer index
supplies owners:

- **Owner key:** a secp256k1 private key equal to
  `keccak256("wasp-provider-index-v1" || K)`. Anyone can compute it, so anyone can
  read and write the index.
- **Slots:** S = 8, where slot i has
  `id = keccak256("wasp-provider-index-v1" || K || byte(i))`.
- **Slot payload** (JSON):

  ```json
  {
    "v": 1,
    "key": "<hex K>",
    "providers": [ { "owner": "<hex 20-byte address>", "expires": 1789500000 } ]
  }
  ```

  A slot holds at most 32 entries.
- **Writing:** a provider writes to slot `keccak256(owner)[0] mod S`, in four steps:
  1. read the slot;
  2. drop expired entries and its own old entry;
  3. add itself, and if the slot is full, drop the entry that expires first;
  4. write the slot, then read it back after one minute. If its entry is missing,
     write again, at most 3 times per refresh.

Because the key is public, the index gives no guarantees:
- **Anyone can erase or fill a slot.**
- **Versions from different writers do not converge.** Different writers stamp
  from different batches, and a node keeps whichever version reached it last
  (`ChunkStore().Replace`, `reserve.go:267-273`).
- **So the index is only a list of candidates.** A candidate is used only after
  its own record passes the checks above.
- **Worst case:** an attacker who erases or floods the index costs requesters the
  preference, and nothing more. They retrieve normally.

**Lookup on the requester.** Discovery runs only when `providers-enable` is on, and
only for `/bzz/{ref}` and `/bytes/{ref}`:
1. Read the S slots in parallel, merge them and drop expired entries.
2. Fetch the records of at most 16 candidates in parallel, and verify them.
3. Connect to verified providers in the background.

The lookup runs alongside the download and never delays the first byte. The
download's context carries a small shared set, which the lookup fills while chunks
are being fetched. Later chunks use the providers as they arrive. Results are
cached per K for 10 minutes.

A lookup costs up to 8 + 16 chunk retrievals. It is the cost the measurement must
compare with the gain.

**Rejected: one shared feed under a key derived from K.** It is simpler to read,
but it breaks in three ways:
- anyone can overwrite any index;
- versions from different writers do not converge;
- an index written far ahead sends the sequence finder into its
  inconsistent-feed retry (`pkg/feeds/sequence/sequence.go:185-188`).

The pointer index has the same openness, but it keeps the trust in the provider's
own record.

**Rejected for now:**
- **A new libp2p protocol to ask connected wasp peers "do you have K".** It adds
  nothing the local-only header does not, and it cannot reach beyond connected
  peers.
- **Queries sent by PSS to the neighbourhood of K.** They would work only where
  wasp nodes happen to live.

### 2. Preferring providers, and falling back

This is in `RetrieveChunk` (`pkg/retrieval/retrieval.go:134`), for origin requests
only. Forwarded requests never carry a preferred set.

**Context.**
- `retrieval.WithPreferredPeers(ctx, *PreferredSet)` and
  `retrieval.PreferredPeers(ctx)` follow the pattern of the redundancy settings
  (`pkg/file/redundancy/getter/strategies.go`).
- The API sets the value next to `SetConfigInContext` (`pkg/api/bzz.go:542, 750`),
  and in `pkg/api/bytes.go`, `chunk.go` and `feed.go`.
- The download path keeps context values down to `RetrieveChunk`: the joiner
  stores the request context, and singleflight replaces only its cancellation.

**Order of attempts** for each chunk:
1. Pick the preferred peers that are connected and not skipped, ordered by XOR
   closeness to the chunk address. Every requester then spreads a file's chunks
   over several providers the same way.
2. Try at most 2 of them, one after the other, each with the stream header
   `wasp-local-only: 1`.
3. If an attempt misses (any error), or gives no answer within 500 ms, move on.
4. After that, normal peer selection continues unchanged.

The existing 1 s preemptive attempt still runs in parallel (`retrieval.go:172-176`).

**Rules for preferred attempts.**
- **A miss is not a peer error.** It does not go into the service-wide `errSkip`
  list (`retrieval.go:282`), which would ignore the provider for one minute across
  all chunks. It also does not use up the origin's 32 allowed errors.
- **Deduplication.** The singleflight key includes a fingerprint of the preferred
  set (`retrieval.go:145-148`). A hinted and an unhinted request for the same
  chunk then never share one flight.
- **Accounting is unchanged.** `prepareCredit` applies to the provider as to any
  peer. On an overdraft the provider is skipped for this attempt (600 ms,
  `retrieval.go:253`), and normal retrieval takes over.
- **Demotion.** After 16 misses in a row from one provider for one K, the
  requester drops that provider for that K for 10 minutes.

### 3. Answering as a provider

**Retrieval handler** (`pkg/retrieval/retrieval.go:421`), when `providers-enable`
is on:
- If the request carries `wasp-local-only` and `Lookup` returns not found, return
  the error `wasp: chunk not held locally`.
  - The handler's existing deferred write sends it as `Delivery{Err}`
    (`retrieval.go:428-434`).
  - This happens before any forwarding and before `PrepareDebit`, so nothing is
    charged or reserved.
- A hit follows the normal path and is charged at the node's own price.
- Without the header, or with the setting off, the handler is unchanged.

**Limit on free misses.** A miss costs the provider one store lookup and earns
nothing. Each peer gets at most 100 free local-only misses per second. Past that,
the header is ignored for that peer: the request is handled exactly as stock, so
it forwards and it is charged.

**Announcing** (new package `pkg/providers`, with the fork copyright header):
- `POST /wasp/providers/{reference}`, with `Swarm-Postage-Batch-Id`:
  - pins the reference if it is not already pinned, reusing the pin traversal in
    `pkg/api/pin.go`;
  - writes the provider record and the pointer-index entry;
  - stores K in the state store, so that it is refreshed. Returns 201.
- `DELETE /wasp/providers/{reference}`: writes the record once more with
  `capability: withdrawn` and stops refreshing. The pin stays; unpinning is
  separate.
- `GET /wasp/providers`: the announced keys, with record expiry and next refresh.
- `GET /wasp/providers/{reference}/lookup`: runs a lookup and returns the verified
  providers, for operators and for the measurement.

Other rules for announcing:
- The upstream `/pins` and `/stewardship` APIs are not changed. The new routes
  live under `/wasp/` so that they cannot clash with a future upstream route.
- **Refresh:** records live 24 hours and are refreshed every 12 hours. Both the
  record and the pointer entry are signed again and stamped from the stored batch,
  using the same stamper the API uses (`getStamper`, `pkg/api/api.go:818`).
- **Unusable batch:** the node logs a warning and keeps serving its pins; its
  records expire.
- **Signing:** records are signed with the node's signer, which `NewBee` already
  receives (`pkg/node/node.go:371`).
- **Light nodes cannot announce in phase 1** (400), because they refuse inbound
  retrieval (`pkg/node/node.go:1369-1371`). #282 covers that case.

### What a provider serves

Pinned content only. Serving the cache as "provided" would publish what the
operator has recently watched. It is left out, and needs its own decision.

### Phases

1. **Phase 1, this spec's implementation.**
   - explicit hint;
   - discovery (records and pointer index);
   - preference and fallback;
   - local-only answers;
   - the `/wasp/providers` API;
   - on-demand connection.
2. **Phase 2.**
   - **Keeping connections.** Keep a provider connection from being pruned while
     it is in use: `Kad.Protect(addr, ttl)`, filtered out of the prune candidates
     (`pkg/topology/kademlia/kademlia.go:787-858`). This builds on #291.
   - **Erasure-coded content.** Carry the preferred set into it, whose prefetch
     starts from `context.Background()`
     (`pkg/file/redundancy/getter/getter.go:242`).
   - **Publisher lists.** A manifest metadata key naming a providers list chosen
     by the publisher.
3. **Phase 3.** Live streams:
   - a provider mode that follows a feed and pins new segments ahead of viewers;
   - requesters track the feed index;
   - "authoritative miss" answers, accepted only from providers the publisher
     names.
4. **Phase 4, only if the measurements justify it.**
   - accounting terms advertised in the record;
   - light-node providers (#282).

## Security and privacy

- **Forged records:** not possible. The record is a SOC signed by the provider's
  key, and its address is signed by the same key.
- **Replayed records:** valid until their expiry, and never more than 7 days.
- **Wrong data:** a malicious provider cannot serve it. Every chunk is checked
  against its address (`retrieval.go:360-364`).
- **Withholding:** a provider that withholds costs a requester one short miss per
  chunk, until the demotion rule drops it.
- **Index flooding:** readers cap their work at 8 slot reads and 16 record
  fetches per K.
- **Provider privacy:** a record links the provider's key and network address to
  K. This is the provider's choice, made per reference.
- **Requester privacy:** the header tells the provider that the requester is the
  origin of the request. Normal forwarding hides that. A requester chooses this
  per request or with the setting.
- **Cost to other nodes:** each record and pointer slot is an ordinary stamped
  chunk, stored by the nodes of its neighbourhood and paid for by the provider's
  batch. Lookups are ordinary retrievals.

## Protocol impact

No change to the frozen surface:
- no new protocol ID;
- no `.proto` change;
- no change to the handshake, chunk format or chain configuration.

`.github/protocol-freeze.lock` does not change, and `make protocol-freeze` passes.

The one new item a peer can see is a key in the stream headers, which every stream
already exchanges (`pkg/p2p/libp2p/libp2p.go:1369-1378`):
- A receiving node copies every header key into a map
  (`pkg/p2p/libp2p/headers.go:34-63`).
- Stock code reads only the keys it knows. The tracing key was renamed in the past
  precisely so that mixed versions ignore each other's payloads
  (`pkg/p2p/p2p.go:219-224`).
- Retrieval registers no header handler, so the reply headers stay empty.

A stock node that receives `wasp-local-only` therefore handles the request as it
always has. It forwards on a miss and it charges. The requester still receives the
chunk, just without the fast miss.

| Requester | Provider | Result |
|---|---|---|
| stock | wasp | Nothing changes. A stock node never sends the header or reads records. |
| wasp | stock | The header is ignored; the stock node answers or forwards as usual. |
| wasp | wasp | Preference, a fast local-only miss, and fallback. |

Records and pointer slots are ordinary SOCs, valid to every stock node.

This is confirmed by a mixed-version test:
- a stock `v2.8.2` binary as the provider, and separately as the requester;
- the bytes delivered must be identical;
- the logs of either side must show no blocklisting and no "failed to meet
  expectation for allowance".

## Measurement

On `bench-1`, following `docs/agent-playbooks/test-bench.md`:
- at least three runs per condition, reported as median and range;
- node state matched across conditions;
- the method fixed in `measurement.md` before the first run.

**Nodes:** a wasp provider P (full node), a wasp requester Q (once as a full node
and once as a light node), and a stock `v2.8.2` node for the mixed cases.

**Content A, held by the network and pinned on P:**
- one fresh file per run, so that no run finds another run's chunks cached;
- downloaded by Q with `Swarm-Cache: false`.

**Content B, held only by P:**
- a file whose batch has expired, or which was never pushed;
- pinned on P;
- it tests hypothesis 2, availability.

**Conditions:**
1. stock Q, no provider;
2. wasp Q, no hint;
3. wasp Q, hint to P;
4. wasp Q, hint to the stock node;
5. wasp Q, P found by discovery;
6. wasp Q, hint to a P that holds half of the file.

Each condition runs with pseudosettle only and with SWAP.

**Metrics:**
- time to first byte and total time;
- the share of chunks served by P;
- overdraft spills to normal retrieval;
- lookup time and lookup retrievals;
- P's upload;
- for content B: success or failure.

**A negative result is any of:**
- no gain in time to first byte or throughput beyond the spread in conditions 3
  and 5;
- a lookup that costs more time than it saves on a file of typical size;
- content B still failing with a correct hint.

The first is enough to stop building phase 2 onwards. The mechanism would still be
kept for availability if content B works.

## Rollout and rollback

- **Default:** `providers-enable` is off. With it off, a node neither announces,
  nor looks up, nor honours `wasp-local-only`. Its retrieval behaviour is identical
  to stock.
- **Explicit hint:** the `Wasp-Providers` header works regardless. It affects only
  the request that carries it.
- **Enabling:** set `providers-enable: true` and restart. A provider then announces
  references through `POST /wasp/providers/{reference}` with a batch.
- **Rolling back:** set it to false and restart. Announced records expire within 24
  hours, and pointer entries with them. Pins stay until the operator removes them.

## Upstream portability

Nothing here needs a protocol version change, so it can be adopted piece by piece:

1. The handler's local-only answer: a few lines in the retrieval handler.
2. The preference in `RetrieveChunk`, and the context helpers.
3. The record and pointer formats. Tag strings, S, the entry limit and the
   checks are all fixed in this spec, so other clients can read and write them.
   bee-js can read them through `GET /soc`. A client that speaks the retrieval
   protocol itself, such as weeb-3, can also send the header.
4. The API, under its own path.

A client that adopts none of it keeps working with every wasp node.

## Configuration

**Setting**
- `providers-enable` (bool, default `false`). A feature switch, not a tuning
  value.
- Turning it on as a provider costs:
  - stamps for records;
  - upload bandwidth for serving;
  - publishing which content the node holds.
- Turning it on as a requester costs up to 24 extra chunk retrievals per new
  content key, and tells providers which requests originate at this node.
- Turning it off costs nothing beyond losing the feature.

**Format constants**, fixed and never settings, because other clients must agree on
them: the tag strings, S = 8, 32 entries per slot, record expiry at most 7 days, and
the JSON fields.

**Tuning constants**, compiled in. Rule 8: none becomes a setting until a
measurement shows it matters. The spec records what each would cost:

| Constant | Value | Raising it costs | Lowering it costs |
|---|---|---|---|
| Wait for a preferred answer | 500 ms | Slower fallback when a provider is slow | Fallbacks while a provider is still answering, so some chunks are fetched and paid for twice |
| Preferred attempts per chunk | 2 | More misses before fallback | Fewer providers tried per chunk |
| Candidates verified per lookup | 16 | More retrievals per lookup | Fewer providers found where many exist |
| Lookup cache | 10 min | Stale provider lists live longer | More lookups |
| Record lifetime / refresh | 24 h / 12 h | Withdrawn or moved providers stay listed longer | More stamped writes, which also cost the nodes storing them |
| Misses before demotion | 16 | A withholding provider costs requesters longer | A provider catching up (partial content) is dropped sooner |
| Free local-only misses per peer | 100/s | Lookups on the provider that nobody pays for | Honest requesters fall back to paid forwarding sooner |

## Test plan

Unit tests, as `package _test`:

**`pkg/retrieval`**
- a preferred hit;
- a local-only miss falls back and leaves no `errSkip` entry;
- the handler with the setting off, or without the header, forwards as before;
- the limit on free misses switches a peer to stock handling;
- singleflight keeps hinted and unhinted requests apart;
- demotion after repeated misses.

**`pkg/providers`**
- a record round trip;
- a record is rejected for a bad signature, expiry beyond 7 days, an owner
  mismatch with `bzz.Address`, a wrong key, or `withdrawn`;
- pointer-slot merge, and the entry limit;
- a lookup ignores junk entries, and survives erased or missing slots.

**`pkg/api`**
- `Wasp-Providers` parsing for overlay and multiaddr entries, and the 8-entry cap;
- `/wasp/providers` status codes, including a light node (400) and a missing
  batch.

Before every push: `make format && make build && make test && make lint &&
make protocol-freeze`.

Node-level:
- the mixed-version test from Protocol impact;
- the measurement above.

## Documentation

- `docs/DIFFERENCES.md`: the setting, the header and the API (rule 13).
- `openapi/Swarm.yaml`: the `/wasp/providers` routes and the `Wasp-Providers`
  header.
- An operator note on what announcing costs and reveals.

Generated with help of AI.
