# Retrying a preferred peer that was refused credit

Issue: [#324](https://github.com/crtahlin/wasp/issues/324). This is the fix for
the cause behind [#313](https://github.com/crtahlin/wasp/issues/313), where a
download of content only a provider holds returns a truncated body and stops.

Code references are to `main` at `6eaaa651`, base `upstream/v2.8.2`.

## Problem

**A transient credit refusal permanently removes the only holder of a chunk.**
The preferred candidate is consumed before the credit check:

```go
// pkg/retrieval/retrieval.go:261-267
if len(candidates) > 0 {
    peer := candidates[0]
    candidates = candidates[1:]          // consumed here
    if !s.retrievePreferred(ctx, spanCtx, quit, chunkAddr, peer, skip, resultC) {
        retry()
        continue
    }
```

`retrievePreferred` returns false when `prepareCredit` fails, after holding the
peer out of selection for 600 ms (`pkg/retrieval/preferred.go:224-228`).
`candidates` is built once per chunk (`retrieval.go:203`) and nothing appends to
it, so the peer never gets a second chance for that chunk and the
`wasp-local-only` header is never sent for it. The chunk falls to ordinary peer
selection, and where the provider is the only holder, it burns its 32 origin
retries (`retrieval.go:150`) and returns `storage.ErrNotFound` (`:374`),
indistinguishable from a chunk that does not exist.

The 600 ms recovery branch at `retrieval.go:281-296` cannot help: it needs
`closestPeer` to exhaust every connected peer first, and with 120 peers against
32 retries the budget runs out long before.

**Observed on the bench**, against content whose batch expired so the provider P
is the only holder. **One run each, exploratory, outside the fixed method**,
with `Swarm-Cache: false` and `Swarm-Lookahead-Buffer-Size: 0`. Under rule 7 a
single run is a diagnosis, not a measurement, and none of these figures is a
result:

| Request | Result | curl exit |
|---|---|---|
| `Range: bytes=0-65535` | 206, complete 65,536 bytes, SHA matches P | 0 |
| `Range: bytes=0-131071` | 206, complete 131,072 bytes, SHA matches P | 0 |
| `Range: bytes=0-262143` | 206, truncated at 131,072 | 18 |
| full 4,194,304-byte download | 200, 196,608 bytes | 18 |
| full download, repeat | 200, 131,072 bytes | 18 |

**What these do and do not show.** They show that the content is intact and
reachable: two ranges returned byte-correct data verified by SHA-256 against P's
own copy, so nothing is lost and the path to P works. They show that a larger
request truncates where a smaller one completes. They do **not** establish where
the cut-off falls or why that particular byte count.

An earlier draft of this spec claimed the two full-download sizes were the only
two a credit ceiling admits. **That claim was wrong and is withdrawn.** Three
things break it. The 32,768-byte granularity is simply `io.Copy`'s default
buffer, which `pkg/api/bzz.go:48` says in a comment, so it is not evidence of
anything about credit. `PrepareCredit` calls `settle` and recomputes the debt
before it tests the overdraft limit (`accounting.go:300-311`, `:320`), so there
is no fixed per-download chunk ceiling to hit. And the same run's
`preferred_attempts` of 65, at about 310,000 a chunk, exceeds the 18,000,000
maximum that limit allows, so the byte count and the counter cannot both follow
from it.

What the counters do show, on the run returning 192 KiB: `preferred_attempts`
0 to 65, `preferred_hits` 0 to 63, `accounting_blocks_count` 0 to 1, and
`settle_error_count` unchanged at 0. `accounting_blocks_count` is node-wide
across all peers, so attributing that single increment to P is an inference, not
an observation. The measurement section below exists to settle what this
paragraph cannot.

**Reconciling with the merged #290 result**, which recorded "not one of 24 runs
retrieved the file" and "no body was transferred at all". Those runs used the
shipped 256 KiB lookahead buffer; these set it to 0, which reduces the read unit
to `io.Copy`'s 32 KiB. A smaller unit means a failure voids less, which is the
likely reason a body appears here and did not there. Nothing here tests that,
so take it as consistent with the header rather than demonstrated.

## Hypothesis

Re-admitting a preferred peer after its 600 ms overdraft window lets a download
proceed at the free refreshment rate instead of stopping. Refreshment is
`4,500,000` units a second against about `310,000` a chunk (`pricer.go:35`,
`E[PO] = 1`), so roughly 14.5 chunks a second, and the API sets no write timeout
(`pkg/node/node.go:694-698`).

**Predicted:** a single request for the 4 MiB file completes in about 70 to 90
seconds with a matching SHA-256, where it currently truncates.

**This does not make downloads faster.** It converts a failure into a slow
success. Any run that is not sole-source will be unaffected, because ordinary
peers already serve those chunks.

## Design

Do not consume the candidate until the peer has actually been asked.

```go
peer := candidates[0]
if !s.retrievePreferred(...) {
    retry()
    continue          // candidate stays, re-admitted after the skip expires
}
candidates = candidates[1:]
```

Three things this must settle, and a naive version of the above gets all three
wrong.

**1. Distinguish a refusal that will pass later from one that will not.**
The reason is available and then thrown away. `prepareCredit`
(`retrieval.go:458-468`) returns the accounting error unmodified, and
`accounting.ErrOverdraft` is an exported sentinel (`accounting.go:193`, returned
at `:322`), so `errors.Is` works at that point today. What loses it is
`retrievePreferred`, which returns a bare `bool` and discards `err` entirely
(`preferred.go:223-227`). So the change belongs there: return the reason, and
keep the candidate only for an overdraft.

One real difficulty this creates. The other refusal worth excluding, "connection
not initialized yet" (`accounting.go:279`), is an anonymous `errors.New` rather
than a sentinel, so it cannot be tested with `errors.Is`. Excluding it needs a
new exported error in `pkg/accounting`, which the implementation must add rather
than pattern-match on the string.

**2. Do not spin on the 600 ms skip.** The peer is held in the request-local
skip list for `overDraftRefresh` (`preferred.go:226`). Re-admitting it must wait
for that entry to expire rather than retrying immediately, or the loop becomes a
busy wait. The existing `time.After(overDraftRefresh)` branch at
`retrieval.go:292-295` is the model, but it is currently reached only after the
topology is exhausted; the preferred path needs its own wait that does not
depend on that.

**3. Bound it.** An unbounded retry against one peer is a livelock if that peer
stays overdrafted, which is exactly what happens when the requester has no
settlement path. The bound must be explicit and stated, not left to the request
context. A count of re-admissions per chunk is the simplest, with the current
behaviour, zero, as the floor.

**Observability, worth fixing in the same change.** An overdraft refusal of a
preferred peer increments no **retrieval** metric: `preferred.go:225-228`
returns before `PreferredAttempts.Inc()` at `:232`. The only signal is
`accounting_blocks_count` (`accounting.go:321`), which is node-wide across every
peer and so cannot tell an operator that a *provider* was refused. Add a counter
for preferred attempts refused for credit, so the two can be compared.

## What this risks

- **A slow download where there used to be a fast failure.** A sole-source
  download becomes a 70 to 90 second request instead of the 2.5 second
  truncation recorded in [results.md](results.md). That is the intent, but a
  caller with its own timeout will see a change in behaviour, and the operator
  documentation must say so.
- **Livelock if the bound in (3) is wrong**, against a peer that never regains
  credit.
- **More load on a provider**, which is the same cost the per-peer credit work
  in [#322](https://github.com/crtahlin/wasp/pull/322) discusses. This change
  makes a requester ask a provider more persistently for the same content.

## Protocol impact

**None.** No wire format, protocol ID, handshake or message changes. This alters
only how many times a requester asks a peer it is already entitled to ask, using
the existing retrieval protocol and the existing local-only header.
`make protocol-freeze` must pass with the fingerprint unchanged.

## Measurement

On the bench, following [test-bench.md](../../agent-playbooks/test-bench.md),
with the expired content B as the sole-source case.

- **Primary:** does one ordinary request return the complete 4,194,304 bytes
  with a matching SHA-256, and how long does it take. Predicted 70 to 90 s.
- **Control, same session:** the stock build on the same content, predicted to
  return a truncated body with curl exit 18. No size is predicted: the two
  figures observed so far rest on the model withdrawn above, so the measurement
  reports the size rather than checking it against a number.
- **Non-regression:** content A, which the network also holds, hinted and
  unhinted, three runs each. Predicted no change beyond the spread, since
  ordinary peers already serve those chunks.
- **Counters per run:** `accounting_blocks_count`, the new refused-for-credit
  counter, `preferred_attempts`, `_hits`, `_misses`, `settle_error_count`, and
  the curl exit code, where 18 means truncated and 0 means complete.
- At least three runs per condition reported with the spread (rule 7).

**A negative result** is the download still stopping, which would mean the
candidate consumption is not the binding constraint and the retry budget or
something else is. That is a useful answer and closes #324.

## Acceptance

**Accept** if a sole-source download that currently truncates instead completes
with a matching SHA-256, **and** content A's download time does not rise beyond
the spread. The second clause matters because this change makes a requester more
persistent with one peer, which could slow the ordinary case.

**Reject** if the download still truncates, or if content A regresses.

### Amendment after measurement

Results: [overdraft-retry-results.md](overdraft-retry-results.md).

**The rule above was written on a single-cause model and the measurement refuted
it.** It says "a sole-source download" as though there were one, and there are
two, which differ by whether the lookahead prefetch is on. The whole result
turns on that distinction, so the rule is restated rather than reinterpreted:

- **With the prefetch off**, the rule is met. (This originally read "one chunk
  in flight at a time", which is wrong. At 0 the read unit is `http.ServeContent`'s
  32 KiB `io.Copy` buffer, which is **8 chunks**, against 64 at the shipped
  buffer, and `joiner.ReadAt` uses an unlimited errgroup so a read unit fans out
  however large it is. On a separate bench session, so not comparable run for run
  with the tables here, the peak reserved balance against the provider was
  2,530,000 to 2,550,000 with the prefetch off and 12,780,000 to 12,880,000 with
  it on. See [overdraft-terms.md](overdraft-terms.md).)
  Grouped by whether credit was refused at all, stock completed 0 of 2 refused
  runs and the fix completed 1 of 1. That is short of rule 7's three per
  condition and is a pointer, not a result.
- **At the shipped lookahead buffer** the download still truncates, so the rule
  is not met. An earlier version of this amendment said the fix delivers five
  times as many bytes there. **That comparison was not controlled and is
  withdrawn**; the results document carries the withdrawal and the matched
  replacement. Matched, three restart cycles per build from a zero balance, the
  fix delivers one read unit in one cycle of three where stock delivers nothing
  in three of three, and always asks the provider more often than stock's
  invariant 59 attempts.
- **Content A does not regress**, which was the second clause. The medians are
  4.30 s and 2,325,064 B/s for the fix against 5.22 s and 1,917,504 B/s for
  stock, and the fix is inside stock's range at its fast end.

**What the amendment does not do is call the first clause satisfied.** The
residual failure has a different cause from the one this spec addresses, and
attributing it here is the point of the amendment. This change removes a
transient refusal turning into a permanent one. It does not remove the refusal,
and with the prefetch on there are hundreds of refusals per download, each of
which now falls through to peers that do not hold the chunk. Falling through is
what keeps content A fast, so it is not a mistake to be tuned away; the two
cases pull in opposite directions and the requester cannot tell which it is in.

The refusal itself is [#327](https://github.com/crtahlin/wasp/issues/327), which
carries the pre-registered prediction that this arm completes once the provider
grants the requester a large enough credit window, with the overdraft counter
falling towards zero as the evidence that the threshold was the constraint.

**So this is accepted as a necessary part with its own measured effect, not as
the fix for #313.** #313 stays open until the sole-source download completes at
the shipped buffer.

## Test plan

Unit tests in `package retrieval_test`:

- a preferred peer refused for overdraft is asked again for the same chunk after
  the skip expires, and the local-only header is sent on the retry;
- a preferred peer refused because it is not connected is **not** retried;
- the re-admission bound is enforced, and a permanently overdrafted peer does
  not prevent the request from terminating;
- a chunk that genuinely does not exist still fails, and in bounded time;
- the refused-for-credit counter moves exactly once per refusal.

Plus the mixed-version check against a stock v2.8.2 node, which sees only more
requests and must blocklist nobody.

## Configuration

No new setting. The re-admission bound from Design (3) is a compiled-in constant
with the current behaviour as its conceptual default. Per rule 8 it becomes a
config option only if the measurement shows a value that matters, with its own
issue.

## Upstream portability

**Nothing to port, and no `affects-upstream` marker.**
`pkg/retrieval/preferred.go` does not exist upstream, and the `candidates` logic
in `pkg/retrieval/retrieval.go` is fork-added:
`git show upstream/v2.8.2:pkg/retrieval/retrieval.go | grep -c candidates`
returns 0. This is a defect in the content providers work
([#290](https://github.com/crtahlin/wasp/issues/290)), not in Bee, so rule 11
does not apply.

## Rollout and rollback

- Nothing to turn on: it takes effect for downloads that already carry a
  provider hint on a node running with `providers-enable`.
- Rollback is a revert of the single commit.
- No migration, no on-disk format change, no stored state.

---

Generated with help of AI.
