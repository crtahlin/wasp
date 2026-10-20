# Results: let the receipt decide after a send that returned an error

Issue: [#802](https://github.com/crtahlin/wasp/issues/802). Spec:
[spec.md](spec.md). Implementation:
[#803](https://github.com/crtahlin/wasp/pull/803), merged as `82c67848`.

## Summary

- **Verified on a real node:** with the fix, a batch purchase whose approve
  and `createBatch` sends were both answered with HTTP 429 completed with
  HTTP 201 in 26.4 s. Each transaction was sent once and mined once, and
  exactly one new batch appeared. Before the fix, the same kind of failed
  send gave HTTP 500, and the transaction was mined anyway.
- **Not covered by this run:** the 10-minute bound, the warning line
  "transaction may still be mined", a send that the node re-broadcasts
  before its receipt arrives, the failover's dial of an endpoint with no
  connection, and the staking transactions. Unit tests and mutation checks
  in #803 cover these. No real node has run them.

## Set-up

- **Node:** the bench-1 Sepolia full node (staked, storage incentives on).
  It ran the upstream v2.8.3 sync branch (#798) merged locally with `main`
  at `82c67848`, build `0.1.5-810c4270e`, linux/amd64,
  `GOEXPERIMENT=nogreenteagc`. That merge was built only for this test and
  was never pushed. Before the merge, the packages it touches passed their
  tests with `-race`.
- **Why a proxy:** the public endpoints that produced #802 had stopped
  rate-limiting sends by the day of the test. No 429 had appeared in the
  node's log for more than 9 hours. A plain purchase would therefore not
  have reached the fixed path.
- **The proxy:** a small local JSON-RPC proxy, listening on the loopback
  interface only, forwarded every request to one public Sepolia endpoint.
  For the first send of each of the first two distinct raw transactions, it
  forwarded the transaction and then answered the node with HTTP 429 in
  place of the real reply. Raw transactions were told apart by a hash of
  their bytes, so a re-broadcast of the same bytes would have passed
  through unchanged. This is the #802 situation: the node sees a failed
  send while the transaction is already in the pool.
- **The proxy was the node's only endpoint during the test.** With other
  endpoints after it in the list, the failover sends the same transaction
  through the next endpoint after a 429 (#772). That send succeeds or
  answers "already known", so `Send` returns no error and the fixed code
  never runs. A run like that would pass on the old build as well. With a
  single endpoint, the node uses no failover wrapper
  (`pkg/node/chain.go`), so the 429 reaches the transaction service as
  the send's result.
- **Purchase:** `POST /stamps/1000000000/17`, a mutable batch.
- **Restarts:** every change of binary or configuration was made while no
  reveal was pending, the node was not selected for the current round, and
  no transaction was pending. The node was ready within a minute each time,
  and its restart count stayed at 0. The proxy was the only endpoint for
  about 15 minutes. Afterwards the normal endpoint list was put back.

## Before the fix (baseline)

This purchase happened before the test, on 2026-10-17, during the
investigation of #802. The node ran the sync branch build
`0.1.5-7b689bd90`, which does not have the fix.

**Table: batch purchase before the fix, bench-1 Sepolia node, build 0.1.5-7b689bd90, real HTTP 429 from the public endpoints**

| Step | Time from the `createBatch` send | Observed in the node's log |
|---|---|---|
| Approve | before the send | Confirmed; its send returned no error |
| `createBatch` send | 0 s | HTTP 429 from both endpoints that were tried |
| API answer | +0.05 s | Error "create batch: create failed", HTTP 500 |
| Re-broadcast of the stored `createBatch` (#772) | +30.2 s | Accepted by an endpoint |
| `createBatch` receipt | +48.2 s | Mined; the node later recovered the batch from the chain |
| Total | 1 purchase | API 500, but the batch was created: the negative result the spec describes |

The two earlier failed purchases on the same day (#802) were the approve
variant of the same failure: the approve send got 429, the API answered 500,
and the approve was mined about 30 to 60 s later, with no batch.

## With the fix

**Table: batch purchase with the fix, bench-1 Sepolia node, build 0.1.5-810c4270e, proxy as the only endpoint, injected HTTP 429 on the first send of the approve and of `createBatch`**

| Step | Time from the request | Observed |
|---|---|---|
| Approve send | +0.9 s | Forwarded to the public endpoint (accepted there); the node got HTTP 429 (injection 1) |
| Node waits for the approve's receipt | +1.0 s to +13.4 s | Receipt polls through the proxy; no new send |
| Approve receipt | +13.4 s | Mined |
| `createBatch` send | +13.9 s | Forwarded (accepted); the node got HTTP 429 (injection 2) |
| Node waits for the `createBatch` receipt | +14.0 s to +26.4 s | Receipt polls; no new send |
| `createBatch` receipt | +26.4 s | Mined |
| API answer | +26.4 s | HTTP 201 with a batch ID |
| Total | 2 sends, 2 injected 429s, 2 transactions mined, 1 new batch | **Pass** |

**Against the pass rule** (fixed before the run):

- **HTTP 201 with a batch ID after the injected 429s:** yes.
- **Approve and `createBatch` each mined once:** yes. The proxy saw exactly
  one `eth_sendRawTransaction` for each, and the node's log has one
  "pending transaction confirmed" line for each.
- **Exactly one new batch:** yes. `/stamps` listed one batch before the
  purchase and two after it, and the new one was usable within 10 minutes.
- **Fail conditions** (a 500 followed by a mined transaction of the same
  purchase, or two new batches): neither happened.

**What the run shows, and what it does not:**

- **Measured:** the node got HTTP 429 for both sends, polled for each
  transaction's receipt, and went on to the next step when the receipt
  arrived. The old build stops the operation on the send error. That was
  observed three times on 2026-10-17, and it is how the code before #803
  reads.
- **Not exercised:**
  - The receipts arrived after about 13 s each, before the 30-second
    re-broadcast. So the re-broadcast path did not take part.
  - The 10-minute bound and its warning line were not reached.
  - The failover change (dialling an endpoint with no connection for a
    send) was not exercised, because only one endpoint was configured.
  - Staking transactions and the claim path were not exercised.
- **The node's log had no warning about the 429s.** With one endpoint
  there is no failover wrapper to log the transport failure. The
  transaction service logs a send error only when it re-broadcasts, and
  the receipt in this run came before that. The proxy's log is the record
  of what the node was told.

## Status

`validated` for the purchase path under an ambiguous send of both the
approve and `createBatch`. The items listed under "Not exercised" are
covered only by the unit tests and mutation checks in #803.
