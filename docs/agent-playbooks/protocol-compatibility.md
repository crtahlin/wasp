# Protocol compatibility

The point of this fork is to run modified Bee nodes **on the real Swarm
network, alongside stock Bee nodes**. Everything else is negotiable; this is
not. A change that makes stock peers refuse to talk to us has not optimized
anything — it has removed the node from the network.

## What is frozen

`.github/protocol-freeze.lock` holds a fingerprint of every value that decides
whether a stock peer will complete a handshake, route to us, or accept our
chunks. CI regenerates the fingerprint on each pull request and fails if it
moved.

| Surface | Where | Why it matters |
|---|---|---|
| Stream-name prefix `"/swarm/"` | `pkg/p2p/p2p.go`, `NewSwarmStreamName` | Every protocol stream is named from it. Change it and no stock peer recognises any of our streams |
| `protocolName` / `protocolVersion` | `pkg/hive`, `pkg/pushsync`, `pkg/pullsync`, `pkg/retrieval`, `pkg/status`, `pkg/pricing`, `pkg/pingpong`, `pkg/settlement/pseudosettle`, `pkg/settlement/swap/swapprotocol` | Matched per-protocol on connect. See the semver rule below |
| `ProtocolVersion` | `pkg/p2p/libp2p/internal/handshake/handshake.go` | The handshake itself. A mismatch means no connection at all |
| Handshake field numbers | `pkg/p2p/libp2p/internal/handshake/pb/handshake.proto` | Protobuf field numbers are permanent. Reusing one silently misinterprets peer data |
| Chunk geometry | `pkg/swarm/swarm.go` — `ChunkSize`, `Branches`, `SectionSize`, `HashSize`, `MaxPO`, the SOC sizes | These determine chunk **addresses**. Changing one makes our hashes disagree with every other client, including the JavaScript ones |
| `NetworkID`, `ChainID`, contract addresses | `pkg/config/chain.go` | `NetworkID` is checked in the handshake **and** mixed into the overlay address and the signed `BzzAddress`. It is the switch that deliberately creates a separate network |

## The semver rule, which is subtler than the freeze

`pkg/p2p/libp2p/version.go`:

```go
return vers.Major == chvers.Major && vers.Minor >= chvers.Minor
```

For any given protocol, a peer will speak to us when the **major matches** and
**our minor is greater than or equal to theirs**. The consequences are
asymmetric and easy to get backwards:

- **Patch bumps are free.** Nothing compares them.
- **Raising a minor** means peers still on the older minor will no longer dial
  us for that protocol. We can dial them; they cannot dial us. On a network
  where most nodes run the stock release, that is a slow, partial isolation that
  looks like a peering problem rather than a version problem.
- **Raising a major** is a hard split for that protocol.

So the freeze check failing on a minor bump is not pedantry. Ask who is still on
the old minor before you argue with it.

## When a change genuinely needs to touch the frozen surface

1. Say so in the spec's **Protocol impact** section: what changes, which peers
   stop interoperating, and what the operator-visible symptom would be.
2. Apply the `protocol-change` label to the pull request. The freeze check is
   gated on its absence, so the label is what lets CI pass — treat applying it as
   a deliberate act, not a way to make a red check go away.
3. Regenerate the lock in the same pull request:

   ```bash
   scripts/protocol-freeze.sh > .github/protocol-freeze.lock
   ```

4. Expect it to be reviewed as a network change, not a code change.

## Upstream syncs are exempt, deliberately

Upstream bumps these values as a normal part of releasing, and the sync workflow
regenerates the lock in the same commit. The check is skipped for branches named
`sync/upstream/*` carrying the `upstream-sync` label — both conditions, so a
hand-pushed branch cannot claim the exemption with a label alone.

On a sync pull request the lock diff **is** the protocol review:

```bash
git diff main...HEAD -- .github/protocol-freeze.lock
```

Read it. If upstream raised a protocol minor, our nodes will stop being dialable
by peers still on the previous release until they upgrade — which is normal and
expected, but it is worth knowing before it happens rather than after.

## What is not frozen, and is safe to change

- The libp2p **user agent** string (`pkg/p2p/libp2p/libp2p.go`). It is
  informational; the handshake does not gate on it. This fork deliberately
  extends it to advertise both the upstream base and the fork version.
- Anything in the HTTP API. It is versioned independently in
  `openapi/Swarm.yaml` and is a local operator interface, not a peer interface.
- Internal storage layout, scheduling, concurrency, caching — the places most
  optimizations belong.
- The `status` protocol snapshot carries no version field, so nothing about our
  version identity reaches peers through it.
