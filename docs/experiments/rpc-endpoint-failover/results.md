# Results — fallback blockchain RPC endpoints

Issue: [#109](https://github.com/crtahlin/wasp/issues/109) ·
Spec: [`spec.md`](spec.md) ·
Code: [#117](https://github.com/crtahlin/wasp/pull/117), [#118](https://github.com/crtahlin/wasp/pull/118)

## Outcome: the node survives an outage that previously killed it

Measured on bench-1, `wasp 0.0.0-untagged-3d1d3d2`, Gnosis mainnet.

**Table — Node survival with its primary RPC endpoint dead**

| arm | endpoints configured | outcome |
|---|---|---|
| **treatment** | primary + standby | **survived 15m43s**, still running when the test ended |
| **control** | primary only | **died at 10m01s** |

Treatment: same process throughout (pid never changed), `/health` `ok`,
`/readiness` `ready`, and `chainTip` advancing at roughly six blocks per thirty
seconds — correct for a five-second block time. The node did not merely stay up;
it kept doing chain work.

Control: `chainTip` unreachable from the moment the endpoint died, `/health`
still reporting `ok`, then the process exited after **601 seconds**. That is
`postageSyncingStallingTimeout`, ten minutes, to the second.

The control is the part that makes this evidence. Without it the treatment shows
only that a node stayed up for fifteen minutes, which a node might do anyway.

## An observation worth keeping

In the control, `/health` reported `ok` for the entire ten minutes while the node
had no chain backend at all and `chainTip` was unreachable throughout. Health did
not notice, and then the process exited. An operator watching `/health` would
have had no warning.

That is arguably its own defect — the endpoint reports process liveness rather
than whether the node can do its job — but it is out of scope here and is
recorded rather than acted on.

## How the outage was produced

Not with a firewall. Both public Gnosis endpoints available to the bench
(`rpc.gnosischain.com` and `rpc.gnosis.gateway.fm`) resolve to the **same
address**, `34.111.230.52`, so no rule could block one without the other — worth
knowing before designing this kind of test, and a reason to be careful about
calling two such endpoints redundant in production.

Instead the primary was a local forwarding proxy on `127.0.0.1:8545` and the
standby was the real endpoint. Killing the proxy is a precise, reversible outage
of exactly one endpoint, and carries no risk of severing the SSH session that is
running the test.

## What this does not cover

- **Recovery**, the `Recover` loop moving back to the primary once it returns. The
  code is tested in unit tests but was not exercised on the node here.
- **A partial failure** — an endpoint that answers but lies, or lags far behind.
  The block-lag bound exists for this and is unit-tested only.
- **Sustained operation** on the standby beyond fifteen minutes.
- **Send behaviour.** `SendTransaction` deliberately does not fail over, and this
  node has no funds, so no transaction was attempted either way.

## Related finding

A first version of the watcher reported `health=none` while the node was healthy:
single quotes inside a heredoc inside a single-quoted `ssh` argument were
stripped, so the parsing failed silently and the harness read as a node failure.
Written locally and copied over instead. A harness that misreports is worse than
no harness, and this one would have reported a false negative for the treatment
arm.
