# A hinted download does not wait for the dial it starts

Issue: [#435](https://github.com/crtahlin/wasp/issues/435). Also reports a
re-measurement of [#313](https://github.com/crtahlin/wasp/issues/313).

Measured 2026-09-22 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester, both on build `0.1.3-main-2026-09-22-2bd2d08c`.
Harnesses `cp290/t313-remeasure.sh`, `cp290/t313-dialrace.sh` and
`cp290/t313-overdraft.sh`, outside this repository.

Content is sole-source throughout: a fresh 4,194,304 byte object per trial,
stored on the provider through `POST /wasp/ingest`, which writes it with no
postage and does not push it to the network. Redundancy is NONE, because with
erasure coding the reader can rebuild a missing chunk and the failure under test
would be hidden. At 4,194,304 bytes that is 1,024 leaf chunks, 8 chunks at the
level above, and 1 root, which is 1,033.

Two terms used throughout, both from [retrieval-rate.md](retrieval-rate.md):
an **overdraft** is a chunk request refused because the requester's debt to that
peer is at the threshold the peer announced, and a **readmit** is that peer
being kept as a candidate for the chunk afterwards rather than dropped from it.

## The hypothesis was registered before this run, elsewhere

[directory-ingest-results.md](directory-ingest-results.md) already named this
cause and named the experiment that would settle it:

> `Wasp-Providers` connects in the background and a preferred candidate is
> filtered to connected peers, so a first request can be made before the
> provider is usable. That is the likeliest cause and it is **correlational, not
> established**: it would take a run that disconnects the provider deliberately
> and then makes one hinted request to settle it.

That is the run below.

## The mechanism

1. A download names a provider in the `Wasp-Providers` header.
2. `ConnectHints` starts the dial in a background goroutine and returns at once
   (`pkg/providers/providers.go:411-445`, the body runs inside
   `s.goBackground`).
3. The API calls it and returns two lines later, so retrieval begins without
   waiting (`pkg/api/providers.go:100-103`).
4. `preferredCandidates` skips any peer that is not already in the connected set
   (`pkg/retrieval/preferred.go:199-206`, the `!s.connectedFullNode(a)` arm).
5. The candidate list is empty for as long as the dial is outstanding, the
   chunks issued in that window fall to ordinary selection, and no ordinary peer
   holds sole-source content.

Step 4 is correct on its own. A node cannot open a retrieval stream to a peer it
has no connection to, and the comment above it gives a second reason: light
peers never enter the connected set, and a light node blocklists a peer that
opens a retrieval stream to it. The defect is that nothing makes the download
wait for the dial that the same request just started.

## The evidence

Three trials. Each disconnects the provider from the requester, runs one hinted
download, waits five seconds, and runs **the same reference** again.

| trial | run A, provider disconnected | run B, five seconds later |
|---|---|---|
| 1 | 404, 36 bytes, 4.596 s | 200, 4,194,304 bytes, checksum matches, 1.744 s |
| 2 | 404, 36 bytes, 5.083 s | 200, 4,194,304 bytes, checksum matches, 1.733 s |
| 3 | 404, 36 bytes, 3.047 s | 200, 4,194,304 bytes, checksum matches, 1.719 s |

Counters, as the change across each run, identical in all three trials:

| counter | run A | run B |
|---|---|---|
| `bee_retrieval_preferred_hits` | 0 | 1033 |
| `bee_retrieval_preferred_attempts` | 14 | 1035 |
| `bee_providers_connects_dialed` | 1 | 0 |
| `bee_providers_connects_already_connected` | 0 | 1 |

Connection state is the only thing the harness **changes** between run A and run
B. It is not the only thing that differs: run B is the second download of the
same reference, so whatever run A managed to put in the requester's cache is
there for run B, and the 14 attempts in run A show it fetched something.

### Part of the registered prediction was wrong

The prediction is in `cp290/t313-dialrace.sh` lines 13 to 18, written before the
run:

> run A (provider just disconnected) fails, `preferred_attempts` does NOT move,
> `providers_connects_dialed` DOES move; run B (same reference, seconds later)
> completes with a matching checksum, `preferred_attempts` moves by about one
> chunk per 4096 bytes.

Three of its four clauses held in all three trials. **The clause naming
`preferred_attempts` on run A was refuted in all three**: it moved by 14, not by
zero. `preferred_hits` is what stayed at zero.

That difference matters and is not a wording detail.
`maxPreferredAttempts = 2` (`pkg/retrieval/preferred.go:34`), so 14 attempts
means at least 7 chunks reached a **non-empty** candidate list during run A. The
candidate list was therefore not empty for the whole of run A, which is why step
5 above says "for as long as the dial is outstanding" rather than "for the whole
request".

The obvious reading is that the dial landed partway through run A and the
attempts after it came too late to matter, which fits
`connects_dialed` rising by one during run A. **That reading is not measured.**
The harness records only the before-and-after difference, so nothing here
timestamps an individual attempt against the moment the dial completed. It is a
plausible account of a counter difference and no more, and it is the account of
a refuted prediction, which is exactly where hedging is owed.

What is measured, and is enough on its own: run A produced **zero** preferred
hits in all three trials while run B produced 1033 in all three, on the same
reference seconds apart.

## Overdrafts are not what fails here

The chain in [truncation-cause.md](truncation-cause.md) needs a preferred
candidate to be dropped from a chunk. Its observable is
`bee_retrieval_preferred_overdrafts` minus `bee_retrieval_preferred_readmits`,
the count of overdrafts not readmitted.

Three further completed downloads, fresh sole-source content each, provider
already connected, harness `cp290/t313-overdraft.sh`:

| run | attempts | hits | overdrafts | readmits | not readmitted |
|---|---|---|---|---|---|
| 1 | 1035 | 1033 | 244 | 244 | **0** |
| 2 | 1035 | 1033 | 185 | 185 | **0** |
| 3 | 1035 | 1033 | 36 | 36 | **0** |

The credit gate refused chunks between 36 and 244 times per download and cost
the download nothing in any of them. The spread in refusals with an unchanging
result is the point: the count varies with the credit state when the download
starts, and the difference stays at zero regardless.

These runs also reproduce the attempt and hit counts independently, 1035 and
1033, identical in all three.

**The constant that chain turns on no longer exists.**
`truncation-cause.md` describes the drop as happening once overdrafts on a chunk
exceed `maxOverdraftReadmits`, which was 8. That constant is gone from
non-test code. [#392](https://github.com/crtahlin/wasp/issues/392) replaced the
count of tries with a time window, `providerCreditWait = 30 * time.Second`
(`pkg/retrieval/retrieval.go:179`, used at `:132`). So on this build the chain
could not run for a structural reason, not merely because no candidate happened
to be dropped. The zero difference above is consistent with that and does not
establish it independently.

## What this says about #313

[#313](https://github.com/crtahlin/wasp/issues/313) reported that a sole-source
provider is asked for only a small part of the content and the download then
fails on peers that never held it. Two different figures in that issue and in
[results.md](results.md) have to be kept apart, because the repository's own
notes say so:

- Across **24 runs**, not one retrieved the file, and the provider served
  **36 to 57 chunks** of the 1,033.
- A **single** diagnostic run, with the retrieval and accounting loggers at
  debug, recorded 45 preferred attempts and 43 hits. `results.md` marks that one
  explicitly: "By rule 7, once is not measured: treat these figures as a
  diagnosis to be confirmed, not as a result."

Against the 24-run figure, which is the one with the sample size:

| | #313, 24 runs | here, 6 runs |
|---|---|---|
| chunks the provider served | 36 to 57 of 1,033 | 1033 of 1,033 |
| bytes delivered | 0, in 24 of 24 | 4,194,304, in 6 of 6 |
| checksum | no body at all | matches the source, 6 of 6 |

1033 hits against 1035 attempts on a 1,033 chunk file is **consistent with**
every chunk of the file being served by the provider. It is not proof of it:
`PreferredHits` (`pkg/retrieval/preferred.go:271`) counts successful preferred
fetches, not distinct chunks, so 1032 distinct chunks plus one retry would
produce the same number.

### Two limits on that claim

**It holds for the already-connected case only.** Every completed download in
every log here ran with the provider already connected, by construction in run B
and throughout the overdraft runs. The first hinted download to an unconnected
provider still returns nothing, which is #435. A reader should take "#313's
failure mode is gone" to mean "gone once the provider is connected".

**The content is not #313's content.** #313 used an object whose postage batch
had expired and which had previously been pushed to the network. These runs use
freshly ingested objects that were never pushed. Both are sole-source at the
time of measurement, which is the property the test needs, but they are not the
same conditions.

This does not attribute the repair to any single merge. Several changes since
#313 was written touch this path, among them the network radius wait
([radius-wait.md](radius-wait.md)), per address already-connected accounting
([already-connected.md](already-connected.md)), keeping a preferred candidate
through a credit refusal ([provider-retention.md](provider-retention.md)) and
#392's replacement of the readmit count with a time window.

## Rates, and why the comparison is loose

Six completed downloads, all logged: 2.41, 2.42 and 2.44 MB/s in the three
trials, and 2.19, 2.51 and 2.96 MB/s in the three overdraft runs. MB is 10^6
bytes. Every one returned 4,194,304 bytes with a checksum matching the source.

[retrieval-rate.md](retrieval-rate.md) records a per-peer baseline of 263,352
to 263,464 B/s, and that figure is specifically its **lookahead buffer 0** arm,
which also recorded zero overdrafts in all three runs. Its buffer 262,144 arm
recorded 1,086,300 B/s from a single completed run.

**These runs did not set a buffer at all**, so the condition is not matched to
either arm and the comparison is indicative, not controlled. Quoting 2.4 MB/s
against 264 KB/s would invite a nine times reading that the two measurements do
not support. What can be said is that six sole-source downloads completed at
between 2.19 and 2.96 MB/s, which is above every completed figure in that table.

## Something the runs showed and this document does not explain

The first harness of the day, `cp290/t313-remeasure.sh`, recorded two runs in
its discovery arm returning **HTTP 200 with a zero-length body**, where the
other seven runs returned 404. A 200 with no body is a different failure from a
404, and the two ran seconds apart against the same node state. Nothing here
explains it, and it is not accounted for by the never-connected story. It is
recorded so it is not lost.

## A wrong conclusion that was nearly published

That first harness returned 404 on all three hinted downloads. Read on its own
that says #313 is not fixed, and the arms had passed every precondition the
harness checked: both builds identical, network radius known on the requester,
provider serving the reference locally.

The preconditions it did not check are the ones that mattered. Checked
afterwards and logged, the hint overlay still matched the provider's live
overlay and `providers-enable` was on at both ends. What had not been checked is
that the two nodes had not connected to each other since the restart an hour
earlier. A counter snapshot taken after those nine runs, before any further
request, read `bee_retrieval_preferred_attempts 0`. That counter only increases,
so none of the nine runs moved it: the preferred path never engaged. What the
harness measured was #435, not #313.

Two nodes with random overlays in a large network have no reason to be connected
to each other. On this bench they are connected only because a hint dialed them
together, which is exactly the condition #435 breaks.

The general point for later harnesses: a measurement of the preferred path must
assert that `bee_retrieval_preferred_attempts` moved. An arm where it stays at
zero did not measure the preferred path, whatever else it reports.

## Upstream portability

None of this applies to upstream Bee. `pkg/providers`,
`pkg/retrieval/preferred.go` and `pkg/api/providers.go` do not exist in
`upstream/v2.8.2`, checked with `git ls-tree`. The preferred path is this fork's
own, added by [#290](https://github.com/crtahlin/wasp/issues/290), so upstream
has no notion of a peer known to hold a chunk and cannot have this behavior.
**No `affects-upstream` marker** on #435 or #313.
