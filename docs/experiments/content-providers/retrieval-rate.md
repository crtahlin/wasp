# Faster provider downloads: stop truncating when credit runs short

Issue: [#343](https://github.com/crtahlin/wasp/issues/343). The companion issue
on spreading a download across several providers is
[#344](https://github.com/crtahlin/wasp/issues/344) and is deliberately not
specified here; see Scope.

Code references are to `main` at `d43f5389`, base `upstream/v2.8.2`.

## Terms

- **In-flight slot**: one chunk request outstanding at a time. Throughput is
  the number of these divided by the round trip, times the chunk size.
- **Read unit**: the span `joiner.ReadAt` is asked for in one call. Its leaf
  fetches run together; the calls themselves run one after another.
- **Sole-source content**: content no other node holds, so a requester can get
  it only from the named provider. Local ingest
  ([#326](https://github.com/crtahlin/wasp/issues/326)) produces it by
  construction.

## Problem

**Provider downloads run at a quarter of the rate the link allows, and the
faster setting truncates them.**

Measured 2026-09-18 on sole-source content, three runs per buffer, interleaved
in one session. Round trip 30.14 ms, mdev 0.016 ms, so **one in-flight slot is
worth about 135,900 B/s**. The provider reads the same file from its own disk in
0.026 s, so disk is not the constraint.

| Buffer | Completed | Rate when complete | Overdrafts per run |
|---|---|---|---|
| 0 | **3 of 3** | 263,352 to 263,464 B/s | 0, 0, 0 |
| 262,144 | 1 of 3 | **1,086,300 B/s** | 609, 240, 438 |
| 524,288 | 0 of 3 | | 840, 387, 471 |
| 2,097,152 | 0 of 3, zero bytes every time | | 2133, 1912, 1901 |

Two things follow, and they are the whole of the problem:

- **The headroom is real.** The single run that completed at the node's own
  chosen buffer moved 4,194,304 bytes in 3.86 s, SHA-256 verified, **4.1 times
  the baseline**. The rate published for #326 was measured with the prefetch
  off and is a floor.
- **Concurrency buys credit refusals at the same rate it buys speed.** Zero
  overdrafts at buffer 0; hundreds at 256 KiB; thousands at 2 MiB, where the
  download returns nothing at all.

### Why running short of credit ends the download

`joiner.ReadAt` fans its leaf fetches out over an `errgroup` with no limit and
then waits on all of them (`pkg/file/joiner/joiner.go:215-223`). **One chunk
that gives up fails the whole read unit**, and the download stops there. A read
unit is 8 leaves at buffer 0 and 64 at 256 KiB, so a bigger fan-out is more
likely to contain at least one failure.

A chunk gives up when `errorsLeft` reaches zero. It starts at `maxOriginErrors`,
which is **32** (`retrieval.go:155`, `:242`).

**There is already a branch that waits for credit instead of giving up, and it
cannot be reached.** `retrieval.go:322-337` waits `overDraftRefresh`, 600 ms,
and retries, but only inside `if errors.Is(err, topology.ErrNotFound)`, which
means `closestPeer` has no peer left that is not skipped. With 120 connected
peers and an error budget of 32, the budget is gone long before the peer list
is. So for sole-source content the sequence is:

1. the provider refuses on credit, and is kept for a later attempt (#324);
2. ordinary selection is tried at once, which is right when other peers hold
   the chunk and useless when none do;
3. 32 peers that do not have the chunk answer in turn;
4. `errorsLeft` hits zero and the chunk fails, **while the one peer that holds
   it was 600 ms away from being able to serve it**;
5. the read unit fails, and with it the download.

The requester is not short of credit for long. It is short of patience, in
exactly the case where patience is the only thing that would work.

## Hypothesis

A chunk that has no peer left except one which is merely out of credit should
wait for that credit rather than fail.

**Predicted:** with that change, the node's own chosen buffer completes
sole-source downloads as reliably as buffer 0 does, at several times the rate,
and the rate on content the network also holds does not regress.

## Design

### 1. Give up only when nobody can serve the chunk, not when patience runs out

The condition for failing a chunk becomes: `errorsLeft` is exhausted **and** no
peer is pending a credit refresh for this chunk. When one is, wait
`overDraftRefresh` once and retry it, exactly as the unreachable branch already
does.

The information needed is already tracked. `skip.PruneExpiresAfter(chunkAddr,
overDraftRefresh)` returns how many peers are skipped only because they
overdrew, and the existing branch already uses it as its test. This change moves
that same test to the place the loop actually reaches.

**This does not reintroduce what #324 removed.** That fix stopped the requester
waiting for an overdrafted peer *instead of* trying ordinary selection, which
cost about 3x on content the network also holds because the wait was paid on
every chunk. Here the wait happens only *after* ordinary selection has been
tried and has failed, so on widely-held content it is never reached. The two are
compatible, and the ordering is the entire point:

| Case | Today | With this change |
|---|---|---|
| Other peers hold the chunk | tried at once, no wait | unchanged |
| Only the provider holds it | 32 peers tried, then fail | 32 peers tried, then wait and retry the provider |

### 2. Bound the waiting

An unbounded wait turns a failed download into one that never returns. Each
chunk gets at most `maxOverdraftWaits` waits of `overDraftRefresh`, a compiled
constant to start under rule 8. The request's own context still applies above
it, so a caller that has given up is not kept waiting.

**What the operator sees when the bound is hit** is what they see today: a short
body. The bound turns an immediate truncation into a slower one, and the metric
below says which happened.

### 3. What must not change

- **The wire.** No protocol identifier, message or handshake changes. This is
  entirely in when the requester chooses to ask again.
- **Ordinary downloads.** Content the network holds must not get slower. The
  measurement below treats that as a reject condition rather than a footnote,
  because the first attempt at #324 regressed it by 3x and that is how the
  regression was found.
- **The credit itself.** Nothing here grants, borrows or announces more credit.
  Raising what a provider grants is [#327](https://github.com/crtahlin/wasp/issues/327),
  and the two are independent: this change makes a given amount of credit usable
  at higher concurrency, #327 changes the amount.

### 4. A metric, because the failure it replaces is silent

A counter of chunks that waited for credit and then succeeded, and a counter of
chunks that exhausted `maxOverdraftWaits`. Without the second, a download that
truncates after waiting looks exactly like one that truncated at once, and this
document's own acceptance could not be checked in the field.

## Scope

**Not in this spec**: spreading one download across several providers,
[#344](https://github.com/crtahlin/wasp/issues/344). It is a genuine second
lever, and the honest ordering is this one first. If a credit refusal stops
ending the download, the value of using several providers changes shape, from
rescuing a failing download to buying throughput, and the two want measuring
separately rather than at once.

**Not in this spec**: making the lookahead buffer a setting. Under rule 8 a dial
that truncates downloads is worse than no dial. It becomes a candidate once this
change makes the larger buffer safe, and it needs its own issue and its own
measurement.

## Protocol impact

**None.** No wire format, protocol identifier, handshake or message changes.
`make protocol-freeze` must pass with the fingerprint unchanged.

## Measurement

The bench now has sole-source content on demand through local ingest, so none of
this waits on a postage batch expiring.

- **The arm this is for.** Sole-source content at the node's own chosen buffer,
  before and after, three runs each, same session, interleaved. Accept on
  completion rate and rate together: completing slowly is the point, and a
  faster run that truncates is not a pass.
- **The regression arm, equally weighted.** Content the network also holds,
  before and after, three runs each. A median more than 10% slower after the
  change is a reject, whatever the sole-source arm did.
- **Buffer sweep.** 0, 262,144, 524,288 and 2,097,152 as measured above, so the
  before and after tables are directly comparable.
- **Per run**: bytes returned against bytes wanted, SHA-256, curl exit code,
  `preferred_attempts`, `preferred_hits`, `preferred_overdrafts`,
  `preferred_readmits`, the two new counters, and
  `accounting_accounting_blocks_count`.
- **Node state matched across every comparison**, which on this bench means the
  same session and interleaved arms rather than one build after the other. Two
  results in this project have had to be withdrawn for getting that wrong, most
  recently the redundancy-level attribution in
  [local-ingest-results.md](local-ingest-results.md).

## Acceptance

**Accept** if sole-source downloads at the node's own chosen buffer complete in
three runs of three, at a median rate at least twice the buffer-0 baseline,
**and** content the network also holds is not more than 10% slower at the
median.

**Reject** if widely-held content regresses beyond that, since the whole
difficulty here is that the two cases want opposite behaviour; or if
sole-source downloads still truncate, which would mean the wait is not reaching
the case it was written for; or if a download that would previously have
truncated now fails to return at all.

## Test plan

In `package retrieval_test`:

- a chunk held only by a peer that is overdrafted at first and has credit after
  one refresh is retrieved, rather than failing;
- that chunk fails after `maxOverdraftWaits` waits when the peer never regains
  credit, rather than waiting for ever;
- a chunk held by an ordinary peer is retrieved without any wait, asserted on
  elapsed time, which is the #324 regression in test form;
- a cancelled context ends the wait at once;
- the two new counters move exactly when their conditions are met.

Each test to be mutation-checked: a test for a timing behaviour that passes with
the behaviour removed is the failure mode this project has already had twice.

## Rollout and rollback

Behaviour change with no setting, on by default, because the case it fixes is
one where the download fails today. Rollback is a revert. Nothing is written to
disk and nothing is announced, so there is no migration and no state to undo.

---

Generated with help of AI.
