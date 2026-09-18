# Sole-source retrieval at four file sizes, and what ends a download

Measured 2026-09-18 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester, provider running `0.1.3-f005605d`. Harness
`cp290/t11.sh`, outside this repository.

**Two results, and the second was not what this set out to measure.**

- Sole-source content retrieves completely at 10 MB and 20 MB, and truncates at
  50 MB and 100 MB, at a steady 263,000 to 265,000 B/s throughout.
- **What ends a download is the requester's unsettled debt reaching the payment
  threshold the provider has announced**, which on this bench has grown to
  94,500,000. That is the mechanism [#343](https://github.com/crtahlin/wasp/issues/343)
  was looking for, and it is not the one an earlier draft of that work guessed.

## What this replaces

This was blocked for a day. The four files were to be uploaded with postage and
measured once the batch expired, which is how every sole-source measurement in
this project had been done. Local ingest
([#326](https://github.com/crtahlin/wasp/issues/326)) removes the wait: content
ingested with no postage was never pushed to a neighbourhood, so it is
sole-source by construction. The four files were made in **5.2 s of ingest
altogether** rather than a day of waiting.

The no-hint control still ran first on each file, because construction is an
argument and a 404 is evidence. All four returned 404.

## Retrieval, lookahead buffer 0

| Size | Run | Balance at start | Delivered | Time | Rate | SHA |
|---|---|---|---|---|---|---|
| 10 MB | 1 | 0 | 10,000,000 | 37.99 s | 263,236 B/s | ok |
| 10 MB | 2 | -3,400,000 | 10,000,000 | 37.91 s | 263,780 B/s | ok |
| 10 MB | 3 | -6,160,000 | 10,000,000 | 37.92 s | 263,706 B/s | ok |
| 20 MB | 1 | -14,590,000 | 20,000,000 | 75.50 s | 264,897 B/s | ok |
| 20 MB | 2 | -14,950,000 | 20,000,000 | 75.61 s | 264,501 B/s | ok |
| 20 MB | 3 | -15,610,000 | 20,000,000 | 75.59 s | 264,599 B/s | ok |
| 50 MB | 1 | -10,450,000 | 30,638,080 | 116.58 s | 262,805 B/s | no |
| 50 MB | 2 | -91,910,000 | 1,343,488 | 8.02 s | 167,598 B/s | no |
| 100 MB | 1 | -85,800,000 | 1,441,792 | 12.07 s | 119,486 B/s | no |
| 100 MB | 2 | -90,560,000 | 1,114,112 | 8.96 s | 124,379 B/s | no |
| 100 MB | 3 | -91,070,000 | 1,146,880 | 8.90 s | 128,841 B/s | no |

Ingest cost 0.22 s, 0.36 s, 0.91 s and 3.74 s for the four sizes, at 2,463,
4,923, 12,305 and 24,609 chunks.

**The rate does not degrade with size.** Every complete run sits between 263,236
and 264,897 B/s, and the 50 MB run that truncated was running at 262,805 B/s
when it stopped. Truncation is a cutoff, not a slowdown.

**Eleven runs, not twelve.** The 50 MB third run is missing: the run before it
had driven the balance to the ceiling, and the harness recorded only two. The
gap is left as it is rather than filled from a later session, which would not be
the same node state.

## What ends a download

The balance column was added to this harness after a review pointed out that the
project requires it and earlier harnesses here did not record it. It is the
column that answers the question.

The requester's accounting entry for the provider, read after the runs:

| Field | Value |
|---|---|
| `thresholdReceived` | 94,500,000 |
| `currentThresholdReceived` | 99,000,000 |
| `thresholdGiven` | 13,500,000 |
| `balance` | -90,220,000 |

**Every truncation happens just under 94,500,000.** The balances at the end of
the truncated runs are -91,910,000, -90,560,000, -91,070,000 and -90,220,000. At
a measured chunk price near 307,000 units, -91,910,000 leaves room for about
eight more chunks.

Neither node sets `payment-threshold`, so both started from the default
13,500,000. The provider's announced threshold has since grown to 94,500,000,
which is 21 times the refresh rate of 4,500,000, with the ceiling at 24 times.
Thresholds grow with settlement history, so this is the ordinary consequence of
these two nodes having traded for days.

### The mechanism

Settlement drains unsettled debt continuously while a download accrues it. So:

**A download completes if it finishes before the balance climbs to the announced
threshold.**

That accounts for every row:

- 10 MB and 20 MB finish with the balance still far below the ceiling.
- **50 MB run 1 is the decisive one.** It started healthy at -10,450,000, at the
  same level as the completing 20 MB runs, ran for 116 s, and stopped when its
  balance reached -91,910,000. Nothing about the file was different; it simply
  ran long enough to exhaust the headroom.
- Runs that started near the ceiling delivered about 1.1 to 1.4 MB, which is
  roughly what the refresh allowance sustains before the gate closes again.

### What this corrects

An earlier draft of the [#343](https://github.com/crtahlin/wasp/issues/343) spec
said credit exhaustion ends these downloads, then withdrew it because the count
of credit refusals did not order the outcomes: in one arm the run with the
fewest refusals truncated earliest.

Both halves were right about something. **Credit is what ends the download, and
the refusal count is not how to see it.** The predictor is the balance against
the announced threshold. A run already near the ceiling refuses early and often
and delivers little; a run with headroom can absorb hundreds of refusals and
still finish, which is exactly what the completing run in that arm did.

The withdrawal stands: the earlier draft proposed waiting for credit inside the
retrieval loop, and nothing here says that would work. What this changes is
where to look.

## What follows

- **[#327](https://github.com/crtahlin/wasp/issues/327) now has a measured
  motivation.** Raising the threshold a provider announces to a peer downloading
  its content raises precisely this ceiling. It is implemented and merged but
  has never been configured on the bench, and this is the arm that would show
  what it buys.
- **The buffer question in #343 needs re-reading against this.** A larger buffer
  consumes credit faster, so it reaches the ceiling sooner while also delivering
  sooner. That is a plausible account of why larger buffers truncate more, and
  it is **not established here**: these runs were all at buffer 0 and varied the
  file size instead.
- **Size is not the variable.** It looked like a size effect and it is a
  duration effect. A 20 MB file that started at -85,000,000 should truncate, and
  a 100 MB file starting at zero on a freshly connected pair may not. Neither
  was run.

## What this does not show

- **Nothing about why buffer 0 is slow.** The rate is 263,000 B/s with zero
  credit refusals in the completing runs. The ceiling explains where downloads
  stop, not why the baseline rate is what it is.
- **Nothing at a different threshold.** One value, 94,500,000, reached by
  history rather than chosen. The relationship between threshold and deliverable
  bytes is one point, not a curve.
- **No second requester or provider**, so nothing about whether the ceiling is
  per-pair in the way this assumes.
- **Eleven runs in one session on one pair of nodes**, with the balance carried
  forward from run to run rather than reset. That is realistic and it is not
  controlled.

## One harness fault, and it produced a full set of plausible rows

The first run of this measurement returned nine rows showing 404 at every size.
They were not retrieval results: the requester's dial breaker had latched during
a provider restart, so it was never connected to the provider, and every hinted
download fell through to ordinary retrieval and returned the same 404 as the
no-hint control. `preferred_attempts` did not move at all, which is the tell.

The harness had tried to reconnect and carried on without checking. **The same
failure produced three bad rows once before in this project.** The connection is
now a gate that stops the run rather than an attempt that precedes it, and the
quarantined rows are kept beside the data as
`t11-sizes-INVALID-not-connected.txt`.

---

Generated with help of AI.
