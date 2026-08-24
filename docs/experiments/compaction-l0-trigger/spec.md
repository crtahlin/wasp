# Restore headroom between L0 compaction and write pause

Issue: [#24](https://github.com/crtahlin/wasp/issues/24)

## Problem

`pkg/storer/storer.go:271` sets `CompactionL0Trigger: 8`. The original fork
identified this as causing a write stall on nodes with large reserves and
reverted it. It is still 8 at `v2.8.1`.

goleveldb has three L0 thresholds. Only the first is set here; the other two keep
their defaults:

| Threshold | goleveldb default | wasp | Effect |
|---|---|---|---|
| `CompactionL0Trigger` | 4 | **8** | L0 file count at which compaction starts |
| `WriteL0SlowdownTrigger` | 8 | 8 | writers begin sleeping 1 ms each |
| `WriteL0PauseTrigger` | 12 | 12 | writers **block** until compaction progresses |

Stock goleveldb gives four files of headroom between compaction starting and
writes slowing, and eight before writes stop. At 8, compaction does not begin
until the exact point writes start being throttled, leaving **four files instead
of eight** before writes stop entirely. The half removed is the early half — the
part that lets compaction get ahead before back-pressure begins.

A second default compounds it: `db-disable-seeks-compaction` defaults to `true`
(`cmd/bee/cmd/cmd.go:277`), so seek-triggered compaction is off and the L0 file
count is essentially the only thing that starts a table compaction.

At the limit, `leveldb/db_write.go:88`:

```go
case tLen >= pauseTrigger:
    atomic.StoreInt32(&db.inWritePaused, 1)
    err = db.compTriggerWait(db.tcompCmdC)
```

`compTriggerWait` blocks on a channel whose only other exits are a compaction
error or the database closing. **There is no timeout.**

### It is not a deadlock, and that changes the goal

There is no lock cycle. It is an unbounded wait on compaction. The symptom is
indistinguishable — writes stop, the node stops working, nothing recovers — so
the original operational report described what was seen accurately. But the
distinction determines what any fix can claim, and the honest claim is *raises
the margin*, not *removes the failure mode*. The pause path at L0 ≥ 12 exists at
any trigger value.

### There is no documented rationale to overturn

`CompactionL0Trigger: 8` arrived in `3c5a8186`, "perf: levelDB options tuning
(#4218)", which in one commit also enabled compression (by deleting
`Compression: opt.NoCompression`) and added a bloom filter. The commit has **no
message body and no recorded measurement**. So this is not a carefully reasoned
decision being second-guessed; it is an undocumented tuning constant with a known
hazard, bundled with two other changes.

## Hypothesis

The stall is compaction falling behind sustained write load, and the raised
trigger is what makes falling behind easy: compaction starts four files later
than goleveldb expects, into a window that ends four files later at a hard block.

If that is right, returning the trigger to 4 should keep L0 depth well below the
pause threshold under the same load, and the node should not stall.

If it is wrong — if L0 depth still reaches 12 with the trigger at 4 — then the
binding constraint is compaction throughput itself, not when it starts, and the
answer lies with the load rather than the threshold: [#23](https://github.com/crtahlin/wasp/issues/23)
(pause pullsync during sampling) and [#29](https://github.com/crtahlin/wasp/issues/29)
(Reserve Put lock contention). That is a real possibility and a useful result.

## Design

Change one character: `CompactionL0Trigger: 8` becomes `4`, goleveldb's default,
restoring a consistent 4/8/12 triple.

**Deliberately not** raising `WriteL0SlowdownTrigger` and `WriteL0PauseTrigger`
to, say, 16/24 while keeping the trigger at 8. That would preserve the
(undocumented) intent of fewer, larger compactions while restoring headroom, and
it is the obvious alternative — but it invents a threshold triple nobody has
tested, and more L0 files means more read amplification on a node whose sampling
path is read-heavy. Returning to the configuration goleveldb is tuned for is the
conservative move. If measurement shows the reverted trigger costs too much write
amplification, that alternative is the fallback, and the spec should be revised
rather than the constant quietly re-tuned.

**Nothing else in `3c5a8186` is touched.** Compression stays enabled and the
bloom filter stays. Reverting the trigger alone keeps this to one variable, which
is what makes the measurement mean anything.

## Protocol impact

**None.** Local storage engine tuning. No value in
`.github/protocol-freeze.lock` is affected, no wire surface, no on-disk format
change that peers or other clients observe. LevelDB rewrites its own files
during normal compaction; nothing here changes what a chunk is or how it is
addressed.

## Measurement

The failure is a stall, so the measurement must observe L0 depth rather than
infer it from symptoms.

1. Instrument L0 file count. goleveldb exposes it through
   `db.GetProperty("leveldb.num-files-at-level0")`; expose it as a metric from
   `pkg/storage/leveldbstore` so both arms are directly comparable.
2. Sustained write load against a large reserve on bench-1 — pullsync ingest with
   sampling running concurrently, which is the combination the original report
   came from.
3. Record, for each arm: peak L0 depth, time spent at or above the slowdown
   trigger, any time at or above the pause trigger, and total compaction bytes
   written.

**Success**: with the trigger at 4, peak L0 depth stays below 12 under load that
drives it to 12 with the trigger at 8.

**A negative result** looks like L0 depth reaching 12 in both arms. That means
compaction throughput is the constraint, and the answer is #23 and #29, not this
constant. It should be reported that way rather than as a failed experiment.

**The control is the current build**, and it matters more than the treatment: if
the current setting cannot be driven to a stall on the bench, then this
experiment has no signal and the whole issue rests on an unreproduced field
report — which is worth knowing before writing code.

**Compaction cost is not a side note.** Reverting is not free: more frequent
compaction means more write amplification. Total compaction bytes is a required
output, not an optional one. A fix that removes a stall by tripling disk writes
on a node that is already disk-constrained is not obviously a win.

## Rollout and rollback

A single constant in `pkg/storer/storer.go`, applied at store open. No migration,
no persisted state, no format change — an existing database opens identically
either way. Rollback is reverting the commit and restarting.

It is deliberately not exposed as a flag. Per rule 8, expose what measurably
matters: this is a value that should be correct, not tuned per operator, and
adding a knob would invite exactly the undocumented divergence that produced the
problem. If measurement shows operators genuinely need different values on
different hardware, that is a follow-up with its own evidence.

## Upstream portability

High, if the measurement holds. It is a one-character change to a constant that
upstream set without recorded rationale, and the mechanism is in goleveldb's
documented behaviour rather than anything wasp-specific. What Ethersphere would
need is exactly what the measurement section produces: L0 depth traces for both
arms, and the compaction-cost figure showing what the revert costs.

The instrumentation is separable and worth offering regardless of the outcome —
L0 depth is not currently visible from bee at all, which is part of why this was
diagnosed operationally rather than measured.

## Configuration

No new configuration. The change is to a compiled-in constant.

For completeness, the related existing option:

| | |
|---|---|
| flag | `db-disable-seeks-compaction` |
| default | `true` (`cmd/bee/cmd/cmd.go:277`) |
| leaving it true costs | one fewer trigger for table compaction, so L0 depth depends almost entirely on the file-count trigger |
| setting it false costs | compactions triggered by read patterns, which adds write amplification on a read-heavy sampling node |
| costs other nodes | nothing; local storage behaviour only |

Whether this default is right for a large reserve is a separate question and
should not be changed in the same experiment.
