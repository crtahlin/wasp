# Bounded, repeated and widened chain calls for the storage lottery

Issue: [#783](https://github.com/crtahlin/wasp/issues/783). Related:
[#772](https://github.com/crtahlin/wasp/issues/772) (the same signed bytes
through the next endpoint), [#583](https://github.com/crtahlin/wasp/issues/583)
(per-attempt deadlines), [#776](https://github.com/crtahlin/wasp/issues/776)
(the first send's deadline share), [#768](https://github.com/crtahlin/wasp/issues/768)
(a 429 on send), [#773](https://github.com/crtahlin/wasp/issues/773) (the play
check that reverts).

## Problem

A staked mainnet node with four RPC endpoints was selected, built its sample
and entered the commit phase. Its commit call went to its first endpoint,
which accepted the request and never answered. No error came back, so the
failover backend never moved to the next endpoint. The call waited for the
whole commit phase and ended only when the reveal phase cancelled it:

```
phase failed  error="commit: obfusHash 0x...: context canceled: Post \"<first endpoint>\": context canceled"  phase=commit
gas estimation failed, using fallback  error="execution reverted"  description="reveal transaction"
phase failed  error="reveal of round ... not mined"  phase=reveal
not playing in this round
```

The node's wallet nonce did not change across the round, so no commit was
mined, and the selected round was lost. The other 19 nodes on the same host
logged no RPC error at that time: one unanswered request, not an outage.

Over three days, the same 20 staked nodes lost 2 of 52 selected rounds: one to
this, one to #773.

## Hypothesis

Three things together let one silent endpoint cost a round:

1. **No attempt has a time limit when the caller has none.**
   `failover.Backend.attemptContext` shares the caller's deadline among the
   endpoints still to try (#583). A caller with no deadline keeps its own
   context, and `TestAttemptWithoutCallerDeadline` checks exactly that. The
   storage-incentives phase handlers run on contexts from `events`
   (`pkg/storageincentives/events.go`) that are only cancelled when the next
   phase starts; they have no deadline. The HTTP transport
   (`pkg/node/chain.go`) sets dial, TLS and idle timeouts but no response
   timeout. So a request the endpoint accepted and never answered waits until
   the phase ends.
2. **A call tries each endpoint at most once.** `call` stops after the last
   endpoint, and `advance` never returns to the first. A short outage of every
   endpoint, or an endpoint that hangs once and then answers, costs the call
   even when the phase has minutes left.
3. **A transaction is sent through one endpoint.** If that endpoint takes the
   transaction and never passes it on, or answers too late, nothing else
   carries it. #772 sends the same bytes through the next endpoint only after
   a transport failure on the active one.

Every call in the lottery's send path goes through the same `call`: the pending
nonce (`PendingNonceAt`), the gas estimate (`EstimateGas`), the fee
(`SuggestedFeeAndTip`, `HeaderByNumber`), the send, and the receipt polling.
Any of them can be the one that hangs. In the observed case the nonce did not
change, so the hang was at or before the send; the log does not say which call.

Bounding each attempt, going round the endpoints again until the phase ends,
and sending the signed transaction through every endpoint would have let the
commit through: three other endpoints were healthy and the phase had more than
two minutes left.

## Design

### A marker on the lottery's contexts

`pkg/transaction` gets a context marker (new file `pkg/transaction/persist.go`):

```go
// WithPersistentCalls marks ctx so that a backend able to route between
// endpoints bounds each endpoint attempt by attempt, goes round its endpoints
// again until ctx ends, and sends a signed transaction through every
// connected endpoint.
func WithPersistentCalls(ctx context.Context, attempt time.Duration) context.Context

// PersistentCalls reports the attempt bound set by WithPersistentCalls.
func PersistentCalls(ctx context.Context) (time.Duration, bool)
```

The marker lives in `pkg/transaction`, not in `pkg/transaction/failover`, so
`pkg/storageincentives` sets it without importing the failover package; the
failover package already imports `pkg/transaction`, so there is no cycle.

A context without the marker behaves exactly as today. Only the storage
lottery sets it.

### Where the marker is set

In `Agent.start` (`pkg/storageincentives/agent.go`), the handlers registered for
the **commit**, **reveal** and **claim** phases wrap their context:

```go
ctx = transaction.WithPersistentCalls(ctx, lotteryAttemptTimeout)
```

with `lotteryAttemptTimeout = 10 * time.Second`, the same value as the
existing `minAttemptTimeout` floor.

The **sample** handler is not marked. It runs the reserve sample, which reads
the whole reserve under that context; nothing here should change how the
sample is bounded, and the play check inside it is #773's subject.

Each handler's context is still cancelled when the next phase starts, as
today. The marker adds no deadline of its own, so no estimate of the phase
length can cut a call short: going round the endpoints stops when the phase
does.

Everything the handlers call inherits the marker: `handleCommit`
(`CurrentRound`, `Commit` and its `sendAndWait`), `handleReveal` and the
revealer's `revealUntil`, and `handleClaim` (`IsWinner`, `ReserveSalt`,
`ExpireBatches`, `Claim`). The transaction service passes the caller's context
to every backend call (`nextNonce`, `prepareTransaction`,
`storeAndBroadcast`), so the nonce, gas and fee reads before signing are
bounded too.

Work started on the transaction service's own context is not marked and does
not change: `waitForPendingTx` and the ambiguous re-broadcast of #772
(`rebroadcast.go`) run on `t.ctx`, with their own timeouts.

### Bounded attempts

`attemptContext` gains a first case: when the context carries the marker, an
attempt gets `context.WithTimeout(ctx, attempt)`. An attempt never outlives the
caller's context, as before. A marked attempt that runs out of time ends with
`context.DeadlineExceeded`, which `isTransportFailure` already treats as "this
endpoint did not answer"; the caller's own context has not ended, so `call`
moves on and `advance` logs the existing warning
`blockchain rpc endpoint failed, moving to the next` with the endpoint and
cause.

Unmarked contexts keep the current rule: a share of the caller's deadline with
a floor of `minAttemptTimeout`, or the caller's own context when it has no
deadline. #776 (the share does not count endpoints before the active one) is
not changed here; it does not affect marked calls, which use a fixed bound.

### Going round the endpoints again

For a marked context, `call` does not stop after the last endpoint. When
`advance` finds no further connected endpoint, it moves the active endpoint
back to the first connected one, logs at warning level

```
all blockchain rpc endpoints failed; starting again at the first  first=<name> cause=<last error>
```

waits `wrapPause` (1 s, or until the context ends), and tries again. This
repeats until an endpoint answers or the context ends. The pause keeps a fast
failure on every endpoint (refused connections, for example) from turning into
a busy loop: at most one round of requests per second per call.

Moving the active endpoint back to the first is what `Recover` would do once
that endpoint answered its probe, so it does not change which endpoint the
node prefers; it only does it sooner. The move uses the same compare-and-swap
as `advance`, so a concurrent call that moved first wins.

Unmarked contexts keep today's single pass.

### Sending through every endpoint

For a marked context, `SendTransaction` does this:

1. Send to the active endpoint with a bounded attempt, as today.
2. If that answered with a non-transport error (`nonce too low`, `insufficient
   funds`, `replacement transaction underpriced`, and so on), return it
   unchanged, as today.
3. If it failed at the transport level, send the same bytes through the next
   connected endpoints in order (#772's loop), each with a bounded attempt,
   and for a marked context **go round again from the first** with the same
   `wrapPause`, until one endpoint accepts or answers, or the context ends.
   #772's classification of answers is kept: `already known` counts as
   accepted, `nonce too low` keeps the first error (the send stays ambiguous),
   any other answer is returned.
4. Once any endpoint has accepted the transaction (or reported it as already
   known), send **copies of the same signed bytes** to every other connected
   endpoint, concurrently, without waiting for them. Each copy has its own
   attempt bound and runs on a context the backend cancels in `Close`, so no
   copy outlives the backend. The result of a copy changes nothing for the
   caller; it is counted and logged at debug level, and an answer other than
   accepted or `already known` is logged at warning level with the endpoint.

Why copies are safe: the bytes are identical, so every copy has the same hash
and the same nonce, and the chain includes at most one transaction per sender
and nonce. Only one copy can be mined and the fee is paid once. The backend
never signs: it has no key, so it cannot re-sign with another nonce. A fee
raise stays what it is today, a deliberate replacement by the transaction
service or the revealer.

Why copies help: a transaction an endpoint accepted is not yet in a block. An
endpoint that accepts and does not propagate, or propagates slowly, no longer
decides whether the commit reaches the block producers.

What copies cost: one extra `eth_sendRawTransaction` per connected endpoint
for each transaction sent from the commit, reveal and claim handlers: the
commit, the reveal and the claim, plus any batch-expiry transaction the claim
handler sends. That is a handful of requests a day per node. It counts against each
provider's rate limit (#768); at this volume that is negligible. Every
configured provider sees the transaction, which is public once mined anyway.

Unmarked sends (cheques, postage, staking and everything else) are unchanged.

### Interaction with #772

- `storeAndBroadcast` is unchanged. A send that returns an error is still
  marked ambiguous and handled as today, including the 30 s re-broadcast of
  the stored bytes on `t.ctx`.
- `ambiguousSend` and `waitOnAmbiguous` in
  `pkg/storageincentives/redistribution` are unchanged: a commit or claim
  whose send stayed ambiguous still waits for its receipt; a reveal still fails
  at once.
- With a marked context, the first send is less likely to end ambiguous at
  all, because the loop keeps trying while the phase lasts.

### Logs and metrics

Existing: `blockchain rpc endpoint failed, moving to the next` (warning, from
`advance`), `send transaction failed at the transport level...` (warning),
`transaction accepted by another endpoint` (info).

New:

- warning `all blockchain rpc endpoints failed; starting again at the first`;
- debug `sent a copy of the transaction to another endpoint` with endpoint,
  tx and result; warning when the result is an answer other than accepted or
  known;
- counter `bee_failover_wraparounds_total`: times a marked call went back to
  the first endpoint;
- counter `bee_failover_send_copies_total{result}`, result `accepted`,
  `known`, `answered` or `failed`.

### Tests (written before the change, each must fail on `main`)

In `pkg/transaction/failover`:

1. **Silent first endpoint, caller without a deadline.** Three endpoints; the
   first blocks until its context ends. A marked call with no caller deadline
   and a 50 ms attempt bound returns the second endpoint's answer within a
   test deadline of a few seconds. On `main` the call blocks until the test's
   own guard fires.
2. **Unmarked call keeps today's behaviour.** The same setup without the marker
   still runs the first attempt on the caller's context
   (`TestAttemptWithoutCallerDeadline` stays as it is and passes).
3. **Going round.** Every endpoint fails at the transport level once, then the
   first answers: a marked call succeeds, and the wrap-around counter is 1. An
   unmarked call fails as today.
4. **Going round stops when the context ends.** Every endpoint keeps failing:
   a marked call returns when its context ends, not before, and does not spin
   (request count bounded by the pause).
5. **Send through a silent active endpoint.** The active endpoint never
   answers a send; a marked send is accepted by the second.
6. **Copies.** A marked send accepted by the active endpoint reaches every other
   connected endpoint with the same hash; an `already known` answer from a copy
   is counted as `known`; the caller gets nil without waiting for a silent
   copy.
7. **Copies stop on Close.** A copy blocked on a silent endpoint ends when the
   backend is closed.
8. **Answers are not retried.** A marked send whose active endpoint answers
   `insufficient funds` returns that error and sends no copies.

In `pkg/storageincentives`: the commit, reveal and claim handlers receive a
marked context and the sample handler does not (a test backend records the
marker on the calls it sees).

Each test is mutation-checked: removing the marker check, the wrap-around, the
copies, or the cancel in `Close` must make at least one test fail.

## Protocol impact

None. Nothing under `pkg/p2p`, `pkg/swarm` or `pkg/config` changes, and the
frozen wire surface is untouched. The change is in how a node talks to its
own RPC providers. The transactions it sends are byte-for-byte those it sends
today; only the number of providers that receive them changes.

## Measurement

On the node that lost the round, and its 19 neighbours on the same host, over
at least seven days after the change:

- **Lost selected rounds** from the two causes in #783 and this spec, counted
  from the chain (rounds that selected the node's neighbourhood against the
  node's `Revealed` events) and from the logs (`phase failed` for commit,
  reveal, claim with `context canceled` or `deadline exceeded`). Before the
  change: 1 lost to a hanging call in 52 selected rounds over three days.
- **The counters** `bee_failover_wraparounds_total` and
  `bee_failover_send_copies_total{result}`, and the warning logs, to show the
  new paths ran.

A selected round is rare (a few per node per week), and a silent endpoint is
rarer, so the field measurement can show that nothing regressed and that the
counters move; it cannot by itself show the fix on a real hang. For that, a
bench check: a proxy in front of the first endpoint of a bench node that
accepts requests and holds them open. Without the change, a call made through
it blocks until its context ends; with it, the call answers through the second
endpoint after one attempt bound, and a send reaches every endpoint. A
negative result would be a marked call that still blocks for longer than one
attempt bound plus one pass, or a send that reaches only one endpoint.

## Rollout and rollback

No configuration. A node with two or more RPC endpoints gets the new behaviour
for its lottery calls on upgrade; a node with one endpoint does not, because
`InitChain` keeps the plain backend for a single endpoint and the marker is
ignored there. Rollback is the previous build.

**Limitation:** a single-endpoint node keeps a hanging call's current
behaviour. Wrapping a single endpoint in the failover backend would give it the
attempt bound and the repeat; that is a separate change, with its own issue if
this one proves itself.

## Upstream portability

Upstream Bee has one RPC endpoint and no failover backend. The part it could
take on its own is the bound: give each storage-incentives chain call an
attempt timeout and retry the same endpoint until the phase ends. The
multi-endpoint parts need #109's failover first. The hang itself (no response
timeout on the RPC client, phase handlers without a deadline) is present in
upstream by reading `pkg/node/chain.go` and `pkg/storageincentives/agent.go` at
`upstream/v2.8.2`; it is not reproduced there, so the issue is not tagged
`affects-upstream`.

## Configuration

No new setting. Three constants, deliberately not exposed (rule 8: measure
first, expose second):

| Constant | Value | Raising it costs | Lowering it costs |
|---|---|---|---|
| `lotteryAttemptTimeout` (`pkg/storageincentives`) | 10 s, the existing `minAttemptTimeout` | a hanging endpoint holds the call longer before the next is tried; at 4 endpoints and a 190 s commit phase, 10 s leaves many passes | a slow but working endpoint is abandoned mid-answer; a gas estimate or send normally answers in well under 1 s, so below about 2 s that becomes likely |
| `wrapPause` (`pkg/transaction/failover`) | 1 s | slower recovery when every endpoint fails fast and then one comes back | more requests per second to endpoints that are all failing; at 1 s it is at most one round per second per call |
| copy attempt bound | `lotteryAttemptTimeout` (carried by the marker) | a copy to a silent endpoint lives longer in the background | a slow endpoint may not receive its copy |

None of these costs other nodes anything: they set how often this node asks
its own RPC providers.

Generated with help of AI.
