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
`p2p/host/basic/basic_host.go:538-546`, where addresses are absorbed into the
peerstore and the early return is guarded only by `forceDirect`, which Bee never
sets (`grep -rn ForceDirectDial pkg cmd` finds nothing). What does happen is a
**new handshake stream** on the existing connection (`libp2p.go:1131`), a
`peerMultiaddrs` exchange, and a full `handshakeService.Handshake` round trip
(`:1156`). Only then does `addIfNotExists` (`peer.go:141`) notice the overlay is
already registered and return `exists == true`, and the branch handling that,
`libp2p.go:1191`, closes the handshake stream and returns `i.BzzAddress, nil`, a
plain success with no error.

So the provider adapter at `pkg/node/providers.go:100-106` sees a nil error, and
reports a dial.

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

That does not follow, and the ordering in the code is what rules it out:

1. `/peers` is `s.p2p.Peers()` (`pkg/api/peer.go:100-103`), which reads the
   libp2p peer registry.
2. The registry entry for a new peer is created by `addIfNotExists`
   (`peer.go:141-162`, writing `r.overlays[peerID]`), called from
   `libp2p.go:1191`, **inside** `Connect` and a few lines before it returns.
3. `ConnectsDialed.Inc()` runs in `countConnect`
   (`pkg/providers/providers.go:387-392`), called at `:376`, **after**
   `s.opts.Connect` has returned, and for the provider adapter later still,
   because `pkg/node/providers.go:115` calls `kad.Connected` first.

So "the peer appears, then `connects_dialed` rises one sample later" is the
**expected signature of the provider service's own successful dial**. The two
events are microseconds apart in the code and merely straddled a 0.2 second
sampling boundary. The run is ambiguous between the defect and entirely correct
behavior, and cannot distinguish them with those two observables.

The same table records the provider's overlay as **absent** from `/peers` at the
response instant for that run, which is the opposite of already connected. That
should have been noticed at the time.

**What the defect actually rests on** is reading `isConnected`, which is
deterministic and not in doubt, plus the unit reproduction below. Rule 11 asks
for measured or reproduced rather than reasoned, so the reproduction is the
part that earns the claim, and it ships with this change rather than being
promised.

## Correction: fixing `pkg/p2p` would not have unblocked #369

The stated reason for the `pkg/p2p` change was that
[#369](https://github.com/crtahlin/wasp/issues/369) arms 2 and 6 cannot be
settled while this holds. That is not right either.
`discovery-lifetime-results.md` records arm 2's three runs, and in runs 2 and 3
the dial completed **inside the request**, before the response ended. Those runs
are inconclusive for a reason this defect has nothing to do with, and a correct
fix would still show `connects_dialed` rising in them, because at the instant
`Connect` ran the peer genuinely was not connected.

Fixing the address keying is therefore **necessary but not sufficient** for
those arms. What they also need is a pair of nodes that do not reconnect to each
other on their own, which the results document already says and which this bench
cannot provide.

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
   held (`kademlia.go:344`, `:387`), so it needs a race between selection and
   dial. The first draft gave it the most space of the three, which was the
   wrong emphasis.
2. **The provider adapter stops notifying topology.** Today the different
   underlay case falls through to `kad.Connected` at `pkg/node/providers.go:115`
   and so reaches `onConnected` (`kademlia.go:1326-1339`), running `Announce`,
   `connectedPeers.Add`, `waitNext.Remove`, `recalcDepth` and
   `detector.Record()`. Under the peer-keyed check the early return at `:106`
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
`bzz.ClassifyTransport` (`pkg/bzz/transport.go:72-80`) keys on `/tcp` alone.

*Mutation:* none, this arm pins existing behavior.

**Arm 2, the counter is right when the peer was already connected.** With a
fake `p2p.Service` whose `Peers()` reports the provider's overlay and whose
`Connect` returns success with a nil error, the adapter's `Connect` returns
`true`.

*Mutation:* return `false` unconditionally, or classify from `err` alone as
today. Both fail this arm. Today's code fails it, which is the point.

**Arm 3, the counter is right when the peer was not connected.** Same fake with
`Peers()` empty. The adapter returns `false`.

*Mutation:* return `true` unconditionally. This is the sensitivity control for
arm 2: without it, a change that always reports already-connected passes arm 2.

**Arm 4, the classification is read before the connect, not after.** The fake's
`Connect` adds the overlay to what `Peers()` subsequently reports. The adapter
must still return `false`, because the peer was not connected when it was asked.

*Mutation:* move the `Peers()` read to after `p2ps.Connect` returns. Arms 2 and
3 both pass under that mutation; only this one fails. It is the arm for the
ordering the whole design rests on.

**Arm 5, the overlay guard still fires.** The fake returns success with an
overlay different from the record's. The adapter returns `errProviderOverlay`
and calls `Disconnect`, and does **not** report a connect of either kind.

*Mutation:* return `wasConnected` before the overlay check. This arm exists
because the first draft's `pkg/p2p` change would have widened exactly this hole
and no arm in that draft tested it.

**Arm 6, the `ErrAlreadyConnected` branch is unchanged.** The fake returns that
error; the adapter returns `true` with no error and does not call
`kad.Connected`.

*Mutation:* delete the branch. Catches a refactor that folds it into the new
classification and changes the early-return behavior with it.

**Known mutation with no arm.** Replacing `connectedOverlay`'s equality test
with one that matches any peer would be caught by arm 3 only when the fake
reports a non-empty peer set of other peers, so arm 3 is written that way,
with one unrelated peer present. Stated because listing a mutation and not
covering it is how the #299 table went wrong.

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

- `pkg/node/providers.go`, the classification and the `connectedOverlay` helper,
  and the stale comment in the `ErrAlreadyConnected` branch which describes the
  behavior this change stops relying on.
- `pkg/node/providers_test.go`, arms 2 to 6 against a fake `p2p.Service`.
- `pkg/p2p/libp2p/connections_test.go`, arm 1. Not `libp2p_test.go`, which holds
  only helpers.
- `docs/experiments/content-providers/discovery-lifetime-results.md`, retracting
  the causal reading of arm 2 run 1. It is merged on `main` and says the node
  was already connected to the peer, which the ordering above rules out.
- `docs/DIFFERENCES.md`, the counter's description, and the header commit line,
  which still names `f5fd2b51` while `main` is further on.

No bench harness, because there is no bench arm.
