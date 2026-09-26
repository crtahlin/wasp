# Results: connecting to an already connected peer

Issue: [#522](https://github.com/crtahlin/wasp/issues/522). Spec:
[spec.md](spec.md). Merged as `1063b2bf` (#526), tag
`exp-connect-already-connected`.

## Outcome

**Validated.** With the fix, `POST /connect` to a peer that is already connected
answered 200 with the peer's overlay in all 15 calls: 9 while the peer held
the connection it had opened, and 6 in the same direction twice. The peer stayed
connected throughout and the node's connected peer count never dropped. The
unfixed v0.1.5 answered 500 on the repeated connect in 3 of 3 rounds. In the
reverse case it no longer answered 500 (0 of 13), but it still ran a second
handshake.

## Setup

- `bench-1` on `main` at `b65c6f2d`, which carries the fix, calls
  `POST /connect`.
- `stake-1` on the v0.1.5 release, which does not carry the fix, is the peer
  it connects to. The fix works on the node that makes the call, so `stake-1`
  making the same calls towards `bench-1` is the control on the same pair of
  machines.
- 2026-09-26. Both nodes are public; `bench-1` is behind a port-forwarding NAT.

## With the fix

**Table: `POST /connect` from `bench-1` (fix) to `stake-1`, 2026-09-26**

| Case | Calls | Answer | Time per call | Peer still connected | `bench-1` peer count |
|---|---|---|---|---|---|
| `stake-1` opened the connection, then `bench-1` connects (five calls in round 1, two in rounds 2 and 3) | 9 | 200, `stake-1`'s overlay | 0.49 to 0.61 ms | 9 of 9 | never dropped |
| `bench-1` connects, then connects again (three rounds) | 6 | 200 | first call 0.23 to 0.33 s (a real dial), second 0.48 to 0.67 ms | 6 of 6 | |

Answers under a millisecond are answers from the registry: no dial and no
handshake ran.

## Without the fix, on the same machines

**Table: `POST /connect` from `stake-1` (v0.1.5) to `bench-1`, 2026-09-26**

| Case | Rounds | Answer | Time per call | Peer still connected |
|---|---|---|---|---|
| `bench-1` opened the connection, then `stake-1` connects | 13 | 200 | 0.07 to 0.16 s | 13 of 13 |
| `stake-1` connects, then connects again | 3 | first 200, second **500** `network status unknown: already connected` | | 3 of 3 |

The reverse case ran a second bzz handshake over the existing connection each
time, which is what the 0.07 to 0.16 s is. It no longer ended in 500, although
it did in both directions when the issue was filed that morning, between
`stake-1` on v0.1.4 and `bench-1` on `c9acd329`. **Why it stopped is not
established.** Both nodes have changed since then: `stake-1` moved to v0.1.5,
which adds #511, and `bench-1` moved to a build with this fix, which changes
only the node that calls. The connection in those earlier cases had also been
open for longer and carried downloads. The duplicate handshake remains on
unfixed nodes, and so does the 500 for a repeated connect in the same
direction, which is the case that does reproduce here.

## Review

An independent review found that the first version made kademlia count a light
node that had connected to this node as a connected full peer. The registry
holds light peers too, and a dial had used to fail with `ErrDialLightNode`,
which kademlia prunes on. `Connect` now returns that error for a held light
peer and keeps its connection. A test covers it, and the mutation that drops
the check fails it. This case was not measured on a node.

## Issue #382

#382 recorded the address keying and chose not to change it. This change
replaces it with a per-peer check, which is what #382 described as the cost of
changing it. The provider connect counters' comments now describe the new
behaviour.

Generated with help of AI.
