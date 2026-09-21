# Provider retention: results

Measured 2026-09-21 on the two-node bench, requester `bench-2` and provider
`bench-1`, round-trip time about 30 ms. Spec:
[provider-retention.md](provider-retention.md). Issue:
[#392](https://github.com/crtahlin/wasp/issues/392).

**Headline: the change does what it was specified to do, and it is not what
decides whether a large sole-source download completes.** Arms 2, 3 and 4 pass.
Arm 1 fails its acceptance condition on the changed build and on the control
alike, and the reason is settlement rather than retention. That is the outcome
the spec pre-registered as the negative, with one correction: the spec named
`joiner.ReadAt` as the next suspect, and the read unit being all or nothing is
indeed how the failure becomes a truncation, but it is not the cause. The cause
is upstream of it. The requester spends its whole credit with the provider in
**under one second**, issues one cheque, and then neither settlement path moves
the debt back under the limit for about **thirty seconds**, during which
nothing is outstanding and nothing arrives. The download that results is not
slow, it is stopped.

## What was measured, and on which build

Two builds, staged together on the requester so the arms could alternate inside
one session per rule 7:

| Name | Version | What it is |
|---|---|---|
| fix | `0.1.3-392-0e866541` | candidate retention bounded by elapsed time, the retention clock kept at the first refusal, and the error budget spent only when no candidate is left |
| control | `0.1.3-359-54c926a5` | the build before that change |

The provider ran `0.1.3-340-d372367f` throughout and was not changed, because
every part of this change is requester-side.

**The measured fix build is `0e866541`, not the branch tip.** Two later commits
are on the branch: one removes a constant and a map that were written and never
read, which cannot change behaviour, and one makes a retained candidate yield
its place among the candidates. The second is a behaviour change, but it is
inert for every run here: it only takes effect with more than one provider in
the hint, and every run below names exactly one. No run was repeated on the
tip, so that claim rests on reading the code rather than on a measurement.

Every run was gated on three things, each of which has produced believable
wrong numbers on this bench when it was missing: the installed build read back
from the version string rather than assumed, the provider present in `/peers`,
and the chequebook able to issue a cheque (`cp290/liquidity-gate.sh`).

## Arm 2: content the network holds, no hint

The reject criterion. Sixteen mebibytes uploaded from the provider with
postage, fetched by the requester with no hint, three runs per build,
interleaved.

| Build | Runs (MB/s) | Median |
|---|---|---|
| fix | 1.55, 4.20, 0.99 | 1.55 |
| control | 1.88, 0.86, 2.32 | 1.88 |

All six complete with matching checksums. The spreads overlap almost entirely,
so there is no regression to detect and none is claimed in either direction;
the spread within each build is wider than the gap between them. **This is the
arm the withdrawn `fix/392-wait-for-credit` design failed**, at a recorded 2 to
3 times slower, so passing it is the point of this arm rather than a formality.

## Arm 3: a reference nothing holds

Six runs per condition, alternating builds.

| Condition | Time to 404 |
|---|---|
| no hint | 2.22 to 2.45 s |
| hint naming a peer that does not have it | 2.70 to 4.15 s |

Every run returned 404. Nothing approaches the 30 second retention window, so a
hopeless search still ends quickly and retention does not extend it. The
hinted case is about 1 to 1.7 s slower than the unhinted one, which is the cost
of asking the provider first and is unchanged between builds.

## Arm 4: content the provider does not hold

The same reference as arm 2, fetched with a hint naming the provider. Most of
those chunks fall outside the provider's neighbourhood, so it answers that it
does not hold them.

| Build | Runs |
|---|---|
| fix | 3.46 MB/s, not recorded, 2.51 MB/s |
| control | 3.41 MB/s, 3.13 MB/s, truncated at 13,107,200 of 16,777,216 |

One fix-build row was lost to a failed capture rather than a failed download,
and is reported as missing rather than guessed at. The one control truncation
is discussed under the limiter below.

**This arm measures existing behaviour.** The spec attached it to a third
change, carrying the provider's own "I do not hold it" back recoverably, which
is not implemented because the behaviour it asks for already holds: a provider
that answers at all has had its candidate consumed before the answer arrives.
The arm is kept as a guard that the change did not break it, and on that it
passes.

### A limiter worth recording

The provider reports `bee_retrieval_local_only_limited` at 116 over the
session, against `bee_retrieval_local_only_misses` at 897. A node refuses a
local-only request outright once misses exceed 100 per second per peer or 1000
per second in total, because a miss earns it nothing. Only arm 4 can reach
that limiter, since it is the only arm asking the provider for chunks it does
not hold, and the one control truncation in this arm is the likeliest
consequence. **This is not caused by #392 and not fixed by it**, and it
deserves its own issue: a hinted download of content the provider holds only
part of can lose chunks to a rate limit on the provider, and nothing at the
requester distinguishes that from a miss.

## Arm 1: 50 MB, sole source, hinted

The arm this experiment exists for, and the one that fails.

| Build | Run 1 | Run 2 | Run 3 |
|---|---|---|---|
| fix | truncated at 524,288 | complete, 54.8 s | truncated at 0 |
| control | truncated at 524,288 | truncated at 524,288 | truncated at 524,288 |

The acceptance condition was all three runs delivering 52,428,800 bytes with a
matching checksum. **The fix delivers one of three and the control none of
three, so the arm fails.** One in three against none in three is not evidence
of an improvement: with three runs per build it is one run of difference, and
the same build produced both outcomes in later runs.

Five of the seven failures stopped at exactly 524,288 bytes, which is 128
chunks, and every failure ended between 30 and 37 seconds.

## What actually decides arm 1

Same build, same size, same script, alternating a run made immediately after
restarting the requester against one made straight afterwards with nothing
changed in between:

| Round | State | Result | Time | Cheques issued | Provider hits |
|---|---|---|---|---|---|
| 1 | cold | complete | 21.0 s, 2.50 MB/s | 57 | 15,516 |
| 1 | warm | complete | 11.7 s, 4.49 MB/s | 55 | 15,566 |
| 2 | cold | truncated at 524,288 | 36.8 s | **1** | 470 |
| 2 | warm | complete | 27.5 s, 1.91 MB/s | 52 | 15,494 |
| 3 | cold | truncated at 524,288 | 36.9 s | **1** | 428 |
| 3 | warm | complete | 13.9 s, 3.78 MB/s | 57 | 15,467 |

Cold completes one of three; warm completes three of three. **The number of
cheques issued during the download separates the two outcomes completely, and
nothing else does.** Every completing run issued 52 to 57 cheques. Every
truncating run issued one. The refusal counts do not separate them at all: one
completing run took 42,988 refusals and finished in 27.5 s, while a truncating
run took 191. An isolated diagnostic run of the same
arm, with counters read before and after, gives the same signature: one cheque,
464 provider hits of 475 attempts, 315 refusals, 732 chunk flights in 38.5
seconds for a file of 13,890 chunks.

### The download does not run slowly, it stops

A first reading of those numbers said the download runs at the pseudosettle
time allowance for its whole length: 470 chunks in 36.8 seconds is 12.8 a
second, and 4,500,000 units a second at about 307,000 a chunk is 14.7 a second.
**That reading is withdrawn. It was an average over a wall clock that is almost
entirely idle, and the agreement was a coincidence.**

Sampling the requester's accounting for the provider every 500 ms through one
such download, with the balance, the reserved balance, the shadow reserve and
the cheque count read together:

| Time | Balance | Reserved | Shadow reserve | Cheques |
|---|---|---|---|---|
| 0.03 s | 0 | 0 | 0 | 0 |
| 0.56 s | -112,410,000 | 0 | 31,890,000 | 0 |
| 1.10 s | -80,520,000 | 0 | 0 | 1 |
| 1.6 s to 30.4 s | **-80,520,000, unchanged** | 0 | 0 | 1 |
| 30.9 s | -610,000 | 0 | 0 | 1 |
| 37.3 s | download returns 524,288 bytes | | | |

The balance changed three times in thirty-seven seconds. **The whole download
happens inside the first second**: debt reaches 112,410,000 by 560 ms, which at
about 307,000 a chunk is roughly 366 chunks, and with the 31,890,000 then held
in the shadow reserve accounts for the 470 the counters report. One cheque
clears exactly that shadow reserve at 1.1 seconds. Then **nothing moves for
about thirty seconds**: no cheque, no fall in the debt, and a reserved balance
of zero throughout, so nothing is even outstanding against the provider. At
30.9 seconds the debt clears in one step of 79,910,000 with the cheque count
still at one, so that step is a refreshment rather than a cheque, and by then
the read has already been abandoned.

So the failure is a **stall**, not a slow rate. The requester spends its credit
in under a second, and then neither settlement path moves the debt back under
the limit for thirty seconds. Because `joiner.ReadAt` reads a whole unit or
none of it, what the caller sees at the end is a truncation.

The same sampling on a download that **completes**, taken minutes later on the
same pair with nothing else changed, shows what the working case looks like:

| Time | Balance | Reserved | Cheques |
|---|---|---|---|
| 0.03 s | -24,350,000 | 950,000 | 1 |
| 0.57 s | -106,700,000 | 19,260,000 | 1 |
| 1.11 s | -130,440,000 | 0 | 2 |
| 1.65 s | -44,390,000 | 99,430,000 | 4 |
| 2.18 s | -2,150,000 | 320,000 | 6 |
| 3.25 s | -34,100,000 | 2,880,000 | 8 |
| 6.48 s | -35,620,000 | 610,000 | 18 |
| 20.47 s | -76,800,000 | 13,600,000 | 60 |

Sixty cheques in 20.5 seconds, between two and three a second throughout, with
the debt oscillating between about 2 million and 157 million and the reserved
balance rarely zero, which is chunks continuously in flight. The two runs are
not a fast one and a slow one. One settles continuously and the other settles
once and then stops.

So the failure is settlement starvation. The provider is healthy throughout:
it served 464 of the 475 attempts it was actually asked, a 97.7 per cent hit
rate, and the hint reached 724 of 732 flights. Retention is working as
specified in exactly the runs that fail: 315 refusals, 315 re-admissions, no
candidate dropped. **Retention keeps the provider available and cannot make the
money arrive**, which is the limit of what this change was ever able to do.

### Two explanations ruled out

**Accumulated debt is not the variable.** A separate run read the balance with
the provider immediately before a cold download that then truncated. It was
**zero**. The requester began that download owing the provider nothing, and
still spent it at the time-allowance rate. The same run ended at
`-24,280,000`, which is well inside the announced threshold, so the download
was not stopped by hitting a limit either.

**The requester's liquidity is not the variable.** The chequebook held about
0.70 BZZ available throughout, the gate passed before every run, and
`bee_accounting_payment_error_count` stayed at zero. No cheque failed. The
question is why none was attempted after the first.

### What is not yet known

Why nothing moves for thirty seconds is not established. The stall is the
thing to explain, and the cheque count is a symptom of it rather than its
cause.

Thirty seconds is also `RetrieveChunkTimeout`, which bounds one peer attempt,
and it is `providerCreditWait`, the retention window this change introduces.
**The window is not the cause**: the control build, which has no such window,
stalled for the same length in the same way, ending at 30.1, 35.7 and 36.5
seconds. Both numbers being about thirty is noted so that the coincidence is
not mistaken for a finding in either direction.

**One suspect is refuted by the sampling and is withdrawn.** The amount a
cheque may pay is capped at `debt - refreshDue - shadowReservedBalance`, and
`shadowReservedBalance` holds the reserved price of every chunk in flight, so a
download with real concurrency looked able to suppress its own cheques by
carrying a large reserve. The measured reserve is **zero for the entire
thirty-second stall**. Whatever declines the settlement, it is not the shadow
reserve.

`paymentOngoing`, which permits one cheque in flight per peer, survives as a
suspect for why no second cheque is issued, but it does not explain the whole
stall, because a refreshment needs no cheque and none happened either until the
single step at 30.9 seconds.

The remaining arithmetic worth checking against the code is
[#316](https://github.com/crtahlin/wasp/issues/316), which reports that
`refreshDue` in `settle` is computed **without** the one second cap that the
equivalent term in `PrepareCredit` has. Read directly: `settle` uses
`(now - refreshTimestampMilliseconds) / 1000 * refreshRate` uncapped, and
`refreshTimestampMilliseconds` is written in one place only, when a refreshment
completes. A term that grows without bound as the last refreshment recedes is
the right shape for a settlement that declines for longer the longer it has
already been declining, which is what the flat thirty seconds looks like. This
is read from the code and consistent with the measurement, which is not the
same as having watched the branch decline.

**Neither has been observed directly, and the attempt to observe them failed.**
Raising `node/accounting` and `node/settlement` to their highest verbosity
produced no settlement lines at all in the captured window, only unrelated
`node/pricing` warnings, and the capture window itself was wrong: the harness
passed a UTC timestamp to `journalctl --since`, which reads it as local time,
so the window began an hour early and the run's own lines were buried. Both
are fixed for the next attempt and neither result is used above.

This belongs in its own issue. It is not something retention can address, and
it is the thing that actually decides whether a large sole-source download
completes.

The gap also means an earlier claim needs narrowing. Funding the requester's
chequebook on 2026-09-21 did take the same download from 17m29s to 16.8 s, and
that remains true and remains the larger effect. But a funded chequebook is
necessary and not sufficient: every run above had one, the liquidity gate
passed before each, and `bee_accounting_payment_error_count` stayed at zero
throughout.

## Harness faults found on the way

Four, each caught by a gate that did not exist beforehand, and each would have
produced believable numbers.

- **A stale version reading.** The old process keeps answering on the API port
  for several seconds after `systemctl restart`, so waiting for a non-empty
  version returns the build that is being replaced. The runner now waits for
  the version to match what was asked for. As first written it reported the
  outgoing build and refused a valid control round, which is the harmless
  direction; the other direction attributes a run to the wrong build.
- **An upload that returned 201 with the network holding part of the content.**
  A direct upload was used so the content would be network-held; the no-hint
  download of it then truncated at 13.6 of 16.8 MB, which reads exactly like a
  retrieval regression. Arm 2 now uses one reference for the whole session,
  uploaded deferred, waited on until every chunk is pushed, and proved to
  download complete with no hint before anything is measured against it.
- **A postage batch that could not be used.** The batch was immutable and every
  bucket was full, so uploads returned 402 and the harness sat in a ten minute
  sync poll that hid the cause. Replaced with a mutable batch, which reuses a
  full bucket instead of refusing. The runner now stops on a non-201 upload.
- **A failed login read as a counter reading.** The post-download metric
  capture failed to authenticate and the file held an SSH error, which the
  differencing step turned into "no counter moved at all". Taken at face value
  that says the hint was ignored, which is the opposite of what the counters
  say when they are actually read.
- **A log window an hour wide and in the wrong place.** The settlement
  diagnostic passed a UTC timestamp to `journalctl --since`, which interprets
  it in the machine's local time. The window opened an hour before the run and
  the output was dominated by unrelated warnings from before it started.

## What this means for the change

The change is sound, does what the spec says, and passes the arm that rejects
it. It should ship on that basis. It should not be described as fixing large
sole-source downloads, because the measurement says it does not, and the reason
is now known well enough to say what would.

The next work is settlement, not retrieval: establish why a cheque is not
followed by another one inside a download, and measure the per-second budget
against it. Steps 3, 4 and 6 of the bandwidth plan all address that budget and
are unaffected by this result, except that step 5's conclusion needs the
narrowing above.
