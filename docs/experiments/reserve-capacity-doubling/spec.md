# Raise the reserve capacity doubling limit

Issue: [#17](https://github.com/crtahlin/wasp/issues/17)

## The question this answers

The issue asks to raise `maxAllowedDoubling` from 1 to 10, so a node can hold
1024 times the default reserve instead of twice it. It was blocked on the belief
that the limit cannot be raised unilaterally, because the same constant also
sets `shallowReceiptTolerance`.

The instruction was to find out whether local capacity can be separated from
receipt tolerance before deciding. It can, but not where it looks, and this spec
is mostly about the difference.

## What the constant actually does

`maxAllowedDoubling` appears twice, in `pkg/node/node.go`:

```go
if o.ReserveCapacityDoubling < 0 || o.ReserveCapacityDoubling > maxAllowedDoubling {
	return nil, fmt.Errorf("config reserve capacity doubling has to be between default: 0 and maximum: %d", maxAllowedDoubling)
}
shallowReceiptTolerance := maxAllowedDoubling - o.ReserveCapacityDoubling
```

The obvious decoupling is to give the cap its own constant and leave the
tolerance referenced to 1. **That is the wrong change**, and seeing why is what
makes the rest of this spec follow.

Doubling lowers the node's storage radius: `CommittedDepth() = radius +
capacityDoubling`, so a node with doubling `d` runs at radius `r_net - d` while
covering the same committed depth as everyone else. The tolerance is then used
as an offset from that lowered radius:

```go
if r >= ps.shallowReceiptTolerance {
	tolerance = r - ps.shallowReceiptTolerance
}
```

Substituting `r = r_net - d` and `shallowReceiptTolerance = maxAllowed - d`:

```
tolerance = (r_net - d) - (maxAllowed - d) = r_net - maxAllowed
```

**The `d` cancels.** The formula exists precisely so that a node's judgement of
receipts it receives stays referenced to the network radius no matter how much
it has doubled. It is not an accidental sharing of a constant, and splitting it
would break the cancellation rather than free anything.

## Where the coupling really is

`pkg/pushsync/pushsync.go`:

```go
if ps.topologyDriver.IsReachable() && swarm.Proximity(ps.address.Bytes(), chunkAddress.Bytes()) >= rad {
	stored, reason = true, "is within AOR"
	return store(ctx)
}
```

`rad` is the node's own storage radius, so **a doubling node stores pushsync
chunks all the way down to `r_net - d`**, and returns a receipt for them. The
uploader then judges that receipt:

```go
if po < tolerance || uint32(po) < receipt.StorageRadius {
	return ErrShallowReceipt
}
```

For a stock uploader, `tolerance = r_net - 1`. A receipt from the doubling node
can carry `po` as low as `r_net - d`, which fails that test for any `d > 1`.

**That is the real limit, and it explains the value.** `maxAllowedDoubling = 1`
is not a cautious round number: it is the largest doubling whose receipts a
stock uploader still accepts, because at `d = 1` the lowest possible `po` is
exactly `r_net - 1`, which is the boundary.

## The decoupling that works

Stop the doubling node from *accepting pushsync chunks* outside the network
radius, while leaving everything else about doubling alone. Gate the store
decision on committed depth rather than on the lowered radius:

```go
if ps.topologyDriver.IsReachable() && swarm.Proximity(...) >= committedDepth {
```

Since `committedDepth = radius + doubling = r_net`, every receipt this node
issues then carries `po >= r_net`, which is at or above a stock uploader's
tolerance for any doubling factor.

**The extended reserve still fills.** It is filled by pullsync, not pushsync,
and pullsync has no receipts:

```go
func (db *DB) IsWithinStorageRadius(addr swarm.Address) bool {
	return swarm.Proximity(addr.Bytes(), db.baseAddr.Bytes()) >= db.reserve.Radius()
}
```

That still uses the lowered radius, so the node keeps wanting every chunk in its
extended neighbourhood and keeps syncing it from peers.

What changes is only how a chunk in the extended area *arrives*. Today it can
arrive directly from an uploader, with a receipt the uploader will reject.
Afterwards it is forwarded to the genuinely closest node and reaches this node
shortly after by pullsync. It arrives later, not never, and the uploader's push
succeeds instead of failing.

## A hazard that the current cap is hiding

`shallowReceiptTolerance` is computed as an `int` and passed as a `uint8`. With
the cap removed and nothing else changed:

| doubling | `maxAllowed - d` | as `uint8` |
|---|---|---|
| 0 | 1 | 1 |
| 1 | 0 | 0 |
| 4 | −3 | **253** |

At 253 the guard `if r >= ps.shallowReceiptTolerance` is false for any real
radius, so `tolerance` stays 0 and **the node accepts every receipt however
shallow**. It would silently stop checking the one thing this code exists to
check.

The validation two lines above is all that prevents it today. Anyone raising the
cap without touching the arithmetic gets that, and gets it silently: no error, no
log, just a node that no longer notices shallow receipts.

So the change is three things, not one:

1. Gate the pushsync store decision on committed depth.
2. Compute the tolerance from committed depth in signed arithmetic, so it cannot
   wrap.
3. Only then raise the cap, as configuration with the current value as default.

## Protocol impact

No wire change. `protocolName`, `protocolVersion` and the message types are
untouched, so the freeze check does not fire.

There is a behaviour change, and it is in the safe direction: this node stores
*fewer* chunks directly from pushsync and issues *fewer* shallow receipts. A
stock peer sees strictly more acceptable receipts than before, never fewer.

The node's advertised `StorageRadius` in a receipt is unchanged, still the
lowered radius, which only ever makes the uploader's second test more lenient.

## Measurement

- **Shallow receipt rate**, `bee_pushsync_shallow_receipt`, on a node running
  with doubling. It should be indistinguishable from a node without doubling.
  This is the whole point of the change and it is directly instrumented already.
- **The extended reserve still fills.** Reserve size against committed depth,
  before and after, on a node with doubling greater than 1. A drop would mean
  the pullsync path does not cover what pushsync stopped taking, which would
  invalidate the argument above.
- **Uploader-side push success.** Chunks pushed by this node should not become
  more likely to be reported shallow, since its own tolerance arithmetic changes
  form but not value at `d <= 1`.

A negative result is that reserve fill slows measurably, meaning the extended
area depends on pushsync arrivals more than this spec claims. That would be a
reason to keep the cap where it is.

**None of this can be measured on bench-1 as it stands.** It needs a node with
doubling greater than 1, which needs the cap raised, and a node whose reserve is
filling, which bench-1's is not. The measurement is the same prerequisite that
parked [#23](https://github.com/crtahlin/wasp/issues/23).

## Rollout and rollback

The cap becomes configuration with 1 as the default, so no node changes what it
does on merge. Rollback is setting it back to 1.

The pushsync gate change is not configurable and applies to every node,
including those with doubling 0 — for which it is a no-op, since
`committedDepth == radius` when `d == 0`.
