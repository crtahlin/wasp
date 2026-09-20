# Counting a provider connect that needed no dial

Issue: [#382](https://github.com/crtahlin/wasp/issues/382).
Type: fix.

**This spec does not change `pkg/p2p`.** An earlier draft did. Two reviews
showed that the `pkg/p2p` change is three behavior changes rather than one, that
it does not deliver the thing it was wanted for, and that the bench observation
offered as evidence for it does not support it. All three are recorded below,
because each one is a correction to something already written down and one of
them is already merged on `main`.

## Problem

`bee_providers_connects_dialed` and `bee_providers_connects_already_connected`
are supposed to separate a provider connect that opened a connection from one
that found the peer already there. They do not. The second counter can only rise
when `p2p.Service.Connect` returns `p2p.ErrAlreadyConnected`, and that error is
decided by matching the **remote address** of an open connection, not by asking
whether the peer is connected at all.

`pkg/p2p/libp2p/libp2p.go:1066-1074`:

```go
remoteAddr := addr.Decapsulate(hostAddr)

if overlay, found := s.peers.isConnected(info.ID, remoteAddr); found {
	address = &bzz.Address{
		Overlay:   overlay,
		Underlays: []ma.Multiaddr{addr},
	}
	return address, p2p.ErrAlreadyConnected
}
```

`isConnected` (`pkg/p2p/libp2p/peer.go:185-212`) requires **both** the peer ID
in `r.overlays` **and** a connection in `r.connections[peerID]` whose
`RemoteMultiaddr()` compares equal to `remoteAddr`. A connection to the same
peer on another underlay satisfies the first and fails the second, so the
function returns false and `Connect` falls through to the dial path.

What follows is not a second transport connection. `s.host.Connect` returns
`nil` without dialing when the peer is already connected, checked in the pinned
dependency rather than recalled: `go-libp2p v0.48.0`,
`p2p/host/basic/basic_host.go:538-547`, where addresses are absorbed into the
peerstore and the early return is taken when the connectedness is already
`Connected`, unless `forceDirect` is set, which Bee never does (`grep -rn
ForceDirectDial pkg cmd` finds nothing). What does happen is a
**new handshake stream** on the existing connection (`libp2p.go:1131`), a
`peerMultiaddrs` exchange, and a full `handshakeService.Handshake` round trip
(`:1156`). Only then does `addIfNotExists` (`peer.go:141`) notice the overlay is
already registered and return `exists == true`, and the branch handling that,
`libp2p.go:1191`, closes the handshake stream and returns `i.BzzAddress, nil`, a
plain success with no error.

So the provider adapter never reaches its `ErrAlreadyConnected` branch
(`pkg/node/providers.go:101-108`). It sees a nil error, falls through the
overlay guard and the `kad.Connected` call, and returns `false, nil` at `:120`,
which `countConnect` records as a dial.

### Why the addresses differ in the first place

Only on the **discovery** path. A provider record is built by `Options.Address`
(`pkg/node/providers.go:76-96`), which filters this node's underlays to public
ones and caps them at `providers.MaxUnderlays`, which is 4
(`pkg/providers/keys.go:31`). Kademlia may hold a connection on a private
address that no record ever carries.

The **hinted** path is different and this matters, because an earlier draft of
this spec proposed measuring the defect on it. `ConnectHints` resolves each
overlay through `s.opts.Resolve` (`pkg/providers/providers.go:435`), which is
`book.Get` (`pkg/node/providers.go:124-126`), the same address book kademlia
dials from. The addresses therefore agree, `isConnected` matches, and the defect
does not reproduce there at all.

## Correction: the bench run is not evidence of this

[#382](https://github.com/crtahlin/wasp/issues/382) carries a section headed
"Measured, not reasoned about", and
[discovery-lifetime-results.md](discovery-lifetime-results.md) draws the same
conclusion in a document already merged on `main`. Both are **withdrawn**. The
observation was:

| | |
|---|---|
| provider's overlay first appears in `/peers` | 1.2 s after the response |
| `bee_providers_connects_dialed` rises | 1.4 s after the response |
| `bee_providers_connects_already_connected` | 0 |

and the conclusion drawn was that the peer was already connected when the
provider service's `Connect` ran, so a connect over an open connection was
counted as a dial.

That does not follow. The ordering in the code makes the same observation the
**expected signature of a completely correct provider dial**:

1. `/peers` is `s.p2p.Peers()` (`pkg/api/peer.go:100-103`), which reads the
   libp2p peer registry.
2. The registry entry for a new peer is created by `addIfNotExists`
   (`peer.go:141-162`, writing `r.overlays[peerID]`), reached on the outbound
   path from `libp2p.go:1191`, **inside** `Connect` and before it returns.
3. `ConnectsDialed.Inc()` runs in `countConnect`
   (`pkg/providers/providers.go:387-392`), called at `:376`, **after**
   `s.opts.Connect` has returned, and later still here because
   `pkg/node/providers.go:116` calls `kad.Connected` first.

**The gap between those two points is several network round trips, not a
moment.** An earlier version of this correction said "microseconds apart", which
is wrong by orders of magnitude and is worth stating properly, because the size
of the gap is exactly what makes the correct-dial explanation sufficient. In
between lie `handshakeStream.FullClose` (`libp2p.go:1201`, which waits on the
remote), a `putHandshakeAddress` statestore write (`:1210`), the whole
`ConnectOut` notifier loop (`:1218-1226`) whose handlers include `pricing.init`
(`pkg/pricing/pricing.go:115-126`) sending a payment threshold over a stream,
and `pseudosettle.init` and `swapprotocol.init`; then, in the adapter,
`kad.Connected` reaching `onConnected` and `Announce`, which blocks on
`BroadcastPeers` (`kademlia.go:1221`). A 0.2 second separation between the two
samples is entirely ordinary for that path.

**The ordering does not rule the defect out either, and saying it did was the
same overreach in the opposite direction.** `addIfNotExists` has more than one
caller: `libp2p.go:661`, in the inbound handshake stream handler, runs on a
libp2p goroutine with no `Connect` on the stack, and `Connect` itself is called
by kademlia's dial path and by `POST /connect` (`pkg/api/peer.go:34`). Under any
of those the overlay can appear in `/peers` with no relation to the provider
service, and a later provider connect on a different underlay would then fall
through and count a dial, which is the defect signature.

So the correct conclusion is neither that the defect was observed nor that it
was excluded: **the run is ambiguous, and those two observables cannot separate
the cases.** The same table also records the provider's overlay as **absent**
from `/peers` at the response instant for that run, which is the opposite of
already connected, and that should have been noticed at the time.

### The consequence for #369's own acceptance criterion

This is larger than one paragraph being wrong, and it reaches a **merged** spec.
[discovery-lifetime.md](discovery-lifetime.md) requires, for arm 2, that "the
overlay's **first appearance in `/peers` is at or after the second in which
`ConnectsDialed` rose**", justified by "if the overlay appears first, kademlia
got there and discovery only observed it."

Point 2 above says a genuine discovery dial writes the overlay **before** the
counter rises, always, because one happens inside the call the other measures.
So that criterion is satisfied only when the two land in the same sampling
bucket, and fails whenever they do not. It is **systematically unsatisfiable by
the behavior it exists to confirm**, and it was the reason run 1 was read as a
failure at all. It is withdrawn in that document by this change.

**What the defect actually rests on** is reading `isConnected`, which is
deterministic and not in doubt, plus the unit reproduction below. Rule 11 asks
for measured or reproduced rather than reasoned, so the reproduction is the
part that earns the claim, and it ships with this change rather than being
promised.

## Correction: fixing `pkg/p2p` would not have unblocked #369

The stated reason for the `pkg/p2p` change was that
[#369](https://github.com/crtahlin/wasp/issues/369) arms 2 and 6 cannot be
settled while this holds. That is not right either, but the reason has to be
stated carefully, because a first version of this section got it wrong in a way
worth naming.

That version said runs 2 and 3 of arm 2 are inconclusive because the dial
completed inside the request, "and a correct fix would still show
`connects_dialed` rising in them, because at the instant `Connect` ran the peer
genuinely was not connected". **That is an unsupported causal claim of exactly
the kind being retracted two sections above, from a less resolved observation.**
For runs 2 and 3 the table records the peer present at the response, first seen
at 0.2 s, and the dial counter already risen at the response. Both events fall
in the first bucket, and the results document itself says that when events land
in the same bucket nothing can be ordered. Whether the peer was connected when
`Connect` ran is precisely what those runs cannot say.

The correct statement rests on reading the code rather than on those runs.
`Discover`'s connect runs in a goroutine bounded by `discoverBound()` and not by
the request (`providers.go:340`, `:413`), so it can and does complete before the
response ends; when it does, the counters have already moved by the time any
instant-of-response reading is taken, and no amount of later sampling recovers
the order. That is independent of how already-connected is decided.

So fixing the address keying is **necessary but not sufficient** for those arms.
What they also need is a pair of nodes that do not reconnect to each other on
their own, which the results document already says and which this bench cannot
provide. Runs 2 and 3 are **unresolved**, not evidence for either side.

## Hypothesis

The question the counters exist to answer, "did this provider connect open a
connection", can be answered correctly in the adapter that owns those counters,
without changing what any connect does. The `pkg/p2p` behavior is a separate
defect with separate consequences and belongs in a separate change.

## Design

In `pkg/node/providers.go`, read whether the provider's overlay is already
connected **immediately before** calling `p2ps.Connect`, and use that to
classify the outcome. Do not short-circuit the connect.

```go
Connect: func(ctx context.Context, addr *bzz.Address) (bool, error) {
	// Whether this connect opened a connection cannot be read from
	// p2p.Connect's result: ErrAlreadyConnected is decided per remote
	// address, so a peer connected on another underlay returns a plain
	// success. Classify from the peer set instead, and let the connect
	// run exactly as it did before.
	wasConnected := connectedOverlay(p2ps, addr.Overlay)

	got, err := p2ps.Connect(ctx, addr.Underlays)
	if errors.Is(err, p2p.ErrAlreadyConnected) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !got.Overlay.Equal(addr.Overlay) {
		_ = p2ps.Disconnect(got.Overlay, "provider overlay mismatch")
		return false, errProviderOverlay
	}
	if err := kad.Connected(ctx, p2p.Peer{Address: got.Overlay, FullNode: true}, false); err != nil {
		_ = p2ps.Disconnect(got.Overlay, "provider not accepted by topology")
		return false, fmt.Errorf("topology: %w", err)
	}
	return wasConnected, nil
},
```

`connectedOverlay` scans `p2ps.Peers()`, which is already on the `p2p.Service`
interface (`pkg/p2p/p2p.go:67`), so no interface gains a method. It is linear in
the peer count, a few hundred at most, and runs once per provider connect, which
is bounded by `lookupCandidates` at 16 (`pkg/providers/providers.go:35`).

### Making it testable, which the code is not today

Five of the six arms below are unwritable against the code as it stands, and
that has to be part of the design rather than discovered during implementation.
`newProvidersService` (`pkg/node/providers.go:37-52`) takes `p2ps
*libp2p.Service` and `kad *kademlia.Kad`, both concrete structs with unexported
state, so no fake can be supplied. The closure is then stored in
`providers.Options` behind `Service.opts`, which is unexported, so a test cannot
reach it even if the service could be built. Every existing `pkg/node` test
covers a pure helper; nothing wired is tested in that package today.

So the closure body moves into a named unexported function:

```go
// providerConnect performs one provider connect and reports whether this node
// was already connected to that peer before the connect ran.
func providerConnect(
	ctx context.Context,
	p2ps p2p.Service,
	notifier p2p.PickyNotifier,
	kadConnected func(context.Context, p2p.Peer, bool) error,
	addr *bzz.Address,
) (bool, error)
```

taking the two dependencies as the narrowest shapes that work rather than as
concrete types, with the closure reduced to a call into it. `*libp2p.Service`
already satisfies `p2p.Service` (`pkg/p2p/libp2p/libp2p.go:1323` provides
`Peers()`), and the single call site at `pkg/node/node.go:1596` passes the same
values, so the change compiles unchanged there. The function is exported for
tests through `pkg/node/export_test.go`, which already exists and already
exports four helpers this way.

The fakes needed exist: `pkg/p2p/mock` has `WithConnectFunc` (`:41`),
`WithPeersFunc` (`:55`), `Peers()` (`:137`) and `Disconnect` (`:118`), and
`pkg/topology/mock` has a `Connected` (`:104`) with the right signature.

This refactor is the precondition for arms 2 to 6 and is listed in Files below.
Without it the arms are aspirational, which is the shape of failure this
repository keeps producing.

**What this changes:** the value of one boolean that feeds two counters.
`p2ps.Connect` is called with the same arguments, in the same place, and every
error path, the overlay guard and the `kad.Connected` call are untouched. The
`ErrAlreadyConnected` branch stays, because when it does fire it is correct and
returning early there is today's behavior.

**The race, stated rather than hidden.** Kademlia can connect the peer between
the `Peers()` read and the moment `Connect` decides, and then a connection this
node did not open is counted as a dial. The window is the few microseconds
between two adjacent statements rather than the 0.2 second sampling interval an
external before-and-after comparison would have, which is what the #369
measurement tried. It is not zero, and the counter is a diagnostic rather than
an accounting record, so a rare miscount is acceptable where a systematic one is
not. No design here removes it without changing `pkg/p2p`.

### Why not change `pkg/p2p`, which is where the defect is

An earlier draft replaced the address-keyed check in `Connect` with a peer-keyed
one. Review found three behavior changes rather than the one it claimed, and
they are the reason that change is not made here:

1. **Kademlia stops announcing a peer it re-dials.** `kademlia.go:1099` returns
   on `ErrAlreadyConnected` **before** `k.detector.Record()` (`:1149`) and
   `k.Announce(ctx, peer, true)` (`:1151`). This one is near-unreachable in
   practice, since `connectBalanced` and `connectNeighbours` skip peers already
   held (`kademlia.go:343`, `:387`), so it needs a race between selection and
   dial. The first draft gave it the most space of the three, which was the
   wrong emphasis.
2. **The provider adapter stops notifying topology.** Today the different
   underlay case falls through to `kad.Connected` at `pkg/node/providers.go:116`
   and so reaches `onConnected` (`kademlia.go:1326-1339`), running `Announce`,
   `connectedPeers.Add`, `waitNext.Remove`, `recalcDepth` and
   `detector.Record()`. Under the peer-keyed check the early return at `:107`
   happens first and none of it runs. This is reachable on every provider
   connect to an already-connected peer, which is the case the change is about.
3. **`POST /connect/{multiaddr}` changes from 200 to 500.** `pkg/api/peer.go:34`
   does not test for `ErrAlreadyConnected` and turns any non-nil error into an
   HTTP 500. Connecting to a peer already connected on a different underlay
   returns 200 today and would return 500, skipping the topology notification.
   Nothing in `pkg/api` tests this: `grep -rn "AlreadyConnected" pkg/api/`
   finds nothing, so the change would ship untested.

Against that, the benefit is removing one redundant handshake per occurrence.
That may well be worth having, but it is a behavior change to shared connection
handling that deserves its own issue, its own measurement of how often the path
is actually taken, and tests for all three consequences. Rule 8's order applies
by analogy: measure that the redundant handshakes matter, then change them.

#382 stays open and keeps `affects-upstream` as the marker for that later
decision. Per rule 11 the label authorizes nothing further, and rule 1 governs
contact with ethersphere without exception.

### Scope against upstream

Checked with `git diff upstream/v2.8.2`, not assumed. Both
`pkg/p2p/libp2p/libp2p.go` and `pkg/p2p/libp2p/peer.go` **are** modified in this
fork: `peer.go` adds `count()`, and `libp2p.go` adds the zero-peer breaker
bypass for [#74](https://github.com/crtahlin/wasp/issues/74) and the extended
user agent. The issue's claim that "neither is modified in this fork" is wrong
as written and is corrected there. What is true is narrower: `isConnected`, the
call site at `libp2p.go:1068`, and the duplicate branch at `libp2p.go:1191` are
byte-identical to that tag, which is what the label needs.

`pkg/node/providers.go` is fork-authored and has no upstream counterpart, so the
change this spec does make has no upstream scope at all.

## Protocol impact

**None, and this draft does not touch `pkg/p2p` at all.** The change is confined
to `pkg/node/providers.go`, which is fork-authored. Nothing in
`.github/protocol-freeze.lock` is involved: no stream name, no protocol name or
version, no handshake field number, no chunk geometry, no `NetworkID`. No
message is sent, changed or withheld, so no peer can observe the difference.
`make protocol-freeze` passes unchanged and the `protocol-change` label is not
applied. `docs/agent-playbooks/protocol-compatibility.md` was read before the
earlier draft, when the change was in `pkg/p2p`.

## Measurement

**This change cannot be settled on the bench, and no bench time is requested.**
That is a conclusion from the evidence above rather than a convenience: the
counters are node-wide and unlabelled, the provider connect runs in a background
goroutine that outlives the request (`providers.go:340`, `:413`), the two nodes
reconnect to each other faster than a request ends, and the dial completes
inside the request in most runs. Four bench arms in the first draft of this spec
were defeated by those four facts between them. The acceptance weight sits on
unit tests, which is appropriate for a change whose whole claim is that one
boolean takes the right value.

Every arm below states the mutation it catches, and a mutation with no arm is
listed as such rather than left out. That is the standing lesson from #299,
where the counter's headline property had no test and the mutation table omitted
exactly the two mutations that mattered.

**Arm 1, the defect reproduced.** In `pkg/p2p/libp2p/connections_test.go`,
connect from A to B on one of B's underlays, then connect again on a **second,
distinct** underlay. Assert the second call returns a **nil** error, with
`expectPeers` showing a single peer.

This arm asserts current behavior, which is unusual and deliberate: it is the
reproduction rule 11 asks for, it is what makes the `affects-upstream` claim
something other than reading, and it pins the behavior so that a later
`pkg/p2p` change has to update it and cannot land silently. The comment on the
test says exactly that.

Two existing tests are the near neighbours and neither covers this:
`TestDoubleConnect` (`connections_test.go:319`, asserting at `:338`) passes the
full underlay slice twice, and `TestDoubleConnectOnAllAddresses` (`:458`,
asserting at `:484`) builds a fresh dialer per address and reconnects on the
**same** address. Both assert `ErrAlreadyConnected` and both are unaffected by
this change.

The number of underlays a test service has is a property of the host's network
interfaces, not of the `":0"` listen address (`libp2p_test.go:58`), so the arm
reads `s.Addresses()` and skips with a stated reason when it holds fewer than
two. A skip that fires everywhere is a test that never runs, so the
implementation reports which CI platforms skipped, and if all of them do, the
fallback is a second multiaddr spelling of the same endpoint, `/dns4/localhost`
against `/ip4/127.0.0.1`, which survives `filterSupportedAddresses` because
`bzz.ClassifyTransport` (`pkg/bzz/transport.go:70-81`) tests websocket and TLS
first and then falls to TCP, which a `dns4` address carrying `/tcp` reaches.

That fallback is a **weaker** reproduction and is not equivalent to the arm
proper: it differs only in how the same endpoint is spelled, so it exercises the
address equality compare in `isConnected` without the peer genuinely being
reachable on two underlays. It is a fallback for a platform that cannot run the
real thing, not a substitute for it.

*Mutation:* none, this arm pins existing behavior.

**Arm 2, already connected: the counter is right AND nothing is skipped.** With
a fake `p2p.Service` whose `Peers()` reports the provider's overlay and whose
`Connect` returns success with a nil error, `providerConnect` returns `true`,
**and the fake's `Connect` was invoked, and the `kadConnected` function was
invoked.** All three assertions are required.

The last two are not padding, and leaving them out is the defect a review found
in the first version of this arm. Without them the following passes every arm
here:

```go
if connectedOverlay(p2ps, addr.Overlay) {
	return true, nil
}
```

That returns the right boolean and skips both `p2ps.Connect` and
`kad.Connected`, which is consequence 2 in the section above, the one this whole
design exists to avoid, and it contradicts "Do not short-circuit the connect" in
the Design section. An arm that cannot reject the single implementation the
design most wants to exclude is not an arm.

*Mutations caught:* return `false` unconditionally; classify from `err` alone as
today; short-circuit before `p2ps.Connect`; short-circuit before
`kad.Connected`. Today's code fails this arm on the second, which is the point.

**Arm 3, not connected: the counter is right.** Same fake, with `Peers()`
reporting **one unrelated peer** rather than an empty set, and the provider's
overlay absent. `providerConnect` returns `false`.

The peer set is non-empty deliberately. With an empty set, an implementation
whose overlay comparison matches any peer at all would still return `false`
here and the mutation would go uncaught.

*Mutations caught:* return `true` unconditionally, which is the sensitivity
control for arm 2; and an overlay comparison that matches any peer rather than
the requested one.

**Arm 4, the classification is read before the connect, not after.** The fake's
`Connect` adds the overlay to what `Peers()` subsequently reports. The adapter
must still return `false`, because the peer was not connected when it was asked.

*Mutation:* move the `Peers()` read to after `p2ps.Connect` returns. Arms 2 and
3 both pass under that mutation; only this one fails. It is the arm for the
ordering the whole design rests on.

**Arm 5, the overlay guard still fires.** The fake returns success with an
overlay different from the record's. `providerConnect` returns
`errProviderOverlay`, calls the fake's `Disconnect` with the overlay it got, and
does **not** call `kadConnected`.

An earlier version of this arm also required that the adapter "does not report a
connect of either kind". That assertion cannot be made here: reporting is
decided by `countConnect` (`pkg/providers/providers.go:387-406`) from the error,
not by the adapter, so there is nothing in a `pkg/node` test to assert it
against. It is true by construction and has been removed rather than left as an
arm that asserts nothing.

*Mutations caught:* return `wasConnected` before the overlay check; drop the
`Disconnect`. This arm exists because the first draft's `pkg/p2p` change would
have widened exactly this hole and no arm in that draft tested it.

**Arm 6, the `ErrAlreadyConnected` branch is unchanged.** The fake returns that
error; the adapter returns `true` with no error and does not call
`kad.Connected`.

*Mutation:* delete the branch. Catches a refactor that folds it into the new
classification and changes the early-return behavior with it.

**Known gap with no arm, stated rather than omitted.** The race in the Design
section, kademlia connecting the peer between the `Peers()` read and the moment
`Connect` decides, is not covered by any arm and cannot be: reproducing it needs
the two statements to interleave with a real dial, and a fake that forces the
interleaving would be asserting the test's own scheduling rather than the code's
behavior. It is a known and accepted miscount, argued in the Design section, not
something the arms establish. Listing a mutation and quietly not covering it is
how the #299 table went wrong, so it is named here instead.

`make test-race` covers `pkg/node` and `pkg/p2p/...` in the same pass.

**What a negative result looks like.** There is no measured quantity here to
come back negative. The arms pass or fail. What would falsify the premise is
arm 1 returning `ErrAlreadyConnected` rather than nil, which would mean the
defect does not exist as described and this change is unnecessary; in that case
the counters were already right and the issue closes as not a defect.

## Rollout and rollback

No configuration, and rule 8 does not apply because no tuning constant is
involved. Nothing on disk, no migration, no peer state, no operator action. The
only visible difference is that two counters that were already published take
different values in a case where one of them was wrong.

Rollback is `git revert` of the merge commit.

## Upstream portability

The change is in fork-authored code with no upstream counterpart, so there is
nothing for Ethersphere to adopt from it.

The **defect** is upstream's, and what would be portable is the argument rather
than a diff: that `isConnected` decides per address while every caller reads the
result as per peer, and that changing it costs a lost kademlia announce, a lost
topology notification on any similar caller, and an HTTP status change on
`POST /connect`. That analysis is in this spec and in #382, which keeps the
`affects-upstream` label as a marker for a later human decision and nothing
more.

## Configuration

None. See "Rollout and rollback".

## Files

Implementation, on `fix/382-provider-connect-counted` after this spec merges:

- `pkg/node/providers.go`, the `providerConnect` function extracted from the
  closure, taking its two dependencies as interfaces, the `connectedOverlay`
  helper, the classification, and the stale comment in the `ErrAlreadyConnected`
  branch which describes the behavior this change stops relying on.
- `pkg/node/export_test.go`, exporting `providerConnect` for tests, alongside
  the four helpers it already exports.
- `pkg/node/providers_test.go`, arms 2 to 6, using `pkg/p2p/mock` and
  `pkg/topology/mock`.
- `pkg/p2p/libp2p/connections_test.go`, arm 1. Not `libp2p_test.go`, which holds
  only helpers.
- `docs/experiments/content-providers/discovery-lifetime-results.md`, retracting
  the causal reading of arm 2 run 1, and the claim about runs 2 and 3. Merged on
  `main`.
- `docs/experiments/content-providers/discovery-lifetime.md`, withdrawing arm
  2's ordering criterion, which the section above shows a correct discovery dial
  cannot satisfy. Also merged on `main`, and the larger of the two corrections.
- `docs/DIFFERENCES.md`, the counter's description, and the header commit line,
  which still names `f5fd2b51` while `origin/main` is further on. `origin/main`
  rather than `main` per rule 13: the local `main` in this worktree is itself
  behind, which is the exact trap that rule describes.

No bench harness, because there is no bench arm.
