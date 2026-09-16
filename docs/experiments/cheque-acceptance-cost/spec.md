# Cheque acceptance cost: take the chain calls off the cheque path

Issues: [#301](https://github.com/crtahlin/wasp/issues/301) (read the issuer once)
and [#302](https://github.com/crtahlin/wasp/issues/302) (do not wait for the
liquidity check). Found through
[#300](https://github.com/crtahlin/wasp/issues/300), while measuring content
providers ([#290](https://github.com/crtahlin/wasp/issues/290)).

Status: spec for review. No code lands until this is merged.

All code references are to wasp `main` at `22f8e28f`, whose base is bee `v2.8.2`.

## Terms

- **Cheque**: a signed promise from one node to another, carrying the total the
  sender has ever promised that receiver. A later cheque replaces an earlier one.
- **Chequebook**: the contract that pays a node's cheques.
- **Accounting units**: the internal currency in which peers price chunks and
  track debt. The exchange rate to tokens comes from a contract.
- **Free allowance**: the debt two peers clear for nothing over time, 4,500,000
  units a second between full nodes.
- **Credit window**: how much one node lets a peer owe it before it refuses to
  serve: the peer's payment threshold plus at most one second of free allowance.

## Problem

A receiver makes three chain calls for every cheque
(`pkg/settlement/swap/chequebook/chequestore.go:162-195`):

1. the chequebook's issuer, above the comment "this does not change for the same
   chequebook";
2. the chequebook's balance, above the comment "basic liquidity check, could be
   omitted as it is not particularly useful";
3. what that chequebook has already paid out to this beneficiary.

Measured on the bench while measuring content providers (#290,
`docs/experiments/content-providers/results.md`):

- a chain call from the node's machine to either configured endpoint takes
  **0.10 s** (five calls each to two endpoints: 0.100 to 0.117 s, one first call
  at 0.194 s);
- accepting one cheque takes **0.28 to 0.31 s**, measured from "sending cheque
  message to peer" to "registering payment sent" in the sender's log;
- in one 16 MiB download, 9 cheques were sent to one peer, about one every 1.2 s,
  each clearing about 6,400,000 units, roughly 21 chunks;
- because a requester's credit window reopens only as debt clears, one peer
  served another at about **34 chunks a second**, where the link carried about
  2,500 chunks a second in the same test.

So the chain calls, not bandwidth, set how much one peer serves another in a short
transfer. That is what made a single content provider deliver 3 to 5% of a
download (#290).

## Hypothesis

Neither of the first two calls has to be on the path that accepts a cheque:

- **The issuer is fixed** for the life of a chequebook, as the code says. Reading
  it once per chequebook and keeping it removes one call, and weakens nothing: the
  signature is still checked against that issuer.
- **The liquidity check is a snapshot** that can be false a moment after it is
  made. What makes a cheque good to its receiver is local: the beneficiary is
  this node, the signature recovers to the issuer, and the cumulative payout is
  higher than the last one. Amortising the liquidity check over a short period
  keeps its protection in practice while taking it off the path.

Then accepting a cheque costs one protocol round trip and local work, and debt
clears roughly as fast as the peers can talk.

## Design

Both changes are inside `pkg/settlement/swap/chequebook`.

**1. Issuer, read once per chequebook (#301)**
- `chequeStore` keeps the issuer with the chequebook's record in the state store,
  under a key beside `lastReceivedChequeKey`.
- On a cheque from a chequebook with no stored issuer, the store reads it from the
  chain, as today, and stores it. The first cheque from a chequebook already pays
  for `factory.VerifyChequebook`, so this adds nothing new there.
- Later cheques from that chequebook use the stored value.
- The issuer cannot change, so the entry needs no expiry.

**2. Liquidity, checked at most once per period (#302)**
- `chequeStore` keeps, per chequebook, the balance and the paid-out total it last
  read, with the time it read them.
- Within `chequeLiquidityValidity` of that reading, the stored values are used.
  Beyond it, the store reads both from the chain, as today.
- The check itself is unchanged: the chequebook's balance must cover the cheque's
  cumulative payout minus what it has already paid out.
- The stored values are updated whenever they are read, and dropped when the node
  restarts. Nothing is written to the chain.

**Constant**

| Name | Value | Why |
|---|---|---|
| `chequeLiquidityValidity` | 30 s | Long enough that a burst of cheques costs one reading, short enough that a drained chequebook is caught quickly. Compiled in; rule 8 says measure before exposing a setting. |

**What is deliberately not changed**
- The per-chequebook lock (`chequestore.go:120-123`) still handles cheques from
  one chequebook. With the calls gone, it is held for local work only.
- Signature, beneficiary and increasing-payout checks stay exactly as they are.
- Paying more than one cheque at a time (#303) and paying larger amounts (#304)
  are separate issues, and are not part of this spec.

## What this risks

Within the validity period, a receiver can accept a cheque from a chequebook that
has been drained since the last reading.

- The exposure is what the node serves in that period. At the rate measured here,
  30 s is roughly 1,000 chunks, about 4 MB, per peer.
- The same exposure exists today between the check and the moment the cheque is
  cashed, which can be much later. The check is a guard against an obviously
  worthless cheque, not a guarantee of payment.
- A node that cares can shorten the period, and after this is measured the
  constant can become a setting.

## Protocol impact

None.
- No new protocol ID, no protobuf change, no stream header, no handshake change.
- The cheque format and the swap protocol are untouched: this changes only how a
  receiver verifies a cheque locally.
- `.github/protocol-freeze.lock` does not change, and `make protocol-freeze`
  passes.
- A node with these changes and a stock node interoperate in both directions,
  which the test plan below checks.

## Measurement

On the bench, following `docs/agent-playbooks/test-bench.md`, using the harness
built for #290: provider and requester on separate machines, 30 ms added between
them, fresh 16 MiB files, three runs per condition, median and spread reported.

**Figures, for each condition:**
- the interval from "sending cheque message to peer" to "registering payment
  sent", from the sender's debug log;
- the number of cheques in a download and what each clears;
- the chunks the provider delivers, and the download's time to first byte and
  total time.

**Conditions:**
1. **Stock**, the current code, which is the #290 result.
2. **A fast endpoint**, before any code change: a caching proxy in front of the
   node's chain endpoint that serves only calls to the paying node's chequebook
   contract. It stands in for a chain node on the same machine and shows what the
   calls cost, without changing Bee.
3. **Issuer read once** (#301).
4. **Issuer read once, and liquidity read at most once per period** (#302).

**Expected:** condition 3 is about one call faster than condition 1, condition 4
about three, and condition 4 lands near condition 2.

**A negative result** is condition 4 showing no change beyond the spread in the
cheque interval or in the provider's share. That would mean the cheque path is
bound by something else, and the changes would be reverted rather than kept.

## Rollout and rollback

- Nothing to turn on: both changes are in how a node verifies cheques it receives,
  and take effect when the node runs this build.
- An operator who wants the old behavior runs the previous build. There is no
  setting, and no state to migrate: the stored issuer and the remembered liquidity
  values are caches, rebuilt on demand.
- Nothing is written to the chain, and no existing state store entry changes
  meaning.

## Upstream portability

Both changes are local to `pkg/settlement/swap/chequebook` and touch no protocol,
so they can be adopted on their own. The measurement above is the argument for
them, and it is reproducible with any two nodes and a chain endpoint whose latency
is known.

## Configuration

None. The one constant, `chequeLiquidityValidity`, stays compiled in until the
measurement shows it matters, as rule 8 requires. If it becomes a setting:
- raising it costs a longer window in which a drained chequebook passes;
- lowering it costs more chain calls, and slower debt clearing with a busy peer;
- it costs other nodes nothing.

## Test plan

Unit tests in `package chequebook_test`:
- a second cheque from the same chequebook makes no issuer call, and a cheque from
  a different chequebook does;
- a cheque whose signature does not recover to the stored issuer is rejected;
- within the validity period, a second cheque makes no balance or paid-out call;
  after it, both are read again;
- a cheque that exceeds the remembered balance is rejected with
  `ErrBouncingCheque`;
- a first cheque from an unknown chequebook still verifies with the factory.

Node level:
- the measurement above;
- a mixed-version check: this build and a stock v2.8.2 node paying each other in
  both directions, with no blocklisting and no rejected cheques.

Generated with help of AI.
