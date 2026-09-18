# Sole-source retrieval at four file sizes

No issue of its own. This is the sole-source size sweep that
[#326](https://github.com/crtahlin/wasp/issues/326) made possible, and it feeds
[#343](https://github.com/crtahlin/wasp/issues/343). The branch name carries a
local task number that is not a GitHub issue; the repository's issue 24 is a
different experiment.

Measured 2026-09-18 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester. Harness `cp290/t11.sh`, with the accounting figures
read separately by `cp290/threshold.sh`, both outside this repository.

**Terms.** An **arm** is one condition measured repeatedly. **Headroom** is the
distance between the requester's debt to a peer and the point at which that peer
refuses further requests.

## What is measured

Four files, ingested with no postage so they are sole-source by construction,
retrieved with the lookahead buffer at 0. Every no-hint control returned 404.

| Size | Run | Balance at start | Delivered | Time | Rate | SHA | Overdrafts |
|---|---|---|---|---|---|---|---|
| 10 MB | 1 | 0 | 10,000,000 | 37.99 s | 263,236 B/s | ok | 2 |
| 10 MB | 2 | -3,400,000 | 10,000,000 | 37.91 s | 263,780 B/s | ok | 0 |
| 10 MB | 3 | -6,160,000 | 10,000,000 | 37.92 s | 263,706 B/s | ok | 0 |
| 20 MB | 1 | -14,590,000 | 20,000,000 | 75.50 s | 264,897 B/s | ok | 0 |
| 20 MB | 2 | -14,950,000 | 20,000,000 | 75.61 s | 264,501 B/s | ok | 0 |
| 20 MB | 3 | -15,610,000 | 20,000,000 | 75.59 s | 264,599 B/s | ok | 0 |
| 50 MB | 1 | -10,450,000 | 30,638,080 | 116.58 s | 262,805 B/s | no | 32 |
| 50 MB | **3** | -91,910,000 | 1,343,488 | 8.02 s | 167,598 B/s | no | 45 |
| 100 MB | 1 | -85,800,000 | 1,441,792 | 12.07 s | 119,486 B/s | no | 67 |
| 100 MB | 2 | -90,560,000 | 1,114,112 | 8.96 s | 124,379 B/s | no | 49 |
| 100 MB | 3 | -91,070,000 | 1,146,880 | 8.90 s | 128,841 B/s | no | 32 |

Three things hold:

- **Sole-source retrieval completes at 10 MB and 20 MB, six runs of six**, and
  truncates at 50 MB and 100 MB, five of five.
- **The rate of a completing run does not depend on size.** All six sit between
  263,236 and 264,897 B/s. The truncating runs are slower at 119,486 to 262,805
  B/s, so the earlier summary of this document, which said a steady 263,000 to
  265,000 B/s throughout, was wrong and is corrected here.
- **Overdrafts separate the two groups**: 0 or 2 in every completing run, 32 to
  67 in every truncating one.

Ingest took 0.22 s, 0.36 s, 0.91 s and 3.74 s, at 2,463, 4,923, 12,305 and
24,609 chunks. **Those durations are one run each**, and the quarantined first
run of the same harness ingested the same sizes in 0.46 s, 0.97 s and 1.84 s, so
the 100 MB figure differs by a factor of two between the two observations. The
chunk counts agree exactly across both.

### The run that is missing

The 50 MB arm has runs **1 and 3**. Run 2 is absent, and the rows are labelled
as they were recorded rather than renumbered.

**Why is not known.** An earlier draft of this document said the previous run had
driven the balance to a ceiling and the harness stopped. That was invented: the
harness runs `for r in 1 2 3` unconditionally and has no balance logic of any
kind, and the 100 MB arm then ran all three of its runs from balances nearer the
supposed ceiling. The likely cause is the silent failure of an `ssh` invocation,
which the harness header already records as having eaten an entire size in the
first run, but that is inference from the 39 s gap between the two timestamps
and not evidence.

## What is not established

**The balance is associated with the outcome. It has not been shown to cause
it, and the simplest version of that claim is false.**

An earlier draft of this document said downloads stop because the balance
reaches the payment threshold the provider announces, gave that threshold as
94,500,000, and called the first 50 MB run decisive. Four things are wrong with
it.

### The gate is not the number used, nor the quantity plotted

`pkg/accounting/accounting.go:325-335` compares against
`paymentThreshold + refreshDue`, which is exactly how `CurrentThresholdReceived`
is computed (`:764-765`). The gate is therefore **99,000,000**, not the
94,500,000 that `ThresholdReceived` reports. The comparison is also against
`increasedExpectedDebt`, which adds the reserved balance, any surplus and the
chunk price, not the settled balance that `/balances` returns and that this
table plots.

### The threshold is not a constant, and was read once, afterwards

`notifyPaymentThresholdUpgrade` (`accounting.go:640-678`) raises the announced
threshold by one refresh rate each time cumulative settlement passes a
checkpoint of 450,000,000. 94,500,000 is the default 13,500,000 plus eighteen
such steps. This session delivered about 126 MB, roughly 9.5 billion units at
the measured chunk price, which is about twenty-one checkpoints' worth, and the
bench notes record this same pair reading 13,500,000 three days earlier. So the
threshold was very likely climbing across these eleven runs, and the single
reading taken after all of them cannot be treated as the value they were
measured against.

Note also that `maxPaymentThreshold`, 108,000,000, is the largest threshold a
node accepts for **its own configuration** (`pkg/node/node.go:245`, `:810-811`).
It does not cap growth, and an earlier draft implied it did.

### An existing dataset falsifies the simple claim

`cp290/t7-cold.txt` is the only other bench data carrying balances. It has runs
starting at a balance of **zero**, the maximum possible headroom, that delivered
262,144 bytes and then nothing at all, five times. "A download completes if it
finishes before the balance climbs to the threshold" cannot survive those rows.
The project already has a competing account of them, the all or nothing
behaviour of `joiner.ReadAt` over one read unit, recorded with
[#324](https://github.com/crtahlin/wasp/issues/324).

### The decisive run is not decisive

Net balance movement, from this table's own numbers:

| Run | Delivered | Time | Net balance change |
|---|---|---|---|
| 20 MB run 1 | 20,000,000 | 75.50 s | -360,000 |
| 20 MB run 2 | 20,000,000 | 75.61 s | -660,000 |
| 20 MB run 3 | 20,000,000 | 75.59 s | **+5,160,000**, debt fell |
| 50 MB run 1 | 30,638,080 | 116.58 s | **-81,460,000** |

Delivery rates differ by under 0.7%, so debt was being **accrued** at the same
rate in all four. Yet the net movement differs by two orders of magnitude, and
in one 20 MB run it ran the other way. At the drift rate of the 20 MB runs,
reaching -81,460,000 would take hours rather than 116 seconds.

So duration is not what separates that run. What separates it is that
**settlement fell short of accrual**, by roughly 3.5% on these numbers, where in
the 20 MB runs it kept up or exceeded it. The controlling quantity is the
settlement shortfall, whose sign varies from run to run in this very table, and
**this harness did not measure it at all**. The conclusion that size is not the
variable and duration is does not follow, and it rested on one run.

### Two smaller corrections

The five truncated runs end at -91,910,000, **-85,800,000**, -90,560,000,
-91,070,000 and -90,220,000. An earlier draft listed four of these and omitted
the one furthest from the supposed ceiling. And those ending values are sampled
after the request returns, with a metrics scrape in between, so they are not the
balance at the moment of truncation: the 50 MB run 3 row shows debt **falling**
by 6,110,000 across its own run.

The short deliveries of the saturated runs were also attributed to the refresh
allowance. The arithmetic does not support it: 1,343,488 bytes is about 328
chunks, roughly 100 million units, against a refresh allowance near 36 million
over that run. The bench notes already record that this pair is cheque-dominated
rather than refresh-bound.

## What this does show, and what to do next

The size sweep itself stands: **sole-source retrieval works at 10 MB and 20 MB
and fails at 50 MB and 100 MB on this bench, in this state**, and the completing
rate is flat across sizes. That was the question this task asked and it is
answered.

The mechanism is not. The candidate worth pursuing is **whether settlement keeps
up with accrual**, not the balance alone:

- Record settlement directly, per run, both pseudosettle and cheques, alongside
  the balance. No harness here does that yet, and it is the quantity the numbers
  above point at.
- Read the announced threshold **before and after every run**, since it grows.
- Re-run the 50 MB arm three times, since the claim that broke rested on one.
- Reconcile with `t7-cold`, where zero balance still failed, rather than around
  it.

**[#327](https://github.com/crtahlin/wasp/issues/327) remains the obvious next
arm** and its motivation is unchanged by all of this: it raises the threshold a
provider announces to a peer downloading its content, which raises whatever
headroom is available, whether or not headroom is the whole story. It is merged
and has never been configured on the bench.

## What this does not show

- **Nothing about the mechanism**, per the section above.
- **Nothing about the settlement rate**, which was not recorded.
- **Nothing at a controlled threshold.** One value, reached by history, read once
  after the fact, and probably moving throughout.
- **Nothing about the baseline rate.** Why buffer 0 runs at 263,000 B/s with
  almost no refusals and a balance far from any limit is untouched here.
- **No second requester or provider**, and eleven runs on one pair in one
  session with the balance carried forward rather than reset.
- **One run per size for the ingest durations**, with a second observation
  elsewhere disagreeing by 2x at 100 MB.
- **The provider build is not in the rows.** The harness reads `/health` to its
  operator log only, and never reads the requester's version. The project's own
  rule, after an earlier mislabelling, is that every row carries the version.

## A harness fault that produced a full set of plausible rows

The first run of this measurement returned nine rows showing 404 at every size
it reached, across three sizes rather than four. They were not retrieval
results: the requester's dial breaker had latched during a provider restart, so
it was never connected to the provider, and every hinted download fell through
to ordinary retrieval and returned the same 404 as the no-hint control.
`preferred_attempts` did not move at all across any of the nine, which is the
tell.

The harness had attempted a reconnect and carried on without checking. The same
failure produced three bad rows once before in this project. The connection is
now a gate that stops the run, and the rows are kept beside the data as
`t11-sizes-INVALID-not-connected.txt`.

That run reached only three sizes because one was lost to the same silent `ssh`
failure that most likely explains the missing 50 MB run above.

---

Generated with help of AI.
