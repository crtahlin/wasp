# Retrying a preferred peer refused credit: results

Spec: [overdraft-retry.md](overdraft-retry.md). Issue:
[#324](https://github.com/crtahlin/wasp/issues/324), which is the cause behind
[#313](https://github.com/crtahlin/wasp/issues/313).

**Summary.** The fix works and it is not sufficient. With the prefetch off, so
one chunk in flight at a time, it turns a truncated sole-source download into a
complete one. At the shipped lookahead buffer the download still truncates on
both builds; on a cold node the fix delivers one read unit in one cycle of three
where stock delivers nothing in three of three, and it consistently asks the
provider more often, 67 to 155 attempts against stock's invariant 59. Content
the network also holds is not slower; the first version of the fix made it about
3x slower and was replaced. The remaining failure is credit refusal itself,
which is [#327](https://github.com/crtahlin/wasp/issues/327).

**An earlier version of this document claimed the fix delivers five times as
many bytes at the shipped buffer. That comparison was not controlled and is
withdrawn**, along with the attribution of the zero-byte first run to the stock
build. Table 2 carries the correction and the matched measurement that replaces
it.

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

## Table 2, withdrawn: it compared conditions, not builds

**This section replaces the shipped-lookahead-buffer comparison that stood here.
The figures in it were real; the comparison was not.**

### What was wrong

The two arms did not start from the same node state.

- The **patched** runs, at 17:21:52, 17:23:24 and 17:24:56, came immediately
  after three 10 MB downloads hinted to the same provider, which ran from
  17:18:28 to 17:21:37. The node entered them carrying debt and settlement
  history with that provider.
- The **stock** runs began at 17:39:30, 120 seconds after a binary swap
  restarted the node, with the balance with the provider at zero and no warm-up
  at all.

So the arms differed in accumulated accounting state as well as in build. Rule 7
says, in as many words, match node state across a comparison. This did not, and
the difference in state turns out to be the larger effect.

### The matched comparison

Restart the node, wait, run once, so the balance with the provider starts at
zero. Three cycles per build, one instance at a time, alternating nothing else:

| Build | Bytes of 4,194,304 | Time | Attempts | Hits | Refused |
|---|---|---|---|---|---|
| stock | 0 | 1.82 s | 59 | 57 | 9 |
| stock | 0 | 1.60 s | 59 | 57 | 9 |
| stock | 0 | 2.47 s | 59 | 57 | 9 |
| the fix | 262,144 | 2.93 s | 155 | 153 | 717 |
| the fix | 0 | 0.88 s | 67 | 65 | 34 |
| the fix | 0 | 0.85 s | 67 | 65 | 35 |

**On a cold node the fix delivers one read unit in one cycle of three, where
stock delivers nothing in three of three.** That is a real difference and a much
smaller one than the withdrawn figure.

**Stock is invariant.** 59 attempts, 57 hits, 9 refusals, zero bytes, three
times across three restarts, and the same three numbers appear in two earlier
sessions on a different harness. The fix never produces 59; it produces 67 or
155. So the mechanism is visible in the attempt counts even in the condition
where the delivery is not.

### What is withdrawn

- **"The fix delivers five times as much", 1,310,720 against 262,144.** A warm
  arm against a cold one.
- **"The mechanism is visible in the attempt counts", 454 against 59.** Same
  defect. Matched, the figures are 67 to 155 against 59.
- **"The first run after a restart delivers nothing at all"** as a statement
  about stock. **It is a property of cold accounting state and happens on both
  builds**, on two of three cycles with the fix in place.

The claim that this reproduces the #313 report still holds, and is strengthened
rather than weakened: 59 attempts, 57 hits and 9 refusals yielding zero bytes is
that report's shape, and it now has an explanation. Nine refusals inside the
first 262,144-byte read unit lose the whole download, because `joiner.ReadAt` is
all or nothing. **What is withdrawn is only the attribution of that outcome to
the stock build.**

### What is not withdrawn

- **The regression test.** It asserts that a preferred peer refused credit is
  asked again for the same chunk with the local-only header, fails on unfixed
  code and passes on fixed code. Deterministic, and owing nothing to bench state.
- **Table 1, the one-chunk-in-flight arm.** Both builds ran the same script with
  the same spacing, each arm beginning with a binary swap and therefore a
  restart, so run 1 is cold and runs 2 to 6 progressively warmer in **both**
  arms. That structure is matched.

### What this changes about the conclusion

Less than it might seem, and it sharpens the next step. The fix removes a
transient refusal turning into a permanent one, and the attempt counts show it
doing exactly that. It does not remove the refusal, and on a cold node, where
there is no settlement history at all, the credit window binds from the first
chunk and the fix has almost nothing to work with.

That is the condition [#327](https://github.com/crtahlin/wasp/issues/327)
addresses, and it gives that work a second pre-registered prediction: a
per-peer threshold should let a **cold** sole-source download deliver something,
where today both builds deliver nothing.

### The rule this adds to the method

**A sole-source row is meaningless without the balance with the provider at the
start of the run.** Every row records it now, before and after. A comparison
whose arms do not start from comparable balances is not a comparison.


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

**And the cold case is the sharper form of the same thing.** A node that has
just connected has settled nothing with the provider, so the window binds from
the first chunk and there is no accumulated headroom for the fix to spend. Both
builds deliver nothing there. That is the condition a per-peer threshold should
relieve most visibly, and #327 carries it as a second prediction.

## Method errors, recorded so they are not repeated

**An uncontrolled comparison, which is the one that reached a merged document.**
The shipped-buffer arms did not start from the same node state: one followed
three 10 MB downloads to the same provider, the other followed a restart. Rule 7
already says to match node state across a comparison, so this was not a gap in
what was written down but in treating it as binding. Every sole-source row now
records the balance with the provider before and after, which makes an unmatched
comparison visible rather than merely possible. Table 2 carries the withdrawal.

**Two instances of the same arm running at once.** A wrapper was piped through
`tail`, which buffers until the pipeline ends, so a running job looked dead and a
second instance was started on top of it. The two restarted the same node against
each other. It was caught because rows arrived 93 s and 75 s apart where the
script sleeps 150 s, and the affected rows are relabelled in the raw data rather
than deleted. Two fixes: the harness now takes a lock and a second instance
refuses to start, and bench wrappers redirect to a file instead of piping through
`tail`, so silence means silence. The bench notes already warned against running
an arm and a binary swap concurrently; this is the same error in a new shape.


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
