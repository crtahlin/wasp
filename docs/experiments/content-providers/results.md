# Content providers: results, phase 1

Issue: [#290](https://github.com/crtahlin/wasp/issues/290). Method:
[measurement.md](measurement.md), merged as `22f8e28f` before the first run.

Status: complete. Content A, the lookup cost, an exploratory run, a payment
threshold sweep and content B (availability) are all recorded below.

## Result

**Negative for speed, by the spec's own rule.** In the SWAP block, which decides
(measurement.md, difference 5):
- a hint to the provider (condition 3) gave 5.12 s (4.89-5.54) against 5.46 s
  (4.95-5.68) with no provider known (condition 2);
- discovery (condition 5) gave 6.01 s (5.81-6.37).

The spreads overlap, so there is no gain in time to first byte or throughput,
and the lookup costs more time than it saves on a 16 MiB file. The spec's rule
says this stops work on phase 2 onwards, and keeps the mechanism for
availability if content B works (spec.md, Measurement).

**Why:** in every hinted condition, in both settlement modes, the provider
delivered only 3 to 5% of the file. Per-peer accounting caps how much one peer
can serve one requester in a few seconds; settlement mode does not change that.
See "Why the provider's share is small".

**The exploratory run** shows what would change it. With P at the largest
payment threshold a node accepts, P delivered about 21% of the file, and the
hint beat no provider beyond the spread: 4.86 s against 5.31 s. That run is
outside the fixed method.

**Negative for availability too, and more sharply.** The spec's rule kept the
mechanism for availability on the condition that content B worked. It did not.
After content B's postage batch expired the content was gone from the network,
404 in 2.32 to 3.66 seconds over six runs; P still held it complete; discovery
still found P; and **not one of 24 runs retrieved the file**. P served 36 to 57
chunks of the 1,033 in the plain copy, and no body was transferred at all.

**The requester never gives the provider the chance.** A diagnostic run with the
requester's retrieval and accounting loggers at debug shows the provider was
asked for 45 chunks and served 43, while credit does not appear to have been the
limit: the requester's balance with P moved by 40,000 units against a threshold
of 18,000,000, and a cheque settled the debt during the download. The other
requests went to peers that do not have the content, 923 of them failed, and the
download aborted after about 77 chunk requests with no body at all. That is one
diagnostic run with debug logging on, so it is a diagnosis to be confirmed
rather than a measured result, and it leaves 32 of those 77 requests
unexplained. Filed as [#313](https://github.com/crtahlin/wasp/issues/313); see
"Content B, availability".

**What that leaves.** Both reasons to keep phase 1 as it stands have now failed
on the bench, which by the spec's rule stops phase 2. It does not follow that
the direction is worthless: it means the next piece of work is #313, which is
smaller and better defined than anything phase 2 proposed, and without which no
amount of provider speed helps content that only the provider holds.

## Setup

Every table below uses:

- **Build:** wasp `main` at `de136880`.
- **Nodes:** provider P on `bench-1` and requester Q on `bench-2`, both full
  nodes on mainnet. Stock bee v2.8.2 node S on `bench-1`, used in condition 4.
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

The request counts match the design: 8 slot reads, plus 1 record fetch per
candidate found. Q's background request rate, measured over 10 s before each
lookup, was zero, so no correction was needed.

## Why the provider's share is small

The cap is Q's accounting with each peer, `pkg/accounting/accounting.go`:

- **The credit window.** `PrepareCredit` refuses credit once Q's debt to one
  peer would exceed that peer's payment threshold plus at most one second of
  refresh (lines 313-323).
  - With the default threshold of 13,500,000 accounting units, that is
    18,000,000. Q's own accounting showed exactly that as its current threshold
    with P.
  - At P's price of about 310,000 per chunk, the window is about 58 chunks.
  - A refused credit skips the preferred attempt, and the chunk goes to normal
    retrieval.
- **The free allowance.** The pseudosettle refresh adds at most 4,500,000 units
  a second, about 14 chunks, once per second (lines 458-466).
- **SWAP adds little in a short download.**
  - A cheque pays only the part of Q's own debt that the refresh has not
    covered, and only one payment is in flight at a time (lines 468-510).
  - Over the whole SWAP block, Q's cheques to P came to about 18,400,000 units,
    roughly 60 chunks, across all hinted runs.
- **The window plus refresh predicts the share.** For a download of 5 to 6 s the
  prediction is about 58 + 5 x 14 = 130 chunks. Measured: 73 to 201.

Normal retrieval spreads a download's debt over about 120 peers, so it never
reaches any one peer's window. A single provider that is meant to serve most of
a file reaches it after the first few dozen chunks.

The evidence for this being the cap **on content A** is the payment threshold
sweep below: raising the threshold raised the provider's share in proportion,
which a cause other than the credit window would not do. It is not the cap on
content B, where the download ends before the window is reached. Note also that
a refused credit is not counted as a preferred attempt, so the attempt counters
cannot by themselves distinguish "refused credit" from "never asked".

The largest threshold a node accepts is 24 times the refresh rate, 108,000,000
units (`pkg/node/node.go:241`). Even that is a window of about 360 chunks, under
10% of a 16 MiB file, unless cheques cycle much faster. The exploratory run
below tests this.

## Other observations

- **Condition 1 against condition 2.**
  - In the SWAP block, condition 2 (setting on, no provider) was faster than
    condition 1 (setting off) beyond the spread: 5.46 s against 6.32 s.
  - In the pseudosettle block the order was the other way round, within the
    spread.
  - Condition 1 always runs soonest after a restart (measurement.md, Order), so
    this difference cannot be put down to the setting.
- **Condition 6, P holds the first half.**
  - Misses were few, 16 to 21, against 73 to 131 hits.
  - The credit window is used up at the start of a download, and the start of
    the file is the half P holds.
- **Condition 4, stock hint target.** The stock node answered local-only
  requests by forwarding them and was paid, as the spec expects. The download
  was not slower than condition 2.
- **Erasure-coded content (2m, 3m).**
  - A hint still reached 176 to 201 chunks of a default-level file, capped like
    the others. The prefetch drops the preferred set only for the chunks it
    claims first (#299).
  - Downloads of default-level files made 4,750 to 5,073 chunk requests, against
    about 4,129 for the same size without erasure coding.
  - The hinted ones made the most (4,899 to 5,073), consistent with the chunks
    fetched twice (measurement.md, difference 10).
- **Lost attempts** (attempts minus hits minus misses) were 0 to 2 per run. P
  answered almost every attempt it was given.

## Corrections to the method

- **The first reading of the content B failure was wrong.** An earlier draft of
  this document said the per-peer credit window was exhausted and that the
  requester therefore abandoned the provider. A diagnostic run refuted it:
  credit was never exhausted, the provider served 43 of the 45 chunks it was
  asked for, and the download aborted because chunks nobody holds could not be
  found. The counters that separate those two readings, preferred attempts, hits
  and misses, are collected by the harness but were never written to the content
  B results file, which is why a wrong reading survived the first pass. They are
  written for content A, and should be for content B too.
- **A prediction written before the runs was not met, and its mechanism is
  unconfirmed.** The method predicted that with a hint the plain copy would
  arrive, and that the erasure coded copy would fail for a different reason: the
  decoder's prefetch starting from a fresh context and losing the preferred set.
  Both copies failed, and their figures are indistinguishable, so these runs
  neither confirm nor rule out that mechanism for the erasure coded copy.
- **There is no control for content B with providers off and P reachable.** Step
  1 makes P unreachable, and every step 2 run has the feature on. So these runs
  cannot say whether the feature changed the outcome from a clean 404 into a
  request that returns no body at all.
- **Overdraft skips.** The method estimated them against condition 3 in the SWAP
  block, "where no overdraft is expected". That assumption was wrong: SWAP did
  not remove overdraft. The estimate is therefore not reported.
  - The direct figure is about 4,129 minus the preferred attempts. In condition
    3, roughly 3,950 to 3,985 chunks per run went to normal retrieval because Q
    had no credit left with P. In condition 5 the same difference, 3,984 to
    4,031, also includes the chunks fetched before discovery found P.
- **P's sent bytes** in pseudosettle condition 5, round 1 read 15 GB. The first
  counter read failed and was taken as zero, so that figure is not measured. It
  was context only, and does not affect the run.

## What happened during the runs

- **The Mac slept.** The machine that drives the runs over SSH went into idle
  sleep during Q's first settle after enabling providers.
  - The settle took 78 minutes instead of 15. The method sets only a minimum.
  - No download was running at the time. The driver ran under a sleep blocker
    from then on.
- **P's chequebook ran dry.** P was set up with an empty chequebook, to receive
  cheques. Its own uploads of the test files then failed to pay its peers by
  cheque, and it fell back to pseudosettle.
  - It lost no peers and no run was affected.
  - 0.5 xBZZ was deposited into P's chequebook at 17:55 UTC, during the
    pseudosettle block's condition 4.
- **Q's chequebook was topped up** to 0.79 xBZZ before the SWAP block, so it
  could not run out during the block.
- **SSH.** Two SSH connections from the driver failed, and both were retried
  successfully. One upload was repeated because of that.

## Content B, availability

**The question:** does content announced by a provider stay retrievable after
the postage batch that paid for it expires? This is the availability case for
content providers, and it matters more than the speed case, which the tables
above ruled out.

**The setup:** a 4 MiB file uploaded on P with batch B, pinned there, and
announced with batch A. The plain copy is 1,033 chunks and the copy at the
default erasure coding level is 1,120 (measurement.md). Batches B and B2 expired
at about 18:35 UTC on 2026-09-16. The announcement records were stamped with
batch A, which is still alive, so the expiry does not take discovery with it.
Both copies were tested throughout, and are called B-0 and B-m below.

### The result

**The content is gone from the network, the provider still has it, the provider
is found, and the download fails anyway.**

**Table 4: content B after its batch expired.** Each row pools six runs, three
of B-0 and three of B-m, so every range spans both copies.

| Step | Condition | Runs | Chunks from P | Outcome |
|---|---|---|---|---|
| 1 | provider unreachable, providers off | 6 | 0 | HTTP 404, 2.32 to 3.66 s |
| 2 | hint to P, pseudosettle | 6 | 36 to 57 | HTTP 200, no body |
| 2 | lookup, pseudosettle | 6 | 40 to 44 | HTTP 200, no body |
| 2 | hint to P, SWAP | 6 | 42 to 57 | HTTP 200, no body |
| 2 | lookup, SWAP | 6 | 41 to 54 | HTTP 200, no body |

Not one of the 24 runs in step 2 returned the file. Four things had to be
checked separately, because each rules out a different explanation:

- **The content really is gone.** Step 1 returned 404 in 2.32 to 3.66 seconds,
  not a timeout, with 121 peers connected. Once the batch expired, the holders
  dropped it. Erasure coding did not change this: redundancy protects against
  missing chunks, not against every holder evicting the content.
- **P really does still have it.** Fetched on P itself: 4,194,304 bytes in
  0.039 s, hash exactly as uploaded. The pin held. This was a single manual
  check, and its output is kept in the bench notes rather than in a results
  file.
- **Discovery really does work.** The lookup returned P's overlay in 0 to 3
  seconds, including on runs where the ten minute cache had expired and a real
  lookup ran. Records outlive the content whose stamp has gone, as long as their
  own batch is alive.
- **No body is transferred at all.** This is not a partial file: the requester's
  output file is never created, and time to first byte equals total time to
  within 50 microseconds in all 24 runs. No hash is computed, because there is
  nothing to hash. "Chunks from P" counts the requester's `retrieved chunk` log
  lines naming P, the same measure as the content A tables, so it records what P
  served rather than what reached the file.

### Why it fails

**Not for the reason an earlier draft of this document gave.** That draft said
the per-peer credit window was exhausted and the requester therefore abandoned
the provider. A diagnostic run, one hinted download with the requester's
retrieval and accounting loggers at debug, refutes the abandonment, and refutes
the claim that credit **alone** explains the failure. It does not refute that
the window was reached, which the arithmetic below suggests it probably was:

- **Credit is not visibly the limit in this run.** The requester's balance
  with P moved from -8,750,000 to -8,790,000 units against a current threshold
  of 18,000,000, and a cheque was sent during the download, so the debt was
  settled as it accrued. That is a before and after snapshot, so a brief
  excursion to the threshold, settled by that same cheque, would not show in it.
  Nothing in the code logs a refusal on the preferred path either, so this is
  evidence against the credit explanation rather than proof that `prepareCredit`
  never refused.
- **The provider served what it was asked for.** Preferred attempts rose by 45,
  hits by 43, misses by 2. P was not the bottleneck at any point.
- **The failures are elsewhere.** 923 "failed to get chunk" messages, of which
  two name P. The rest are peers that do not have the content.
- **The counting was not capped.** journald dropped nothing during the window,
  so the chunk figures are not a logging artifact.

What happens instead: the requester asks the provider for a few dozen chunks,
asks other peers for the rest, those fail because the content is gone, and the
whole download aborts after about 77 chunk requests with no body transferred at
all. The preferred set is consulted per chunk request
(`pkg/retrieval/retrieval.go:160-215`), so the number of chunks the provider is
ever asked for is bounded by how many the requester asks for before giving up.

**Thirty-two of those 77 requests made no preferred attempt at all**, and this
run does not say why. A request whose context does not carry the preferred set
looks exactly like one that was refused credit, so that gap is where the earlier
explanation could still be living, and it is the more interesting half for
[#313](https://github.com/crtahlin/wasp/issues/313).

**The provider is never given the chance to serve the file.** That is a
different and more basic failure than a rate limit, and it is consistent with
the counts clustering at 41 to 44 rather than at the 58 the credit window would
predict. The clustering is across 24 runs; this explanation of it rests on one.

**The diagnostic is a single run with two loggers at debug**, and debug logging
slows a download, as the cheque diagnostic elsewhere in this campaign also
records. The 77 is the requester's retrieval request count, which counts
requests it relays for others as well, so it is an upper bound on what this
download asked for. By rule 7, once is not measured: treat these figures as a
diagnosis to be confirmed, not as a result. The counters that would confirm it
are already collected by the harness and need only be written to the results
file.

**Settlement mode makes no difference.** Pseudosettle and SWAP produce the same
numbers, and the download ends too quickly for either to change the outcome.

**This run does not separate the two mechanisms, and the arithmetic says both
may have been at work.** The requester started the download 8,750,000 units in
debt to P against a threshold of 18,000,000, which is 9,250,000 of headroom,
about 30 chunks at P's price. P then served 43, roughly 13,300,000 of new debt.
So the debt crossed that headroom partway through, and was cleared by the cheque
and the refresh the diagnostic records. In other words the balance sat at or
near the threshold during the download, which is exactly the condition under
which `prepareCredit` refuses. Unless P's price is well below 215,000, the
credit window plausibly limited P to 43 **and** the abort ended the download at
77 requests. Both can be true, and this run cannot tell them apart.

**What would happen if the requester persisted is not measured.** Credit would
become the limit at that point, and at the refresh rate of about 14 chunks a
second the file would take on the order of a minute or two. That is an estimate
from the accounting figures, not an observation, and it is the obvious thing for
a follow-up to measure.

### What this means for the feature

Phase 1 does what its spec says: announce, discover, prefer. All three work
here. What the spec never settled is what should happen when the preferred peer
is the **only** source. The requester asks it for a few dozen chunks, asks other
peers for the rest, and gives up when those fail, so the provider is never asked
for most of the content it holds. That gap is now
[#313](https://github.com/crtahlin/wasp/issues/313).

**This does not dismiss the accounting work.**
[#303](https://github.com/crtahlin/wasp/issues/303) and
[#304](https://github.com/crtahlin/wasp/issues/304) raise the credit rate, and
on the arithmetic above the credit window may well have been limiting the
provider at the same time as the abort was ending the download. What can be said
is that raising the rate alone would not have retrieved this file, because the
requester stopped asking. Both would need addressing, and which matters more is
not something these runs establish.

### What happened during these runs

- The bench machines were unreachable by 18:50 UTC on 2026-09-16, when the test
  first tried to run, and came back at about 05:10 UTC on 2026-09-17. When they
  went down is not recorded; the last successful contact was around 13:00 UTC.
  So 11.1 hours passed between the batches expiring and step 1, and P was not
  serving for at least the last 10.6 of them. This weakens step 1 as evidence
  about expiry alone, though a 404 returned in 2.32 to 3.66 s says the content
  was absent rather than merely hard to find.
- After the restart, a service from an earlier experiment started first and took
  P's API port, so a different node answered as P, with the same wallet and
  plausible looking output. It was caught before any run by comparing the
  overlay against P's known one. No measurement was taken against the wrong
  node, and the check is now part of the bench notes.

### Content B raw data

Every run, in the order they ran. No run produced a file, so there is no column
for a hash: the 404s returned nothing, and the 200s transferred no body at all.
"Chunks from P" counts the requester's `retrieved chunk` log lines naming P, so
it is what P served, not what reached a file. "Lookup s" is the time the
provider lookup took, and is blank where the provider was given directly as a
hint.

| Time (UTC) | Copy | Mode | Condition | Round | Chunks from P | Lookup s | HTTP | Total s |
|---|---|---|---|---|---|---|---|---|
| 05:40:03 | B-0 | ps-drop | none | 1 | 0 | - | 404 | 2.32 |
| 05:40:08 | B-m | ps-drop | none | 1 | 0 | - | 404 | 2.36 |
| 05:40:12 | B-0 | ps-drop | none | 2 | 0 | - | 404 | 3.14 |
| 05:40:17 | B-m | ps-drop | none | 2 | 0 | - | 404 | 2.69 |
| 05:40:22 | B-0 | ps-drop | none | 3 | 0 | - | 404 | 3.66 |
| 05:40:27 | B-m | ps-drop | none | 3 | 0 | - | 404 | 3.33 |
| 05:55:34 | B-0 | ps | hint | 1 | 57 | - | 200 | 2.25 |
| 05:55:42 | B-m | ps | hint | 1 | 51 | - | 200 | 1.14 |
| 05:55:45 | B-0 | ps | hint | 2 | 36 | - | 200 | 2.24 |
| 05:55:50 | B-m | ps | hint | 2 | 41 | - | 200 | 3.17 |
| 05:55:54 | B-0 | ps | hint | 3 | 43 | - | 200 | 2.96 |
| 05:55:58 | B-m | ps | hint | 3 | 41 | - | 200 | 2.53 |
| 05:56:02 | B-0 | ps | lookup | 1 | 44 | 0 | 200 | 2.44 |
| 05:56:05 | B-m | ps | lookup | 1 | 40 | 0 | 200 | 2.31 |
| 06:07:11 | B-0 | ps | lookup | 2 | 43 | 2 | 200 | 1.84 |
| 06:07:16 | B-m | ps | lookup | 2 | 41 | 1 | 200 | 1.94 |
| 06:18:22 | B-0 | ps | lookup | 3 | 43 | 2 | 200 | 2.47 |
| 06:18:27 | B-m | ps | lookup | 3 | 41 | 2 | 200 | 2.16 |
| 06:33:33 | B-0 | sw | hint | 1 | 57 | - | 200 | 2.32 |
| 06:33:36 | B-m | sw | hint | 1 | 43 | - | 200 | 2.26 |
| 06:33:40 | B-0 | sw | hint | 2 | 43 | - | 200 | 2.64 |
| 06:33:44 | B-m | sw | hint | 2 | 42 | - | 200 | 2.54 |
| 06:33:47 | B-0 | sw | hint | 3 | 43 | - | 200 | 2.35 |
| 06:33:51 | B-m | sw | hint | 3 | 42 | - | 200 | 2.45 |
| 06:33:55 | B-0 | sw | lookup | 1 | 44 | 0 | 200 | 2.19 |
| 06:34:00 | B-m | sw | lookup | 1 | 54 | 2 | 200 | 2.20 |
| 06:45:06 | B-0 | sw | lookup | 2 | 43 | 3 | 200 | 2.03 |
| 06:45:12 | B-m | sw | lookup | 2 | 41 | 2 | 200 | 2.09 |
| 06:56:18 | B-0 | sw | lookup | 3 | 43 | 2 | 200 | 2.77 |
| 06:56:24 | B-m | sw | lookup | 3 | 41 | 2 | 200 | 2.67 |

## Exploratory: provider with the maximum payment threshold

**Outside the fixed method.** It was run after the SWAP block to test the
explanation above, and it does not change the result judged by the method's
rules.

**What changed:**
- P announced a payment threshold of 108,000,000 units, the largest a node
  accepts (`payment-threshold`, `pkg/node/node.go:241`), instead of the default
  13,500,000.
- P was restarted and settled for 15 minutes. Q's accounting then showed P's
  threshold as 108,000,000.
- Everything else was as in the SWAP block, three runs each, alternating.
- P's default threshold was restored afterwards.

**Table 5: SWAP, P at the maximum payment threshold**

| Condition | Time to first byte, s | Total, s | MB/s | Chunks from provider | Preferred attempts | Misses |
|---|---|---|---|---|---|---|
| 2, no provider known | 0.53 (0.49-0.67) | 5.31 (5.30-5.79) | 3.16 (2.90-3.17) | 0 | 0 | 0 |
| 3, hint to P | 0.31 (0.31-0.31) | 4.86 (4.77-4.88) | 3.45 (3.44-3.52) | 869 (787-888) | 871 (789-890) | 2 |

**What it shows:**
- **The explanation holds.** With the larger credit window, P delivered about
  21% of the file, 5 times more than at the default threshold. That is more than
  the window alone allows, about 430 chunks, because each cheque can also cover
  more once the window is larger.
- **With the window lifted, the hint makes the download faster.** Here condition
  3 beats condition 2 beyond the spread:
  - total time 4.86 s against 5.31 s, about 8% less;
  - time to first byte 0.31 s against 0.53 s, about 40% less.
- **How far this goes is not settled.** Three runs outside the fixed method are
  a pointer for a follow-up, not a result.
- **What the follow-up must state:** a provider that raises its threshold
  extends more unsecured credit to every peer. That is the cost to the provider,
  and the follow-up must say so.

**P's bytes sent** during condition 3 were 3.7 to 4.1 MB, consistent with about
870 chunks of 4 KiB.

### How fast the credit window reopens

One further diagnostic download, hinted to P, with Q's accounting, swap and
pseudosettle loggers at debug. Its timing is not comparable with the tables
above, because the logging itself slows the download.

Measured over one download of 10.6 s:
- **9 cheques sent to P**, one about every 1.2 s;
- **57,790,000 accounting units** cleared by those cheques in total, about
  6,400,000 each, roughly 21 chunks;
- **about 0.3 s** between a cheque being sent and the payment being registered;
- **9 free-allowance refreshes**, one per second, as the code sets.

Two things follow:
- **A cheque clears much less than the window.** The amount paid is the debt
  minus what the refresh is about to cover, and only one payment is in flight at
  a time (`pkg/accounting/accounting.go:468-510`).
- **Each cheque costs three chain calls on the receiving side.** `ReceiveCheque`
  asks the chequebook contract for its issuer, its balance and what it has
  already paid out (`pkg/settlement/swap/chequebook/chequestore.go:162-195`),
  for every cheque, although the issuer cannot change for a given chequebook and
  the code's own comment calls the balance check "not particularly useful". The
  same code is in unmodified upstream bee; filed as
  [#300](https://github.com/crtahlin/wasp/issues/300).

**The 0.3 s is the chain calls.** A single chain call from P's machine to either
of its configured endpoints took 0.10 s (five calls each, 0.100 to 0.117 s, one
first call at 0.194 s). Three calls per cheque is 0.30 s, which is what the
cheque cycle takes.

So the rate at which one provider may serve one requester is about the free
allowance, 4,500,000 units a second, plus what cheques clear, about 5,000,000 to
6,000,000 units a second. At P's price that is roughly 34 chunks a second, which
is what the tables show: 163 chunks in about 5 s.

**Cashing out plays no part in this.** A cheque clears the debt when it is
accepted. Turning cheques into tokens on the chain is separate, and P had cashed
nothing during any of these runs.

Raw data:

| Time (UTC) | Cond | Round | TTFB s | Total s | Q peers | From provider | Attempts | Hits | Misses | Requests |
|---|---|---|---|---|---|---|---|---|---|---|
| 19:58:09 | 2 | 1 | 0.673 | 5.790 | 123 | 0 | 0 | 0 | 0 | 4129 |
| 19:59:23 | 3 | 1 | 0.308 | 4.767 | 123 | 888 | 890 | 888 | 2 | 4132 |
| 20:00:38 | 2 | 2 | 0.525 | 5.305 | 123 | 0 | 0 | 0 | 0 | 4129 |
| 20:01:53 | 3 | 2 | 0.309 | 4.877 | 123 | 869 | 871 | 869 | 2 | 4129 |
| 20:03:08 | 2 | 3 | 0.488 | 5.299 | 123 | 0 | 0 | 0 | 0 | 4129 |
| 20:04:22 | 3 | 3 | 0.312 | 4.858 | 123 | 787 | 789 | 787 | 2 | 4131 |

## Exploratory: payment-threshold sweep

**Outside the fixed method, and written before its runs** (measurement.md,
"Exploratory: provider payment-threshold sweep"). It does not change the result
judged by the method's rules.

**What ran.** Three of the four planned steps: 13,500,000 (the default),
27,000,000 and 54,000,000. At each step P was restarted, settled for 15 minutes
and until it had at least 100 peers, and Q's accounting was read to confirm the
threshold it had received. The 108,000,000 step did not run, for the reason
below, so the only measurement at that threshold is the earlier one in Table 5,
taken in a different session.

**Table 6: SWAP, P at three payment thresholds, three runs each**

| Threshold | Condition | Time to first byte, s | Total, s | Chunks from P | Share of file |
|---|---|---|---|---|---|
| 13,500,000 | 2, no provider known | 0.49 (0.44-0.57) | 7.05 (6.95-8.81) | 0 | 0% |
| 13,500,000 | 3, hint to P | 0.46 (0.43-0.48) | 6.80 (6.68-7.35) | 176 (164-235) | 4.3% |
| 27,000,000 | 2, no provider known | 0.71 (0.65-0.76) | 8.63 (7.96-9.24) | 0 | 0% |
| 27,000,000 | 3, hint to P | 0.31 (0.31-0.58) | 8.10 (7.90-8.74) | 453 (407-486) | 11.1% |
| 54,000,000 | 2, no provider known | 0.90 (0.70-1.21) | 10.79 (8.16-13.39) | 0 | 0% |
| 54,000,000 | 3, hint to P | 0.308 and 0.308 | 9.46 and 9.57 | 1261 and 1405 | 31% and 34% |

The 54,000,000 step has **two** runs of condition 3, not three. Its figures are
both runs, not a median.

**What it shows:**
- **The provider's share rises with the threshold**, from about 4% of the file
  at the default to about 11% at twice the default and about a third at four
  times it. The direction is clear and it matches the explanation in "Why the
  provider's share is small".
- **Time to first byte is the steadiest effect.** With a hint it was 0.308 s and
  0.308 s at 54,000,000, and 0.307 s to 0.312 s in the earlier session at
  108,000,000, while the controls in the same blocks ranged from 0.44 s to
  1.21 s. The hint saves the search for a source, which is a fixed cost and does
  not depend on the threshold.
- **Total time is not settled by this sweep.** Condition 3 is faster than
  condition 2 in every block, but the control spreads are wide, and at
  54,000,000 the three controls ran 8.16 s, 10.79 s and 13.39 s.

**Two cautions about the chunk counts, which matter more than the counts:**
- **Counts from different blocks are not comparable.** Credit available over a
  download is roughly the threshold plus a part that grows with how long the
  download lasts, so a slower download lets a provider deliver more at the same
  threshold. The blocks got slower as the campaign went on: the same condition
  took 5.3 s in the earlier session and 10.79 s at the 54,000,000 step.
- **Dividing by the duration does not repair this.** Delivery is roughly a fixed
  amount plus a rate, not a pure rate, so chunks per second is a rough
  normalization and not a quantity that should be equal across blocks. It is
  reported here only to show the size of the effect: about 26 chunks a second at
  the default, 52 at 27,000,000, 133 to 147 at 54,000,000, and about 178 in the
  earlier 108,000,000 session.

Taken together, the 54,000,000 step showing a larger **share** than the earlier
108,000,000 session is explained by its slower downloads, not by a provider
doing better with less credit. This sweep does not establish how the share
behaves between 54,000,000 and the maximum.

**Why it stopped early.** P's postage batch filled up. All three of P's batches
report a utilization ratio of 1, and each is immutable, so an upload fails when
any of its chunks falls in a bucket that is already full. Every run uploads a
fresh 16 MB file, and the campaign had done dozens. The first failure was the
third run of the 54,000,000 step, which returned no reference at all. The run
was correctly marked invalid, and no bad row reached the data.

**A harness defect this exposed, which cost the time.** When the upload returns
no reference it also returns no tag, and the wait-for-sync helper was then
called with no argument. Under `set -u` an unbound argument kills only the
subshell of the command substitution inside the loop, not the loop, so the
helper ran its full 240 iterations at 5 seconds each: 20 minutes per failed
upload, with a retry behind it. The only symptom in the log was an unexplained
repeated "unbound variable" line with no timestamp. The helper now returns
straight away when it is given no tag. This is bench tooling, not node code.

P's default payment threshold was restored afterwards, and the node was
confirmed running on it.

**What a follow-up must state:** a provider that raises its threshold extends
more unsecured credit to every peer, not only to the peer it wants to help. That
is the cost to the provider, and it is the reason this is not simply a setting
to recommend.

**Raw rows**, in the order they ran. The 54,000,000 step has no third run of
condition 3.

| Threshold | Cond | Round | TTFB s | Total s | From provider |
|---|---|---|---|---|---|
| 13,500,000 | 2 | 1 | 0.565 | 8.813 | 0 |
| 13,500,000 | 3 | 1 | 0.482 | 7.348 | 235 |
| 13,500,000 | 2 | 2 | 0.444 | 7.051 | 0 |
| 13,500,000 | 3 | 2 | 0.427 | 6.677 | 164 |
| 13,500,000 | 2 | 3 | 0.488 | 6.954 | 0 |
| 13,500,000 | 3 | 3 | 0.458 | 6.797 | 176 |
| 27,000,000 | 2 | 1 | 0.764 | 9.240 | 0 |
| 27,000,000 | 3 | 1 | 0.307 | 8.735 | 453 |
| 27,000,000 | 2 | 2 | 0.653 | 8.629 | 0 |
| 27,000,000 | 3 | 2 | 0.308 | 8.095 | 407 |
| 27,000,000 | 2 | 3 | 0.714 | 7.962 | 0 |
| 27,000,000 | 3 | 3 | 0.580 | 7.899 | 486 |
| 54,000,000 | 2 | 1 | 1.212 | 13.391 | 0 |
| 54,000,000 | 3 | 1 | 0.308 | 9.463 | 1261 |
| 54,000,000 | 2 | 2 | 0.895 | 10.789 | 0 |
| 54,000,000 | 3 | 2 | 0.308 | 9.574 | 1405 |
| 54,000,000 | 2 | 3 | 0.700 | 8.156 | 0 |

## Raw data

One row per run, in the order they ran. "From provider" counts log lines naming
P, or S in condition 4. "Requests" is Q's retrieval request count during the
download, which also counts requests Q relayed for others.

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
