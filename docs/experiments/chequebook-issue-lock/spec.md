# Spec: make the cheque allocation atomic per beneficiary

Issue: [#317](https://github.com/crtahlin/wasp/issues/317). Type: fix. Area: incentives.
Affects upstream: **no, deliberately**. See "Why this carries no upstream label".

## Problem

`chequebook.Issue` allocates the next cumulative payout with a read, an addition and a
write, and holds no lock across the sequence
(`pkg/settlement/swap/chequebook/chequebook.go:190-250`):

1. read the last cheque for the beneficiary (`LastCheque`, `:198-206`);
2. add the amount to its cumulative payout (`:209`);
3. sign (`:218-226`);
4. send, through the callback (`:228-234`);
5. persist the new last issued cheque (`:236-239`).

The service does have a mutex, but it covers only the reserved total: inside
`reserveTotalIssued` and `unreserveTotalIssued`, and again at `:241-249` for
`totalIssued`. It is **not** held across steps 1 to 5.

Two calls for one beneficiary that overlap would both read the same cumulative payout at
step 1 and both send the same next value at step 4.

## It is not reachable today, and that is stated first

No caller can produce two overlapping calls for one beneficiary, checked rather than
assumed:

- **`Issue` has exactly one call path.** `grep` over `pkg/settlement` finds one:
  `swap.Pay` passes `s.chequebook.Issue` to `proto.EmitCheque`
  (`pkg/settlement/swap/swap.go:146`). There is no API endpoint and no other caller.
- **`settle` starts a payment only when `paymentOngoing` is false**, sets the flag before
  starting and clears it when the payment is reported sent. The flag is on the per-peer
  accounting record, so one peer has at most one payment in flight.
- **Two peers cannot share a beneficiary.** `Handshake` looks the beneficiary up in
  reverse first, and when it already belongs to a different peer it calls `MigratePeer`,
  which writes the beneficiary to the new peer and then deletes the old peer's entry.

So this is a latent hazard, not a live defect. Nobody is losing payments to it.

## What it would cost if it became reachable

Cheques are cumulative, and a receiver credits the difference between the new payout and
the last one it accepted, so a value that does not increase is not credited. Two cheques
carrying the same payout give:

- the receiver rejecting the second as not increasing, or crediting the difference once
  while the sender records itself as having paid twice; and
- whichever write of step 5 lands last setting `lastIssuedCheque`, which can leave the
  persisted value **lower than what was actually sent**.

The second does lasting damage. Every later cheque to that peer is computed from the
persisted value, so every later cheque repeats a payout the receiver has already seen and
none is credited. **Payment to that peer stops for good**, and nothing detects it or
recovers from it.

## The change

Serialize the allocation **per beneficiary**, not service-wide: a map of mutexes keyed by
beneficiary, taken at the top of `Issue` and released when it returns.

**Per beneficiary rather than the existing service lock.** The existing `s.lock` is
service-wide. Holding it across steps 1 to 5 would serialize every cheque this node
issues behind every other, because step 4 is a network send: on a node with a hundred and
some peers that turns independent payments into a queue. A per-beneficiary lock removes
the race without introducing that.

**The send-then-save order is preserved.** The comment at `:227` says "actually send the
check before saving to avoid double payment", which is deliberate: a send that fails must
not burn a payout value. An alternative shape, allocating and persisting before sending
and rolling back on failure, would invert that and would have to decide what to do when a
send fails after the peer has already seen it. The lock makes the existing order safe
without touching it.

**The interface gains the invariant in writing.** `Chequebook.Issue` says nothing today
about concurrent calls, which is how the hazard stayed invisible. Whether or not the lock
lands, the contract should be stated, and with the lock it becomes a guarantee the
implementation makes rather than a precondition the caller must keep.

## Why this carries no upstream label

The code is the same upstream, and the defect is equally unreachable there for the same
three reasons. Rule 11 says to tag defects rather than preferences, and to leave a
suspected problem untagged when it is reasoned from reading the code rather than
reproduced. **A hazard that no caller can reach is not a defect in the running node**, so
the label stays off, and the issue already records that.

What would change it: a caller that can overlap two `Issue` calls for one beneficiary.
[#303](https://github.com/crtahlin/wasp/issues/303) proposes exactly that, allowing more
than one payment in flight per peer, which is why this is worth fixing before rather than
after.

## Verification

Unit tests only. There is nothing to measure on a node, because the condition cannot
occur on one.

- A test driving many concurrent `Issue` calls for **one** beneficiary and asserting that
  every cumulative payout sent is distinct and strictly increasing, and that the last
  persisted value equals the highest sent. Under the race detector.
- The same for **different** beneficiaries, asserting they are not serialized, so the fix
  does not quietly become the service-wide lock it is avoiding. This needs care to avoid
  a test that passes by timing luck; state how it discriminates.
- The existing chequebook tests keep passing, including the send-failure path, which must
  still leave the persisted value untouched.
- Mutations over the whole package with `-run .`: removing the lock must fail the first
  test; replacing the per-beneficiary lock with the service lock must fail the second; a
  mutation that fails to compile is reported as a build failure rather than as caught.

## Scope

`pkg/settlement/swap/chequebook/chequebook.go` and its tests. No configuration, no wire
change, and nothing an operator can observe while the hazard remains unreachable, so
`docs/DIFFERENCES.md` gains no row. That is itself worth stating in the pull request: a
change that fixes nothing observable today needs to say so plainly rather than imply a
benefit it does not deliver.
