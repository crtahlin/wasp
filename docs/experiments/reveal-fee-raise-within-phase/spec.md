# Raise a stuck reveal's fee within the reveal phase

Issue: [#793](https://github.com/crtahlin/wasp/issues/793) (p0, `stake-risk`).
Related: [#725](https://github.com/crtahlin/wasp/issues/725) (the Revealer),
[#734](https://github.com/crtahlin/wasp/issues/734) (replacement raise for small
values), [#772](https://github.com/crtahlin/wasp/issues/772) and
[#783](https://github.com/crtahlin/wasp/issues/783) (how the lottery's calls are
sent).

## Problem

A staked production node was selected, committed (mined), and sent its reveal
at the start of the reveal phase. The reveal's fee cap was about twice the base
fee at that moment, with a tip of a few wei. Within the next blocks the chain
filled up (blocks at 100 % of the gas limit) and the base fee rose to about
twice and then about four times its earlier value, above the reveal's fee cap.
No block could include the reveal. The node did not raise the fee: it waited for
the first send's receipt until the claim phase started. The reveal was mined 11
blocks after the reveal phase ended, the round's winner claimed, and the
contract froze the node for 155,648 blocks (about 216 hours) for committing
without revealing.

Four other nodes in the same neighbourhood revealed within the same congested
blocks, so a reveal with a higher fee cap, or a raised one, would have been
included. Three more nodes there were frozen in the same claim.

A freeze costs every round the node would have played for nine days. Every
staked wasp node is exposed, on every build since the Revealer (#725).

## Hypothesis

The Revealer already knows how to fix a stuck reveal: `revealStep` replaces a
listed reveal with raised fees when it has been pending for
`revealReplaceAfterBlocks` (6) blocks or when its fee cap is below the latest
base fee (`feeTooLow`). That code never ran in time because of the shape of the
first step:

- `revealUntil` runs each step under `stepCtx`, whose deadline is the first
  block of the claim phase.
- The first step has no listed reveal, so it calls `contract.Reveal`, which
  sends and then waits for the receipt (`sendAndWait`, `WaitForReceipt`) until
  `stepCtx` ends.
- A second step, the only place where the replacement runs, therefore starts at
  the claim phase, when the round is lost.

If each step's wait is bounded to a few blocks, the loop gets back to
`revealStep` while the reveal phase is open, sees a pending reveal whose fee cap
the base fee has overtaken, and replaces it. Replacing **before** the base fee
reaches the cap, and starting with more room above the base fee, keep the reveal
includable through a run of full blocks. With that, the reveal in the incident
would have been replaced within a few blocks of the base fee doubling and
included in the same congested blocks as the other four.

## Design

All changes are in `pkg/storageincentives/revealer.go` and, for the initial fee
cap, in `pkg/transaction`. The commit, the claim and the rest of the agent are
unchanged.

### 1. Each reveal step waits at most a few blocks

`revealUntil` derives each step's context with a deadline of
`min(claim phase start, current block + revealStepBlocks)` blocks from now,
instead of the claim phase start alone. `revealStepBlocks` is 3 (about 15 s at
5 s blocks; the reveal phase is 38 blocks).

When the step ends on that bound, the reveal is still listed (its hash is added
by the before-broadcast hook, #725), so the next step reads its status and acts:
mined (done), pending (wait, or replace, below), not found (rebroadcast or
replace), cancelled (send a new one). The bound replaces the "retry at the next
block" error log for this expected case with a debug line; a step error that is
not the step's own deadline keeps the error log.

A step whose bound falls inside a send (gas estimate, nonce, broadcast) behaves
as a send cut short does today: the stored and listed reveal is rebroadcast or
replaced by the next step (`TxNotFound` branch). With #783, a send attempt to a
silent endpoint is bounded to 10 s, so a 15 s step still leaves room for a
second endpoint; if it does not, the next step rebroadcasts.

### 2. Replace before the base fee reaches the fee cap

`feeTooLow` becomes `feeAtRisk`: a listed reveal is replaced when its fee cap is
below the latest base fee multiplied by `(9/8)^revealFeeHeadroomBlocks`, rounded
up. A full block raises the base fee by at most 12.5 %, so a fee cap below that
value can be overtaken within `revealFeeHeadroomBlocks` (3) full blocks, which
is about the time until the next step looks again. The factor for 3 blocks is
729/512, about 1.42. The check is used in both places `feeTooLow` was used
(pending and not-found reveals).

The replacement itself is unchanged: `ReplaceTransaction` keeps the nonce and
signs with the larger of a fresh suggestion (twice the current base fee plus
the boosted tip) and the old cap and tip raised by 12 %, rounded up (#734). A
fresh suggestion is about 2/1.42 = 1.4 times above the replace threshold, so a
base fee that keeps climbing causes one replacement every few blocks, and a base
fee that stops climbing causes none. A replacement that is mined is the round's
reveal; any listed reveal that is mined ends the round's steps
(`listedRevealMined`), so the round is revealed once.

### 3. The first reveal starts with more room

`transaction.TxRequest` gains `MinBaseFeeMultiple`. When it is above 0 and the
request has no fixed gas price, `prepareTransaction` raises the fee cap to at
least `MinBaseFeeMultiple` times the latest base fee plus the tip. The reveal
sets it to `revealBaseFeeMultiple` = 4. Other transactions leave it at 0 and are
unchanged (their cap stays at twice the base fee plus the tip).

**Cost.** Under EIP-1559 a transaction pays `min(fee cap, base fee + tip)` per
gas; the cap is only the most it may pay. Raising the cap changes what is paid
only when the base fee rises above the old cap, which is exactly when the old
cap would have left the reveal stuck. At the gas limit of 500,000 and a base fee
of 1,000 wei, a cap of 4 times the base fee is a reservation of 2,000,000,000
wei (0.000000002 xDAI) against the wallet balance, refunded except for the fee
paid. A cap of 4 times survives about 11 full blocks of rising base fee
(1.125^11.8 = 4), against about 6 for 2 times.

### 4. A late reveal is logged at warning level

When a round's reveal is listed but not mined and the head is within
`revealLateWarnBlocks` (3) of the claim phase, the Revealer logs one warning per
round with the round, the newest reveal's hash, its fee cap and the latest base
fee. An operator then sees a reveal that almost missed its phase, before a miss becomes a freeze.

### What stays the same

- **One reveal per round:** steps still go through `singleflight` and `stepMu`;
  a mined listed reveal ends the round's steps.
- **Same nonce for replacements:** `ReplaceTransaction` signs the stored nonce.
- **No reveal after the claim phase starts:** `revealUntil` still returns at the
  first block of the claim phase, and each step's deadline is at most that block.
- **#725 (stop wait, early reveal):** they call `revealUntil`, so they get the
  same bounded steps; their own time limits are unchanged.
- **#772 (`failOnAmbiguous`):** the reveal still returns a send error at once.
- **#783 (persistent calls):** the reveal's context keeps its mark; the bounded
  step is a deadline on that context, so attempts share it as designed.

### Commit and claim

They have the same "send, then wait for the receipt until the phase ends" shape.
A stuck commit or claim loses the round but does not freeze the stake, and
neither has a replacement loop to hand control back to. They are not changed
here; a follow-up issue covers them.

### Tests (written before the change, each must fail on `main`)

1. **Base fee overtakes the first reveal's cap:** the first reveal is pending
   and its receipt wait blocks until its context ends; the base fee rises above
   its fee cap one block into the reveal phase. On `main` no replacement is made
   before the claim phase; after the change one is made, listed, with the same
   round, inside the reveal phase.
2. **Replace before the cap is reached:** a pending listed reveal whose cap is
   above the base fee but below 729/512 of it is replaced; one at or above that
   value is not.
3. **A normally mined reveal is not replaced:** a reveal mined within the first
   step ends the round with one send and no replacement.
4. **Nothing after the claim phase:** with the reveal still pending at the last
   reveal-phase block, no send, rebroadcast or replacement happens at or after
   the first claim-phase block.
5. **Initial cap:** a request with `MinBaseFeeMultiple` 4 is signed with a fee
   cap of at least 4 times the latest base fee plus the tip; a request without it
   keeps today's cap.
6. **Late warning:** a reveal pending within 3 blocks of the claim phase is
   logged once at warning level.

The replacement keeping the nonce and raising cap and tip is already covered by
the transaction package's replacement tests; test 1 checks that the replacement
path is taken.

## Protocol impact

None. The change is in when this node signs and sends its own transactions and
with what fee; the wire surface (`.github/protocol-freeze.lock`) and the
contract calls are unchanged. A reveal with a higher fee cap is an ordinary
transaction to the network.

## Measurement

- **Unit:** tests 1 to 6 above, each mutation-checked.
- **Production:** count, over the fleet, reveals that needed a replacement
  (log line "replacing a pending reveal with raised fees"), late-reveal warnings,
  and freezes for not revealing (`StakeFrozen` events for our overlays), before
  and after the change. Before: one freeze in 59 selected rounds on 20 nodes. A
  negative result is a further freeze for a reveal mined late while the new code
  ran, which would show a remaining gap.
- **Bench:** a test-network node whose reveal is sent with a deliberately low
  fee cap still reveals within the phase. Planned, not required for the merge.

## Rollout and rollback

On by default; there is nothing to turn on. Rollback is the previous build. The
change keeps the stored state format (reveal hashes and blocks are listed as
before), so a node can move between builds between rounds.

## Upstream portability

Verified at `upstream/v2.8.2`: `handleReveal` in `pkg/storageincentives/agent.go`
calls `contract.Reveal` once with the reveal phase's context, and `sendAndWait`
waits for the receipt until that context ends. Upstream has no replacement loop
at all, so a base fee that rises above the reveal's cap (twice the base fee plus
the tip, `baseFeeMultiplier` 2 in `pkg/transaction/wrapped/fee.go`) leaves the
reveal stuck until the phase ends, with the same freeze. Upstream would need a
retry loop with fee replacement (wasp's Revealer, #725) and this bounded wait;
`MinBaseFeeMultiple` alone is a small, separate change it could take on its own.

## Configuration

Compiled-in constants; none is exposed as a setting until a measurement shows it
matters (rule 8).

| Constant | Value | Raising it costs | Lowering it costs |
|---|---|---|---|
| `revealStepBlocks` | 3 blocks | a stuck reveal is looked at less often, so a fast-rising base fee can pass its cap before the next replacement | more status reads per reveal (about 3 calls per step); below 2 blocks, a send that is still in progress can be cut short and rebroadcast |
| `revealFeeHeadroomBlocks` | 3 blocks (factor 729/512) | earlier and more frequent replacements, each a new signed transaction for the same nonce | a reveal can be overtaken between two looks, as before the change |
| `revealBaseFeeMultiple` | 4 | a larger reservation against the wallet balance (not a larger fee paid) | fewer full blocks before the first reveal is stuck |
| `revealLateWarnBlocks` | 3 blocks | more warnings for reveals that are mined in time anyway | less time between the warning and the end of the phase |

None of these costs other nodes anything: they change only this node's own
transactions.

Generated with help of AI.
