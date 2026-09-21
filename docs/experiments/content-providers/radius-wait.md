# Retrieval must not wait for the network storage radius

Issue: [#398](https://github.com/crtahlin/wasp/issues/398). Label:
`affects-upstream`.

## The defect

`pkg/node/node.go` builds one radius lookup and hands it to three consumers:

```go
waitNetworkRFunc := func() (uint8, error) {
    if networkR.Load() == uint32(swarm.MaxBins) {   // sentinel: not learned yet
        select {
        case <-initialRadiusC:                      // blocks
        case <-ctx.Done():
            return 0, ctx.Err()
        }
    }
    ...
}
```

`networkR` starts at the sentinel and is written only when
`SubscribeNetworkStorageRadius` first delivers a value, so until that happens
every call blocks. One of the three consumers is retrieval, and it calls this
**inside the per-chunk retrieval loop**:

```go
if radius, err := s.radiusFunc(); err == nil && swarm.Proximity(...) >= radius {
    for ; forwards > 0; forwards-- {
        retry()
        errorsLeft++
    }
}
```

That block is the multiplexer: an optimization that fans a request across the
neighbourhood when this node is close to the chunk. **Its guard already skips
it when the lookup returns an error**, so retrieval has nothing to wait for.
Blocking there stops the flight before peer selection and before accounting.

So for the first seconds to minutes after a restart, every chunk of every
download waits. A download long enough to still be running does not merely go
slowly: `joiner.ReadAt` reads a whole unit or none of it, so the caller gets
**HTTP 200 with a truncated body** and no error anywhere.

## Evidence it is this and not something else

Measured on the bench, requester `bench-2`, provider `bench-1`:

- A goroutine dump taken twelve seconds into a stalled 50 MB download:
  **324 goroutines in `node.NewBee.func14`**, which is this function, with 449
  singleflight waiters behind them. A representative stack runs
  `RetrieveChunk.func2` at `retrieval.go:402` into `node.go:1355`.
- The same download, same pair, with the peer relationship reset but **no
  restart**: **six of six complete** at 2.7 to 4.1 MB/s.
- Immediately after a restart: **four of four truncated**, at exactly 524,288
  bytes in five of the seven failures recorded across two sessions.
- `/readiness` is not the discriminator. An arm that waited for it reached 200
  in 31 seconds and truncated anyway, because readiness does not wait for the
  radius.
- During the stall the balance is frozen, the reserved balance is **zero**,
  and `settle` is not called for the provider. That is a loop that never asks
  for credit, not one being refused it. This is why
  [#396](https://github.com/crtahlin/wasp/issues/396), which read the same
  stalls as a settlement defect, is superseded.

## The change

Give retrieval a lookup that reports the radius as unknown rather than waiting
for it, and leave the other two consumers alone:

```go
networkRNoWaitFunc := func() (uint8, error) {
    if networkR.Load() == uint32(swarm.MaxBins) {
        return 0, errNetworkRadiusUnknown
    }
    return waitNetworkRFunc()
}
```

`retrieval.New` takes `networkRNoWaitFunc`. `pushsync.New` and
`StartReserveWorker` keep `waitNetworkRFunc`, because neither should act on a
radius it does not have: pushsync decides whether a chunk has reached its
neighbourhood, and the reserve worker decides what this node is responsible
for storing. Retrieval decides only whether to send one extra copy of a
request it is sending anyway.

The sentinel error is a package-level `var`, not built per call, because the
lookup runs once per peer selection per chunk.

**This is one line of wiring plus a wrapper.** It changes no protocol message,
no constant, and no behaviour once the radius is known.

## What it costs

State both directions, per rule 8.

**While the radius is unknown**, which is the seconds after a restart, a
request that would have been multiplexed across the neighbourhood is sent to
one peer at a time. For a chunk this node is close to, that means more rounds
to find it, and a download started immediately after a restart may be slower
than the same download started later. It will not be truncated.

**This costs other operators nothing**, and in the window concerned it costs
them slightly less: the multiplexer is what sends the same request to several
neighbours at once, and it is the part being skipped.

**Once the radius is known** the behaviour is identical, because the wrapper
then calls the same function and returns the same value.

There is no configuration. The window is however long the node takes to hear a
radius from its peers, which is not something an operator should have to tune,
and a dial here would only offer a way to reintroduce the defect.

## Protocol impact

None. No wire message, constant or version changes, and `make protocol-freeze`
must pass unchanged. The `protocol-change` label does not apply.

## Measurement

Rule 7: three runs per condition, spread reported, node state matched, arms
interleaved, every run gated on the installed build read back from the version
string and on the provider being in `/peers`.

**Arm 1, the defect.** Restart the requester, connect, download 50 MB of
content only the provider holds, with a hint. Three runs on the change and
three on the control, alternating. Passes when all three runs on the change
deliver 52,428,800 bytes with a matching checksum. The control is expected to
truncate; if it does not, the bench is no longer reproducing the defect and
nothing here can be concluded.

**The mechanism check.** During each run, count goroutines whose stack
contains the radius function, sampled once eight seconds in. Expected to be
**zero** on the change and non-zero on the control. This is recorded because
delivered bytes alone cannot tell a fixed cause from a lucky run, and the
earlier arm 1 of [#392](https://github.com/crtahlin/wasp/issues/392) is what
that mistake looks like.

**Arm 2, no regression once the radius is known.** The same download on a node
that has been up long enough to know the radius, three runs per build. Passes
when the wall-clock rate on the change is within the spread of the control.
This is the arm that rejects the change: the wrapper must be inert once the
radius is known, and this is what proves it rather than asserting it.

**Arm 3, sizes.** With the change only, 4 MiB, 16 MiB and 50 MB, five runs
each, each from a reset peer relationship, recording delivered bytes and
checksum. This is the question the content-providers feature actually rests
on and it has never been answered cleanly, because every previous attempt
restarted the node inside the run.

**What a negative looks like.** If arm 1 still truncates on the change with
the waiter count at zero, then the radius wait was one cause and not the only
one, and the next thing to read is the goroutine dump again rather than the
accounting counters.

## Rollout and rollback

No configuration and no migration. Rollback is `git revert` of the merge
commit, which restores the wait.

## Upstream portability

The defect is upstream's, verified against `upstream/v2.8.2`, this fork's
recorded base:

- `pkg/node/node.go:1150-1156`, the same blocking `select` on
  `initialRadiusC`;
- `pkg/node/node.go:1172`, `waitNetworkRFunc` passed to `retrieval.New`;
- `pkg/retrieval/retrieval.go:244`, the same call site with the same
  `err == nil &&` guard.

So an unmodified Bee node truncates downloads silently, with a 200, for the
first seconds to minutes after every restart. Nothing in this fork introduced
it and nothing in this fork is required to reproduce it.

Per rule 11 the `affects-upstream` label is a marker for a later human
decision and authorises nothing further. The change proposed here is confined
to `pkg/node/node.go`, so a patch against upstream would be the same two
edits.
