# Spec: a configurable reserve capacity

Issue: [#283](https://github.com/crtahlin/wasp/issues/283). Enables the radius
regimes of the resource-curve harness ([#279](https://github.com/crtahlin/wasp/issues/279))
on an unstaked node, and gives operators a direct capacity knob.

## Problem

The reserve capacity is the fixed constant `DefaultReserveCapacity` (1<<22 =
4,194,304 chunks), scaled only by the staking-gated `reserve-capacity-doubling`.
An operator cannot set it directly, and an unstaked node cannot change it at all.
The node's storage radius is driven by the reserve count against this capacity
(the reserve worker lowers the radius when the count is below capacity/2 with sync
idle, and raises it when over capacity), so without a settable capacity there is
no way to exercise radius behaviour on an unstaked node, and no way to size the
reserve to a disk.

## Design

- A `reserve-capacity` setting, in chunks. Unset keeps 1<<22, so an existing node
  is unchanged.
- In `pkg/node/node.go`, where `reserveCapacity` is computed, use the setting as
  the base: `reserveCapacity = (1 << reserve-capacity-doubling) * base`, where
  `base` is the setting if given, else `storer.DefaultReserveCapacity`. The
  doubling still multiplies on top, so the two compose and staked doubling is
  unaffected.
- The value flows through the existing `storer.Options.ReserveCapacity`, already
  the field the reserve reads, so the storer core is unchanged.
- Validation: reject a value below a small floor (a healthy multiple of the sample
  size, so the reserve sample still has chunks to draw from); log the effective
  capacity at startup.
- `DefaultReserveCapacity` stays a constant; the setting overrides the base at the
  node layer. No env var in the shipped feature (the throwaway `BEE_RESERVE_CAPACITY`
  env used to validate this is dropped).

## Protocol impact

None. Capacity is a local decision that sets how many chunks the node holds and
therefore its radius, which is already autonomous and needs no chain. No wire,
proof, or on-chain surface changes; not near the protocol-freeze lock. Default is
unchanged, so an upgrading node behaves identically until the operator sets it.

## Tests

- The effective capacity equals the setting when given and composes with the
  doubling; unset yields 1<<22.
- A below-floor value is rejected with a clear error.

## Configuration

- `reserve-capacity`: reserve size in chunks; default 1<<22 (4,194,304). Sizing
  the reserve to a disk, or driving radius behaviour in testing.

## Rollout and portability

Default-off (unset = today's constant), so safe to merge and ship. Self-contained
in the node and cmd layers; ports across an upstream sync without conflict. Not
intended upstream as-is (a client policy knob); no `affects-upstream` label.

Generated with help of AI.
