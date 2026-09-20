# Continuous accrual of the refresh allowance: measured

Issue: [#359](https://github.com/crtahlin/wasp/issues/359).
Spec: [continuous-accrual.md](continuous-accrual.md).
Build: `0.1.3-359-54c926a5` on the requester, provider unchanged.

**The mechanism works exactly as designed, at the predicted values, and the
safety hazard did not occur in fourteen downloads. It delivered no additional
bytes, because the truncation the experiment was designed around no longer
happens on this build.** The acceptance table cannot be applied as written, for
the reason given under "The control condition did not reproduce".

## What ran

Twelve runs, three rounds of two accrual modes at two lookahead sizes,
interleaved per round so that node state drifting over the session could not
separate the arms. Each mode switch restarts the requester, so the requester
was reconnected to the provider and the link verified before every run. Two
further downloads were run afterwards for the mechanism check below, making
fourteen in total.

Content is 4,194,304 bytes of random data, uploaded to the provider at
redundancy level 0, pinned and announced, on a postage batch bought for this
measurement. The provider's credit grant is absent from its configuration, so
it is zero, which the spec requires.

Raw rows are in `cp290/359-results.tsv`, outside the repository per rule 10.

## The safety gate, which overrides everything else

**The provider never blocklisted the requester.** Its `/blocklist` was empty
before and after all fourteen downloads, and
`bee_accounting_disconnects_overdraw_count` stayed at zero throughout.

That is the hazard the cap exists to prevent, and the spec makes it a reject
whatever else happened. It did not occur.

## The mechanism, checked directly

The split the spec calls floor and ceiling refusals **cannot be compared across
the arms**, and reporting it would have been an artifact rather than a result.
It is defined on `refresh_due` being exactly zero, which is one of only two
values the step model can produce and a value the continuous model almost never
produces. The apparent collapse of "floor refusals" from thousands to zero is
therefore true by construction and says nothing.

What is comparable is the **overdraft limit at which refusals actually happen**,
read from the refusal log line. One download per arm, all refusals counted:

| `overdraft_limit` at refusal | step | continuous |
|---|---|---|
| 13,500,000, the bare announced threshold | **461** | **0** |
| 16,874,999, the capped limit | 0 | **420** |
| 18,000,000, threshold plus a full allowance | 988 | 513 |
| 22,500,000, a grown threshold | 985 | 130 |

and the allowance itself:

| `refresh_due` | step | continuous |
|---|---|---|
| 0 | 2,424 | 0 |
| 3,374,999, the cap | 0 | **420** |
| 4,500,000, a full rate | 10 | 688 |
| a continuum: 679,500, 715,500, 720,000, 756,000, … | 0 | present |

Three things are confirmed by this and none of them are inferred:

1. **The step model is two-valued in practice**, not just in the source:
   `refresh_due` is 0 or 4,500,000 and nothing else.
2. **The cap binds at exactly 3,374,999**, giving an overdraft limit of exactly
   **16,874,999**. That is the value the design predicts, and it is strictly
   below the 16,875,000 at which a stock peer disconnects. The minus one is
   doing its job.
3. **Refusals at the bare announced threshold are eliminated**, 461 to 0. This
   is the artifact-free form of the claim the floor and ceiling split was
   reaching for.

## The primary observable

Overdrafts not readmitted, at the lookahead the spec calls the arm under test:

| round | step | continuous |
|---|---|---|
| 1 | 217 | 56 |
| 2 | 150 | 0 |
| 3 | 50 | 0 |

median 150 against 0. At lookahead 0, the no-harm check, it is 0 in five of six
runs and 1 in the sixth, in both arms: no regression, and nothing to improve.

Total refusals at the arm under test fall as well, 6,021 / 5,536 / 2,277 under
step against 3,005 / 1,238 / 3,063 under continuous, but the spread is wide
enough on both sides that the median is the only defensible summary: 5,536
against 3,005.

## What did not move

**Delivered bytes. Every one of the twelve runs completed**: 4,194,304 bytes,
HTTP 200, `curl` exit 0, SHA-256 matching the uploaded file, in both arms at
both lookahead sizes.

Download duration did not separate the arms either: 38.4 / 40.6 / 44.0 seconds
under step against 45.0 / 40.6 / 42.6 under continuous at the arm under test.

## The control condition did not reproduce

The spec's acceptance table is built on a control that truncates. It cites
`retrieval-rate.md:41-46`, where the unmodified node completed **1 of 3** at
this lookahead size, and sets the primary observable's baseline at "about 4 per
truncating run".

Neither holds on this build. The control completed **3 of 3**, and its
overdrafts-not-readmitted were 217, 150 and 50 rather than about 4.

So the table's outcome 1, "goes to 0 and the file completes with a matching
SHA", is satisfied on its letter and means much less than it was written to
mean: the file completes in the control too, so completion does not
discriminate between the arms. **No outcome in the table describes what
happened**, which is that everything the change targets improved while the
user-visible result was already fine.

Why the control no longer truncates is **not established here**. Several
changes have landed since that measurement, including the readmit path in
[#324](https://github.com/crtahlin/wasp/issues/324), the preferred set reaching
erasure-coded downloads in
[#299](https://github.com/crtahlin/wasp/issues/299), and this measurement uses
freshly uploaded content rather than the content that baseline used. Attributing
it to any of those would be the same unsupported causal reading this repository
has had to retract twice already. What is recorded is that the baseline moved.

## Disposition

The measurement is **neutral** on the claim that motivated the issue.

- The defect is real and is now demonstrated on a node rather than argued from
  the source: the allowance is granted as a step, and 461 refusals in a single
  download occurred at the bare announced threshold with the allowance at zero.
- The change removes those refusals entirely and drops overdrafts not readmitted
  to a median of zero, at a limit that is exactly one unit below what a stock
  peer tolerates, without a single blocklisting in fourteen downloads.
- It delivers **no additional bytes** in the condition measured, because that
  condition no longer truncates.

Rule 8 says a dial that turns out not to matter is worse than not shipping one.
This dial demonstrably changes what the node does, and the change is in the
intended direction and within the intended bound. It has not been shown to
change what a user receives. The option therefore stays **off by default and
marked provisional** in `docs/DIFFERENCES.md`, and this document is the record
of why, rather than the option being presented as a settled improvement.

What would settle it is a condition where the control does truncate. Finding one
on this bench is its own piece of work, because the condition that used to
produce truncation no longer does.
