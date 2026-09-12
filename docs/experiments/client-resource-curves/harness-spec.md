# Spec: the client resource-curve harness

Issue: [#279](https://github.com/crtahlin/wasp/issues/279). Executes the
methodology in [methodology.md](methodology.md) ([#252](https://github.com/crtahlin/wasp/issues/252))
and subsumes the under-load reserve-proof study
([#278](https://github.com/crtahlin/wasp/issues/278)) as its steady-plus-load case.

## Problem

The resource-curve methodology (#252) is written but not executed: it defines the
three regimes (steady operation, radius increase as an eviction burst, radius
decrease as a backfill plateau), the instrumentation, how to induce each regime,
the expected curves, the flatness score, and the pass-or-fail frame. What is
missing is the harness that runs it and the resulting curves. This spec defines
that harness and the run plan, for upstream bee, wasp+goleveldb, and wasp+pebble.

## Goal

Produce, for each of the three clients and each of the three regimes, the
resource-through-time curves and the two flatness numbers from #252, overlaid for
comparison, on one dedicated host, one client's heavy phase at a time. The
deliverable is a results document with the curves; the harness itself is reusable
and applies to any Swarm client, not only this fork.

## Host and the prerequisite

The bench-1 VM, repurposed. It is chosen for its large drive and, more valuable,
its already full goleveldb reserve of about 3.77 million chunks, which is the
starting image for the bee and wasp+goleveldb runs without days of re-syncing.

A resource-curve measurement needs the box to itself: #252 states that if two
nodes share one host they contend and the numbers are meaningless. So before any
run, the current mainnet bench-1 node and the standing baselines it carries, the
crash-watch and the rchash-cron time series, are retired or paused, and the
crash-watch monitor is stopped or repointed. This ends bench-1's present role and
is the one destructive step; it runs only on explicit confirmation. Everything in
this spec is inert until then.

## Design

### Reserve images

- bee and wasp+goleveldb run from a frozen copy of bench-1's existing goleveldb
  reserve, so every run starts from the same store.
- wasp+pebble needs a pebble-format reserve, because the two on-disk formats are
  not interchangeable (docs/DIFFERENCES.md). No converter is built (that rabbit
  hole is out of scope): a fresh pebble node syncs its reserve from the network,
  reusing bench-1's identity key so no wallet funding is needed (the node runs
  swap-disabled), then the filled pebble store is frozen as the pebble image. The
  fill is the long pole, days, so it runs in the background while the bee and
  wasp+goleveldb arms run off the goleveldb image. The large drive holds both
  images plus compaction headroom.
- Each run resets the node to its frozen image, so radius and reserve size start
  known and identical across clients (the procedure's fixed-radius, known-size
  requirement).

### Instrumentation, sampled every ten seconds

- Host, from the operating system: per-core and total processor use, resident
  memory, disk reads and writes as operations per second and megabytes per second
  and average wait, disk space used, page-cache size, network in and out.
- Client, from the metrics endpoint: reserve size and reserve size within radius,
  storage radius, committed depth, per-level file counts, level-0 read-amplification,
  compaction debt, write-stall counters, sampler duration and the sampling-in-progress
  flag (#23), pull-sync and push-sync rates and queue depth, garbage-collection
  cycles, goroutine count, and redistribution phase and round outcome.
- Cross-engine rule: compare at level-0 read-amplification, not raw file count,
  because pebble's trigger counts sublevels and goleveldb's counts files (#278).
  Pebble's sublevel count is exposed as a metric if it is not already; it is a small
  addition and a prerequisite for the pebble runs.
- Run metadata: the clean, committed version string and the full configuration,
  logged before any measurement. A dirty build once made a sample six times slower
  and invalidated a comparison (#252); the harness refuses to measure a version
  string marked dirty.

### The traffic generator

A scriptable, rate-controlled load against the node's API: chunk uploads at a set
rate to feed writes, and retrievals at a set rate to feed reads, run concurrently
and independently. It holds a steady load for the steady regime and drives the
sustained ingestion for the backfill plateau. Rates are logged with each run so a
run is reproducible.

### The regime driver

- Steady: fixed radius, known reserve, the traffic generator holding a light,
  constant load. The baseline window covers at least two redistribution rounds so
  the periodic sampler peak is captured.
- Radius increase, the eviction burst: force the node to deepen its radius, by
  filling the reserve past its capacity or by lowering the committed-depth or
  capacity configuration, so the node evicts the chunks now outside radius. This is
  #252's configuration-manipulation option; it isolates the storage-layer cost
  without waiting for the network.
- Radius decrease, the backfill plateau: after raising capacity or committed depth
  so the node is responsible for more, drive a controlled ingestion rate to
  reproduce the sustained backfill. On a single node this is forced and is labelled
  as such; the natural dynamics need a cluster (phases below).
- The sampler peak within any regime is induced with `/rchash` (unstaked, as in the
  Phase C reserve-proof measurement), or through a private-chain redistribution
  harness if a full round is wanted.

### Analysis and plotting

For each metric, aligned at the event time: baseline level, peak, peak over
baseline, time above a chosen threshold, area above baseline (the total extra
work), and time to return to baseline. Plus the health outcomes during the event:
interface latency at the median and ninety-ninth percentile, any readiness flip,
any write-stall increments, any missed redistribution commit. The two flatness
numbers, peak over baseline and total-extra-work over peak. The three clients'
curves are overlaid per regime at the same reserve size and event size, and the
degradation thresholds, defined in advance, are evaluated. Output is rendered as
charts in a results document.

## Procedure

Per #252: warm each client to steady state on the same host and image; record a
baseline over at least two redistribution rounds; mark the event at time zero;
record until the metrics return to baseline or a maximum window; three runs per
condition, reported with the spread. Only one client's heavy phase runs at a time.

## Phases

- Phase 1, the single repurposed VM: steady, radius increase, forced radius
  decrease, and the #278 load runs, for all three clients. This is the bulk of the
  value and is what the bench-1 host delivers.
- Phase 2, optional: a small private cluster, bee containers on one larger host via
  the beelocal or k3d path (the integration role in docs/agent-playbooks/test-bench.md),
  for natural radius dynamics rather than forced ones.

## Protocol impact

None. The harness measures; it changes no client code, no wire surface, no
on-chain surface, and does not touch the protocol-freeze lock. The clients under
test run their normal builds.

## Configuration

No new client configuration. The harness uses existing settings to induce regimes:
committed depth and reserve capacity for the radius events, and the pull-sync and
puller rate limits and sync intervals (#25, #26, #58, #59) where a run needs to cap
or spread the backfill.

## Rollout and rollback

Phase 1 is confined to the repurposed bench-1 VM and touches no other machine.
Rollback is discarding the VM's harness state; the mainnet role is not resumed on
it, since repurposing is the point. The frozen reserve images are retained for
re-runs.

## Upstream portability

Not a client change, so nothing to port. The method and harness are general and
meant to apply to any Swarm client, including upstream bee, which is one of the
three clients measured. No `affects-upstream` label.

Generated with help of AI.
