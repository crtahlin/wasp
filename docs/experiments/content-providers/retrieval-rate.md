# Faster provider downloads: what is measured, and what is not yet known

Issue: [#343](https://github.com/crtahlin/wasp/issues/343). The companion issue
on spreading a download across several providers is
[#344](https://github.com/crtahlin/wasp/issues/344).

Code references are to `main` at `d43f5389`, base `upstream/v2.8.2`.

**This document proposes no code change.** An earlier draft did, and a review
found that its central mechanism could not work and that its own data
contradicted the cause it assumed. Both are recorded below rather than removed,
because the second one is the reason this is now a plan to isolate a cause
rather than a plan to fix one.

## Terms

- **In-flight slot**: one chunk request outstanding at a time. Throughput is
  the number of these times the chunk size divided by the round trip.
- **Read unit**: the span `joiner.ReadAt` is asked for in one call. Its leaf
  fetches run together, and it is all or nothing: one leaf that fails fails the
  whole unit and `ReadAt` returns no data at all
  (`pkg/file/joiner/joiner.go:215-223`).
- **Overdraft**: a peer refusing a request because the requester has reached
  the credit that peer extends it. Distinct from a peer not holding the chunk.
- **Readmit**: keeping an overdrafted preferred peer for a later attempt at the
  same chunk instead of dropping it, added by
  [#324](https://github.com/crtahlin/wasp/issues/324).
- **Sole-source content**: content no other node holds. Local ingest
  ([#326](https://github.com/crtahlin/wasp/issues/326)) produces it by
  construction.

## What is measured

Sole-source content, 4,194,304 bytes at redundancy level NONE, 1,033 chunks.
Three runs per buffer. Round trip between the two nodes is 30.14 ms, mdev
0.016 ms, measured by `ping` in the same session; the provider reads the same
file from its own disk in 0.026 s, measured separately. Neither figure is in the
run's own data file, which is a gap in the harness rather than a claim about the
run.

| Buffer | Completed | Rate when complete | Overdrafts per run |
|---|---|---|---|
| 0 | **3 of 3** | 263,352 / 263,424 / 263,464 B/s | 0, 0, 0 |
| 262,144 | 1 of 3 | 1,086,300 B/s (n=1) | 609, 240, 438 |
| 524,288 | 0 of 3 | | 840, 387, 471 |
| 2,097,152 | 0 of 3, zero bytes every time | | 2133, 1912, 1901 |

Three things are solid:

- **A larger buffer can be much faster.** One run at 262,144 delivered the whole
  file in 3.86 s, SHA-256 verified, against a baseline of about 15.92 s. That is
  **n=1** and rule 7 says once is not measured, so it is a demonstration that
  the rate is achievable, not an estimate of it.
- **A larger buffer truncates.** Nine of twelve runs above buffer 0 returned a
  short body, and at 2,097,152 every run returned nothing.
- **Truncation is read-unit aligned.** The short bodies are exact multiples of
  the buffer: 3,145,728 and 2,621,440 are 12 and 10 units of 262,144; 524,288
  and 1,048,576 are 1 and 2 units of 524,288. That is direct support for the all
  or nothing property of `ReadAt` above, and it is the strongest evidence in the
  set.

## What is not established

### The cause of the truncation

An earlier draft of this document said the downloads truncate because credit
runs short, and proposed waiting for credit instead of giving up. **Its own data
does not support that.** Within the 262,144 arm:

| Credit refusals in the run | Bytes delivered |
|---|---|
| 609 | 3,145,728 |
| **240** | **2,621,440**, the earliest truncation |
| 438 | **4,194,304**, the only complete run |

The run with the **fewest** refusals did worst and the middle one completed, so
refusal count does not order the outcomes. In that completing run every refusal
was readmitted, 438 of 438, which says the existing #324 path already recovers
from hundreds of refusals inside one download without any wait at all.

So credit pressure is present and is **not** shown to be what ends these
downloads. Something fails a read unit; what it is has not been isolated.

### Why the baseline is four times slower than the same model predicts

At buffer 0 a read unit is 8 leaves, which the in-flight model puts at about
1.09 MB/s. The measured baseline is 263,424 B/s, near two slots rather than
eight, **with zero overdrafts in all three runs**. Whatever holds buffer 0 to a
quarter of its own predicted rate is not credit, is not named here, and would
not be addressed by anything the earlier draft proposed. It may be the larger
lever of the two.

Note also that `langos.NewBufferedLangos` (`pkg/api/bzz.go:825`) fetches the
next buffer while the current one is being read, so read units are not strictly
sequential and in-flight leaves may be up to twice the per-unit figure. An
earlier draft of this document stated the opposite.

### Whether the existing credit wait ever fires

`retrieval.go:322-337` waits `overDraftRefresh`, 600 ms, and retries, but only
when `closestPeer` reports no peer left. An earlier draft asserted this is
unreachable because the error budget, `maxOriginErrors = 32`, runs out first.
**That assertion was not verified and is wrong in general**: `errorsLeft` is
decremented in one place only (`retrieval.go:408`), on a result carrying an
error, while an ordinary peer refused credit (`:361`) is skipped and retried
without spending any budget. Peers can therefore leave the selection pool for
free, and the branch is reachable whenever enough of them are refused.

It may well have been firing during these runs. `accounting_blocks_count` rose
by more than `preferred_overdrafts` in every arm, and the difference is refusals
by ordinary peers: 72 at buffer 524,288 and 309 at 2,097,152 in run 1 alone.
Nothing in the data says whether the branch fired, because nothing counted it.

### What the size sweep added, after this document was written

[sole-source-sizes.md](sole-source-sizes.md) measured the same provider at four
file sizes with the balance recorded, and it changes what should be instrumented
here.

**A candidate that is not credit exhaustion: settlement falling short of
accrual.** Three 20 MB downloads and one 50 MB download ran at the same delivery
rate to within 0.7%, so all four accrued debt at the same rate. Their net
balance movement was -360,000, -660,000, **plus 5,160,000** and
**-81,460,000**. Debt fell during one of them. Whatever separates a download that
finishes from one that does not, it tracks the **difference** between accrual
and settlement rather than either alone, and **no harness in this project has
ever recorded the settlement rate**.

**An existing dataset already refutes a pure headroom story.** `cp290/t7-cold.txt`
has runs starting at a balance of **zero**, the maximum possible headroom, that
delivered 262,144 bytes and then nothing at all, five times. Any explanation
resting on running out of credit has to account for those rows, and the
project's existing account of them is the all or nothing behaviour of
`joiner.ReadAt` over one read unit.

**Two corrections to how credit is read**, which apply to any measurement here:

- The gate is `paymentThreshold + refreshDue` (`accounting.go:325-335`), which
  the API reports as `CurrentThresholdReceived`, not `ThresholdReceived`.
- It compares against a debt figure that includes the reserved balance, not the
  settled balance that `/balances` returns.
- The announced threshold **grows** by one refresh rate per settlement
  checkpoint, so it must be read before and after a run rather than once.

So step 1 below gains two observables: the settlement rate per run, split into
pseudosettle and cheques, and the announced threshold at both ends of each run.

## What to do next, in order

### 1. Isolate why a read unit fails

Nothing further should be specified until this is answered, and it is cheap to
answer. The observable is already there: `retrieval.go` logs
`sleeping to refresh overdraft balance` on the wait branch, and
`joiner.ReadAt` returns the error that killed the unit.

- Run the 262,144 arm with the node's debug logging on and record, per failed
  read unit, the error `ReadAt` returned and what the retrieval loop did with
  the chunk that failed: exhausted its error budget, ran out of candidates,
  or something else.
- Count how often the existing wait branch fires. If it fires often, the earlier
  draft's premise was inverted and any design that adds more waiting is starting
  from the wrong place.
- Record the requester's balance with the provider at the start of every run,
  the announced threshold at both ends, and the settlement rate split into
  pseudosettle and cheques. The balance rule came from a withdrawal in
  [overdraft-results](overdraft-retry-results.md); the other two come from
  the size sweep above.

### 2. Explain the buffer-0 gap

Separately and with the same instrumentation: at buffer 0 there is no credit
pressure at all, so whatever limits it to about two slots is a clean target with
no confound. Candidates worth separating: the per-unit intermediate chunk being
refetched, `singleflight` collapsing concurrent callers (`retrieval.go:203`),
and the service-wide one-minute `errSkip` list (`:122`).

### 3. Only then consider a change

Any candidate has to answer what the earlier draft did not:

- **`maxOverdraftReadmits = 8`** (`retrieval.go:159`) drops a preferred peer
  from a chunk's candidate list after eight refusals of that chunk. A design
  that waits and retries the provider has to say how the provider gets back into
  the candidate list, and the earlier draft did not mention this constant.
- **`skip.PruneExpiresAfter` cannot be used as a test.** It deletes the entries
  it counts (`pkg/skippeers/skippeers.go:100-122`), so it cannot be asked the
  same question twice; the per-request list is created with no pruning interval
  (`retrieval.go:204`) so entries outlive their expiry; and it is not
  overdraft-specific, since any `prepareCredit` error adds one (`:361`).
- **Waiting has been measured once in this project and it cost about 2x.** In
  [overdraft-retry-results.md](overdraft-retry-results.md) the one run where a
  refused peer was waited back finished at 140,849 B/s against a 264,000 B/s
  unrefused run.
- **The refresh ceiling is structural.** `accounting.go` caps the refresh term
  at `min(elapsed, 1)` times the refresh rate and refuses a refreshment inside
  999 ms, so waiting longer than a second buys no more credit. Any design that
  waits must state the sustained rate that ceiling permits.
- **Forwarding must be excluded.** A forwarded request gets `errorsLeft = 1` and
  no preferred candidates (`retrieval.go:237-243`), and the handler holds the
  requesting peer's stream open while it runs. Added waiting there would spend
  another node's connection, which rule 8 requires be stated as a cost to them.

## Protocol impact

**None so far, because nothing is proposed.** Any change considered here is
client-side scheduling: no wire format, protocol identifier, handshake or
message change, and `make protocol-freeze` unaffected. The forwarding note above
is about how long this node holds a stream open, not about what it sends.

## Upstream portability

Everything examined here is **unmodified upstream code**: the `errorsLeft` loop,
`maxOriginErrors`, `maxOverdraftReadmits`, the wait branch, `skippeers`, and the
unlimited errgroup in `joiner.ReadAt` are all as they are in `upstream/v2.8.2`.

No `affects-upstream` marker is claimed, and under rule 11 that is the correct
outcome for now: the truncation is reproduced but its cause is not isolated, and
a set of findings is worth only as much as its weakest member. If step 1 shows a
defect rather than a tuning question, the marker becomes appropriate and the
issue should say what was checked against the upstream tree.

## Configuration

**None proposed.** The lookahead buffer is already settable per request through
`Swarm-Lookahead-Buffer-Size`, and making its default a node setting is exactly
what rule 8 forbids until the measurement justifies it: at present the larger
value truncates, so shipping it as a dial would hand operators a way to break
their own downloads.

## Measurement plan for step 1

- The 262,144 arm, three runs, with debug logging on, recording per failed read
  unit the returned error and the fate of the chunk that failed.
- Buffer 0 in the same session as a zero-credit-pressure control.
- **Randomised buffer order.** The existing data runs the buffers in a fixed
  order, so order is confounded with condition: every 262,144 run followed a
  buffer-0 run, and the only completing one was in the last cycle.
- Per run: balance with the provider at start, bytes against bytes wanted,
  SHA-256, curl exit, `preferred_attempts`, `preferred_hits`,
  `preferred_overdrafts`, `preferred_readmits`, `accounting_blocks_count`, and
  a count of the existing wait branch firing.
- Node state matched across any comparison, in one session, with the reason
  recorded: two results in this project have been withdrawn for getting that
  wrong.

No acceptance criterion is stated, because step 1 is a diagnosis and not a
change. A criterion will belong to whatever it turns out to justify.

---

Generated with help of AI.
