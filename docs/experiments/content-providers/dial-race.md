# A hinted download does not wait for the dial it starts

Issue: [#435](https://github.com/crtahlin/wasp/issues/435). Also closes out
[#313](https://github.com/crtahlin/wasp/issues/313), which this measurement
shows is fixed.

Measured 2026-09-22 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester, both on build `0.1.3-main-2026-09-22-2bd2d08c`.
Harnesses `cp290/t313-remeasure.sh` and `cp290/t313-dialrace.sh`, outside this
repository.

Content is sole-source throughout: a fresh 4,194,304 byte object per trial,
stored on the provider through `POST /wasp/ingest`, which writes it with no
postage and does not push it to the network. Redundancy is NONE, because with
erasure coding the reader can rebuild a missing chunk and the failure under test
would be hidden.

## The mechanism

1. A download names a provider in the `Wasp-Providers` header.
2. `ConnectHints` starts the dial in a background goroutine and returns at once
   (`pkg/providers/providers.go:411-445`, the body runs inside
   `s.goBackground`).
3. The API returns on the next line and retrieval begins
   (`pkg/api/providers.go:100-103`).
4. `preferredCandidates` skips any peer that is not already in the connected set
   (`pkg/retrieval/preferred.go:199-206`, the `!s.connectedFullNode(a)` arm).
5. The candidate list is therefore empty, the chunk falls to ordinary selection,
   and no ordinary peer holds sole-source content.

Step 4 is correct on its own. A node cannot open a retrieval stream to a peer it
has no connection to, and the comment above it gives a second reason: light
peers never enter the connected set, and a light node blocklists a peer that
opens a retrieval stream to it. The defect is that nothing makes the download
wait for the dial that the same request just started.

## The evidence

Three trials. Each disconnects the provider from the requester, runs one hinted
download, waits five seconds, and runs **the same reference** again. Connection
state is the only thing that differs between run A and run B.

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

Run A dials and gets no hits at all. Run B finds the peer already connected and
takes the whole object from it. The 14 attempts in run A are the tail of the
read arriving after the dial landed, and none of them produced a hit before the
read had already failed.

The prediction was written before the run: run A fails with no movement in
`preferred_hits` and one dial, run B completes. It held in three trials of
three, with no exception. A completing run A, or a failing run B, would have
refuted it.

## What this also settles: #313 is fixed

[#313](https://github.com/crtahlin/wasp/issues/313) recorded that the provider
is asked for only a small part of the content and the download then dies on
peers that never had it. Its measurement was 45 preferred attempts and 43 hits
against a 4 MiB file of 1,033 chunks, with 24 of 24 runs returning no body at
all.

Run B above is the same size of content on the same bench pair:

| | when #313 was written | now |
|---|---|---|
| `bee_retrieval_preferred_attempts` | 45 | 1035 |
| `bee_retrieval_preferred_hits` | 43 | **1033** |
| bytes delivered | 0, in 24 of 24 runs | 4,194,304 |
| checksum | no body at all | matches the source |

1,033 hits against the 1,033 chunks #313 counts is every chunk of the file,
served by the provider.

Rates, sole-source, one provider: 2.41, 2.42 and 2.44 MB/s across the three
trials, and 3.07 MB/s on a fourth download of a different reference. The
recorded per-peer baseline in [retrieval-rate.md](retrieval-rate.md) is about
264 KB/s.

The earlier diagnosis in [truncation-cause.md](truncation-cause.md), that
overdrafts exhaust `maxOverdraftReadmits` and the only holder is then dropped
from the chunk, also did not run here. A completed download records 471
overdrafts and 471 readmits, a difference of zero, so no preferred candidate was
dropped on any chunk. The credit gate refused chunks 471 times and cost the
download nothing.

This does not attribute the repair to any single merge. Several changes since
#313 was written touch this path, among them the network radius wait
([radius-wait.md](radius-wait.md)), per address already-connected accounting
([already-connected.md](already-connected.md)), and no longer dropping a
preferred candidate on overdraft
([provider-retention.md](provider-retention.md)). The measurement establishes
that the behaviour is fixed, not which change fixed it.

## A wrong conclusion that was nearly published

The first run of the day, `cp290/t313-remeasure.sh`, returned 404 on all three
hinted downloads. Read on its own that says #313 is not fixed, and the arms had
passed every precondition the harness checked: both builds identical, network
radius known on the requester, provider serving the reference locally.

The preconditions it did not check are the ones that mattered. The hint overlay
was still correct and `providers-enable` was on at both ends, but the two nodes
had not connected to each other since the restart an hour earlier, and
`bee_retrieval_preferred_attempts` was still 0 after all nine runs. The preferred
path had never engaged. What the harness measured was #435, not #313.

Two nodes with random overlays in a large network have no reason to be connected
to each other. On this bench they are connected only because a hint dialled them
together, which is exactly the condition #435 breaks.

The general point for later harnesses: a measurement of the preferred path must
assert that `bee_retrieval_preferred_attempts` moved. An arm where it stays at
zero did not measure the preferred path, whatever else it reports.
