# Hosting content without a stamp: results

Issue: [#326](https://github.com/crtahlin/wasp/issues/326). Spec:
[local-ingest.md](local-ingest.md). Implementation merged as `35144644`.

Measured 2026-09-18 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester, provider running `0.1.3-f005605d`, requester
running an earlier fork build. Harness `cp290/t9.sh` and `cp290/t9b.sh`,
outside this repository.

**The feature is accepted.** Content ingested with no postage is addressed
identically to content paid for, is unreachable without naming the holder, and
is retrieved from the holder **complete and SHA-256 verified at the same speed
as content that was paid for**, measured against a matched control in the same
session.

## What was measured

All sizes are 4,194,304 payload bytes of random data, which is 1,033 chunks at
redundancy level NONE.

### 1. Address equivalence

Two separate files, each ingested with no postage and then uploaded with a
stamp through `POST /bytes` on the same node at the same redundancy level.

| File | Ingested reference | Stamped reference | Equal |
|---|---|---|---|
| 1 | `181f39f4f86bea3e...2561d026` | `181f39f4f86bea3e...2561d026` | yes |
| 2 | `48c801870a9b20ba...53baf579` | `48c801870a9b20ba...53baf579` | yes |

Both ingests reported 1,033 chunks, which is the figure the spec predicted from
the chunk arithmetic rather than one chosen to fit.

One run each, deliberately. This is a deterministic hash comparison, and rule 7
is about quantities with variance.

### 2. Unreachable without a hint

A second node with no hint asked for freshly ingested content, before any hinted
download had run.

| Attempt | Status | Time |
|---|---|---|
| 1 | 404 | 3.45 s |
| 2 | 404 | 3.54 s |

This arm is the reason the feature is worth having as a test bed. Every earlier
sole-source measurement in this project waited a day for a postage batch to
expire and then argued that relay caches had dropped the content. Here nothing
was ever pushed to a neighbourhood, so the property holds by construction.

It still lasts only until the first retrieval, because every forwarding peer
caches what it relays.

### 3. Retrieval from the holder

**The first attempt at this arm was not a controlled comparison, and its result
is reported here rather than quietly replaced.** It used the route default of
MEDIUM redundancy, while the only previous full retrieval on this bench used
content at level NONE. Three runs, lookahead buffer 0:

| Run | Bytes returned | Wanted | SHA matched |
|---|---|---|---|
| 1 | 0 | 4,194,304 | no |
| 2 | 32,768 | 4,194,304 | no |
| 3 | 32,768 | 4,194,304 | no |

Truncation, at exactly one 32,768-byte unit. Attributing that to local ingest
would have been wrong, because two things differed from the run it was being
compared with: the redundancy level, and a provider that had been restarted
twenty minutes earlier, which resets accounting with every peer.

So the arm was rerun **matched**: freshly ingested content at level NONE against
`content B-0`, which is level NONE, 1,033 chunks, paid for and pinned on the
provider days earlier, and previously retrieved complete. The two were
interleaved in one session so that any drift in node state fell on both.

| Run | Ingested, level NONE | Control, level NONE, paid for |
|---|---|---|
| 1 | 4,194,304 B, 15.90 s, 263,751 B/s | 4,194,304 B, 16.01 s, 261,929 B/s |
| 2 | 4,194,304 B, 15.97 s, 262,680 B/s | 4,194,304 B, 15.99 s, 262,297 B/s |
| 3 | 4,194,304 B, 15.96 s, 262,743 B/s | 4,194,304 B, 15.95 s, 262,991 B/s |

Median 262,743 B/s ingested against 262,297 B/s paid for, a difference of 0.17%.
Ranges 262,680 to 263,751 and 261,929 to 262,991, which overlap. Every run
returned the whole file with a matching SHA-256 and a curl exit code of 0. Time
to first byte was 0.215 s to 0.217 s across all six.

An earlier session, cut short by a problem on the measuring machine rather than
on the bench, produced two more pairs in the same shape: 261,513 and 262,460 B/s
ingested against 260,075 and 262,162 B/s paid for. They are reported because
they exist, not as independent confirmation.

**So the truncation belongs to the redundancy level, not to local ingest.**
Chasing it is [#327](https://github.com/crtahlin/wasp/issues/327) and is not
resolved by this work. What this arm establishes is narrower and is the thing
the issue asked: content stored without paying is served exactly as well as
content that was paid for.

### 4. The limit

Provider configured with `local-ingest-limit: 8192`, holding 4,309 chunks. An
ingest of 67,108,864 bytes, whose declared length alone needs more than the
remaining room:

```
507  {"message":"local ingest limit reached","held":4309,"limit":8192}
```

Usage before 4,309, usage after 4,309. The refusal left nothing behind, which is
the assertion the spec named, and the response says both the limit and what is
held so an operator can act on it.

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

The usage figure is exact across the whole session. After two further level-NONE
ingests it read 5,255, which is 3,189 plus 1,033 twice.

## Against the acceptance conditions

The spec said accept if the reference from an unencrypted local ingest equals
the reference from `POST /bytes` of the same bytes at the same redundancy level,
**and** a fresh ingest is unreachable without a hint, **and** it is retrievable
with one over three runs with a matching SHA-256.

All three hold. None of the reject conditions fired: the addresses did not
differ, the no-hint control did not return 200, and no abandoned ingest left
chunks the usage figure does not count.

## What this does not show

- **Nothing about MEDIUM redundancy**, beyond that it truncates for a reason
  that predates this feature.
- **Nothing about encrypted ingests**, which cannot be tested for address
  equality at all, because a fresh random key per chunk makes two encryptions of
  identical bytes give different references.
- **Nothing about directories**, which this endpoint does not handle.
- **Nothing about disk actually consumed.** The usage figure counts distinct
  chunks and is an upper bound: a chunk the node already held cost no new disk
  and is still counted.
- **One size only.** Every figure here is for 4,194,304 payload bytes.

## Two harness faults worth recording

Both produced plausible output rather than an obvious failure, which is the kind
that gets believed.

- The settle gate counted peers by looking for `overlay` in `/peers`, where the
  field is called `address`. It read zero peers for fifteen minutes while the
  node held 121. A gate that cannot see peers looks exactly like a node that has
  none.
- The row lookup used `grep -P`, which BSD grep on macOS does not have. It
  matched nothing and stopped the run after the ingest had already happened.

---

Generated with help of AI.
