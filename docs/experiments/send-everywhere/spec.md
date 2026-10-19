# Send every transaction to every endpoint, resend the stored bytes, and watch pending nonces

Issue: [#819](https://github.com/crtahlin/wasp/issues/819) (p0, `stake-risk`,
`affects-upstream`). Related: [#772](https://github.com/crtahlin/wasp/issues/772)
(the same signed bytes through the next endpoint),
[#783](https://github.com/crtahlin/wasp/issues/783) (persistent lottery calls
and copies), [#802](https://github.com/crtahlin/wasp/issues/802) (ambiguous
sends wait for the receipt), [#793](https://github.com/crtahlin/wasp/issues/793)
(reveal fee raise). Follow-up: [#821](https://github.com/crtahlin/wasp/issues/821)
(cancellation stored after it is sent).

## Problem

A sender's transactions are mined in nonce order. When one transaction never
reaches the block producers, every later transaction from the same node waits
behind it, storage-lottery commits included. The node then misses every round
it is selected for, and nothing in the node reports it.

### Only lottery transactions are copied to every endpoint

`failover.Backend.SendTransaction` (`pkg/transaction/failover/send.go`) sends
copies of an accepted transaction to every other connected endpoint only for a
persistent send, that is a context marked with `transaction.WithPersistentCalls`.
Only `Agent.start` (`pkg/storageincentives/agent.go`) sets the mark, on the
commit, reveal and claim handlers (#783). Every other transaction goes to the
active endpoint only: the wallet withdrawal (`POST /wallet/withdraw/{coin}`),
the stake approve and deposit, the postage approve, `createBatch`, `topUp`,
`increaseDepth` and `expireLimited`, the chequebook's transactions and the
API's cancellation. A transport failure on that endpoint fails over (#772). An
endpoint that **accepts** the transaction into its own pool and never passes it
on decides alone.

Observed on a production fleet on 2026-10-19:

- 5 of 17 BZZ withdrawals were accepted by one public endpoint and never
  propagated. The other endpoints had never seen them, and they stayed unmined
  for 30 minutes.
- One lottery commit, sent before #783 was deployed, had stayed unmined the
  same way for four days, and blocked the node's later transactions.
- Sending the same signed bytes to the other endpoints got each of them mined
  within about a minute, at its original fee. Their tips were 6 to 168 wei.
  Most Gnosis transactions in the same blocks carried tips under 1,000 wei, so
  the fee was not the cause.

### An "already known" answer from the active endpoint stops the copies

For a persistent send, copies go out only after `SendTransaction` returns nil
from the active endpoint, or after the transport-failure loop finds an endpoint
that accepted or answered `already known`. When the active endpoint itself
answers `already known` (the expected answer when the same bytes are sent
again to the endpoint that holds them), that answer is a JSON-RPC error, not a
transport failure. `SendTransaction` returns it at once, and no copy is sent.
So a re-send of a transaction stuck in the active endpoint's pool never reaches
the other endpoints, even for a lottery transaction.

### The resend endpoint cannot resend

`POST /transactions/{hash}` calls `ResendTransaction`
(`pkg/transaction/transaction.go`). It builds the transaction again from the
stored fields with a **newly suggested** fee cap and tip
(`SuggestedFeeAndTip`) and signs it. Unless the suggestion happens to equal the
stored fees, the hash differs and it returns `transaction hash changed`. When
the hash matches, it sends on the service's own context, not the request's, and
ignores every send error except one containing `already imported`, reporting
success.

The stored record already holds what a real resend needs. `StoredTransaction`
keeps the fee cap (`GasFeeCap`), the tip (`GasTipCap`), the nonce, gas limit,
recipient, value, data and, since `storeAndBroadcast` was changed to store
before sending, the signed bytes as sent (`Raw`). `RebroadcastTransaction`
already sends `Raw` unchanged, checks the hash and returns the send error; only
the internal re-broadcast loop and the revealer use it.

### Nothing watches a pending nonce

- An ambiguous transaction (its send returned an error) is re-broadcast every
  30 s by `rebroadcastAmbiguous` (`pkg/transaction/rebroadcast.go`, #772),
  until the backend knows it, a newer transaction with its nonce is stored, its
  nonce is used, or until two intervals before `AmbiguousPendingTimeout`
  (10 minutes, so about 9 minutes). This covers only sends that **returned an
  error**. A send that an endpoint accepted is never sent again.
- The revealer replaces a reveal with raised fees when it is lost or stuck
  (#793). No other transaction gets a fee raise.
- `waitForPendingTx` waits for the receipt with no time limit. The monitor
  (`monitor.go`) reports a receipt, or a cancellation once another transaction
  has used the nonce 12 blocks deep. A transaction that is never mined and
  whose nonce is never used is waited for forever, and no log line or metric
  shows it.

### The node can forget an accepted transaction and reuse its nonce

`nextNonce` asks the active endpoint for the pending nonce
(`PendingNonceAt`) and raises it to one more than the highest nonce among the
stored pending transactions. Those come from `filterPendingTransactions`, which
asks the active endpoint for each one (`TransactionByHash`). When the answer is
`not found` and the transaction is not a recent ambiguous one, it **deletes the
pending key** ("pending transactions not found") and the nonce no longer counts.

That is safe only if "the active endpoint does not know it" means "nobody does".
With the endpoint that keeps a transaction to itself as the active endpoint,
the pending key stays (that endpoint knows it), which is why the stuck
transactions in the observation were listed by `GET /transactions`. When the
active endpoint changes to one that never saw the transaction, the key is
deleted and the next transaction is signed with the same nonce: two different
transactions at one nonce, of which at most one can be mined, and the caller of
the first is never told. Upstream Bee has the same `filterPendingTransactions`
(checked at `upstream/v2.8.3`).

### The ambiguous timeout releases a nonce on a timer

An ambiguous transaction's nonce counts for `AmbiguousPendingTimeout`
(10 minutes). After that, `recentAmbiguousTransaction` returns nil, and the
next `filterPendingTransactions` deletes the entry if the active endpoint does
not know it. During the incident, one transaction was unknown to every
endpoint for a few minutes, then reappeared and was mined. "No endpoint holds
it" is therefore not proof that a transaction is gone, and a timer is not a
safe reason to give its nonce to another transaction.

## Hypothesis

1. The transactions in the observation were not mined because only one endpoint
   had them. Sending the same signed bytes to every configured endpoint, for
   every transaction, removes that single point of failure at send time. It is
   safe for the reason #772 and #783 rely on: identical bytes have one hash and
   one nonce, the chain includes at most one transaction per sender and nonce,
   and the fee is paid once.
2. Copies at send time can still miss: an endpoint can be down for the copy,
   drop the transaction from its pool later, or the node can restart with a
   pending transaction. A watchdog that sends the stored bytes again to every
   endpoint while a nonce stays unmined covers those cases, and turns a silent
   multi-day stall into an error log and a metric.
3. Neither helps if the node forgets a transaction and reuses its nonce. If the
   only event that frees a nonce is the chain's account nonce passing it, the
   node can never put a second, different transaction at that nonce by
   accident.

## Design

**Invariant.** The node never releases a nonce on its own, and it never sends
two different transactions at one nonce except as a deliberate replacement
that it keeps tracking. A pending entry is removed only when the chain's
account nonce (`NonceAt(latest)`) is greater than the entry's nonce. Every
part below follows from this rule, and a test checks it (Measurement, test 17).

### 1. Copies to every configured endpoint (`pkg/transaction/failover`)

`SendTransaction` sends copies after every accepted send, not only after a
persistent one:

- When the active endpoint accepts the transaction, or a further endpoint takes
  it in the #772 loop, `sendCopies` runs for every context. The copy attempt
  bound is the persistent bound when the context carries one, and
  `minAttemptTimeout` (10 s) otherwise.
- When the active endpoint answers `already known` (`classifySendAnswer` returns
  `answerKnown`), `SendTransaction` treats it as accepted: it sends copies and
  returns nil. Today that answer is returned as an error and stops the copies.
  `nonce too low` and every other answer from the active endpoint are returned
  unchanged, with no copies, as today.
- Copies go to **every configured endpoint**, not only the connected ones. For
  an endpoint without a live connection, the copy goroutine first dials it
  with `connect`, bounded by `copyDialTimeout` (5 s), and then sends. All of
  this runs in the background goroutines that `sendCopies` already starts, on
  the backend's `closed` context, so it never delays the caller and `Close`
  ends it. A connection made this way is kept, as #802's dial keeps one, and it
  never changes the active endpoint: `connect` only stores the connection, and
  only `advance` and `Recover` move the active index.

The other parts of a persistent send stay limited to the lottery: the fixed
10 s attempt bound for callers without a deadline, and going round the
endpoints again until the context ends. `SendWithHook` holds the service lock
across the send, and the API's, postage's and staking's contexts have no phase
end, so a send that kept going round would block every other transaction of
the node, commits included, for as long as every endpoint failed.

At startup, `InitChain` (`pkg/node/chain.go`) logs a warning when fewer than two
blockchain endpoints are configured: such a node gets no copies, and one
endpoint that keeps a transaction to itself decides alone.

### 2. Broadcast to every endpoint, with every answer (`pkg/transaction/failover`)

A new method for the watchdog and the resend:

```go
// Broadcast sends tx to every configured endpoint concurrently, dialling one
// without a connection first, each attempt bounded, and waits for all of
// them. It returns each endpoint's answer.
func (b *Backend) Broadcast(ctx context.Context, tx *types.Transaction) []transaction.SendAnswer
```

with `transaction.SendAnswer{Endpoint string; Err error}`. A helper
`transaction.ClassifySendAnswer(err) AnswerKind` names each answer:

| Kind | Answers (matched without regard to case) | Meaning |
|---|---|---|
| `Accepted` | nil, `already known`, `known transaction`, `AlreadyKnown` | the endpoint holds the bytes |
| `NonceUsed` | `nonce too low`, `OldNonce` | the chain has used the nonce |
| `HeldOther` | `replacement transaction underpriced`, `ReplacementNotAllowed` | the endpoint holds another transaction at this nonce |
| `FeeTooLow` | `transaction underpriced`, `FeeTooLow`, `max fee per gas less than block base fee`, `fee cap less than block base fee` | the fee is the problem |
| `Definite` | `insufficient funds`, `intrinsic gas too low`, `gas limit reached`, `exceeds block gas limit`, `InsufficientFunds`, `GasLimitTooLow` | the bytes can never be mined as they are |
| `Other` | any other JSON-RPC answer | logged, no action |
| `Unreachable` | a transport failure or a failed dial | the endpoint did not answer |

The go-ethereum strings are the ones #772 verified against go-ethereum
v1.17.3. The implementation verifies the others against each configured
provider on a test network and keeps the answers as test fixtures, as #772 did.
The existing `classifySendAnswer` moves to `pkg/transaction`, so both packages
use one copy.

`transaction.Service` reaches `Broadcast` through an optional interface:

```go
type Broadcaster interface {
	Broadcast(ctx context.Context, tx *types.Transaction) []SendAnswer
}
```

The plain single-endpoint backend does not implement it. The service then uses
`SendTransaction` and wraps its error as the only answer.

### 3. Entries are removed only by the chain nonce (`filterPendingTransactions`)

**Must fix.** `filterPendingTransactions` never deletes a pending entry
because the active endpoint answers `not found`. It reads the chain nonce once
per call. An entry whose nonce is below the chain nonce is deleted, and the
monitor reports its receipt or its replacement to whoever waits. Every other
entry is kept, and its nonce counts for `nextNonce`. When the chain nonce
cannot be read, every entry is kept, as an error from `TransactionByHash`
already keeps an entry today.

`AmbiguousPendingTimeout` no longer releases a nonce. An ambiguous entry's
nonce counts until the chain nonce passes it, like any other entry. The
constant keeps one meaning only: the longest time `WaitForSentReceipt` (#802)
makes a caller wait after an ambiguous send. When that wait ends, the caller
gets its error as today, but the transaction stays tracked, and the watchdog
keeps sending it. The #772 30-second loop is removed. The watchdog covers
ambiguous entries from their first check onward, so there is one mechanism
that sends a pending transaction again, not two.

Every read-and-write of a pending key takes a new mutex, `pendingMu`. It is
held only around store operations, never across a network call, and never
while waiting for `t.lock`.

### 4. Replacements are tracked, and callers wait on the nonce

`StoredTransaction` gains one field:

```go
Replaces common.Hash // the transaction this one replaces at the same nonce, or zero
```

`ReplaceTransaction`, the watchdog's fee raise (part 6) and `CancelTransaction`
set it, so every transaction at a nonce links back to the first one, the
**original**. A record without the field decodes as zero, and an earlier build
ignores the field, so the stored data stays compatible both ways.

**How the receipt wait learns about replacements.** Today `WaitForReceipt`
watches one hash, and the monitor reports `ErrTransactionCancelled` to it when
another transaction's receipt confirms the nonce. The change:

1. The monitor, when a receipt confirms a nonce, passes that **receipt** to
   every watch on the nonce, not only to the matching hash. Every transaction
   the node stores at a nonce is watched (`storeAndBroadcast` and
   `CancelTransaction` call `waitForPendingTx`), so the receipt of a
   replacement is fetched whichever transaction is mined.
2. `WaitForReceipt(ctx, hash)` becomes a wait **on the nonce**: it resolves the
   original of `hash` through `Replaces`, and returns the confirming receipt
   when its hash is the original or any transaction whose `Replaces` chain
   leads to that original **and** whose recipient, value and data equal the
   original's. That is the operation's outcome, and callers read it as today
   (status, logs). A replacement found in the store after the wait started
   counts too, because the check runs when the receipt arrives, not when the
   wait starts.
3. A receipt for any other transaction at the nonce is a cancellation:
   `ErrTransactionCancelled`, as today. That covers `CancelTransaction`'s
   zero-value transfer, whose data differs, and a transaction the store does
   not know.

So postage, staking, the chequebook and the withdrawal see success when a
replacement of their transaction is mined, and a cancellation only when the
operation was really cancelled. The revealer already handles its own reveals
by hash, and its replacements now carry `Replaces` as well.

### 5. Resend (`ResendTransaction`, `POST /transactions/{hash}`)

`ResendTransaction(ctx, hash)` returns a result instead of only an error:

1. Unknown hash: `ErrUnknownTransaction` (404, as today).
2. If the chain nonce is above the transaction's nonce, the nonce is used:
   - the transaction itself was mined (its receipt exists): **200** with the
     hash and the block number;
   - another transaction used the nonce: **409 Conflict** with that
     transaction's hash, when the node knows it (a stored replacement or
     cancellation, from the receipt the monitor saw or from the stored
     transactions at that nonce); otherwise the body says the nonce was used
     by a transaction this node did not store.
3. Otherwise it sends the stored bytes with `Broadcast` on the caller's
   context. A record without `Raw` is rebuilt from its stored fields, fee cap
   and tip included; signing is deterministic (`btcec.SignCompact`, RFC 6979),
   so the hash is the original's, and if it is not, nothing is sent and the
   answer is 500. A freshly suggested fee is never used. The answer is **200**
   with the state (`pending`, `overdue` or `blocked`) and each endpoint's
   answer: `accepted`, `already known`, or `refused` with the endpoint's text.
   It is 200 also when every endpoint refused, because the request itself
   succeeded; the answers say what happened.
4. **400** only for a malformed request (an invalid hash), as today.

`ErrAlreadyImported` and the `already imported` match are removed: no
endpoint in use answers with it, and an `already known` answer is now
`accepted`.

`GET /transactions` and `GET /transactions/{hash}` entries gain a `state`
field: `pending`, `overdue` (unmined past the watchdog's bound), `blocked`,
with a `blockedReason` holding the endpoint's answer, or `mined`. The OpenAPI
document (`openapi/Swarm.yaml`) describes the new answers and fields, and the
code pull request records each change as a row in `docs/DIFFERENCES.md`.

### 6. Pending-transaction watchdog (`pkg/transaction/watchdog.go`)

One goroutine per service, started in `NewService` on `t.ctx` and stopped by
`Close` (`t.wg`). It never holds `t.lock` while it sends.

The bounds are in blocks, converted to time on every check with the measured
block time: `transaction.ObservedBlockTime(backend, blockTime)` (the failover
backend reports `AverageBlockTime` from its active endpoint; `blockTime` is the
configured block time, used until something is measured):

- an entry is **overdue** when it was signed more than `overdueBlocks` (12)
  blocks ago;
- the watchdog checks every `checkBlocks` (6) blocks.

On Gnosis (5 s blocks) that is 60 s and 30 s; on Sepolia (12 s blocks),
144 s and 72 s.

On each check:

1. Read the chain nonce once: `NonceAt(ctx, sender, nil)`.
2. List the pending entries (`PendingTransactions`, without the filter). For
   each nonce, take only the newest stored transaction (`newerThan`), so a
   replaced or cancelled transaction is never sent again over its
   replacement.
3. Skip nonces below the chain nonce (settled; part 3 removes them), entries
   that are not yet overdue, and blocked entries (see below).
4. For each remaining entry, in ascending nonce order (a later nonce cannot be
   mined before an earlier one), `Broadcast` its stored bytes with a context
   bounded by one check interval.
5. Act on the answers, in this order:
   - any `Accepted`: nothing more; the entry is sent again at the next check
     while it stays unmined;
   - any `NonceUsed`: nothing; the next check sees the chain nonce pass it;
   - any `FeeTooLow` answer, or the entry's fee cap below the latest block's
     base fee: **replace** it (below);
   - any `Definite` answer and no `Accepted`: mark the entry **blocked**;
   - anything else (`HeldOther`, `Other`, `Unreachable`): nothing; try again at
     the next check.

**Fee raise only when the fee is the problem.** The watchdog calls
`ReplaceTransaction` for the entry. It signs the same recipient, value, data
and gas limit at the same nonce, with the fee cap and the tip each raised to
the larger of the current suggestion and the stored value plus
`ReplacementBumpPercent` (12 %, above the 10 % minimum nodes require). It
stores the replacement with `Replaces` set, under `t.lock`, before sending, and
sends it to every endpoint. Limits:

- at most one replacement per nonce per check;
- the fee cap of a replacement is never above `maxFeeRaise` (4) times the
  original's fee cap. When the next raise would pass that, the entry is marked
  blocked with the reason `fee raise limit reached`;
- reveals are not replaced by the watchdog. The revealer replaces its own
  reveals (#793), and two replacers on one nonce would raise the fee twice.
  The watchdog still sends a reveal's newest bytes again. The revealer's sends
  are told apart by a new `TxRequest.SelfReplacing` flag, stored with the
  transaction.

**Blocked nonces.** A blocked entry stays pending, and its nonce still counts.
The node:

- stores the reason (the endpoint's answer) in the pending value
  (`pendingTransaction.Blocked`, `BlockedReason`);
- logs at error level: `pending transaction cannot be mined; free its nonce with DELETE /transactions/{hash}`,
  with the hash, nonce, description and reason, once when it is blocked and
  then once an hour;
- keeps checking it: if the chain nonce passes it, it is removed as any other
  entry. If the chain nonce has not passed it, it is not sent again, because
  every endpoint gave a definite answer, but an `Accepted` answer at a later
  resend from the API clears the mark.

The operator frees a blocked nonce with the existing
`DELETE /transactions/{hash}`, which signs a zero-value transfer to the node's
own address at that nonce. Nothing frees it automatically. A cancellation
needs native funds for its gas; a node blocked on `insufficient funds` with no
native funds at all must be funded first.

**Logging.** When an entry first becomes overdue, the watchdog logs at error
level: `pending transaction not mined; sending it to every endpoint again`,
with the hash, nonce, description, age in blocks and the answers. While it
stays overdue, the line is repeated at most every 10 minutes per entry. When
an entry the watchdog has sent is mined, an info line says so.

**Metrics** (a new `Metrics()` on the service, registered once in `pkg/node`,
as the failover backend's are since #779):

- `bee_transaction_oldest_pending_age_seconds` (gauge): the age, since
  `Created`, of the pending entry with the lowest nonce at or above the chain
  nonce; 0 when there is none;
- `bee_transaction_pending_resent_total{result}` (counter): watchdog
  broadcasts by outcome, `accepted`, `nonce_used`, `fee_too_low`, `definite`,
  `other` or `unreachable`;
- `bee_transaction_replacements_total{reason}` (counter): replacements the
  watchdog signed, by reason, `fee_too_low` or `below_base_fee`;
- `bee_transaction_blocked_nonces` (gauge): the number of blocked entries.

#### Why 12 blocks and 6 blocks

- A transaction that reached the block producers is normally mined in the next
  one or two blocks. In the observation, transactions with tips of 6 to 168 wei
  were mined within about a minute of reaching the other endpoints.
- A stuck earlier transaction must still be mined within a commit phase, or the
  commit behind it is lost. A phase is 38 blocks. The worst case from sending to
  the watchdog's broadcast is 12 + 6 = 18 blocks, which leaves 20 blocks.
- Fewer blocks send more broadcasts of transactions that are only waiting for a
  block, each answered `already known`. That costs one request per endpoint and
  is harmless, but it also logs errors for transactions that are not stuck.
- Counting in blocks keeps the bounds right on chains with other block times.

### Interaction with existing mechanisms

- **#772 ambiguous re-broadcast.** Removed (part 3). `storeAndBroadcast` still
  marks a send that returned an error as ambiguous; the mark now only tells
  `WaitForSentReceipt` to wait for the receipt.
- **#802 `WaitForSentReceipt`.** The caller's wait keeps its bound. The
  transaction is no longer dropped after it.
- **#783 persistent calls.** Unchanged for the lottery; its copies now also
  follow an `already known` answer and reach unconnected endpoints.
- **The revealer.** Unchanged in what it decides. Its replacements now carry
  `Replaces`, and the watchdog does not replace reveals.
- **`CancelTransaction`.** It sets `Replaces`. It still sends before it stores,
  which breaks the invariant if the node stops between the two; that is
  [#821](https://github.com/crtahlin/wasp/issues/821), follow-up work fixed
  straight after this one.

### Costs this design accepts

- **Later transactions wait behind a blocked nonce.** A blocked nonce holds
  every later transaction of the node until the operator frees it, lottery
  commits included. That is today's behaviour while an endpoint holds the
  transaction; the difference is that the node now says so in its log, its
  metrics and `GET /transactions`. Commits and claims signed behind a blocked
  nonce are mined after it is freed, and those whose round has passed revert
  and pay gas. The implementation may refuse new lottery sends while a nonce
  is blocked; that is a separate decision, not part of this spec.
- **A transaction that nobody will ever mine is sent again forever** until the
  operator cancels it, unless an endpoint gives a definite answer. That costs
  one request per endpoint per check.

### Tests (written first; each must fail on `main` unless marked as a guard)

A test double, `blackholeBackend`, plays one endpoint that accepts a
transaction, answers `TransactionByHash` with it, never mines it and never
passes it on. The other endpoints share a simulated chain that mines what they
receive (`backendsimulation`). Each test has its own deadline. Tests:

1. **Withdrawal is mined through another endpoint.** The black hole is active,
   with two healthy endpoints. An unmarked `Send` is mined through a copy.
2. **A later commit is not blocked.** After test 1's send, a persistent send at
   the next nonce is mined, and both are mined in order.
3. **Copies after `already known`.** The active endpoint answers
   `already known`: `SendTransaction` returns nil and every other endpoint
   receives the same bytes.
4. **Copies dial an unconnected endpoint.** One endpoint has no connection: it
   is dialled in the background and receives the copy; the caller returns
   before the dial ends; the active endpoint is unchanged; a dial that hangs
   ends at `copyDialTimeout`.
5. **Unmarked sends do not go round again** (guard). Every endpoint fails at
   the transport level: one pass, and `bee_failover_wraparounds_total` stays.
6. **Startup warning** with one configured endpoint, and none with two.
7. **Entries are removed only by the chain nonce.** An entry the active
   endpoint does not know, with its nonce unused, is kept, also past
   `AmbiguousPendingTimeout`, and `nextNonce` returns the nonce after it. With
   the nonce used, it is removed.
8. **Watchdog re-sends an overdue entry.** With copies disabled in the double,
   a black-holed transaction is broadcast after 12 blocks (simulated block
   time) and mined; the gauge rises and returns to 0; one error line.
9. **Watchdog limits.** Nothing is sent before 12 blocks, for a settled nonce,
   or for an older transaction whose nonce has a newer one. Overdue entries go
   out in ascending nonce order. The check interval follows a changed block
   time. The goroutine ends when the service closes.
10. **Replacement after an underpriced refusal, and the caller sees success.**
    Every endpoint answers `transaction underpriced` to a postage approve. The
    watchdog signs one replacement, raised by at least 10 % on fee cap and tip,
    with `Replaces` set, stored before it is sent. The replacement is mined.
    The approve's caller, waiting on the original hash, gets the replacement's
    receipt and succeeds. `bee_transaction_replacements_total{reason="fee_too_low"}`
    is 1.
11. **Replacement below the base fee**, and **the raise limit**: a fee cap
    below the base fee is replaced; after the limit, the entry is blocked with
    `fee raise limit reached` and no further replacement is signed.
12. **No fee raise without a fee reason.** Every endpoint is unreachable, or
    answers `replacement transaction underpriced`: no replacement is signed.
    A reveal (`SelfReplacing`) is never replaced by the watchdog.
13. **A cancellation is reported as a cancellation.** A zero-value cancellation
    at the nonce is mined: the original's caller gets `ErrTransactionCancelled`.
14. **Blocked nonce on insufficient funds, freed by cancel.** Every endpoint
    answers `insufficient funds`: the entry is blocked with that reason, the
    error line is logged, `bee_transaction_blocked_nonces` is 1,
    `GET /transactions` shows `blocked`, and the nonce still counts for
    `nextNonce`. Nothing frees it while time passes. After funding,
    `DELETE /transactions/{hash}` signs a cancellation at the nonce; when it is
    mined, the entry is removed and the gauge returns to 0.
15. **Resend answers.**
    - mined: 200 with the block number;
    - nonce used by a stored replacement: 409 with the replacement's hash;
    - nonce used by a cancellation: 409 with the cancellation's hash;
    - nonce used by a transaction the node did not store: 409 saying so;
    - pending: 200 with each endpoint's answer, also when all refuse;
    - the bytes sent are exactly the stored `Raw`, compared byte for byte, and
      `SuggestedFeeAndTip` is never called;
    - a record without `Raw` is rebuilt to the same hash; a rebuild with a
      different hash sends nothing and answers 500;
    - an invalid hash: 400.
16. **`state` field.** `GET /transactions` shows `pending`, `overdue`,
    `blocked` with its reason, and `mined` for an entry whose receipt has
    arrived and that the next filter will remove.
17. **The invariant.** A randomized test runs sends, black holes, refusals,
    replacements, cancellations, endpoint switches, restarts and long waits
    against the simulated chain. After each step, it checks that no two
    different transactions the node signed share a nonce unless one's
    `Replaces` chain leads to the other, and that no entry was removed while
    the chain nonce was at or below its nonce.

**Mutation checks.** Each of these must make at least one test fail with a
message, not a hang: copies only for marked contexts again; `already known`
from the active endpoint returned as an error again; copies skipping
unconnected endpoints; the watchdog's broadcast removed; the ascending order
reversed; the newest-per-nonce rule removed; `filterPendingTransactions`
deleting an entry on `not found`; `AmbiguousPendingTimeout` releasing a nonce
again; the receipt wait matching only its own hash; a fee raise without a fee
reason; the raise limit removed; `ResendTransaction` re-signing with a
suggested fee.

## Protocol impact

None. Nothing under `pkg/p2p/`, `pkg/swarm/` or `pkg/config/` changes, and the
frozen wire surface is untouched. Re-sent transactions are the node's own
signed transactions, byte for byte. A fee-raise replacement is a normal
same-nonce replacement, as the revealer already sends. Only how many of the
node's own RPC endpoints receive them, and how often, changes. The API changes
are the answers of `POST /transactions/{hash}` and the `state` field on
`GET /transactions`; both go into the OpenAPI document and
`docs/DIFFERENCES.md`.

## Measurement

**Unit tests** 1 to 17 above, required for merge.

**On the bench.** On the `bench-1` Sepolia full node only, never on a fleet
host. A local JSON-RPC proxy on the loopback interface, as in #802's results,
sits in front of each endpoint. Per scenario it can accept a send without
forwarding it, drop only the first send of each transaction, or answer a send
with a given JSON-RPC error. It forwards every other request to a public
Sepolia endpoint, and logs the hash, nonce and a digest of the bytes of every
send. Withdrawals are small transfers of the test token to an address the
operator controls.

| Scenario | Set-up | Action | Pass (new build) | Expected on the stock build |
|---|---|---|---|---|
| A. One endpoint keeps it | first endpoint accepts and never forwards | one withdrawal | mined within 3 blocks of the send | not mined after 30 minutes |
| B. Copies also lost | every endpoint drops the first send of each transaction | one withdrawal | mined within 12 + 6 + 3 blocks; one error line; the age gauge rises and returns to 0 | not mined after 30 minutes |
| C. A later transaction | as A | a second withdrawal while the first is held | both mined, in nonce order | neither mined |
| D. Unconnected endpoint | as A, and the second endpoint unreachable at startup, then reachable | one withdrawal | the copy reaches the second endpoint after it becomes reachable; mined; the active endpoint unchanged | not mined |
| E. Underpriced | every endpoint answers `transaction underpriced` to the first two sends of each transaction | a stake approve and deposit | one replacement per refused transaction, at most 12 % above; `POST` of the stake answers success | the stake call fails or never returns |
| F. Insufficient funds | every endpoint answers `insufficient funds` | one withdrawal | blocked with that reason after 12 blocks; the nonce still held after 30 minutes; freed by `DELETE /transactions/{hash}` once the proxy stops refusing | the entry dropped silently and the nonce reused |
| G. Resend | as B, `POST /transactions/{hash}` at each stage | resend while pending, after a replacement, after mining | 200 with answers; 409 with the replacement's hash; 200 with the block number | 500 `transaction hash changed`, or 200 with nothing sent |

Each scenario runs three times on each build (rule 7), reported with the
spread of the time from send to mined.

**Pass rule.** The change passes when every scenario meets its pass column in
three of three runs on the new build, **and** in every run the proxy's log
shows, for each nonce, either exactly one distinct signed transaction, or a
chain of replacements that the node stored with `Replaces` before sending
them, **and** no pending entry disappears from `GET /transactions` while the
chain nonce is at or below its nonce.

**A negative result** would be any of these: a withdrawal in A, B or D not
mined within its bound; a later transaction in C that waits longer than the
earlier one's delivery; a caller in E that reports failure although a
replacement was mined; a nonce in F released by anything other than the
operator's cancel or the chain; a resend whose bytes differ from the stored
ones; or two different signed transactions at one nonce that no stored
replacement or cancellation explains. Any of them means the hypothesis is
wrong for that case, and the result is recorded as such.

**On the fleet, after release**, read-only: the number of nodes whose
`bee_transaction_oldest_pending_age_seconds` exceeds 300 s, the number of
blocked nonces, the replacement counter, and how often the external check (see
"Rollout and rollback") still finds a stuck nonce. Before the change it found
5 of 17 withdrawals stuck on one day.

## Rollout and rollback

Always on; there is no setting. A node with two or more endpoints gets the
copies on upgrade. Every node, including one with a single endpoint, gets the
resend answers, the nonce rule, the replacement tracking and the watchdog; a
single-endpoint node's watchdog sends to its one endpoint, and it logs the
startup warning.

The external check the operator runs today stays in place until the bench and
fleet measurements are in, and is removed afterwards.

Rollback is the previous build. The stored data is compatible both ways: the
new fields (`Replaces`, `SelfReplacing`, `Blocked`, `BlockedReason`) are JSON
fields that an earlier build ignores and this build reads as empty. An earlier
build treats a kept entry as it does today: it may delete it when its active
endpoint does not know it, and it does not know about blocked entries.

## Follow-up work

- [#821](https://github.com/crtahlin/wasp/issues/821): `CancelTransaction` sends
  before it stores, so a crash between the two leaves a mined or pending
  cancellation that the node does not track. That breaks the same invariant
  and is fixed straight after this one.

## Upstream portability

`affects-upstream`. Checked at `upstream/v2.8.3`, the latest Bee release and
wasp's base:

- `ResendTransaction` is the same code: it re-signs with a freshly suggested
  fee, returns `transaction hash changed` when the hash differs, and ignores
  every send error except `already imported`.
- `filterPendingTransactions` deletes the pending key of any transaction its
  single endpoint answers `not found` for, so the next transaction can take its
  nonce while the original may still be in a pool elsewhere.
- `WaitForReceipt` watches one hash, so a caller is told its transaction was
  cancelled when a replacement of it is mined.
- Nothing re-sends an accepted pending transaction, and no metric shows a
  pending nonce's age.
- `CancelTransaction` sends before it stores (#821).

These are reasoned from the code and from the wasp fleet's observation; no
stock Bee node was measured.

What upstream could take:

- **The resend fix**, without wasp's `Raw` field: rebuild from the stored
  fields with the stored fee cap and tip. Deterministic signing gives the
  original bytes.
- **The nonce rule, replacement tracking, the wait on the nonce and the
  watchdog**, all against one endpoint.
- **Copies to every endpoint** need wasp's failover backend (#109) and do not
  apply to upstream's single endpoint.

## Configuration

No new setting. Constants, deliberately not exposed until a measurement shows
that they matter (rule 8 in `AGENTS.md`):

| Constant | Value | Raising it costs | Lowering it costs |
|---|---|---|---|
| `overdueBlocks` | 12 blocks (60 s on Gnosis) | a stuck transaction, and every transaction behind it, waits longer; above about 30 blocks a stuck transaction just before a commit phase leaves the commit too little of the 38-block phase | more broadcasts and error lines for transactions that are only waiting for a block |
| `checkBlocks` | 6 blocks (30 s on Gnosis) | slower detection, up to one interval more | more `NonceAt` reads and broadcasts; per overdue entry, one send per endpoint per check |
| `ReplacementBumpPercent` | 12 % (existing) | each replacement pays more than needed | below 10 % nodes refuse the replacement |
| `maxFeeRaise` | 4 times the original fee cap | a congested chain can take more of the node's funds for one transaction | a transaction is blocked, waiting for the operator, during a base-fee spike it could have outbid |
| `copyDialTimeout` | 5 s | a dial to a dead endpoint holds a background goroutine longer | a slow but working endpoint misses its copy |
| error log repeat | 10 minutes per overdue entry, 1 hour per blocked one | less visibility of a long stall in the log | log noise |

Other nodes pay nothing: these set how often this node sends its own
transactions to its own RPC providers. The copies cost each provider one
`eth_sendRawTransaction` per transaction the node signs, a few dozen a day for
a staked node, which counts against each provider's rate limit (#768) and is
negligible at that volume.

Generated with help of AI.
