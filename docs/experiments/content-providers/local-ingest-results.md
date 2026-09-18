# Hosting content without a stamp: results

Issue: [#326](https://github.com/crtahlin/wasp/issues/326). Spec:
[local-ingest.md](local-ingest.md). Implementation merged as `35144644`, whose
second parent `f005605d` is the build the bench ran.

Measured 2026-09-18 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester. Harnesses `cp290/t9.sh`, `t9b.sh`, `t9c.sh` and
`t9f.sh`, outside this repository.

**Terms.** The **lookahead buffer** is the prefetch the download path reads
ahead with, set by `Swarm-Lookahead-Buffer-Size` and held at 0 throughout, so
each read fetches one unit rather than many in flight. A **settle gate** is the
check that a node has rebuilt its peer set after a restart before it is
measured.

**The feature is accepted.** Content ingested with no postage is addressed
identically to content that was paid for, is unreachable without naming the
holder, and is retrieved from the holder complete and SHA-256 verified with no
difference detectable at this bench's resolution against a matched control.

## What was measured

Sizes are 4,194,304 payload bytes of random data, 1,033 chunks at redundancy
level NONE, except the limit arm in section 4, which uses 67,108,864 bytes.

### 1. Address equivalence

Two separate files, each ingested with no postage and then uploaded with a stamp
through `POST /bytes` at the same redundancy level.

| File | Ingested reference | Stamped reference | Equal |
|---|---|---|---|
| 1 | `181f39f4f86bea3e...2561d026` | `181f39f4f86bea3e...2561d026` | yes |
| 2 | `48c801870a9b20ba...53baf579` | `48c801870a9b20ba...53baf579` | yes |

Both ingests reported 1,033 chunks, the figure the spec derived from the chunk
arithmetic rather than one chosen to fit.

**This arm deviates from the pre-registered plan and the deviation is not
harmless.** The spec said to upload the stamped copy **on another node**; both
ran on the provider. So the stamped upload wrote chunks the node already held,
and the comparison shows that one node's two write paths agree rather than that
two nodes agree. The Acceptance sentence does not require a second node, so the
condition is met as written, but the stronger claim was not tested. Repeating it
across two nodes is cheap and should be done.

One run each. This is a deterministic hash comparison, and rule 7 is about
quantities with variance.

### 2. Unreachable without a hint

A second node with no hint asked for freshly ingested content, before any hinted
download of it had run.

| Content | Status | Time | Source |
|---|---|---|---|
| level NONE, file A | 404 | 3.45 s | `t9b`, first session |
| level NONE, file B | 404 | 3.54 s | `t9b`, second session |
| MEDIUM | 404 | 3.52 s | `t9` |

Three attempts on three different files across two harness runs, not one arm of
three runs. They are listed separately because they are not repeats of one
measurement.

This arm is why the feature is worth having as a test bed. Every earlier
sole-source measurement here waited a day for a postage batch to expire and then
argued that relay caches had dropped the content. Nothing was ever pushed to a
neighbourhood, so the property holds by construction. It still lasts only until
the first retrieval, because every forwarding peer caches what it relays.

### 3. Retrieval from the holder

**The first attempt at this arm was not a controlled comparison. It is reported
rather than replaced, and its result is not used.** It ran at the route default
of MEDIUM redundancy, 1,120 chunks, with the lookahead buffer at 0:

| Run | HTTP | Bytes returned | Wanted | SHA matched | curl exit |
|---|---|---|---|---|---|
| 1 | 200 | 0 | 4,194,304 | no | 18 |
| 2 | 200 | 32,768 | 4,194,304 | no | 18 |
| 3 | 200 | 32,768 | 4,194,304 | no | 18 |

Every one answered **HTTP 200** while returning a truncated body, which is the
signature this project has recorded before: a 200 is never proof of health.
Runs 2 and 3 stopped after one 32,768-byte unit; run 1 returned nothing at all.

So the arm was rerun **matched**: freshly ingested content at level NONE against
`content B-0`, also level NONE and 1,033 chunks, pinned on the provider days
earlier. The two were interleaved within one session, ingested always first so
any warming favoured the control rather than the claim.

| Run | Ingested, no postage | Control, pinned earlier |
|---|---|---|
| 1 | 4,194,304 B, 15.90 s, 263,751 B/s | 4,194,304 B, 16.01 s, 261,929 B/s |
| 2 | 4,194,304 B, 15.97 s, 262,680 B/s | 4,194,304 B, 15.99 s, 262,297 B/s |
| 3 | 4,194,304 B, 15.96 s, 262,743 B/s | 4,194,304 B, 15.95 s, 262,991 B/s |

Median 262,743 B/s ingested against 262,297 B/s control, a difference of 0.17%
in the ingested arm's favour. Ranges 262,680 to 263,751 and 261,929 to 262,991,
which overlap. Every run returned the whole file with a matching SHA-256 and
curl exit 0, at HTTP 200. Time to first byte was 0.215 s to 0.217 s across all
six.

**The right claim is "no difference detectable here", not "the same speed".**
All six runs sit within 15.90 s to 16.01 s, a spread under 0.7%, which suggests
the rate is set by something outside the storage path. A bench this tight can
only fail to find a difference.

**Both arms were genuinely served by the provider.** Every run in both arms
raised the requester's `preferred_attempts` by exactly 1,155 and its
`preferred_hits` by 1,153. The control was not being quietly served by the
network instead.

**What the control is, precisely.** `content B-0` was uploaded with a stamp days
earlier, but that batch has since expired, so at measurement time both arms were
unstamped pinned collections on the provider. This arm therefore shows that the
ingest path produces content served exactly as the pinning path serves it. It
does **not** compare against content with live postage, and the distinction
matters because the retrieval path does not consult postage at all. It is still
the comparison #326 needs, since the question is whether unpaid content is
served normally.

#### What the rerun cannot settle

**An earlier draft of this document said the truncation "belongs to the
redundancy level, not to local ingest". That was not established and is
withdrawn.** Two things differed between the failed arm and the matched rerun,
and the rerun changed both:

- the redundancy level, MEDIUM against NONE;
- the provider's accounting state. The failed arm ran twenty minutes after a
  restart, and the requester's `accounting_blocks_count` rose by **201, 211 and
  41** across its three runs. In the matched session the same counter rose by
  **4, then 2, then not at all** for the remaining eight runs.

No arm ran MEDIUM content under the settled conditions, so nothing separates the
two candidates. Writing it as settled would repeat exactly the error withdrawn
from [#324](https://github.com/crtahlin/wasp/issues/324).

One detail points away from a simple credit story and is recorded for whoever
picks this up: in the failed arm `preferred_overdrafts` stayed at **0** while
`preferred_attempts` rose by only 11, 12 and 12 per run, against 1,155 in the
good runs. The requester was not refused by the provider; it stopped asking.

**What is established** is the narrower negative, and it is enough for this
issue: **the truncation is not a property of locally ingested content**, because
ingested content retrieved completely in three runs of three under the same
conditions as the control. Separating the level from the accounting state is
[#327](https://github.com/crtahlin/wasp/issues/327) territory and needs its own
matched arm.

### 4. The limit

Provider configured with `local-ingest-limit: 8192`, holding 4,309 chunks. An
ingest of 67,108,864 bytes:

```
507  {"message":"local ingest limit reached","held":4309,"limit":8192}
```

Usage before 4,309, usage after 4,309.

**This exercised the declared-length refusal, not the enforcement.** `curl`
sends `Content-Length`, so the handler refused before reading the body, which
the spec calls a convenience rather than the enforcement. Nothing was written,
so usage being unchanged is trivially true here and proves no cleanup. The
mid-stream refusal, where the limit is crossed part way through a body and a
partial collection has to be removed, is measured separately below. What this
arm does show is that the refusal names both the limit and what is held, so an
operator can act on it.

### 5. Removal

Unpinning an ingested reference through the existing `DELETE /pins/{reference}`:

| Check | Result |
|---|---|
| Listed as pinned before | 200 |
| Delete | 200 |
| Listed after | 404 |
| Usage before | 4,309 chunks |
| Usage after | 3,189 chunks |
| Dropped | 1,120, the exact chunk count that ingest reported |

The 1,120 is the MEDIUM-redundancy ingest from section 3, which is why it is not
1,033.

A later reading of `bee_localstore_local_ingest_chunks` gave 5,255, which is
3,189 plus 1,033 twice, matching the two level-NONE ingests exactly. **That
reading was taken by hand and is not in either raw file**, because `t9b.sh`
never records the usage metric. It is reported for what it is worth and should
not be treated as part of the record; the harness should capture it.

## Against the acceptance conditions

The spec said accept if the reference from an unencrypted local ingest equals
the reference from `POST /bytes` of the same bytes at the same redundancy level,
**and** a fresh ingest is unreachable without a hint, **and** it is retrievable
with one over three runs with a matching SHA-256.

All three hold, with the same-node caveat on the first recorded in section 1.

Of the reject conditions, two are positively excluded: the addresses did not
differ, and no no-hint control returned 200. **The third, that an abandoned
ingest must not leave chunks the usage figure does not count, was not tested on
the bench at all.** No arm ends a body early or disconnects mid-upload. It is
covered by tests in `package storer_test`, and an earlier draft of this document
wrongly listed it among the conditions the bench had cleared.

## Three of the gaps, closed

Measured the same night, once [#341](https://github.com/crtahlin/wasp/issues/341)
had listed them. Harnesses `cp290/t9c.sh` and `t9f.sh`. Same provider,
`local-ingest-limit: 8192`.

These three needed no change to the node's configuration. The fourth gap, a
second node producing the same reference, still stands.

### Mid-stream refusal fires on a node

The limit arm in section 4 fired the declared-length refusal, because `curl`
sends `Content-Length`. Sending the body with **chunked transfer encoding**
removes it, so that branch cannot fire and the limit has to bind while the body
is being read.

Two runs, 19,152,896 bytes against 2,676 chunks of room:

```
507  {"message":"local ingest limit reached","held":5516,"limit":8192}
```

**The status alone does not say which branch produced it**, and an earlier draft
of this section claimed the mid-stream path on that evidence alone. Two things
settle it, and the second is the stronger:

- The branches log different lines. Both runs logged
  `local ingest refused, limit reached`, and neither logged
  `local ingest refused, declared length does not fit under the limit`.
- **A paired control already existed.** An earlier attempt sent its body the
  same way, from standard input, but **without** the chunked header, and was
  refused on declared length with `wanted=8257`, which is exactly
  33,554,432 / 4096 leaves plus its intermediates. Same body source, same
  endpoint, header the only difference, different branch. That is what
  establishes the header's effect, rather than any claim about what `curl` does
  internally.

Because this branch fires inside the pipeline, it is also the first run outside
a unit test of the out-of-band limit report that exists because
[#337](https://github.com/crtahlin/wasp/issues/337) discards the error chain.

### Neither refusal nor abandonment leaves chunks behind

This is the arm the spec cared about most: on a node that is not restarted, a
leaked partial ingest is the one failure no limit would catch, because only the
startup pass removes it.

**The local ingest usage figure cannot answer this**, and two earlier attempts
at this arm failed in different ways before that was clear:

- The first posted a body larger than the room left, so the node refused it on
  declared length and nothing was abandoned at all. It asserted on nothing.
- The second genuinely abandoned an ingest, but asserted on the usage figure.
  That figure is the committed total, and a leak is by definition chunks it does
  not count, so a flat reading is equally consistent with cleanup working and
  with a leak. The same objection applies to the refusal path, where the claim
  is released whether or not anything was deleted.

The quantity that can answer it is `ChunkStore.TotalChunks` from `/debugstore`,
which counts what is on disk. Each window is paired with a control window of the
same length and no ingest, because the node is syncing and that counter can move
on its own.

| Window | What the ingest did | Control drift | Measured drift |
|---|---|---|---|
| abandoned 1 | 2,097,024 B uploaded, then cut off | 0 | 0 |
| abandoned 2 | 2,097,024 B uploaded, then cut off | 0 | 0 |
| abandoned 3 | 2,097,024 B uploaded, then cut off | 0 | 0 |
| refusal 1 | wrote its room, then 507 | 0 | 0 |
| refusal 2 | wrote its room, then 507 | 0 | 0 |

An abandoned 2 MiB ingest at level NONE writes about 512 chunks, and a refusal
at this limit writes about 2,676 before the limit bites. **A leak would show as
those numbers. Every window shows zero**, and `Pinning.TotalChunks` stayed at
539,290 throughout, so no partial collection was committed either.

Each window carries its own evidence rather than borrowing another's. Every
abandoned run recorded 2,097,024 bytes uploaded and curl exit 28, every refusal
recorded its 507, and the node's log for this run alone holds exactly three
`local ingest: split write all failed` entries and two
`local ingest refused, limit reached` entries, one per window in order.

**Fresh random bytes every run**, which is what keeps the measurement from being
blind: a re-ingest of content the node already held would raise `SharedSlots`
and `ReferenceCount` rather than `TotalChunks`.

An earlier pass of the abandoned arm saw drifts of 0, +5 and 0 against a control
of 0, +1 and 0, on a node whose counter was moving slightly at the time. It
pointed the same way and is superseded by the run above, where the counter was
steady and every window was flat.

### An encrypted ingest round-trips

| Property | Value |
|---|---|
| Reference length | 128 hex characters, so 64 bytes |
| Chunks reported | 261, and usage rose by exactly 261 |
| Retrieved | 1,048,576 bytes in 4.10 s, from the other node, naming the holder |
| SHA-256 | matched |

The 64-byte reference is the case the record was deliberately sized for, working
on a node rather than in a test. Address equality is not testable for encrypted
content at all, because a fresh random key per chunk gives a different reference
for identical bytes. Such a reference cannot be announced through
`/wasp/providers`, which refuses encrypted references, so it is reachable only
by explicit hint.

This arm also settles the by-hand usage reading section 5 disowns: a later
refusal reported `held=5255`, matching it.

### What these cost in method

Three attempts across one night went wrong, and each produced a
confident-looking pass:

- a refusal reported as an abandonment, when the node had rejected the body on
  its declared length before anything was written;
- a leak measured with the counter that by definition cannot see it, on both the
  abandoned path and the refusal path;
- an arm that recorded no evidence of its own, whose write-up then borrowed log
  lines belonging to different runs.

The discriminators that resolved all three were already in the code and the node
rather than in the harness: two distinct log lines for the two refusal branches,
and a chunk counter separate from the usage figure. Worth building into the next
harness rather than reaching for afterwards.

## Two nodes produce the same reference

Arm 1 of [#341](https://github.com/crtahlin/wasp/issues/341), the last of the
four, measured 2026-09-18. Harness `t17.sh`, outside this repository; rows in
`t17-two-node-reference.txt`.

**What #326 actually showed** was that one node's two write paths agree: both
the ingest and the stamped upload ran on the provider. The claim the feature
rests on is stronger, that the reference is a function of the bytes, so two
independent nodes must produce the same one for the same input. That had never
been run.

**Method.** Each 4,194,304-byte file is generated once locally, copied to both
nodes, and its SHA-256 read back from each node and compared with the local one
**before** anything is ingested. A run where the SHAs differ proves nothing and
the harness refuses it. Both ingests send `Swarm-Redundancy-Level: 0`
explicitly, because the level changes the reference for the same bytes.

The requester does not ship with local ingest on, so it was enabled for the run
and restored afterwards, verified by the endpoint answering 403 again.

**Result: equal in all three runs**, at 1,033 chunks on both nodes each time.

| Run | Reference, both nodes | Chunks |
|---|---|---|
| 1 | `9be81f2d1c639d80014a563aa74981de14dff4c03b9e2b77ca821bd2c763480e` | 1,033 |
| 2 | `7050edb276cd73ec797216852b2764148eff27c5c4bd692842eda72e2136f325` | 1,033 |
| 3 | `126a12639081a106a88c09c935e90d92cbb6404b813a1e0a655401b7159d224a` | 1,033 |

The prediction was registered before the run: the references are byte-for-byte
equal, because content addressing is a function of the bytes and the redundancy
level. A difference would have meant something node-specific was reaching the
reference, which would have been a more important finding than the one sought.

**What this does not show.** Three files of one size at one redundancy level,
on two nodes of the same build. It does not test a wasp node against a stock Bee
node, nor a size that crosses a different trie shape, nor encrypted content,
where a fresh random key per chunk gives different references for identical
bytes by design and address equality cannot be tested at all.

## What this does not show

- **Nothing about MEDIUM redundancy**, and nothing about why it truncated.
- ~~Nothing about a second node producing the same reference~~. Closed above: two nodes produce the same reference for the same bytes, three runs.
- **Nothing about mid-stream refusal** on a real node in the arms above, now
  covered separately.
- **Nothing about abandoned ingests** in the arms above, now covered
  separately.
- **Nothing about encrypted address equality**, which is not testable at all:
  a fresh random key per chunk gives different references for identical
  bytes. Encrypted retrieval is covered separately.
- **Nothing about directories**, which this endpoint does not handle.
- **Nothing about disk actually consumed.** The usage figure counts distinct
  chunks and is an upper bound: a chunk the node already held cost no new disk
  and is still counted.
- **One file size for every arm but the limit**, which used 67,108,864 bytes.

## Three harness faults worth recording

All three produced plausible output rather than an obvious failure, which is the
kind that gets believed.

- The settle gate counted peers by looking for `overlay` in `/peers`, where the
  field is called `address`. It read zero peers for fifteen minutes while the
  node held 121. A gate that cannot see peers looks exactly like a node that has
  none.
- The row lookup used `grep -P`, which BSD grep on macOS does not have. It
  matched nothing and stopped a run after the ingest had already happened.
- `t9b.sh` records no usage metric, which is why the figure in section 5 had to
  be taken by hand and cannot be checked against the raw rows.

---

Generated with help of AI.
