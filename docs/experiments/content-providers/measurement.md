# Content providers: measurement method, phase 1

Issue: [#290](https://github.com/crtahlin/wasp/issues/290). Spec:
[spec.md](spec.md), section Measurement.

Status: the method, fixed before the first run (rule 7). Results go to
`results.md`, with the raw numbers of every run.

## Terms

The terms of [spec.md](spec.md) apply. In addition:

- **Time to first byte**: the time from sending a download request until the first
  byte of the response arrives.
- **Block**: a group of runs with the same settlement mode on the requester.
- **Spread**: the range, minimum to maximum, of the runs of one condition.

## What is measured

Hypothesis 1 (speed) and hypothesis 2 (availability) of the spec. Hypothesis 3
(live streams) is not part of phase 1.

## Builds

- **wasp** `main` at `de136880`. It contains phase 1, merged as `5608aa53`; every
  change since then is documentation only (`git diff --stat 5608aa53 de136880`).
- **bee v2.8.2**, the stock release, for the stock node S.

## Nodes

- **P, the provider.** A full node on `bench-1`, on mainnet, running the wasp
  build with `providers-enable: true` and `swap-enable: true`. It has a chequebook
  with no deposit, so that it can receive cheques. It is an existing node whose
  reserve is already filled. It holds the test content, pinned.
- **Q, the requester.** A full node on `bench-2`, on mainnet, running the wasp
  build. Its settings change per block (see Order). It has a chequebook with a
  deposit, which it uses only in the SWAP block.
- **S, a stock node.** A bee v2.8.2 full node on `bench-1`, started with an empty
  data directory about 20 minutes before the runs that need it, and stopped after
  them. It is the hint target of condition 4.

**Network path.** The direct path between the two bench machines has a round trip
below 1 ms, far shorter than a usual path between Swarm nodes. That would favor
P. For the whole measurement, 30 ms is added to every packet that `bench-1` sends
to `bench-2`. Before each block, Q pings P, and S when it runs, with
`POST /pingpong/{overlay}`. A round trip below 25 ms means the delay is not in
the path, and the block does not start.

## Differences from the spec

1. **Condition 1 runs the wasp build with `providers-enable: false`, not stock
   bee.** Q's data directory uses the Pebble storage engine, which bee v2.8.2
   cannot open. A separate stock node would differ from Q in state: peers, reserve
   and radius. With the setting off, phase 1 is inactive: no header is sent or
   honored, no lookup runs, and retrieval takes the same path as stock (spec.md,
   Rollout and rollback). Condition 1 therefore measures the node without the
   feature. The speed of the stock build itself is not measured.
2. **Q is a full node only.** The runs with a light requester are not made in this
   round, because work on light nodes has been set aside. They would need a funded
   light node.
3. **Content B is not tested with discovery inside the download.** A download
   starts a lookup only at its 64th chunk request (`pkg/api/providers.go:114-119`).
   A file the network has lost fails at its root chunk, its first request, so
   that lookup never starts. Content B is tested with the hint, and with a lookup
   in two steps: `GET /wasp/providers/{reference}/lookup`, then the download with
   the returned overlays as the hint. The gap is filed as
   [#297](https://github.com/crtahlin/wasp/issues/297).

## Postage

- **Batch A**, depth 20, bought by P for about 4 days. It stamps content A and all
  provider records, including the records that announce content B.
  - The runs upload about 162,000 chunks. At depth 20 each of the batch's 65,536
    buckets holds 16 chunks. At depth 19 it would hold 8, and some buckets would
    overflow before the runs end.
- **Batch B**, depth 17, bought by P for the contract's minimum validity
  (17,280 blocks, about 24 hours) plus 10%. It stamps content B only, and is left
  to expire.

## Content

**Content A**, one fresh file per run:
- 16 MiB of random bytes: 4,096 data chunks, 32 intermediate chunks and a root,
  4,129 chunks in all.
- P uploads it through `POST /bytes` with `Swarm-Pin: true` and batch A. It waits
  until the upload tag reports every chunk synced, then 60 s more.
- Q has never requested it. Q downloads it with `GET /bytes/{reference}` and
  `Swarm-Cache: false`, and checks its SHA-256 against P's copy.
- **Only the files of condition 5 are announced.** P announces each with
  `POST /wasp/providers/{reference}` and batch A after the upload has synced, and Q
  starts the download 3 minutes later. The files of the other conditions are not
  announced, so a lookup by Q finds nothing for them. The cost of that lookup is
  part of what those conditions measure.
- **Condition 6** uses a file X that P uploads unpinned, and a file Xa, the first
  8 MiB of X, that P uploads pinned.
  - Xa's data chunks are the first 2,048 data chunks of X. Its 16 intermediate
    chunks are also X's first 16, because they have the same children.
  - So P holds 2,064 of X's 4,129 chunks, about half. The measured share of chunks
    served by P is reported as it comes out.

**Content B:**
- One fresh 4 MiB file, uploaded by P with batch B and pinned, at the start of the
  measurement.
- Announced by P with batch A. P renews its records every window by itself.
- Tested after batch B has expired and at least 1 hour more has passed, so that
  the reserves holding it have dropped it.

## Order

**P** keeps its settings throughout. **Q** runs two blocks:

1. **Pseudosettle block**, Q with `swap-enable: false`.
2. **SWAP block**, Q with `swap-enable: true`.

Within each block:
1. **Q with `providers-enable: false`:** condition 1, three runs.
2. **Q with `providers-enable: true`:**
   - three rounds, each running conditions 2, 3, 5 and 6 once, in an order rotated
     by one position per round;
   - then condition 4, three runs, with S started for them only, so that S's
     reserve filling does not load P's machine during the other conditions.

**After each change of Q's settings**, which is a restart, Q waits at least
15 minutes, and until its connected peers are back to at least 80% of the count
before the restart.

**Lookup cost**, in the pseudosettle block with `providers-enable: true`:
- three lookups, through Q's `GET /wasp/providers/{reference}/lookup`, of fresh
  1 MiB files that P uploaded and announced 3 minutes earlier;
- three lookups of random references that nobody announced, which is the common
  case.

**Content B**, after batch B has expired, Q with `providers-enable: true`, three
runs each:
1. no hint: the check that the network has really lost the file;
2. the hint to P;
3. the two-step lookup, then the download with the result as the hint.

If the download without a hint succeeds, forwarding nodes still cache the file,
and content B is reported as not testable.

## Conditions

As in the spec, with the differences above:

| # | Q's setting | Provider for Q |
|---|---|---|
| 1 | `providers-enable: false` | none |
| 2 | on | none known |
| 3 | on | hint to P |
| 4 | on | hint to S, a stock node |
| 5 | on | P found by discovery |
| 6 | on | hint to P, which holds about half of the file |

## What each run records

Before the download: Q's connected peers, storage radius and load average, and
whether P (or S) is connected to Q.

The download:
- time to first byte, total time, bytes and HTTP status, from `curl -w`;
- whether the SHA-256 matches.

After the download, differences in Q's metrics:
- `bee_retrieval_preferred_attempts`, `bee_retrieval_preferred_hits`,
  `bee_retrieval_preferred_misses`;
- `bee_retrieval_request_count`. It also counts the requests Q relays for others,
  so it is an upper bound for this download.

On P: bytes sent on its network interface during the run. These include all of P's
other traffic, so they are context, not the measure.

**Derived per run:**
- throughput: bytes divided by total time;
- share served by the provider: preferred hits divided by 4,129;
- chunks P did not serve, in conditions 3 and 5, where P holds every chunk. Misses,
  slow attempts and overdraft skips together; misses are also reported alone.

**Expected limit.** Without SWAP, P can serve Q at most about 14 chunks per second
once Q's allowance with P is used up (spec.md, Hypothesis), so in the pseudosettle
block P will serve only a small share of a 16 MiB file. The SWAP block has no such
limit.

For each lookup: its wall time, and the difference in Q's request count as an upper
bound on the retrievals it made.

For content B: success or failure, and total time.

## When a run is invalid

A run is invalid, counted, and repeated when:
- content A arrives with a status other than 200, or with a SHA-256 that does not
  match;
- Q's connected peers have dropped below 80% of the count at the start of the
  block;
- the delay check of the block failed.

## What a negative result is

As in the spec, any of:
- **No gain.** Conditions 3 and 5 show no gain in time to first byte or throughput
  over condition 2, beyond the spread: the spreads overlap.
- **The lookup does not pay.** A lookup costs more time than it saves on a 16 MiB
  file, that is, condition 5 is not faster than condition 2 beyond the spread.
- **Availability fails.** Content B still fails with a correct hint.

The first stops work on phase 2 onwards. The mechanism is still kept for
availability if content B works (spec.md, Measurement).

## Reporting

`results.md` gives, per block and condition:
- every run's raw numbers;
- the median and the spread.

Each table's caption names the build, the machine roles, mainnet, the added delay
and the block. The scripts, addresses and batch identifiers stay outside the
repository (rule 10).

Generated with help of AI.
