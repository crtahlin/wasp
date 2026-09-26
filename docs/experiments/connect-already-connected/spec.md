# Connecting to an already connected peer answers 200 and keeps the connection

Issue: [#522](https://github.com/crtahlin/wasp/issues/522). Related:
[#382](https://github.com/crtahlin/wasp/issues/382).
Type: fix.

## Problem

`POST /connect/<address>` to a peer this node is already connected to answers
500. Seen on 2026-09-26 in the stampless handoff walkthrough, in both
directions between `stake-1` and `bench-1`, and reproduced on demand. The
handoff procedure tells a downloader to connect to the provider first and to
read a failed connect as "the provider is not reachable", so this sends an
operator after a network problem that does not exist.

There are two paths to it, both in code unmodified from upstream. Both
reproduce on `upstream/v2.8.2` with the same test (comment on #522):

1. **Already connected over another address, or from the other side.**
   `Connect` (`pkg/p2p/libp2p/libp2p.go`) treats a peer as connected only when
   `peerRegistry.isConnected` (`pkg/p2p/libp2p/peer.go:185-212`) finds the peer
   ID **and** an open connection whose remote address equals the one being
   dialled. When the existing connection was opened by the other node, its
   remote address differs, so the check misses. `s.host.Connect` then returns at
   once over the existing connection, and `Connect` runs a **second bzz
   handshake** on it and returns no error. The API handler's next step,
   `topologyDriver.Connected`, fails with `new stream: peer not found`, and the
   handler then **disconnects the peer** and answers 500. A repeat call succeeds
   only because the first call tore the connection down.
2. **Already connected over the same address.** `Connect` returns
   `p2p.ErrAlreadyConnected`, and `peerConnectHandler` (`pkg/api/peer.go`)
   answers every error with 500.

## Why change the keying now

#382 found the same address keying and chose not to change it, because the
shape "looks deliberate rather than accidental, since it is paired with
building a `bzz.Address` from the matched underlay, so the change needs a
reason for the current shape before it is made." #382's only consequence was a
miscounted metric. This issue supplies the reason the keying is harmful: an
API call against a healthy connection **disconnects the peer**. No caller
depends on the address keying for anything but the returned address, which is
built from the dialled address anyway.

## Design

### 1. `Connect` decides "already connected" per peer

Before dialling, if the registry holds the peer ID (`r.overlays[peerID]`,
which is set when the bzz handshake completed), return
`p2p.ErrAlreadyConnected` with that overlay and the dialled address as the
underlay, as the current branch does. The address comparison is dropped.

Callers already handle `ErrAlreadyConnected`:

- **Kademlia** (`pkg/topology/kademlia/kademlia.go:1117`) checks that the
  overlay is the expected one and treats the connection as made. More of its
  dials to peers already connected inbound will now take this branch rather than
  run a duplicate handshake, which is the intended effect.
- **Providers** (`pkg/node/providers.go:98`) counts it as "already
  connected", which becomes accurate for peers on another underlay. The comment
  there, and the caveat on `ConnectsDialed` that points at #382, are updated to
  match.

### 2. The API answers 200 for an already connected peer

`peerConnectHandler` answers `ErrAlreadyConnected` with 200 and the peer's
overlay, and does not call `topologyDriver.Connected` or disconnect. The peer is
already known to topology through its original connection.

### Not changed

- The handshake, its messages and every constant in
  `.github/protocol-freeze.lock`.
- The case of a peer whose transport connection exists but whose bzz
  handshake has not finished. The registry does not hold it yet, so `Connect`
  behaves as today.

## Protocol impact

**None on the wire.** No message, field or constant changes, and
`make protocol-freeze` must report the surface unchanged. A node stops starting
a second handshake on a connection that already carries one. A stock Bee peer
on the other end sees fewer duplicate handshakes, never a new kind of message.

## Tests

In `pkg/p2p/libp2p` and `pkg/api`, mutation checked:

- **A connects to B while B is already connected to A.** `Connect` returns
  `ErrAlreadyConnected` with B's overlay, no second handshake runs, and both
  still list each other as peers afterwards. Fails today: it returns nil after a
  second handshake.
- **The same direction twice** still returns `ErrAlreadyConnected`, as today.
- **`POST /connect` to an already connected peer answers 200** with the
  overlay, and does not disconnect it. Fails today with 500.
- **A peer that is not connected** is dialled and handshaken as today.

Mutations: restore the address comparison; answer 500 again for
`ErrAlreadyConnected`; disconnect on `ErrAlreadyConnected`.

## Measurement

On the bench, with the build deployed to `bench-1` and `stake-1` on a release:

- `POST /connect` from `bench-1` to `stake-1` while `stake-1` is connected to
  `bench-1` inbound, five times: expect 200 each time, with the peer still in
  `/peers` throughout and no drop in `bench-1`'s connected peer count.
- The same in the same direction twice: 200 both times.
- The handoff walkthrough's case 1 then no longer records `connect 500`.

A negative result is any 500 or a disconnection.

## Rollout and rollback

No configuration. Rollback is reverting the merge.

## Upstream portability

`isConnected`, the `Connect` call site and `peerConnectHandler` are unmodified
upstream code, so the change applies to upstream as is. The issue carries
`affects-upstream`: the reproduction ran on `upstream/v2.8.2`.

## Files

- `pkg/p2p/libp2p/libp2p.go`: the already-connected check in `Connect`.
- `pkg/p2p/libp2p/peer.go`: a per-peer lookup, if the registry needs one.
- `pkg/api/peer.go`: `peerConnectHandler`.
- `pkg/node/providers.go`, `pkg/providers/metrics.go`: comments that describe
  the old keying.
- tests in `pkg/p2p/libp2p` and `pkg/api`.
- `docs/DIFFERENCES.md`, `docs/UPSTREAM.md`, `docs/experiments/INDEX.md`.

Generated with help of AI.
