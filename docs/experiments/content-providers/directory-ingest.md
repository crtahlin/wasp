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
- **A dispersed replica** is an extra copy chunk written for redundancy. At
  level `MEDIUM` there are 2 of them per pipeline run
  (`replicaCounts`, `pkg/file/redundancy/level.go:174`).
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
`storage.Getter` and nothing else (`pkg/api/dirs.go:148-158`), and the manifest
loader is built from that same putter (`dirs.go:163`). Neither `pkg/manifest`
nor `pkg/file/loadsave` references postage at all. So the same argument #326
made for `POST /bytes` holds unchanged for the manifest path, and this is
plumbing rather than a new mechanism.

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
`application/x-tar` or `multipart/form-data` (`pkg/api/dirs.go:59-71`). A
directory request with neither is refused 400, matching
`pkg/api/bzz.go:157-161`.

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
answers failures through `ow`, not `w`, so `cleanupOnErrWriter` releases the
collection (`localingest.go:99-107`, `pkg/api/api.go:931-939`). The directory
path has more error branches than the blob path, and each one leaks a pinned
collection until restart if it answers through the wrong writer.

### The chunk-count pre-flight does not work for directories

`localingest.go:82-89` pre-checks the limit from `Content-Length` using
`CalculateNumberOfChunks`, which models a flat blob (`api.go:944-963`). For an
archive that figure is wrong in three separate directions: it does not know
about tar or multipart framing, it does not know about manifest node chunks, and
it does not know about dispersed replicas, of which there are 2 per pipeline run
at the default level and one run per manifest node.

**So the pre-flight is skipped for a directory request**, and the mid-stream
`Reserve` path is the only enforcement, which is already the real one
(`pkg/storer/localingest.go:112-121`). Skipping a check that would produce a
confident wrong answer is better than keeping it; the cost is that an
over-limit directory is discovered after the body has been read rather than
before, and the response is the same 507 either way.

### Encryption is allowed and is not equivalent

An encrypted manifest generates a **random obfuscation key per node**
(`pkg/manifest/mantaray/marshal.go:130-138`); the unencrypted case sets a zero
key once (`pkg/manifest/mantaray.go:38-42`). Three consequences, stated rather
than discovered later:

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

**No new setting.** This rides on `local-ingest-enable` and
`local-ingest-limit`, which already exist and are already documented with what
raising and lowering them costs. Rule 8 is satisfied by not adding a dial.

What changes for an operator is that the **same limit now has to cover manifest
chunks as well**, and a directory of many small files costs more chunks per byte
than one large file. The endpoint's documentation says so, and the 507 response
already reports both what is held and the limit
(`localIngestFullResponse`, `localingest.go:36-44`).

## Protocol impact

**No frozen surface is touched.** No message type, no protobuf field, no
handshake value, no constant in `pkg/swarm` or `pkg/config`. Nothing is sent to
another node at any point: local ingest pushes nothing, which #326 measured
rather than asserted. `make protocol-freeze` is unaffected and no
`protocol-change` label applies.

The manifest format itself is unchanged. An ingested manifest is byte-identical
to a stamped one for the same input, which is the first acceptance condition
below, so a stock node asked for it later resolves it normally.

## What this risks

- **Under-counting the limit.** If `Reserve` is not taken for manifest chunks,
  the node holds more than it reports and the limit is bypassed permanently for
  that much. This is the same class of defect as the usage gauge written on one
  of three paths, found in the #326 review, and it is what the unpin arm below
  tests.
- **A leaked collection on an error path**, per the `ow` discipline above.
- **An operator expecting a website to serve at the bare root** and getting 404
  because no index document was named.

## Measurement

Bench, provider holding the content and requester with no hint.

**Rule 7 and why three runs do not apply to most of this.** Every arm but the
last is a deterministic hash or count comparison, and rule 7 is about
quantities with variance. `local-ingest-results.md:48-49` takes the same
position for the same reason. Any arm reporting a rate or a duration gets three
runs with the spread.

Arms:

1. **Manifest equivalence.** One tar of several files at several path depths,
   ingested unencrypted with an index document, against the same tar uploaded
   through `POST /bzz` with `Swarm-Collection: true`, the same redundancy level
   and the same index document. Two separate archives, one run each.
2. **Serving the root.** `GET /bzz/{root}/` on the holder returns the index
   document with a matching SHA-256.
3. **Serving every path.** `GET /bzz/{root}/{path}` for each file, SHA-256
   matched against the local original.
4. **Unreachable without a hint.** A second node with no hint asks for the root
   and for one inner path. Both 404.
5. **The count covers the manifest.** Record the chunk count the ingest reports,
   then unpin the root and confirm reported usage falls by **exactly** that
   count. This is the arm that catches manifest chunks stored but not counted,
   and it must be run at a non-zero redundancy level so the replicas are in
   scope.
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

- the roots differ, which would mean something node-specific reaches the
  manifest and the claim this rests on is false;
- usage falls by less than the reported count on unpin, which means chunks are
  held and not counted and the limit can be bypassed;
- any error path leaves a pinned collection behind.

**What invalidates a run** rather than deciding it: comparing against a stamped
upload at a different redundancy level or with a different index document, since
either changes the root for the same bytes; or an encrypted arm used for the
equivalence comparison, which cannot hold by construction.

## Rollout and rollback

Nothing new to turn on. A node with `local-ingest-enable` off refuses directory
ingests with the same 403 it already returns. Rolling back is removing the
flag. Nothing persists that an older build cannot read: the manifest is the
stock format, and the chunk-count record is the one #326 already writes.

## Upstream portability

Low value upstream as it stands, because it is a route over upstream's own
directory builder, and upstream has no local-ingest concept to hang it on. What
**is** portable is the observation that `storeDir` has no postage dependency at
all, which is a property of upstream's code that this work relies on. That is
not a defect, so no `affects-upstream` label under rule 11.

## Files and test plan

- `pkg/api/localingest.go`: header struct, the `Swarm-Collection` branch, the
  pre-flight skip for directories, and the `storeDir` call wired at the counting
  putter.
- `pkg/api/localingest_test.go`, `package api_test`, following the existing
  pattern at `:69-74`:
  - `TestLocalIngestDirAddressEquivalence`, ingested root equals the stamped
    root for the same tar at the same level;
  - `TestLocalIngestDirCountsManifestChunks`, the reported count exceeds the
    file chunks alone and matches what unpin releases;
  - `TestLocalIngestDirServesIndexDocument`;
  - `TestLocalIngestDirWithoutIndexDocument`, the warning fires and the bare
    root is not servable;
  - `TestLocalIngestDirRejectsMissingContentType`;
  - `TestLocalIngestDirLimitMidStream`, 507 through `ow` with the collection
    released;
  - `TestLocalIngestDirEncryptedIsNotDeterministic`, two ingests of the same
    archive give different roots, pinning the documented behaviour so it is not
    mistaken for a defect later;
  - `TestLocalIngestBlobUnchangedByCollectionHeaderAbsent`, a tar with no
    `Swarm-Collection` still stores as a blob.
- `openapi/Swarm.yaml`: the new request shape. Fix the 507 field name while
  there: the schema says `chunks` (`openapi/Swarm.yaml:1414`) and the code emits
  `held` (`localingest.go:42`).
- `docs/DIFFERENCES.md`: amend the local ingest row, since the endpoint accepts
  something new.
- `docs/experiments/content-providers/`: the results document.

Generated with help of AI.
