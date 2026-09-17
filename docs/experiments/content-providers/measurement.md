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
- **Batch depth, bucket**: a postage batch of depth d can stamp 2^d chunks. They
  are divided into 65,536 buckets by chunk address, each holding 2^(d-16) chunks.
  A batch counts as full as soon as one bucket is full.
- **Chequebook**: the contract through which a node pays and receives SWAP
  cheques.
- **Pebble**: one of the two storage engines a wasp node can use for its local
  store.
- **Upload tag**: the counter a node keeps for an upload. It reports how many
  chunks the upload split into (`split`), how many the node already had (`seen`)
  and how many have reached their neighborhood (`synced`).
- **Storage radius**: the proximity order that defines the neighborhood a full
  node stores.
- **Erasure coding**: extra parity chunks added to each level of a file's chunk
  tree, so that a download can rebuild chunks it cannot retrieve. The level
  (0 none, 1 MEDIUM, up to 4 PARANOID) is chosen at upload with the
  `Swarm-Redundancy-Level` header.
- **Redundancy decoder**: the component that fetches the children of one chunk of
  an erasure-coded tree, and rebuilds missing ones from the parity chunks.
- **Prefetch**: the decoder's own fetching of those children, which it starts as
  soon as it is created, before anything asks for them.

## What is measured

Hypothesis 1 (speed) and hypothesis 2 (availability) of the spec. Hypothesis 3
(live streams) is not part of phase 1.

## Builds

- **wasp** `main` at `de136880`. It contains phase 1, merged as `5608aa53`; every
  change since then is documentation only (`git diff --stat 5608aa53 de136880`).
- **bee v2.8.2**: the stock release, for the stock node S.

## Nodes

The machine roles are defined in `docs/agent-playbooks/test-bench.md`.

- **P, the provider.** A full node on `bench-1`, on mainnet, running the wasp
  build with `providers-enable: true` and `swap-enable: true`. It has a chequebook
  with no deposit, so that it can receive cheques. It is an existing node whose
  reserve is already filled. It holds the test content, pinned.
- **Q, the requester.** A full node on `bench-2`, on mainnet, running the wasp
  build. Its settings change between blocks and within a block (see Order). It has
  a chequebook with a deposit, which it uses only in the SWAP block.
- **S, a stock node.** A bee v2.8.2 full node on `bench-1` with `swap-enable:
  false`. It is the hint target of condition 4.
  - It runs only for the condition 4 runs, starting about 20 minutes before them,
    so that its reserve filling does not load P's machine during the other
    conditions.
  - It keeps its data directory, and so its overlay, between the two blocks. The
    directory is deleted after the SWAP block.
  - Because S has no chequebook, Q settles with S by pseudosettle in both blocks.
    Condition 4 therefore tests whether a stock hint target slows a download down,
    not how fast S can serve.

**Connections.** At the start of each block Q connects to P, and before the
condition 4 runs to S, with `POST /connect/{multiaddr}`. A hint dials only an
overlay that Q's address book already knows, so without this step condition 4
could silently turn into condition 2. A `POST /connect` to a peer that is already
connected answers with an error (`pkg/api/peer.go:34-39`); that answer counts as
success here.

**Network path.**
- The direct path between the two bench machines has a round trip below 1 ms, far
  shorter than a usual path between Swarm nodes. That would favor P.
- For the whole measurement, 30 ms is added to every packet that Q's machine sends
  to P's machine, by any route.
- **The check:** Q pings P with `POST /pingpong/{overlay}` before and after each
  block, and pings S before the condition 4 runs. A round trip below 25 ms means
  the delay is not in the path:
  - before a block, the block does not start;
  - after a block, the whole block is invalid.

  The ping includes opening a stream, so it serves as a check only and is not
  reported as the path's round trip.

## Differences from the spec, and choices the spec leaves open

1. **Condition 1 runs the wasp build with `providers-enable: false`, not stock
   bee.**
   - Q's data directory uses Pebble, which bee v2.8.2 cannot open. A separate
     stock node would differ from Q in state: peers, reserve and radius.
   - With the setting off, the providers service is never created and phase 1
     is inactive: no header is sent or honored, no lookup runs, and retrieval
     takes the same path as stock (spec.md, Rollout and rollback).
   - Condition 1 therefore measures the node without the feature. The speed of
     the stock build itself is not measured.
2. **Q is a full node only.** Work on light nodes has stopped for now (#282), so
   the runs with a light requester are not made in this round.
3. **Q runs on `bench-2`**: not on `bench-1` as the spec says, so that provider and
   requester are on different machines.
4. **One file size, 16 MiB,** stands for the spec's "file of typical size".
5. **The SWAP block decides the result.**
   - Without SWAP, P can serve Q only about 14 chunks per second once Q's
     allowance with P is used up (spec.md, Hypothesis), whatever phase 1 does.
   - The pseudosettle block is reported, and shows the effect of that limit. The
     negative-result rules below are judged on the SWAP block.
6. **Content B is not tested with discovery inside the download.**
   - A download starts a lookup only at its 64th chunk request
     (`pkg/api/providers.go:114-119`).
   - A file the network has lost fails at its root chunk, its first request, so
     that lookup never starts.
   - Content B is therefore tested with the hint, and with a lookup in two steps:
     `GET /wasp/providers/{reference}/lookup`, then the download with the returned
     overlays as the hint.
   - The gap is filed as [#297](https://github.com/crtahlin/wasp/issues/297).
7. **Content B's first step runs condition 1 with P unreachable from Q** (see
   Content B). Otherwise P, which holds the files pinned and is connected to Q,
   could deliver them directly as an ordinary peer.
8. **The lookup-cost runs are made in the pseudosettle block only.** A lookup
   makes at most 24 chunk retrievals, spread over different chunk addresses and so
   over different peers. Each retrieval may try several peers, but the cost stays
   far below Q's free allowance with each peer, so settlement does not change it.
9. **Overdraft spills** (spec.md, Measurement, Metrics) cannot be counted
   directly. They are derived as described under "What each run records".
10. **Test content is uploaded without erasure coding** (`Swarm-Redundancy-Level:
    0`), and two extra conditions measure the default.
    - By default an upload is erasure coded at level MEDIUM
      (`pkg/file/redundancy/level.go:181`, applied to `/bytes` at
      `pkg/api/bytes.go:48`).
    - A download of such content fetches the children of each chunk through a
      redundancy decoder. The decoder's prefetch starts as soon as the decoder is
      created (`pkg/file/redundancy/getter/getter.go:81`), from a fresh background
      context that carries no preferred set (`getter.go:242`).
    - Each child goes to whichever side claims it first (`getter.go:128`): the
      prefetch, without preference, or the download's own request, with it.
    - After a decoder has succeeded, later reads of its children go through a
      fallback getter that uses the download's context
      (`pkg/file/redundancy/getter/redecoder.go:41-45`,
      `pkg/file/joiner/joiner.go:99-121`). With `Swarm-Cache: false` the prefetched
      chunks were not stored, so those reads fetch them a second time.
    - Phase 1 preference therefore reaches only part of the chunks of
      erasure-coded content, a part that depends on timing, and some chunks are
      fetched twice. Content without erasure coding never uses a decoder
      (`joiner.go:92-93`). The spec lists carrying the preferred set into the
      prefetch as phase 2 work (spec.md, Phases). Because the prefetch is on the
      default path, it is filed as
      [#299](https://github.com/crtahlin/wasp/issues/299).
    - So conditions 1 to 6 use content without erasure coding, which is what phase
      1 was built for. Conditions **2m** (no provider) and **3m** (hint to P) use
      files at the default level, three runs each in the SWAP block, to measure
      the share of chunks that reaches P and the speed.
    - Content B is tested at both levels (see Content).

## Postage

- **Batch A**: depth 20, bought by P for about 4 days. It stamps content A and all
  provider records, including the records that announce content B.
  - The runs upload about 188,000 chunks, 207,000 with a margin of 10% for
    repeated runs.
  - At depth 20 each bucket holds 16 chunks. At depth 19 it would hold 8, and
    some buckets would overflow before the runs end.
- **Batches B and B2**: depth 17, each bought by P for the contract's minimum
  validity (17,280 blocks, about 24 hours) plus 10%. B stamps content B at the
  default level and B2 content B without erasure coding.
  - With one file of about 1,100 chunks per batch, a bucket (2 chunks at depth 17)
    overflowing is unlikely, about 1 in 20 per file. If an upload reports a full
    bucket, a new batch is bought and the file uploaded again.
  - Both are left to expire.

## Content

**Content A**, one fresh file per run:
- 16 MiB of random bytes. Without erasure coding that is 4,096 data chunks,
  32 intermediate chunks and a root, 4,129 chunks in all.
- The files of conditions 2m and 3m, at the default level, split into about 4,453
  chunks. That includes 313 parity chunks and 2 root replicas, which a download
  does not normally fetch. A download needs 4,132 of them: 4,096 data chunks,
  35 intermediate chunks and the root.
- P uploads it through `POST /bytes` with `Swarm-Pin: true`,
  `Swarm-Redundancy-Level: 0` (level 1, MEDIUM, in conditions 2m and 3m) and
  batch A. It waits until the upload tag reports `synced` plus `seen` equal to
  `split`, then 60 s more. If that takes more than 20 minutes, the run is invalid:
  a chunk that cannot be synced never reaches the count.
- Q has never requested it. Q downloads it with `GET /bytes/{reference}` and
  `Swarm-Cache: false`, and checks its SHA-256 against P's copy.
- **Only the files of condition 5 are announced.** P announces each with
  `POST /wasp/providers/{reference}` and batch A after the upload has synced, and Q
  starts the download 3 minutes later. The files of the other conditions are not
  announced, so a lookup by Q finds nothing for them. The cost of that lookup is
  part of what those conditions measure.
- **Condition 6** uses a file X, and a file Xa made of the first 8 MiB of X.
  1. P uploads X unpinned, and waits for it to sync.
  2. Then P uploads Xa pinned, and waits for it to sync.

  How much of X P ends up holding:
  - Xa's data chunks are the first 2,048 data chunks of X. Its 16 intermediate
    chunks are also X's first 16, because they have the same children.
  - P deletes X's other chunks once they have synced, except the few that fall
    into its own neighborhood.
  - So P holds 2,064 of X's 4,129 chunks, about half. The share of chunks that P
    serves is reported as measured.

**Content B**, two fresh 4 MiB files, uploaded by P and pinned at the start of the
measurement:
- **B-default**: at the default level MEDIUM, with batch B: 1,120 chunks.
- **B-0**: without erasure coding, with batch B2: 1,033 chunks.
- Both are announced by P with batch A. P renews its records every window by
  itself.
- Both are tested after their batches have expired and at least 1 hour more has
  passed, so that the reserves holding them have dropped them.
- **Success is judged by bytes and SHA-256, not by HTTP status.** Once the root
  arrives, the response starts with status 200 (`pkg/api/bzz.go:825`), and a
  failure later ends the body early.
- **Expected from the code:** with a hint, B-0 arrives. B-default is expected to
  fail:
  - its root arrives from P, fetched with the download's context
    (`joiner.go:134-138`);
  - the chunks the prefetch claims are fetched without the preferred set and are
    not found. The decoder's first strategy tolerates no error
    (`getter.go:219-222`), and its fallback, which also fetches the parity
    chunks, fails because they are lost too (`getter.go:227-233`);
  - the download's waiting reads then return not found (`getter.go:109-111`,
    `165-169`), and the failed decoder is not retried (`joiner.go:74-76`);
  - this is not certain, because a prefetch request can still reach P as an
    ordinary peer.

**Lookup files:** fresh 1 MiB files, uploaded by P pinned, because a reference
must be pinned to be announced.

## Order

**P** keeps its settings throughout. **Q** runs two blocks:

1. **Pseudosettle block**: Q with `swap-enable: false`.
2. **SWAP block**: Q with `swap-enable: true`.

Within each block:
1. **Q with `providers-enable: false`:** condition 1, three runs.
2. **Q with `providers-enable: true`:**
   - three rounds, each running conditions 2, 3, 5 and 6 once, in an order rotated
     by one position per round;
   - in the SWAP block only, conditions 2m and 3m, three runs each, alternating;
   - then condition 4, three runs, with S running.

**After each change of Q's settings**, which is a restart, Q waits at least
15 minutes, and until its connected peers are back to at least 80% of the count
before the restart.

**Drift over time.** Condition 1 always runs soonest after a restart, and
conditions 2m, 3m and 4 always run last in a block. Any drift of node state over
time therefore falls on those conditions more than on the others. Results report
this next to their numbers.

**Lookup cost**, in the pseudosettle block with `providers-enable: true`:
- three lookups, through Q's `GET /wasp/providers/{reference}/lookup`, of lookup
  files that P announced 3 minutes earlier;
- three lookups of random references that nobody announced, which is the common
  case.

**Content B**, after both of its batches have expired, for each of B-0 and
B-default:
1. **The network check.**
   - Q runs with `providers-enable: false`, and every packet from Q's machine to
     P's machine is dropped, so that P cannot deliver directly.
   - Three runs. The file must not arrive. If it does, forwarding nodes still hold
     it, or P delivered it through a relay, and that file is reported as not
     testable.
2. **The delay is restored and Q reconnects to P.** The delay is checked as for a
   block, before and after this step. Then, with Q's settings of each block in
   turn (`providers-enable: true`, first without and then with SWAP), three runs
   each of:
   - the hint to P;
   - the two-step lookup, then the download with its result as the hint.

   The two-step runs are spaced more than 10 minutes apart. Lookup results,
   including empty ones, are cached per key for 10 minutes, so a closer run
   would measure the cache.

## Conditions

As in the spec, with the differences above:

| # | Q's setting | Provider for Q | Content |
|---|---|---|---|
| 1 | `providers-enable: false` | none | level 0 |
| 2 | on | none known | level 0 |
| 3 | on | hint to P | level 0 |
| 4 | on | hint to S, a stock node | level 0 |
| 5 | on | P found by discovery | level 0 |
| 6 | on | hint to P, which holds about half of the file | level 0 |
| 2m | on | none known | level 1, MEDIUM; SWAP block only |
| 3m | on | hint to P | level 1, MEDIUM; SWAP block only |

## What each run records

**Before the download:**
- Q's connected peers, storage radius and load average;
- whether P, or S in condition 4, is connected to Q.

**The download:**
- time to first byte, total time, bytes and HTTP status, from `curl -w`;
- whether the SHA-256 matches.

**During the download,** Q's retrieval logger runs at debug level. Each
successful retrieval then logs a `retrieved chunk` line naming the peer that
delivered the chunk (`pkg/retrieval/retrieval.go:353`). The logger returns to its
normal level afterwards.

**After the download**, differences in Q's metrics:
- `bee_retrieval_preferred_attempts`, `bee_retrieval_preferred_hits` and
  `bee_retrieval_preferred_misses`;
- `bee_retrieval_request_count`. It also counts the requests Q relays for others,
  so it is an upper bound for this download.

**On P:** bytes sent on its network interface during the run. These include all
of P's other traffic, so they are context, not the measure.

**Derived per run:**
- **Throughput:** bytes divided by total time.
- **Delivered by the provider:**
  - the number of `retrieved chunk` lines naming P, or S in condition 4, during the
    download;
  - it includes P's deliveries as an ordinary peer after an overdraft skip;
  - it also includes the few requests Q relays for others through P in the same
    interval;
  - it is reported next to the preferred hits, which count only the winning
    preferred attempts;
  - in conditions 2m and 3m it is reported against the 4,132 chunks a download
    needs. Some chunks are fetched twice there (difference 10), so the request
    count is reported next to it.
- **Overdraft skips** (spec's "overdraft spills"):
  - A preferred attempt is counted only after `prepareCredit` succeeds
    (`pkg/retrieval/preferred.go:224-232`), so a chunk whose provider was
    overdrawn has no attempt.
  - A run's preferred attempts can exceed 4,129, because the root is also
    requested as replicas and intermediate chunks are requested again on each
    read.
  - Overdraft skips are therefore estimated as the median preferred attempts of
    condition 3 in the SWAP block, where no overdraft is expected, minus the
    run's attempts.
  - In condition 5 the same difference also includes the chunks fetched before
    discovery found P.
- **Lost attempts:** preferred attempts minus hits minus misses. These are mostly
  slow answers that came after normal retrieval had already delivered the chunk,
  which are paid for but not used. A few are slow misses or stream errors, which
  are not paid. None is counted in a delivery figure.

**Expected limit.** Without SWAP, P can serve Q about 14 chunks per second once
Q's allowance with P is used up, more for the chunks close to it, which cost less
(spec.md, Hypothesis). In the pseudosettle block P will therefore serve only a
small share of a 16 MiB file. The SWAP block has no such limit.

**For each lookup:**
- its wall time;
- the difference in Q's request count, minus Q's background rate measured over an
  idle interval of the same length just before;
- the count the design implies: 8 slot reads, plus one record fetch per candidate
  found.

**For content B:** success or failure by bytes and SHA-256, total time, and the
deliveries by P as above.

## When a run is invalid

A run is invalid, counted, and repeated when:
- content A arrives with a status other than 200, or with a SHA-256 that does not
  match;
- its upload did not sync within 20 minutes;
- in a hinted run, the hinted node was not connected to Q at the start and one
  reconnection did not help;
- Q's connected peers have dropped below 80% of the count at the start of the
  block.

A block is invalid when the delay check after it fails.

## What a negative result is

As in the spec, judged on the SWAP block (difference 5). Any of:
- **No gain.** Conditions 3 and 5 show no gain in time to first byte or throughput
  over condition 2, beyond the spread: the spreads overlap.
- **The lookup costs more than it saves.** On a 16 MiB file, condition 5 is not
  faster than condition 2 beyond the spread.
- **Availability fails.** Content B-0 still does not arrive complete, by bytes and
  SHA-256, with a correct hint.

The first stops work on phase 2 onwards. The mechanism is still kept for
availability if content B-0 works (spec.md, Measurement). Conditions 2m and 3m and
B-default are reported as the size of the gap in #299, not judged against these
rules.

## Exploratory: provider payment-threshold sweep

**Outside the fixed method, and written before its runs.** The conditions above
showed the provider capped at 3 to 5% of a download, and a first exploratory run
with the provider at the largest threshold a node accepts raised that to about 21%
(results.md). This sweep measures how the share and the download time follow the
threshold.

- **Settings:** the SWAP block's, that is Q with `swap-enable: true` and
  `providers-enable: true`.
- **Steps:** P announces `payment-threshold` of 13,500,000 (the default),
  27,000,000, 54,000,000 and 108,000,000 (the largest a node accepts,
  `pkg/node/node.go:241`).
- **Per step:** P is restarted and settles for at least 15 minutes and until it has
  at least 100 peers. Q reconnects to P, and Q's accounting is read to confirm the
  threshold it received. Then three runs each of condition 3 (hint to P) and
  condition 2 (no provider known), alternating.
- **Records:** as for content A, in `results.tsv`, with the threshold in the block
  label.
- **Reported:** chunks delivered by P, time to first byte and total time against
  the threshold.
- **Not judged** by the negative-result rules, which stand on the fixed method.
- **P's default threshold is restored** at the end.

## Reporting

`results.md` gives, per block and condition:
- every run's raw numbers;
- the median and the spread.

Each table's caption names the build, the machine roles, mainnet, the added delay
and the block. The scripts, addresses and batch identifiers stay outside the
repository (rule 10).

Generated with help of AI.
