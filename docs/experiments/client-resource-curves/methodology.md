# A methodology for measuring Swarm client resource curves across radius events

Issue: [#251](https://github.com/crtahlin/wasp/issues/251). Related: the storage-engine
comparison ([#198](https://github.com/crtahlin/wasp/issues/198)), the probe-sample cost
and accuracy work ([#241](https://github.com/crtahlin/wasp/issues/241),
[#245](https://github.com/crtahlin/wasp/issues/245)), compaction level-0
([#24](https://github.com/crtahlin/wasp/issues/24)), and reserve doubling
([#62](https://github.com/crtahlin/wasp/issues/62)).

**Purpose: give a repeatable way to measure how a Swarm client's resource use behaves over
time, both at rest and during the events that peak it, so an operator can answer two
questions. How much load does this client place on a machine, and can it stop performing
during an event such as a change in storage radius. The best client keeps its resource use
as flat as possible, spreading work over time rather than in sharp bursts. This document is
the method and the expected curves it will test; the measurement runs and any harness are a
later phase, recorded in a results document when done. The expected curves here are reasoned
from earlier measured work and are stated as hypotheses, not results.**

Terms used once and then reused. Reserve: the set of chunks a node is paid to store.
Storage radius: how deep into the address space the node's responsibility reaches; a larger
radius means a smaller neighborhood and fewer chunks. Reserve sample, or proof of resources:
the periodic proof, run every redistribution round, that a node still holds its share.
Compaction: the background work a storage engine does to merge its on-disk files. Level 0,
or L0: the top layer of that on-disk structure, whose depth drives read cost.

## What consumes resources in a storage node

Five recurring load sources, listed with what stresses each.

1. Pull-sync and push-sync ingestion. Continuous network download and chunk writes (a blob
   append plus an index insert). Roughly proportional to how much the node still needs to
   fetch, so it is high while a node is filling and low once it is full.
2. Reserve maintenance. A periodic wake-up (15 minutes by default) that counts the reserve
   and evicts what no longer belongs. Cheap except during the events below.
3. The redistribution sampler. Every round it makes a full pass over the chunks within
   radius, two to four million of them, to build the proof of resources. This is the largest
   periodic peak in central-processor and disk use, about 30 to 50 seconds on the bench, and
   it is deadline-bound: it must finish inside the commit phase of the round or the node
   loses that round.
4. Index compaction. The storage engine's background file merging. Its cost tracks the write
   rate, and its shape is where goleveldb and pebble differ most.
5. Chain interaction. Postage events and redistribution transactions. Light.

Only the sampler is measured so far ([#241](https://github.com/crtahlin/wasp/issues/241),
[#198](https://github.com/crtahlin/wasp/issues/198),
[#54](https://github.com/crtahlin/wasp/issues/54)). The event behavior below is the gap this
study fills.

## The three regimes

- Regular operation. A full node at a fixed radius, in steady state. The load is the
  baseline sync and compaction plus the periodic sampler peak.
- Radius increase, an eviction burst. The network has grown, so the node's neighborhood
  shrinks to a deeper prefix. The chunks now outside the neighborhood are evicted in bulk,
  which is a deletion, compaction, and disk-reclaim burst. The count of chunks within radius
  drops, so the next sampler peak is smaller. This event is sharp and short.
- Radius decrease, a backfill plateau. The network has shrunk, so the neighborhood grows to
  a shallower prefix. The node must pull-sync a large volume of chunks it does not yet hold,
  which is a sustained period of high ingestion, writes, and compaction. The reserve grows,
  so the next sampler peak is larger. This event is a long plateau, not a peak.

The asymmetry is the point. An increase is a short burst of deletion and compaction; a
decrease is a long plateau of sustained writing.

## Expected curves, stated as hypotheses

These are reasoned from the merged findings and are what the measurement will confirm or
refute. They are not results.

**Table: expected load profile by client and regime. This is a set of hypotheses to test,
not measured data. V marks a point already verified on the bench, H marks a hypothesis.**

| Regime | Upstream bee, goleveldb | wasp, goleveldb | wasp, pebble |
|---|---|---|---|
| Regular | steady sync, plus a slower sampler peak (no SIMD by default) and bursty goleveldb compaction | shorter sampler peak (SIMD about 25 percent, V, [#54](https://github.com/crtahlin/wasp/issues/54); read and hash split, [#9](https://github.com/crtahlin/wasp/issues/9)); sync rate and interval are tunable to spread load | shortest read latency; flattest compaction (leveled); sampler at parity with goleveldb (V, about 33 seconds at 3.77M) |
| Radius increase, eviction burst | bulk delete and compaction; goleveldb reclaims disk fast but stalls reads while doing so (H, [#198](https://github.com/crtahlin/wasp/issues/198)); risk of missing a round if it overlaps the commit phase | same engine, same stall risk, but a faster sampler leaves more headroom | stays responsive during eviction (H, [#198](https://github.com/crtahlin/wasp/issues/198)); reclaims disk more slowly but without the read stall |
| Radius decrease, backfill plateau | sustained sync; level 0 grows toward the write-slowdown trigger (8) and write-pause trigger (12) if ingestion outruns compaction; no configuration to spread it | the same, but pull-sync and puller rate limits ([#25](https://github.com/crtahlin/wasp/issues/25), [#26](https://github.com/crtahlin/wasp/issues/26)) and sync intervals ([#58](https://github.com/crtahlin/wasp/issues/58), [#59](https://github.com/crtahlin/wasp/issues/59)) let an operator cap and spread the backfill | the same knobs, plus a shallow-L0 leveled engine that absorbs the write plateau more smoothly (H) |

The through-line to test: wasp on pebble should be the closest to a flat profile, because leveled
compaction spreads write amplification, it is expected to stay responsive during eviction, and
the fork's rate and interval knobs let an operator cap the backfill. goleveldb's advantage is
faster disk reclaim after an eviction, at the cost of read stalls while it reclaims.

## Can a client stop performing

Three failure modes to watch for, each event-driven, defined so the measurement can detect them.

1. A missed redistribution round. An eviction burst or an over-long sampler runs past the
   commit-phase deadline, the node fails to commit its proof, and it loses the round. Most
   likely on goleveldb during a radius increase.
2. A write stall. During backfill, ingestion outruns compaction, level 0 reaches the pause
   trigger, and sync throughput drops to zero until compaction catches up.
   [#24](https://github.com/crtahlin/wasp/issues/24) found the trigger alone needs about
   two gigabytes per second of sustained ingestion to reach, which a normal link cannot, but
   a backfill together with the periodic sampler could combine to reach it.
3. Read unresponsiveness. Reads stall during a mass-eviction compaction. Expected on
   goleveldb, expected to be avoided on pebble.

## The measurement method

### Instrumentation

Sample at a fixed interval, about every 10 seconds, the same for every client.

- Host: central-processor use per core and total, resident memory, disk reads and writes as
  operations per second and megabytes per second and average wait, disk space used, page
  cache size, network in and out.
- Client, from the metrics endpoint: reserve size and reserve size within radius, storage
  radius, committed depth, storage-engine level file counts and compaction debt and any
  write-stall counters, sampler duration and whether a sample is running
  ([#23](https://github.com/crtahlin/wasp/issues/23)), pull-sync and push-sync rates and
  queue depth, garbage-collection cycles, goroutine count, redistribution phase and round
  outcome.

Record as run metadata the clean running version and the full configuration. A build made
from uncommitted changes, marked dirty, once made a node's sample about six times slower and
silently invalidated a comparison; the running version must be a clean, committed build and
must be logged before any measurement.

### Inducing each regime

Radius changes are driven by the network, so choose the level of control the question needs,
and state which was used.

- A private test network where nodes can be added or removed to force a radius change on the
  node under test. The most reproducible, the least like the real network.
- Manipulating the node's reserve capacity or committed depth through configuration to force
  it to grow or shrink on demand. This isolates the storage-layer cost of the event without
  waiting for the network to move.
- Driving a controlled ingestion rate to reproduce the backfill plateau, and triggering an
  eviction directly to reproduce the burst.
- Running on the main network and capturing a natural radius change when it happens, tagged
  from the storage-radius metric. The most realistic, the least controllable.

### Procedure

- Warm each client to steady state at a fixed radius and a known reserve size, on matched
  hardware. If two nodes share one physical host, run only one node's heavy phase at a time;
  running both together makes them contend and the numbers meaningless.
- Record a baseline window that covers at least two redistribution rounds, so the periodic
  sampler peak is captured.
- Mark the event at time zero and record through it until the metrics return to baseline or a
  maximum window is reached.
- Three runs per condition, reporting the spread, matching the bench rule that one run
  measures the node's mood rather than its behavior.

### Derived curves and the flatness score

For each metric, aligned at the event time, report the baseline level, the peak, the peak
divided by the baseline, the time spent above a chosen threshold, the area above the baseline
which is the total extra work, and the time to return to baseline. Alongside, record the
health outcomes during the event: the interface latency at the median and 99th percentile,
any change in the readiness signal, any write-stall increments, and any missed redistribution
round.

Two numbers summarize how flat a client is, which is the property the operator wants.

- Peak divided by baseline, where lower is flatter.
- Total extra work divided by peak, where higher means the same work was spread more evenly.

### Comparison and the pass-or-fail frame

Overlay the three clients' curves for each regime, at the same reserve size, on the same
hardware or the same host one node at a time, with the same event size. Define the
degradation thresholds in advance: an interface 99th-percentile latency above a chosen bound,
a readiness signal turning false, a missed redistribution commit, or sync throughput dropping
to zero for longer than a chosen time. A client stops performing if it crosses one during an
event. The aim is that none do, and that the flattest client sits furthest from them.

## Scope

This document is the method and the expected curves. The measurement runs, the instrumentation
harness, and the resulting curves are the next phase and will be recorded in a results
document. The method is general and meant to apply to any Swarm client, not only this fork. It
changes nothing in the client.

Generated with help of AI.
