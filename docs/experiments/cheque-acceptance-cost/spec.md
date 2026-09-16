# Cheque acceptance cost: take the chain calls off the cheque path

Issues: [#301](https://github.com/crtahlin/wasp/issues/301) (read the issuer once)
and [#302](https://github.com/crtahlin/wasp/issues/302) (do not wait for the
liquidity check). Found through
[#300](https://github.com/crtahlin/wasp/issues/300), while measuring content
providers ([#290](https://github.com/crtahlin/wasp/issues/290)).

Status: spec for review. No code lands until this is merged.

All code references are to wasp `main` at `22f8e28f`, whose base is bee `v2.8.2`.

The measured figures come from `docs/experiments/content-providers/results.md` on
branch `docs/290-results`, commit `04b1b2d2`. That document is not on `main` yet;
its own pull request follows the content B runs.

## Terms

- **Cheque**: a signed promise from one node to another, carrying the total the
  sender has ever promised that receiver. A later cheque replaces an earlier one.
- **Chequebook**: the contract that pays a node's cheques.
- **Accounting units**: the internal currency in which peers price chunks and
  track debt. The exchange rate to tokens comes from a contract.
- **Free allowance**: the debt two peers clear for nothing over time, 4,500,000
  units a second between full nodes (`pkg/node/node.go:232`).
- **Credit window**: how much one node lets a peer owe it before it refuses to
  serve: the peer's payment threshold plus at most one second of free allowance
  (`pkg/accounting/accounting.go:313-323`).
- **Cheque cycle**: sending a cheque, the receiver accepting it, and the sender
  registering the payment. Until it completes, the sender starts no further
  payment to that peer (`accounting.go:468-511`).

## Problem

A receiver makes three chain calls for every cheque
(`pkg/settlement/swap/chequebook/chequestore.go:162-195`):

1. the chequebook's issuer, above the comment "this does not change for the same
   chequebook";
2. the chequebook's balance, above the comment "basic liquidity check, could be
   omitted as it is not particularly useful";
3. what that chequebook has already paid out to this beneficiary.

They run while the receiver holds `chequeStore`'s mutex (`chequestore.go:52`,
taken at `:122`). That mutex is **node-wide**, not per chequebook: the comment at
`:121` says a per-chequebook lock would be enough, which is not what the code
does.

Measured on the bench (#290):

- a chain call from the node's machine to either configured endpoint takes
  **0.10 s** (five calls each to two endpoints: 0.100 to 0.117 s, one first call
  at 0.194 s);
- accepting one cheque takes **0.28 to 0.31 s**, from "sending cheque message to
  peer" to "registering payment sent" in the sender's log;
- in one 16 MiB download, 9 cheques went to one peer, about one every **1.2 s**,
  each clearing about 6,400,000 units, roughly 21 chunks;
- so one peer served another at about **34 chunks a second**: 14 from the free
  allowance and 20 from cheques. The same download pulled about 800 chunks a
  second in total, spread over roughly 120 peers.

Two costs follow:

- **Per peer.** A requester's credit window reopens only as debt clears, so one
  provider could deliver only 3 to 5% of a download (#290).
- **Per node.** With the mutex held for 0.28 to 0.31 s per cheque, a node accepts
  at most about **3 cheques a second across all its peers**, however many peers
  pay it. This is not visible in the #290 runs, which had one paying peer.

## Hypothesis

Neither of the first two calls has to be on the path that accepts a cheque:

- **The issuer is fixed** for the life of a chequebook. The contract sets it at
  construction and exposes it read-only, and the code's own comment says so.
  Reading it once per chequebook weakens nothing: the signature is still checked
  against that issuer.
- **The liquidity check is a snapshot** that can be false a moment after it is
  taken. What makes a cheque good to its receiver is local: the beneficiary is
  this node, the signature recovers to the issuer, and the cumulative payout is
  higher than the last one.

**What that is worth, in numbers.** Removing all three calls leaves local work and
one protocol round trip:

| Quantity | Now | Expected after |
|---|---|---|
| Accepting one cheque | 0.28 to 0.31 s | below 0.1 s |
| Cheque cycle to one peer | 1.2 s | about 0.9 s |
| Cheques accepted node-wide | about 3 a second | limited by local work, not the chain |
| One peer serving another | about 34 chunks a second | about 39, roughly 15% more |

The per-peer gain is small because the cheque cycle is mostly not chain calls: the
other 0.9 s is the accounting gates, the once-a-second free allowance
(`accounting.go:458-466`) and the single payment in flight per peer
(`accounting.go:468-511`). Those are #304 and #303, and they matter more than this
change for a single provider. **The gain this spec claims is the node-wide one**, and a
shorter, more predictable cheque cycle beneath whatever #303 and #304 do later.

## Design

Both changes are inside `pkg/settlement/swap/chequebook`.

**1. Issuer, read once per chequebook (#301)**
- `chequeStore` stores the issuer in the state store under its own prefix,
  `swap_chequebook_issuer_`.
  - It must not begin with `swap_chequebook_last_received_cheque_`, which
    `LastCheques` iterates, parsing an address out of every key it finds
    (`chequestore.go:226-239`). A key under that prefix would be read as a cheque.
- On a cheque from a chequebook with no stored issuer, the store reads it from the
  chain as today and stores it. The first cheque from a chequebook already pays
  for `factory.VerifyChequebook`, so nothing is added there.
- A stored entry that is missing, zero or unparseable counts as absent, and the
  issuer is read from the chain again. Without that, one corrupt entry would
  reject every later cheque from that chequebook with `ErrChequeInvalid`, because
  the code would never look again. Upstream guards the same class of corruption
  for cheques at `chequestore.go:98-102` and `:141-144`.
- The only writer is this code, after a chain read keyed by the chequebook address
  the cheque itself names, so a peer cannot cause another chequebook's issuer to
  be stored wrongly.
- The entry is permanent, one per chequebook the node has ever received from, and
  needs no expiry because the issuer cannot change.

**2. Liquidity, read at most once per period (#302)**
- `chequeStore` keeps, in memory, the balance and paid-out total it last read for
  each chequebook, with the time of that reading. They are caches, so they are
  gone after a restart and read again on demand.
- Within `chequeLiquidityValidity` of the reading, those values are used. Beyond
  it, both are read from the chain, as today.
- The check itself is unchanged: the balance must cover the cheque's cumulative
  payout minus what has already been paid out.

**What a stale reading can and cannot do**
- **This node cashing out cannot cause a wrong answer.** A cash-out of d lowers the
  balance by d and raises this beneficiary's paid-out total by d
  (`cashout.go:131-164`). The test compares the balance against payout minus
  paid-out, so both sides move by d and the errors cancel.
- **A deposit** makes the stored values pessimistic, which only rejects cheques
  that would have passed.
- **Another beneficiary cashing out, or the issuer withdrawing,** lowers the
  balance without changing this node's paid-out total. That is the case where a
  stale reading accepts a cheque the chain would refuse.

**What a rejection costs today.** A cheque that fails the check returns
`ErrBouncingCheque` (`chequestore.go:193-195`), which increments `ChequesRejected`
and ends the handler (`swap.go:95-99`, `swapprotocol.go:154`). There is no
blocklisting on that path, so a peer that has just topped up its chequebook loses
a settlement, not the connection.

**Constant**

| Name | Value | Why |
|---|---|---|
| `chequeLiquidityValidity` | 30 s | One reading serves a burst of cheques, and a chequebook that loses its funds is caught within half a minute. Compiled in; rule 8 says measure before exposing a setting. |

**Metrics.** Two counters next to the existing ones
(`pkg/settlement/swap/metrics.go:13-18`): chain reads made for cheque
verification, and chain reads avoided because a stored value was used. Without
them an operator cannot tell from the node whether this works.

**What is deliberately not changed**
- The mutex stays as it is. This spec shortens how long it is held; making it
  per chequebook is separate work.
- Signature, beneficiary and increasing-payout checks stay exactly as they are.
- Paying more than one cheque at a time (#303) and paying larger amounts (#304)
  are separate issues.

## What this risks

Within the validity period, a receiver can accept a cheque from a chequebook that
has been drained by someone else since the last reading.

- **The exposure is the validity period times what the node serves that peer in
  it.** At the rate measured today, 34 chunks a second, 30 s is about 1,000 chunks,
  4 MB. At the rate this spec expects afterwards, about 39 chunks a second, it is
  about 1,200 chunks, 4.8 MB.
- If #303 and #304 later lift the accounting gates, the same 30 s covers whatever
  the link then carries: at 800 chunks a second that is 24,000 chunks, about
  100 MB. **The constant must be revisited when those land**, and this spec says so
  rather than leaving it to be discovered.
- The same exposure already exists between the check and the moment a cheque is
  cashed, which can be much later. The check guards against an obviously worthless
  cheque; it is not a guarantee of payment.

## Protocol impact

None.
- No new protocol ID, no protobuf change, no stream header, no handshake change.
- The cheque format and the swap protocol are untouched: this changes only how a
  receiver verifies a cheque locally.
- `.github/protocol-freeze.lock` does not change, and `make protocol-freeze`
  passes.
- One peer-visible difference: within the validity period a receiver may reject a
  cheque that stock would have accepted, or accept one stock would have rejected.
  That is a behavior change, not a protocol change, and the mixed-version check
  below covers it.

## Measurement

On the bench, following `docs/agent-playbooks/test-bench.md`, with the harness
built for #290: provider and requester on separate machines, 30 ms added between
them, fresh 16 MiB files without erasure coding, three runs per condition, median
and spread reported.

**Primary figure: how long accepting a cheque takes.** From the sender's debug
log, "sending cheque message to peer" to "registering payment sent". It is the
quantity this change acts on, and its spread today is narrow, 0.28 to 0.31 s.

**Secondary figures:**
- the cheque cycle, that is the spacing between cheques to one peer;
- chunks the provider delivers, and the download's time to first byte and total
  time. The expected gain here is about 15%, which is inside the spread already
  seen for that figure, so it is reported rather than judged.

**Node-wide figure:** with three peers paying the provider at once, cheques
accepted per second across all of them, from the provider's log. This is where
the mutex is expected to show.

**Conditions:**
1. **Stock**, the current code. The #290 runs are this, for one paying peer.
2. **A fast endpoint**, before any code change: a caching proxy in front of the
   node's chain endpoint that answers only calls to the paying node's chequebook
   contract. It stands in for a chain node on the same machine and shows what the
   calls cost, without changing Bee.
3. **Issuer read once** (#301).
4. **Issuer read once, and liquidity read at most once per period** (#302).

**Expected:** condition 3 is about 0.1 s faster to accept a cheque, condition 4
about 0.3 s, and condition 4 lands near condition 2.

**A negative result** is condition 4 showing no shortening of the accept time
beyond the spread, or no rise in cheques accepted per second node-wide. Either
would mean the chain calls are not what they appear to cost, and the change would
be reverted rather than kept.

## Rollout and rollback

- Nothing to turn on: both changes are in how a node verifies the cheques it
  receives, and take effect when the node runs this build.
- An operator who wants the old behavior runs the previous build. There is no
  setting. The stored issuer entries stay behind but are only read by this code,
  and the liquidity values live in memory only.
- Nothing is written to the chain, and no existing state store entry changes
  meaning.

## Upstream portability

Both changes are local to `pkg/settlement/swap/chequebook` and touch no protocol,
so they can be adopted on their own. The measurement above is the argument for
them, and it is reproducible with two nodes and a chain endpoint of known latency.

## Configuration

None. The one constant, `chequeLiquidityValidity`, stays compiled in until the
measurement shows it matters, as rule 8 requires. If it becomes a setting:
- raising it costs a longer window in which a drained chequebook passes, and that
  window is worth more as clearing gets faster (see What this risks);
- lowering it costs more chain calls, slower clearing with a busy peer, and more
  load on the chain endpoint, which for a public one is a cost to whoever runs it;
- it costs other Swarm nodes nothing.

## Test plan

Unit tests in `package chequebook_test`:
- a second cheque from the same chequebook makes no issuer call, and one from a
  different chequebook does;
- a stored issuer that is zero or unparseable is treated as absent, and the issuer
  is read again;
- a cheque whose signature does not recover to the stored issuer is rejected;
- within the validity period a second cheque makes no balance or paid-out call,
  and after it both are read again;
- a cheque above the remembered balance is rejected with `ErrBouncingCheque`;
- a first cheque from an unknown chequebook still verifies with the factory;
- the issuer key is not picked up by `LastCheques`.

Node level:
- the measurement above;
- a mixed-version check: this build and a stock v2.8.2 node paying each other in
  both directions, with no blocklisting and no rejected cheques.

The implementation's pull request updates `docs/DIFFERENCES.md`, since a node with
this build verifies cheques differently from stock (rule 13).

Generated with help of AI.
