# Spec: stop the bin pruner disconnecting a static peer

Issue: [#291](https://github.com/crtahlin/wasp/issues/291). Type: fix. Area: kademlia.
Affects upstream: **yes**, decided by the reproducing test. See "Upstream" below.

## Problem

A **static peer** is one named in `--static-nodes`. The help text for that flag says it
protects those peers "from getting kicked out on bootnode"
(`cmd/bee/cmd/cmd.go:429`, and line 379 in upstream v2.8.2; an earlier version of this
spec and the issue both cited 417, which is the block sync interval). The pruner that
trims over-saturated bins honours that promise only halfway.

A **bin** is a group of peers sharing a proximity order with this node, and an
**over-saturated** bin holds more peers than the node wants to keep. The pruner then
disconnects some of them.

The counting half does exclude static peers:

```go
// pkg/topology/kademlia/kademlia.go:998-1007, binPruneCount
if po == bin && !exclude(addr) && !staticNode(addr) {
	size++
}
```

The choosing half does not. `pruneOversaturatedBins` builds its candidates from
`balancedSlotPeers` (`kademlia.go:809`), which filters on proximity alone
(`kademlia.go:848-858`), and then picks one of three ways (`kademlia.go:815-833`):

- the first candidate the health collector reports as not healthy;
- otherwise a candidate whose reachability is not public;
- otherwise one at random.

**None of the three excludes a static peer**, so all three can select one. The issue
named the random pick; the two earlier branches have the same defect and this spec
covers all three, because fixing only the random one would leave a static peer that is
merely unreachable still liable to be disconnected.

That `randomPeer` does filter static peers (`kademlia.go:1746`) is the reason to read
this as an oversight rather than a deliberate exception.

## Who this reaches

In stock Bee only bootnodes may set static nodes (`cmd/bee/cmd/start.go:283-285`), so
today it reaches bootnode operators. It matters for this fork beyond that: the
content-providers work ([#290](https://github.com/crtahlin/wasp/issues/290)) needs to
hold a provider connection open while a download is using it, and the natural way to do
that is the protection static peers are supposed to already have. Building on a
protection that does not hold would produce a download that drops its provider under bin
pressure, which is the failure this whole line of work exists to remove.

## The change

Filter static peers out of the candidate list in `pruneOversaturatedBins`, after
`balancedSlotPeers` returns and **before** the `len(peers) <= 1` guard:

```go
peers := k.balancedSlotPeers(k.commonBinPrefixes[i][j], binPeers, i)
peers = slices.DeleteFunc(peers, k.staticPeer)
if len(peers) <= 1 {
	continue
}
```

`k.staticPeer` already exists on the struct (`kademlia.go:211`, assigned at `:265`) and
is what `randomPeer` uses, so this adds no new mechanism and no new configuration.

**Filtering before the guard, not after, is the point, and the first reason is safety
rather than balance.** The random pick is `peers[rand.Intn(len(peers))]`, and
`rand.Intn(0)` panics. Filtering after the guard lets a slot whose candidates are all
static filter down to an empty list, reach that line with nothing to choose from, and
bring the node down. Filtering first guarantees at least two candidates by the time the
pick runs.

The second reason is the one about balance. A slot holding one static peer and one
ordinary peer has two candidates today. Filtering first leaves one, the guard fires, and
nothing is disconnected. Filtering afterwards would prune the ordinary peer on its own.
The conservative reading matches `binPruneCount`, which already counts as though static
peers were not there.

**Progress is still guaranteed at the shipped settings.** Slot membership partitions a
bin, and a positive prune count means more than `overSaturationPeers` ordinary peers
spread across `2^BitSuffixLength` slots. At the defaults of 18 and 4 that is at least 19
peers in 16 slots, so some slot always holds two and the pruner always has something it
may take. That guarantee depends on the numbers: this fork exposes
`--kademlia-over-saturation-peers` with no lower bound, and below 16 a bin can hold a
positive prune count with no slot holding two ordinary candidates, leaving it over its
limit. The same stall already exists upstream for a bin whose slots each hold one peer,
so this widens an existing configuration hazard rather than creating one, and it costs
held connections and nothing worse.

**`balancedSlotPeers` is left alone.** Its name describes proximity to a slot, and it
has exactly one caller, so pushing the static filter into it would hide the rule in a
function whose name does not mention it.

## What this costs

A bin whose over-saturation is made up of static peers can no longer be brought back
under its limit by pruning, because the pruner now has nothing it is allowed to
disconnect. That is the intended meaning of the flag: an operator who names more static
peers than a bin should hold has chosen to hold them. It is also already true of the
counting half, so this makes the two halves agree rather than introducing a new state.

## Upstream

The same code is in `upstream/v2.8.2`, where `balancedSlotPeers` is called at line 731 and
the `len(peers) <= 1` guard follows at 732, with no static-peer filter between them,
exactly as here.

**The test reproduced it, so the label goes on.** The condition this spec set was that
`affects-upstream` stays off until a test shows a static peer actually being
disconnected on unmodified code. It does, and not marginally: **12 rounds out of 12**.
The defect is deterministic in this scenario rather than a matter of the random pick, and
the mechanism was measured rather than guessed. The static peer is connected first, so it
sits first in its slot's candidate list, and the **first** branch fires: `Counters.Healthy`
is a plain boolean that nothing in the test records, so every peer reads as not healthy
and that branch breaks on the first candidate.

Only that branch breaks. An earlier version of this spec said "the two earlier branches
take the first candidate they match", which is **wrong about the second**: the
unreachable branch has no `break` and so keeps the **last** match, not the first. It is
never reached in this test either way.

Per rule 11 the label is a marker for a later human decision and nothing more.

## Verification

**The reproducing test comes first, and is the evidence.**

- A kademlia unit test that puts a static peer into an over-saturated bin shallower than
  the node's depth, runs the pruner, and asserts the static peer is still connected.
  On unmodified code this test must fail. That failure is what justifies the fix and the
  upstream label; without it there is only a reading of the code.
- **Per-branch coverage was required here and is not written. Corrected after
  implementing.** This spec asked for a case per branch, on the assumption that the
  repair might be made in each of the three picks. It is not: the fix removes static
  peers from the candidate list **before any of the three runs**, so there is one thing
  to test rather than three, and a fix that repaired only one branch is not a shape this
  change can take.

  What the test actually exercises, measured by instrumenting the chooser rather than
  assumed: the first branch, `!ss.Healthy`, in all 12 rounds. `Counters.Healthy` is a
  plain boolean that nothing in the test records, so every peer with a snapshot reads as
  not healthy and the branch takes the first candidate. The other two are never reached.

  That is stated here rather than quietly dropped, because a requirement removed without
  a reason is indistinguishable from one forgotten.
- The random branch needs no special care for the same reason, so the earlier requirement
  to say how it was driven does not apply.
- An ordinary peer in an over-saturated bin is still pruned, so the fix did not simply
  switch pruning off.
- The existing kademlia tests keep passing.
- A mutation: removing the filter must fail the new test.

**Corrected after implementing.** This spec also said that moving the filter after the
`len(peers) <= 1` guard "must fail the one-static-one-ordinary case". **It does not, and
the claim was wrong.** Measured: with the filter after the guard the test still passes.

The reason is that the two orderings do not differ in whether a static peer survives,
only in whether its slot-mate does. Filtering first skips a slot holding one static and
one ordinary peer, leaving both. Filtering afterwards prunes the ordinary one. Since
`binPruneCount` already counts the bin as though static peers were absent, a bin with a
positive prune count genuinely does hold too many ordinary peers, so pruning that one is
defensible rather than a defect.

Filtering before the guard is still the right order, because it keeps the slot balanced
and matches what the counting half assumes. But it is a judgement about slot balance, not
something this test distinguishes, and saying otherwise would claim coverage that does
not exist.

Per rule 7 this is code, not a bench measurement, so the discipline that applies is the
mutation check rather than three runs on a node.

## Scope

`pkg/topology/kademlia/kademlia.go` and `pkg/topology/kademlia/kademlia_test.go`. No
wire change, no configuration, no new flag. What a node does with `--static-nodes`
changes in a way an operator can observe, so `docs/DIFFERENCES.md` gains a row per
rule 13.
