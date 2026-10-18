# Results: bench tests of the Bee v2.8.3 sync

Pull request: [#798](https://github.com/crtahlin/wasp/pull/798). This sync is
exempt from a spec (see `docs/agent-playbooks/upstream-sync.md`). Each risk
that v2.8.3 brought, and each upstream default it changed, got a bench test
with a pass rule fixed before the run. This page gives the result of each
test.

## Summary

- **10 of 12 tests pass.** Four of them have low statistical power, and one
  has a freeze check still owed.
- **One upstream default failed and was reverted:** the gossip broadcast
  bin size is back at 2 (Bee v2.8.3: 6). The traffic test then passed at 2.
- **One test is inconclusive:** the 250 ms pull-sync page timeout. Nothing
  failed on either build, but nothing measured the serving side. The
  timeout is still in the branch, and whether to keep it is an open
  decision.
- **Taken from v2.8.3 after their tests:**
  - the rogue-chunk migration;
  - the per-peer stream limits (128 sync, 4 cursor);
  - the 10-minute sync-rate window;
  - libp2p 0.49;
  - the 800k lottery gas minimum.
- **Kept from wasp:** the postage page of 5,000 blocks (Bee: 1,000). The
  catch-up test showed no more calls and no more rate-limit errors with it.
- **The sampler** combines wasp's structure with Bee's read buffers. On a
  real reserve it gives identical samples and is about 14 % faster in the
  realistic case.

## Set-up

- **Host:** one bench host. Timed runs never overlapped; the start and end
  of each run were logged.
- **Pre-merge comparison:** wasp `main` at `a07745812`, which is based on
  Bee v2.8.2. Some tests also used the official Bee v2.8.3 binary as a
  stock control.
- **Builds under test.** Each test ran on the branch head of its day:
  - `182c04b4c`: migration on a copy, sampler.
  - `7b689bd90`: chequebook gas fallback at 900k. Used for the migration
    at a node's first start, stream limits, page timeout, broadcast size 6,
    lottery, libp2p health and postage catch-up.
  - `3f621a701`: broadcast bin size back to 2. Used for the broadcast
    re-run.
  - `810c4270e`: `3f621a701` merged with `main` at `82c67848`, which
    carries [#803](https://github.com/crtahlin/wasp/pull/803). Used for the
    provider features and the [#802](https://github.com/crtahlin/wasp/issues/802)
    validation.
- **Why the earlier results still hold:** the commits after each test do
  not touch the code that test covers. The harness fix changes tests only,
  and the other commits change the chequebook gas, the kademlia bin size
  and the transaction service. This is read from the commit list; the
  tests were not re-run on the final head. The final head is the same
  merge as `810c4270e`, plus documentation.
- **Targets:**
  - **Migration and sampler:** a stopped copy of a mainnet reserve (pebble,
    3.77 M entries, of which 0.65 M single-owner chunks). It is about a
    quarter of the size of a full doubling-2 reserve, and it has no pinned
    or locally ingested content.
  - **Stream limits, page timeout and broadcast size:** fresh mainnet
    nodes started for each 15-minute window. Each window used a new data
    directory, which was deleted afterwards.
  - **Lottery, libp2p health, postage catch-up and provider features:** a
    staked Sepolia full node, plus throwaway Sepolia nodes.

## Results

**Table: Bee v2.8.3 sync bench tests, one bench host, merged builds vs pre-merge wasp `a07745812`, 2026-10-15 to 2026-10-18**

| Test | Measured value | Result | Default decision |
|---|---|---|---|
| Rogue-chunk migration | Reserve copy as found: 0 invalid entries, 0 removed. With planted entries: 500 invalid removed, 1,000 mistyped retyped, and the removed set equals the invalid set. A kill -9 before the migration and during the removal ended consistent both times. Speed: 260 us per single-owner chunk entry, peak RSS 352 MB. At the Sepolia node's first start (3.70 M entries): 0 removed, 4.9 s to the end of the migration, peak RSS at most 506 MB | **pass**; low power as found, because no real reserve had an entry to remove | taken from Bee |
| Sampler equivalence | 5 input sets pinned to real rounds, plus 3 repeats. In 8 of 8 pairs the items (address, transformed address, data hash, stamp) and the sample hash are identical, and every item re-hashes | **pass** | combined |
| Sampler time and memory | Realistic filter: 60.1-61.2 s against 70.4-74.1 s, about 14 % faster in every pair. Peak RSS 87-96 MB against 92-97 MB. Extreme filter (65 % of the reserve below the minimum balance): median 44.5 s against 33.8 s, with overlapping spreads | **pass**; the extreme case is **inconclusive** | - |
| Pull-sync stream limits | 9 windows with a stock v2.8.3 peer: `stream_limit_exceeded` 0, no limit or blocklist lines, no disconnects. Rate 933 chunks/s in every merged and stock window | **pass, low power**: every window ran at the puller's 1,000 chunks/s cap | Bee's 128 sync and 4 cursor streams per peer taken |
| 250 ms page timeout | No timeout or failed-page line on any node. There is no serving-side page metric, and the pre-merge pairs ran at a different storage radius | **inconclusive** | kept provisionally; open decision |
| 10-minute sync-rate window | 23.5 h of a node's synced counter replayed offline: 15-minute window fully synced 88.20 % of the time, 16 flips; 10-minute window 92.10 %, 16 flips | **pass, low power**: 8 sync events in the recording | taken from Bee |
| Gossip broadcast bin size | Bin size 6: 2.3x the hive messages and 9.6x the peer records per minute of pre-merge, against a limit of 2x. That run was confounded by radius 0 (see below). Bin size 2, single node at radius 10 on both builds: 0.58x the messages and 1.03x the records | **fail at 6, reverted; pass at 2** | **reverted to 2** |
| Lottery path and on/off switch | Sepolia: 5 commits, 5 reveals, 4 won claims. The node was switched off right after a commit; it still revealed and claimed, skipped the next round, and was switched back on. It reported "not frozen" at every later check | **pass**; one freeze check is owed (see below) | Bee's 800k lottery gas minimum taken; switch guard combined |
| libp2p 0.49 health | Sepolia, 24 h merged against a 4.7 h pre-merge baseline, an RPC outage excluded. Peers 18/18/18, 0 restarts, 0 libp2p and kademlia log lines. 0.6 error and 2.4 warning lines per hour against 0.4 and 0.0, all from the chain path | **pass, low power** | libp2p 0.49 taken |
| Provider and stampless features | Sepolia holder; 3 runs per build, 21 rows each. In 42 of 42 rows the status codes, checksums and paths are the same as each other and as the 2026-09-26 mainnet baseline | **pass** | - |
| Cold postage catch-up | 5.33 M blocks. Merged 696.9-763.2 s, pre-merge 705.5-792.5 s. Mean listener calls 2,748 against 2,757. Mean rate-limit errors 123 against 125 | **pass** | wasp's page of 5,000 kept |
| Receipt wait after a failed send ([#802](https://github.com/crtahlin/wasp/issues/802)) | HTTP 429 injected on both sends of a batch purchase: HTTP 201 in 26.4 s, each transaction mined once, 1 new batch. Details in [the #802 results](../ambiguous-send-receipt-wait/results.md) | **pass** | - |
| **Total** | 12 tests | **10 pass** (4 low power, 1 with a freeze check owed), **1 fail, reverted and passed at 2**, **1 inconclusive** | 1 default reverted, 1 kept provisionally |

## Findings beyond the pass rules

- **Radius 0 on fresh nodes, measured: a test artefact, not a regression.**
  - Two empty nodes that start in the same neighbourhood within a few
    seconds of each other each take their first storage radius from the
    other, at depth 0. They then stay at radius 0. This happens on stock
    v2.8.3 as well.
  - A fresh node started on its own reached the network radius on every
    build: 12 of 12 nodes.
  - This set-up is what confounded the first broadcast-size run. At radius
    0 every connected peer is a neighbour, so a node announces its whole
    peer list.
  - From code reading: a restarted node takes its stored radius, so running
    nodes are not exposed.
- **An endpoint that refuses a method is not counted as failed.** One
  public Sepolia endpoint began to answer HTTP 400 for
  `eth_getBlockByNumber`. The failover treated that as an answer, so the
  node stalled and would not start. This was measured on both builds, so it
  is not caused by the merge.
  [#801](https://github.com/crtahlin/wasp/issues/801) tracks it.
- **Recovery after a kill.** A kill -9 before the migration finishes makes
  the next start run sharky recovery over every chunk. That took 10 to 12
  minutes on the 3.77 M-entry copy. This start behaviour existed before
  v2.8.3.

## Not measured, or measured with low power

- **No staked mainnet node ran the merged build.** All lottery evidence is
  from Sepolia, at radius 3 to 4 with 18 peers.
- **Content the migration must keep.** The migration never ran on a node
  that holds pinned, locally ingested or provider content. Only the unit
  test `TestRemoveRogueChunksKeepsWaspContent` covers that.
- **Full-size reserve.** The migration and the sampler did not run on a
  full doubling-2 reserve of about 16.8 M entries. By proportion, the
  migration would take about 12 minutes, and a kill during it would cost
  45 to 55 minutes of recovery. These are estimates.
- **Load and scale.**
  - The pull-sync tests ran at the puller's cap.
  - The sync-rate recording saw only a few sync events.
  - The libp2p health check compared against a short baseline on a small
    network.
- **The freeze check after the one lost round.** For the one revealed round
  the node did not win, no on-chain freeze query was made. The node's own
  status said "not frozen".
- **Transactions and paths not run:**
  - the 900k chequebook gas fallback;
  - staking transactions;
  - the #803 paths that the bench run did not reach.
- **Rolling back.** Running the pre-merge build on a migrated store was not
  tried. Entries the migration removed stay removed.

## Rollout

These are the operator's steps, and none of them has been taken:

1. Merge #798, set `.upstream-base` to v2.8.3, tag, and re-enable the sync
   workflow.
2. Install the release on **one staked mainnet node first**. Restart only
   when no reveal is pending and the node is not selected, and never stop
   the node during the migration.
3. Watch that node for at least 7 days and at least 2 selected rounds:
   - the migration's counts, duration and memory;
   - radius and depth;
   - commits, reveals and freezes;
   - restarts;
   - error lines by logger;
   - peers;
   - pull-sync limit hits;
   - memory.
4. Only then roll out to the fleet, staged: one node first, then small
   groups, one restart at a time.

## Status

The sync is `validated on the bench`, with the caveats above. Decisions
still open before the release:

- whether to keep the 250 ms page timeout;
- the owed freeze check.
