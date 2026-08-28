Title: A stalled log sink deadlocks peer connectivity — node reports healthy with zero peers and never recovers

### Context

- Bee 2.8.1 (`2.8.1-7cf53193`), light node, macOS on arm64, launched by Swarm Desktop
- Observed and diagnosed on 2.8.1. I have not reproduced it on a later release, so I am reporting what I saw. The code involved is `pkg/log/logger.go` and `pkg/pricing/pricing.go`, and neither looked to have changed since — worth checking whether it still applies to current versions.
- Node uptime at the time of capture: the fault had been in place 105 minutes

### Summary

On 2.8.1, if whatever consumes bee's log output stops reading, `Logger.Write`
blocks indefinitely. Because the peer connection path logs synchronously, that block
propagates into kademlia: the manage loop parks on `wg.Wait()` and never dials
again, never runs its zero-peer bootnode fallback, and never updates its
gauges.

The node stays up, serves its API and reports `{"status":"ok"}` while holding
zero peers. Only a restart recovers it.

This is not specific to Swarm Desktop. Any stalled consumer produces it — a full
disk, a paused container, an unread pipe, a stalled journal. Swarm Desktop is
just where I hit it, because it pipes bee's stdout and stderr over unix sockets
and stopped draining them.

### Expected behavior

A log consumer that stops reading should cost log lines, not connectivity. A
node should keep dialling peers regardless of whether anything is listening to
its logs, and if it cannot log it should degrade by dropping messages.

### Actual behavior

The node held zero peers and made no dial attempts at all. Three samples,
twenty seconds apart:

```
/peers                                          0
/topology .connected                            0
bee_kademlia_currently_connected_peers          88     <- stale
bee_kademlia_total_outbound_connection_attempts 699    <- frozen
bee_kademlia_total_outbound_connection_failed   262    <- frozen
/health                                         {"status":"ok"}
```

The gauge disagreeing with reality is itself diagnostic:
`CurrentlyConnectedPeers` is set inside the manage loop, so 88 is the value from
the last iteration that completed.

From `/debug/pprof/goroutine?debug=2`:

```
goroutine 2148 [sync.WaitGroup.Wait, 105 minutes]:
sync.(*WaitGroup).Wait(...)
github.com/ethersphere/bee/v2/pkg/topology/kademlia.(*Kad).manage(...)
	pkg/topology/kademlia/kademlia.go:629
created by ...kademlia.(*Kad).Start
```

and seven of these:

```
goroutine 2177 [semacquire, 105 minutes]:
internal/poll.runtime_Semacquire(...)
internal/poll.(*fdMutex).rwlock(...)
internal/poll.(*FD).Write(...)                     internal/poll/fd_unix.go:361
os.(*File).Write(...)                              os/file.go:215
github.com/ethersphere/bee/v2/pkg/log.(*logger).log(...)      pkg/log/logger.go:245
github.com/ethersphere/bee/v2/pkg/log.(*logger).Warning(...)  pkg/log/logger.go:199
github.com/ethersphere/bee/v2/pkg/pricing.(*Service).init(...) pkg/pricing/pricing.go:119
github.com/ethersphere/bee/v2/pkg/p2p/libp2p.(*Service).Connect(...) pkg/p2p/libp2p/libp2p.go:1201
github.com/ethersphere/bee/v2/pkg/topology/kademlia.(*Kad).connect(...) pkg/topology/kademlia/kademlia.go:975
```

Checking the process's file descriptors confirms where that write goes: file
descriptors 1 and 2 were both unix sockets held open by the parent process, not
a terminal or a file. The parent had stopped reading them.

The chain:

1. The consumer stops reading; the socket buffer fills.
2. `pricing.(*Service).init` logs a Warning during `Connect` — it fires whenever
   `AnnouncePaymentThreshold` returns an error, which is common as peers go bad.
3. `logger.go:245` does a bare `l.sink.Write(buf)` with no timeout, no bounded
   buffer and no drop path, so it blocks for ever.
4. The dial goroutine never returns.
5. `manage` is inside `wg.Wait()` at `kademlia.go:629`, so the iteration never
   completes.
6. No further dials, and the `connectedPeers.Length() == 0` bootnode fallback is
   never reached.

Worth ruling out explicitly: this is not the connection breaker.
`TotalOutboundConnectionAttempts.Inc()` at `kademlia.go:973` runs *before*
`p2p.Connect`, so a breaker-refused dial would still increment it. That counter
is frozen, so the code never reaches the breaker.

### Steps to reproduce

The logger half reproduces on its own, with no node and no network:

```go
func TestLoggerBlocksOnAStalledSink(t *testing.T) {
	r, w, _ := os.Pipe()
	t.Cleanup(func() { r.Close(); w.Close() })

	lg := log.NewLogger("demo", log.WithSink(w), log.WithVerbosity(log.VerbosityAll))

	var completed atomic.Int64
	go func() {
		for {
			lg.Warning("could not send payment threshold announcement to peer",
				"peer_address", "0000000000000000000000000000000000000000000000000000000000000000")
			completed.Add(1)
		}
	}()

	time.Sleep(2 * time.Second)
	a := completed.Load()
	time.Sleep(5 * time.Second)
	b := completed.Load()
	t.Logf("log calls completed: %d after 2s, %d after 7s", a, b)
}
```

Result — nothing reads `r`, the pipe fills, and the logger stops for good:

```
log calls completed: 368 after 2s, 368 after 7s
```

End to end on a real node, without Swarm Desktop:

1. `bee start | cat`
2. Once it has peers, `kill -STOP` the `cat`.
3. Wait for the pipe to fill (a few hundred log lines).
4. `curl localhost:1633/peers` drains to zero and stays there; `/health` keeps
   reporting ok; `bee_kademlia_total_outbound_connection_attempts` stops moving.

### Possible solution

The logger is the right place to fix it. Fixing only the one call site leaves
every other log statement able to do the same thing.

- A bounded buffered sink with an explicit drop policy, so a stalled consumer
  costs log lines rather than the node. Counting dropped lines matters, or the
  loss is silent.
- Failing that, a write deadline on the sink when it is a pipe or socket.

Narrower and cheaper, if the above is too invasive: make the connect path not
log synchronously. That fixes this instance and leaves the class open.

A separate hardening that would have limited how far this could reach: `manage`'s
`wg.Wait()` at `kademlia.go:629` has no bound, so one stuck dial parks the whole
loop indefinitely. A timeout there would keep a node dialling even when
something below it misbehaves.

### AI Disclosure
- [x] This issue contains suggestions and text generated by an LLM.
- [ ] I have reviewed the AI generated content thoroughly.
- [ ] I possess the technical expertise to responsibly review the AI generated content mentioned in this issue.

---
Generated with help of AI.
