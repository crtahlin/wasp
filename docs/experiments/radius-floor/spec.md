# Storer: a doubled node does not lower its radius below its neighbourhood's depth, or into a lower radius that is already full

Issue: [#822](https://github.com/crtahlin/wasp/issues/822). Type: fix. Areas: storage, incentives. Stake risk: yes.
Builds on #696 (`docs/experiments/radius-decrease-after-sync/spec.md`, the settle window) and #583 (the stall gate). Related: #658 and #663 (`docs/experiments/lottery-gate-expiry-and-depth/spec.md`, when a selected node sits out a round).

## Terms

- **Doubling d:** `reserve-capacity-doubling`. The reserve capacity is the base capacity times 2^d, and the **committed depth** is radius + d (`CommittedDepth`, `pkg/storer/reserve.go:708-714`). It is the depth the node samples and reveals at.
- **Reserve worker check:** the `thresholdTicker` case of `reserveWorker` (`pkg/storer/reserve.go:326-366`), every `reserve-wakeup-duration`, 15 minutes by default.
- **Decrease condition:** `count < threshold(capacity) && SyncRate() == 0 && radius > minimumRadius` (`reserve.go:347`), with `threshold(capacity) = capacity * 5 / 10` (`reserve.go:81`) and `count` the reserve chunks at or above the radius.
- **Unreserve loop:** `unreserve` (`reserve.go:568-671`). When the reserve is over capacity it evicts, batch by batch, chunks in bins below the radius, up to the eviction target (size minus capacity); if that does not reach the target it raises the radius by one and repeats.
- **Neighbours:** connected full-node peers within the node's storage radius that answered the status request in the last salud pass (`peer.neighbor`, `pkg/salud/salud.go:170`).
- **Neighbourhood depth N:** the most common committed depth among the neighbours, as salud computes it (`committedDepth`, `salud.go:316-334`, the second return value, today exported only as the gauge `bee_salud_neighborhood_radius`).
- **Network depth M:** the most common committed depth among all peers that answered, from the same salud pass (the first return value; salud already publishes it to `SubscribeNetworkStorageRadius`).
- **Floor F:** `max(minimumRadius, min(N, M) - d)`, defined only when the reading is robust (Design, part 1).
- **Held at r - 1:** the number of reserve chunks the node already stores in bins at or above r - 1, that is `count` plus the chunks it holds in bin r - 1.

## Problem

Measured on production nodes on 2026-10-19, build 7158c0ed9, mainnet network depth 10. These are the operator's measurements; this spec did not repeat them.

- A doubling-1 node (capacity 2^23 = 8,388,608 chunks) holds about 4.06 M chunks within radius 9, under the threshold of 4,194,304.
- Every 30 minutes it logs `reserve radius decrease radius=8`; about a minute later an unreserve at radius 8 evicts a few chunks and logs `reserve radius increase radius=9`.
- At radius 8 it commits at depth 9 while its neighbourhood plays at depth 10.
- In one cycle it settled at radius 8 with 8.37 M chunks, just under capacity, and stays there until new data fills it.
- The median node in the same fleet holds 4.59 M within radius 9; another holds 4.29 M, 2.4 % over the threshold. Any neighbourhood that holds less than half of a doubled node's capacity triggers the decrease, so this is general for doubling nodes.

### What the code says (read, not measured)

The measured sequence matches the code, step by step:

1. **The decrease.** At radius 9 the decrease condition holds (4.06 M < 4,194,304, rate 0 once the puller is idle). After the #696 settle window the check lowers the radius to 8.
2. **The refill overflows.** The reserve is full at radius 9 before the decrease, because the unreserve loop evicts only down to capacity: the bin 8 chunks from the previous cycle are still there, outside the radius. After the decrease the puller fetches the missing bin 8 chunks, the reserve goes over capacity, and the unreserve loop at radius 8 finds only a few chunks below bin 8 to evict ("evicts a few chunks"), so it raises the radius to 9 and evicts in bin 8 down to capacity. The next cycle starts from the same state.
3. **The 30-minute period.** After the increase the puller acts on radius 9 within one recalculation, so `ActedOnRadius` reports radius 9 with a fresh `since`. The next check (15 minutes after the decrease) finds the settle window still running and defers (`puller_settling`); the check after that decreases again. Without #696, as on upstream, the period would be 15 minutes (hypothesis, from the code).
4. **The depth.** At radius 8 the committed depth is 9. salud marks the node unhealthy when its neighbourhood depth equals the network depth and its own committed depth differs (`salud.go:247-250`), and the storage incentives agent then skips a round (`pkg/storageincentives/agent.go:540`). So for most of the time at radius 8 the node cannot win. The freeze risk is in a narrower window: salud passes are up to 5 minutes apart, and the agent reads the health flag once per phase (`agent.go:250`), so a sample taken between the decrease and the next salud pass is made, committed and revealed at depth 9, against a neighbourhood revealing at depth 10.

Point 4 qualifies the issue's "risks a freeze": the risk is real but limited to that window; the larger cost is skipped rounds. This is reasoned from the code, not measured.

### Why the decrease rule is wrong for a doubled node

The rule says: under half capacity, one step down roughly doubles the content (the sibling neighbourhood is about as large as the node's own), so it still fits. Two things go wrong for a node with d > 0:

- **The depth.** A doubled node stores 2^d neighbourhoods at depth N so that it can commit at depth N, the same depth as its neighbours. Below N - d its committed depth differs from theirs. A stock node's decrease is how a neighbourhood's depth falls; a doubled node that decreases on its own no longer commits at the same depth as its neighbourhood.
- **The fit.** "About twice" is an estimate. A node just under half capacity is, at the lower radius, at or just over capacity, and its own reserve already says so: it holds most of bin r - 1 from earlier. The rule does not look.

The same is on upstream v2.8.3: the decrease condition at `pkg/storer/reserve.go:176`, the unreserve loop's `radius++` at line 398, and salud's self-health check at `pkg/salud/salud.go:247`, checked with `git show upstream/v2.8.3:<path>`.

## Signals for the depth

| Signal | What it gives | Used? |
|---|---|---|
| Neighbours' committed depth over the status protocol (salud) | N: the depth the node's own neighbourhood commits at, already computed every salud pass, already used for the node's self-health. Reported by each peer about itself. | **Yes, primary.** |
| All peers' committed depth over the status protocol (salud) | M: the depth most of the network commits at, over far more peers than N. | **Yes, as the lead when the network shrinks** (part 1). |
| Reveals on chain (`Revealed` event with `depth`, `TruthSelected`, `winner().depth`, `currentMinimumDepth()`, all in the mainnet redistribution ABI, `go-storage-incentives-abi` v0.9.4) | The depth staked nodes actually revealed at, weighted by stake, but for the neighbourhood the round selected, a different one each round. | **No.** Wasp binds none of these today (the agent only checks its own reveal, `revealer.go`); adding them is a chain call or a log filter per round. Kept as an open question: a cross-check against peers that misreport. |
| Peers' storage radius via pullsync | Nothing: the pullsync messages carry cursors and chunks, not radii. | No. |
| Kademlia neighbourhood depth (`recalcDepth`, `pkg/topology/kademlia/kademlia.go:1073`) | The connectivity depth, from how many peers are connected per bin, and it follows the node's own storage radius (`SetStorageRadius`). It says nothing about the reserve depth of others, and using it would be circular. | No. |

## Hypothesis

1. A doubled node whose radius never goes below `min(N, M) - d` commits at its neighbourhood's depth, so it neither skips rounds nor risks a freeze from this cause, and the flap cannot start from a decrease below that floor.
2. A decrease that goes ahead only when the chunks held at r - 1 are clearly under capacity no longer starts the flap for any node, doubled or not, and costs nothing: when they are not under capacity, the decrease cannot give the node room, so it would only refill bin r - 1 and step back.

## Design

All in `pkg/storer` except the salud export and the wiring. Order in the reserve worker check:

```
case <-thresholdTicker.C:
    radius := db.reserve.Radius()
    count := reserveWakeupScan(...)            // unchanged
    lift to the floor (part 2)                 // new, before the stall gate: it is an increase
    stall gate (#583)                          // unchanged
    if decrease condition {                    // unchanged
        floor (part 1)                         // new: reason below_neighbourhood_depth
        fit check (part 3)                     // new: reason lower_radius_full
        #696 deferral                          // unchanged
        radius--                               // unchanged
    }
```

A deferral is counted once per check, under the first reason that applies.

### 1. The floor (doubled nodes only)

**salud exports the reading.** `committedDepth` also returns, for the neighbourhood and for the network, how many peers answered and how many of them report the most common depth. salud keeps the last reading with its time and implements a new interface defined in `pkg/storer` (salud imports `storer`, so the interface lives there):

```go
// DepthReporter is implemented by salud. It reports the committed depths the
// neighbours and the network play at, as the last salud pass computed them (#822).
type DepthReporter interface {
	CommittedDepths() (DepthReading, bool) // false before the first pass
}

type DepthReading struct {
	Neighbourhood, Network     uint8     // most common committed depth
	Neighbours, NeighboursAt   int       // neighbours that answered; of them, at Neighbourhood
	Peers, PeersAt             int       // all peers that answered; of them, at Network
	At                         time.Time // when the pass finished
}
```

`node.go` calls `localStore.SetDepthReporter(saludService)` next to `SetPostageSyncHealth`, before `StartReserveWorker`, on every full node; the storer ignores it when d = 0. Without a reporter (the existing tests, a light node) nothing changes.

**When the reading is robust.** Each depth is used only when at least 3 peers answered for it and more than half of them report the most common value (so a tie, or a spread such as 2 at 10 and 2 at 9, is not robust), and the reading is at most 15 minutes old (three salud passes at its maximum interval of 5 minutes). Then:

- both robust: `F = max(minimumRadius, min(N, M) - d)`;
- only N robust: `F = max(minimumRadius, N - d)`;
- N not robust: no floor; the fit check (part 3) still applies.

**A floor value is acted on only after two consecutive checks computed it.** Until then the previous settled floor stays, and before any settled floor there is none. A salud reading that alternates between two depths therefore does not move the radius every 15 minutes.

**The floor check.** When the decrease condition holds, d > 0, a settled floor exists and `radius - 1 < F`, the decrease is deferred, reason `below_neighbourhood_depth`.

**Why `min(N, M)`, and what happens when the depth changes:**

- **The network shrinks**, for example when a large postage batch expires. Chunks of a batch are spread evenly over the address space, so an expiry lowers the content of every neighbourhood alike. Stock nodes everywhere decrease first, M falls, the floor follows within two checks, and the doubled node's decrease goes ahead under the normal rule (decrease condition, fit check, #696 settle). Using M as well as N is what lets a neighbourhood made only of doubled wasp nodes decrease: none of them would lead, but M falls with the rest of the network.
- **The network or the neighbourhood grows** (N rises): the floor rises above the radius, and part 2 lifts the radius.
- **A neighbourhood deeper than the network** (N > M): the floor follows M, so a doubled node there may still decrease below N - d. Its decrease condition rarely holds in a dense neighbourhood. Recorded as a known limit, and the flap metric (part 4) would show it.
- **No neighbours, or too few answering** (a sparse neighbourhood, a restart before salud's first pass): no floor, behaviour as today plus the fit check.

### 2. Lift to the floor (doubled nodes only)

At the check, when d > 0, a settled floor exists, `radius < F` and no reserve sample is running (`IsSampling`), the radius is raised by one step with `SetRadius(radius + 1)`. **Nothing is evicted.** The chunks in the bin below become out-of-radius chunks, which the unreserve loop evicts first when the reserve next goes over capacity, as it does for chunks below the radius today. The puller cancels that bin at its next recalculation. One step per check.

This covers a node that already sits below the floor: the node in the Problem that settled at radius 8 with 8.37 M chunks, a node restarted with a persisted radius from an older build, and a neighbourhood whose depth rose.

The lift is an increase, so it counts in `RadiusState` and the agent skips a round whose sample overlapped it (#658), as for any increase. It waits while a sample runs, so it never lands inside one. It is placed before the stall gate because the stall gate blocks only decreases.

### 3. The fit check (all nodes)

When the decrease condition holds and the floor allows it, the worker counts the chunks it holds in bin r - 1 (`IterateBin(radius - 1, 0, ...)`, one bin, only on this path), adds `count`, and defers the decrease, reason `lower_radius_full`, when

```
held at r - 1 >= capacity - capacity/16
```

So a decrease goes ahead only when at least one sixteenth of the capacity is still free at the lower radius before the puller adds anything. For the node in the Problem, held at radius 8 is about the capacity (point 2 above), so it is deferred. After a large batch expiry the held count falls in every bin, and the decrease goes ahead.

This applies to d = 0 nodes as well, because the flap does not depend on doubling: a stock-configured node just under half capacity whose lower radius is already full flaps the same way. For such a node the check only stops decreases that could not have given it room.

The margin of 1/16 is a compiled constant. By rule 8 it becomes a setting only if the measurement shows that its value matters.

### 4. Flap detection (all nodes)

The unreserve loop's increase records whether the radius it raises to equals a radius the reserve worker decreased from within the last hour. If so: counter `bee_localstore_radius_flap_total` and a warning, `reserve radius increased again within an hour of a decrease from it` with `radius` and `since_decrease`. It changes no decision. It is the measurement's main signal, and it shows any flap the floor and the fit check miss.

### Metrics

| Metric | Type | Meaning |
|---|---|---|
| `bee_localstore_radius_decrease_deferred_total{reason}` | counter, existing (#696) | two new labels, `below_neighbourhood_depth` and `lower_radius_full`, created at start; the help text is widened from "because the puller had not yet synced" to "and why" |
| `bee_localstore_radius_floor` | gauge | the settled floor; 0 when there is none (radius 0 is never a meaningful floor on mainnet) |
| `bee_localstore_radius_lift_total` | counter | lifts to the floor (part 2) |
| `bee_localstore_reserve_size_at_lower_radius` | gauge | held at r - 1, set whenever the fit check computes it |
| `bee_localstore_radius_flap_total` | counter | part 4 |
| `bee_salud_neighborhood_depth_peers` and `bee_salud_neighborhood_depth_agreeing` | gauges | the two neighbour counts behind N |

### Log lines

- Debug, on a deferral, extending the existing #696 line: `reserve radius decrease deferred` with `radius`, `reason`, and per reason `floor`, `neighbourhood_depth`, `network_depth` or `held_lower`, `capacity`.
- Info, on a lift: `reserve radius raised to the neighbourhood depth floor` with `radius`, `floor`, `neighbourhood_depth`, `network_depth`.
- Info, when the settled floor changes: `reserve radius floor changed` with `old`, `new`, `neighbourhood_depth`, `network_depth`, `neighbours`.
- Warning, part 4.

The existing `reserve radius decrease` and `reserve radius increase` lines stay as they are, so the operator's current log searches keep working.

### Not changed

The threshold, the increase path, the unreserve loop's eviction order, the #696 settle window, the #583 stall gate, salud's own health check, and how the start radius is chosen (`waitNetworkRFunc`, `pkg/node/node.go:1540-1555`). A fresh doubled node still starts at the network's committed depth and walks down; with the floor it stops at N - d, which is where the #696 spec already says it should end.

## Trade-offs

- **Genuine decreases when the network shrinks** still happen: through M (part 1) and because a large expiry lowers the held count (part 3). The cost is up to two extra checks (30 minutes) for the floor to settle, on top of the #696 cost.
- **A node kept too high.** The floor holds a doubled node at `min(N, M) - d`, which is where it holds the same area as its neighbours, scaled by 2^d; that is the point of doubling. The fit check holds a node at r only when r - 1 is already within 1/16 of capacity, so the content it gives up is at most that sixteenth.
- **Trust in peers' reports.** Committed depths come from peers' own status answers. A majority of a node's neighbours (at least 3) reporting a false depth could raise its floor. salud's self-health check already trusts the same reports, and N is bounded by M through `min`, so peers that misreport can hold the node no higher than the network depth minus d. An on-chain cross-check is an open question.
- **Wrong-depth windows that remain:** a doubled node over capacity at its floor still raises its radius above it (it cannot store more), and a node with no robust reading behaves as today apart from the fit check.

## Protocol impact

None. No message, field or encoding changes; the status protocol already carries each peer's committed depth, and salud already reads it. `.github/protocol-freeze.lock` is untouched. On chain, the node reveals at a depth chosen by a different local rule, through the same call with the same encoding; the intended effect is that the depth matches its neighbours' more often.

## Tests

1. **Reproduction of the flap, `TestRadiusDoesNotFlapIntoAFullLowerRadius`, portable to upstream.** A storer with a small capacity C (for example 1,024 chunks), a short `ReserveWakeUpDuration`, started with `StartReserveWorker` and a test syncer whose `SyncRate` is 0 (and, for wasp, whose `ActedOnRadius` reports the current radius with an old `since`, so #696 does not defer). The reserve holds chunks at radius r = 3: fewer than C/2 in bins at or above 3, and enough in bin 2 that the reserve is at capacity. A test goroutine plays the puller: when the radius drops to 2 it puts a few more bin 2 chunks, which takes the reserve over capacity. Over at least six checks the test records the radius timeline. **On today's code, and on unmodified upstream v2.8.3, the radius goes 3, 2, 3, 2, ... and the test fails** (it requires no decrease); with the change it stays at 3, and `lower_radius_full` is counted.
2. **Reproduction at the floor, `TestDoubledNodeDoesNotDecreaseBelowNeighbourhoodDepth`.** As test 1 with d = 1 and only a few chunks in bin r - 1 (so the fit check passes), and a stub `DepthReporter` with a robust reading N = M = r + 1. No decrease over six checks; `below_neighbourhood_depth` counted. Without the reporter the radius drops.
3. **Genuine decrease:** as test 2, then the stub lowers N and M by one; after two checks the radius drops one step, and not two.
4. **M leads:** N stays, M drops by one: the floor follows `min(N, M)` and the decrease goes ahead.
5. **Robustness:** 2 neighbours answering: no floor. 3 at r + 1 and 3 at r: no floor (no majority). A reading 16 minutes old: no floor. In each case the decrease goes ahead when the fit check allows.
6. **Settling the floor:** a reading that alternates between two depths at each check never moves the settled floor; a value seen at two consecutive checks does.
7. **Lift:** d = 1, radius r, settled floor r + 1, no sample running: at the next check the radius is r + 1, the reserve size is unchanged (nothing evicted), `radius_lift_total` is 1. With `IsSampling` true the lift waits for a later check. With floor r + 2 it takes two checks.
8. **d = 0:** with a reporter and a high floor, no lift and no floor deferral; the fit check still applies.
9. **Fit check boundary:** held at r - 1 equal to `C - C/16`: deferred; one less: decrease. The bin count comes from bin r - 1 only, and only on the decrease path (a counting stub shows no bin scan on a check where the decrease condition does not hold).
10. **Order:** the stall gate still blocks decreases first and counts no deferral; the lift happens while the stall gate is closed; a check where both the floor and the fit check apply counts only `below_neighbourhood_depth`; the #696 deferral still applies after both pass.
11. **Flap metric:** a decrease from r, then an unreserve increase to r 10 minutes later: counter 1 and the warning. The same 61 minutes later: counter 0.
12. **salud:** with a mock status service, `CommittedDepths` reports the neighbour and peer counts and the agreeing counts, the `At` time of the pass, and false before the first pass.

**Mutation checks** (each must make a named test fail, and each is checked as applied before it is run, per the playbook):
floor check removed (2); `<=` instead of `<` in `radius - 1 < F` (2, 3); `min(N, M)` replaced by N (4); peer minimum ignored (5); majority ignored (5); age ignored (5); floor applied without settling (6); lift removed (7); lift evicts (7, reserve size); lift ignores `IsSampling` (7); floor or lift applied when d = 0 (8); fit check removed (1, 9); margin `>` instead of `>=` (9); held count starting at r instead of r - 1 (9, 1); bin scan on every check (9); lift after the stall gate (10); deferral counted twice (10); flap window ignored (11).

Race detector on `pkg/storer` and `pkg/salud`; check the exit status directly.

**Upstream check, part of the implementation:** run test 1 on an unmodified `upstream/v2.8.3` worktree (without the `ActedOnRadius` method, which upstream ignores anyway). If the radius alternates, the `affects-upstream` label on #822 stands, with what was run; if not, the label is removed and the reason recorded.

## Measurement

On **bench-1 only**, never on production fleet hosts. bench-1 is at present the resource-harness host and not on mainnet; the operator decides when it runs mainnet nodes for this.

**Set-up.** Six doubling-1 full nodes on bench-1, connected to mainnet, unstaked (the radius logic does not depend on the lottery). Each overlay is mined with `target-neighborhood` into a different depth-10 neighbourhood whose radius-9 content is known to be under half of the doubled capacity, chosen from the operator's private notes and recorded in `measurement.md` only by its fraction of the threshold, never by prefix. No two of the six share their first 8 bits, so none is within another's radius and none feeds another's neighbourhood depth. Three run the current `main` (control) and three the change, paired by that fraction. Resources are checked before starting (six reserves of 8.4 M chunks each).

**Duration.** 48 hours after the initial fill (the first time each node's within-radius count stops rising for an hour).

**Recorded per node:** the radius and committed depth timeline from `/status`; `radius_flap_total`, `radius_lift_total`, `radius_decrease_deferred_total` by reason, `radius_floor`, `reserve_size_at_lower_radius`; salud's neighbourhood depth, its peer counts and the self-health warning count; reserve size and within-radius count.

**Pass, all three treatment nodes:**

- `radius_flap_total` stays 0;
- committed depth equals the neighbourhood depth in at least 99 % of salud passes after the initial fill;
- no `storage radius discrepancy` warning after the initial fill;
- the radius is never above the floor while the reserve is under capacity (not held too high);
- a robust floor exists in at least 90 % of checks (the signal is usable on mainnet).

**The control must show the problem:** at least two flaps per control node in 48 hours. If it does not, the run is inconclusive and the neighbourhoods are chosen again; it is not reported as a pass.

**Genuine decreases** are covered by tests 3 and 4. If the network depth falls during a run, the results report whether each treatment node followed within two checks; if it does not fall, the results say this was not observed on the bench.

**Negative result:** a treatment node flaps (a path the floor and the fit check miss); a treatment node stays at a higher radius than its neighbours' depth minus d after they have decreased; or the floor is missing in more than 10 % of checks, which would mean the status-protocol signal is not robust enough and the on-chain reveal depth should be considered instead.

## Rollout and rollback

A code change with no setting. It ships in the next build. Order: bench-1 (the measurement above), then a single production node on the operator's decision, then the rest. Rollback is the previous build; the persisted radius is valid for it, and it resumes today's behaviour (including the flap).

## Upstream portability

The defect is present, unmodified, in upstream v2.8.3 (paths and lines under Problem). Upstream would need:

- the fit check (part 3): self-contained in `pkg/storer/reserve.go`, about twenty lines and one bin scan; test 1 runs on upstream as written;
- the floor and the lift (parts 1 and 2): salud already computes N and M, so it needs only to export the counts behind them, the `DepthReporter` interface in `pkg/storer`, and one call in `node.go`;
- nothing from #696 or #583: the parts are independent of the settle window and the stall gate, though without the settle window upstream would flap every 15 minutes rather than 30.

A row for #822 is added to `docs/UPSTREAM.md` with this spec.

## Configuration

None. The constants (fit margin 1/16, at least 3 peers and a strict majority for a robust reading, at most 15 minutes old, two checks to settle a floor, one hour for the flap window) are compiled in. Each becomes a setting only if the measurement shows its value matters (rule 8).

## Open questions for the operator

1. Whether the floor should also apply to d = 0 nodes. A stock-configured wasp node whose neighbourhood is under half its capacity also commits below its neighbours after a decrease that fits. This spec leaves d = 0 to the fit check only, because for a d = 0 node the decrease is how a neighbourhood's depth falls.
2. Whether to add the on-chain reveal depth (`Revealed` events or `winner().depth`) as a cross-check against peers that misreport, at the cost of a chain call per round.
3. When bench-1 can run mainnet nodes for the measurement.

Generated with help of AI.
