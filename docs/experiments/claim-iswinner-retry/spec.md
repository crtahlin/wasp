# Ask the winner check again instead of losing the claim

Issue: [#855](https://github.com/crtahlin/wasp/issues/855). Related:
[#773](https://github.com/crtahlin/wasp/issues/773) (the same boundary for the
play check, fixed by `play-check-retry`),
[#838](https://github.com/crtahlin/wasp/issues/838) (the same boundary for the
commit), [#836](https://github.com/crtahlin/wasp/issues/836) (a lost round
logged as a failed claim phase).

## Problem

At the start of the claim phase `Agent.handleClaim`
(`pkg/storageincentives/agent.go`) asks the redistribution contract whether
the node won (`isWinner`, through `redistribution.Contract.IsWinner`). If the
call fails, `handleClaim` returns the error and the claim handler does not run
again in that round, although the claim phase lasts 76 blocks. A winner that
gets an error here never claims, and its reward moves to a later round.

Measured on the bench-1 private chain (anvil, 1 s blocks, the real
Redistribution contract, three staked nodes, 163 rounds on 2026-11-27):

- 17 of 137 won claims were never sent, on both builds (13 of 91 on the
  lottery fee-pricing build, 4 of 46 on `main`).
- Each of the 17 nodes entered the claim phase at the first claim block and,
  59 to 114 ms later, logged `IsWinner: ... execution reverted: custom error
  0x6f118d82`. `0x6f118d82` is the selector of `NotClaimPhase()`.
- The chain's own log puts each reverted `eth_call` after the last reveal block
  was mined and before the first claim block was mined.
- Of 106 claim-phase entries at the first claim block, 53 reverted this way; of
  383 entries at later blocks, none did.

Not measured: how often this happens on Gnosis with 5 s blocks. The code path
is the same.

## Hypothesis

The agent decides that the claim phase has started from `BlockNumber()`, which
in `pkg/transaction/wrapped` is an estimate: the last fetched header's number
plus the blocks its average block time says have passed since. Header
timestamps are whole seconds and earlier than the moment the block is mined,
so the estimate can reach the first claim block before the chain has it, and
the `eth_call` at `latest` runs on the last reveal block. The contract reverts
with `NotClaimPhase()`. One block later the same call succeeds.

This is the play check's failure (#773) at the same boundary, and the play
check's fix applies: ask again after a block time. A real "not a winner"
answer is not an error and is unaffected.

## Design

All in `pkg/storageincentives` and its `redistribution` subpackage; no change
to the transaction service, the failover backend or the contract binding.

1. **One retry rule for both checks.** `redistribution.PlayCheckRetryable`
   becomes a thin wrapper over a shared helper,
   `revertRetryable(err, selector)`, with the same rules: retryable when the
   revert data starts with `selector`, or when the error carries no revert
   data (a transport error); not retryable on any other selector, or on a
   cancelled or expired context. A new `redistribution.WinnerCheckRetryable`
   wraps it with the `NotClaimPhase()` selector. `PlayCheckRetryable`'s
   behaviour does not change.
2. **`Agent.isWinner(ctx)`** replaces the direct call in `handleClaim`, shaped
   like `Agent.isPlaying`: on a retryable error it waits one block time and
   asks again, up to `winnerCheckAttempts` calls (6, as the play check). It
   stops when the context (the claim phase) ends. A `true` or `false` answer
   is returned at once. A non-retryable error, such as `AlreadyClaimed()`, is
   returned at once as today. When every attempt fails, the last error is
   returned as today, counted in `ErrWinner`, with one warning naming the
   attempts.
3. **Metrics:** `WinnerCheckRetries` (checks asked again) and
   `WinnerCheckRecovered` (checks that succeeded after a retry), next to the
   play check's.
4. **Readable errors.** When `IsWinner` or `IsPlaying` returns a revert whose
   selector is one of the Redistribution ABI's errors, the returned error names
   it (for example `NotClaimPhase()`) in addition to the selector, so the log
   line is readable. Matching uses the ABI the contract binding already holds.

### Not changed

- The phase decision itself. Waiting for a fresh chain header before every
  phase would cover #838 as well, but it changes every phase's timing; it is
  left to #838.
- The claim send and its replacement loop (#796, #849).

### Tests (written before the change, each must fail on `main`)

1. `IsWinner` returns a revert with the `NotClaimPhase()` selector, then
   `true`: the node claims once in the same claim phase, `ErrWinner` is not
   counted, `WinnerCheckRecovered` is 1.
2. A revert with another selector (`AlreadyClaimed()`): no retry, the error is
   returned and counted as today.
3. An error without revert data, then `false`: one retry, then "not a winner",
   no claim.
4. Every attempt fails: `winnerCheckAttempts` calls, then the error is returned
   and counted once.
5. The context ends during the wait: the loop returns without a further call.
6. `WinnerCheckRetryable` and `PlayCheckRetryable` over a table of errors
   (each selector, other selectors, short data, no data, transport error,
   cancelled context); the play check's table must be unchanged.
7. The error of a `NotClaimPhase()` revert names `NotClaimPhase()`.

Each test is mutation-checked: remove the retry, the selector match or the
context check, and the matching test must fail with a message, not a hang.

## Protocol impact

None. The change only repeats a read-only `eth_call` within the claim phase.
Nothing the node sends to other nodes changes, and nothing under `pkg/p2p/`,
`pkg/swarm/` or `pkg/config/` is touched, so `.github/protocol-freeze.lock` is
unaffected.

## Measurement

- **Unit:** tests 1 to 7, mutation-checked.
- **bench-1 private chain, before the merge:** the same setup as the run on
  2026-11-27, with the fixed build on two nodes and `main` on the third, for at
  least 60 calm rounds. Pass: no won claim on the fixed build is left unsent,
  and its logs show `NotClaimPhase()` reverts followed by a successful check a
  block later (`WinnerCheckRecovered` above 0). The `main` node keeps losing
  claims at the first claim block, which shows the boundary was hit.
- **Production, after the merge:** count `WinnerCheckRetries` and
  `WinnerCheckRecovered` over the fleet; a recovered check on Gnosis shows the
  failure happens there too.

## Rollout and rollback

On by default; there is nothing to turn on. Rollback is the previous build. No
stored state changes.

## Upstream portability

Verified at `upstream/v2.8.3`: `handleClaim` calls `IsWinner` once and returns
on any error, and `phaseCheck` decides the phase from the same estimated block
number. Upstream needs the retry in `handleClaim` and the selector match; it
has neither the play-check retry of #773 nor the helper, so it would take both.

## Configuration

Compiled-in constants; none is exposed as a setting until a measurement shows
it matters (rule 8).

| Constant | Value | Raising it costs | Lowering it costs |
|---|---|---|---|
| `winnerCheckAttempts` | 6 calls | more `eth_call`s when the error does not pass; at most 5 block times of the 76-block claim phase spent waiting | a lead of more than a few blocks, or a lagging endpoint, still loses the claim |

The retry costs other nodes nothing: it is a read against this node's own RPC
endpoints.

Generated with help of AI.
