# Hosting a directory or website without a stamp

Issue: [#340](https://github.com/crtahlin/wasp/issues/340). Predecessor:
[local-ingest.md](local-ingest.md) for
[#326](https://github.com/crtahlin/wasp/issues/326), with its measured result in
[local-ingest-results.md](local-ingest-results.md).

Code references are to commit `2268503b`, base `upstream/v2.8.2`.

## Terms

- **A manifest** is the index that maps a path inside a collection to the
  reference of the file at that path. Without one, `GET /bzz/{ref}/{path}`
  cannot resolve anything.
- **Mantaray** is the manifest implementation the upload path uses by default
  (`DefaultManifestType`, `pkg/manifest/manifest.go:17`). It is a trie whose
  nodes are themselves chunks.
- **A node chunk** is one serialised mantaray trie node. A manifest costs one
  chunk per node at minimum, and the node count follows the shape of the path
  strings rather than the number of files.
- **A pipeline run** is one pass of the chunk-splitting pipeline over a stream
  of bytes, producing chunks and a single root reference. `storeDir` performs
  one per file and one per manifest node.
- **A dispersed replica** is an extra copy chunk written for redundancy, at the
  root of each pipeline run. At level `MEDIUM` there are 2 of them per run
  (`replicaCounts`, `pkg/file/redundancy/level.go:174`).
- **Residue** is chunks left on disk by a request that did not complete, which
  nothing afterwards counts or removes. **A paired control window** is an
  equal-length period either side of a measured one, recorded to show the node
  was otherwise quiet, so a change during the measured window is attributable.
- **Local ingest** is `POST /wasp/ingest` from #326: it stores content in this
  node's own store with no postage and pushes nothing to the network.
- **The limit** is `local-ingest-limit`, a node-wide ceiling on distinct chunks
  held through local ingest.

## Problem

`POST /wasp/ingest` reproduces the `POST /bytes` path only. It splits a request
body into chunks and returns a file reference
(`pkg/api/localingest.go:109-134`). There is no manifest, so there is no path
resolution, and `GET /bzz/{ref}/{path}` cannot serve anything from it.

The motivation recorded in #326 names gateways and publishers, which means
directories and websites. The merged spec stated the gap rather than leaving it
to be discovered; this is that follow-up.

The gap is narrow, because the hard part is already true: **building a manifest
needs no postage.** `storeDir` takes a bare `storage.Putter` and
`storage.Getter`, alongside a logger, two filenames and a redundancy level, and
**no stamper and no batch** (`pkg/api/dirs.go:148-158`); the manifest loader is
built from that same putter (`dirs.go:163`). Neither `pkg/manifest` nor
`pkg/file/loadsave` references postage at all. So the same argument #326 made
for `POST /bytes` holds unchanged for the manifest path, and this is wiring
rather than a new mechanism.

## Hypothesis

A directory ingested with no postage produces **the same manifest root** as the
same archive uploaded with a stamp at the same redundancy level, and the holding
node serves every path in it. If that holds, hosting a website without postage
needs no new machinery beyond routing the existing directory builder at the
existing local ingest session.

## Design

**One route, selected by an explicit header.**

`POST /wasp/ingest` with **`Swarm-Collection: true`** takes the directory path.
Everything else keeps today's behaviour exactly.

`Content-Type` then selects the archive format, as it already does for `/bzz`:
`application/x-tar` or `multipart/form-data` (`pkg/api/dirs.go:59-71`). An
**empty** content type is refused 400 as `/bzz` refuses it
(`pkg/api/bzz.go:157-161`); a present but unsupported one is refused at
`dirs.go:67-70`. Both apply here.

**Why the header and not the content type alone.** `bzzUploadHandler` also
dispatches on a multipart content type without any header
(`bzz.go:143-144,151`). Copying that here would silently change what an existing
request means: `POST /wasp/ingest` with `Content-Type: application/x-tar`
stores the tar as a blob today, and would store its contents tomorrow.
Requiring the header means no request that works today changes meaning. The
cost is that the two routes are not symmetrical, which is worth one sentence in
the endpoint's documentation.

**Headers honoured**, added to the existing two (`localingest.go:60-63`):

| Header | Effect |
|---|---|
| `Swarm-Collection` | selects the directory path |
| `Content-Type` | selects tar or multipart |
| `Swarm-Index-Document` | stored as `WebsiteIndexDocumentSuffixKey` on the root entry |
| `Swarm-Error-Document` | stored as `WebsiteErrorDocumentPathKey` on the root entry |

The last two are what `storeDir` already accepts (`dirs.go:74-84`,
`dirs.go:210-223`). **`Swarm-Index-Document` is not optional in practice**: with
no root index document, `GET /bzz/{ref}/` answers 404 even when every chunk is
present locally (`bzz.go:631-649`). A website ingested without it can only be
served by full path, so the endpoint warns when a directory ingest omits it.

**What is reused and what is not.**

`storeDir` is reused directly. `dirUploadHandler` is not, for two reasons, both
small: it classifies `postage.ErrBucketFull` (`dirs.go:89`), which cannot occur
here, and it calls `putter.Done(reference)` with the `PutterSession` signature
(`dirs.go:123`), which `LocalIngestSession` does not satisfy because its `Done`
takes a root **and a chunk count** (`pkg/storer/localingest.go:56-76`).

**One session for everything.** The file chunks, every manifest node chunk and
the root all go through the same `countingPutter` wrapping the same
`LocalIngestSession`, and `Done` is called once with the **manifest** root:

```go
session, err := s.storer.NewLocalIngestCollection(r.Context())
counted := newCountingPutter(session)
root, err := storeDir(ctx, headers.Encrypt, reader, logger, counted,
        s.storer.ChunkStore(), indexFilename, errorFilename, rLevel)
...
err = session.Done(root, counted.distinct())
```

This is what the stamped directory path already does with one `PutterSession`
(`dirs.go:123`). Three properties make it safe here:

- `LocalIngestSession.Put` runs one transaction per chunk
  (`pkg/storer/localingest.go:286-292`), so session size is not a concern.
- `countingPutter.admit` takes a mutex (`localingest.go:225-248`), so the
  concurrent node saves that mantaray performs (`pkg/manifest/mantaray/persist.go:72-77`)
  count correctly.
- Passing `counted` as the loader's putter means **manifest chunks are counted
  against the limit**, which they must be: they are chunks on the same disk.

**Every error path routes through the cleanup writer.** The existing handler
answers failures through `ow`, the cleanup-on-error response writer, rather than
the plain `w`, so that writing any status of 400 or above releases the
collection (`localingest.go:99-107`, `pkg/api/api.go:931-939`). The directory
path has more error branches than the blob path, and each one leaks a pinned
collection until restart if it answers through the wrong writer.

**One thing an implementer will wonder about and need not.** `storeDir` hands
the getter to `loadsave.New` (`dirs.go:163`), which raises the question whether
chunks still inside an open `LocalIngestSession` have to be visible through
`ChunkStore()`. They do not: every trie node is held in memory until `Store`, so
no `Load` occurs while a manifest is being built, and the getter is never asked
to see the session's own writes. Note also that `dirUploadHandler` closes the
request body itself (`dirs.go:72`); the reused path needs its own close.

### The chunk-count pre-flight does not work for directories

`localingest.go:82-89` pre-checks the limit from `Content-Length` using
`CalculateNumberOfChunks`, which models a flat blob (`api.go:944-963`). For an
archive that figure is wrong in two directions it cannot be corrected for: it
does not know about tar or multipart framing, and it does not know about
manifest node chunks.

(It also ignores dispersed replicas, but that is **not** directory-specific: the
blob path's pre-flight is already short by the same 2 at the default level. The
two reasons above carry the point on their own, and an earlier draft listed the
replicas as a third as though archives were special.)

**So the pre-flight is skipped for a directory request**, and the mid-stream
`Reserve` path is the only enforcement, which is already the real one
(`pkg/storer/localingest.go:112-121`). Skipping a check that would produce a
confident wrong answer is better than keeping it; the cost is that an
over-limit directory is discovered after the body has been read rather than
before, and the response is the same 507 either way.

### Encryption is allowed and is not equivalent

**The operative cause is not the manifest at all, and an earlier draft of this
spec named the wrong one.** Every encrypted chunk gets a fresh random key
(`pkg/encryption/chunk_encryption.go:21-22`), which the #326 spec already
recorded: two encryptions of identical bytes give different references by
construction. So the **file** references inside the manifest already differ
before mantaray is reached, and an encrypted single-file ingest is
non-deterministic on the existing blob route too. Nothing about this is new
here, and a fixed obfuscation key would not make encrypted directories
deterministic.

On top of that, an encrypted manifest also generates a **random obfuscation key
per trie node** (`pkg/manifest/mantaray/marshal.go:130-138`), where the
unencrypted case sets a zero key once and propagates it to children
(`pkg/manifest/mantaray.go:38-42`), which is what makes the unencrypted case
deterministic.

Three consequences, stated rather than discovered later:

- An encrypted directory ingest is **not deterministic**. The same archive
  ingested twice gives different roots, so the address-equivalence property does
  not hold for it and cannot be asserted.
- `ErrLocalIngestDuplicate` can never fire for one, so a repeat ingest silently
  stores a second copy.
- An encrypted root **cannot be announced**: `/wasp/providers` refuses a 64-byte
  reference because a record would publish its key
  (`pkg/api/providers.go:236-239`).

It is still allowed, because the holder can serve it and refusing it would be a
capability the blob path has and this one does not.

## Configuration

**No new setting.** This uses the existing `local-ingest-enable` and
`local-ingest-limit`, which already exist and are already documented with what
raising and lowering them costs. Rule 8 is satisfied by not adding a dial.

What changes for an operator is that the **same limit now has to cover manifest
chunks as well**, and the cost per file is higher than it looks. `storeDir` runs
a full pipeline **per file** (`dirs.go:185`) as well as per manifest node, and
`hashtrie` writes `replicaCounts[level]` dispersed replicas of the root chunk of
**every** run whose level is not NONE (`pkg/file/pipeline/hashtrie/hashtrie.go:259-269`),
single-chunk files included. So for N files and M manifest nodes the replicas
number 2(N + M) at the default level, not 2M, and a directory of many small
files costs roughly **three chunks per file**. Against the shipped
`local-ingest-limit` of 65,536 that is the number an operator needs, so the
endpoint's documentation gives it rather than saying "more chunks per byte". The
507 response already reports both what is held and the limit
(`localIngestFullResponse`, `localingest.go:36-44`).

## Protocol impact

**No frozen surface is touched.** No message type, no protobuf field, no
handshake value, no constant in `pkg/swarm` or `pkg/config`. Nothing is sent to
another node at any point: local ingest pushes nothing, which #326 measured
rather than asserted. `make protocol-freeze` is unaffected and no
`protocol-change` label applies.

The manifest format itself is unchanged: what this route writes is stock
mantaray, built by upstream's own builder, so a stock node asked for it later
resolves it normally. That follows from the format, not from the equivalence
arm, which compares two uploads on one node and is a separate claim.

## What this risks

- **Under-counting the limit.** If `Reserve` is not taken for manifest chunks,
  the node holds more than it reports and the limit is bypassed permanently for
  that much. This is the same class of defect as the usage gauge written on one
  of three paths, found in the #326 review, and it is what the unpin arm below
  tests.
- **A leaked collection on an error path**, per the `ow` discipline above.
- **An operator expecting a website to serve at the bare root** and getting 404
  because no index document was named.
- **More work done before a request can be refused.** Skipping the pre-flight
  removes the only refusal that happened before the body was read, so a
  directory request now makes the node read an arbitrary archive and write
  chunks up to the limit before refusing, repeatedly, cleaning up each time. The
  #326 spec describes this endpoint as reachable by anyone who can reach the
  API, including a page in the operator's own browser. The decision to skip is
  still right, because the mid-stream claim is exact and no chunk escapes it,
  but the exposure is wider and that is recorded here rather than found later.
- **A stamped pin shadowing an ingest.** An existing pin collection at the same
  root, however it was created, makes a re-ingest answer 200 with
  `soleSource: false`, and that content is then not counted against the local
  ingest limit.

## Measurement

Bench, provider holding the content and requester with no hint.

**Rule 7, and exactly which arms it exempts.** Arms 1, 2, 3 and 5 are
deterministic hash or count comparisons, and rule 7 is about quantities with
variance. `local-ingest-results.md:48-49` takes that position, but note it sits
under the address-equivalence arm alone and does not license a blanket
exemption. **Arms 4, 4b and 6 get three runs with the spread**: a 404 arrives on
a timeout and depends on whether a forwarding peer has cached the content, a
served download reports a rate, and a mid-stream refusal depends on how far the
body got. An earlier draft of this spec claimed the exemption for every arm but
the last, which would have covered the 404 arm wrongly.

Arms:

1. **Manifest equivalence, on one node.** One tar of several files at several
   path depths, ingested unencrypted with an index document, against the same
   tar uploaded through `POST /bzz` with `Swarm-Collection: true`, the same
   redundancy level and the same index document, **both on the holder**. Two
   separate archives, one run each. The node each side runs on is stated in the
   result, because #326's equivalent arm did not state it and the omission cost
   a correction.

   **This claim is host-scoped and the spec says so rather than discovering it
   later.** A manifest is not pure content hashing the way a blob reference is.
   `storeDir` puts host-derived data into it: the entry content type comes from
   `mime.TypeByExtension` (`pkg/api/dirs.go:259`), and Go reads that table from
   files on the host, so two nodes running different distributions can type the
   same file differently. That type is stored in the entry metadata
   (`dirs.go:191-194`) and therefore in the node chunks and the root. Path
   handling has an operating-system branch beside it (`dirs.go:262-271`).

   So equality is claimed **within one process**, which is what this arm tests.
   A cross-host run is a separate and more interesting question, and it is the
   evidence that would justify calling the MIME dependence a defect rather than
   a property. It is not claimed here and not tested here.
2. **Serving the root.** `GET /bzz/{root}/` on the holder returns the index
   document with a matching SHA-256.
3. **Serving every path.** `GET /bzz/{root}/{path}` for each file, SHA-256
   matched against the local original.
4. **Unreachable without a hint.** A second node with no hint asks for the root
   and for one inner path. Both 404. **Three runs**: this is a network
   retrieval whose answer arrives on a timeout and whose outcome depends on
   whether any forwarding peer has cached the content, so it is not a
   deterministic comparison. #326 ran the equivalent arm three times and
   recorded the times.

4b. **Served to a second node that names the holder.** The requester asks with
   `Wasp-Providers` naming the holder, for the bare root and for every inner
   path, each SHA-256 matched against the local original. **Three runs with the
   spread**, since this reports a rate.

   This is the arm that demonstrates what #340 actually asks for, and an earlier
   draft did not have it: arms 2 and 3 read from the holder itself, which is a
   local store lookup and says nothing about hosting. It also exercises the one
   genuinely new interaction with the providers work, which is favourable and
   undemonstrated: a request that already carries a preferred set, such as the
   manifest entry of a `/bzz` download, keeps it, so the lookup uses the
   **manifest root** as the content key rather than re-deriving one per entry
   (`pkg/api/providers.go:65-70,75-77`). Without this arm a reader cannot tell
   whether announcing a manifest root makes the whole site fetchable or only
   its root chunk.
5. **The count covers the manifest.** Record the chunk count the ingest reports,
   and compare it against the rise in `ChunkStore.TotalChunks`
   (`pkg/storer/debug.go:43`) across the ingest on an otherwise idle node, with
   a paired control window either side to show the node is quiet. Run at a
   non-zero redundancy level so the replicas are included. This is the arm that
   catches manifest chunks stored but not counted.

   **An earlier draft of this spec had this arm unpin the root and check that
   reported usage fell by exactly the reported count. That arm cannot fail.**
   The count the ingest reports is the count `Done` writes into the record
   (`pkg/storer/localingest.go:309`), is the count added to the committed total
   (`:247`), and is the count `subtract` removes on unpin (`:433`). All three
   are one number, so an ingest that stores 300 manifest chunks while counting
   none of them reports a short count, commits the short count, releases the
   short count, and passes. The defect the arm existed to catch was invisible to
   it. `TotalChunks` is arrived at independently of anything the code under test
   reports, which is the whole point.
6. **Limit enforcement mid-stream.** A directory ingest against a limit it
   crosses answers 507 and leaves no residue, measured on
   `ChunkStore.TotalChunks` against a paired control window, as #341 did for the
   blob path.

Recorded per run: the reported chunk count, reported usage before and after,
`ChunkStore.TotalChunks`, the HTTP status, and the response body.

## Acceptance

**Accept** if all of:

- the ingested manifest root equals the stamped root for the same archive at the
  same level, on both archives;
- the holder serves the bare root and every inner path with matching SHA-256;
- a node with no hint gets 404 for the root and for an inner path;
- unpinning drops reported usage by exactly the count the ingest reported, at a
  non-zero redundancy level;
- an over-limit directory answers 507 and leaves `ChunkStore.TotalChunks` flat.

**Reject**, meaning the change does not land as written, if any of:

- the roots differ **on one node**, which would mean the ingest path and the
  stamped path disagree and the claim this rests on is false;
- the reported count differs from the `TotalChunks` rise in **either**
  direction: short means chunks are held and not counted, so the limit can be
  bypassed; over means the node reports holding more than it does;
- any error path leaves a pinned collection behind, or leaves `TotalChunks`
  raised after the collection is gone;
- **any Accept condition fails for a reason not listed under what invalidates a
  run.** This clause is here because an earlier draft set five Accept conditions
  against three Reject clauses, leaving at least four outcomes that satisfied
  neither: residue without a leaked collection, an inner path answering 404 on
  the holder, the no-hint node answering 200, and usage moving by more than the
  reported count.

**What invalidates a run** rather than deciding it:

- comparing against a stamped upload at a different redundancy level, with a
  different index document, or with ACT on one side, since each changes the root
  for the same bytes;
- an encrypted arm used for the equivalence comparison, which cannot hold by
  construction;
- the two sides of arm 1 running on **different hosts**, since the MIME table
  and the path handling are host-derived. Different operating systems, or hosts
  with different MIME databases, invalidate that comparison rather than refuting
  the design;
- comparing a tar against a multipart body of the same directory. The two
  readers derive paths and content types differently (`dirs.go:252-286` against
  `:294-320`), so "the same archive" means the same format as well as the same
  bytes.

## Rollout and rollback

Nothing new to turn on. A node with `local-ingest-enable` off refuses directory
ingests with the same 403 it already returns. Rolling back is removing the
flag. Nothing persists that an older build cannot read: the manifest is the
stock format, and the chunk-count record is the one #326 already writes.

## Upstream portability

Low value upstream as it stands, because it is a route over upstream's own
directory builder and upstream has no local-ingest concept for it to attach to.
What **is** portable is the observation that `storeDir` has no postage
dependency at all, which is a property of upstream's code that this work relies
on. That is not a defect, so the route itself carries no `affects-upstream`
label under rule 11.

Two things in the upstream code this route touches were found while writing
this, and they are handled differently on purpose:

- **A malformed `Swarm-Index-Document` returns 500 rather than 400.** The check
  at `dirs.go:170-172` returns a bare `errors.New`, which matches no case in the
  handler's switch and falls to `default: jsonhttp.InternalServerError`
  (`dirs.go:88-97`), while the two neighbouring validation failures both return
  400 correctly. `pkg/api/dirs.go` is unmodified from `upstream/v2.8.2`, checked
  with `git diff --stat upstream/v2.8.2 origin/main -- pkg/api/dirs.go`, which
  prints nothing. This route honours that header and would inherit the
  behaviour. Filed as [#366](https://github.com/crtahlin/wasp/issues/366) with
  `affects-upstream`.
- **The host-derived MIME table**, above. A content-addressed system whose
  address depends on `/etc/mime.types` is arguably a defect, but it is reasoned
  from the code and **not reproduced**, so rule 11 says to leave it untagged and
  record what evidence would justify the tag: a run of the same tar on two hosts
  with different MIME databases, which is the cross-host arm this spec
  deliberately does not claim.

## Files and test plan

- `pkg/api/localingest.go`: header struct, the `Swarm-Collection` branch, the
  pre-flight skip for directories, and the `storeDir` call wired at the counting
  putter.
- `pkg/api/localingest_test.go`, `package api_test`, following the existing
  pattern at `:69-74`. These run against `mockstorer`, so they may assert only
  on what the handler does:
  - `TestLocalIngestDirAddressEquivalence`, ingested root equals the stamped
    root for the same tar at the same level;
  - `TestLocalIngestDirCountsManifestChunks`, the reported count exceeds the
    file chunks alone and equals `mockstorer`'s `StoredChunkCount`
    (`pkg/storer/mock/mockstorer.go:146-156`), which exists precisely to give a
    test a count arrived at independently of what the code reports;
  - `TestLocalIngestDirServesIndexDocument`;
  - `TestLocalIngestDirWithoutIndexDocument`, the warning fires and the bare
    root is not servable;
  - `TestLocalIngestDirRejectsMissingContentType`, and a present but unsupported
    one;
  - `TestLocalIngestDirEncryptedIsNotDeterministic`, two ingests of the same
    archive give different roots, recording the documented behaviour so it is
    not mistaken for a defect later;
  - `TestLocalIngestBlobUnchangedWithoutCollectionHeader`, for **both** a tar
    and a **multipart** body. Multipart is the sharper case, because `/bzz`
    treats a multipart body as a directory with no header at all
    (`pkg/api/bzz.go:151`), so it is where the two routes deliberately differ.
- `pkg/storer/localingest_test.go`, `package storer_test`, against a real
  database. The predecessor spec records why, and an earlier draft of this one
  repeated the mistake it warns about: `mockstorer.DeletePin` never touches the
  committed total (`mockstorer.go:270-281`) and its `Cleanup` removes no chunks
  (`:125-136`), so neither release nor residue can be asserted in `api_test` at
  all. The mock says so itself at `mockstorer.go:63-66`:
  - `TestLocalIngestDirUnpinReleasesTheCount`;
  - `TestLocalIngestDirLimitMidStreamLeavesNoResidue`, the 507 through `ow` with
    the collection released and the chunk count back where it started;
  - `TestLocalIngestDirDuplicate`, including the case that matters to an
    operator: an existing pin collection at that root, whatever created it,
    makes a re-ingest answer 200 with `soleSource: false` and the content is
    **not** counted against the local ingest limit. So a site already pinned
    from a stamped upload shadows an ingest of it.
- `openapi/Swarm.yaml`: the new request shape. Fix the 507 field name while
  there: the schema says `chunks` (`openapi/Swarm.yaml:1414`) and the code emits
  `held` (`localingest.go:42`).
- `docs/DIFFERENCES.md`: amend the local ingest row, since the endpoint accepts
  something new.
- `docs/experiments/content-providers/`: the results document.

Generated with help of AI.
