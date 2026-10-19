# Send every transaction to every endpoint, resend the stored bytes, and watch pending nonces

Issue: [#819](https://github.com/crtahlin/wasp/issues/819) (p0, `stake-risk`,
`affects-upstream`). Related: [#772](https://github.com/crtahlin/wasp/issues/772)
(the same signed bytes through the next endpoint),
[#783](https://github.com/crtahlin/wasp/issues/783) (persistent lottery calls
and copies), [#802](https://github.com/crtahlin/wasp/issues/802) (ambiguous
sends wait for the receipt), [#793](https://github.com/crtahlin/wasp/issues/793)
(reveal fee raise).

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

## Hypothesis

1. The transactions in the observation were not mined because only one endpoint
   had them. Sending the same signed bytes to every connected endpoint, for
   every transaction, removes that single point of failure at send time. It is
   safe for the reason #772 and #783 rely on: identical bytes have one hash and
   one nonce, the chain includes at most one transaction per sender and nonce,
   and the fee is paid once.
2. Copies at send time can still miss: an endpoint can be down for the copy,
   drop the transaction from its pool later, or the node can restart with a
   pending transaction. A watchdog that sends the stored bytes again to every
   endpoint, once a pending nonce stays unmined past a bound, covers those
   cases, and turns a silent multi-day stall into an error log and a metric.
3. Neither helps if the node forgets the transaction and reuses its nonce, so
   the node must keep counting the nonce of a transaction an endpoint accepted
   until the chain settles it.

## Design

### 1. Copies for every send (`pkg/transaction/failover`)

`SendTransaction` sends copies after every accepted send, not only after a
persistent one:

- When the active endpoint accepts the transaction, or a further endpoint takes
  it in the #772 loop, `sendCopies` runs as today, for every context. The copy
  attempt bound is the persistent bound when the context carries one, and
  `minAttemptTimeout` (10 s) otherwise.
- When the active endpoint answers `already known` (`classifySendAnswer` returns
  `answerKnown`), `SendTransaction` treats it as accepted: it sends copies and
  returns nil. Today that answer is returned as an error and stops the copies.
  `nonce too low` and every other answer from the active endpoint are returned
  unchanged, with no copies, as today.

The other parts of a persistent send stay limited to the lottery: the fixed
10 s attempt bound for callers without a deadline, and going round the
endpoints again until the context ends. Applying them to every send would be
wrong here. `SendWithHook` holds the service lock across the send, and the
API's, postage's and staking's contexts have no phase end, so a send that
keeps going round would block every other transaction of the node, commits
included, for as long as every endpoint fails.

Copies still go only to connected endpoints, as today. An endpoint that has no
connection is dialled only by the transport-failure loop (#802).

### 2. Broadcast to every endpoint, with every answer (`pkg/transaction/failover`)

A new method for the watchdog and the resend:

```go
// Broadcast sends tx to every connected endpoint concurrently, each attempt
// bounded, and waits for all of them. It returns each endpoint's answer.
func (b *Backend) Broadcast(ctx context.Context, tx *types.Transaction) []transaction.SendAnswer
```

with `transaction.SendAnswer{Endpoint string; Err error}` and a classification
helper `transaction.ClassifySendAnswers(answers) BroadcastResult`:

- `Accepted`: at least one endpoint accepted the bytes or answered
  `already known`;
- `NonceUsed`: no endpoint accepted, and one answered `nonce too low`;
- `Refused`: no endpoint accepted, and every endpoint that answered gave
  another JSON-RPC answer about the transaction (insufficient funds,
  underpriced, and so on). `replacement transaction underpriced` does **not**
  count as refused: it means that endpoint holds another transaction with this
  nonce, so the nonce is not free;
- `Unreachable`: every attempt failed at the transport level.

The string matching is the existing `classifySendAnswer`; it moves to
`pkg/transaction` so that both packages use one copy.

`transaction.Service` reaches `Broadcast` through an optional interface:

```go
type Broadcaster interface {
	Broadcast(ctx context.Context, tx *types.Transaction) []SendAnswer
}
```

The plain single-endpoint backend does not implement it; the service then uses
`SendTransaction` and wraps its error as the one answer.

### 3. Resend sends the stored bytes (`pkg/transaction/transaction.go`, `pkg/api`)

`ResendTransaction(ctx, hash)`:

1. Loads the stored transaction. Unknown hash: `ErrUnknownTransaction`, as
   today.
2. Takes the signed bytes from `Raw`. A record without `Raw` (stored by a build
   before the store-before-send change) is rebuilt from the **stored** fields,
   fee cap and tip included, and signed. Signing is deterministic
   (`btcec.SignCompact`, RFC 6979), so the same fields give the same bytes and
   the same hash. If the hash still differs, it returns
   `transaction hash changed` and sends nothing. A freshly suggested fee is
   never used: a different fee is a replacement, which is
   `ReplaceTransaction`'s job.
3. Sends the bytes with `Broadcast` on the **caller's** context.
4. Returns nil for `Accepted`. For any other result, it returns an error that
   joins every endpoint's answer. `nonce too low` maps to a new
   `ErrNonceUsed`. A legacy `already imported` answer still maps to
   `ErrAlreadyImported`.

The API keeps its routes and status codes: 200 with the hash on success,
404 for an unknown hash, 400 for `ErrAlreadyImported` and now also for
`ErrNonceUsed` (the nonce is used on chain, so there is nothing to resend), and
500 otherwise. The 500 is now a real failure rather than an error that was
ignored, and the log line carries the endpoints' answers. `openapi/Swarm.yaml`
documents the 400 for a used nonce.

### 4. Keep the nonce of an accepted transaction (`filterPendingTransactions`)

A pending entry is one of two kinds, recorded in the existing
`pendingTransaction.Ambiguous` field:

- **Ambiguous** (`Ambiguous: true`): its send returned an error and no endpoint
  has accepted it since. Unchanged: its nonce counts for
  `AmbiguousPendingTimeout`, and the #772 loop re-broadcasts it. When a
  re-broadcast is accepted (or `already known`), the loop now clears the flag,
  so the entry becomes accepted. The flag is cleared only if the pending key
  still exists.
- **Accepted** (`Ambiguous: false`, which is also how entries stored by earlier
  builds and by `CancelTransaction` decode): some endpoint took the bytes.

`filterPendingTransactions` changes for accepted entries only. When the active
endpoint answers `not found`, the entry is kept, and its nonce still counts, as
long as the nonce is not used on chain (`NonceAt(latest) <= nonce`). Once the
nonce is used, the entry is deleted as today, and the monitor reports the
receipt or the cancellation to whoever waits. The extra read is one `NonceAt`
per call of `filterPendingTransactions`, not one per entry.

So the node signs a new transaction at an accepted transaction's nonce only by
a deliberate replacement (`ReplaceTransaction`, `CancelTransaction`), or after
the watchdog releases the nonce under the rule in part 5.

Every read-and-write of a pending key (the deletion here, the flag clear in the
#772 loop, the release in part 5) takes a new mutex, `pendingMu`. It is held
only around store operations, never across a network call, and never together
with `t.lock` in the other order, so it cannot block `SendWithHook` on a
network send.

### 5. Pending-transaction watchdog (`pkg/transaction/watchdog.go`)

One goroutine per service, started in `NewService` on `t.ctx` and stopped by
`Close` (`t.wg`). It never takes `t.lock`.

Every `watchdogInterval` (30 s):

1. Read the chain nonce once: `NonceAt(ctx, sender, nil)`.
2. List the pending entries (`PendingTransactions`, without the filter, which
   could delete entries). For each nonce, keep only the newest stored
   transaction (`newerThan`, as #772 uses), so a replaced or cancelled
   transaction is never sent again over its replacement.
3. Skip entries whose nonce is below the chain nonce (settled; the monitor
   handles them), ambiguous entries (the #772 loop handles them while their
   nonce is held), and entries younger than `pendingResendAfter`.
4. For the remaining **overdue** entries, in ascending nonce order (a later
   nonce cannot be mined before an earlier one), take the bytes as in part 3,
   step 2, and `Broadcast` them with a bounded context (`watchdogInterval`).
5. Act on the result:
   - `Accepted`: log at debug level. The entry stays; the next check sends it
     again if it is still unmined.
   - `NonceUsed`: nothing; the next check sees the chain nonce move past it.
   - `Unreachable`: nothing; tried again at the next check.
   - `Refused`: count it. When an entry is `Refused` at three consecutive
     checks and is older than `AmbiguousPendingTimeout`, the watchdog
     **releases its nonce**: it deletes the pending key (under `pendingMu`) and
     logs at error level with each endpoint's answer. It sends nothing for
     that entry afterwards.

**Logging.** When an entry first becomes overdue, the watchdog logs at error
level: `pending transaction not mined; sending it to every endpoint again`,
with the hash, nonce, description, age and the broadcast result. While it stays
overdue, the same line is logged again at most every 10 minutes per entry. When
an entry that the watchdog has sent is later mined, an info line says so.

**Metrics** (a new `Metrics()` on the service, registered once in `pkg/node`,
as the failover backend's are since #779):

- `bee_transaction_oldest_pending_age_seconds` (gauge): the age, since
  `Created`, of the pending entry with the lowest nonce at or above the chain
  nonce; 0 when there is none. An operator alerts on it, for example above
  300 s.
- `bee_transaction_pending_resent_total{result}` (counter): watchdog
  broadcasts by result, `accepted`, `nonce_used`, `refused` or `unreachable`.
- `bee_transaction_pending_released_total` (counter): nonces released under
  the refused rule.

#### Why `pendingResendAfter` is 60 seconds

- A transaction that reached the block producers is normally mined in the next
  one or two blocks. In the observation, transactions with tips of 6 to 168 wei
  were mined within about a minute of reaching the other endpoints. On Gnosis
  (5 s blocks), 60 s is 12 blocks; on Sepolia (12 s), 5 blocks.
- The bound must leave a stuck earlier transaction time to be mined within a
  commit phase, or the commit behind it is lost. A phase is 38 blocks, about
  190 s on Gnosis. The worst case from sending to the watchdog's broadcast is
  `pendingResendAfter + watchdogInterval` = 90 s, which leaves about 100 s.
- A lower bound sends more broadcasts of transactions that are only waiting
  for a block, each answered `already known`. That costs one request per
  endpoint and is harmless, but it also logs errors for transactions that are
  not stuck. Above 60 s, the blocking lasts longer than it needs to.
- The revealer replaces a reveal after 6 blocks. The watchdog does not raise
  fees, so it can wait longer before it acts.

#### When no endpoint holds the transaction any more

The watchdog first sends the identical bytes to every endpoint. That covers the
observed case, where an endpoint dropped the transaction or never had it: every
endpoint that can still take it does, at the original fee.

If every endpoint refuses the bytes, the design does **not** sign anything new
automatically:

- Signing again at the same nonce with the same fields is not a separate
  option: signing is deterministic, so it gives the same bytes, and that is
  what the watchdog already sends.
- An automatic fee raise is a replacement with a new hash. A caller waiting on
  the original hash (postage, staking, the chequebook) is told by the monitor
  that its transaction was cancelled, even though the replacement did the same
  thing. That changes what those callers report, and the observation gives no
  case where the fee was the cause. The reveal keeps its own raise (#793).
- Holding the nonce forever would block the node's later transactions, commits
  included, for good.

So the watchdog releases the nonce, which is what the node does today, but on
much stronger evidence: today the key is deleted as soon as the **active**
endpoint does not know the transaction; under this design, only when **every**
connected endpoint refused its exact bytes at three checks in a row, and not
before `AmbiguousPendingTimeout` has passed since it was signed. The operator
can still cancel or replace earlier by hand (`DELETE /transactions/{hash}`).

**Nonce safety.** The node never has two different transactions at one nonce
except a deliberate replacement or cancellation:

- the watchdog and the resend only send stored, signed bytes, unchanged;
- for each nonce, they send only the newest stored transaction;
- an accepted entry's nonce is kept until the chain uses it or the watchdog
  releases it, and the watchdog sends nothing for a released entry;
- an ambiguous entry is handled by the #772 loop, which stops two intervals
  before its nonce can be released.

### Interaction with existing mechanisms

- **#772 ambiguous re-broadcast and `storeAndBroadcast`.** Unchanged in what
  they cover: `storeAndBroadcast` marks every send that returned an error as
  ambiguous, and the loop re-broadcasts it every 30 s for about 9 minutes,
  **only on an error**. It never re-sends a transaction that an endpoint
  accepted; that is the watchdog's job. The two never send the same entry: the
  watchdog skips ambiguous entries, and an accepted re-broadcast now turns an
  ambiguous entry into an accepted one.
- **#802 `WaitForSentReceipt`.** Unchanged. A caller still waits at most
  `AmbiguousPendingTimeout` after an ambiguous send. The transaction itself is
  now kept and re-sent beyond that wait once any endpoint has accepted it.
- **#783 persistent calls.** Unchanged for the lottery. Their copies now also
  follow an `already known` answer from the active endpoint.
- **The revealer.** Its reveals are pending entries like any other. The
  watchdog sends only the newest transaction per nonce, so it never sends a
  reveal the revealer has replaced, and a broadcast of the newest reveal's
  bytes does not change what the revealer decides.
- **`CancelTransaction`.** Unchanged, apart from copies of its transaction. It
  still sends before storing; that is outside this change.

### Tests (written first; each must fail on `main`)

A test double, `blackholeBackend`, plays one endpoint that accepts a
transaction, answers `TransactionByHash` with it, never mines it and never
passes it on. The other endpoints share a simulated chain that mines what they
receive (`backendsimulation`). Tests:

1. **Withdrawal is mined through another endpoint.** Failover backend with the
   black hole active and two healthy endpoints. An unmarked `Send` (as the
   wallet withdrawal uses) is mined, through a copy. On `main` it is never
   mined.
2. **A later commit is not blocked.** After test 1's send, a persistent send at
   the next nonce is mined, and both are mined in order.
3. **Copies after `already known`.** The active endpoint answers
   `already known`: `SendTransaction` returns nil and every other connected
   endpoint receives the same bytes.
4. **Unmarked sends do not go round again.** Every endpoint fails at the
   transport level: an unmarked send makes one pass and returns, and
   `bee_failover_wraparounds_total` does not move.
5. **Resend sends identical bytes.** `ResendTransaction` sends exactly the
   stored `Raw` bytes (compared byte for byte) to every endpoint; it never
   calls `SuggestedFeeAndTip`; it returns the endpoints' answers when all
   refuse; `nonce too low` gives `ErrNonceUsed`; a record without `Raw` is
   rebuilt to the same hash, and a record whose rebuild changes the hash sends
   nothing. The API test maps these to 200, 400 and 500.
6. **The nonce is kept.** An accepted entry that the active endpoint does not
   know, with its nonce unused on chain, is kept by
   `filterPendingTransactions`, and `nextNonce` returns the nonce after it.
   With its nonce used on chain, it is deleted.
7. **Watchdog re-sends an overdue entry.** With copies disabled in the double
   (so only the watchdog can deliver it) and the bound shortened, a
   black-holed transaction is broadcast after the bound and mined; the gauge
   rises above 0 and returns to 0; one error line is logged.
8. **Watchdog limits.** It does not send an entry younger than the bound, an
   ambiguous entry, an entry whose nonce is used, or an older transaction
   whose nonce has a newer stored one. It sends overdue entries in ascending
   nonce order. It releases a nonce only after three consecutive refusals and
   after `AmbiguousPendingTimeout`, and sends nothing for it afterwards. It
   ends when the service closes.
9. **Promotion.** An ambiguous entry whose re-broadcast is accepted becomes
   accepted, and is then kept past `AmbiguousPendingTimeout`.

**Mutation checks.** Each of these must make at least one test fail with a
message, not a hang (each test has its own deadline): copies only for marked
contexts again; `already known` from the active endpoint returned as an error
again; the watchdog's broadcast removed; the ascending order reversed; the
newest-per-nonce rule removed; `filterPendingTransactions` deleting accepted
entries on `not found` again; `ResendTransaction` re-signing with a suggested
fee again; the release rule's count or age check removed.

## Protocol impact

None. Nothing under `pkg/p2p/`, `pkg/swarm/` or `pkg/config/` changes, and the
frozen wire surface is untouched. The transactions on the chain are the same
signed transactions the node signs today, byte for byte; only how many of the
node's own RPC endpoints receive them, and how often, changes. The API change
is one more 400 case on `POST /transactions/{hash}` and a resend that now does
what its documentation says.

## Measurement

**Unit tests** above, required for merge.

**On the bench.** On the `bench-1` Sepolia full node only, never on a fleet
host. A local JSON-RPC proxy on the loopback interface, as in #802's results,
plays the endpoint that keeps transactions to itself: it answers
`eth_sendRawTransaction` with the transaction's hash without forwarding it,
answers `eth_getTransactionByHash` for those hashes as a pending transaction,
and forwards every other request to a public Sepolia endpoint. It logs the
hash, nonce and a digest of the bytes of every send it sees. Withdrawals are
small transfers of the test token to an address the operator controls.

| Scenario | Endpoint list | Action | Pass (new build) | Expected on the stock build |
|---|---|---|---|---|
| A. One endpoint keeps it | proxy first, two public Sepolia endpoints after it | one withdrawal | mined within 3 blocks of the send | not mined after 10 minutes |
| B. Copies also lost | the proxy in front of every endpoint, each dropping only the first send of each transaction it sees | one withdrawal | mined within `pendingResendAfter` + `watchdogInterval` + 3 blocks; one error line; the gauge rises and returns to 0 | not mined after 10 minutes |
| C. A later transaction | as A | a second withdrawal sent while the first is held | both mined, in nonce order | neither mined |
| D. Resend | as B | `POST /transactions/{hash}` 10 s after the send, before the watchdog acts | 200; the proxy's log shows the same byte digest as the first send; mined | 500 `transaction hash changed`, or 200 with nothing sent |

Each scenario runs three times on each build (rule 7), reported with the
spread of the time from send to mined. In every run, the proxy's log must show
exactly one distinct byte digest per nonce.

**A negative result** would be any of these: a withdrawal in A or B that is not
mined within its bound on the new build; a later transaction in C that waits
for longer than the earlier one's delivery; a resend whose bytes differ from
the first send; or two different signed transactions at one nonce in the
proxy's log that neither a replacement nor a cancellation explains. Any of them
means the hypothesis is wrong for that case, and the result is recorded as
such.

**On the fleet, after release**, read-only: the count of nodes whose
`bee_transaction_oldest_pending_age_seconds` exceeds 300 s, and how often the
external check (see "Rollout and rollback") still finds a stuck nonce.
Before the change it found 5 of 17 withdrawals stuck on one day.

## Rollout and rollback

Always on; there is no setting. A node with two or more endpoints gets the
copies on upgrade. Every node, including one with a single endpoint, gets the
resend fix, the nonce rule and the watchdog; a single-endpoint node's watchdog
re-sends to its one endpoint.

The external check the operator runs today (it reads `GET /transactions`,
compares each nonce with the chain and sends the node's signed bytes to every
public endpoint) stays in place until the bench and fleet measurements are in,
and is removed afterwards.

Rollback is the previous build. The stored data is compatible both ways: no
new keys and no new fields. An earlier build treats a kept accepted entry as
it does today, and deletes it if its active endpoint does not know it.

## Upstream portability

`affects-upstream`. Checked at `upstream/v2.8.3`, the latest Bee release and
wasp's base:

- `ResendTransaction` is the same code: it re-signs with a freshly suggested
  fee, returns `transaction hash changed` when the hash differs, and ignores
  every send error except `already imported`. So `POST /transactions/{hash}`
  resends only when the suggested fee equals the stored one.
- `filterPendingTransactions` deletes the pending key of any transaction its
  single endpoint answers `not found` for, so the next transaction can take its
  nonce while the original may still be in a pool elsewhere.
- Nothing re-sends an accepted pending transaction, and no metric shows a
  pending nonce's age.

These are reasoned from the code and from the wasp fleet's observation; no
stock Bee node was measured.

What upstream could take:

- **The resend fix**, without wasp's `Raw` field: rebuild from the stored
  fields with the stored fee cap and tip. Deterministic signing gives the
  original bytes, so the hash check passes, and the send error can be returned.
- **The nonce rule** (keep an accepted entry while its nonce is unused on
  chain) and **the watchdog**, both against one endpoint.
- **Copies to every endpoint** need wasp's failover backend (#109) and do not
  apply to upstream's single endpoint.

## Configuration

No new setting. Constants, deliberately not exposed until a measurement shows
that they matter (rule 8 in `AGENTS.md`):

| Constant | Value | Raising it costs | Lowering it costs |
|---|---|---|---|
| `pendingResendAfter` | 60 s (12 Gnosis blocks) | a stuck transaction, and every transaction behind it, waits longer; above about 100 s a stuck transaction just before a commit phase leaves the commit too little of the phase | more broadcasts and error lines for transactions that are only waiting for a block |
| `watchdogInterval` | 30 s, as the #772 re-broadcast | slower detection, up to one interval more | more `NonceAt` reads and broadcasts; at 30 s that is two reads a minute and, per overdue entry, two sends per endpoint a minute |
| refusals before release | 3 consecutive checks, and at least `AmbiguousPendingTimeout` | a nonce no endpoint will take blocks the node longer | a temporary refusal (a full pool) can release a nonce whose transaction is later mined, so a new transaction at that nonce is lost instead |
| error log repeat | 10 minutes per entry | less visibility of a long stall in the log | log noise |

Other nodes pay nothing: these set how often this node sends its own
transactions to its own RPC providers. The copies cost each provider one
`eth_sendRawTransaction` per transaction the node signs, a few dozen a day for
a staked node, which counts against each provider's rate limit (#768) and is
negligible at that volume.

Generated with help of AI.
