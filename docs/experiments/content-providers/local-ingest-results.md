# Hosting content without a stamp: results

Issue: [#326](https://github.com/crtahlin/wasp/issues/326). Spec:
[local-ingest.md](local-ingest.md). Implementation merged as `35144644`, whose
second parent `f005605d` is the build the bench ran.

Measured 2026-09-18 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester. Harnesses `cp290/t9.sh` and `cp290/t9b.sh`, outside
this repository.

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
partial collection has to be removed, is covered by unit tests only and has not
been measured on a node. What this arm does show is that the refusal names both
the limit and what is held, so an operator can act on it.

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

## What this does not show

- **Nothing about MEDIUM redundancy**, and nothing about why it truncated.
- **Nothing about a second node** producing the same reference, per section 1.
- **Nothing about mid-stream refusal** on a real node, per section 4.
- **Nothing about abandoned ingests** on a real node, above.
- **Nothing about encrypted ingests**, which cannot be tested for address
  equality at all: a fresh random key per chunk gives different references for
  identical bytes.
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
