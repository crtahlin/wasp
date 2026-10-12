# Send a failed transaction again, unchanged, through the next endpoint

Issue: [#772](https://github.com/crtahlin/wasp/issues/772). Related:
[#768](https://github.com/crtahlin/wasp/issues/768) (a 429 on send),
[#727](https://github.com/crtahlin/wasp/issues/727) (the ambiguous nonce).

## Problem

When `eth_sendRawTransaction` fails at the transport level (an HTTP 5xx or
429, a timeout, a reset connection), the failover backend
(`pkg/transaction/failover`) returns the error and does not try another
endpoint. `transaction.Service` then marks the transaction as ambiguous and
keeps counting its nonce as used for `AmbiguousPendingTimeout` (10 minutes),
because the endpoint may have accepted it before the response was lost.

On a mainnet node of one operator on 2026-10-11, this cost a won round and the
round after it:

1. The node had won its round. Its claim transaction's send got HTTP 500
   (`-32701 broker: node error`) from the first of four configured
   endpoints. The log shows "send transaction failed at the transport level;
   not retrying elsewhere".
2. `handleClaim` returned the error. Nothing claimed the round: no
   `WinnerSelected` event was emitted for it, and the pot went to the next
   round's winner.
3. The node was chosen again for the next round. Its commit failed with
   `context canceled` when the reveal phase began. That round was lost too.
   The cause is inferred from the code, not observed directly: `nextNonce`
   still counted the claim's nonce as used, so the commit was signed with the
   nonce after it. A transaction cannot be mined while an earlier nonce of
   the same sender is missing, so `sendAndWait` waited for a receipt that
   could not come until the phase ended.
4. Ten minutes after the claim, the ambiguous claim was dropped ("pending
   transactions not found"). Whether that freed the nonce is not checked: if
   the queued commit was still known to the endpoint, it kept counting, and
   the gap may have stayed open until the endpoint dropped the commit. The
   implementation checks the nonce of the node's next transaction in its
   logs.

Five hours later, another node of the same operator lost a commit the same
way: HTTP 500 from the same endpoint, transaction never mined, round lost.
The reveal guard of #752 correctly declined to reveal, so neither node was
frozen.

In both cases the other three endpoints were healthy. Both nodes listed the
failing endpoint first; the 15 nodes that listed another endpoint first had
no failed sends in the same period.

## Hypothesis

The reason given for not failing over a send
(`docs/experiments/rpc-endpoint-failover/spec.md`) is that a resend elsewhere
"risks a duplicate", and that two endpoints may disagree on the pending nonce.
Neither applies when the resend is **the same signed bytes**:

- The same bytes have the same hash and the same nonce. A chain includes at
  most one transaction per sender and nonce, so at most one copy can be
  mined. A second copy is rejected (`already known`, or `nonce too low` once
  the first is mined).
- The nonce is inside the signed bytes. The second endpoint's view of the
  pending nonce is not consulted.

So sending the unchanged transaction to the next endpoint after a transport
failure cannot create a second transaction, and it delivers the transaction
whenever any endpoint works. That removes most ambiguous sends, and with them
the nonce gap that cost the following round.

## Design

### 1. `failover.Backend.SendTransaction`: same bytes, next endpoint

When the active endpoint's send fails and `isTransportFailure(err)` is true:

- try the same `*types.Transaction` on each other connected endpoint in list
  order, without changing which endpoint is active;
- the first endpoint that returns no error ends the loop: return nil, and log
  at info level which endpoint took the transaction;
- an endpoint answering with a JSON-RPC error that means "this transaction
  is already in my pool" also counts as success. Matched without regard to
  case: `already known`, `known transaction` (go-ethereum, Erigon) and
  `AlreadyKnown` (Nethermind result name);
- an endpoint answering `nonce too low` (go-ethereum) or `OldNonce`
  (Nethermind) means a transaction with this nonce is already mined, this
  one or another: stop and return the **original** error, so the transaction
  stays ambiguous as today;
- any other JSON-RPC error is an answer about the transaction and is
  returned, not treated as success. That explicitly includes
  `replacement transaction underpriced` / `ReplacementNotAllowed`,
  `transaction underpriced` / `FeeTooLow`, and `insufficient funds`;
- a transport failure moves on to the next endpoint;
- if every endpoint failed at the transport level, return the first error,
  as today.

The go-ethereum strings are verified in go-ethereum v1.17.3
(`core/txpool/errors.go`, `core/error.go`). The Nethermind names are not
verified here: the implementation captures real responses from each
configured provider by sending one raw transaction twice on the Chiado test
network, and keeps them as test fixtures.

Each attempt uses the caller's context and the same per-attempt timeout as
reads (`attemptContext`). The loop stops as soon as the caller's context is
done (`ctx.Err() != nil`), as `call` does, because an expired deadline is
itself classified as a transport failure and would otherwise run through
every endpoint.

The active endpoint does not change. Every send therefore still tries the
failing endpoint first and pays its latency, and reads stay on it until a read
fails. That is accepted: switching the active endpoint on a write failure is a
separate decision, and moving a provider to the end of
`blockchain-rpc-endpoint` already gives the operator that choice.

With a single configured endpoint, the plain backend is used and part 1 does
nothing. Reads keep their current failover.
The "not retrying elsewhere" warning is replaced by one warning per failed
endpoint and an info line for the endpoint that accepted.

A new counter, `failover_send_resent_total{result="accepted|known|failed"}`,
counts these attempts.

### 2. `transaction.Service`: re-broadcast an ambiguous transaction

A send can still end ambiguous (every endpoint failed, or a timeout). There
is no periodic pending-transaction loop today; each pending transaction has
one goroutine, started by `waitForPendingTx`, that waits for its receipt.
Restored pending transactions get the same goroutine at start through
`waitForAllPendingTx`.

For a transaction whose pending key is marked ambiguous, that goroutine also
re-broadcasts it every `ambiguousRebroadcastInterval` (30 seconds, a package
variable so that tests do not sleep) with the existing
`RebroadcastTransaction`, which sends the stored raw bytes unchanged and
checks the hash. It stops re-broadcasting when any of these holds:

- the backend knows the transaction (`TransactionByHash` finds it), or its
  receipt arrives;
- `AmbiguousPendingTimeout` has passed since it was created;
- another stored pending transaction has the same nonce: it was replaced
  (`ReplaceTransaction`, used by the revealer) or cancelled
  (`CancelTransaction`, the API). Re-sending the original after that could
  let an endpoint whose pool lacks the replacement mine the original
  instead;
- `TransactionStatus` reports it cancelled;
- the service is closed (`t.ctx`, `t.wg`).

The goroutine never takes `t.lock`: `SendWithHook` holds that lock across a
network send, and a re-broadcast must not wait on it or block it.

Reveals already have their own re-broadcast and replacement in the revealer
(`revealer.go`). Part 2 applies to them as well; the same-nonce rule above
makes it stop as soon as the revealer replaces one, and before that a
re-broadcast of the same bytes is harmless.

`ResendTransaction` is not used: it re-signs with a fresh fee, which changes
the hash.

### 3. `sendAndWait`: wait on an ambiguous transaction instead of failing

In `pkg/storageincentives/redistribution`, `sendAndWait` returns at once when
the send returns an error. It now waits for the receipt, with the caller's
context, when the send error is **not** `transaction.ErrNotBroadcast` and a
non-zero transaction hash was returned. That is exactly the set of sends
`storeAndBroadcast` marks as ambiguous: the transaction is stored and may be
on its way. With part 2 re-broadcasting it, a claim whose first send failed is
mined later in the same claim phase. If the phase ends first, the caller's
context ends the wait, as today.

The transport-failure classifier is not used here: it is unexported in
`failover`, which imports `transaction`, so `transaction` and the
redistribution package cannot reach it without an import cycle. The cost of
this simpler rule: a send that an endpoint rejected outright (for example
insufficient funds) also waits until the phase ends instead of failing at
once. The round is lost either way, and the error is still logged when the
wait ends.

Errors before the broadcast (`ErrNotBroadcast`, zero hash) still return at
once.

### Code, tests and documents that change with it

- `TestSendTransactionDoesNotFailOver` (`failover_test.go`) is inverted.
- The comment above `SendTransaction` in `failover.go` and the
  "`SendTransaction` does not fail over" paragraph of
  `docs/experiments/rpc-endpoint-failover/spec.md` are updated to point here.
- The `failover` package gets a `Metrics()` method; the backend is created in
  `pkg/node/chain.go` and its collectors are registered with the node's
  other metrics.
- `backendmock` already offers `WithSendTransactionFunc` and
  `WithTransactionByHashFunc`, enough for parts 1 and 2. Part 3 needs a
  transaction-service fake that returns a hash together with an error.
- `docs/DIFFERENCES.md` gets a row.

### Not changed

- Which endpoint is active, and the recovery to the first endpoint.
- Reads and calls.
- `AmbiguousPendingTimeout` and the nonce counting of #727.
- The reveal guard and its missing-commit check (#752).

## Protocol impact

None. No change under `pkg/p2p/`, `pkg/swarm/` or `pkg/config/`; nothing a
peer sees changes. The change only alters how this node delivers its own
transactions to its own RPC endpoints.

## Measurement

**Unit tests (required for merge):**

- Part 1: first endpoint returns HTTP 500 on send, second accepts: the send
  returns nil, the same hash reached the second endpoint, the active endpoint
  is unchanged. Also: second answers `already known` (success), `nonce too
  low` (original error returned), insufficient funds (that error returned),
  all endpoints fail (first error returned, every endpoint tried once).
- Part 2: a send that failed is re-sent with identical raw bytes on each
  interval until the backend knows it; not after the timeout; not after a
  same-nonce replacement or cancel is stored; never re-signed; the
  goroutine ends when the service closes. Also for a pending transaction
  restored at start.
- Part 1, context: with the caller's context already done, no endpoint after
  the first is tried.
- Part 3: a claim whose send fails at the transport level, then is mined:
  `Claim` returns success within the context; with no receipt before the
  context ends, it returns the context error.
- A mutation check for each part: removing the loop, the re-broadcast or the
  wait makes the matching test fail with a message, not a hang.

**On real nodes:** run the build on the operator's 20 staked mainnet nodes
for at least 7 days, with the failing endpoint back in first place on five of
them (the configuration that lost two rounds in 13 hours). Count from logs
and the new counter:

- transport-level send failures, and how many were accepted by another
  endpoint;
- rounds lost to a failed claim, commit or reveal (today 2 in 13 hours).

A positive result: send failures still happen, but no round is lost to them.
A negative result: rounds are still lost to send failures, which would mean
the failures are not transport failures, or that the other endpoints fail at
the same moment.

## Rollout and rollback

Always on; there is no setting. Part 1 only acts after a send has already
failed, so on a healthy first endpoint nothing changes. Rollback is the
previous build.

An operator who wants to avoid one provider today can move it to the end of
`blockchain-rpc-endpoint`; that stays useful after this change, because a
send that succeeds on the first try is still the fastest.

## Upstream portability

Upstream Bee has one RPC endpoint and no failover backend, so parts 1 and 2
have nothing to attach to there. Part 3 and the reasoning "re-send the same
signed bytes after a transport failure" apply to upstream's single endpoint
too (a re-send to the same endpoint), but this spec does not claim an
upstream defect and the issue is not labelled `affects-upstream`.

## Configuration

None. The 30-second re-broadcast interval is a constant: it is short
compared with the 10-minute timeout and with a 76-block claim phase, and
costs one `eth_sendRawTransaction` per ambiguous transaction per interval, on
this node's own endpoints only. Other nodes pay nothing.
