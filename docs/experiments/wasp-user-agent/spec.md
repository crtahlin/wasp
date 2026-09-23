# The libp2p user agent should lead with wasp

Issue: [#474](https://github.com/crtahlin/wasp/issues/474).
Type: chore.

## Problem

A wasp node's libp2p user agent begins with `bee/`, so a reader of a list of
agents sees a Bee node:

```
bee/2.8.2 wasp/0.1.4-6484a665 go1.26.4 linux/amd64
```

Upstream v2.8.2 emits `bee/<version> <go> <os>/<arch>`, so the fork already
adds a `wasp/` token. The token is there; its **position** is the problem.

## Why position is what matters, measured

swarmscan's `/v1/network/nodes` stores the agent verbatim and
`/v1/network/stats` aggregates **by the complete string**. Checked 2026-09-23,
its distribution was:

| user agent | nodes |
|---|---|
| `bee/2.8.2-7e703f49 go1.26.7 linux/amd64` | 3,414 |
| `storer-node/0.1.0` | 376 |
| (empty) | 520 |
| five other `bee/...` variants | 27 |

So a wasp node **already** lands in its own row, because the whole string is the
key. What it does not do is *read* as a distinct client: the row begins `bee/`
like every other row except one.

**`storer-node/0.1.0` is the precedent and the evidence.** Another client on
this network leads with its own name, and its 376 nodes show that an agent
which does not begin `bee/` takes part normally.

## The change

One line in `pkg/p2p/libp2p/libp2p.go`:

```go
// before
return fmt.Sprintf("bee/%s wasp/%s %s %s/%s",
    strings.TrimPrefix(bee.UpstreamBase, "v"), bee.Version, ...)

// after
return fmt.Sprintf("wasp/%s bee/%s %s %s/%s",
    bee.Version, strings.TrimPrefix(bee.UpstreamBase, "v"), ...)
```

giving `wasp/0.1.4-6484a665 bee/2.8.2 go1.26.4 linux/amd64`.

The `bee/<upstream base>` token is **kept**, deliberately. It says which Bee the
node derives from, which is information an operator and a crawler both want, and
it keeps the string parseable by anything that looks for the base.

## Rejected: putting wasp in the version number

The obvious alternative is to encode the fork in the version, for example
`bee/2.8.2-wasp.0.1.4`. Rejected:

- It claims to be a **prerelease of 2.8.2**, which it is not.
- It sorts **below** 2.8.2 in any semver comparison, so wasp nodes would read as
  outdated rather than as different.
- It is the same reasoning `docs/agent-playbooks/release-process.md` already
  uses to reject `v2.8.1-exp.1` as a release tag, applied to the same string in
  a different place.

Changing the release version alone would also not achieve the goal: swarmscan
keys on the whole agent, so a version bump relabels the row without making it
recognisable.

## What it costs

**Anything counting Bee nodes by a `bee/` prefix stops counting wasp nodes.**
That is the intent, and it is a cost as well as a benefit: a wasp node is
protocol-compatible and does carry network traffic, so a network-health count
that drops it becomes slightly less accurate about the network while becoming
more accurate about clients. Stated here so the trade is visible rather than
discovered later.

Nothing else changes. The agent gates nothing.

## Protocol impact

**None.** The user agent is not in `.github/protocol-freeze.lock`. Connections
are decided by the handshake, `ProtocolVersion` and the network id, not by the
agent string, and `storer-node`'s 376 nodes demonstrate that a non-`bee/` agent
connects normally. `make protocol-freeze` must still report the wire surface
unchanged, and that is one of the checks below.

## Tests

In `pkg/p2p/libp2p`, mutation checked.

- **The agent starts with `wasp/`.** Fails today. This is the whole point, and
  an assertion on the prefix rather than on the whole string, so the Go version
  and architecture do not make the test brittle.
- **The agent still carries `bee/<upstream base>`**, so the derivation is not
  lost. Fails if the token is dropped rather than moved.
- **The version in the `wasp/` token is the fork's version and the version in
  the `bee/` token is the upstream base**, which is what catches the two being
  swapped, the mistake with the highest chance of passing a prefix-only test.
- The existing `TestUserAgentLogging` already asserts the agent appears in the
  connection log and must keep passing, unchanged.

Mutations, each of which must break a named test: restore the old order; drop
the `bee/` token; put the upstream base in the `wasp/` token and the fork
version in the `bee/` token. A mutation that fails to compile proves nothing
and is redone.

## Verification on the bench

The unit tests cover the string. What they cannot show is that a peer still
connects, so after deploying:

- both nodes reach their usual connected peer count, and
- `bee_libp2p_handled_connection_count` keeps rising, which is inbound
  connections from peers that are overwhelmingly stock Bee.

That second one is the real check: it is stock Bee nodes choosing to talk to a
node whose agent no longer says `bee/` first.

## Rollout and rollback

No configuration and no on-disk change. Rollback is reverting the merge commit.
A node that rolls back reverts to the old string with no other effect.

## Upstream portability

Not applicable. The `wasp/` token does not exist upstream, so there is nothing
to port and the issue carries no `affects-upstream` label.

## Files

- `pkg/p2p/libp2p/libp2p.go`, `userAgent()`.
- `pkg/p2p/libp2p/libp2p_test.go` or a new test file for the assertions above.
- `docs/DIFFERENCES.md`: the user agent a wasp node presents.
- `docs/experiments/INDEX.md`: the ledger row.

Generated with help of AI.
