# Retrieval and the network radius: results

Measured 2026-09-21 on the two-node bench, requester `bench-2` and provider
`bench-1`, round-trip time about 30 ms. Spec:
[radius-wait.md](radius-wait.md). Issue:
[#398](https://github.com/crtahlin/wasp/issues/398).

**Headline: the change removes the truncation, and does not make the download
fast.** A 50 MB file held by a single provider, fetched immediately after
restarting the requester, now arrives complete with a matching checksum where
the control truncates, three runs to none. It arrives at about **51 KB/s**, which
is a separate problem and is recorded here rather than left out.

## Builds

| Name | Version | What it is |
|---|---|---|
| fix | `0.1.3-f398-3a31b982` | retrieval given a radius lookup that reports the radius as unknown instead of waiting |
| control | `0.1.3-359-54c926a5` | the build before that change |

The provider was not changed and ran throughout, because the change is
requester-side.

## Arm 1: the defect, with its mechanism

Fifty megabytes, sole source, hinted, started as soon as the requester
answered after a restart. The content is ingested **before** the restart, so
the download begins as early in the window as possible.

| Round | Build | Delivered | Result | Time | Rate | Radius before | Goroutines waiting |
|---|---|---|---|---|---|---|---|
| 1 | fix | 52,428,800 | complete | 1,012 s | 51,809 B/s | 0 | **0** |
| 1 | control | 524,288 | truncated | 53 s | | 0 | **210** |
| 2 | control | 524,288 | truncated | 52 s | | 0 | **328** |
| 2 | fix | 52,428,800 | complete | 1,024 s | 51,224 B/s | 0 | **0** |
| 3 | fix | 52,428,800 | complete | 998 s | 52,549 B/s | 0 | **0** |
| 3 | control | 1,048,576 | truncated | 30 s | | 0 | **330** |

**The fix completes three of three with matching checksums. The control
completes none of three.** The acceptance condition in the spec was all three
runs on the change delivering 52,428,800 bytes with a matching checksum, and
it is met. The control was expected to truncate, and did; had it not, nothing
here could have been concluded.

Where the control stops varies, 524,288 bytes twice and 1,048,576 once, which
is 128 and 256 chunks. It is where the read unit that was in flight gave up,
not a constant of the defect.

**Every run had the condition under test present.** `bee_salud_network_radius`
read zero immediately before each download, so the control is a control rather
than an assumption. A control run that found the radius already known would
prove nothing and is excluded by design.

**The mechanism check separates the builds completely.** Goroutines parked in
the radius lookup during the download: **zero on all three fix runs, and
210, 328 and 330 on the three control runs**. This is recorded beside the delivered bytes because bytes alone
cannot distinguish a fixed cause from a lucky run, and the arm 1 of
[#392](https://github.com/crtahlin/wasp/issues/392) is what that mistake looks
like.

**The two times are not a speed comparison.** The control "finishes" in 52
seconds because it gives up; the fix takes longer because it keeps working.

## What is still wrong: the rate in that window

The fix delivers at 51,809, 51,224 and 52,549 B/s. That is a spread of 2.6
per cent across three runs, which is the signature of a hard limit rather than
of a busy network.

At 4,096 bytes a chunk that is about 12.6 chunks a second. The pseudosettle
time allowance is 4,500,000 units a second and the measured price is about
307,000 units a chunk, which is 14.7 chunks a second. **The download after a
restart is running at close to the refreshment allowance alone**, which means
few or no cheques are being issued.

For contrast, the same pair with the node already up runs the same download at
**2.7 to 4.1 MB/s**, which is past the 1 MB/s this work was aiming at. So the
slowness belongs to the minutes after a restart, not to sole-source downloads
in general.

This is where [#396](https://github.com/crtahlin/wasp/issues/396) and
[#316](https://github.com/crtahlin/wasp/issues/316) apply. #396 was filed
against these stalls and read them as a settlement defect; that reading was
wrong about the truncation, which was this issue, and may be right about the
rate. #316 reports that `refreshDue` in `settle` is computed without the one
second cap that `PrepareCredit` applies, so the term grows as the last
refreshment recedes and can push a cheque below `minimumPayment`. That is the
right shape for a download running on refreshments alone, and it is now
testable for the first time, because the blocking that used to mask everything
is gone.

## What this does not show

- **Nothing about warm downloads.** The change is inert once the radius is
  known, and that is argued from the code and from the unit tests rather than
  measured here. The no-regression arm in the spec is still to run.
- **Nothing about other sizes.** Only 50 MB was run. The size sweep is a
  separate arm and is what answers whether the feature is reliable.
- **Nothing about the fan-out this gives up.** While the radius is unknown a
  request is no longer multiplexed across the neighbourhood. For sole-source
  content that costs nothing, since no neighbour has the chunk, but that is
  reasoning rather than measurement, and content the network holds is where it
  would show.

## Harness notes

Every run used a guarded restart, after a shutdown-ordering race
([#399](https://github.com/crtahlin/wasp/issues/399)) crashed the node with a
SIGSEGV at 12:49:08 on the same day: the reserve worker was iterating the
store while it was being closed. A crashed shutdown is not a clean restart,
because pebble replays its write-ahead log, so the guard refuses to restart a
node that is not healthy, waits for the reserve worker's counters to stop
moving, checks the unit start time actually changed, and marks any run
spanning a SIGSEGV as discarded rather than recording it. **No run reported
here spans a crash**, checked against the node's own journal.

Three earlier attempts at this measurement were defeated by the harness rather
than by the node, and each is worth stating because each looked like a result:

- waiting for `/readiness` before downloading took nine minutes on one run, by
  which time the radius is long known and the control has nothing to fail on;
- ingesting the 50 MB file after the restart consumed the window being tested;
- a restart command that silently did nothing left the node reporting the
  previous build for four minutes, so a later run was attributed to the wrong
  build until the version check caught it.
