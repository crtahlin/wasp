# Content providers: let a node that holds content be found and asked first

Issue: [#290](https://github.com/crtahlin/wasp/issues/290). Related:
[#282](https://github.com/crtahlin/wasp/issues/282) (light-node cache serving),
[#291](https://github.com/crtahlin/wasp/issues/291) (static peers and pruning).

Status: spec for review. No code lands until this is merged.

All code references are to wasp `main` at `8e53d493`, whose base is bee `v2.8.2`.

## Terms

- **Provider**: a node that holds some content and says so.
- **Requester**: a node downloading that content.
- **Content key K**: what a provider announces.
  - For `/bzz` and `/bytes` it is the reference, 32 bytes. An encrypted reference
    is 64 bytes and carries its decryption key, which a record would publish, so
    encrypted references are never announced or looked up.
  - For a feed it is `keccak256("wasp-feed-v1" || owner || topic)`. Feeds are
    handled in phase 3.
- **SOC**: a single owner chunk. Its address is derived from an owner key and an
  id, and it carries the owner's signature.
- **Overlay**: a node's Swarm address. **Underlay**: its network address, as a
  libp2p multiaddr.
- **Origin request**: a retrieval the node starts for itself, as opposed to one it
  forwards for a peer.
- **Preemptive attempt**: the extra parallel attempt an origin request starts every
  second while it waits (`pkg/retrieval/retrieval.go:176-181`).
- **Singleflight**: the mechanism that merges identical concurrent retrievals into
  one, keyed by a string (`retrieval.go:145-148`).
- **errSkip**: a list, shared across all requests, of peers that recently failed a
  chunk; each entry lasts one minute (`retrieval.go:282`).
- **Reserve**: the chunks a full node stores for its neighbourhood. **Pullsync**:
  how neighbours copy reserve chunks from each other.
- **Stamp index**: the slot in a postage batch that a chunk's stamp uses.
- **Pseudosettle**: the free, time-based allowance that clears a debt between two
  peers. **SWAP**: payment of that debt with cheques. **Overdraft**: when a
  requester owes a peer more than the peer allows, so it cannot ask that peer until
  the debt clears.
- **PSS**: Swarm's message delivery to an address or neighbourhood.
- **Local-only miss**: a provider's immediate "I do not have this chunk" answer,
  defined in section 3.
- **Window**: a 12-hour period, number `w = floor(unix seconds / 43200)`.
- **bee-js**: the JavaScript client for the Bee API. **weeb-3**: a community Swarm
  client that runs in the browser as a light node.

## Problem

A node that holds some content in full is asked for it only when it is on the route
that normal retrieval picks:

- A requester sends each chunk request to its connected peer closest to the chunk
  address (`closestPeer`, `pkg/retrieval/retrieval.go:388-419`).
- For an origin request, that may be any peer (line 406).

If peers' addresses were spread evenly, a node holding a whole file would answer
roughly one chunk in N for a requester with N peers. Kademlia bins are not even, and
this is not measured. Either way, no other node can know that it holds the content.

Serving is not the gap. The retrieval handler answers from `Lookup()`
(`retrieval.go:467`), which reads any chunk in the chunk store, whether reserve,
pinned or cache (`pkg/storer/internal/cache/cache.go:136`).

Pinning keeps content without a postage stamp:
- `POST /pins/{ref}` walks the reference and stores each chunk with no stamp
  (`pkg/api/pin.go:61-125`, `pkg/storer/internal/pinning/pinning.go:89-122`).
- Pinned chunks are served like any other.

What is missing is a way for a node to say it holds content, and a way for other
nodes to use that.

The idea is the one once called **global pinning**: content that a node pins is
available to, and discoverable by, the whole network, with no stamp paid for the
content. Only the idea is taken here, not any earlier implementation.

## Hypothesis

A provider announces that it holds content K. Requesters try it first for the chunks
of K, and keep normal retrieval as the fallback for every chunk it does not answer.
Then:

1. **Speed.** A download of content that the network also holds gets faster, or at
   least no slower, when a provider holds all of it and has a lower round-trip time
   than the peers normal retrieval uses.
2. **Availability.** Content that the network no longer holds, for example after its
   batch expired, stays retrievable from a provider that pinned it. No stamp is
   needed for the content itself.
3. **Live streams.** An event stream can be carried mostly by a few nodes set up for
   it. This is not tested in phase 1; see Phases.
4. **Stock nodes.** Stock Bee nodes see nothing new.
   - A wasp provider that lacks a chunk costs a requester up to 2 attempts, and at
     most about 1 s for that chunk, until the requester drops that provider.
   - A stock or disabled node gives no fast miss: it forwards the request, it is
     paid, and the requester may pay twice for that chunk.

Accounting limits hypothesis 1. Without SWAP, the sustained rate one provider can
give one requester is about its refresh rate divided by the chunk price. At the
highest price, a chunk at proximity 0 (`pkg/pricer/pricer.go:35`), that is:
- about 4,500,000 / 320,000 ≈ 14 chunks per second to a full node;
- about 1.4 to a light node (`pkg/node/node.go:230-232`).

Chunks closer to the provider cost less. Beyond this rate, `PrepareCredit` reports an
overdraft and the request moves on to normal retrieval, which is the intended
fallback. These figures are read from the code; the measurement will state real ones.

## Design

The design has three parts: how a requester learns about providers, how it prefers
them and falls back, and how a provider answers. Only wasp changes. **Everything is
off unless `providers-enable` is on**, including the request header.

### 1. Learning about providers

A requester builds a **preferred set** for each download.

#### 1a. Explicit hint

A request header on `GET /bzz/...`, `GET /bytes/{ref}`, `GET /chunks/{addr}` and
`GET /feeds/{owner}/{topic}`:

```
Wasp-Providers: <hex overlay>[,<hex overlay>...]
```

- **Honoured only when `providers-enable` is on.** Otherwise the header is ignored,
  as it is on a stock node.
- **At most 8 entries** are used.
- **Connecting:** an overlay that is not connected is dialled from the address book
  (`pkg/addressbook/addressbook.go:124-139`), with `forceConnection=false`. Kademlia
  may therefore refuse it when that bin is full, exactly as for any other peer.
- **No network addresses in the header.** Multiaddr entries are not accepted, so a
  public download request cannot make the node dial an arbitrary address. An
  operator who needs to reach a node the address book does not know can still use
  the existing `POST /connect/{multi-address}` debug route.
- **Browsers:** `Wasp-Providers` is added to the CORS allowed headers
  (`pkg/api/api.go:610-618`), so a player running in a browser can send it.

This is the source for the event case: the player knows which node carries the
stream.

#### 1b. Discovery by content key

Discovery has two layers:
- a record that only the provider can write, which says "this node holds K";
- an open index that lists candidates.

Both are written **once per window and never overwritten**. A fresh chunk address per
window avoids two problems:
- nodes that cached an older version of a chunk keep serving it (`cache.go:90-93`;
  `Download` reads the local store first, `pkg/storer/netstore.go:98`);
- different writers of the same address never converge on one version.

A provider always keeps two windows written: the current one, and, during the last
hour of each window, the next one. Readers then need to read only the current window.

**Provider record (authoritative).**
- A SOC owned by the provider's node key, with
  `id = keccak256("wasp-providers-v1" || K || uint64be(w))`.
- The wrapped chunk's payload is JSON:

  ```json
  {
    "v": 1,
    "key": "<hex K>",
    "window": 3291000,
    "address": { "overlay": "...", "underlays": ["..."], "signature": "...",
                 "nonce": "...", "timestamp": 0, "chequebook": "..." },
    "capability": "full"
  }
  ```

  - `capability` is `full` or `partial`.
  - `address` is a `bzz.Address` in its existing JSON form
    (`pkg/bzz/address.go:200`). The provider signs it fresh with `bzz.NewAddress`,
    with at most 4 underlays, public addresses first, so that the payload stays
    small. It carries no chequebook address, which a record does not need to
    reveal.
  - A provider refuses to write a payload larger than 4096 bytes.

A reader accepts a record only if all of these hold:
1. The SOC is valid. Retrieval already checks this (`retrieval.go:360-364`).
2. The address verifies. Parse it with `UnmarshalJSON`, which does not verify, then
   re-serialize the underlays with `SerializeUnderlays`, then pass everything to
   `bzz.ParseAddress` with this node's network ID (`pkg/bzz/address.go:84`).
   `ParseAddress` checks the overlay against the signing key and returns that key's
   Ethereum address (`address.go:122-133`).
3. That Ethereum address equals the SOC owner.
4. `v` is 1, `key` equals K, and `window` equals the current window.

Addresses taken from records are used only to dial. The address book is never
written from a record; a successful dial stores the address that the handshake
verified, as any connection does.

Signing records with the node's key cannot be confused with its other signatures:
- handshake data starts with `"bee-handshake-"` (`address.go:139`);
- SOC digests hash the id and the address;
- stamp digests hash the address, batch, index and timestamp.

**Pointer index (open, no guarantees).** A reader who knows only K cannot compute a
provider's record address, because the owner is part of it. The index supplies
owners:

- **Owner key:** the secp256k1 private key made from the 32 bytes
  `keccak256("wasp-provider-index-v1" || K)`, reduced modulo the curve order. This
  is what `crypto.DecodeSecp256k1PrivateKey` does (`pkg/crypto/crypto.go:84-90`),
  and other clients must do the same. Anyone can compute the key, so anyone can
  read and write the index.
- **Slots:** S = 8, where slot i in window w has
  `id = keccak256("wasp-provider-index-v1" || K || uint64be(w) || byte(i))`.
- **Slot payload** (JSON):

  ```json
  { "v": 1, "key": "<hex K>", "window": 3291000,
    "providers": ["<hex 20-byte owner address>"] }
  ```

  A slot holds at most 32 entries.
- **Writing:** a provider writes to slot `keccak256(owner)[0] mod S`, in three
  steps:
  1. read the slot;
  2. add itself, and if the slot is full, drop the first entry;
  3. write the slot.

  After one minute it reads the slot back with `RetrieveChunk` directly, which skips
  its own local copy. If its entry is missing, it writes again, at most 3 times per
  window.

Because the key is public, the index gives no guarantees. Anyone can erase or fill a
slot, and within one window different writers' versions do not converge. The index
is therefore only a list of candidates, and a candidate is used only after its own
record passes the checks above. An attacker who erases or floods the index costs
requesters the preference, and nothing more: they retrieve normally.

**Lookup on the requester.** Discovery runs only when `providers-enable` is on, only
for `/bzz/{ref}` and `/bytes/{ref}`, and only once a download has fetched 64 chunks
and is still running. Smaller downloads cannot gain enough to pay for a lookup.

1. Read the 8 slots of the current window in parallel. Each read has a 2 s deadline,
   so a slot that was never written does not hold the lookup up.
2. Fetch the records of at most 16 candidates in parallel, and verify them.
3. Connect to verified providers in the background, with `forceConnection=false`.

The download's context carries a small shared set, which the lookup fills while
chunks are being fetched; later chunks use the providers as they arrive. Results are
cached per K for 10 minutes, and so are empty results.

A lookup costs up to 8 + 16 chunk retrievals. When K has no providers, which is the
common case, all 8 slot reads are for chunks that do not exist.
- Each such read keeps looking until its 2 s deadline.
- The forwarding nodes on its path may keep working after that.
- A failed retrieval earns them nothing.

That unpaid work for other nodes is the reason for the 64-chunk threshold and the
cache of empty results.

**Rejected:**
- **One shared feed under a key derived from K.** Anyone can overwrite any index
  of it, versions from different writers do not converge, and an index written far
  ahead sends the sequence finder into its inconsistent-feed retry
  (`pkg/feeds/sequence/sequence.go:185-188`).
- **Records refreshed in place at a fixed address.** Nodes that cached an older
  version keep serving it (see above).
- **A new libp2p protocol to ask connected wasp peers "do you have K".** It adds
  nothing that the local-only header does not, and it cannot reach beyond connected
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
- The API sets them next to `SetConfigInContext` (`pkg/api/bzz.go:542, 750`), and in
  `pkg/api/bytes.go`, `chunk.go` and `feed.go`.
- The download path keeps context values down to `RetrieveChunk`: the joiner stores
  the request context, and singleflight replaces only its cancellation.

**Order of attempts** for each chunk:
1. Candidates are the preferred peers that are connected full nodes, taken from the
   topology's set of connected peers. A light peer never enters that set: the
   connection handler hands it to a separate light-node list, and only full nodes
   reach the topology (`pkg/p2p/libp2p/libp2p.go:704-726`).
   - This matters because a light node treats an incoming retrieval stream as
     misbehaviour and blocklists the peer that opened it
     (`pkg/p2p/specwrapper.go:25-29`).
   - Skipped peers are left out. The rest are ordered by XOR closeness to the chunk
     address, so every requester spreads a file's chunks over several providers the
     same way.
2. At most 2 of them are tried, one after the other, each with the stream header
   `wasp-local-only: 1`.
3. When an attempt misses, or has no answer after 500 ms, the next one starts.
   - A slow attempt is **not canceled**. It continues in the background and is paid
     if it succeeds, as preemptive attempts are today.
   - Canceling it would leave the provider holding a reservation it cannot apply.
4. After that, normal peer selection continues unchanged. The existing preemptive
   attempt still runs in parallel.

**Rules for preferred attempts.**
- **A miss is not a peer error.** It does not go into `errSkip`, which would ignore
  the provider for one minute across all chunks. It also does not use up the origin
  request's 32 allowed errors.
- **Deduplication.** The singleflight key includes a fingerprint of the preferred
  set. A hinted and an unhinted request for the same chunk then never share one
  retrieval.
- **Accounting is unchanged.** `prepareCredit` applies to a provider as to any peer.
  On an overdraft the provider is skipped for this attempt (600 ms,
  `retrieval.go:253`), and normal retrieval takes over.
- **Demotion.** After 16 misses in a row from one provider for one K, the requester
  drops that provider for that K for 10 minutes. An invalid chunk from a provider
  drops it at once.
- **Sharing.** Downloads of the same K share one preferred set, kept until 10 minutes after
  its last use, so
  discovered providers and dropped providers carry over from one request to the
  next. An explicit hint applies to its own request only, so one client's hint
  never steers another client's downloads.
- **Candidates.** A provider that failed this chunk in the last minute is left
  out.

### 3. Answering as a provider

**Retrieval handler** (`pkg/retrieval/retrieval.go:421`), when `providers-enable` is
on:
- **Local-only miss.** If the request carries `wasp-local-only` and `Lookup` returns
  not found, the handler returns the error `wasp: chunk not held locally`.
  - The existing deferred write sends it as `Delivery{Err}`
    (`retrieval.go:428-434`).
  - This happens before any forwarding and before `PrepareDebit`, so nothing is
    charged or reserved on either side.
- **Hit.** A chunk found locally follows the normal path and is charged at the
  node's own price.
- **Limits on free misses.** A miss costs the provider a store lookup and earns
  nothing, so misses are capped at 100 per second per peer and 1000 per second for
  the whole node. Past a limit, the handler answers `wasp: local-only limit`, also
  before `PrepareDebit`, and the requester treats that as a miss. Falling back to
  forwarding instead would cost the provider more.
- **Unchanged otherwise.** Without the header, or with the setting off, the handler
  is unchanged.

**What a local-only request can see.** `Lookup` covers everything the node holds:
reserve, pins and cache. A local-only request is answered from all of it.
- It therefore lets any connected peer test, cheaply and precisely, whether this
  node is missing a given chunk.
- Stock Bee reveals the same only through timing.
- An operator who turns the setting on accepts this. The spec states it; it does not
  hide it.

**Announcing** (new package `pkg/providers`, with the fork copyright header).
- **`POST /wasp/providers/{reference}`**, with `Swarm-Postage-Batch-Id`:
  - requires the reference to be pinned already, with `POST /pins/{reference}`,
    and returns 400 otherwise. Pinning inside this call would mean refactoring
    upstream's `pkg/api/pin.go`, which makes every upstream sync more expensive;
  - stores K and the batch in the state store;
  - writes the record and the pointer entry for the current window. Returns 201.
- **`DELETE /wasp/providers/{reference}`:** stops announcing. The node stays listed
  until the last window it wrote ends, at most 24 hours. The pin stays; unpinning is
  separate.
- **`GET /wasp/providers`:** the announced keys, the batch, and the windows written.
- **`GET /wasp/providers/{reference}/lookup`:** runs a lookup and returns the
  verified providers, for operators and for the measurement.

Other rules for announcing:
- **Paths.** The upstream `/pins` and `/stewardship` APIs are not changed. The new
  routes live under `/wasp/` so that they cannot clash with a future upstream route.
- **Upload.** Each record and pointer write is an ordinary SOC upload, stamped from
  the stored batch with the stamper the API uses (`getStamper`,
  `pkg/api/api.go:818`). It costs one stamp slot. Per K, that is about 2 records and
  2 pointer writes a day, plus rewrites.
- **Unusable batch.** The node logs a warning and keeps serving its pins; it simply
  stops being listed.
- **Signing.** Records are signed with the node's signer, which `NewBee` already
  receives (`pkg/node/node.go:371`).
- **Light nodes cannot announce in phase 1** (400). A light node blocklists any peer
  that opens a retrieval stream to it (`pkg/node/node.go:1370-1372`). #282 covers
  that case.

A provider **announces** pinned content only. Serving the cache as "provided" would
publish what the operator has recently watched.

### Phases

1. **Phase 1, this spec's implementation.**
   - the explicit hint;
   - discovery (records and pointer index);
   - preference and fallback;
   - local-only answers;
   - the `/wasp/providers` API;
   - connecting to a provider when a request first needs it.
2. **Phase 2.**
   - **Keeping connections.** Keep a provider connection from being pruned while it
     is in use: `Kad.Protect(addr, ttl)`, filtered out of the prune candidates
     (`pkg/topology/kademlia/kademlia.go:787-858`). This builds on #291.
   - **Erasure-coded content.** Carry the preferred set into it, whose prefetch
     starts from `context.Background()`
     (`pkg/file/redundancy/getter/getter.go:242`).
   - **Publisher lists.** A manifest metadata key naming a providers list chosen by
     the publisher.
3. **Phase 3, live streams.**
   - a provider mode that follows a feed and pins new segments ahead of viewers;
   - requesters track the feed index;
   - "authoritative miss" answers, accepted only from providers the publisher names.
4. **Phase 4, only if the measurements justify it.**
   - accounting terms advertised in the record;
   - light-node providers (#282).

## Security and privacy

- **Forged records:** not possible. The record is a SOC signed by the provider's key,
  and the address inside it is signed by the same key.
- **Replayed records:** they cannot outlive their window, because a reader accepts
  only the current one.
- **Dialling someone else's address:** a provider can list any IP as its underlay,
  as it can today in hive gossip. A requester dials at most 16 candidates per
  lookup, with `forceConnection=false`. A wrong address fails the libp2p security
  handshake.
- **Wrong data:** not possible. Every chunk is checked against its address
  (`retrieval.go:360-364`), and an invalid chunk demotes the provider at once.
- **Withholding:** a provider that withholds costs a requester up to 2 attempts and
  about 1 s per chunk, until demotion.
- **Index flooding:** readers cap their work at 8 slot reads and 16 record fetches
  per K.
- **Provider privacy:** a record links the provider's key and network address to K.
  That is the provider's choice, made per reference. Local-only answers also reveal
  which chunks the provider lacks (section 3).
- **Requester privacy:** the header tells a provider that the requester is the
  origin of the request, which normal forwarding hides. The requester's operator
  chooses this by turning the setting on.
- **Cost to other nodes:**
  - records and pointer slots are ordinary stamped chunks, stored by the nodes of
    their neighbourhood and paid for by the provider's batch;
  - lookups for content with no providers cause unpaid failed retrievals (section
    1b).

## Protocol impact

No change to the frozen surface:
- no new protocol ID;
- no `.proto` change;
- no change to the handshake, chunk format or chain configuration.

`.github/protocol-freeze.lock` does not change, and `make protocol-freeze` passes.

The one new item a peer can see is a key in the stream headers, which every stream
already exchanges before its first message (`pkg/p2p/libp2p/libp2p.go:1369-1378`):
- a receiving node copies every header key into a map
  (`pkg/p2p/libp2p/headers.go:34-63`);
- stock code reads only the keys it knows. The tracing key was renamed in the past
  precisely so that mixed versions ignore each other's payloads
  (`pkg/p2p/p2p.go:219-224`);
- retrieval registers no header handler, so the reply headers stay empty.

A stock node that receives `wasp-local-only` therefore handles the request as it
always has: it forwards on a miss and it charges. The requester still receives the
chunk, just without the fast miss.

| Requester | Provider | Result |
|---|---|---|
| stock | wasp | Nothing changes. A stock node never sends the header or reads records. |
| wasp | stock | The header is ignored; the stock node answers or forwards as usual. |
| wasp | wasp | Preference, a local-only miss when the provider lacks a chunk, and fallback. |

Records and pointer slots are ordinary SOCs, valid to every stock node.

A mixed-version test confirms this before the implementation merges:
- a stock `v2.8.2` binary as the provider, and separately as the requester;
- the bytes delivered must be identical;
- the logs of either side must show no blocklisting and no "failed to meet
  expectation for allowance".

## Measurement

On `bench-1`, following `docs/agent-playbooks/test-bench.md`:
- at least three runs per condition, reported as median and range;
- node state matched across conditions;
- the method fixed in `measurement.md` before the first run.

**Nodes:** a wasp provider P (full node), a wasp requester Q (once as a full node and
once as a light node), and a stock `v2.8.2` node for the mixed cases.

**Content A, held by the network and pinned on P:**
- one fresh file per run, larger than the 64-chunk lookup threshold, so that no run
  finds another run's chunks cached;
- downloaded by Q with `Swarm-Cache: false`.

**Content B, held only by P:**
- a file uploaded with a batch that is left to expire, then pinned on P.
- This needs at least the contract's minimum batch validity, about 24 hours.
- Forwarders may still cache some chunks after that, so condition 1 runs first to
  confirm that the network really fails to deliver it.
- It tests hypothesis 2, availability.

**Conditions:**
1. stock Q, no provider;
2. wasp Q with `providers-enable` on, no provider known;
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
- no gain in time to first byte or throughput in conditions 3 and 5 compared with
  condition 2, beyond the spread;
- a lookup that costs more time than it saves on a file of typical size;
- content B still failing with a correct hint.

The first is enough to stop building phase 2 onwards. The mechanism would still be
kept for availability if content B works. Hypothesis 3, live streams, is not tested
in phase 1.

## Rollout and rollback

- **Default:** `providers-enable` is off. With it off, a node neither announces, nor
  looks up, nor honours `Wasp-Providers` or `wasp-local-only`. Its retrieval
  behaviour is identical to stock.
- **Enabling:** set `providers-enable: true` and restart. A provider then announces
  references through `POST /wasp/providers/{reference}` with a batch.
- **Rolling back:** set it to false and restart.
  - The node's records and pointer entries stay valid until the last window it
    wrote ends, at most 24 hours.
  - Until then, requesters that find it send local-only requests. It handles them
    as stock: it forwards on a miss and it charges. That is correct, just without
    the fast miss.
  - Pins stay until the operator removes them.
- **Enabling again:** the stored keys resume announcing with their stored batch, if
  the batch is still usable.

## Upstream portability

Nothing here needs a protocol version change, so it can be adopted piece by piece:

1. The handler's local-only answer: a few lines in the retrieval handler.
2. The preference in `RetrieveChunk`, and the context helpers.
3. The record and pointer formats.
   - The tag strings, the window length, S, the entry limit, the key derivation and
     the checks are all fixed in this spec, so other clients can read and write
     them.
   - bee-js can read them through `GET /soc`.
   - A client that speaks the retrieval protocol itself, such as weeb-3, can also
     send the header.
4. The API, under its own path.

A client that adopts none of it keeps working with every wasp node.

## Configuration

**Setting**
- `providers-enable` (bool, default `false`). A feature switch, not a tuning value.
- Turning it on as a provider costs:
  - stamp slots for records;
  - upload bandwidth for serving;
  - publishing which content the node holds, and answering local-only requests.
- Turning it on as a requester costs up to 24 extra chunk retrievals per new content
  key on larger downloads, and tells providers which requests originate at this
  node.
- Turning it off costs nothing beyond losing the feature.

**Format constants**, fixed and never settings, because other clients must agree on
them: the tag strings, the 12-hour window, S = 8, 32 entries per slot, at most 4
underlays, the 4096-byte limit, the key derivation, and the JSON fields.

**Tuning constants**, compiled in. Rule 8: none becomes a setting until a measurement
shows it matters. The spec records what each would cost:

| Constant | Value | Raising it costs | Lowering it costs |
|---|---|---|---|
| Wait before the next preferred attempt | 500 ms | Slower fallback when a provider is slow | More parallel attempts, so some chunks are paid for twice |
| Preferred attempts per chunk | 2 | More misses before fallback, and unpaid lookups on providers | Fewer providers tried per chunk |
| Hint entries used | 8 | More connections per request | Fewer providers usable from one hint |
| Download size before a lookup | 64 chunks | Fewer downloads can find providers | More lookups, and more unpaid failed retrievals on other nodes |
| Slot read deadline | 2 s | Slower lookups on content with no providers, and longer unpaid work on forwarders | Slots on slow paths are missed |
| Candidates verified per lookup | 16 | More retrievals per lookup, paid or failed, on other nodes | Fewer providers found where many exist |
| Lookup cache, including empty results | 10 min | New providers are noticed later | More lookups, and more load on other nodes |
| Pointer read-back delay / rewrites | 1 min / 3 | Lost entries are repaired later / more stamped writes | Reads that race the write / entries stay lost |
| Misses before demotion | 16 | A withholding provider costs requesters longer | A provider holding part of the content is dropped sooner |
| Free local-only misses | 100/s per peer, 1000/s per node | Unpaid lookups on the provider | Honest requesters get a limit answer, and fall back, sooner |

## Test plan

The in-memory stream helper used by the retrieval tests does not yet pass request
headers to the handler's side of a stream. `Recorder.NewStream` hands them only to a
registered header handler, never to the handler's stream `streamIn`
(`pkg/p2p/streamtest/streamtest.go:130-165`). Phase 1 therefore adds one test-only
line, `streamIn.headers = h`, so that the handler tests see `wasp-local-only` the way
a real libp2p stream delivers it (`pkg/p2p/libp2p/headers.go:42`).

Unit tests, as `package _test`:

**`pkg/retrieval`**
- a preferred hit;
- a local-only miss falls back and leaves no `errSkip` entry;
- a slow preferred attempt is not canceled, and is paid when it answers;
- only connected full nodes are chosen;
- the handler with the setting off, or without the header, forwards as before;
- past the miss limits, the limit answer is sent before any debit;
- singleflight keeps hinted and unhinted requests apart;
- demotion after repeated misses, and at once after an invalid chunk.

**`pkg/providers`**
- a record round trip through `UnmarshalJSON`, `SerializeUnderlays` and
  `ParseAddress`;
- a record is rejected for a bad signature, an owner that differs from the address
  key, a wrong key, or a window other than the current one;
- a payload over 4096 bytes is refused;
- the pointer key derivation matches a fixed test vector;
- slot merge and the entry limit;
- a lookup ignores junk entries, survives erased or missing slots, and caches empty
  results;
- the read-back does not read the local copy.

**`pkg/api`**
- `Wasp-Providers` parsing, the 8-entry cap, rejection of non-overlay entries, and
  the header being ignored when the setting is off;
- the CORS allowed headers include it;
- `/wasp/providers` status codes, including a light node (400), an unpinned
  reference (400) and a missing batch.

Before every push: `make build && make test && make lint && make protocol-freeze`.
Do not run `make format` across the repository. Under make, its `gci` call gets
an empty local prefix and regroups the imports of unrelated files; format only
the files that changed.

Node-level:
- the mixed-version test from Protocol impact;
- the measurement above.

## Documentation

- `docs/DIFFERENCES.md`: the setting, the header and the API (rule 13).
- `openapi/Swarm.yaml`: the `/wasp/providers` routes and the `Wasp-Providers` header.
- An operator note on what announcing costs and reveals.

Generated with help of AI.
