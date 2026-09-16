# Content providers: results, phase 1

Issue: [#290](https://github.com/crtahlin/wasp/issues/290). Method:
[measurement.md](measurement.md), merged as `22f8e28f` before the first run.

Status: content A, the lookup cost and an exploratory run are complete. Content B
(availability) is recorded below once its batches have expired.

## Result

**Negative for speed, by the spec's own rule.** In the SWAP block, which decides
(measurement.md, difference 5):
- a hint to the provider (condition 3) gave 5.12 s (4.89-5.54) against 5.46 s
  (4.95-5.68) with no provider known (condition 2);
- discovery (condition 5) gave 6.01 s (5.81-6.37).

The spreads overlap, so there is no gain in time to first byte or throughput, and
the lookup costs more time than it saves on a 16 MiB file. The spec's rule says this
stops work on phase 2 onwards, and keeps the mechanism for availability if content
B works (spec.md, Measurement).

**Why:** in every hinted condition, in both settlement modes, the provider
delivered only 3 to 5% of the file. Per-peer accounting caps how much one peer can
serve one requester in a few seconds; settlement mode does not change that. See
"Why the provider's share is small".

**The exploratory run** shows what would change it. With P at the largest payment threshold
a node accepts, P delivered about 21% of the file, and the hint beat no provider
beyond the spread: 4.86 s against 5.31 s. That run is outside the fixed method.

## Setup

Every table below uses:

- **Build:** wasp `main` at `de136880`.
- **Nodes:** provider P on `bench-1` and requester Q on `bench-2`, both full nodes on
  mainnet. Stock bee v2.8.2 node S on `bench-1`, used in condition 4.
- **Network:** 30 ms added to every packet from Q's machine to P's machine.
- **Content:** fresh 16 MiB random files, three runs per condition.
- **Erasure coding:** none, except conditions 2m and 3m.
- **Figures:** median, with the spread (minimum to maximum) in brackets.
- **Validity:** all 42 runs were valid, and none was repeated.

**Table 1: pseudosettle block** (Q with `swap-enable: false`)

| Condition | Time to first byte, s | Total, s | MB/s | Chunks from provider | Preferred attempts | Misses |
|---|---|---|---|---|---|---|
| 1, providers off | 0.51 (0.40-0.58) | 6.31 (5.60-6.42) | 2.66 (2.61-3.00) | 0 | 0 | 0 |
| 2, no provider known | 0.49 (0.49-0.50) | 7.02 (5.95-7.14) | 2.39 (2.35-2.82) | 0 | 0 | 0 |
| 3, hint to P | 0.37 (0.33-0.49) | 6.48 (6.41-6.56) | 2.59 (2.56-2.62) | 151 (142-169) | 153 (144-171) | 2 |
| 4, hint to stock S | 0.63 (0.58-0.70) | 6.23 (6.10-6.90) | 2.69 (2.43-2.75) | 117 (116-131) | 119 (118-136) | 1 (0-4) |
| 5, discovery | 0.52 (0.46-0.65) | 6.68 (5.85-6.80) | 2.51 (2.47-2.87) | 108 (98-133) | 108 (98-133) | 0 |
| 6, P holds half | 0.49 (0.35-0.63) | 6.55 (5.81-6.55) | 2.56 (2.56-2.89) | 104 (73-116) | 123 (94-132) | 19 (16-21) |

**Table 2: SWAP block** (Q with `swap-enable: true`), the deciding block

| Condition | Time to first byte, s | Total, s | MB/s | Chunks from provider | Preferred attempts | Misses |
|---|---|---|---|---|---|---|
| 1, providers off | 0.41 (0.40-0.52) | 6.32 (6.17-6.43) | 2.65 (2.61-2.72) | 0 | 0 | 0 |
| 2, no provider known | 0.42 (0.35-0.57) | 5.46 (4.95-5.68) | 3.07 (2.95-3.39) | 0 | 0 | 0 |
| 3, hint to P | 0.37 (0.37-0.41) | 5.12 (4.89-5.54) | 3.27 (3.03-3.43) | 163 (160-177) | 165 (162-179) | 2 |
| 4, hint to stock S | 0.59 (0.57-0.61) | 5.54 (5.35-6.52) | 3.03 (2.57-3.14) | 101 (100-145) | 108 (104-152) | 5 (2-6) |
| 5, discovery | 0.51 (0.47-0.54) | 6.01 (5.81-6.37) | 2.79 (2.63-2.89) | 116 (116-145) | 116 (116-145) | 0 |
| 6, P holds half | 0.43 (0.38-0.45) | 5.23 (5.15-5.78) | 3.21 (2.90-3.26) | 117 (86-131) | 136 (106-151) | 20 (19-20) |
| 2m, default level, no hint | 0.46 (0.43-0.47) | 5.60 (5.34-5.84) | 3.00 (2.87-3.14) | 0 | 0 | 0 |
| 3m, default level, hint to P | 0.46 (0.42-0.57) | 6.09 (5.84-6.75) | 2.75 (2.49-2.87) | 177 (176-201) | 178 (178-203) | 0 |

"Chunks from provider" counts Q's `retrieved chunk` log lines naming P, or S in
condition 4, during the download (measurement.md). A download of a file without
erasure coding needs 4,129 chunks, so 163 is 3.9%.

**Table 3: lookup cost** (pseudosettle block, Q with `providers-enable: true`)

| Lookup | Time, s | Requests | Found |
|---|---|---|---|
| Announced file, 3 runs | 1.67 (1.61-1.67) | 9 each | P, each time |
| Reference nobody announced, 3 runs | 1.54 (1.52-1.56) | 8 each | nothing |

The request counts match the design: 8 slot reads, plus 1 record fetch per candidate
found. Q's background request rate, measured over 10 s before each lookup, was zero,
so no correction was needed.

## Why the provider's share is small

The cap is Q's accounting with each peer, `pkg/accounting/accounting.go`:

- **The credit window.** `PrepareCredit` refuses credit once Q's debt to one peer
  would exceed that peer's payment threshold plus at most one second of refresh
  (lines 313-323).
  - With the default threshold of 13,500,000 accounting units, that is 18,000,000.
    Q's own accounting showed exactly that as its current threshold with P.
  - At P's price of about 310,000 per chunk, the window is about 58 chunks.
  - A refused credit skips the preferred attempt, and the chunk goes to normal
    retrieval.
- **The free allowance.** The pseudosettle refresh adds at most 4,500,000 units a
  second, about 14 chunks, once per second (lines 458-466).
- **SWAP adds little in a short download.**
  - A cheque pays only the part of Q's own debt that the refresh has not covered,
    and only one payment is in flight at a time (lines 468-510).
  - Over the whole SWAP block, Q's cheques to P came to about 18,400,000 units,
    roughly 60 chunks, across all hinted runs.
- **The window plus refresh predicts the share.** For a download of 5 to 6 s the
  prediction is about 58 + 5 × 14 = 130 chunks. Measured: 73 to 201.

Normal retrieval spreads a download's debt over about 120 peers, so it never
reaches any one peer's window. A single provider that is meant to serve most of a
file reaches it after the first few dozen chunks.

The largest threshold a node accepts is 24 times the refresh rate, 108,000,000
units (`pkg/node/node.go:241`). Even that is a window of about 360 chunks, under 10%
of a 16 MiB file, unless cheques cycle much faster. The exploratory run below
tests this.

## Other observations

- **Condition 1 against condition 2.**
  - In the SWAP block, condition 2 (setting on, no provider) was faster than
    condition 1 (setting off) beyond the spread: 5.46 s against 6.32 s.
  - In the pseudosettle block the order was the other way round, within the spread.
  - Condition 1 always runs soonest after a restart (measurement.md, Order), so
    this difference cannot be put down to the setting.
- **Condition 6, P holds the first half.**
  - Misses were few, 16 to 21, against 73 to 131 hits.
  - The credit window is used up at the start of a download, and the start of the
    file is the half P holds.
- **Condition 4, stock hint target.** The stock node answered local-only requests
  by forwarding them and was paid, as the spec expects. The download was not
  slower than condition 2.
- **Erasure-coded content (2m, 3m).**
  - A hint still reached 176 to 201 chunks of a default-level file, capped like the
    others. The prefetch drops the preferred set only for the chunks it claims
    first (#299).
  - Downloads of default-level files made 4,750 to 5,073 chunk requests, against
    about 4,129 for the same size without erasure coding.
  - The hinted ones made the most (4,899 to 5,073), consistent with the chunks
    fetched twice (measurement.md, difference 10).
- **Lost attempts** (attempts minus hits minus misses) were 0 to 2 per run. P
  answered almost every attempt it was given.

## Corrections to the method

- **Overdraft skips.** The method estimated them against condition 3 in the SWAP
  block, "where no overdraft is expected". That assumption was wrong: SWAP did not
  remove overdraft. The estimate is therefore not reported.
  - The direct figure is about 4,129 minus the preferred attempts. In condition 3,
    roughly 3,950 to 3,985 chunks per run went to normal retrieval because Q had no
    credit left with P. In condition 5 the same difference, 3,984 to 4,031, also
    includes the chunks fetched before discovery found P.
- **P's sent bytes** in pseudosettle condition 5, round 1 read 15 GB. The first
  counter read failed and was taken as zero, so that figure is not measured. It was
  context only, and does not affect the run.

## What happened during the runs

- **The Mac slept.** The machine that drives the runs over SSH went into idle sleep
  during Q's first settle after enabling providers.
  - The settle took 78 minutes instead of 15. The method sets only a minimum.
  - No download was running at the time. The driver ran under a sleep blocker from
    then on.
- **P's chequebook ran dry.** P was set up with an empty chequebook, to receive
  cheques. Its own uploads of the test files then failed to pay its peers by
  cheque, and it fell back to pseudosettle.
  - It lost no peers and no run was affected.
  - 0.5 xBZZ was deposited into P's chequebook at 17:55 UTC, during the
    pseudosettle block's condition 4.
- **Q's chequebook was topped up** to 0.79 xBZZ before the SWAP block, so it could
  not run out during the block.
- **SSH.** Two SSH connections from the driver failed, and both were retried
  successfully. One upload was repeated because of that.

## Content B, availability

*To be recorded after batches B and B2 have expired (from about 18:35 UTC on
2026-09-16).*

## Exploratory: provider with the maximum payment threshold

**Outside the fixed method.** It was run after the SWAP block to test the explanation
above, and it does not change the result judged by the method's rules.

**What changed:**
- P announced a payment threshold of 108,000,000 units, the largest a node accepts
  (`payment-threshold`, `pkg/node/node.go:241`), instead of the default 13,500,000.
- P was restarted and settled for 15 minutes. Q's accounting then showed P's
  threshold as 108,000,000.
- Everything else was as in the SWAP block, three runs each, alternating.
- P's default threshold was restored afterwards.

**Table 4: SWAP, P at the maximum payment threshold**

| Condition | Time to first byte, s | Total, s | MB/s | Chunks from provider | Preferred attempts | Misses |
|---|---|---|---|---|---|---|
| 2, no provider known | 0.53 (0.49-0.67) | 5.31 (5.30-5.79) | 3.16 (2.90-3.17) | 0 | 0 | 0 |
| 3, hint to P | 0.31 (0.31-0.31) | 4.86 (4.77-4.88) | 3.45 (3.44-3.52) | 869 (787-888) | 871 (789-890) | 2 |

**What it shows:**
- **The explanation holds.** With the larger credit window, P delivered about 21%
  of the file, 5 times more than at the default threshold. That is more than the
  window alone allows, about 430 chunks, because each cheque can also cover more
  once the window is larger.
- **With the window lifted, the hint makes the download faster.** Here condition 3 beats condition 2
  beyond the spread:
  - total time 4.86 s against 5.31 s, about 8% less;
  - time to first byte 0.31 s against 0.53 s, about 40% less.
- **How far this goes is not settled.** Three runs outside the fixed method are a
  pointer for a follow-up, not a result.
- **What the follow-up must state:** a provider that raises its threshold extends
  more unsecured credit to every peer. That is the cost to the provider, and the
  follow-up must say so.

**P's bytes sent** during condition 3 were 3.7 to 4.1 MB, consistent with about 870
chunks of 4 KiB.

### How fast the credit window reopens

One further diagnostic download, hinted to P, with Q's accounting, swap and
pseudosettle loggers at debug. Its timing is not comparable with the tables above,
because the logging itself slows the download.

Measured over one download of 10.6 s:
- **9 cheques sent to P**, one about every 1.2 s;
- **57,790,000 accounting units** cleared by those cheques in total, about
  6,400,000 each, roughly 21 chunks;
- **about 0.3 s** between a cheque being sent and the payment being registered;
- **9 free-allowance refreshes**, one per second, as the code sets.

Two things follow:
- **A cheque clears much less than the window.** The amount paid is the debt minus
  what the refresh is about to cover, and only one payment is in flight at a time
  (`pkg/accounting/accounting.go:468-510`).
- **Each cheque costs three chain calls on the receiving side.** `ReceiveCheque`
  asks the chequebook contract for its issuer, its balance and what it has already
  paid out (`pkg/settlement/swap/chequebook/chequestore.go:162-195`), for every
  cheque, although the issuer cannot change for a given chequebook and the code's
  own comment calls the balance check "not particularly useful". The same code is
  in unmodified upstream bee; filed as
  [#300](https://github.com/crtahlin/wasp/issues/300).

**The 0.3 s is the chain calls.** A single chain call from P's machine to either of
its configured endpoints took 0.10 s (five calls each, 0.100 to 0.117 s, one first
call at 0.194 s). Three calls per cheque is 0.30 s, which is what the cheque cycle
takes.

So the rate at which one provider may serve one requester is about the free
allowance, 4,500,000 units a second, plus what cheques clear, about 5,000,000 to
6,000,000 units a second. At P's price that is roughly 34 chunks a second, which is
what the tables show: 163 chunks in about 5 s.

**Cashing out plays no part in this.** A cheque clears the debt when it is accepted.
Turning cheques into tokens on the chain is separate, and P had cashed nothing
during any of these runs.

Raw data:

| Time (UTC) | Cond | Round | TTFB s | Total s | Q peers | From provider | Attempts | Hits | Misses | Requests |
|---|---|---|---|---|---|---|---|---|---|---|
| 19:58:09 | 2 | 1 | 0.673 | 5.790 | 123 | 0 | 0 | 0 | 0 | 4129 |
| 19:59:23 | 3 | 1 | 0.308 | 4.767 | 123 | 888 | 890 | 888 | 2 | 4132 |
| 20:00:38 | 2 | 2 | 0.525 | 5.305 | 123 | 0 | 0 | 0 | 0 | 4129 |
| 20:01:53 | 3 | 2 | 0.309 | 4.877 | 123 | 869 | 871 | 869 | 2 | 4129 |
| 20:03:08 | 2 | 3 | 0.488 | 5.299 | 123 | 0 | 0 | 0 | 0 | 4129 |
| 20:04:22 | 3 | 3 | 0.312 | 4.858 | 123 | 787 | 789 | 787 | 2 | 4131 |

## Raw data

One row per run, in the order they ran. "From provider" counts log lines naming P,
or S in condition 4. "Requests" is Q's retrieval request count during the download,
which also counts requests Q relayed for others.

| Time (UTC) | Block | Cond | Round | TTFB s | Total s | Q peers | From provider | Attempts | Hits | Misses | Requests |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 15:46:39 | ps | 1 | 1 | 0.511 | 6.309 | 117 | 0 | 0 | 0 | 0 | 4126 |
| 15:47:55 | ps | 1 | 2 | 0.402 | 6.419 | 117 | 0 | 0 | 0 | 0 | 4123 |
| 15:49:10 | ps | 1 | 3 | 0.583 | 5.600 | 117 | 0 | 0 | 0 | 0 | 4128 |
| 17:08:27 | ps | 2 | 1 | 0.486 | 7.016 | 117 | 0 | 0 | 0 | 0 | 4127 |
| 17:09:42 | ps | 3 | 1 | 0.485 | 6.477 | 117 | 151 | 153 | 151 | 2 | 4129 |
| 17:14:01 | ps | 5 | 1 | 0.517 | 6.798 | 117 | 98 | 98 | 98 | 0 | 4134 |
| 17:15:22 | ps | 6 | 1 | 0.353 | 6.553 | 117 | 116 | 132 | 116 | 16 | 4135 |
| 17:16:37 | ps | 3 | 2 | 0.329 | 6.564 | 117 | 142 | 144 | 142 | 2 | 4133 |
| 17:20:57 | ps | 5 | 2 | 0.655 | 6.680 | 117 | 108 | 108 | 108 | 0 | 4136 |
| 17:22:18 | ps | 6 | 2 | 0.486 | 6.549 | 117 | 104 | 123 | 104 | 19 | 4133 |
| 17:23:34 | ps | 2 | 2 | 0.492 | 7.137 | 117 | 0 | 0 | 0 | 0 | 4127 |
| 17:27:51 | ps | 5 | 3 | 0.462 | 5.851 | 117 | 133 | 133 | 133 | 0 | 4131 |
| 17:29:12 | ps | 6 | 3 | 0.635 | 5.808 | 117 | 73 | 94 | 73 | 21 | 4128 |
| 17:30:27 | ps | 2 | 3 | 0.504 | 5.947 | 117 | 0 | 0 | 0 | 0 | 4135 |
| 17:31:42 | ps | 3 | 3 | 0.370 | 6.410 | 117 | 169 | 171 | 169 | 2 | 4137 |
| 17:53:00 | ps | 4 | 1 | 0.633 | 6.900 | 118 | 131 | 136 | 131 | 4 | 4133 |
| 17:54:16 | ps | 4 | 2 | 0.577 | 6.096 | 118 | 116 | 118 | 116 | 1 | 4124 |
| 17:55:31 | ps | 4 | 3 | 0.697 | 6.232 | 118 | 117 | 119 | 117 | 0 | 4131 |
| 18:27:36 | sw | 1 | 1 | 0.400 | 6.321 | 118 | 0 | 0 | 0 | 0 | 4122 |
| 18:28:51 | sw | 1 | 2 | 0.524 | 6.433 | 118 | 0 | 0 | 0 | 0 | 4125 |
| 18:30:06 | sw | 1 | 3 | 0.412 | 6.170 | 118 | 0 | 0 | 0 | 0 | 4126 |
| 18:46:24 | sw | 2 | 1 | 0.419 | 5.682 | 122 | 0 | 0 | 0 | 0 | 4131 |
| 18:47:38 | sw | 3 | 1 | 0.369 | 5.124 | 122 | 163 | 165 | 163 | 2 | 4131 |
| 18:51:54 | sw | 5 | 1 | 0.508 | 6.015 | 122 | 116 | 116 | 116 | 0 | 4134 |
| 18:53:15 | sw | 6 | 1 | 0.455 | 5.785 | 122 | 131 | 151 | 131 | 20 | 4132 |
| 18:54:36 | sw | 3 | 2 | 0.372 | 5.537 | 122 | 177 | 179 | 177 | 2 | 4129 |
| 18:58:53 | sw | 5 | 2 | 0.540 | 6.375 | 122 | 116 | 116 | 116 | 0 | 4130 |
| 19:00:13 | sw | 6 | 2 | 0.432 | 5.235 | 122 | 117 | 136 | 117 | 19 | 4124 |
| 19:01:28 | sw | 2 | 2 | 0.573 | 5.462 | 122 | 0 | 0 | 0 | 0 | 4130 |
| 19:05:44 | sw | 5 | 3 | 0.467 | 5.813 | 122 | 145 | 145 | 145 | 0 | 4130 |
| 19:07:04 | sw | 6 | 3 | 0.380 | 5.146 | 122 | 86 | 106 | 86 | 20 | 4130 |
| 19:08:18 | sw | 2 | 3 | 0.355 | 4.947 | 122 | 0 | 0 | 0 | 0 | 4129 |
| 19:09:32 | sw | 3 | 3 | 0.415 | 4.895 | 122 | 160 | 162 | 160 | 2 | 4136 |
| 19:10:47 | sw | 2m | 1 | 0.466 | 5.341 | 122 | 0 | 0 | 0 | 0 | 4830 |
| 19:12:04 | sw | 3m | 1 | 0.571 | 6.747 | 122 | 201 | 203 | 201 | 0 | 5073 |
| 19:13:19 | sw | 2m | 2 | 0.457 | 5.840 | 122 | 0 | 0 | 0 | 0 | 4750 |
| 19:14:35 | sw | 3m | 2 | 0.462 | 5.841 | 122 | 177 | 178 | 177 | 0 | 4978 |
| 19:15:50 | sw | 2m | 3 | 0.431 | 5.595 | 122 | 0 | 0 | 0 | 0 | 4760 |
| 19:17:05 | sw | 3m | 3 | 0.424 | 6.093 | 122 | 176 | 178 | 176 | 0 | 4899 |
| 19:38:22 | sw | 4 | 1 | 0.586 | 6.517 | 123 | 145 | 152 | 145 | 5 | 4131 |
| 19:39:37 | sw | 4 | 2 | 0.568 | 5.544 | 123 | 100 | 104 | 100 | 2 | 4134 |
| 19:40:52 | sw | 4 | 3 | 0.607 | 5.345 | 123 | 101 | 108 | 101 | 6 | 4132 |

Generated with help of AI.
