# Hosting content without a stamp: local ingest

Issue: [#326](https://github.com/crtahlin/wasp/issues/326). A node puts content
into its own store, pays no postage, and serves it to anyone who asks for it.

Code references are to `main` at `7bd5da14`, base `upstream/v2.8.2`.

**This is revision 5.** Four adversarial reviews. Revision 5 fixes a crash this
document's own design would have caused, and three mechanisms that could not be
built as described. Where a claim has changed, the earlier one is marked rather
than removed.

## Terms

- **Sole-source content**: content that no other node holds, so a requester can
  get it only from this one.
- **Collection**: a set of chunks committed together under one root reference,
  which is how pinning stores things (`pkg/storer/pinstore.go:36-61`).
- **Payload bytes** against **stored bytes**: a 4,194,304-byte file is 1,033
  chunks, and a full chunk costs 4,096 data bytes plus an 8-byte span, so at
  redundancy level NONE the store holds about 4,239,432 bytes for it. **The
  route defaults to MEDIUM**, which adds parity chunks and two dispersed root
  replicas, so the real figure is roughly a tenth higher again. An earlier
  draft gave the NONE figure as an upper bound, which it is not. Payload and
  stored bytes are not interchangeable, and this document says which it means
  every time.
- **Refcount**: `chunkstore` keeps one copy of a chunk with a reference count
  (`pkg/storer/internal/chunkstore/chunkstore.go:92`), so a chunk this node
  already holds costs no new disk when ingested again.

## Problem

**A node cannot host content unless somebody is paying network rent for it.**

- **Pinning needs no stamp.** `pinRootHash` reads only
  `Swarm-Redundancy-Level` (`pkg/api/pin.go:25-59`); there is no postage header.
  It stores through `s.storer.NewCollection(r.Context())` (`:61`), an unstamped
  putter, and those chunks are served like any others because the retrieval
  handler answers from `Lookup()` (`pkg/retrieval/retrieval.go:589`), which
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
POST /wasp/ingest
```

**Not `/wasp/local`**, which an earlier draft proposed. "Local" already has a
settled meaning on the wire in this fork: `LocalOnlyHeader` is
`wasp-local-only` (`pkg/retrieval/preferred.go:29`), the header meaning "answer
from your own store, do not forward". A route called `/wasp/local` would mean
something unrelated to it.

- Body: the content, as `POST /bytes` takes it.
- Headers honoured: `Swarm-Redundancy-Level`, `Swarm-Encrypt`.
- Response 201 with the reference, plus a field saying the content is held by
  this node only. **It is not an upload and the response must not read like
  one**: the reference resolves for nobody else unless they ask this node, and
  losing this node loses the content.
- 200 when the reference is already held, matching `pinRootHash`
  (`pkg/api/pin.go:49-59`) rather than failing on a duplicate. See 2(a).
- 403 when the feature is off, following `providersEnabled`
  (`pkg/api/providers.go:210-216`), which is the existing precedent for a fork
  route gated by a setting.
- 507 when the limit is reached, naming the limit and current usage.

**The route is mounted whether or not the feature is on, and answers 403 when it
is off.** An earlier draft said both this and "the route must not exist unless
the operator enabled it", which are different designs. This one is chosen
because it is what `providersEnabled` already does, and because a conditional
`handle` call is a larger change for no gain: an unmounted route and a route
that always refuses are indistinguishable to anyone probing the API.

**Two things `mountAPI`'s `handle` closure does automatically**
(`pkg/api/router.go:251-255`), neither of which an earlier draft mentioned:

- it registers `/v1/wasp/ingest` alongside the bare path, so the mirror exists
  whether or not this document asks for it;
- it wraps the route in `s.checkRouteAvailability`, so the route answers 503
  until the node is fully up.

**The route is registered through `handle`, with that wrapper, and an earlier
draft of this document was wrong to drop it.** The argument for dropping it was
that an ingest touches no network and so ought to work while a node is still
catching up. That nicety buys a crash.

The API server starts serving at `pkg/node/node.go:706`. `storer.New` is not
called until `:1056`, and `s.storer` is not assigned until `Configure` at
`:1669`. Every ordinary route survives that gap **because** `checkRouteAvailability`
answers 503 until `EnableFullAPI` at `:1674`, which runs after both. A route
registered outside it is reachable with `s.storer` still nil, which is a nil
dereference reached from an ordinary local HTTP request, for the whole of chain
sync and kademlia bootstrap.

The fork's own `/wasp/providers` routes go through `handle` for the same reason
(`pkg/api/router.go:416-427`). The `providersEnabled` precedent this document
cites is a precedent for the 403 flag, not for dropping the availability guard.

CORS needs nothing new: `Swarm-Redundancy-Level` and `Swarm-Encrypt` are already
allowed (`pkg/api/api.go:615-622`).

**Directories are out of scope for this issue.** The sketch below reproduces the
`POST /bytes` path only. A website or a directory needs the manifest path that
`fileUploadHandler` builds (`pkg/api/bzz.go:173-306`), with the directory case
in `dirUploadHandler` (`pkg/api/dirs.go:39`), and which
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
session, err := s.storer.NewLocalIngestCollection(ctx)   // fork-authored, section 4
ow := &cleanupOnErrWriter{ResponseWriter: w, onErr: session.Cleanup, logger: logger}
counted := &countingPutter{Putter: session, limit: room} // pointer: it mutates
p := requestPipelineFn(counted, encrypt, rLevel)
reference, err := p(ctx, r.Body)                 // every error answered through ow
err = session.Done(reference)                    // writes the localIngestItem too
```

**`cleanupOnErrWriter` wraps the RESPONSE WRITER, not the body**, and an earlier
draft of this document said the opposite and sketched code that would not
compile. It embeds `http.ResponseWriter` and fires `onErr` from `WriteHeader`
when the status is 400 or above (`pkg/api/api.go:913-925`); both upload handlers
pass `w`, and feed the pipeline `r.Body` directly (`bytes.go:107-108`).

That is not a typo with no consequence. It means **cleanup happens because the
handler writes its error status through `ow`**, so every error path in the
handler must go through `ow` or the leak section 2(b) is about comes straight
back. It also means the 507 path already triggers `Cleanup` through `ow`, and an
explicit second call is harmless only because `collectionPutter.Cleanup` returns
nil once closed (`pinning.go:157-159`).

**(a) The duplicate path.** `putter.Done` on an already-pinned root returns
`ErrDuplicatePinCollection` (`pkg/storer/internal/pinning/pinning.go:136-138`).
`pinRootHash` avoids it by checking `HasPin` first and returning 200
(`pin.go:49-59`); `DB.Upload` handles it by calling `Cleanup`
(`pkg/storer/uploadstore.go:104-106`). Without either, re-ingesting the same
bytes returns 500 where it should return 200. It does **not** leak, and an
earlier draft said it did: as long as that 500 is written through `ow`, which
the sketch requires of every error path, `WriteHeader` fires `Cleanup` and
`Close` returns before marking the session closed (`pinning.go:137`), so the
dirty record, the index entries and the refcounts are all removed. The
consequence of missing this is a wrong status code. The acceptance plan below
ingests the same content repeatedly, so it fires immediately.

**And the duplicate cannot be pre-checked the way pinning does it.**
`pinRootHash` can call `HasPin` first because the client gave it the reference;
this route does not know the reference until the body has been split. So it
follows `DB.Upload`'s shape instead: let `Done` return
`ErrDuplicatePinCollection`, call `Cleanup`, answer 200
(`pkg/storer/uploadstore.go:104-106`). A duplicate ingest therefore still costs
a full read and split.

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

**(c) The count comes from a putter wrapper, because the pipeline cannot be
watched.** `requestPipelineFn` returns `func(context.Context, io.Reader)
(swarm.Address, error)` and the builder feeds to end of input before returning,
so the handler sees nothing until it is over. An earlier draft said the handler
"counts chunks as the pipeline produces them", which is not something it can do.
The counter therefore wraps the putter, and three things follow:

- **It must be safe for concurrent use.** `replicas.putter.Put`
  (`pkg/replicas/putter.go:37-65`, joining errors at `:64`) calls the wrapped
  putter from several goroutines when the dispersed root replicas are stored,
  so a plain integer is a data race.
- **The abort is clean**, which is the one part of this that works without
  effort: an error from the wrapper propagates synchronously back out of the
  pipeline, and the replicas putter joins errors rather than swallowing them.
- **It must report the condition out of band, not only as a wrapped error.**
  A sentinel tested with `errors.Is` is not enough, because `hashtrie.Sum`
  formats the dispersed-replica failure with `%s` against `err.Error()` rather
  than `%w` (`pkg/file/pipeline/hashtrie/hashtrie.go:267`), so the chain is
  discarded and `errors.Is` returns false. That put happens inside `Sum`, after
  the whole body has been read, which is exactly where a large ingest crosses
  its limit. So the handler asks the wrapper directly whether the limit was
  exceeded when the pipeline returns any error, and that is robust against this
  formatting and against any future wrapping. Filed separately as
  [#337](https://github.com/crtahlin/wasp/issues/337), tagged
  `affects-upstream`.

**(d) The redundancy default is the upload one.** `pkg/api/bytes.go:48` uses
`redundancy.DefaultUploadLevel`, which is `MEDIUM`; `pkg/api/pin.go:44` uses
`DefaultDownloadLevel`, which is `PARANOID`
(`pkg/file/redundancy/level.go:178`, `:181`). Implemented by copying `pin.go`,
which is this document's own worked example of an unstamped putter, the
address-equivalence test below fails for a reason that has nothing to do with
the feature, and a reviewer would read that as the Reject condition.

**(e) Nothing reaches the pusher, and nothing else observes these chunks.**
Verified rather than asserted. `SubscribePush` iterates `upload.IteratePending`
only (`pkg/storer/subscribe_push.go:35`) and `pkg/pusher/pusher.go:120` is its
only consumer, while a collection writes `pinChunkItem` plus the chunkstore. The
reserve is fed only by the push-sync and pull-sync putters. The cache getter
returns a chunkstore hit without creating a cache entry
(`pkg/storer/internal/cache/cache.go:143-148`). No stamp index entry is written.
`/tags` never sees an ingest, so there is no progress reporting, which is worth
saying rather than leaving a caller to find out.

**(f) Locking is already handled.** `pinstore`'s doc comments demand caller
serialisation (`pinning.go:70`, `:88`), and `DB.NewCollection` takes
`db.Lock(uploadsLock)` inside its put, done and cleanup closures
(`pkg/storer/pinstore.go:36-61`), so a new route inherits the same serialisation
the pin route gets. The operator-visible consequence, stated more precisely
than an earlier draft did: `uploadsLock` is taken and released inside the
per-chunk put
(`pinstore.go:40-41`), not held for the session, so whole uploads do not
serialise against each other. What contends is chunk writes, on one node-wide
mutex shared with every other upload. That is a throughput cost, not
serialisation.

**(g) Node mode.** `pinRootHash` has no mode check, so a light node can pin;
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

So the **feature flag is the real control**, not a convenience: with it off the
handler refuses every request, so a node whose API is exposed for another reason
does not silently gain a way to be filled. The route is still mounted and
answers 403, per section 1; an unmounted route and one that always refuses are
indistinguishable to anyone probing the API, and this way follows the precedent
that already exists. There is existing
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

So: **a `localIngestItem`, keyed by the root reference, holding the chunk
count.** That gives the scoped limit and the listing together.

**It is written inside the same transaction as the collection commit**, and
"when the ingest commits" was not precise enough in an earlier draft. Written
after `Done` returns, a crash in between leaves a committed collection with no
item, so usage undercounts for good and that much of the limit is bypassed
permanently. Written before, a failing `Close` leaves an orphan item and usage
overcounts for good. Neither is repairable, because `pinstore.CleanupDirty`
(`storer.go:927`) iterates `dirtyCollection` and would know nothing about a new
namespace.

The transaction lives in the `done` closure of `DB.NewCollection`
(`pkg/storer/pinstore.go:50-56`), which calls
`db.storage.Run(ctx, func(s transaction.Store) error { ... })`, and `pkg/api`
cannot reach `s.IndexStore()`. **So this needs a fork-authored
`DB.NewLocalIngestCollection` in `pkg/storer`**, which is the same shape with
the extra write inside the same `Run`. That is buildable without touching the
internal package: `pinstore.NewCollection` is exported, `putterSession` is in
`package storer`, and `indexTrx.Put` writes into the same batch that `Run`
commits, so the item and the collection commit atomically.

**But the count has to reach that closure, and `Done` has nowhere to put it.**
`PutterSession.Done` is `Done(swarm.Address) error` (`storer.go:62`), and the
closure cannot see a wrapper living in `pkg/api`. An earlier draft had the
counter in the handler and the write in the storer with no route between them.

So `NewLocalIngestCollection` returns a **fork-specific session type** whose
`Done` carries the count:

```go
type LocalIngestSession interface {
    storage.Putter
    Done(root swarm.Address, chunks uint64) error
    Cleanup() error
}
```

It follows that no commit-time crash can leave an orphan item, because the item
and the collection are written together. The startup pass below is not for that
case; it is for the unpin case in section 5.

**The item must size its buffer for a 64-byte reference**, as
`pinCollectionItem` does with `encryption.ReferenceSize` (`pinning.go:350`,
with the length test on unmarshal at `:386`). The route honours `Swarm-Encrypt`,
and a 32-byte assumption would break every encrypted ingest silently.

**The running total is held in memory and rebuilt at startup** by one iteration
over the namespace. The alternative, a singleton total item, is a second
transactional write and therefore a second way for two records to disagree.

Four things an earlier draft left unanswered:

- **Where the rebuild runs**: beside `pinstore.CleanupDirty` in `storer.New`
  (`storer.go:926-927`), which completes before the API is built and before the
  listener opens (`node.go:668`), so there is no window in which the route
  serves against an unbuilt total.
- **Crash safety**: the item is committed with the collection and
  `CleanupDirty` touches only dirty collections, so a rebuild from disk is
  always correct. The same pass drops any item whose root no longer answers
  `HasPin`, which is what repairs the orphan case in section 5.
- **Unpin decrements it**, section 5.
- **Concurrent ingests need a reservation, not a check.** `uploadsLock` is per
  chunk, not per session, so two ingests interleave. Checking a committed total
  lets both pass and together exceed the limit; incrementing the committed total
  as chunks arrive inflates it for good on every 507, every abandoned request
  and every duplicate re-ingest, which would also break this document's own
  acceptance assertion that the usage figure returns to its previous value.

  So: under the total's mutex, **reserve** one unit for each newly distinct
  address, and refuse when `committed + reserved` would cross the limit. The
  session's whole reservation is released on `Cleanup`, on the duplicate-root
  path and on any error, and is converted into the committed total when the
  `done` transaction commits.

- **Lock order.** Two mutexes exist: one guarding a session's distinct-address
  set, because the replicas putter calls `Put` from several goroutines, and one
  guarding the node-wide total. **The total's mutex is never held while calling
  into the storer.** That is what keeps it clear of `uploadsLock`, which
  `DB.DeletePin` holds for its whole body (`pinstore.go:65-81`) and would
  otherwise give an AB/BA inversion against a non-reentrant multex
  (`storer.go:1046-1052`).

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
network, It does **not** overcount, and an earlier draft said it did:
`Total` rises at `:95` and `DupInCollection` at `:108`, and `debug.go:136`
subtracts them, which is the distinct-chunk count. The reasons to reject it
stand without that.

**Enforcement is mid-stream, not pre-flight.** `Content-Length` is absent under
chunked transfer encoding and is client-supplied in any case, so a pre-flight
refusal cannot be the only gate. The counting putter of 2(c) stops on crossing
the limit, and the handler answers 507 through `ow`, which triggers cleanup. A
pre-flight check on `Content-Length` when it is present refuses before reading
the body, which is a convenience rather than the enforcement.

**One number, counted by the wrapper itself.** An earlier draft had two: `Put`
calls for the mid-stream limit, and the collection's own
`Total - DupInCollection` read back at commit for the metric. **The read-back
cannot be done**, on two independent grounds. `pinstore.NewCollection` returns
`internal.PutterCloserWithReference`, whose only methods are `Put`, `Close` and
`Cleanup` (`pkg/storer/internal/internal.go:20-25`), and the one exported reader
of the stat hands its callback a bare `CollectionStat` with no address attached
(`pinning.go:337-347`), so a stat cannot be matched to a root. And a read inside
the same transaction would not see it anyway: `Close` writes into the batch, and
a `Get` in that transaction reads the underlying store.

So **the counting wrapper keeps the set of distinct chunk addresses it has
seen** and reports that count. That is exactly `Total - DupInCollection`, with
no read-back, no access to unexported internals, and no second transaction. The
mid-stream limit and the recorded usage become the same quantity, which is one
fewer thing to explain and one fewer way for two records to disagree.

**The equality holds for a reason worth writing down, because a later change
could break it silently.** `Total` rises on every `Put` (`pinning.go:95`) and
`DupInCollection` rises exactly when the chunk is already in *this* collection
(`:101`, `:108`), keyed by the collection UUID, so a chunk the node already
holds elsewhere counts as distinct and its refcount is bumped
(`chunkstore.go:92`). The wrapper sits one-to-one above that call. It works
**only because each chunk is its own `db.storage.Run`** (`pinstore.go:42`) and
`indexTrx.Has` reads the underlying store rather than the pending batch
(`transaction.go:291`). Batch a session's puts into one transaction and
`DupInCollection` stops counting, and this equality goes with it.

**The cost is memory during an ingest**, and an earlier draft understated it by
naming the address width rather than the cost of holding it. A
`map[[32]byte]struct{}` costs roughly 60 bytes an entry at realistic load
factors, so a million-chunk ingest is of the order of 60 MB, and a
`map[string]struct{}` would be nearer 110. The implementation uses the array key
for that reason. That is the price of not needing the read-back, and it is
stated rather than hidden.

Two things it still does not count, so the figure remains an **upper bound on
disk**: `chunkstore.Put` only increments a refcount for a chunk the node already
holds (`chunkstore.go:92`), and nothing here knows what the reserve or cache
already has.

**The 507 may not reach a client that is still uploading.** Go's server discards
only a bounded amount of an unread request body before closing the connection,
so a client still writing a large body is likely to see a reset rather than the
status. The test for this keeps the body small enough to observe the status, and
accepts a connection error as the alternative outcome rather than treating it as
a failure.

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
no expiry. So removal is part of the design, and the existing unpin route
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

**Unpinning must remove the item, and that is an edit to an upstream
function.** It goes in `DB.DeletePin` (`pkg/storer/pinstore.go:64-80`), and an
earlier draft's reason for choosing it was wrong: it said the removal would then
share the collection delete's transaction. It cannot. `DB.DeletePin` is a
five-line wrapper that locks and calls `pinstore.DeletePin`, and the transaction
lives inside the internal package (`pinning.go:309-315`). Putting the removal
inside it would mean editing that package, which cannot reference a
`localIngestItem` defined in `pkg/storer` without an import cycle.

**The order of the two steps matters and only one of them is repairable.**
`pinstore.DeletePin` runs **first**. Only once it returns does the item get read,
deleted, and its count subtracted from the total. The other order is
unrepairable: item gone, root still answering `HasPin`, chunks still on disk,
and nothing to detect it. A missing item is a no-op, because an ordinary pin has
none.

**The decrement needs the item's count**, so the item is read before it is
deleted. And it happens outside the total's mutex-with-storer rule from section
4: `DB.DeletePin` holds `uploadsLock` for its whole body, so taking the total's
mutex inside it is only safe because the ingest path never holds that mutex
while taking `uploadsLock`.

**What the startup repair covers, and what it does not.** It covers the window
after `pinstore.DeletePin` returns. It does not cover a crash **inside** it:
that function deletes the collection chunks in many independent per-chunk
transactions and the root last (`pinning.go:275-286`, `:309-315`), so a crash
part way leaves the root present, `HasPin` true, the item surviving, and its
count referring to chunks that are gone. `CleanupDirty` handles only
`dirtyCollection` and `DeletePin` writes no dirty marker. That is upstream
behaviour rather than something introduced here, and it is stated so the repair
is not read as covering more than it does.

One more case the repair cannot distinguish: a root that is ingested locally,
unpinned, then pinned again from the network leaves an item `HasPin` cannot tell
from a live one.

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
  fail to retrieve it. Expect 404. **This uses different content from the
  address-equivalence arm**, which publishes its bytes to the network by
  definition; an earlier draft used one file for both and the second arm could
  not have passed.

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
- the counting putter is safe under the concurrent calls the replicas putter
  makes, asserted under the race detector;

Tests in `package storer_test`, against a real database rather than the mock,
because `pkg/storer/mock/mockstorer.go` has no dirty-collection concept at all:
its `NewCollection` never returns `ErrDuplicatePinCollection` and its
`Cleanup()` returns nil unconditionally. An earlier draft put these in
`package api_test`, where they cannot be written:

- re-ingesting an already-held reference returns 200 rather than 500, and leaves
  no dirty collection;
- a body that ends early leaves no dirty collection and no orphaned chunks,
  asserted through the usage figure;
- the `localIngestItem` and the collection commit in one transaction, so a
  failure in either leaves neither;
- the item round-trips a 64-byte encrypted reference;
- the limit is enforced mid-stream, returns 507 naming the limit and usage, and
  the partial collection is cleaned up;
- the warning fires below the limit, not only at it;
- an ingested reference is listed among the node's pins, can be unpinned, and
  unpinning removes its `localIngestItem`;
- nothing is handed to the pusher, asserted on a mock.

## Configuration

| Setting | Default | Meaning |
|---|---|---|
| `local-ingest-enable` | `false` | Whether `POST /wasp/ingest` does anything. The route is always mounted and answers 403 when this is off. |
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
- `openapi/Swarm.yaml`: the `/wasp/ingest` route.

## Upstream portability

Upstream has no equivalent, so this is a fork feature rather than an upstream
defect: **no `affects-upstream` marker** under rule 11 for the feature itself.
One defect found while specifying it does carry the marker and is filed
separately as [#337](https://github.com/crtahlin/wasp/issues/337).

**But it is not confined to `pkg/api`, and an earlier draft said it was.** Four
things are needed outside it, two of them inside upstream functions, which is
what the next upstream sync will meet:

1. **`localIngestItem` and `DB.NewLocalIngestCollection`** in `pkg/storer`. New
   files, so no upstream edit.
2. **A method on the API's `Storer` interface** (`pkg/api/api.go:141-155`),
   which the fork already edits.
3. **`pkg/storer/mock/mockstorer.go`** must grow the same method or the
   `pkg/api` tests stop compiling. Upstream file.
4. **The unpin hook** in `DB.DeletePin` (`pkg/storer/pinstore.go:64-80`).
   Upstream file.
5. **Nothing in `pkg/jsonhttp`.** It has no `InsufficientStorage` helper, but
   `jsonhttp.Respond(w, statusCode, response)` is generic (`jsonhttp.go:47`), so
   the fork's handler answers 507 through it and no upstream file is touched.
   Counted here because an earlier draft listed it as an edit.
6. **The two settings** in `cmd/bee/cmd/cmd.go` and their wiring in
   `pkg/node/node.go`. Both upstream files, and the Configuration section
   requires them.
7. **`openapi/Swarm.yaml`**, named under Documentation.

An earlier draft counted four and said the work was confined to `pkg/api` plus
one new item.

Nothing here changes an existing type's shape, and the pipeline and
`storer.NewCollection` are reused unchanged.

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
