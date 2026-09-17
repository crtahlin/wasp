# Retrying a preferred peer refused credit: results

Spec: [overdraft-retry.md](overdraft-retry.md). Issue:
[#324](https://github.com/crtahlin/wasp/issues/324), which is the cause behind
[#313](https://github.com/crtahlin/wasp/issues/313).

**Summary.** The fix works and it is not sufficient. With the prefetch off, so
one chunk in flight at a time, it turns a truncated sole-source download into a
complete one. At the shipped lookahead buffer the download still truncates, but
it delivers five times as many bytes as stock, 31.2% of the file against 6.2%,
and against 0% on stock's first attempt after a restart.
Content the network also holds is not slower; the first version of the fix made
it about 3x slower and was replaced. The remaining failure is credit refusal
itself, which is [#327](https://github.com/crtahlin/wasp/issues/327).

## Setup

Following [test-bench.md](../../agent-playbooks/test-bench.md). Requester Q and
provider P, both on mainnet, roles only (rule 10).

- **Content B**, the sole-source case: 4,194,304 bytes, 1,033 chunks. Its
  postage batch expired days ago, so the network answers 404 for it without a
  hint. P holds every chunk because they are pinned, which needs no stamp. A
  no-hint control was run at the start of each session and returned 404
  throughout.
- **Content A**, the non-regression case: 10,000,000 bytes, batch alive, so the
  network holds it too.
- Every request carried `Swarm-Cache: false` and a `Wasp-Providers` hint naming
  P.
- **Builds.** Stock is `0.1.3-de136880`; the fix is `0.1.3-324v2-dev`. Every row
  records the version the node reported at `/health` at the moment of the run,
  for the reason in Method errors below.
- `blocks` is `bee_accounting_accounting_blocks_count` on Q, the count of
  requests refused for credit. `overdrafts` and `readmits` are the two counters
  this change adds; stock does not have them, so a stock row reads 0 because the
  counter is absent, not because nothing was refused. **`blocks` is the
  observable that works on both builds.**
- `curl` exit 18 means the body was truncated. An HTTP 200 proves nothing here:
  `joiner.ReadAt` is all or nothing and `http.ServeContent` discards the copy
  error, so a truncated download still returns 200.

## Table 1: sole-source, one chunk in flight

`Swarm-Lookahead-Buffer-Size: 0`, which turns the lookahead prefetch off. Runs
back to back with no spacing, because that is what depletes credit with one
peer.

**Stock, 6 runs:**

| Run | Bytes of 4,194,304 | Time | Rate | curl | blocks |
|---|---|---|---|---|---|
| 1 | 4,194,304 | 15.95 s | 262,921 B/s | 0 | 0 |
| 2 | 4,194,304 | 15.89 s | 263,954 B/s | 0 | 0 |
| 3 | 4,194,304 | 15.89 s | 263,928 B/s | 0 | 0 |
| 4 | 2,621,440 | 11.83 s | 221,655 B/s | **18** | **392** |
| 5 | 36 | 2.67 s | 13 B/s | 0 | **1,126** |
| 6 | 4,194,304 | 15.90 s | 263,806 B/s | 0 | 0 |

**The fix, 6 runs:**

| Run | Bytes of 4,194,304 | Time | Rate | curl | blocks | overdrafts | readmits |
|---|---|---|---|---|---|---|---|
| 1 | 4,194,304 | 29.78 s | 140,849 B/s | 0 | **1,395** | 1,395 | 1,395 |
| 2 | 4,194,304 | 15.88 s | 264,085 B/s | 0 | 0 | 0 | 0 |
| 3 | 4,194,304 | 15.89 s | 263,967 B/s | 0 | 0 | 0 | 0 |
| 4 | 4,194,304 | 15.90 s | 263,774 B/s | 0 | 0 | 0 | 0 |
| 5 | 4,194,304 | 15.90 s | 263,795 B/s | 0 | 0 | 0 | 0 |
| 6 | 4,194,304 | 15.88 s | 264,057 B/s | 0 | 0 | 0 | 0 |

**Read it by the `blocks` column, not by the totals.** A run with `blocks` at
zero never met the condition under test and completes on either build, so
averaging all six of each is misleading. Grouped by whether credit was refused
at all:

| | Runs with blocks = 0 | Runs with blocks > 0 |
|---|---|---|
| Stock | 4 of 4 complete | **0 of 2 complete** |
| The fix | 5 of 5 complete | **1 of 1 complete** |

The one run where the defect fired on the fixed build completed, and took 29.78 s
against the 15.9 s of an unrefused run, which is the cost of going back to a peer
whose credit has to clear.

**This pairing is 2 stock runs against 1, and rule 7 asks for three per
condition.** It is reported as a pointer, not as the result. The reason the
condition is so hard to provoke here is that one chunk in flight rarely outruns
the free refresh allowance. Table 2 is the arm where it fires every time.

## Table 2: sole-source at the shipped lookahead buffer

No header overrides beyond the cache and hint headers, which is what a real
client sends. This is the arm the spec's primary test actually describes, and it
is the one that was missing when the fix was first called done.

**Stock.** Two separate sessions, each a fresh start of the node, three runs
each:

| Session | Run | Bytes of 4,194,304 | Share | Time | Rate | First byte | curl | blocks | attempts | hits |
|---|---|---|---|---|---|---|---|---|---|---|
| A | 1 | **0** | 0% | 1.72 s | 0 B/s | 1.723 s | 18 | 9 | 59 | 57 |
| A | 2 | 262,144 | 6.2% | 2.30 s | 114,216 B/s | 0.338 s | 18 | 1,037 | 74 | 72 |
| A | 3 | 262,144 | 6.2% | 2.27 s | 115,407 B/s | 0.339 s | 18 | 595 | 102 | 100 |
| B | 1 | **0** | 0% | 1.59 s | 0 B/s | 1.590 s | 18 | 9 | 59 | 57 |
| B | 2 | 262,144 | 6.2% | 2.58 s | 101,458 B/s | 0.338 s | 18 | 398 | 74 | 72 |
| B | 3 | 262,144 | 6.2% | 2.63 s | 99,752 B/s | 0.338 s | 18 | 204 | 103 | 101 |

**The first run after a restart delivers nothing at all**, and it does so
identically in two independent sessions: 59 preferred attempts, 57 of them hits,
9 requests refused for credit, 0 bytes out, HTTP 200 with `curl` exit 18. The
attempt and hit counts repeat to the digit across both sessions on all three
runs, 59/57, 74/72, then 102/100 and 103/101, so this is a deterministic path
rather than a sampled one.

That is the original #313 report reproduced, and it is now explained rather than
described. #313 recorded "asked the provider for 45 chunks, was served 43, and
abandoned the download with nothing transferred". The same shape appears here
with 59 and 57, and the missing piece is the 9: nine refusals inside the first
read unit are enough to lose the whole download, because each refused chunk is
then sought from peers that do not hold it and the unit is all or nothing. **The
provider served 57 chunks and the requester could use none of them.**

So the zero-byte outcome is not a separate failure from the 6.2% one. It is the
same failure landing before the first read unit completes rather than after it,
and what differs is only whether the node has settled accounting state with the
provider yet.

**The fix, 3 runs:**

| Run | Bytes of 4,194,304 | Share | Time | Rate | First byte | curl | blocks | attempts | hits | overdrafts | readmits |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | 1,310,720 | 31.2% | 1.38 s | 952,710 B/s | 0.278 s | 18 | 360 | 454 | 452 | 360 | 356 |
| 2 | 1,310,720 | 31.2% | 1.42 s | 924,411 B/s | 0.278 s | 18 | 429 | 454 | 452 | 429 | 425 |
| 3 | 1,310,720 | 31.2% | 1.34 s | 976,922 B/s | 0.307 s | 18 | 311 | 455 | 453 | 311 | 308 |

**Both builds truncate, and the fix delivers five times as much**, exactly five
times: 1,310,720 is 5 units of 262,144 against stock's 1, or stock's 0 on a
first run. 262,144 is `smallFileBufferSize` in `pkg/api/bzz.go:51`, the
granularity at which the API reads, so the file arrives in whole units of it or
not at all.

**The mechanism is visible in the attempt counts, which is the point of the
change.** Stock asked the provider 59, 74 and 102 times; the fix asked it 454,
454 and 455 times, between 4.4 and 7.7 times as often, with the hit rate
unchanged at over 99% on both. Stock stops asking the only holder of a chunk
after one refusal. The fix keeps asking, which is the whole of #324, and the
extra bytes are the direct consequence.

The deliveries are also faster per byte, 924,411 to 976,922 B/s against 99,752
to 115,407 B/s, and reach the first byte in 0.278 to 0.307 s against 0.338 s, or
1.59 to 1.72 s on a first run after a restart.

**`blocks` is higher on stock than on the fix in four of six rows**, for example
1,037 against 360, while stock delivers less. That counter covers every
accounting refusal, not only the ones on the preferred path, so a build that
gives up on the provider and turns to ordinary peers is refused by those peers
instead. It is reported because it is the only refusal observable that exists on
both builds, but the preferred-path counters are the ones that speak to this
change, and no conclusion here rests on the `blocks` difference.

## Table 3: non-regression, content the network holds

Content A, 10,000,000 bytes, shipped lookahead buffer, hinted to P.

| Build | Runs | Times | Rates |
|---|---|---|---|
| Stock | 5 | 4.48, 4.85, 5.22, 7.24, 36.76 s | 2,231,337, 2,061,816, 1,917,504, 1,381,560, 272,036 B/s |
| First version, waits on refusal | 3 | 12.09, 15.86, 16.38 s | 826,940, 630,523, 610,340 B/s |
| The fix, tries ordinary peers at once | 3 | 3.84, 4.30, 5.11 s | 2,605,796, 2,325,064, 1,955,240 B/s |

Medians: stock 5.22 s and 1,917,504 B/s; the fix 4.30 s and 2,325,064 B/s.

**The first version failed this clause and was replaced.** Waiting
`overDraftRefresh`, 600 ms, for a refused peer is paid on every chunk, and on
content other peers can serve immediately it bought nothing. The fix keeps the
peer but tries ordinary selection in the same pass. Stock's 36.76 s run is
reported rather than discarded; it is why the comparison is made on the median
with the range beside it.

## Why the shipped buffer still fails

The prefetch puts many chunks in flight at once. Many are therefore refused at
once, and every refused chunk falls through to ordinary selection, which is what
made Table 3 fast. For content only P holds, ordinary selection is where the
chunk is lost: the peers it picks never had it, the chunk spends its 32 origin
retries (`maxOriginErrors`) and returns `storage.ErrNotFound`, which is
indistinguishable from content that does not exist.

So the two behaviours are in tension by construction, and no setting of the
re-admission bound resolves it. Falling through is right when the network holds
the content and wrong when it does not, and the requester cannot tell which case
it is in.

**Removing the permanent drop does not remove the refusal.** That is #327: grant
the requester a large enough credit window at the provider that provider
requests do not get refused at all. The prediction recorded there is that this
arm completes and the overdraft counter falls towards zero.

## Method errors, recorded so they are not repeated

**A control that removed the condition it was meant to test.** The first
comparison spaced its runs 90 s apart, to clear the one-minute `errSkip` list.
That spacing also let debt settle, so `blocks` was zero on every row and the
defect never fired on either build. Both builds returned the complete file and
the comparison proved nothing. A control has to reproduce the condition under
test, and 90 s of quiet is the condition not under test.

**A mislabelled row.** A chained job ran the stock arm after the binary had
already been swapped to the fixed build. It was caught because the row carried
a non-zero `preferred_overdrafts`, a counter stock does not have. The row was
quarantined and the cause fixed by recording the version the node reports at
`/health` in every row, which is what the `ver=` field in the raw data is for.

**A sole-source figure that was a property of the test.** The 264,000 B/s quoted
for content B in earlier notes was taken entirely with
`Swarm-Lookahead-Buffer-Size: 0`, which reads nearly sequentially at a 30 ms
round trip. That bounds throughput for reasons unrelated to credit. Table 2
exists because of this, and it changed the conclusion rather than confirming it.

## Raw data

Per-run rows, with the counters and the node-reported version, are in the bench
harness outside this repository (rule 10).

---

Generated with help of AI.
