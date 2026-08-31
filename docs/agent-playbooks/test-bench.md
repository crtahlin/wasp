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

## Go microbenchmarks drift with position in the process

The three-runs rule above is about live-node noise. Go microbenchmarks — the
`storagetest` suite, `beebench` — have a second, quieter trap that the spread
alone does not catch: **the same benchmark reports a different number depending
on how much ran in the process before it.** It is directional, always making
later runs look faster, so a before-and-after comparison that puts the "after"
arm second in the process shows an improvement that is not there.

The cause is garbage-collector pacing, established by measurement, not guessed.
Running one allocation-free read benchmark six times in a single process, each
with its own fresh store:

**Table — `inmemchunkstore` ReadRandom, Apple M5 Pro, `-benchtime 3000x -count=6`, one process, ns/op**

| GC setting | run 1 | 2 | 3 | 4 | 5 | 6 | spread |
|---|---|---|---|---|---|---|---|
| default | 57.8 | 43.0 | 27.0 | 34.0 | 21.5 | 40.7 | 2.7x |
| `GOGC=off` | 27.7 | 23.2 | 22.9 | 24.7 | 25.1 | 21.4 | 1.3x |

The loop allocates nothing (`0 allocs/op`), so this is not the benchmark's own
garbage — it is background GC driven by the heap the setup phase left live,
stealing cycles from early measured runs and settling as the process continues.
`GOGC=off` removes it almost entirely. Longer `-benchtime` dampens it (the same
benchmark at `40000x` spread 1.4x rather than 2.7x) but does not remove it. And
`runtime.GC()` in `resetBenchmark` does **not** fix it: it completes one cycle,
it does not change pacing during the timed region.

Rules for microbenchmarks:

- **Each condition gets its own process.** Compare `A` and `B` by running each in
  a separate `go test -bench` invocation, not as two sub-benchmarks of one run.
  This is the robust answer and the one to reach for by default.
- If A and B must share a process, **alternate and interleave** several
  repetitions of each rather than all of A then all of B, so the drift falls on
  both equally, and report the spread.
- For an **allocation-free** microbenchmark, `GOGC=off` flattens the drift and is
  safe. Do **not** use it for a benchmark that allocates in its loop: there it
  would hide a real, GC-driven cost that a production node actually pays.
- A ratio quoted from two sub-benchmarks of a single process is not trustworthy.
  This is how issue #172 was found, while correcting the numbers in #173.

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

## When the index store stops accepting writes

A node can enter a state where the LevelDB index store stops accepting writes and
does not come back. It is not a crash: the process stays up, `/health` can still
answer, but sync makes no progress. Issue #176.

**How to recognise it.** The node logs, at warning level:

```
index store has stopped accepting writes: level 0 has too many files and
compaction is behind; the node will not make progress until compaction catches up
```

with a `level_0_files` field. The Prometheus gauge `bee_leveldb_write_paused`
reads 1, and `bee_leveldb_level_tables{level="0"}` sits at or above the pause
trigger (12 by default).

**Why it does not recover on its own.** goleveldb blocks the writer when level 0
reaches `WriteL0PauseTrigger` and waits for compaction, with no timeout. The
blocked writer holds goleveldb's write lock, and the only call that could force
compaction to drain level 0 needs that same lock, so it queues behind the stuck
writer. Nothing in the running process can break the cycle.

**The fix is a restart.** There is no in-process recovery. Restarting reopens the
store and lets compaction run from a clean state. Be aware that `Store.Close`
itself writes (#115), so a store already at the pause trigger can make even a
clean shutdown slow; give it time before killing harder.

**Reaching it is a slow-disk-under-contention concern, not a fast-disk one.** On
fast NVMe with the machine otherwise idle, the `compaction-l0-trigger` experiment
needed roughly 2 GB/s of sustained ingest to get here, about 16x a saturated
1 Gbit/s link — effectively unreachable, and well above the puller's default
`puller-max-chunks-per-second` (1000/s, roughly 4 MB/s). It becomes reachable when
compaction throughput collapses: a slow disk, sharky chunk writes competing for
the same device, or reserve sampling reads stealing seeks (that last is #23). On
storage roughly 20x slower it can arrive around 24,000 writes/sec, inside line
rate.

**If a node on slow storage keeps hitting it**, two levers, both cost-both-ways:

- Keep `puller-max-chunks-per-second` conservative, so ingest cannot outrun
  compaction. The cost is slower reserve filling.
- Widen the gap between the slowdown and pause triggers with
  `--db-write-pause-trigger` (and `--db-write-slowdown-trigger`), so goleveldb's
  own per-write slowdown has more room to work before the hard stop. The default
  is `8/8/12` (compaction/slowdown/pause), which starts compaction exactly when
  writes begin slowing and leaves only four files of headroom. Raising the pause
  trigger buys burst headroom, but more level-0 files means more read amplification
  on the sampling path. Any non-default value should be measured on the target
  hardware; on fast disk it changes nothing, so do not set it there.

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

## A timeout panic names its culprit in the header, not in the stacks

The same trap as above, in a different costume. `panic: test timed out` dumps **every**
goroutine in the process. Reading the stacks and picking a plausible-looking one gives
the wrong answer, because the goroutines that look interesting are usually just present.

Only the header says which test actually overran:

```
panic: test timed out after 10m0s
	running tests:
		TestBzzUploadDownloadWithRedundancy (9m42s)
		TestBzzUploadDownloadWithRedundancy/level=4/encrypt=true_levels=3_chunks=401 (31s)
```

That is the culprit. Everything below it is context.

This cost real time. A `pkg/api` timeout was diagnosed from its stacks as a websocket
deadlock in `gsocListeningWs`, filed as a hang, and hunted with 225 reproduction runs of
`-run 'TestGsocWebsocket|TestPss'` — a set that does not contain the test that was
actually slow. The conclusion was "cannot reproduce". It then reproduced on the next
unrelated pull request, one that changed only `.gitignore`.

Also worth knowing: a slow test and a hung test look identical from the outside. Check
before assuming. `TestBzzUploadDownloadWithRedundancy` takes 6 s normally and 123 s under
`-race` — a 20x multiplier that turns a comfortable test into most of a package's budget.
Time it both ways before calling anything deadlocked.

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

## A soak must assert the node is under load

A crash-free soak proves nothing unless the node was doing work. This is not a
theoretical caution: a 65-minute clean run was recorded here on a node that had
**zero peers** and a load average of 0.04. It was not stable, it was asleep. The
faults under investigation only appear under read load, so an idle node cannot
produce them and its silence means nothing.

Worse, the node reported `{"status":"ready"}` from `/readiness` throughout, so every
health signal an operator would normally trust said the run was valid (#74).

Every soak and benchmark must therefore treat "is the node working" as a measured
precondition, not an assumption:

- **Peer count above zero**, read from `/peers` or `/topology`'s `connected`. Treat
  zero as a distinct *invalid run* outcome, not as a pass and not as a failure.
- **Load average above idle**, from `/proc/loadavg`.
- For read-path work, **drive load explicitly**. A node whose reserve is already full
  does very little on its own. `/rchash/{depth}/{anchor1}/{anchor2}` in a loop
  produces sustained heavy reads — roughly 137 s per run at depth 9 on a 4M-chunk
  reserve, taking load average to about 6. `anchor1` must be the node's own overlay
  from `/addresses`.

Report the load evidence alongside the result. "Ran 90 minutes without crashing" is
not a result; "ran 90 minutes at 87 peers and load 5.7, driven by continuous rchash
sampling, without crashing" is.

## Write soak harnesses so they can actually fail

Two harness bugs in one session produced confident, false "SOAK PASSED" reports. Both
are worth guarding against by construction.

**Shell word-splitting.** A harness written for `bash` and run under `zsh` silently
changes meaning: zsh does not word-split unquoted parameter expansions, so
`set -- $OUT` yields one argument instead of several. Every field after the first was
empty, `${3:-0}` defaulted to `0`, and the failure check could never fire. It printed
`soak ok: 75m clean (state= restarts= crashes=)` and then `SOAK PASSED: 0 crashes` for
a window containing two crashes and two restarts.

Do the parsing on the remote host inside an explicit `bash -s`, emit one
`key=value` line, and parse that. Then **validate the harness against a live tick
before trusting it** — print the raw line and the parsed fields, and confirm they
match.

**Silent skips.** An SSH failure that is logged and skipped will hide the very outcome
you are watching for, because a node that takes the whole machine down also stops
answering SSH. Retry, then escalate: after a small number of consecutive unreachable
ticks, abort the run with a distinct status rather than continuing to report ticks.

A harness that cannot produce a failure is not measuring anything. Before trusting
one, ask what it would print if the node died right now — and if the honest answer is
"the same thing it prints now", fix it before starting the run.

## SIMD hashing: fixed, and what the fix was

`use-simd-hashing` corrupted memory and killed nodes within ~12 minutes under load. Issue
#92 is the report; the fix is #94: the assembly stub now runs the XKCP blob on a **scratch
stack** rather than the goroutine stack.

Two panics exist to make a future regression here loud instead of silent, because no test
of hash output can catch one — the digests are correct either way:

```
panic: keccak: SIMD blob overflowed its 65536-byte scratch stack ...
panic: keccak: SIMD blob moved the stack pointer by N bytes and did not put it back ...
```

The first means a blob outgrew its 64 KiB buffer — expected only after regenerating the
`.syso` files from a newer XKCP. The second means the blob violated the SysV ABI. Either
way the node is telling you the truth immediately rather than dying in an unrelated
subsystem twelve minutes later, which is what the original bug did. Do not work around
either by enlarging the buffer or removing the check until the cause is understood.

Go's goroutine stacks are small, growable and movable — the runtime relocates them and
the collector scans them. Foreign machine code executing on one is unsafe, which is why
cgo switches to the system stack before entering C. The stub now does the same, using a
pooled 64 KiB byte slice: no pointers, never scanned, never moved.

Measured, same node and load, SIMD on throughout:

| stub | result |
|---|---|
| goroutine stack (original) | crash ~12 min |
| scratch stack | **7.6 h clean, 113 peers, 305 rchash runs** |

Two cheap corrections were tried first and both failed — `runtime.Pinner` (~4.5 min) and
`NO_LOCAL_POINTERS` (~3 min). Record them before trying either again: the pointer map and
pointer pinning were not the problem, the execution context was.
