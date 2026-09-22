# Measured: the first hinted download now completes

Issue: [#435](https://github.com/crtahlin/wasp/issues/435). Spec:
[preferred-rebuild.md](preferred-rebuild.md). Measurement that produced the
issue: [preferred-cold-start.md](preferred-cold-start.md).

Measured 2026-09-22 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester, both deployed on `fe0a9569`, the merge of the fix.
Harnesses `t435-verify.sh` and `t435-failfast.sh`, outside this repository.

## Arm 1: does the first hinted download after a disconnect complete?

This is the arm the issue exists for, and the one that failed in **every** run
recorded before the fix.

Each trial ingests a fresh 4 MiB object on the provider through
`POST /wasp/ingest`, which stores it with no postage so the network cannot hold
it, at redundancy NONE so the reader cannot rebuild a missing chunk. The
provider is then disconnected from the requester, confirmed absent from both
connected sets, and one hinted download is issued.

| trial | run A, provider disconnected | run B, same reference five seconds later |
|---|---|---|
| 1 | **200**, 4,194,304 bytes, checksum matches, 2.224 s | 200, 4,194,304 bytes, checksum matches, 1.726 s |
| 2 | **200**, 4,194,304 bytes, checksum matches, 1.992 s | 200, 4,194,304 bytes, checksum matches, 1.679 s |
| 3 | **200**, 4,194,304 bytes, checksum matches, 1.967 s | 200, 4,194,304 bytes, checksum matches, 1.604 s |

Before the fix, run A was **404 with a 36-byte error body in fifteen runs out
of fifteen**, across four separate harnesses.

The counters say it is the provider serving, and not some other peer having
picked the content up:

| | before the fix | after |
|---|---|---|
| `bee_retrieval_preferred_attempts`, run A | +14, in every trial | **+1044, +1045, +1041** |
| `bee_retrieval_preferred_hits`, run A | **0**, in every trial | **+1041, +1041, +1037** |

The object is 1,033 chunks. So the provider now serves essentially all of them
on the first attempt, where before it served none and the fourteen attempts
were for chunks that were not the download's at all.

## Arm 5: does a reference nobody holds still fail quickly?

This is the reject criterion. The fix keeps a flight alive longer by design, so
the thing worth proving is that it does not turn a fast not-found into a hang.

A random 32-byte reference that has never been stored, three runs each:

| condition | results |
|---|---|
| hinted at the provider | 404 in 2.32 s, 3.23 s, 4.33 s |
| no hint | 404 in 3.54 s, 0.90 s, 0.90 s |

Six of six return not-found in seconds. Nothing hangs.

## What is not measured here, and why

**Arms 2, 3 and 4 of the spec are not run.** Saying so matters more than the
arms themselves, because the numbers above would otherwise read as a fuller
validation than they are.

- **Arm 2, the warm hinted download**, and any rate comparison at all, is
  **deliberately not claimed**. Both nodes were restarted to deploy the fix and
  the runs above began about five minutes later, with peer counts recovered to
  124 and 123 against 124 and 121 before. Peer count is not warmth: an earlier
  arm on this bench read 1.2 times its true value two minutes after a restart,
  and that was the restart rather than the change. Rule 7 asks for matched node
  state, and this comparison does not have it. **The run B figures above are
  recorded for completeness and are not compared with anything.**
- **Arm 3, an unhinted download of network-held content**, is not run because
  every reference on this bench is sole-source. The same gap is recorded
  against #438's arm 1.
- **Arm 4, how much wider the ordinary walk gets**, is not run because the
  metric it would use cannot answer it. `peer_request_count` is incremented at
  the top of the retry branch, before the candidates block, so it counts loop
  iterations rather than outbound requests to ordinary peers, and this change
  alters the iteration count directly. A counter that separates ordinary
  dispatches is needed first. The unit tests do bound the concern: three runs
  at each of zero, one, two, four and eight providers all asked exactly 32
  ordinary peers, the error budget, unchanged.

The qualitative result is safe against the node-state caveat in the direction
that matters. Warming makes a node slower, so it could turn a 200 into a 404,
never a 404 into a 200. Arm 1 passing on a node five minutes from a restart is
therefore a stronger result than the same arm passing on a settled one, not a
weaker one.

## Node state

Both nodes on `0.1.3-main-2026-09-22-fe0a9569`, version read back from
`/health` after the restart rather than assumed. No restarts during the runs.
Requester 124 connected peers, provider 123. No blocklist entries. The stock
`bee.service` on the provider inactive throughout. The provider was
disconnected with `DELETE /peers` before each run A, and its absence confirmed
in both the libp2p and the Kademlia connected sets before the request was
issued.

Generated with help of AI.
