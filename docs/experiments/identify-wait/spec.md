# Answer a peer behind NAT without waiting 10 s for its addresses

Issue: [#511](https://github.com/crtahlin/wasp/issues/511).
Type: fix.

## Problem

A fresh ultra-light node behind NAT takes about 10.5 s to connect to a full node
with a public address. Measured on 2026-09-25 on the bench with a plain
`POST /connect` from three throwaway ultra-light nodes to `stake-1`: 10.61, 10.54
and 10.65 s. The same connect from `bench-1`, a full node that advertises its
public address, took 0.23 s. A debug trace of one slow connect shows nothing
logged for 10.3 s and then `handshake finished for peer (outbound)`, so the time
is spent before the handshake completes (comment on #511).

It matters beyond one test: every first connection from such a node pays it, and
the provider wait bound had to be raised from 10 s to 20 s because of it (#513).

## Hypothesis

The delay is on the **responding** side, in `peerMultiaddrs`
(`pkg/p2p/libp2p/libp2p.go:1557-1564`), called by the inbound stream handler
(`:605`) before the handshake:

1. `peerMultiaddrs` waits up to `peerstoreWaitAddrsTimeout`, 10 s, for the
   peerstore to hold at least one address of the remote peer
   (`waitPeerAddrs`, `:1754`).
2. For an inbound peer, the peerstore holds only what identify reported. libp2p's
   identify keeps **only public addresses** from a peer that connected over a
   public address (`filterAddrs`, `p2p/protocol/identify/id.go:1076-1087` in
   go-libp2p v0.48.0).
3. A node behind NAT without `nat-addr` has only private listen addresses, so
   after identify the responder's peerstore entry is empty, the wait runs to its
   end, and the handler falls back to `stream.Conn().RemoteMultiaddr()`. The
   comment at `:614-617` already names this case: "This typically means the peer
   is behind NAT".

So the 10 s buys nothing: once identify has finished, no address is coming. The
dialling side (`:1139`) does not wait, because the dial itself added the target's
addresses to the peerstore.

## Design

Wait for identify to **finish**, not for an address to **appear**:

- `peerMultiaddrs` takes the connection. It waits on the host's
  `IDService().IdentifyWait(conn)` channel, still bounded by
  `peerstoreWaitAddrsTimeout`, then reads the peerstore once. If identify has
  already completed, as it usually has by the time the handshake stream arrives,
  this returns at once.
- An empty result keeps today's fallback to the connection's remote address, so
  what goes into the handshake is unchanged; only the wait before it is.
- When the host does not expose an identify service, the current address wait
  is kept, unchanged.

Both call sites use the new form. The dialling side's behaviour does not change
in practice, since its peerstore already holds the addresses.

## Protocol impact

**None.** The handshake messages and every constant in
`.github/protocol-freeze.lock` are unchanged, and so is the observed underlay a
node sends. Only how long the responder waits before sending it changes.
`make protocol-freeze` must report the surface unchanged.

## Tests

In `pkg/p2p/libp2p`, mutation checked:

- **An inbound peer whose identify completed with no addresses is answered at
  once.** A unit test of the address helper with a fake identify service whose
  wait channel is already closed and an empty peerstore: returns in well under
  the timeout, empty. Fails today, where it waits the full timeout.
- **Identify not finished yet: the wait is still bounded** by the timeout.
- **Addresses present: they are returned**, as today.
- The existing connection tests keep passing unchanged.

Mutations: wait for addresses instead of identify; drop the timeout on the
identify wait.

## Measurement

The effect is on the responder, so the responder must run the change and the
requester must be behind NAT with a different public IP.

- **Responder:** a throwaway full node on a host with a public address, with
  payments off and a fresh data directory, run first on v0.1.4 and then on the
  build with the change, and removed afterwards.
- **Requester:** fresh ultra-light nodes on the `bench-1` host, which is behind NAT.
- **Test:** `POST /connect` from the requester to the responder, three runs per
  build, each from a new fresh node.

Expected: about 10.5 s on v0.1.4, well under 1 s with the change. A negative
result is a connect that still takes about 10 s with the change, which would
mean the wait is elsewhere. Results go into `results.md`.

If v0.1.4 behaves as measured, which is unmodified upstream code, the issue gets
`affects-upstream`, with the unit test and this measurement as the evidence.

## Rollout and rollback

No configuration. Rollback is reverting the merge.

## Upstream portability

The change is contained in `peerMultiaddrs` and its two callers, which are
unmodified upstream code, so it applies to upstream Bee as is.

## Files

- `pkg/p2p/libp2p/libp2p.go`: `peerMultiaddrs`, `waitPeerAddrs`, both callers.
- a test file in `pkg/p2p/libp2p`.
- `docs/DIFFERENCES.md`, `docs/experiments/INDEX.md`.

Generated with help of AI.
