# Hosting a directory or website without a stamp: results

Issue: [#340](https://github.com/crtahlin/wasp/issues/340). Spec:
[directory-ingest.md](directory-ingest.md), merged as
[`ef00d3eb`](https://github.com/crtahlin/wasp/commit/ef00d3eb). Implementation
merged as [`d32cb541`](https://github.com/crtahlin/wasp/commit/d32cb541).

Measured 2026-09-19 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester. Harness `cp290/t19.sh`, rows in
`cp290/t19-directory-ingest.txt`, both outside this repository per rule 10.

**Terms.** A **collection** is a directory stored with the manifest that maps a
path to the file at that path. **The reported count** is the `chunks` field of
the ingest response. **`TotalChunks`**, **`SharedSlots`** and
**`ReferenceCount`** are the three `ChunkStore` counters `/debugstore` exposes
(`pkg/storer/debug.go`): distinct chunk entries, slots shared by more than one
reference, and total references. A **control window** is an equal period read
immediately before a measured one, showing how far the counters drift with the
node doing nothing.

**The feature is accepted.** A directory ingested with no postage produces the
same manifest root as a stamped upload of the same archive, the holder serves
every path in it, a node with no hint cannot reach it, a node that names the
holder can, the reported chunk count is exact, and an over-limit archive is
refused without residue.

**Two defects were found in the spec rather than in the code**, and both were
found only by running it. They are in their own section below because they are
the useful part of this document.

## The provider was running the wrong build, and the first arm failed against it

Recorded first because it nearly produced a false refutation.

The provider was on `0.1.3-f005605d`, the build from the #326 branch, which has
no #340 code at all and therefore ignores `Swarm-Collection` and stores the
archive as a single blob. Arm 1 ran against it and the roots differed.

That was not read as a result, because the ingest reported 19 chunks for a
five-file site, too few for a manifest. **Proved rather than assumed**: the same
tar ingested with the header and without it returned the identical reference,
`6b16797b6648bcdc31d6ad11f1745bfe2e9f18b3fa9ec84bdfab799b4a8b87e6`, so the
header was being ignored.

Redeployed from `main` at `d372367f` as `0.1.3-340-d372367f`, with the unit, the
process and the version read back after the restart rather than the port, which
is what `deploy-326.sh` warns about in its own header. Arm 1 then passed at the
first attempt. The requester stayed on `0.1.3-353-5e527a22`: this is a
provider-side feature.

## Preconditions, measured

`ChunkStore.TotalChunks` was **flat across four consecutive 20-second windows**
before anything ran, which is what arms 5 and 6 need and what
[local-ingest-results.md](local-ingest-results.md) records an earlier pass
failing to have. Local ingest usage was 119,063 of 131,072, so **12,009 chunks
of headroom**, and that figure turns out to matter for arm 6. The provider grant
was zero on both settings.

## What was measured

### 1. Manifest equivalence, on one node

| Archive | Ingested root | Chunks | Stamped root | Equal |
|---|---|---|---|---|
| site | `3257e80e...11927b0c` | 53 | `3257e80e...11927b0c` | yes |
| site2 | `245f07cf...bd98206c` | 35 | `245f07cf...bd98206c` | yes |

Both sides on the provider, same redundancy level, same index and error
document. 53 chunks against 19 for the same tar stored as a blob, which is the
manifest path doing work.

Scoped to one node deliberately. A manifest is not a pure function of the bytes
the way a blob reference is: for a tar the entry content type comes from
`mime.TypeByExtension`, whose table Go reads from files on the host. A cross-host
run is a separate question and is not claimed here.

### 2 and 3. The holder serves the root and every path

Six of six, every SHA-256 matching the local original, including the bare root
resolving through the index document.

| Path | HTTP | Bytes | SHA matches original |
|---|---|---|---|
| `/` | 200 | 95 | yes, the index document |
| `/index.html` | 200 | 95 | yes |
| `/css/style.css` | 200 | 44 | yes |
| `/a/b/note.txt` | 200 | 29 | yes |
| `/img/pixel.bin` | 200 | 40,960 | yes |
| `/404.html` | 200 | 9 | yes |

### 4. A second node with no hint

404 for the root and for an inner path, in all three runs, at 5.64, 1.80 and
1.80 seconds.

**This arm failed on the first attempt for a reason that is the spec's, not the
node's**, and that is the first of the two spec defects below.

### 4b. A second node that names the holder

**Inner paths: 12 of 12**, every one 200 with a matching SHA-256 and the right
byte count.

**The bare root: 8 of 10, intermittent.** Two hinted requests for the root
returned 404 where the others returned 200 with the index document. Six
further root-only runs were 200 without exception.

Both failures were at first contact with the provider, and the earlier of them
ran with the provider **not connected** to the requester, read back as zero at
the start of that sequence. `Wasp-Providers` connects in the background, and a
preferred candidate is filtered to connected peers, so a first request can be
made before the provider is usable. That is the likeliest cause. It is
**correlational and not established**: it would take a run that disconnects the
provider deliberately and then makes one hinted request to settle it.

Credit refusals do occur on this path and do not prevent the root from serving:
two of the six root-only runs raised `preferred_overdrafts` by 12 and by 3 and
both returned 200, because a refusal falls through to ordinary selection.

### 5. Does the count cover the manifest

| Run | Control drift | Reported | `TotalChunks` rise | `SharedSlots` | `ReferenceCount` rise |
|---|---|---|---|---|---|
| 1 | 0 | 206 | 203 | +3 | **206** |
| 2 | 0 | 206 | 203 | 0 | **206** |
| 3 | 0 | 206 | 203 | 0 | **206** |

**The reported count is exact against `ReferenceCount`, in all three runs.** It
is short by 3 against `TotalChunks`, and that shortfall is not a miscount. Two
diagnostics locate it:

| Kind | Reported | `TotalChunks` rise | `ReferenceCount` rise | Shortfall |
|---|---|---|---|---|
| blob, 225,280 bytes | 65 | 65 | 65 | **0** |
| collection, one small file and an index | 26 | 23 | 26 | **3** |

So the shortfall is a **constant 3 for a collection and 0 for a blob**, the same
3 whether the collection is 26 chunks or 206. Those are manifest chunks the node
already held, and `TotalChunks` cannot rise for a chunk that is already stored.
`SharedSlots` rising by 3 on the first such ingest and by 0 afterwards is those
slots going from one reference to two, and then to three.

That makes the spec's own rule for this arm wrong, which is the second defect
below.

### 6. An over-limit collection

Three qualifying runs, all 507, all leaving `TotalChunks` exactly flat.

| Run | Control drift | HTTP | `TotalChunks` rise after | Qualifies |
|---|---|---|---|---|
| 1 | 0 | **201** | 5,832 | no, it fit |
| 2 | 0 | 507 | +2 | yes |
| 3 | 22 | 507 | 0 | no, control not flat |
| 4 | 0 | 507 | 0 | yes |
| 5 | 0 | 507 | 0 | yes |
| 6 | 1 | 507 | 0 | no, control not flat |
| 7 | 0 | 507 | 0 | yes |

The 507 body carries both figures: `{"message":"local ingest limit reached",
"held":125685,"limit":131072}`.

**Run 1 is reported rather than dropped.** A 20 MB archive cost 5,832 chunks
against 11,228 of headroom, so it was accepted and tested a large ingest instead
of the limit. Sizing an archive to cross a limit needs the headroom read first,
and that run is what then gave arm 6 a limit to bind against. Run 2's `+2` is the
only nonzero rise and sits against a flat control window; runs 4, 5 and 7 are all
exactly 0, so it reads as background rather than residue.

## The two spec defects

### Arm 5 compared against the wrong counter, and would have rejected correct code

The merged spec makes Accept condition 5 "the reported chunk count equals the
`TotalChunks` rise", and rejects a difference in either direction beyond the
control drift. **On a collection that can never hold.** Every unencrypted
manifest shares a small number of canonical node chunks with every other one, so
a node that has ingested anything before already holds them, and `TotalChunks`
does not rise for a chunk that is already stored. The shortfall here is a
constant 3.

`ReferenceCount` is the invariant the arm was reaching for: it rose by exactly
the reported count in all five measurements, collections and blob alike. The spec
is corrected to compare against it, with the `TotalChunks` rise and
`SharedSlots` kept as the evidence that explains any difference.

The spec's reject clause also said a run whose `SharedSlots` or `ReferenceCount`
moved should be discarded as content the node already held. `ReferenceCount`
moves on **every** ingest, by one per chunk stored, so that clause would have
discarded every run ever made. Corrected to `SharedSlots` alone.

### Arm 1 destroys arm 4's precondition

Arm 1 requires a **stamped** upload of the same archive. The roots are identical,
which is arm 1's own result. So that upload publishes the content to the network
under the exact reference arm 4 needs to be unreachable.

The first attempt at arm 4 returned 200 for the root and the inner path in all
three runs, on content that was supposed to be reachable only by naming the
holder. Nothing was wrong with the node. Rerun on an archive that was ingested
and never stamped, it returns 404 in all three runs.

The spec now says arms 4 and 4b take an archive that has never been stamped, and
the invalidation list says that reusing arm 1's archive invalidates them.

## A harness fault worth recording

A first pass at arm 4b wrote every path to one output file and printed no size.
So `/blob.bin` reported the SHA of the previously fetched file and looked exactly
like a manifest serving the wrong content for a path. Fetched on its own it is
16,384 bytes with the correct SHA, and with one output file per path all twelve
fetches are correct.

It is recorded because it produced plausible output rather than an obvious
failure, which is the kind that gets believed. Printing the byte count alongside
the hash is what would have caught it immediately, and the arm now does.

## What this does not show

- **Nothing about a cross-host manifest root.** Both sides of arm 1 ran on one
  node. The entry content type for a tar comes from the host MIME table, so two
  nodes on different distributions could produce different roots for the same
  archive. That is the open question the spec records and neither claims nor
  tests.
- **Nothing about multipart.** Every arm used a tar. The two readers derive paths
  and content types differently, so a multipart collection is a separate case.
  The unit tests cover it; the bench does not.
- **Nothing about encrypted collections**, which cannot have a stable reference
  by construction and so have no equivalence to test.
- **Nothing about the bare-root intermittency's cause**, only its rate and a
  correlation with the provider not yet being connected.
- **Nothing about a directory large enough to cross a trie shape boundary.** The
  largest collection measured is 5,832 chunks and the sites are small.
- **Nothing about disk actually consumed**, as distinct from chunks counted. The
  usage figure counts distinct chunks and is an upper bound on new disk.

---

Generated with help of AI.
