# Deciding already-connected per peer rather than per address

Issue: [#382](https://github.com/crtahlin/wasp/issues/382).
Type: fix. Labels include `affects-upstream`.

## Problem

`p2p.ErrAlreadyConnected` is decided by matching the **remote address** of an
open connection, not by asking whether the peer is connected at all. A connect
to a peer that is already connected on a different underlay therefore does not
take that branch. It runs the dial path, the handshake, and the topology path,
and returns success indistinguishable from a first connection.

The check is in `Connect`, `pkg/p2p/libp2p/libp2p.go:1066-1074`:

```go
remoteAddr := addr.Decapsulate(hostAddr)

if overlay, found := s.peers.isConnected(info.ID, remoteAddr); found {
	address = &bzz.Address{Overlay: overlay, Underlays: []ma.Multiaddr{addr}}
	return address, p2p.ErrAlreadyConnected
}
```

`isConnected` (`pkg/p2p/libp2p/peer.go:185`) requires **both** the peer ID in
`r.overlays` **and** a connection in `r.connections[peerID]` whose
`RemoteMultiaddr()` compares equal to `remoteAddr`. An open connection to the
same peer on another underlay satisfies the first and fails the second, so the
function returns `false` and execution falls through.

### What the fall-through actually costs

The issue estimated the cost as "one `host.Connect` that returns immediately
plus a handshake path". Reading the rest of `Connect` makes that more precise,
and the second half is the larger part:

1. `s.host.Connect` returns almost immediately. go-libp2p adds the addresses to
   its peerstore and then returns `nil` when `Connectedness(peerID)` is already
   `Connected`, without opening a transport connection. Checked in the pinned
   dependency rather than recalled: `go-libp2p v0.48.0`,
   `p2p/host/basic/basic_host.go:543-546`, where the early return is guarded
   only by `forceDirect`, which Bee never sets. So there is no second TCP dial,
   and what is wrong is the name of the counter that rises rather than the
   connection being wasteful.
2. `Connect` then opens a **new handshake stream** on the existing connection
   (`libp2p.go:1131`), exchanges `peerMultiaddrs`, and runs the full
   `handshakeService.Handshake` round trip (`:1156`). That is real work with the
   peer, repeated every time this path is taken.
3. Only at the end does `addIfNotExists` (`peer.go:141`) notice the overlay is
   already registered and return `exists == true`. The branch that handles it,
   `libp2p.go:1191`, closes the handshake stream and returns
   **`i.BzzAddress, nil`**, a plain success with no error at all.

Point 3 was not in the issue and matters for the design below: there is already
a place in `Connect` that knows the peer was a duplicate, and it deliberately
reports success rather than `ErrAlreadyConnected`.

### Why this fork cares

The provider service publishes `bee_providers_connects_dialed` and
`bee_providers_connects_already_connected`, and two acceptance arms of
[#369](https://github.com/crtahlin/wasp/issues/369) are built on the pair. The
addresses in a provider record are liable to differ from the live connection by
construction: `Options.Address` in `pkg/node/providers.go` filters the node's
underlays to public ones and caps them at `providers.MaxUnderlays`, which is 4,
while kademlia may hold a connection on a private address.

Observed on the bench while measuring #369 arm 2, sampling `/peers` and
`/metrics` every 0.2 seconds after a download completed:

| | |
|---|---|
| provider's overlay first appears in `/peers` | 1.2 s after the response |
| `bee_providers_connects_dialed` rises | 1.4 s after the response |
| `bee_providers_connects_already_connected` | 0 |

The peer was connected before the provider service's `Connect` ran, and the
connect was still counted as a dial. One run, so it is an observation and not a
measurement under rule 7. The fix is justified by reading the code; the bench
arms below are what would measure it.

### Correction to the issue

[#382](https://github.com/crtahlin/wasp/issues/382) states that
`pkg/p2p/libp2p/libp2p.go` and `pkg/p2p/libp2p/peer.go` are "unmodified here".
That is too broad and is wrong as written. Both files carry fork changes:
`peer.go` adds `count()`, and `libp2p.go` adds the zero-peer breaker bypass for
[#74](https://github.com/crtahlin/wasp/issues/74) and the extended user agent.

What is true, and is what rule 11 needs, is narrower and was checked with
`git diff upstream/v2.8.2`: the three hunks this spec is about, `isConnected` in
`peer.go`, the call site at `libp2p.go:1068`, and the duplicate branch at
`libp2p.go:1191`, are byte-identical to `upstream/v2.8.2`. The fork's changes to
those two files are elsewhere in them. The `affects-upstream` label is earned,
but the sentence supporting it in the issue is being corrected rather than
repeated.

## Hypothesis

Matching on the remote address was chosen so the returned `bzz.Address` could
name an underlay known to be carrying the connection, not to express a rule that
a peer may be connected once per address. Deciding the case per peer, while
still returning the requested address, keeps every caller's use of the result
working and removes both the redundant handshake and the false dial count.

## Design

### The change

In `Connect`, replace the address-keyed pre-dial check with a peer-keyed one
that is confirmed against go-libp2p's own view of the transport:

```go
if overlay, found := s.peers.connected(info.ID); found &&
	s.host.Network().Connectedness(info.ID) == network.Connected {
	address = &bzz.Address{Overlay: overlay, Underlays: []ma.Multiaddr{addr}}
	return address, p2p.ErrAlreadyConnected
}
```

`connected` is a new method on `peerRegistry` returning the overlay when
`r.overlays[peerID]` is present and `r.connections[peerID]` is non-empty. It
replaces `isConnected`, which has no other caller.

The second condition is not redundant. The registry is updated by notification
and can briefly hold a peer whose transport connection has already gone. Without
the cross-check, a stale entry would suppress a legitimate re-dial, which is a
worse failure than the one being fixed because it is silent and persists.
Asking go-libp2p directly makes the suppression impossible: if the transport is
gone, the dial proceeds as it does today.

The returned `Underlays` is the address that was asked for. That is the same
shape the code returns today, and worth stating plainly because the meaning
changes: it is now the underlay the caller requested, not necessarily the one
carrying the connection. No current caller reads it.

### What this changes for callers

Three callers test for the error. Two are in kademlia and one is this fork's
provider adapter.

- `pkg/topology/kademlia/kademlia.go:953`, the bootnode path. Logs and returns.
  No behavior change beyond the log line being reached more often.
- `pkg/topology/kademlia/kademlia.go:1099`, the general connect path. On
  `ErrAlreadyConnected` it checks the overlay matches and returns `nil`, and
  that return is **before** `k.detector.Record()` and `k.Announce(ctx, peer,
  true)` at the end of the function. So a kademlia dial to a peer already
  connected on another underlay will, after this change, stop re-announcing that
  peer and stop recording a reachability sample.

  This is the one real behavior change and it is called out rather than
  discovered later. It is judged acceptable because the peer was announced when
  it first connected, inbound connections announce through `handleIncoming`, and
  re-announcing a peer that was already connected is duplicated work rather than
  a distinct signal. If measurement suggests otherwise the fix is to move
  `detector.Record()` above the switch, which is a separate change and is not
  made here.
- `pkg/node/providers.go:101`, this fork's adapter, which returns
  `(true, nil)` meaning "connected, no dial needed". This is the caller the
  counters serve, and it starts being correct. The comment there describing the
  address-keyed behavior is removed in the same change, since leaving a comment
  that describes the old behavior is how a stale claim survives a fix.

### Options considered and rejected

**Return `ErrAlreadyConnected` from the duplicate branch at `libp2p.go:1191`
instead of `nil`.** Rejected. It corrects the reported outcome but keeps the
redundant handshake, which is the part that costs something, and it imposes the
same kademlia change as the chosen design without the benefit.

**Check connectedness in the caller, `pkg/node/providers.go`, and never touch
`pkg/p2p`.** The adapter does have what it needs: `addr.Overlay` is in hand, and
`p2p.Service.Peers()` is exported. Rejected as the primary fix for two reasons.
It leaves the defect in place for every other caller, including kademlia, where
it costs a handshake per occurrence. And the check would sit outside the lock
that `Connect` takes, so a kademlia dial landing between the check and the call
would still be counted as our dial. That race is narrower than the current
defect but it is a race, whereas the chosen design has none: the decision is
made inside `Connect` on the same registry the connection notification updates.

It is recorded here rather than left out because it is the rollback shape if the
`pkg/p2p` change has to be reverted.

## Protocol impact

**None.** Nothing in `.github/protocol-freeze.lock` is touched: no stream name,
no `protocolName` or `protocolVersion`, no handshake `ProtocolVersion` or field
number, no chunk geometry, no `NetworkID`. The change decides whether this node
opens a redundant handshake stream to a peer it is already connected to. It
sends no new message, removes no message a peer expects, and changes no value a
peer compares against.

A stock Bee peer cannot observe the difference except as the absence of a second
handshake it would have answered, which is a request it never required.
`make protocol-freeze` is expected to pass unchanged, and the
`protocol-change` label is not applied. Per rule 6 this spec is written after
reading `docs/agent-playbooks/protocol-compatibility.md`.

## Measurement

The claim to be shown is narrow: **a connect to a peer already connected on a
different underlay reports already-connected and opens no handshake stream.**
It is a correctness and observability fix, so the arms are pass or fail rather
than a before-and-after number.

**Arm 1, unit, deterministic.** Test hosts listen on `":0"`, so a test service
has more than one underlay in `Addresses()`. Connect from A to B on the first
underlay, then connect again on a second, distinct one. Passes when the second
call returns `p2p.ErrAlreadyConnected` with the expected overlay. On unfixed
code this returns a nil error, which is the mutation that must fail.

If a machine reports only one underlay the test would have nothing to drive, so
it skips with a stated reason rather than passing empty. Because a skip that
fires everywhere is a test that never runs, the implementation must confirm on
each CI platform that it did not skip, and if it skips anywhere the fallback is
to dial the same endpoint by a second multiaddr spelling, `/dns4/localhost/...`
against `/ip4/127.0.0.1/...`, after confirming `filterSupportedAddresses` keeps
`dns4`.

**Arm 2, unit.** Same setup, but close the connection from B's side and wait for
A's registry to see the disconnect before the second connect. Passes when the
second connect dials and succeeds. This is the sensitivity control for arm 1: it
fails if the new check reports already-connected whenever it has ever seen the
peer, which is the failure mode the `Connectedness` cross-check exists to
prevent.

**Arm 3, bench, three runs.** Re-run #369 arm 2: a requester downloads with a
provider hint for a provider that kademlia already holds. Records the change in
`bee_providers_connects_dialed` and
`bee_providers_connects_already_connected` across the download.

Passes when, on all three runs, `already_connected` rises by exactly 1 and
`dialed` does not rise. The same measurement on the current build produced
`dialed` 1 and `already_connected` 0.

**Arm 4, bench, three runs, sensitivity control for arm 3.** The same download
against a provider the requester is **not** connected to, confirmed by reading
`/peers` immediately before the request. Passes when `dialed` rises by 1 and
`already_connected` does not. Without this arm, a fix that made
`already_connected` rise unconditionally would pass arm 3.

**What a negative result looks like.** Arm 3 showing `dialed` still rising means
either the provider genuinely was not connected at that instant, which arm 4's
`/peers` read is there to distinguish, or the connect is reaching a path this
change does not cover. Either is reported as a negative result for the arm, not
explained away. A single failing run in arms 3 or 4 is a reject for that arm.

`make test-race` on `pkg/p2p/...` and `pkg/topology/...` is part of the same
pass, since the change reads a registry that a notification goroutine writes.

## Rollout and rollback

No configuration. This is a defect fix with one behavior, and rule 8 does not
apply because no tuning constant is involved: there is no value an operator
could reasonably want set either way.

Rollback is `git revert` of the merge commit. It restores the address-keyed
check exactly, because the change replaces `isConnected` rather than layering
over it. If only the kademlia consequence needs undoing and the counter fix is
to be kept, the rejected caller-side option above is the shape to fall back to,
and it lives entirely in fork code.

Nothing an operator has to do, nothing to migrate, no on-disk layout touched.

## Upstream portability

The defect is upstream's. The three hunks are byte-identical at
`upstream/v2.8.2`, checked with `git diff` rather than assumed, and the patch
applies to that tree without adaptation because it depends on nothing this fork
added.

What Ethersphere would need is the argument, not the diff: a reason the
address-keyed check was written that way, and a judgment on whether losing the
re-announce at `kademlia.go:1099` is acceptable. This spec states both, and
that is the reusable part.

Per rule 11 the `affects-upstream` label is a marker for a later human decision
and nothing more. Nothing here authorizes contacting ethersphere, opening
anything in their tracker, or sending a patch, and rule 1 governs that without
exception.

## Configuration

None. See "Rollout and rollback".

## Files

Implementation, on `fix/382-already-connected` after this spec merges:

- `pkg/p2p/libp2p/peer.go`, `isConnected` replaced by `connected`.
- `pkg/p2p/libp2p/libp2p.go`, the call site at `:1068`.
- `pkg/node/providers.go`, the stale comment in the `ErrAlreadyConnected`
  branch.
- `pkg/p2p/libp2p/libp2p_test.go`, arms 1 and 2.
- `docs/DIFFERENCES.md`, a row for the behavior change, per rule 13.

Bench harness for arms 3 and 4 lives outside the repository under `cp290/`,
per rule 10.
