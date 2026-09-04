# Raise the reserve capacity doubling limit

Issues: [#17](https://github.com/crtahlin/wasp/issues/17) (measure raising it),
[#62](https://github.com/crtahlin/wasp/issues/62) (expose the limit as
configuration), [#219](https://github.com/crtahlin/wasp/issues/219) (expose the
redistribution sync-rate eligibility threshold).

The core of this spec, the pushsync receipt analysis below, was written for
[#17](https://github.com/crtahlin/wasp/issues/17) and is unchanged. Three sections
were added afterwards for the wider cluster: what an operator has to tune alongside
the limit, a second participation blocker in redistribution eligibility, and the
reward-versus-freeze economics that set a practical ceiling. They do not alter the
receipt analysis; they surround it.

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

## Filling and sampling a larger reserve: what to tweak

Raising the cap only permits a larger reserve. Filling it and then sampling it
fast enough are separate problems, and they are the ones an operator actually
feels. Reserve fill is bounded by the sync rate, not the disk: at the roughly 825
chunks per second measured in
[#26](https://github.com/crtahlin/wasp/issues/26), a default reserve takes about 85
minutes to fill, and each doubling adds roughly that again. An operator who raises the cap without also raising the sync rates gets
a reserve that fills proportionally slower and may never complete a sample in time.

Most of the knobs that address this are already configuration in wasp, each with
the stock value as its default and each gated on its own measurement, so an
operator raising the cap already has the dials to manage the result:

| Knob | Flag | Default | Issue |
|---|---|---|---|
| Per-peer pull-sync rate | `--pullsync-rate-limit` | 250 chunks/s | [#25](https://github.com/crtahlin/wasp/issues/25) |
| Global puller rate | `--puller-rate-limit` | 1000 chunks/s | [#26](https://github.com/crtahlin/wasp/issues/26) |
| Pull-sync peer recalculation interval | `--pullsync-recalc-interval` | 5 min | [#58](https://github.com/crtahlin/wasp/issues/58), [#59](https://github.com/crtahlin/wasp/issues/59) |
| Concurrent `ReserveHas` checks | `--reserve-has-concurrency` | 0 (unbounded) | [#20](https://github.com/crtahlin/wasp/issues/20) |
| Reserve wake-up (eviction scan) interval | `--reserve-wakeup-duration` | 15 min | [#58](https://github.com/crtahlin/wasp/issues/58) |
| Level 0 compaction trigger (goleveldb) | `--db-compaction-l0-trigger` | 8 | [#24](https://github.com/crtahlin/wasp/issues/24) |

Pausing pull-sync while a sample runs, so replication does not contend with the
sampler, is already in wasp and unconditional
([#23](https://github.com/crtahlin/wasp/issues/23)). So the sync and sampling
levers exist; this cluster adds only the cap itself, the receipt correctness above,
and the eligibility gate below.

## The other participation blocker: redistribution eligibility (#219)

The receipt analysis above lets a doubled node stay a well-behaved storer. A second
change is needed before it can earn, and it is unrelated to receipts.

A node only enters the redistribution draw once it considers itself fully synced.
The gate is (`pkg/node/node.go:1372`):

```go
return localStore.ReserveSize() >= reserveThreshold &&
    pullerService.SyncRate() == 0 &&
    detector.IsStabilized()
```

The `SyncRate() == 0` term is the problem for a doubled node. Covering `2^d`
neighborhoods means proportionally more chunk offers, so the sync rate rarely
settles to exactly zero. The node fills its reserve but never reports fully synced,
so it never plays. That is the negative result
[#17](https://github.com/crtahlin/wasp/issues/17) anticipated arriving by a
different route: the capacity is worthless because the node cannot participate.

Expose this as configuration with the current behavior as the default
([#219](https://github.com/crtahlin/wasp/issues/219)):

- Add `--redistribution-sync-rate-threshold` in chunks per second.
- **Default 0**, special-cased to keep the exact current gate `SyncRate() == 0`, so
  behavior is unchanged unless set.
- A positive value `t` switches the gate to `SyncRate() < (t * 2^d)`, scaling by
  the doubling since that is the axis the offer rate grows on.

This is the most consequential knob in the set, for the reason the next section
gives.

## The economics: correlated freezing and a sweet spot

Everything above keeps a doubled node correct and able to play. Whether it should
double, and by how much, is an economic question with a real ceiling, and it
belongs in the operator documentation.

The redistribution game penalizes a node that commits to a round but fails to
reveal a valid sample in time. That penalty freezes the node out of earning for a
number of rounds. (This is the redistribution freeze, a game-level penalty, and is
a different thing from the wire-compatibility freeze check in the Protocol impact
section below.)

A node with doubling `d` is one machine with one reserve covering `2^d`
neighborhoods. Each round, whichever of its neighborhoods is drawn, it must sample
that one reserve inside the round window. Because all `2^d` neighborhoods run off
the same reserve, a single overlong sample makes the node miss the round for every
one of them at once. The failures are correlated, not independent: they share one
cause, the time to sample one growing reserve. This is the shape of "when one
freezes, all freeze."

That sets a ceiling on how far raising `d` pays. Each extra doubling adds win
chances but also enlarges the reserve, which lengthens sampling, which raises the
chance of missing the window on any round. Because a miss loses the whole round
across all neighborhoods at once, past some point the growing chance of a
correlated freeze outweighs the extra win chances and expected earnings fall. There
is no single correct value: it depends on the machine's disk speed, its sync rate,
and the round timing on the network. The `--redistribution-sync-rate-threshold`
knob sits right on this tradeoff, since a wider "synced enough" band lets the node
commit while still syncing and so raises the chance of revealing an incomplete
sample.

Finding roughly where sampling starts to overrun the window on real hardware is the
economic half of the measurement below.

## Protocol impact

No wire change. `protocolName`, `protocolVersion` and the message types are
untouched, so the freeze check does not fire.

There is a behaviour change, and it is in the safe direction: this node stores
*fewer* chunks directly from pushsync and issues *fewer* shallow receipts. A
stock peer sees strictly more acceptable receipts than before, never fewer.

The node's advertised `StorageRadius` in a receipt is unchanged, still the
lowered radius, which only ever makes the uploader's second test more lenient.

The redistribution eligibility change ([#219](https://github.com/crtahlin/wasp/issues/219))
is observable on-chain through when a node commits, but its default preserves the
current strict gate exactly, so a node behaves as today unless an operator opts in.
A node that opts in and sets the threshold too high can commit an incomplete sample
and be frozen; that is the operator's risk, stated in the documentation. None of
the three changes alter `.github/protocol-freeze.lock`.

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

Alongside the receipt checks above, the economic half measures whether the node can
actually play at each doubling level:

- **Sample duration against the round window.** For each doubling level from 0
  upward, once the reserve is full, observe at least three redistribution rounds
  (rule 7) and record how long the sample takes against the commit window, and
  whether commit and reveal land in time. Stop raising the level where sampling
  first begins to overrun the window on this hardware. That level, less a margin, is
  the practical ceiling to document.
- **Fill time and peak memory** at each level, so the operator documentation can
  state what a given doubling costs to reach and hold.

**What the node needs.** The measurement requires a node with enough disk for a
doubled reserve, running the default engine, and building that reserve from an empty
data directory. A node whose reserve is already full on another engine is not a
substitute: the doubled reserve has to fill from empty on the engine under test. The
reachable doubling levels on any given machine are set by its disk headroom and its
sync rate, so those are reported with the result rather than fixed here.

## Rollout and rollback

The cap becomes configuration with 1 as the default, so no node changes what it
does on merge. Rollback is setting it back to 1.

The pushsync gate change is not configurable and applies to every node,
including those with doubling 0 — for which it is a no-op, since
`committedDepth == radius` when `d == 0`.

## Documentation to add or update

The operator-facing documentation is a first-class deliverable, not an afterthought.
Doubling is easy to turn on and easy to get wrong, and the failure mode is losing
redistribution earnings, so the risks have to be written down where an operator
setting the flag will read them.

- **A new operator guide**, `docs/reserve-capacity-doubling.md`, covering: what
  doubling does and how it changes storage radius and committed depth; that fill is
  bounded by sync rate, not disk; the full list of knobs to raise alongside it (the
  table above) and what each costs; the correlated-freeze reasoning and that there
  is a sweet spot beyond which raising the level loses money; and the ceiling found
  by the measurement on real hardware.
- **Each new flag's help text and the annotated config**
  ([#37](https://github.com/crtahlin/wasp/issues/37)) states what raising and what
  lowering it costs, per [AGENTS.md](../../../AGENTS.md) rule 8. For
  `--max-reserve-capacity-doubling`: raising needs disk and sync-rate headroom and
  risks freezing; lowering shrinks earning potential. For
  `--redistribution-sync-rate-threshold`: raising trades sample completeness for
  participation and is the direct freeze lever; lowering is safer but a doubled node
  may never qualify.
- **A short note in the push-sync or protocol-compatibility documentation** that a
  doubled node gates its pushsync store decision on committed depth and issues no
  receipt shallower than the network radius, so its receipts stay acceptable to
  stock uploaders at any doubling level.

## Order of work

1. This extended spec merged.
2. [#62](https://github.com/crtahlin/wasp/issues/62): the pushsync store gate on
   committed depth, the signed tolerance arithmetic, then the doubling limit as a
   flag with default 1. Behavior unchanged on merge.
3. [#219](https://github.com/crtahlin/wasp/issues/219): the redistribution
   sync-rate threshold as a flag with default 0. Behavior unchanged on merge.
4. [#17](https://github.com/crtahlin/wasp/issues/17): run the measurement on the
   chosen machine, find the ceiling, record it.
5. Only then, if the measurement supports it, consider whether any default should
   move. Until then every default preserves current behavior.
6. Documentation lands with the flags it describes.
