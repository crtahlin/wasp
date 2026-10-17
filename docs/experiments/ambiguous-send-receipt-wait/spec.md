# Let the receipt decide after a send that returned an error

Issue: [#802](https://github.com/crtahlin/wasp/issues/802). Related:
[#772](https://github.com/crtahlin/wasp/issues/772) (re-broadcast of an
ambiguous transaction), [#783](https://github.com/crtahlin/wasp/issues/783)
(persistent lottery sends), [#801](https://github.com/crtahlin/wasp/issues/801)
(HTTP 400 / -32601). First code: pull request
[#803](https://github.com/crtahlin/wasp/pull/803), held until this spec is
merged.

## Problem

On a Sepolia test node with three RPC endpoints, three batch purchases
(`POST /stamps`) on 2026-10-17 answered HTTP 500 "cannot create batch". Each
time:

1. `CreateBatch` sent the BZZ approve. The endpoints tried answered
   `eth_sendRawTransaction` with HTTP 429, so `Send` returned the approve's
   hash and the error.
2. `sendApproveTransaction` returned the error before waiting for a receipt,
   and `CreateBatch` stopped.
3. The approve was mined 30 to 60 s later: since #772 the transaction service
   keeps a transaction whose send failed (`storeAndBroadcast` marks it
   ambiguous), holds its nonce for `AmbiguousPendingTimeout` (10 minutes) and
   re-broadcasts it every 30 s.

Result: gas spent, an allowance left on the chain, no batch, and an API error.
Repeating the request repeats the pattern.

The same shape is in every send helper of `pkg/postage/postagecontract` and
`pkg/storageincentives/staking`: each returns the send error and only waits
for a receipt after a send without error. It covers the approve,
`createBatch`, `topUp`, `increaseDepth` (dilute) and `expireLimited` in the
postage contract, and the approve, `manageStake` (deposit and overlay change),
`withdrawFromStake` and `migrateStake` in the staking contract. Unmodified Bee
v2.8.2 has the same code (checked at `upstream/v2.8.2`), without the
re-broadcast.

Two further points, found while reading the code for #802:

- **A JSON-RPC answer is not final either.** `storeAndBroadcast` marks every
  send that returned an error as ambiguous, whatever the error, and
  re-broadcasts it. A temporary refusal (a rate-limit code inside HTTP 200,
  "txpool is full", funds topped up within the window) can therefore be mined
  after the caller reported a failure. On `createBatch` a retry by the caller
  then buys a second batch.
- **The send failover skips endpoints with no connection.**
  `failover.resend` tries every other endpoint, but skips one whose dial
  failed at startup, without a log line, and `Recover` only probes endpoints
  ahead of the active one. Such an endpoint is never tried for a send. On the
  test node the third endpoint's dial failed at every start (the reviewer's
  check of #803), so this did not cause #802; it is changed here on its own
  merits.

## Hypothesis

A send that returned an error after the transaction was signed and stored has
an unknown outcome: the node itself keeps trying to get it mined. Only the
receipt, or the end of the time the node holds the nonce for it, settles it.
If the caller waits for that, instead of reporting the send error, a batch
purchase or stake deposit whose approve is mined late completes, and a
`createBatch` that is mined late is reported as created instead of as a 500.

## Design

### 1. `transaction`: wait for the receipt after a send error

A new method on `transaction.Service`:

```go
// WaitForSentReceipt waits for the receipt of a transaction for which Send
// or SendWithHook returned txHash and sendErr.
WaitForSentReceipt(ctx context.Context, txHash common.Hash, sendErr error) (*types.Receipt, error)
```

- `sendErr == nil`: the same as `WaitForReceipt`.
- `sendErr` wraps `ErrNotBroadcast`, or `txHash` is zero: return `sendErr` at
  once. Nothing was sent.
- Any other `sendErr`: wait for the receipt, under the caller's context and at
  most `AmbiguousPendingTimeout` from the start of the wait. On a receipt,
  return it; the caller checks its status as today. When the wait ends
  without one, return `errors.Join(sendErr, waitErr)`.

This is exactly the set of sends that `storeAndBroadcast` marks as ambiguous,
so the caller's view and the service's view of a send can no longer differ.

**The rule for a JSON-RPC answer: wait in that case too.** The other option
was to treat a JSON-RPC error answer (`rpc.Error`) as final, release its nonce
and stop re-broadcasting it. That option is rejected because it cannot be made
safe:

- With several endpoints, the failover returns the last endpoint's answer
  after earlier endpoints failed at the transport level. An earlier endpoint
  may have taken the transaction before its response was lost, and a persistent
  send (#783) also sends copies to every endpoint. The answer of one endpoint
  says nothing about the others.
- Releasing the nonce lets the next transaction take it. If the first one is
  then mined from another endpoint's pool, the next one is the one that fails,
  which is the ambiguity of #727 moved elsewhere.

So the waiting rule is the only one that cannot leave a mined `createBatch`
behind an API 500 while the node still holds its nonce. What it does not
cover: a copy that is still in some pool after the 10 minutes and is mined
later. Section 4 makes that case visible.

The cost: a send that an endpoint refused for good (for example "insufficient
funds for gas") now holds the caller for up to 10 minutes instead of failing
at once. During those 10 minutes the node holds that nonce anyway, so no
later transaction of the node can be mined either; failing early only gave an
earlier error message. For `POST /stamps` and the stake endpoints the HTTP
request waits that long.

### 2. HTTP 4xx answers

An HTTP error status other than 429 (for example 400, or 401 for a wrong API
key) is an `rpc.HTTPError`, not a JSON-RPC answer. Under the rule above it is
waited on like every other send error.

That differs from `failover.isTransportFailure`, which treats a 4xx other than
429 as an answer and does not try the next endpoint on it. The two answer
different questions and are not meant to agree:

- `isTransportFailure`: would another endpoint do better with this request?
  (#801 is about where that line should be.)
- `WaitForSentReceipt`: can this transaction still be mined? Yes, for any
  error after it was stored, because the service re-broadcasts it.

A wrong API key usually fails the reads before any send (`CreateBatch` reads
the balance and the price first), so a 4xx on the send alone is rare.

### 3. Callers

Both send helpers of `pkg/postage/postagecontract` (the approve and the
contract call) and both of `pkg/storageincentives/staking` call
`WaitForSentReceipt` with the result of `Send`. This covers every
fund-moving transaction those two packages send:

| Package | Transactions |
|---|---|
| `postagecontract` | approve, `createBatch`, `topUp`, `increaseDepth`, `expireLimited` |
| `staking` | approve, `manageStake` (deposit and overlay change), `withdrawFromStake`, `migrateStake` |

The status check after the receipt is unchanged: a reverted approve fails the
operation, and the second transaction is not sent.

Two callers change behaviour beyond the API:

- **Lottery claim (`handleClaim`).** A winner calls `ExpireBatches` before the
  claim. When an `expireLimited` send returns an error, `ExpireBatches` now
  waits for its receipt before the claim is sent, bounded by the claim phase's
  context (76 blocks on mainnet, about 6 minutes, shorter than the 10-minute
  bound). Before, it returned at once and the claim was sent; but the claim
  was then signed with the nonce after the ambiguous expiry and could not be
  mined before the expiry was. So the claim's chance of being mined does not
  change: it lands in the phase exactly when the expiry does. The difference
  is a delay of up to one polling interval between the expiry's receipt and
  the claim's send. Lottery calls are persistent (#783), so such a send error
  is rare: the failover goes round the endpoints until one takes the
  transaction or the phase ends.
- **Startup (`ChangeStakeOverlay`).** A node whose overlay changed sends
  `manageStake` at startup with the node's context. When that send returns an
  error, startup now waits for the receipt, up to 10 minutes, instead of
  exiting with "cannot change staking overlay address". When the transaction
  is mined, startup continues with the stake on the new overlay; when it is
  not, startup fails as before, 10 minutes later.

`pkg/storageincentives/redistribution.sendAndWait` (#772) already waits after
any send error for a claim or commit, and fails at once for a reveal. It is
not changed here; making it call `WaitForSentReceipt` is a later cleanup.

### 4. Logging when the wait ends without a receipt

When a wait after a send error ends without a receipt (the 10-minute bound,
or the caller's context), the service logs at warning level:

```
transaction may still be mined; check it on chain before sending it again
  tx=<hash> description=<request description> error=<send error>
```

Today the API logs the `CreateBatch` error only at debug level, so an
operator who retries a purchase after a 500 has no way to know that the
previous one may still land. With the hash in the log, they can look it up
first.

### 5. `failover`: try endpoints without a connection last, for sends

After every connected endpoint failed to take a send at the transport level,
`resend` dials each configured endpoint that has no connection and, when the
dial works, sends the same signed transaction through it. Connected endpoints
always come first, so a healthy endpoint later in the list is not delayed by a
dial. The dial and the send share one attempt context.

- A connection made this way is kept, as `Recover` keeps one. From then on the
  endpoint is a normal connected endpoint: a target for read failover, for the
  copies of a persistent send (#783), and part of the attempt-time share.
  That is the same state the endpoint would be in after `Recover` had dialled
  it.
- A persistent send goes round the endpoints again every second until one
  takes the transaction. An unconnected endpoint is dialled only in the first
  round of a send, so an unreachable endpoint costs one dial and one warning
  per send, not one per second.
- A refusal from another endpoint during the resend is logged at warning
  level with the endpoint name, as a transport failure already is. Before, it
  was returned without a log line.
- Reads keep skipping unconnected endpoints and leave them to `Recover`.

Known and not changed: a dial that completes after `Close` leaves its
connection open. `Recover` has the same race.

### Code, tests and documents that change with it

- `transaction.Service` gains `WaitForSentReceipt`; `transaction/mock`
  implements it with the same logic over its `WaitForReceipt` function.
- `docs/DIFFERENCES.md`: a row for the receipt wait, naming the claim-path and
  startup changes and the 4xx handling, and the `blockchain-rpc-endpoint` row
  for the failover change.

### Not changed

- `AmbiguousPendingTimeout`, the 30-second re-broadcast and the nonce
  counting of #727.
- `failover.isTransportFailure` (#801 covers it).
- The chequebook (`factory.Deploy`, `erc20.Transfer`, `Withdraw`,
  `CashCheque`). Those return an empty hash on a send error, so the hash has
  to be stored before any wait can work; a separate issue.
- `redistribution.sendAndWait`.

## Protocol impact

None. No change under `pkg/p2p/`, `pkg/swarm/` or `pkg/config/`; nothing a
peer sees changes. The change only alters how this node reports the outcome of
its own transactions and which of its own RPC endpoints it sends them through.

## Measurement

**Unit tests (required for merge):**

- `CreateBatch` on a real transaction service over a fake chain:
  - the approve's send answers HTTP 429 and the approve is mined later: the
    batch is created, `createBatch` is sent once, the stamp issuer is stored;
  - the `createBatch` send answers HTTP 429 and is mined later: the batch is
    recorded and `createBatch` is sent once;
  - the approve's send gets a JSON-RPC refusal and the approve is mined later
    (a temporary refusal): the batch is created;
  - the approve is mined with a revert: `ErrTransactionReverted`, no
    `createBatch` sent;
  - the approve is never mined: the call ends with the caller's context, the
    error contains the send error, no `createBatch` is sent, and the warning
    of section 4 is logged with the hash.
- Stake deposit: the approve's send fails and the approve is mined; the
  deposit's send fails and the deposit is mined; each time the deposit is sent
  once and reported as made. A reverted approve sends no deposit.
- `WaitForSentReceipt`: no wait for `ErrNotBroadcast` or a zero hash; the
  wait's deadline is at most `AmbiguousPendingTimeout`; the send and wait
  errors are joined.
- Failover: an unconnected third endpoint takes a send that the two connected
  ones rate-limited; a connected endpoint is tried before an unconnected one;
  a failed dial returns the first error; a persistent send dials an
  unconnected endpoint once, not on every round.
- A mutation check for every new test, including reverting both
  `sendTransaction` helpers and both approve helpers to "send error, return".

**On a real node:** on the bench-1 Sepolia node, with the endpoint order that
produced #802, buy batches while the endpoints rate-limit sends. Positive
result: no purchase answers 500 while its approve or `createBatch` is later
mined, and every purchase that fails names a transaction hash in a warning
line. Negative result: a 500 followed by a mined approve or `createBatch` of
the same purchase.

## Rollout and rollback

Always on; there is no setting. Nothing changes for a send that succeeds on
the first try. Rollback is the previous build.

## Upstream portability

Upstream Bee has the same send helpers in `postagecontract` and `staking`. It
has no re-broadcast and one endpoint, but a send whose response is lost after
the node accepted the transaction has the same effect there: the operation
reports a failure while the transaction is mined. Part 1 and the callers
would port as they are; part 5 has nothing to attach to upstream. This spec
does not claim an upstream defect and the issue is not labelled
`affects-upstream`: the upstream case is reasoned from the code, not
observed.

## Configuration

None. The 10-minute bound is `AmbiguousPendingTimeout`, the time the node
already holds a nonce for an ambiguous transaction; a different bound for the
wait would let the caller give up while the node still reserves the nonce, or
wait after the node released it. Other nodes pay nothing.
