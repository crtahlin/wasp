# Fallback blockchain RPC endpoints

Issue: [#109](https://github.com/crtahlin/wasp/issues/109)

## Problem

`blockchain-rpc-endpoint` takes exactly one endpoint. There is one dial site, it
runs once at startup, and nothing re-dials it afterwards:

```
pkg/node/chain.go:84
  rpcClient, err := rpc.DialOptions(ctx, rpcCfg.Endpoint, ...)
```

A `grep` for `rpc.Dial`/`ethclient.Dial` across non-test code returns that line
and one unrelated ENS resolver dial. `pkg/transaction/wrapped/` contains no retry
or backoff.

When the endpoint fails at startup, the node refuses to start; the log tells the
operator to reconfigure and restart. When it fails at runtime the outcome is
worse, because it is delayed and then terminal:

1. Chain calls fail. The node stays up but degrades — batch events stop syncing
   so stamp validity drifts from consensus, the redistribution agent cannot play
   its rounds, chequebook operations fail.
2. After `postageSyncingStallingTimeout` (10 minutes, `pkg/node/node.go:208`)
   the listener returns `ErrPostageSyncingStalled` and signals `syncingStopped`,
   which `cmd/bee/cmd/start.go:108` turns into a full shutdown.
3. If the endpoint is still down, the node will not start again.

A transient provider outage therefore becomes an operator-attended outage that
costs redistribution rounds throughout.

## Hypothesis

The node's dependence on a single endpoint is the whole of the problem, and it is
addressable without touching consensus-relevant behaviour, because **every chain
call bee makes is a discrete request/response**. `transaction.Backend` embeds
`backend.Geth`, which is 16 methods, all request/response. `FilterLogs` polls; there
is no `SubscribeFilterLogs` and no long-lived subscription anywhere. So there is
no streaming state to migrate on failover, and a per-call routing layer is
sufficient.

If that is right, routing each call to the first healthy endpoint should let the
node ride out an outage of any single provider with no operator action and no
change to what the node computes.

If it is wrong, the failure will be that endpoints disagree in ways bee cannot
tolerate — most plausibly head-block regression on failover being read as a
reorg by the postage listener. That is the risk the design below spends most of
its care on, and a negative result would look like the listener re-processing or
stalling after a switch. That outcome would still be worth knowing, and would
push the answer toward requiring endpoints to be closely synchronised rather
than abandoning failover.

## Design

A new package, `pkg/transaction/failover`, implementing `transaction.Backend`
over an ordered list of backends, each built from its own `rpc.Client`. It sits
exactly where the single backend sits today, so nothing downstream changes.

```
InitChain
  └─ for each endpoint: rpc.DialOptions → ethclient → wrapped.NewBackend
       └─ failover.New([]transaction.Backend, logger) → transaction.Backend
```

**Selection is priority order, not round-robin.** Operators generally have a
preferred provider — paid, closer, higher rate limit — and a worse standby.
Round-robin would send half the traffic to the worse one permanently and would
double the exposure to any one provider's inconsistency.

**Failover triggers on transport failure, never on an answer.** The distinction
is load-bearing:

| Class | Examples | Action |
|---|---|---|
| Transport | connection refused, DNS failure, TLS error, timeout, HTTP 5xx, HTTP 429 | try the next endpoint |
| Answer | `execution reverted`, invalid params, a returned error from a contract call | **return it** |

Retrying a revert on another endpoint would be wrong twice over: it is a correct
answer, and hiding it behind a second opinion would mask a genuine contract or
configuration problem.

**`SendTransaction` does not fail over.** If the primary accepted the
transaction and the response was lost, resending elsewhere risks a duplicate, and
the two endpoints may have different mempool views so `PendingNonceAt` can
disagree. bee already persists pending transactions through
`transaction.Service` and its state store, and has a monitor that watches for
confirmation. On a send failure the failover layer returns the error and lets
that existing machinery own the retry. Reads fail over; writes do not.

**Chain ID is validated per endpoint at startup and any mismatch is fatal.** A
disagreement means the operator pointed at the wrong network, which is a
configuration error, not a health problem. Failing over to it would silently put
the node on a different chain.

**Head-block regression is bounded.** A secondary may legitimately be a block or
two behind. `failover` tracks the highest block number it has observed and, on a
switch, rejects an endpoint reporting a height more than `maxBlockLag` behind it,
moving to the next instead. This is the mechanism protecting the postage
listener from reading a switch as a reorg.

**Recovery is periodic and biased to the primary.** A background probe checks
higher-priority endpoints on an interval and switches back when one is healthy
and within `maxBlockLag`. To avoid flapping, an endpoint that has failed is not
retried for a backoff period that grows on repeated failure.

**Every switch is logged at Info**, naming the endpoint moved from and to and
the reason. An operator debugging odd behaviour should not have to infer which
provider was serving.

Config plumbing: `optionNameBlockchainRpcEndpoint` becomes a `StringSlice`, and
`node.Options.BlockchainRpcEndpoint` becomes `[]string`. The nested
`blockchain-rpc.endpoint` form and `bindBlockchainRpcConfig` must keep working.
Note that viper's `GetStringSlice` on a scalar string splits on whitespace; URLs
contain none, so a single-endpoint config yields a one-element slice, but this
needs an explicit test rather than trust.

## Protocol impact

**None.** This touches no value in `.github/protocol-freeze.lock`. It is
client-side connection management: no wire format, no stream name, no protocol
version, no on-disk format, no migration. Nodes with and without it are
indistinguishable to peers, because peers never observe which RPC endpoint a node
talks to.

The one consensus-adjacent surface is indirect: the postage listener derives
batch state from chain events, and batch state does affect what a node stores and
how it is judged in redistribution. That is why head-block regression is bounded
rather than tolerated, and why chain ID mismatch is fatal.

## Measurement

On bench-1, with a primary and a secondary configured.

1. Confirm the node is healthy and the postage listener is advancing.
2. Block the primary at the firewall.
3. Assert: chain calls keep succeeding, the listener keeps advancing, a switch is
   logged, and **the node is still running after 15 minutes** — past the
   ten-minute stall timeout that currently kills it.
4. Restore the primary; confirm the node returns to it and does not flap.

**The control matters more than the test.** Run the same procedure against the
current build, where the node must shut down at ten minutes. Without that, the
test cannot fail, and a test that cannot fail is not evidence. This project has
already produced one soak that reported success because its harness could not
observe failure.

**A negative result** looks like the listener stalling or re-processing after a
switch because the secondary was behind. That would mean `maxBlockLag` is doing
its job badly, or that bee's event handling is less tolerant of a backwards step
than assumed, and it should be reported as a constraint on how far apart
endpoints may be — not as a reason to drop failover.

## Rollout and rollback

Additive and off by default in effect: a single-endpoint configuration behaves
exactly as it does today, one endpoint with no failover partner. An operator opts
in by listing more than one. Rolling back is removing the extra entries; there is
no persisted state, no migration, and nothing to undo. Reverting the commit
restores the single-string option.

## Upstream portability

Good. The change is contained in one new package plus config plumbing, it does
not touch consensus or protocol code, and it keeps the existing behaviour as the
default. That is the shape Ethersphere is most likely to accept — it changes
nothing for an operator who does not opt in.

The failover package has no wasp-specific dependencies and would apply to
`ethersphere/bee` unmodified. `scripts/export-patch.sh rpc-endpoint-failover`
generates the series against current upstream.

## Configuration

| | |
|---|---|
| flag | `blockchain-rpc-endpoint` (now repeatable) |
| default | unchanged — whatever single endpoint is configured today; no fallback unless a second is listed |
| adding endpoints costs | more startup validation calls; a chance of serving reads from a slightly stale secondary; more provider accounts to hold |
| listing none costs | today's behaviour: a single point of failure that shuts the node down after ten minutes |
| costs other nodes | nothing. This is the node's own upstream connection and is not peer-facing |

Two internal constants that should be exposed only if measurement shows they
matter, per rule 8:

| | |
|---|---|
| `maxBlockLag` | how far behind a candidate endpoint may be before it is skipped. Too low and failover fails when it is most needed; too high and the listener sees a backwards step |
| probe interval and failure backoff | too short and a half-broken endpoint is retried constantly; too long and the node stays on the standby well after the primary recovers |
