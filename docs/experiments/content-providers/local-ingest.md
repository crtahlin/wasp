# Hosting content without a stamp: local ingest

Issue: [#326](https://github.com/crtahlin/wasp/issues/326). A node puts content
into its own store, pays no postage, and serves it to anyone who asks for it.

Code references are to `main` at `7bd5da14`, base `upstream/v2.8.2`.

**This is revision 2.** An adversarial review found four load-bearing sections
wrong and eleven things missing. Where a claim has changed, the earlier one is
marked rather than removed.

## Terms

- **Sole-source content**: content that no other node holds, so a requester can
  get it only from this one.
- **Collection**: a set of chunks committed together under one root reference,
  which is how pinning stores things (`pkg/storer/pinstore.go:36-61`).
- **Payload bytes** against **stored bytes**: a 4,194,304-byte file is 1,033
  chunks, and a full chunk costs 4,096 data bytes plus an 8-byte span, so the
  store holds at most about 4,239,432 bytes for it, less because the
  intermediate chunks are not full. The two are not interchangeable and
  this document says which it means every time.
- **Refcount**: `chunkstore` keeps one copy of a chunk with a reference count
  (`pkg/storer/internal/chunkstore/chunkstore.go:92`), so a chunk this node
  already holds costs no new disk when ingested again.

## Problem

**A node cannot host content unless somebody is paying network rent for it.**

- **Pinning needs no stamp.** `pinRootHash` reads only
  `Swarm-Redundancy-Level` (`pkg/api/pin.go:25-59`); there is no postage header.
  It stores through `s.storer.NewCollection(r.Context())` (`:61`), an unstamped
  putter, and those chunks are served like any others because the retrieval
  handler answers from `Lookup()` (`pkg/retrieval/retrieval.go:554`), which
  reads the whole chunk store with no proximity guard.
- **But pinning can only adopt, not create.** It traverses the reference and
  downloads each chunk (`pin.go:68-115`), committing with `putter.Done`
  (`:125`). It works only while the network still holds the content.

**Every path that creates content requires postage, though not all in the same
way**, and an earlier draft of this document got that wrong. `POST /bytes` and
`POST /bzz` carry `Swarm-Postage-Batch-Id` with `validate:"required"`
(`pkg/api/bytes.go:34`, `pkg/api/bzz.go:72`). `POST /chunks` and `POST /soc`
carry it with **no validate tag** (`chunk.go:36`, `soc.go:53`) and accept a
presigned stamp in `Swarm-Postage-Stamp` instead, enforced by hand:

```go
if len(headers.BatchID) == 0 && len(headers.StampSig) == 0 {   // chunk.go:66, soc.go:66
```

A presigned stamp is still postage, so the conclusion holds: an operator who
wants to host a file must pay to put it in the network and keep paying, or pin
it before the rent lapses and hope they were in time.

**The end state is demonstrated to work, with one caveat that matters.** Content
B's postage batch expired days ago. The network answers 404 for it in **2.32 to
3.66 s over six runs** ([results.md](results.md), the raw rows). An earlier
draft of this document quoted "about 2.3 s", which is the single fastest run
reported as though it were typical, in a document that cites rule 7. The
provider still holds every chunk, unstamped, because they are pinned, and a
requester that names the provider retrieves the complete 4,194,304 bytes,
SHA-256 verified, at about 264,000 B/s.

**That retrieval was measured with the lookahead prefetch off**, with
`Swarm-Lookahead-Buffer-Size: 0`. At the shipped buffer the same download
truncates at 31.2% of the file, because the prefetch puts many chunks in flight
and many are refused credit at once. That is
[#327](https://github.com/crtahlin/wasp/issues/327), not this issue, and the
figures are in [overdraft-retry-results.md](overdraft-retry-results.md).

So serving unstamped pinned content works; serving it at full speed to an
ordinary client does not yet. This spec is about how the content gets there,
which is a separate and unblocked question.

## Hypothesis

A local ingest that splits content into chunks and stores them unstamped lets a
node host indefinitely at only its own disk cost.

**Predicted:** unencrypted content ingested this way is byte-identical in address
to the same content uploaded with a stamp through `POST /bytes` at the same
redundancy level, is retrievable from the ingesting node by a requester that
names it, and is retrievable from nowhere else before its first retrieval.

## Design

### 1. The route, named

```
POST /wasp/local
```

- Body: the content, as `POST /bytes` takes it.
- Headers honoured: `Swarm-Redundancy-Level`, `Swarm-Encrypt`.
- Response 201 with the reference, plus a field saying the content is held by
  this node only. **It is not an upload and the response must not read like
  one**: the reference resolves for nobody else unless they ask this node, and
  losing this node loses the content.
- 403 when the feature is off, following `providersEnabled`
  (`pkg/api/providers.go:210-216`), which is the existing precedent for a
  fork route gated by a setting.
- 507 when the limit is reached, naming the limit and current usage.

**Directories are out of scope for this issue.** The sketch below reproduces the
`POST /bytes` path only. A website or a directory needs the manifest path that
`dirUploadHandler` builds (`pkg/api/bzz.go:213-306`), and which
`GET /bzz/{ref}/{path}` needs in order to serve anything. The motivation names
gateways and publishers, which implies directories, so the gap is stated rather
than left for a reader to discover. It is a follow-up issue, not a silent
omission.

### 2. The handler

The splitter does not know about stamps: `requestPipelineFn(s storage.Putter,
encrypt bool, rLevel redundancy.Level)` takes a bare putter
(`pkg/api/api.go:900-905`). Stamping is a property of the putter, and pinning
already supplies an unstamped one. So the handler is the upload handler with the
postage machinery removed. **The sketch an earlier draft gave was missing the
two paths every real caller has**, and both matter here more than elsewhere:

```go
rLevel := redundancy.DefaultUploadLevel          // NOT DefaultDownloadLevel
if has, _ := s.storer.HasPin(reference); has { ... }   // see (a)
putter, err := s.storer.NewCollection(ctx)
defer func() { if err != nil { _ = putter.Cleanup() } }()   // see (b)
p := requestPipelineFn(putter, encrypt, rLevel)
reference, err := p(ctx, cleanupOnErrWriter-wrapped body)
err = putter.Done(reference)
```

**(a) The duplicate path.** `putter.Done` on an already-pinned root returns
`ErrDuplicatePinCollection` (`pkg/storer/internal/pinning/pinning.go:136-138`).
`pinRootHash` avoids it by checking `HasPin` first and returning 200
(`pin.go:49-59`); `DB.Upload` handles it by calling `Cleanup`
(`pkg/storer/uploadstore.go:104-106`). Without either, re-ingesting the same
bytes returns 500 **and** leaves the dirty-collection record, the per-chunk
index entries and the chunkstore refcounts behind. The acceptance plan below
ingests the same content repeatedly, so this fires immediately.

**(b) The cleanup path.** Both upload handlers wrap the body in
`cleanupOnErrWriter{onErr: putter.Cleanup}` (`bytes.go:101-105`,
`bzz.go:135-139`), and `pinRootHash` calls `putter.Cleanup()` on a traverse
error (`pin.go:116`). Without it, a client that disconnects mid-body leaves a
dirty collection whose chunks are removed only by `pinstore.CleanupDirty`,
called once at startup (`pkg/storer/storer.go:927`). **On a node that is not
restarted, an abandoned ingest is permanently leaked disk that no limit counts
and no operator action removes**, which is the exact failure the limit exists
to prevent, on the one endpoint whose whole risk is unbounded disk. The mechanism
to name is `collectionPutter.Cleanup` (`pinning.go:156-174`), which deletes the
collection chunks and the dirty record.

**(c) The redundancy default is the upload one.** `pkg/api/bytes.go:48` uses
`redundancy.DefaultUploadLevel`, which is `MEDIUM`; `pkg/api/pin.go:44` uses
`DefaultDownloadLevel`, which is `PARANOID`
(`pkg/file/redundancy/level.go:178`, `:181`). Implemented by copying `pin.go`,
which is this document's own worked example of an unstamped putter, the
address-equivalence test below fails for a reason that has nothing to do with
the feature, and a reviewer would read that as the Reject condition.

**(d) Nothing reaches the pusher, and nothing else observes these chunks.**
Verified rather than asserted. `SubscribePush` iterates `upload.IteratePending`
only (`pkg/storer/subscribe_push.go:35`) and `pkg/pusher/pusher.go:120` is its
only consumer, while a collection writes `pinChunkItem` plus the chunkstore. The
reserve is fed only by the push-sync and pull-sync putters. The cache getter
returns a chunkstore hit without creating a cache entry
(`pkg/storer/internal/cache/cache.go:143-148`). No stamp index entry is written.
`/tags` never sees an ingest, so there is no progress reporting, which is worth
saying rather than leaving a caller to find out.

**(e) Locking is already handled.** `pinstore`'s doc comments demand caller
serialisation (`pinning.go:70`, `:88`), and `DB.NewCollection` takes
`db.Lock(uploadsLock)` inside its put, done and cleanup closures
(`pkg/storer/pinstore.go:36-61`), so a new route inherits the same serialisation
the pin route gets. The operator-visible consequence: `uploadsLock` is node-wide
and shared with `DB.Upload` and `DeletePin`, so a long ingest serialises against
every stamped upload on the node.

**(f) Node mode.** `pinRootHash` has no mode check, so a light node can pin;
`providersAnnounceHandler` requires `FullMode` (`providers.go:224`). Local
ingest follows pinning and is allowed on a light node, since it costs the
network nothing.

### 3. Owner-only, and there is no authentication layer

An endpoint that stores unlimited data for free is a way to fill a stranger's
disk. **But there is nothing to put it behind, and this spec does not pretend
otherwise.** `Mount()` builds one router (`pkg/api/router.go:30-52`), served on
one listener (`pkg/node/node.go:668`), with `api-addr` defaulting to
`127.0.0.1:1633` (`cmd/bee/cmd/cmd.go:352`). There is no token, no restricted
mode, no `pkg/auth` and no second address.

So the **feature flag is the real control**, not a convenience: the route must
not exist unless the operator enabled it, so a node whose API is exposed for
another reason does not silently gain a way to be filled. There is existing
route-gating machinery to follow rather than invent: `checkRouteAvailability`
(`router.go:182`), the chain-availability siblings (`:192-232`), and
`providersEnabled` returning 403 (`providers.go:210-216`).

**And the loopback binding is not the defence it looks like.** `corsHandler`
(`pkg/api/api.go:627-636`) only sets response headers; it calls
`h.ServeHTTP(w, r)` unconditionally and `checkOrigin` gates nothing. A
cross-origin simple POST from any web page the operator visits reaches a handler
on `127.0.0.1:1633` and executes. Only the *response* is withheld from the page,
and for a write endpoint that is enough to fill the disk. **The documented cost
of enabling the flag must say this**, because an operator who reads "loopback"
as "safe" will be wrong in exactly the way this endpoint is dangerous.

If a real authorisation mechanism is wanted, it is its own piece of work and is
not invented here.

### 4. The limit, and what can actually be counted

**A new index item, because nothing existing can answer the question.**
`pinCollectionItem` persists `Addr`, `UUID` and `CollectionStat{Total,
DupInCollection}` (`pinning.go:358-362`) and nothing else. There is no origin
marker and no size, so a collection made by this route is indistinguishable from
one made by `POST /pins/{reference}`. A limit scoped to locally ingested content
therefore needs state that does not exist.

So: **a `localIngestItem`, keyed by the root reference, written once when the
ingest commits, holding the chunk count.** That gives the scoped limit and the
listing together.

**An earlier draft of this document said "no on-disk format change". That was
wrong** and it made the work look smaller than it is. There is still **no
migration**, because absence of the item means "not locally ingested", which is
true of every existing collection.

**The unit is chunks**, because that is what the index counts. A chunk is 4,096
payload bytes plus an 8-byte span, so a limit of N chunks is about `N * 4,104`
bytes of store. Payload bytes would systematically understate disk, and more so
with redundancy and encryption.

**Usage is an upper bound on disk, not a measurement of it.** `chunkstore.Put`
increments a refcount (`chunkstore.go:92`), so a chunk this node already holds
in its reserve or cache costs no additional disk and is still counted. Say so in
the metric's documentation rather than letting an operator read it as bytes
consumed.

**The existing count cannot be reused.** `pkg/storer/debug.go:123-140` sums
`stat.Total - stat.DupInCollection` across all collections through
`pinstore.IterateCollectionStats`, exposed at `/debugstore`. It is a full
iteration rather than O(1), it covers every pin including those adopted from the
network, and `CollectionStat.Total` is incremented before the duplicate check
(`pinning.go:95`), so it overcounts.

**Enforcement is mid-stream, not pre-flight.** `Content-Length` is absent under
chunked transfer encoding and is client-supplied in any case, so a pre-flight
refusal cannot be the only gate. The handler counts chunks as the pipeline
produces them, and on crossing the limit it stops, calls `putter.Cleanup()` and
returns 507 naming the limit and the usage. A pre-flight check on
`Content-Length` when it is present is a convenience that fails fast, not the
enforcement.

**A warning below the limit, not only at it.** A compiled-in threshold to start,
at 90% of the limit, logged at a level an operator sees, with a usage metric
beside it. Rule 8 applies to the threshold: it becomes a setting only if the
measurement shows the value matters.

**The limit itself ships as a setting from the start**, which is the rule 8
exception this document claims explicitly: it is a property of the operator's
disk rather than of the software, it cannot be measured centrally, and its
absence is itself the failure mode.

### 5. Removal, and what "reclaimed" can honestly mean

Nothing evicts this content: pinned data is never garbage collected and there is
no expiry. So removal is a first-class operation, and the existing unpin route
already does it.

**But "confirm the space is reclaimed" is not achievable as an earlier draft
phrased it.** `DeletePin` calls `ChunkStore().Delete` per chunk
(`pinning.go:281`), and `chunkstore.Delete` decrements the refcount and releases
the sharky slot **only at zero** (`chunkstore.go:130-133`). A chunk also held by
the reserve, the cache or another collection is not freed. And a released sharky
slot is reusable rather than returned: shard files are never truncated
(`pkg/sharky/shard.go:197-198`), so free disk does not increase and an operator
running `du` sees no change.

What can be promised, and what the documentation must say: **the chunk count
attributable to the reference drops to zero, the freed slots become reusable by
this node, and on-disk shard files do not shrink.**

**"Ingested and also pinned for another reason" cannot exist**, so the earlier
requirement to keep them from dropping each other has no referent. Collections
are keyed by root address (`pinning.go:364`), `pinRootHash` short-circuits on
`HasPin` and returns 200 without creating anything (`pin.go:49-59`), and a
second `Close` on the same root returns `ErrDuplicatePinCollection`. There is at
most one collection per reference. What the spec must instead define is the
reverse: **unpinning a locally ingested reference through the existing route
must also remove its `localIngestItem`**, or the usage figure drifts upward for
good.

### 6. Holding and advertising are two axes, not one

The code already enforces the split. Announcing is its own call,
`POST /wasp/providers/{reference}`, and it refuses a reference that is not
pinned: `"reference is not pinned; pin it first with POST /pins/{reference}"`
(`pkg/api/providers.go:249-259`). It takes the stamp (`:241-247`) and refuses
encrypted references outright, because the record would publish their decryption
key (`:236-239`).

| How it arrived | Cost | Advertised? |
|---|---|---|
| Uploaded with a stamp | postage, ongoing | operator's choice |
| Pinned from the network | free | operator's choice |
| Ingested locally, this spec | free | operator's choice |

**An encrypted ingest cannot be announced**, by that same check. If the handler
honours `Swarm-Encrypt`, the result is reachable only by explicit hint. One
sentence in the route's documentation.

**The advertisement mechanism must be a choice, not a constant.** Today there is
one, stamped records. Others are foreseen and must not be designed out: the peer
summaries in [passive-sharing.md](passive-sharing.md), which cost nothing
because nothing is written to the network, and the explicit hint, which needs no
advertisement at all. So the setting is "which mechanisms, if any", not a
boolean, and it should read that way from the start even while only one exists.

**Advertising does not default to on, and the reason is cost rather than taste.**
It requires a stamp today, so defaulting a pin to advertise would make
`POST /pins/{reference}`, which is free and takes no postage header, start
demanding a batch and failing without one. That is a silent change to the cost
of an existing free operation. There is a second reason: advertising tells the
network what this node holds, which some operators will not want.

**The default is off for a reason that may expire.** When a mechanism exists
that costs nothing, this should be revisited rather than inherited. Only the
stamped mechanism is wired up now.

## What this risks

- **Disk, with no rent to bound it.** The cost the operator accepts, and the
  reason for sections 3 and 4.
- **A leaked abandoned ingest** if the cleanup path in 2(b) is not built, which
  no limit would catch.
- **No redundancy.** Content that exists only here is gone if this node is. A
  stamped upload is replicated by the neighbourhood; this is not. The
  documentation must say so, because the endpoint will feel like an upload.
- **A false sense of publication.** The content is addressable but not findable.
  Without a hint or an announcement nobody can reach it.
- **A write endpoint reachable from the operator's own browser**, section 3.

## Protocol impact

**None.** No wire format, protocol identifier, handshake or message change. The
chunks are ordinary content-addressed chunks with ordinary addresses, served by
the existing retrieval protocol. A stock peer asked for one answers exactly as
it would for any chunk it does not hold. `make protocol-freeze` must pass with
the fingerprint unchanged.

## Measurement

- **Address equivalence, unencrypted only.** Ingest a file locally and upload the
  same bytes with a stamp through `POST /bytes` on another node, at the same
  redundancy level. The references must be identical.

  **Encrypted content cannot be tested this way**, and an earlier draft's
  "at the same encryption setting" did not save it: `chunkEncrypter.EncryptChunk`
  generates a fresh random key per chunk, so two encryptions of identical bytes
  give different references by construction. Encryption is therefore tested for
  round-trip retrieval, not for address equality.

  **The comparison is against `POST /bytes` and not `POST /bzz`**, because
  `fileUploadHandler` returns a manifest root built from the file reference plus
  the filename and sniffed content type (`bzz.go:213-306`), with the filename
  defaulting to the file hash (`:228-229`), so it never matches a raw split.

  **One run, not three.** This is a deterministic hash comparison; rule 7 is
  about quantities with variance, and three runs of a hash equality buys nothing.

- **Absence from the network, and its expiry.** A second node with no hint must
  fail to retrieve it. Expect 404.

  **The sole-source property lasts only until the first retrieval**, and an
  earlier draft's claim that "no peer can ever have cached it" was wrong. Every
  forwarding peer caches what it relayed (`pkg/retrieval/retrieval.go:627-632`,
  `if s.caching && forwarded`), and the requesting node caches through
  `Download(true)`. So the no-hint control runs **before** any hinted download,
  or each run ingests fresh content. It is still a real improvement on the
  expired-batch method, because nothing was ever pushed to a neighbourhood
  reserve, so only peers on the retrieval path can hold it at all.

- **Retrieval from the holder.** The same node, naming the holder, retrieves it
  completely with a matching SHA-256. Three runs with the spread, bytes per
  second beside every timing. This is the arm with variance and therefore the
  arm rule 7 is about.

- **The limit.** An ingest that crosses it is refused with 507, the usage metric
  matches the chunk count, and no partial collection survives, asserted by the
  usage figure returning to its previous value.

- **No waiting.** Unlike the expiry route, this is testable the moment it is
  built, at any size.

**This also gives the project a sole-source test bed**, with the caveat above.
Every provider measurement so far has depended on a batch expiring, which takes
a day and leaves relay caches as a confound. Content ingested this way was never
pushed anywhere, so a fresh ingest is sole-source by construction rather than by
argument. That matters immediately for
[#327](https://github.com/crtahlin/wasp/issues/327), whose primary arm is a
sole-source download and currently rests on a 404 control plus an assumption
about caches.

## Acceptance

**Accept** if the reference from an unencrypted local ingest equals the reference
from `POST /bytes` of the same bytes at the same redundancy level, **and** a
fresh ingest is unreachable without a hint, **and** it is retrievable with one
over three runs with a matching SHA-256.

**Reject** if the addresses differ **after** the redundancy level and encryption
setting have been confirmed equal on both sides, since a mismatch there is a
setup error rather than a design failure; or if the no-hint control returns 200
on a fresh ingest, which would mean the content reached the network; or if an
abandoned ingest leaves chunks the usage figure does not count.

## Test plan

Unit tests in `package api_test`:

- the reference from an unencrypted local ingest equals the reference from a
  stamped `POST /bytes` of identical bytes at the same redundancy level;
- the handler uses `redundancy.DefaultUploadLevel` when no header is given;
- the endpoint returns 403 when the feature is off;
- re-ingesting an already-held reference returns 200 rather than 500, and leaves
  no dirty collection;
- a body that ends early leaves no dirty collection and no orphaned chunks,
  asserted through the usage figure;
- the limit is enforced mid-stream, returns 507 naming the limit and usage, and
  the partial collection is cleaned up;
- the warning fires below the limit, not only at it;
- an ingested reference is listed among the node's pins, can be unpinned, and
  unpinning removes its `localIngestItem`;
- nothing is handed to the pusher, asserted on a mock.

## Configuration

| Setting | Default | Meaning |
|---|---|---|
| `local-ingest-enable` | `false` | Whether `POST /wasp/local` exists at all. |
| `local-ingest-limit` | conservative, in chunks | The most chunks this node will hold from local ingests. |

**What enabling `local-ingest-enable` costs:** local storage with no rent, content
that only this node holds, no replication so losing the node loses the content,
and a write endpoint reachable by anyone who can reach the API, including a web
page in the operator's own browser (section 3).

**What raising `local-ingest-limit` costs:** disk that nothing will reclaim,
about `limit * 4,104` bytes of store at the limit, as an upper bound because of
refcounting. What lowering it costs: ingests refused.

The warning threshold stays a compiled-in constant under rule 8.

## Documentation

- `docs/DIFFERENCES.md` (rule 13), in three sections it already has: the API
  endpoint, the setting, and the metric and warning log line.
- `openapi/Swarm.yaml`: the `/wasp/local` route.

## Upstream portability

Upstream has no equivalent and no preferred-peer concept, so this is a fork
feature rather than an upstream defect: **no `affects-upstream` marker** under
rule 11. The handler is fork-authored in `pkg/api` and reuses
`storer.NewCollection` and the pipeline unchanged. The one addition outside
`pkg/api` is the `localIngestItem`, which is a new index item in `pkg/storer`
and adds no divergence in existing types.

## Rollout and rollback

- Off unless the flag is set. Nothing changes for an operator who does not
  enable it.
- Rollback is a revert. Content already ingested stays pinned and served, since
  it is an ordinary pinned collection; its `localIngestItem` records become
  unread rather than invalid.
- **A new index item, and no migration**, because absence of the item means "not
  locally ingested", which is true of every collection that exists today. An
  earlier draft said there was no on-disk format change at all, which was wrong.

---

Generated with help of AI.
