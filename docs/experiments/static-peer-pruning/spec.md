# Spec: stop the bin pruner disconnecting a static peer

Issue: [#291](https://github.com/crtahlin/wasp/issues/291). Type: fix. Area: kademlia.
Affects upstream: to be decided by the reproducing test. See "Upstream" below.

## Problem

A **static peer** is one named in `--static-nodes`. The help text for that flag says it
protects those peers "from getting kicked out on bootnode"
(`cmd/bee/cmd/cmd.go:417`). The pruner that trims over-saturated bins honours that
promise only halfway.

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

**Filtering before the guard, not after, is the point.** A slot holding one static peer
and one ordinary peer has two candidates today. Filtering first leaves one, the guard
fires, and nothing is disconnected. Filtering after the guard would leave the ordinary
peer to be pruned on its own, which changes behaviour in a direction nobody asked for.
The conservative reading matches `binPruneCount`, which already counts as though static
peers were not there.

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

The same code is in `upstream/v2.8.2`. Per rule 11 the `affects-upstream` label stays
**off** until the reproducing test below actually shows a static peer being
disconnected. The issue says as much, and this spec keeps to it: the label goes on when
the test reproduces the defect on unmodified code, and not before. If the test does not
reproduce it, the reading was wrong and this spec is withdrawn rather than fixed up.

## Verification

**The reproducing test comes first, and is the evidence.**

- A kademlia unit test that puts a static peer into an over-saturated bin shallower than
  the node's depth, runs the pruner, and asserts the static peer is still connected.
  On unmodified code this test must fail. That failure is what justifies the fix and the
  upstream label; without it there is only a reading of the code.
- Because the choice has three branches, the test covers each: a static peer reported
  not healthy, a static peer whose reachability is not public, and a static peer left to
  the random pick. A test that only covers the random branch would pass on a fix that
  repaired one third of the defect.
- The random branch needs care to avoid a test that passes by luck. Either drive it with
  enough peers and repetitions that an unfixed pruner picks the static peer with near
  certainty, or assert on every disconnect the mock records rather than on one run.
  State which was used and why it is sound.
- An ordinary peer in an over-saturated bin is still pruned, so the fix did not simply
  switch pruning off.
- The existing kademlia tests keep passing.
- A mutation: removing the filter must fail the new tests; moving it after the
  `len(peers) <= 1` guard must fail the one-static-one-ordinary case.

Per rule 7 this is code, not a bench measurement, so the discipline that applies is the
mutation check rather than three runs on a node.

## Scope

`pkg/topology/kademlia/kademlia.go` and `pkg/topology/kademlia/kademlia_test.go`. No
wire change, no configuration, no new flag. What a node does with `--static-nodes`
changes in a way an operator can observe, so `docs/DIFFERENCES.md` gains a row per
rule 13.
