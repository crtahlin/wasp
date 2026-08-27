# A stalled log sink must not deadlock the node

Issue: [#156](https://github.com/crtahlin/wasp/issues/156) ·
Upstream: [ethersphere/bee#5581](https://github.com/ethersphere/bee/issues/5581)

## Problem

`pkg/log/logger.go:245` is the only write to the log sink in the whole package:

```go
if _, err = l.sink.Write(buf); err != nil {
```

It is a bare, unbounded, synchronous write. The production sink is
`cmd.OutOrStdout()` — `os.Stdout` — set once at `cmd/bee/cmd/cmd.go:540`, which
under systemd, Docker, or a desktop launcher is a pipe or a socket. When the
consumer at the far end stops reading, the buffer fills and `write(2)` never
returns.

Because the peer-connection path logs synchronously, that block propagates into
networking. Observed on a stock `bee 2.8.1` light node on 2026-08-27, 105
minutes after the fault:

```
internal/poll.(*FD).Write             blocked 105 minutes
os.(*File).Write
pkg/log.(*logger).log                 pkg/log/logger.go:245
pkg/log.(*logger).Warning
pkg/pricing.(*Service).init           pkg/pricing/pricing.go:119
pkg/p2p/libp2p.(*Service).Connect     pkg/p2p/libp2p/libp2p.go:1201
pkg/topology/kademlia.(*Kad).connect  pkg/topology/kademlia/kademlia.go:975
```

Seven dial goroutines were stuck there, and kademlia's manage loop was parked in
`sync.WaitGroup.Wait` at `kademlia.go:629` waiting for them. The node held zero
peers, had made zero dial attempts, and reported `/health` `ok` throughout. Only
a restart recovered it.

**Table 1 — bench readings from the affected node, three samples 20s apart**

| reading | value |
|---|---|
| `/peers` | 0 |
| `bee_kademlia_currently_connected_peers` | 88, stale |
| `total_outbound_connection_attempts` | 699, frozen |
| `/health` | `{"status":"ok"}` |

The gauge disagreeing with reality is itself diagnostic: `CurrentlyConnectedPeers`
is set inside the manage loop, so 88 is the value from the last iteration that
completed.

Demonstrated without a node: a logger whose sink was an unread pipe completed
368 calls in two seconds and exactly 368 five seconds later.

This is not specific to any one deployment. A full disk, a paused container, a
stalled journal or an unread pipe from a shell all produce it. `pkg/log` is
unmodified from upstream, so **wasp `v0.1.0` ships this defect**.

## Hypothesis

Log delivery and node liveness are wrongly coupled. If the sink is made bounded
and non-blocking, a stalled consumer costs log lines and nothing else: dialling
continues, the manage loop keeps cycling, and the node stays reachable.

If that is wrong, the block is not only in the sink — some other unbounded wait
on the connect path would keep the loop parked, and the kademlia test below would
still fail after the change. That is a real possibility worth naming rather than
assuming away, and it is what the end-to-end test is for.

## The decision everything else follows from

**`Write` must return `(len(p), nil)` when it drops a line.**

All four level methods do the same thing with an error:

```go
if err := l.log(...); err != nil { fmt.Fprintln(os.Stderr, err) }
```

at `logger.go:182`, `191`, `200` and `209`. Returning an error on drop turns
every dropped line into an unbounded blocking write to `os.Stderr` — and under
`bee ... 2>&1 | consumer` that is the *same stalled pipe*. The bug would be
rebuilt one level up, in the error path, where it is harder to find.

This is the single easiest way to implement the fix and still have the defect.

## Design

### Where the wrapping happens

Inside `log.NewLogger` (`pkg/log/registry.go:56`), wrapping `options.sink`.

Not at the `cmd/bee` call site, and not as an opt-in helper callers may use. The
defect class is "somebody passed a writer that can block", and a wrapper nobody
is obliged to use does not close it. It also could not be regression-tested: a
test for an opt-in wrapper is compile-error-versus-pass, not a test that fails
against today's code.

### The registry hash is the trap

`hash()` at `logger.go:261-269` folds `reflect.ValueOf(sink).Pointer()` into the
key that `loggers` (a global `sync.Map`) deduplicates on.

If `NewLogger` wraps afresh on every call, every call produces a new pointer, so
dedup breaks and each call returns a *different* logger — each with **its own
drain goroutine**. Thirty-six call sites do `logger.WithName(...).Register()`.

So: hash on the **original** sink, and keep a package-level map from underlying
writer to its wrapper, so one writer has exactly one wrapper and one goroutine
for the life of the process.

### Shape

```go
// pkg/log/asyncsink.go
func NewAsyncSink(w io.Writer, capacity int) *AsyncSink
func (s *AsyncSink) Write(p []byte) (int, error) // never blocks; (len(p), nil) even on drop
func (s *AsyncSink) Dropped() uint64
func (s *AsyncSink) Close() error                // drains with a deadline, joins the goroutine
```

A FIFO channel of `capacity` lines and one drain goroutine. Delivered output is
an **order-preserving subsequence** of what was submitted: drops punch holes,
they never reorder.

`Close` must be idempotent, and `Write` after `Close` must return
`(len(p), nil)` and count a drop rather than panic on a closed channel. A node
logs during shutdown.

### Drop accounting

Silent loss would be a milder member of the same family as `/health` reporting
`ok` with zero peers — an instrument present and saying nothing. Three things:

- a monotonic `Dropped()` counter;
- a synthetic `dropped N log lines` line emitted into the stream once per stall
  episode, when the sink recovers, so the gap is visible to whoever reads the
  log and not only to Prometheus;
- a Prometheus counter through the hook mechanism that `pkg/log/metrics.go`
  already implements.

Drop accounting must not depend on the hook firing. Hooks fire *after* the write
at `logger.go:250`, so a stalled sink freezes the log metrics too — which is
why the counter lives in the sink.

### Shutdown

`Close()` drains until the buffer is empty **or** a bounded deadline elapses,
then abandons and reports how many lines remained.

The lines immediately before shutdown are the ones an operator needs, so
draining is right. An unbounded drain would reintroduce this very defect at
shutdown, which is worse than the original: a node that will not exit. The
deadline is what keeps the drain from becoming a hang.

`Logger` is an interface with no `Close` or `Flush`, and adding a method to it
is a breaking change for every implementation, so the closer is returned
alongside the logger from `newLogger` in `cmd/bee/cmd/cmd.go` and called on
shutdown.

### Two existing call sites break, and both fail silently

- `pkg/util/testutil/helpers.go:74-80` — `testutil.NewLogger(t)` sinks to
  `t.Log` and registers **no** `t.Cleanup`. An async drain calling `t.Log` after
  the test returns panics with `Log in goroutine after Test... has completed`,
  and it would do so in random packages across the repository rather than in
  `pkg/log`. It takes the synchronous option; it is a debugging aid and blocking
  is fine there.
- `pkg/log/example_test.go` — `Example()` compares exact stdout via
  `// Output:`. Async delivery makes that nondeterministic. Same treatment.

## Configuration, and why the default changes

`--log-sink-buffer`, in lines. `0` means synchronous, restoring today's
behaviour exactly.

**The default is non-zero.** This deliberately departs from rule 8 in
`AGENTS.md`, which says a tuning constant becomes configuration with its current
value as the default.

Rule 8 exists so that a merge changes no node's behaviour. Here the current
behaviour *is* the defect. Defaulting to `0` would ship a release whose nodes
still go permanently deaf when their log reader stalls, and would make the fix
opt-in for exactly the operators who do not know they need it. The rule is worth
breaking here, and worth saying out loud rather than quietly.

## Measurement

The property is binary rather than numeric, so this is verified by test rather
than by benchmark.

- **`TestLoggerDoesNotBlockOnStalledSink`** — a logger built through the public
  API completes its calls when the writer never returns. Written against
  today's API so it compiles and fails on `main`.
- **`TestKademliaDialsWhileLogSinkIsStalled`** — the property that actually
  matters: dial attempts continue while the log consumer is stalled.
  `p2pMock` in `kademlia_test.go` already counts dials.

Both are watched failing before any implementation begins. If either does not
fail on `main`, it is not exercising the defect and the test is worthless.

`testing/synctest` is the tool for both — already used in eight packages
including `pkg/topology/kademlia`. Inside a bubble, `synctest.Wait()` returns
once every other goroutine is durably blocked, which turns "hangs for ten
minutes and then panics the package" into an instant, deterministic failure.
The stall fixture must block on a **channel**; a mutex is not durably blocking
and would deadlock the bubble instead of failing it.

End to end, on a node: `bee start | cat`, `kill -STOP` the reader, and confirm
`/peers` stays populated and `total_outbound_connection_attempts` keeps
climbing. The same procedure on `main` drives both to zero.

A negative result is that dialling still stops after the change, which would
mean the sink was not the only unbounded wait on that path.

## Protocol impact

None. `pkg/log` has no wire surface, no chunk format and no consensus surface.
The protocol freeze check does not fire. Nothing a peer can observe changes.

## Rollout and rollback

Rollback is `--log-sink-buffer 0`, which restores the synchronous sink exactly.

Log ordering within a single writer is preserved. Operators who parse logs see
no format change; they may see a `dropped N log lines` notice where previously
they would have seen a node that stopped working.

## Related, and deliberately not in scope

`manage`'s `wg.Wait()` at `kademlia.go:629` is unbounded, so any future stuck
dial parks the loop the same way whatever the sink does. That is defence in
depth against the next instance of this shape rather than part of this fix, and
it gets its own issue.
