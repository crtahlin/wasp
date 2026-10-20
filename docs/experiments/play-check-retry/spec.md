# Ask the play check again instead of skipping the round

Issue: [#773](https://github.com/crtahlin/wasp/issues/773). Related:
[#783](https://github.com/crtahlin/wasp/issues/783) (a commit request that
never gets an answer), [#772](https://github.com/crtahlin/wasp/issues/772).

## Problem

At the start of every claim phase the storage-incentives agent asks the
redistribution contract whether the node's neighbourhood is chosen for the
coming round (`isParticipatingInUpcomingRound`, called through
`redistribution.Contract.IsPlaying` from `Agent.handleSample` in
`pkg/storageincentives/agent.go`). If the call fails, `handleSample` returns
the error: the sample handler logs `phase failed ... phase=sample ...
IsPlaying: ... execution reverted` and the node does not sample. The round is
skipped even when the neighbourhood was chosen. Nothing asks again.

Measured on 20 staked mainnet nodes of one operator, about 42 hours after
2026-10-13: 82 play checks failed this way, about 2 % of all checks, on every
node (1 to 8 each). Most hit rounds the node was not chosen for. One hit a
round where the node's neighbourhood was chosen: the node did not play, and
the reveals on chain show the other nodes of that neighbourhood played. Over
three days 2 of 52 rounds that chose one of these nodes were lost, this one
and one to #783.

The revert is the contract's phase check. For one traced case, calling the
contract for the same owner and depth at the last block of the reveal phase
reverts with `WrongPhase()` (`0xe2586bcc`); at the first block of the claim
phase it returns normally. All traced failures happened in the first block of
the claim phase.

## Hypothesis

The agent decides that the claim phase has started from `BlockNumber()`, which
in `pkg/transaction/wrapped` is an estimate: the last fetched header's number
plus the blocks its average block time says have passed since. That estimate
can run a block ahead of the chain, and the endpoint that answers the
`eth_call` (at `latest`) can be a block behind the one that answered the
header request. Either way the call runs against the last block of the reveal
phase, and the contract reverts with `WrongPhase()`.

One block later the same call succeeds. Asking again after a block time, a few
times, turns almost every one of these failures into a correct answer, and a
real "not chosen" answer is unaffected because it is not an error.

## Design

All in `pkg/storageincentives`; no change to the contract binding, the
failover backend or the transaction service.

1. `Agent` keeps the `blockTime` function it already receives in `New`.
2. A new method `Agent.isPlaying(ctx, depth)` replaces the direct call in
   `handleSample`. It calls `contract.IsPlaying`. On an error that may pass
   a block later it waits one block time and asks again, up to
   `playCheckAttempts` calls in total (6, so at most 5 waits). It stops early
   when the context ends.

   Which errors are retried. The redistribution contract's ABI lists other
   errors besides `WrongPhase()`, among them `NotStaked()` and
   `MustStake2Rounds()`. Those hold for the whole round: an unstaked full node
   gets one every round, and asking again would only add calls and warning
   lines. The failover backend returns a JSON-RPC error unchanged, so the
   revert data reaches the agent as a go-ethereum `rpc.DataError`. The rule:
   - a revert whose data starts with the `WrongPhase()` selector
     (`0xe2586bcc`): retried;
   - a revert whose data starts with any other 4-byte selector: not retried,
     returned at once as today;
   - any other error, including a revert that carries no data (some providers
     strip it) and a transport error: retried, since a block later it may
     pass, and each retry is a free `eth_call`.

   The check lives in `pkg/storageincentives/redistribution` as
   `PlayCheckRetryable(err)`, next to the contract binding, so the agent
   does not parse revert data itself.
3. Every retry is counted and logged at debug level with the attempt number
   and the error. A check that succeeds after a retry is counted as recovered.
   A check that fails every attempt, or whose phase ends during a wait, is
   logged once at warning level ("play check failed after retries, skipping
   the round") with the number of attempts and the last error, counted in the
   existing `is_playing_errors` counter, and the round is skipped. It is not
   also returned as an error, so the sample handler does not log it a second
   time as `phase failed`.
4. A `false` answer is never retried: it is the contract's real answer.

Time budget. The sample handler starts at the first block of the claim phase
and may run until the end of the next commit phase: 76 + 38 = 114 blocks,
about 570 s at 5 s blocks. A sample of a doubling-2 node takes about 2
minutes on the operator's nodes. Five waits of one block cost at most 25 s,
and only when the first call failed. In the traced case the same call
succeeds one block after the one that reverted, so the expected cost is one
wait of about 5 s.

New metrics, both counters in the `storageincentives` subsystem:

| Name | Counts |
|---|---|
| `bee_storageincentives_play_check_retries` | each repeated play check |
| `bee_storageincentives_play_check_recovered` | play checks that succeeded after at least one retry |

## Protocol impact

None. The change is local to the agent's handling of a read-only contract
call. It sends no new message to peers and does not touch
`.github/protocol-freeze.lock`. On chain the node calls the same view
function, at most six times instead of once, and only when it fails.

## Measurement

On the operator's 20 staked nodes, after the change is deployed:

- `play_check_retries` rises at about the rate the failures were logged
  before (about 2 % of checks).
- `play_check_recovered` rises with it, and `is_playing_errors` stays at
  0 or close to it.
- No `phase failed ... IsPlaying` lines, and no round that chose a node's
  neighbourhood goes unplayed for this reason. The check is the operator's
  existing comparison of chosen rounds (from the chain's reveals) against the
  node's own reveals.

A negative result: `play_check_recovered` stays at 0 while retries rise, which
would mean the revert is not a one-block lag. That would show in the warning
lines with the last error, and the round would still be skipped as today.

Unit tests (written before the change):

- a contract that fails once with a revert and then answers `true`: the agent
  samples, commits and reveals;
- a contract that fails on every call: the agent does not play, makes exactly
  `playCheckAttempts` calls, logs the warning, and counts the error once;
- a contract that answers `false`: exactly one call, no retry;
- a contract that reverts with another selector (`NotStaked()`): exactly one
  call, no retry, the error returned as today;
- the classifier: `WrongPhase()` data, other selector, no data, a plain
  error and a cancelled context, each with the expected answer.

## Rollout and rollback

The behaviour is always on; there is no flag. Rolling back means running the
previous build: the node then skips a round on a failed check, as before.

## Upstream portability

Bee v2.8.2 has the same direct call in `handleSample` and the same block
number estimate in `pkg/transaction/wrapped`, so the defect is in upstream
code unmodified. Checked against `upstream/v2.8.2`: `handleSample` returns the
`IsPlaying` error and the round is skipped; wasp had not changed those lines.
The issue carries the `affects-upstream` label and has a row in
[`docs/UPSTREAM.md`](../../UPSTREAM.md). The change is a small, self-contained retry around one call
and would apply to Bee as it is.

## Configuration

No new setting. `playCheckAttempts` (6) is a constant, not a tuning dial:
the number of attempts only has to cover a lag of one or two blocks, and
there is no measurement suggesting a different value matters (AGENTS.md rule
8: measure first, expose second). Raising it would only spend more of the
sample's time budget on a check that has already failed for half a minute;
lowering it to 1 restores today's behaviour. It costs other nodes nothing:
the extra calls go to the operator's own RPC endpoints.
