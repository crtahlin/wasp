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

**Five of the six arms pass. The sixth, arm 4b, does not meet its condition.**

What passes: a directory ingested with no postage produces the same manifest
root as a stamped upload of the same archive, the holder serves every path in it,
a node with no hint cannot reach it, the reported chunk count is exact, and an
over-limit archive is refused without residue.

What does not: arm 4b asks that a node naming the holder be served every inner
path with a matching hash **in all three runs**. It was not. The one multi-chunk
file in the collection returned HTTP 200 with an **empty body** in 2 of 13
attempts. Every single-chunk file in the same collection, through the same
manifest over the same path, was correct 12 of 12.

That failure is the signature
[#313](https://github.com/crtahlin/wasp/issues/313) reports and
[#343](https://github.com/crtahlin/wasp/issues/343) traced, on a path both issues
leave open: a chunk refused credit is dropped from that chunk's preferred path,
falls to peers that never held it, and `joiner.ReadAt` is all or nothing, so one
lost chunk loses the whole read unit. Nothing measured here puts it in #340's
code, and nothing measured here proves the attribution either. It is stated as
consistent with those issues, not as established. The consequence for this
experiment is that **hosting a multi-chunk file for a remote reader is not yet
shown to work reliably**, whatever the manifest does correctly.

**Two of the spec's own rules were also found wrong**, and both were found only by
running it: one would have rejected correct code, and one made an arm test
something other than what it claimed. They have their own section below, as do
the two faults found in the measuring script, one of which had already reported
arm 4b as a pass.

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

### 4b. A second node that names the holder. Condition not met

Two passes over the same never-stamped collection, three runs each. The second
pass is the one that counts, because it reads `provider_connected` back as 1
before making any request and the first pass did not gate on that at all.

Correct below means all three of: HTTP 200, the exact byte count, and a SHA-256
matching the local original. **Status alone is not correct**, which is the second
harness fault recorded further down.

| Path | Chunks | First pass, ungated | Second pass, gated |
|---|---|---|---|
| `/` bare root, through the index document | 1 | 2 of 3 | 3 of 3 |
| `/css/style.css` | 1 | 3 of 3 | 3 of 3 |
| `/a/b/note.txt` | 1 | 3 of 3 | 3 of 3 |
| `/blob.bin` | **4** | 3 of 3 | **1 of 3** |

The two `/blob.bin` failures were **HTTP 200 with an empty body**: zero bytes
returned for a 16,384-byte file, and the harness first scored them as successes.

Six further runs of `/blob.bin` on its own, with the overdraft counter read
either side of each, were all correct:

| Run | HTTP | Bytes | Seconds | curl exit | SHA | `preferred_overdrafts` |
|---|---|---|---|---|---|---|
| 1 | 200 | 16,384 | 0.248 | 0 | matches | 0 |
| 2 | 200 | 16,384 | 0.248 | 0 | matches | 0 |
| 3 | 200 | 16,384 | 0.247 | 0 | matches | 0 |
| 4 | 200 | 16,384 | 0.411 | 0 | matches | **+16** |
| 5 | 200 | 16,384 | 0.247 | 0 | matches | 0 |
| 6 | 200 | 16,384 | 0.747 | 0 | matches | **+11** |

Two of those six took credit refusals and still returned the whole file, so a
refusal is not sufficient on its own to lose the content.

**Tally for the multi-chunk file: 11 correct of 13.** Both failures fell in one
sequence, as the fourth request of four in rapid succession. But the first pass
fetched it in that same position three times and got it right three times, so
**"fourth in succession" is not the cause and the cause is not established.**
What separates the successful conditions from the failing one is at most timing,
and the evidence does not distinguish timing from chance at this sample size.

**The bare root failed twice in thirteen attempts across every pass**, both times
at first contact with the provider and both times in a sequence with no
connectivity gate. The earlier of the two ran with the provider read back as
**not connected**.

**Those two failures are a different mode and are not pooled with the empty
bodies.** The root failed with **404**, which is the path not resolving. The
multi-chunk file failed with **200 and no bytes**, which is the path resolving
and the content not arriving. Pooling them would hide the one observation this
arm contributes.
`Wasp-Providers` connects in the background and a preferred candidate is filtered
to connected peers, so a first request can be made before the provider is usable.
That is the likeliest cause and it is **correlational, not established**: it
would take a run that disconnects the provider deliberately and then makes one
hinted request to settle it. The gated pass had the root 3 of 3 and six
root-only runs were 6 of 6, two of them with `preferred_overdrafts` rising by 12
and by 3 and both still serving, because a refusal falls through to ordinary
selection.

**Why this arm is reported as not met rather than as a pass with a caveat.** The
spec's Accept condition 4 wants every inner path served correctly in all three
runs, and its catch-all clause rejects anything the accept conditions do not
cover. Two empty bodies in a qualifying, gated pass is a reject under both. The
single-chunk paths passing 12 of 12 does not rescue it, because a website is
made of files of arbitrary size and the only multi-chunk file in the fixture is
the one that failed. Splitting the difference here would be the same mistake as
scoring on the status code.

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

## Two harness faults worth recording

Both were in the measuring script rather than the node, both produced plausible
output rather than an obvious failure, and the second one hid a real result for
two passes.

**One output file for every path, and no size printed.** A first pass at arm 4b
wrote every fetch to the same file, so `/blob.bin` reported the SHA of the
previously fetched file and looked exactly like a manifest serving the wrong
content for a path. Fetched on its own it is 16,384 bytes with the correct SHA,
and with one output file per path all twelve fetches were correct. Printing the
byte count alongside the hash is what would have caught it immediately.

**Scoring on the HTTP status code.** The gated pass at arm 4b printed
`paths_200=4/4` for a run in which `/blob.bin` returned **zero bytes**, because
the script counted statuses. Rule 7 of `AGENTS.md` says in one line that an HTTP
200 from an API endpoint is never proof a node is healthy, and this is that rule
being broken inside the tool written to enforce it. The arm now scores a fetch
correct only on status, byte count and hash together, which is what turned a
reported pass into the reject above.

The general lesson is the same one both times: **a measuring script needs the
same adversarial reading as the code it measures.** A harness that cannot fail
the node cannot validate it either, and here one nearly published an accept for a
condition that was not met.

## What follows from arm 4b

The implementation is not changed and #340 is not reopened. Every arm that tests
#340's own code passes, and the arm that does not fails on the retrieval path,
which this change does not touch: the collection's single-chunk files travel the
same hinted path through the same manifest and never failed.

What arm 4b establishes is narrower and worth saying plainly: **serving a
multi-chunk file from a node that holds it, to a node that names it as the
holder, is not yet reliable.** For hosting a website that is not a detail, since
any image or stylesheet above 4,096 bytes is multi-chunk. The feature is usable
for a holder serving itself, which arms 2 and 3 show without a failure, and
provisional for a remote reader.

The evidence belongs on
[#313](https://github.com/crtahlin/wasp/issues/313) rather than in a new issue,
because #313 is the same observation and is open. What this pass adds to it is
the contrast the earlier reports did not have: **single-chunk content on the same
path, to the same peer, in the same collection, at the same moment, never
failed.** That narrows the candidate causes to ones that need more than one
chunk, which is what #343's reading of `joiner.ReadAt` predicts and what a
connectivity or discovery fault would not.

It also gives #313 a **cheap reproduction**, which every arm on that issue so far
has lacked. A collection holding one four-chunk file fails in roughly one attempt
in six at sub-second latency, and a single-chunk file in the same collection is a
built-in control saying whether a candidate fix broke the path rather than
removed the size dependence. Every previous arm on #313 needed a 4 MiB object and
a download lasting a minute or more, and none ever returned a complete body, so
this is also the first evidence that **small content over that path completes at
all.** Posted there as a comment rather than filed as a new issue.

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
- **Nothing about why a multi-chunk file returns an empty body**, only that it
  did, twice in thirteen attempts over the hinted path, and that the signature
  matches what #313 reports and #343 traced. No run here isolated a cause. The
  two failures and the eleven successes differ in timing and in nothing else the
  harness recorded, and thirteen attempts cannot separate timing from chance.
- **Nothing about where the size threshold for that failure is.** The fixture has
  exactly one multi-chunk file, at four chunks. Whether the rate rises with size,
  and whether a one-chunk file can fail at all, are unmeasured.
- **Nothing about a directory large enough to cross a trie shape boundary.** The
  largest collection measured is 5,832 chunks and the sites are small.
- **Nothing about disk actually consumed**, as distinct from chunks counted. The
  usage figure counts distinct chunks and is an upper bound on new disk.

---

Generated with help of AI.
