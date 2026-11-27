# Price lottery transactions from recent blocks, and replace commit and claim within their phase

Issues: [#796](https://github.com/crtahlin/wasp/issues/796) (commit and claim
are priced once and never repriced) and
[#846](https://github.com/crtahlin/wasp/issues/846) (the reveal's fee raise
keeps the endpoint's suggested tip). Related:
[#793](https://github.com/crtahlin/wasp/issues/793) /
[#795](https://github.com/crtahlin/wasp/pull/795) (the reveal's replacement
loop, which this spec reuses), [#845](https://github.com/crtahlin/wasp/issues/845)
(what the node does after a commit was not mined),
[#847](https://github.com/crtahlin/wasp/issues/847) (how that is logged),
[#819](https://github.com/crtahlin/wasp/issues/819) (the pending-transaction
watchdog).

## Problem

A lottery transaction has a hard deadline: a commit or reveal must be mined
within its 38-block phase, a claim within its 76-block phase. The node prices
all three the same way, in `SuggestedFeeAndTip`
(`pkg/transaction/wrapped/fee.go`):

- **tip** = the RPC endpoint's `eth_maxPriorityFeePerGas` plus
  `BoostTipPercent` (50 %), with the `minimum-gas-tip-cap` floor (default 0);
- **fee cap** = 2 x the latest base fee plus the tip (4 x for the reveal since
  #795, `RevealBaseFeeMultiple`).

On 2026-11-26 the three public Gnosis endpoints answered
`eth_maxPriorityFeePerGas` with 1, 1 and 283 wei, so the tip is 1 wei in
practice. After the send, the commit and the claim wait for their receipt
until the phase ends (`sendAndWait` in
`pkg/storageincentives/redistribution/redistribution.go`) and are never
repriced. The reveal is replaced with raised fees within its phase (#795), but
`ReplaceTransaction` asks the endpoint again and adds
`ReplacementBumpPercent` (12 %), so its tip stays near 1 wei too.

That fails in two ways, often together:

- **The tip.** When blocks are full, they are filled in order of tip, and
  sometimes even blocks with room include nothing below a few million wei. A
  1 wei tip then waits.
- **The fee cap.** In full blocks the base fee rises by up to 12.5 % per
  block, so it passes 2 x its value at the send in about 6 blocks, and no
  block can include the transaction until it falls again.

**Table: lottery transactions on staked production nodes that were broadcast
and not mined in their phase, November 2026 (measured on chain; phase block =
block % 152)**

| Round | Transaction | Priced at phase block | Tip | Fee cap | Base fee rose to | Mined | Result |
|---|---|---|---|---|---|---|---|
| 325461 | commit | 1 | 1 wei | 199 wei | 6,574 wei | phase block 125, status 0 | round not played |
| 325251 | commit | 3 | 1 wei | 8,249 wei | 24,390 wei | phase block 70, status 0 | round not played |
| 325190 | claim | 80 | 1 wei | 4.6 M wei | 26.6 M wei | two rounds later, status 0 | the win was lost |

No freeze from the reveal's tip has been observed. #846 is a risk shown by the
data: in round 325461 the 10th-percentile tip of included transactions
reached several hundred million wei, while the node's reveal had a tip of
about 35 wei after five replacements. It could not be mined anyway, because
its commit was not (#845). Had the commit been mined, the reveal would most
likely have missed its phase too (an inference, not an observation), and a
commit without a reveal freezes the stake.

### How often, over 30 days

To size the problem and choose the parameters, the per-block fee history of
Gnosis from 2026-10-27 to 2026-11-26 (518,400 blocks, 3,406 rounds) was
collected from public endpoints with `eth_feeHistory`: base fee, gas used
ratio, and the tips of included transactions at the 0th to 90th percentiles.
Each lottery transaction of every round was then replayed against it under
each pricing policy.

**The inclusion model.** A pending transaction with tip t and fee cap c is
included in block b when all three hold: c is at least b's base fee; its
effective tip, min(t, c - base fee), is at least the lowest tip b included;
and, if b is full (95 % of gas or more), the effective tip is at least b's
10th-percentile tip. The thresholds of 25 % and 50 % were run as well. This is
a model of the chain, not the chain, and it was checked against it: under
today's pricing it misses all three production cases in the table above, with
the transactions priced at the phase blocks their fee caps show. Commit and
reveal are priced 2 blocks into their phase in the runs below, the claim 4,
and the commit was also run at 1 and 3.

**Table: missed lottery transactions in 3,406 rounds under today's pricing
(Gnosis, 2026-10-27 to 2026-11-26, model above)**

| Transaction | Missed, full-block threshold p10 | p25 | p50 |
|---|---|---|---|
| Commit | 13 to 21 | 13 to 21 | 15 to 23 |
| Reveal | 5 | 5 | 5 |
| Claim | 13 | 13 | 15 |
| **Total** | **31 to 39** | **31 to 39** | **35 to 43** |

The ranges are over the commit's send block (phase block 1 or 3). That is 31 to
39 missed transactions in 3,406 rounds, roughly one per hundred rounds, for
every node selected in that round. A missed commit or claim loses the round;
each of the 5 missed reveals would freeze the stake if its commit had been
mined.

Two kinds of round produce the misses: runs of full blocks (the longest in the
30 days was 76 blocks; 70 commit phases and 70 reveal phases had 10 or more
full blocks), and a stretch of about 10 hours (rounds 324888 to 324948) in
which blocks were 10 to 80 % full but none included a tip below 4 to 10 M wei.

## Hypothesis

The endpoint's suggestion is not a usable price for a transaction with a
deadline on Gnosis: it answers 1 wei while included transactions pay millions.
What recent blocks actually included is the price, and `eth_feeHistory`
reports it. With that tip, the reveal's existing replacement loop is enough;
the commit and the claim also need a replacement loop, because a tip that is
right at the send can be overtaken within the phase.

The replay supports this, and shows which part does what:

**Table: missed lottery transactions in 3,406 rounds by part of the change
(Gnosis, 2026-10-27 to 2026-11-26, commit priced at phase block 1; full-block
threshold p10, p25 and p50 gave the same zeros)**

| Policy | Commit | Reveal | Claim | Total |
|---|---|---|---|---|
| Today | 21 | 5 | 13 | 39 |
| Tip from recent blocks only; commit and claim not replaced | 6 | 0 | 4 | 10 |
| Tip from recent blocks and a fee cap of base fee x (9/8)^6 plus the tip; commit and claim not replaced | 4 | 0 | 4 | 8 |
| Tip from recent blocks; commit and claim replaced, fee cap 2 x base fee as today | 0 | 0 | 0 | 0 |
| **Proposed (Design below): tip from recent blocks; all three with the reveal's fee cap (4 x) and replacement after 6 blocks** | **0** | **0** | **0** | **0** |

- **For the reveal (#846)** the tip alone closes the gap: its 5 misses go to 0.
- **For the commit and the claim (#796)** the tip alone is not enough; with the
  reveal's replacement loop they go to 0. A separate fee-cap formula adds
  nothing the replacement loop does not already give, so this spec does not
  introduce one.
- The result does not depend on the details: a window of 5, 20 or 50 blocks,
  with or without the 10 % margin, and replacing after 3 or 6 blocks all gave
  0 misses, at every full-block threshold and both commit send blocks.
- Upstream's pricing, where the reveal is never replaced either, misses 11
  reveals in the same rounds instead of 5.

A negative result would be a lottery transaction priced this way that is
still not mined in its phase, on a production node or in the bench check
below.

## Design

### 1. `transaction`: a tip from recent blocks

Add `FeeHistory(ctx, blockCount uint64, lastBlock *big.Int, rewardPercentiles []float64) (*ethereum.FeeHistory, error)`
to `backend.Geth` (`pkg/transaction/backend/backend.go`); go-ethereum's
`ethclient.Client` has it. Wrap it in `wrapped` (with a call and an error
counter, like the other methods), in `failover` (the usual `call`), and in the
mock, simulation and no-op backends.

Add `TxRequest.TipFromHistory bool`, stored with the transaction
(`StoredTransaction.TipFromHistory`), so a replacement prices the same way as
the original. When it is set and `GasPrice` is nil, `prepareTransaction` and
`ReplaceTransaction` take the tip as the highest of:

1. today's tip: the suggestion plus the boost, with the minimum floor;
2. the **history tip**: ask `eth_feeHistory` for the last
   `historyTipBlocks` (20) blocks at the 10th percentile, take the median of
   the 20 per-block values (all blocks, full or not), and add
   `historyTipMarginPercent` (10 %);
3. for a replacement, the stored tip plus `ReplacementBumpPercent` (as today,
   so go-ethereum's 10 % rule for replacements still holds);
4. for a replacement, also the **recent tip**: the median of the 10th-percentile
   tips of the last `recentTipBlocks` (3) blocks, plus the same margin (added
   2026-11-27, see Measurement).

The result is capped at `maxHistoryTip` (100 gwei), but never below today's
tip or the bumped stored tip, so the cap can only lower what the history adds.
The fee cap stays as today (the multiple of the base fee plus the tip, and the
bumped stored cap for a replacement).

The history is read at most once per block: the answer is kept with the
block number it was read at and reused until a new block is seen (added
2026-11-27). Without that, every `GET /redistributionstate` costs one
`eth_feeHistory` call, because the balance check behind it (part 4) prices
with the history tip; a dashboard that polls the node would multiply the
node's RPC calls.

When `eth_feeHistory` fails or returns no rewards (an endpoint without the
method, a thin history), the tip is today's, and the failure is logged at
debug level. A missing history never stops a send.

Every other transaction (postage, staking, chequebook, swap) is unchanged:
the flag is set only for lottery transactions.

### 2. Commit and claim: replaced within their phase

Today `Commit` and `Claim` send once and wait for the receipt until the phase
ends. They move to the reveal's loop shape (#795), with its constants:

- each step waits at most `revealStepBlocks` (3 blocks), then reads the
  newest sent transaction's status;
- a pending or lost transaction is replaced with raised fees, repriced as in
  part 1, once it is `revealReplaceAfterBlocks` (6) blocks old or when its fee
  cap is below the base fee that `revealFeeHeadroomBlocks` (3) full blocks can
  reach (`feeAtRisk`);
- the loop ends when one of the round's sent transactions is mined (status 1:
  done; status 0: the error, as today), or when the phase ends.

The send itself is the request as today plus `TipFromHistory`,
`MinBaseFeeMultiple: RevealBaseFeeMultiple` and `SelfReplacing: true` (the
watchdog sends it again but leaves replacing to the loop, as for the reveal).

The commit's hash is recorded before each broadcast, the first send and every
replacement, through the existing `beforeBroadcast` hook (`SetCommitTx`), so
the revealer's `commitOnChain` reads the newest commit. When an older commit
of the round is mined instead, the newest reads as cancelled, its nonce reads
as used, and the revealer reveals, as it does today for a cancelled commit.

The step loop and `feeAtRisk` move from `Revealer` into a small helper in
`pkg/storageincentives` that all three use, so the reveal's behaviour is
unchanged and is tested once.

### 3. The reveal

The reveal request gets `TipFromHistory`. Nothing else in the revealer
changes.

### 4. The balance check

`HasEnoughFundsToPlay` requires 15 x 250,000 gas at today's price (2 x the
base fee plus today's tip). At a base fee of about 100 wei that is a few
billion wei, less than one transaction at a history tip of 1 gwei (about
0.00025 xDAI on 250,000 gas). A node could then commit and not afford its
reveal, which freezes the stake. The check prices with the history tip as
well, through the same helper as part 1.

### What stays the same

- What happens to a commit or claim still pending when its phase ends is
  #845 (cancel it with a self-transfer), and how that is logged is #847. Both
  build on this spec and are not part of it.
- The watchdog (#819) is unchanged: it already leaves `SelfReplacing`
  transactions to their sender.

### Tests (written before the change, each must fail on `main`)

1. **History tip at the first send.** A backend whose suggestion is 1 wei and
   whose `eth_feeHistory` reports 10th-percentile tips of 2 M wei: a lottery
   request is sent with a tip of at least 2.2 M wei; a request without
   `TipFromHistory` keeps 1 wei.
2. **History tip at a replacement.** The same backend: `ReplaceTransaction`
   of a stored lottery transaction with a 1 wei tip sends a tip of at least
   2.2 M wei, and both the tip and the fee cap are at least 10 % above the
   stored ones.
3. **No history.** `eth_feeHistory` returns an error, and in a second case no
   rewards: the tip is today's and the send goes ahead.
4. **The ceiling.** History tips of 10^15 wei: the tip is `maxHistoryTip`; a
   stored tip already above it is still bumped by 12 %.
5. **Commit replaced within the phase.** A backend that leaves the commit
   pending: the commit is replaced after 6 blocks with a history-priced tip,
   the commit hash in the state is the replacement's, and when the replacement
   is mined the commit returns without an error before the phase ends.
6. **Claim replaced within the phase**, as test 5.
7. **Fee cap at risk.** A base fee that rises 12.5 % per block: the commit is
   replaced before its fee cap is passed, as the reveal is today.
8. **Older commit mined.** The first commit is mined after a replacement was
   sent: the loop returns done, and the revealer reveals.
9. **Balance check.** A balance that covers 15 transactions at a 1 wei tip but
   not at the history tip: the node does not play the round.
10. **One history read per block.** Twenty balance checks and two sends within
    one block make one `eth_feeHistory` call; the first read after a new
    block makes a new one.
11. **Recent tip at a replacement.** The last 20 blocks' 10th-percentile tips
    are 2 wei except the last 3, which are 1 gwei: the replacement's tip is
    at least 1.1 gwei, while the first send's tip stays at the 20-block
    median.

Each test is mutation-checked: remove the history tip, the replacement, the
hook or the balance price, and the matching test must fail with a message,
not a hang.

## Protocol impact

None. The change sets the fee fields of the node's own transactions and the
times it replaces them. Nothing it sends to other nodes changes, and nothing
under `pkg/p2p/`, `pkg/swarm/` or `pkg/config/` is touched, so
`.github/protocol-freeze.lock` is unaffected. The contract sees the same
commit, reveal and claim calls.

## Measurement

- **Replay, done (this spec).** The tables above, from 30 days of public
  `eth_feeHistory` data. The method is described in Problem and is enough to
  repeat it; any public Gnosis endpoint serves the data.
- **Unit:** tests 1 to 11, mutation-checked.
- **Code-level replay, before the merge.** A Go harness drives the real
  pricing and replacement code of this change (the transaction service and
  the phase loop) against a simulated chain fed with the recorded Gnosis fee
  history and the inclusion model above, for every phase of every round, for
  this build and for `main`. It replaces the Python reimplementation as the
  evidence that the code, not a model of it, behaves as the tables say.
- **Private chain on bench-1, before the merge.** The real contracts
  (`ethersphere/bee-localchain`, the contract version of the ABI module
  v0.9.4) on a local chain with 1 s blocks, where the test nodes are the only
  stakers: each is selected in every round and wins it, so every round of
  2.5 minutes exercises commit, reveal and claim. Three local nodes: two of
  this build, one of `main` as the control. A congestion generator replays
  recorded Gnosis blocks into the chain (filler transactions from funded test
  accounts at the recorded tips and block fullness): the commit phase of round
  325461, round 325251, the claim phase of round 325190, the bench-2 probe's
  spike at block 49488638, a stretch in which blocks have room but nothing
  below 4 to 10 M wei is included, and calm rounds. Pass: this build misses
  no commit, reveal or claim in any episode; the `main` node misses where
  production did. If the `main` node does not miss, the generator does not
  reproduce the episode, and that is fixed before any result is used. This
  measures pricing, replacement and inclusion through the real contracts; it
  does not show how Gnosis validators choose transactions, which the bench-2
  probe does.

  It replaces the Sepolia node check planned first: that node is selected in
  about 5.7 % of rounds (18 of 317 over 7 days), and an 11-hour run on
  2026-11-26 was not selected once, so it measured nothing about the lottery.
  It did show that the balance check reads the history (part 4) and led to
  the per-block read in part 1.
- **bench-2 host, Gnosis, before the merge.** Inclusion under real
  congestion, which the replay only models: from a separate probe wallet
  funded with a little xDAI (never a node's own, whose nonces the node owns),
  send a zero-value
  self-transfer every 10 blocks priced by part 1 for at least 48 hours, and
  record the blocks to inclusion and the full-block share at each send.
  Positive: every probe included within 6 blocks. Negative: a probe that
  waits longer during full blocks. One bench at a time; the two benches
  share a host. Cost: at most a few cents of xDAI.

  **First 24 hours (2026-11-26 to 27):** 757 probes, 751 included within 2
  blocks. One waited 12 blocks: it was sent at block 49488638, when the
  10th-percentile tip of the previous 20 blocks was 1 to 39,000 wei; from
  the next block the included tips jumped to 0.1 to 1 gwei for about 10
  blocks. The median of 20 blocks lags such a jump, so the replacement after
  6 blocks was priced at 76 wei and the probe waited the jump out. The
  replay reproduces it (13 blocks against 12 measured). Raising the
  percentile would raise every tip to cover a rare jump; pricing the
  replacement from the last 3 blocks as well (part 1, item 4) outbids it
  instead. In the replay that cuts the wait to 7 blocks with no new misses
  over 3,486 rounds (2026-10-27 to 2026-11-27); the highest tip rises from
  1.16 to 5.67 gwei (about 0.001 xDAI on a claim).
- **Production, after the merge.** Over the fleet, count lottery transactions
  not mined in their phase (#847 makes them visible in the log), commits and
  claims that needed a replacement, and `StakeFrozen` events, against the
  rounds before. Today's rate in the replay is about 1 % of rounds.

## Rollout and rollback

On by default for lottery transactions; there is nothing to turn on. Rollback
is the previous build. The stored transaction gets one new field, which an
older build ignores and a newer build reads as false when missing (the same
JSON encoding `SelfReplacing` used in #819), so a node can move between
builds between rounds. There is no setting to turn the history tip off; one
is added only if a measurement shows it is needed (rule 8).

## Upstream portability

Verified at `upstream/v2.8.3`: `pkg/transaction/wrapped/fee.go` is identical
(the tip from `SuggestGasTipCap` plus the boost, the fee cap 2 x the base fee
plus the tip), and `Commit`, `Reveal` and `Claim` each call `sendAndWait`,
which waits for the receipt with no replacement at all. In the replay,
upstream misses the same commits and claims as wasp does today, and more
reveals, because it never replaces them: 11 against wasp's 5 (#725, #793).

What upstream would need, in order of size:

1. `FeeHistory` on its backend interface and the history tip for the three
   lottery transactions, a small change. In the replay it cuts upstream's
   misses by about two thirds, but not to zero: reveals from 11 to 4, commits
   from 13 to 23 down to 2 to 9, claims from 13 to 15 down to 4 to 5.
2. A bounded replacement loop for its lottery transactions, which takes the
   rest to zero in the replay. Upstream has none for any transaction; wasp's
   revealer (#725, #795) is the model.

## Configuration

Compiled-in constants. None is exposed as a setting until a measurement shows
it matters (rule 8).

| Constant | Value | Raising it costs | Lowering it costs |
|---|---|---|---|
| `historyTipBlocks` | 20 blocks | a slower reaction to a change in what blocks include; one larger `eth_feeHistory` answer per send | a tip that follows a single block's outlier more closely (5 blocks still gave no misses in the replay) |
| `historyTipPercentile` | 10th | a higher tip in every round, paid also when blocks have room | a tip that a full block more often leaves out |
| `recentTipBlocks` | 3 blocks | a replacement that follows a sudden rise more slowly | a replacement that follows one block's outlier; 1 block gave the same result as 3 in the replay |
| `historyTipMarginPercent` | 10 % | a slightly higher tip | none measured: 0 % also gave no misses |
| `maxHistoryTip` | 100 gwei | a larger worst-case tip if the history is wrong (100 gwei on a 450,000-gas claim is about 0.045 xDAI) | a ceiling below what congested blocks include; the highest tip the proposed policy paid in the replay was 1.16 gwei |
| `revealStepBlocks`, `revealReplaceAfterBlocks`, `revealFeeHeadroomBlocks` | 3, 6, 3 blocks | as in the #795 spec, now for commit and claim too | as in the #795 spec |

**Cost.** In the replay the proposed pricing raised the mean fee per
transaction from 0.0000002 to 0.0000003 xDAI for the commit, 0.0000002 to
0.0000005 for the reveal, and 0.0000005 to 0.0000009 for the claim: about
0.000001 xDAI more per selected round. The highest tip it paid was 1.16 gwei.

**What it costs other nodes.** Nothing directly: other nodes' transactions are
not changed. In a full block a higher tip from this node can push out a
lower-tipped transaction of another sender, which is how the fee market
works for every sender that prices this way. Nodes running stock Bee keep
pricing from the endpoint's suggestion (most lottery transactions in a sample
of two rounds on 2026-11-26 paid 150 wei), and in full blocks they keep
missing (see Upstream portability).

Generated with help of AI.
