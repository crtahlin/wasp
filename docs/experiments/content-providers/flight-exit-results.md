# Measured: holding the error budget does not widen the peer walk

Issue: [#438](https://github.com/crtahlin/wasp/issues/438). Spec:
[flight-exit.md](flight-exit.md), arm 2.

Measured 2026-09-22 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester. Harness `cp290/t438-sweep.sh`, outside this
repository. Fresh sole-source content per run through `POST /wasp/ingest`,
4,194,304 bytes at redundancy NONE, downloaded from the requester with a hint
naming the provider.

The question arm 2 exists to answer: **does holding the error budget while a
provider answers widen the ordinary peer walk enough to need a cap?**

It was the one cost of the change that was reasoned about rather than measured.
The unit tests put the worst case at 40 of 40 peers against 32, and
`docs/DIFFERENCES.md` already records roughly fourfold for the neighbouring
[#392](https://github.com/crtahlin/wasp/issues/392) guard on a 150-peer node.

## The answer: no

Outbound retrieval requests per flight, which is
`bee_retrieval_peer_request_count` divided by
`bee_retrieval_request_attempts_count` over one download:

| build | per flight | mean |
|---|---|---|
| before, `2bd2d08c` | 1.389, 1.319, 1.301 | **1.337** |
| after, `4f88343c` | 1.367, 1.315, 1.284, 1.209 | **1.294** |

The ratio is **0.97**. There is no measurable widening; if anything the after
arm is marginally lower, which is within the drift both arms show as a node
warms.

## Why the worst case does not arrive

The same runs say it. `bee_retrieval_preferred_hits` rose by **1033** against
1035 attempts, in every run of both arms, on a file of 1,033 chunks. The
provider serves essentially every chunk on its first preferred attempt, so the
ordinary walk is never reached for it, and there is nothing for the held budget
to lengthen.

The walk only widens for a chunk whose provider is slow or refused, and on a
sole-source download from a healthy provider that is close to none of them. The
40-of-40 figure from the unit tests is a worst case constructed by making the
provider answer after a lag longer than the whole ordinary sweep. It is the
right number for that test and the wrong number to generalise from, which is
what an earlier draft of the differences row did.

**On this evidence the sweep does not need a cap.**

## Node state, and what is not claimed

Rule 7 asks for matched node state, and the first attempt at this arm did not
have it. Run two minutes after a restart the after arm read 1.645 and 1.537 per
flight, about 1.2 times the before arm, and that difference was the restart, not
the change. The table above is from a node settled about 25 minutes, at 122 and
123 peers against the before arm's 122 and 126.

**The download rate is not compared here**, deliberately. The four after runs
came in at 1.80, 2.05, 2.68 and 3.28 MB/s, a monotonic warming trend across
runs, against 2.37, 2.45 and 2.45 MB/s before. The arms overlap and the after
arm's spread is wider than the difference between them, so the honest statement
is that no rate regression is visible and this measurement is not sensitive
enough to find a small one. All seven downloads returned 4,194,304 bytes with a
checksum matching the source, which is the acceptance criterion the spec sets.

Arm 1, no regression on content the network holds, is **not run**. Every
reference on this bench is sole-source, so it needs a stamped upload first.

## What this changes in the documents

`docs/DIFFERENCES.md` said a hinted download "can be asked of the whole
connected set rather than of 32 peers", citing the unit test figures. That is
true as a worst case and misleading as a description, and it is amended to carry
this measurement alongside it.
