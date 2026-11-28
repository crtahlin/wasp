# Wait for the contract's round before committing

Issue: [#838](https://github.com/crtahlin/wasp/issues/838). Related:
[#855](https://github.com/crtahlin/wasp/issues/855) (the same one-block lead at
the claim boundary), [#773](https://github.com/crtahlin/wasp/issues/773) (the
same lead for the play check), [#540](https://github.com/crtahlin/wasp/issues/540)
(the round check this change builds on).

## Problem

A node can send its lottery commit one block before the commit phase starts on
chain. The commit is mined in the last block of the previous round, the
contract reverts it with `CommitRoundNotStarted()`, and the node does not
commit again in that round, so it misses a round it was selected for.

Measured on mainnet with build `a53e5c07`, two staked nodes, 26 to 28 Oct 2026
(from #838): both nodes logged `entered new phase commit` at the first block of
the round and, about 6 s later, `phase failed ... CommitRoundNotStarted()`. On
chain both commits were mined at position 151 of 152 of the previous round with
status 0. Two failures in 10 days across 40 nodes on one host.

## Hypothesis

The agent enters the commit phase from its estimated block number
(`pkg/transaction/wrapped`), which can run a block ahead of the chain. Before
committing, `handleCommit` already reads the contract's round
(`contract.CurrentRound`, from #540) and tolerates a contract that is one round
behind, because the commit "would succeed once mined". That is true only if the
commit is mined after the new round starts. When the chain is still on the last
block of the previous round, a commit sent at once can be mined in that block
and revert.

The contract's answer already tells the node it is early. Waiting a block and
asking again, until the contract reports the node's round, removes the revert at
the cost of a few `eth_call`s, without spending gas.

## Design

All in `pkg/storageincentives/agent.go`.

1. **`Agent.waitForCommitRound(ctx, round)`**, called in `handleCommit` where
   `CurrentRound` is read today, before `skipBehindBlockedNonce` and the
   commit. When the contract reports exactly `round - 1` (the node one round
   ahead), it waits one block time and reads again, up to
   `commitRoundAttempts` reads (6). It returns as soon as the contract reports
   `round`, and stops when the context (the commit phase) ends.
2. **Unchanged cases.** A read error commits on the node's own round, as
   today. A difference of more than one round is `roundsDiffer`: not
   committing, as today (#540). The contract one round ahead of the node is
   left as today.
3. **When the wait runs out** (every read still reports `round - 1`), the node
   commits as today, with one warning naming the attempts. A contract that
   stays a round behind for 6 block times points to a lagging endpoint
   rather than to an early node, and the commit, sent to every endpoint, can
   still be mined in the right round.
4. **When the context ends during the wait**, the commit is not sent and the
   phase ends without an error, as for a phase that ends early.
5. **Metrics:** `CommitRoundWaits` (reads repeated) and `CommitRoundCaughtUp`
   (waits that ended with the contract on the node's round), next to
   `RoundMismatch`.

### Not changed

- The phase decision itself, and the claim boundary (#855).
- The commit send and its replacement loop (#796, #849).

### Tests (written before the change, each must fail on `main`)

1. The contract reports `round - 1`, then `round`: the commit is sent once,
   after one block time; `CommitRoundWaits` and `CommitRoundCaughtUp` are 1;
   no commit is sent before the contract reports `round`.
2. The contract reports `round` at once: the commit is sent without a wait,
   both counters 0.
3. The contract reports `round - 1` on every read: `commitRoundAttempts` reads,
   then the commit is sent with one warning.
4. The context ends during the wait: no commit is sent, no further read, no
   hang.
5. A read error: the commit is sent at once, as today.
6. A contract more than one round away: no commit, `RoundMismatch` counted, as
   today.

Each test is mutation-checked: remove the wait, the context check or the
attempt bound, and the matching test must fail with a message, not a timeout.

## Protocol impact

None. The change repeats a read-only `eth_call` and delays the node's own
commit within the commit phase. Nothing it sends to other nodes changes, and
nothing under `pkg/p2p/`, `pkg/swarm/` or `pkg/config/` is touched.

## Measurement

- **Unit:** tests 1 to 6, mutation-checked.
- **bench-1 private chain, before the merge:** the fixed build on two nodes
  and `main` on the third, at least 60 calm rounds. Pass: no
  `CommitRoundNotStarted()` revert from the fixed nodes, and
  `CommitRoundCaughtUp` above 0 on them, which shows the early entry happened
  and was waited out. The `main` node's reverts in the same rounds, if any,
  show the boundary was hit.
- **Production, after the merge:** count `CommitRoundNotStarted()` reverts and
  `CommitRoundCaughtUp` over the fleet.

## Rollout and rollback

On by default. Rollback is the previous build. No stored state changes.

## Upstream portability

Verified at `upstream/v2.8.3`: `handleCommit` has no round check at all (the
`CurrentRound` read came with #540), and it commits at once. Upstream would
need the `CurrentRound` read and this wait.

## Configuration

Compiled-in constants; none is exposed as a setting until a measurement shows
it matters (rule 8).

| Constant | Value | Raising it costs | Lowering it costs |
|---|---|---|---|
| `commitRoundAttempts` | 6 reads | more `eth_call`s, and up to 5 block times of the 38-block commit phase spent waiting when an endpoint lags | a lead of more than a few blocks still sends the commit early |

The wait costs other nodes nothing: it is a read against this node's own RPC
endpoints.

Generated with help of AI.
