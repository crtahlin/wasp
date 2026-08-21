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
