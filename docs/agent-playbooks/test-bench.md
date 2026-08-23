# Test bench

Most of what this repository produces is a claim about performance or network
behaviour. A claim about peer discovery or hashing throughput is worth nothing
until it has been measured on a real node against the real network. The bench is
where that happens.

> **Status: not yet provisioned.** This playbook is written ahead of the
> machines so the measurement method is fixed before there is any temptation to
> reverse-engineer it around a result.
>
> For what to provision and why, see
> [`bench-vm-spec.md`](bench-vm-spec.md). Machines for this project are
> provisioned fresh; no existing node estate is reused.

## Roles

| Role | Purpose |
|---|---|
| `mainnet-canary` | Runs the current release against Swarm mainnet. Confirms a build connects to stock peers and stays healthy. This is the gate on merging any experiment |
| `bench-1` | Repeatable before-and-after measurement: the same workload against a stock build and an experimental build |
| `integration-1` | Runs the local k3d cluster suite (`make beelocal`, `make deploylocal`, `make testlocal`), restoring the coverage lost when upstream's beekeeper workflow was removed |
| `soak-1` | Long-running experiments whose effect only appears over days |

Machines are referenced by role name everywhere — issues, specs, results,
commit messages. **Never by address.**

## Where the inventory lives

**Not in this repository.** This repository is public.

Hostnames, addresses, SSH configuration, node keys, wallet keys, and postage
batch identifiers live outside it, in the operator's private workspace, and are
recorded in engram memory under an infrastructure domain. What lives here is
`infra/` — provisioning scripts and unit templates that read their targets from
an untracked inventory file.

## Validating a build (the merge gate)

An HTTP 200 is not proof a node is healthy. Check all of it:

```bash
curl -s localhost:1633/health     | jq        # status, version, apiVersion
curl -s localhost:1633/readiness  | jq        # ready, and the version it reports
curl -s localhost:1633/status     | jq        # beeMode, reserveSize, storageRadius, committedDepth
curl -s localhost:1633/peers      | jq '.peers | length'
curl -s localhost:1633/topology   | jq '{depth, connected, population}'
```

Then watch the logs for handshake failures — `incompatible network ID`, protocol
version mismatches, or a peer count that climbs and then collapses. A node that
starts, reports healthy, and quietly fails to peer is the exact failure mode this
fork is most likely to introduce, and it does not show up in `/health`.

Confirm the version string is what you think it is:

```bash
bee version                    # fork line
curl -s localhost:1633/health | jq -r .version
```

## Triggering a reserve sample without stake

An **unstaked node never samples.** The redistribution agent fails every round
with `IsPlaying: ... execution reverted`, so the sampling phase never runs and
`SampleStats` never appears. Staking is not a workaround: participation is a
lottery, so you cannot choose when a sample happens, which makes it useless for
measurement.

`GET /rchash/{depth}/{anchor1}/{anchor2}` runs the sampler directly. Its own
source comment says it exists for testing the sampler.

**The anchor cannot be arbitrary, and this is the part that wastes an afternoon.**
Phase 1 of the sampler filters:

```go
if swarm.Proximity(ch.Address.Bytes(), anchor) < committedDepth {
    return false, nil   // skip
}
```

Only chunks within `committedDepth` of the anchor are sampled. A random anchor
addresses a neighbourhood this node does not store, so every chunk is skipped,
`TotalIterated` comes back 0, and the request fails with

```
make proofs: reserve sample items should have 16 elements
```

which reads like a bug and is not one. **anchor1 must be the node's own overlay**
(or something inside its neighbourhood); anchor2 can be random:

```bash
OVERLAY=$(curl -s localhost:1633/addresses | jq -r .overlay)
DEPTH=$(curl -s localhost:1633/status  | jq -r .storageRadius)
curl -s "localhost:1633/rchash/$DEPTH/$OVERLAY/$(openssl rand -hex 32)"
```

Takes 80-170 seconds on a full reserve. Read the result from the log, not the
HTTP body:

```bash
journalctl -u bee --since '-30 min' | grep 'reserve sampler finished' | tail -1
```

One trap when reading the response: `jq '{hash, durationSeconds}'` prints `null`
for **missing** keys, so an HTTP 500 error body looks identical to a successful
but empty result. Always check the status code, or print the raw body.

## Three runs minimum, or do not report a ratio

**A single run of anything I/O-adjacent on a live node measures the node's mood.**

Two sampling runs on this bench with **identical code and identical peer count**
differed by **1.82x** on disk time per chunk — 225.83 µs against 123.94 µs. That
noise floor swamps most effects worth measuring, and it has already produced one
wrong published figure and one prematurely closed issue.

Rules:

- **At least three runs per condition.** Report the spread, never a lone number.
- **Match node state across the comparison**: comparable peer count, a fixed
  interval after restart, the same storage radius. Radius drifts as the reserve
  fills and silently changes what is being compared.
- **Normalise per chunk iterated.** `TotalIterated` varies between runs, so raw
  `TaddrDuration` and `ChunkLoadDuration` totals are not comparable; the
  per-chunk figures are.
- A restarted node is not a settled node. Peer count recovering from 2 to 79
  changed disk time per chunk by 2.6x on its own.

## Measuring an experiment

The method, fixed in advance in the experiment's `measurement.md`:

1. **Baseline.** The stock build for the same upstream base — that is what
   `v0.1.0` exists for, an unmodified `v2.8.1` with fork packaging.
2. **Same everything else.** Same machine, same config, same postage batch
   depth, same neighbourhood if it matters, same duration.
3. **Repeat.** A single run of anything network-adjacent measures the network,
   not the change. Three runs minimum, report the spread and not just the mean.
4. **State what a negative result looks like** before running, and honour it.
5. Record raw numbers in `results.md`, not just conclusions. Someone will want
   to re-derive them.

Every table in a result document gets a caption naming the build, the machine
role, the network, and the conditions — results get read on their own, out of
context.

## Self-hosted CI runner

Only after the roles above are stable, and with one hard constraint: **this is a
public repository**. A self-hosted runner reachable from `pull_request` events
would let any stranger's fork execute code on the machine. If a runner is
attached at all, it must be restricted to workflows triggered by the repository
owner, never `pull_request` from forks.

## Staking risk

Running non-reference code on a mainnet node that has stake at risk can cost
real value if the node misbehaves during a redistribution round. Canary and
bench nodes should be unstaked, or staked only with an amount that is acceptable
to lose. This is not a theoretical caution — it is the main way an experiment
here could cost money.

## When the node crashes, read the crashing goroutine first

Go dumps every goroutine on a fatal error, so a naive grep over the dump finds
whatever the node happened to be doing — pullsync, kademlia, sharky — and invites
you to blame it. Only one goroutine is crashing. Find the `[running]` one, or the
`runtime stack:` block that precedes it, and read that.

This matters because the two failure classes look identical in a log summary and
have nothing to do with each other:

| Sign | Meaning |
|---|---|
| `panic:` with a bee frame in the running goroutine | a real bee/wasp bug |
| `fatal error:` with only `runtime.*` frames, on a GC worker or `runtime stack:` | a Go runtime bug, or genuine memory corruption from `unsafe`/cgo |

The runtime's heap-integrity assertions — `found pointer to free object`,
`sweep increased allocation count`, `s.allocCount != s.nelems`, `fault`, and
`index out of range` raised from `runtime.panicBounds` on the system stack — are
all the second class. With `CGO_ENABLED=0` and no `unsafe` in the change under
test, a bee-level data race is not a sufficient explanation for them.

Issue #69 is the worked example: several days were spent attributing these crashes
to a Sharky change, and a correct change was reverted, because the dump was read as
"sharky appears in the stack" rather than "the crashing goroutine is a GC worker".

## GOEXPERIMENT is a build-time variable

`GOEXPERIMENT` configures the Go toolchain when it compiles. Setting it in a
systemd unit, a shell profile, or any other runtime environment has **no effect
whatsoever** — and it fails silently, so an experiment that "disabled" a GC feature
this way produces confident, entirely meaningless results.

Set it for the build, and then verify it landed in the binary rather than assuming:

```bash
make binary                                    # the Makefile sets the flag itself
go version -m dist/bee | grep GOEXPERIMENT     # must print: build GOEXPERIMENT=nogreenteagc
```

The Makefile assigns it with `:=`, so an inherited `GOEXPERIMENT` in your environment
cannot silently override it. To deliberately build *with* Green Tea — to retest it
once Go ships a fix — pass it on the command line, which does win:

```bash
make GOEXPERIMENT=greenteagc binary
```

Verify the **deployed** binary too, not just the one you built:

```bash
go version -m /usr/bin/bee | grep GOEXPERIMENT
```

Do not try to confirm this by looking for GC symbols with `go tool nm` — release
binaries are linked with `-s -w` and carry no symbol table, so the check returns
zero for both a correct and an incorrect build. The build info is the only
trustworthy signal. CI enforces it on every push; see `.github/workflows/go.yml`.
