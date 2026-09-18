# Keep the only peer that holds the chunk

Issue: [#343](https://github.com/crtahlin/wasp/issues/343). Diagnosis:
[truncation-cause.md](truncation-cause.md).

Code references are to `main` at `527f32b2`, base `upstream/v2.8.2`.

**Two designs have already been withdrawn on this issue.** The first said credit
exhaustion ends these downloads, and the data refuted it. The second proposed
waiting for credit where the loop gives up, and a review showed the provider is
no longer in the candidate list by then, so there would be nothing to retry.
This one is built on the measurement rather than on reasoning about the code,
and the thing that killed the second design is the thing it is designed around.

## Terms

- **Named provider**: a peer the requester was explicitly told holds the
  content, through the `Wasp-Providers` header. Distinct from a peer chosen by
  proximity.
- **Readmission**: keeping a named provider for a later attempt at the same
  chunk after it refused on credit, instead of dropping it, added by
  [#324](https://github.com/crtahlin/wasp/issues/324).
- **Skip**: a per-chunk exclusion with an expiry. A peer refused on credit is
  skipped for `overDraftRefresh`, 600 ms.
- **Sole-source content**: content no other node holds.

## Problem

**A chunk stops asking the only peer that can serve it, a fifth of the way into
its own life, and spends the rest asking peers that cannot.**

Measured, seven failing chunks across three runs:

| | Range | Median |
|---|---|---|
| Readmit window, the 8 readmissions | 0.141 to 0.832 s | **0.217 s** |
| Whole chunk, first attempt to not-found | 0.978 to 1.951 s | **1.06 s** |

The sequence (`retrieval.go:274-315`, and see
[truncation-cause.md](truncation-cause.md) for the evidence):

1. The named provider refuses on credit. It is skipped for 600 ms and kept,
   up to `maxOverdraftReadmits`, which is 8 (`:159`).
2. Each readmission falls through to ordinary selection in the same iteration,
   so the eight are consumed in about **0.22 s**.
3. On the ninth refusal the `default:` arm runs `candidates = candidates[1:]`
   (`:299`). **The provider is gone from this chunk for good.**
4. The chunk spends its remaining 0.84 s on ordinary peers. For sole-source
   content none of them can answer.
5. It exhausts `maxOriginErrors`, 32, raised by up to `maxMultiplexForwards`, 2,
   and returns `storage: not found`.
6. `joiner.ReadAt` is all or nothing, so one such chunk fails its read unit and
   the download truncates.

**The code already intends to do the right thing and cannot.** The comment at
`:284-286` says the loop "returns to this peer once its `overDraftRefresh` skip
has expired". It never does, because the candidate has been consumed at 0.22 s
and the skip does not expire until 600 ms.

**The chunk is not short of time.** It lives 1.06 s. A retry when the skip
expires at 600 ms would fall **0.46 s inside the life it already has**.

## Hypothesis

A named provider refused on credit should not be dropped from a chunk while the
reason it was refused is one that expires.

**Predicted:** sole-source downloads that truncate today complete, and content
the network also holds is not slower, because ordinary selection still runs
first and unchanged and will have succeeded long before 600 ms.

## Design

### 1. Do not consume the candidate for a reason that expires

The `default:` arm at `:297-302` drops the candidate for every error that is not
a readmitted overdraft. It should distinguish two cases:

- **The reason expires**, which today means an overdraft past the readmit bound.
  Keep the provider for the chunk and let its skip pace the retry.
- **The reason does not expire**, for example a peer that is not connected.
  Drop it, exactly as now.

Nothing about ordinary selection changes. A refused provider still falls through
to ordinary peers in the same iteration, which is what #324 established and what
keeps widely-held content fast.

### 2. The retry is paced by the existing skip, not by a new wait

**No new waiting is introduced anywhere.** A skipped peer is simply not selected
until its 600 ms expires; the loop continues doing ordinary work in the
meantime. This is the whole reason the design is cheap, and it is what separates
it from the withdrawn one, which proposed a blocking wait.

One consequence must be stated: `candidates` is computed once, at `:212`, and
not recomputed. So keeping the provider is not enough on its own; the loop must
be able to consider it again after its skip expires. Whether that is a recompute
or a separate holding place is an implementation choice, and the spec requires
only that a provider kept under 1 is actually reachable again.

### 3. A bound remains, because one is needed

Removing `maxOverdraftReadmits` entirely would let a chunk keep asking a peer
that will never pay. Two bounds already prevent that and neither is removed:
`errorsLeft` caps total attempts at 32 to 34, and the request context caps the
whole download.

The readmit count itself becomes a count of **refusals that were forgiven**
rather than a licence to fail. Whether 8 is still the right number is a question
for the measurement below, not an assumption of this design.

### 4. What must not change

- **Ordinary downloads.** Content the network holds must not get slower. This is
  a reject condition, not a footnote: the first attempt at #324 regressed it
  about 3x and that is how the regression was found.
- **The wire.** No protocol identifier, message or handshake change.
- **Forwarded requests.** A forwarded request gets `errorsLeft = 1` and no
  preferred candidates (`:237-243`), so it does not reach this path. The change
  must be verified not to touch it, because holding a forwarded request open
  spends another node's connection, and rule 8 requires that cost be stated.

### 5. A counter, because the failure it replaces is silent

A counter of chunks where a named provider was kept past the readmit bound and
then served the chunk, and one where it was kept and the chunk still failed.
Without the second, a change that keeps providers and still truncates looks
exactly like one that works.

## Protocol impact

**None.** No wire format, protocol identifier, handshake or message change. This
is entirely in which peer the requester asks next. `make protocol-freeze` must
pass with the fingerprint unchanged.

## Measurement

Sole-source content is available on demand through local ingest
([#326](https://github.com/crtahlin/wasp/issues/326)), so none of this waits on
a postage batch.

- **The arm this is for.** Sole-source content, before and after, three runs
  each, at the buffer used for the diagnosis. Accept on completion, with the
  rate reported beside it.
- **The regression arm, weighted equally.** Content the network also holds,
  before and after, three runs each. A median more than 10% slower after the
  change is a reject whatever the first arm did.
- **Per run**: bytes against bytes wanted, SHA-256, curl exit, the balance with
  the provider at the start, the announced threshold at both ends,
  `preferred_attempts`, `preferred_hits`, `preferred_overdrafts`,
  `preferred_readmits`, the two new counters, and `accounting_blocks_count`.
- **Per failing chunk, where any remain**: the attempt count and the readmit
  window, as measured here, so a partial improvement is visible rather than
  being averaged away.
- **Node state matched**, in one session, with the arms interleaved. Both arms
  restart nothing. Three results in this project have been withdrawn for getting
  this wrong.

**Pre-registered falsifier.** If sole-source downloads still truncate with the
provider kept, then the readmit bound was not what ended them and this document
is wrong in the same way as the two before it. The per-chunk numbers above are
what would show it.

## Acceptance

**Accept** if sole-source downloads complete in three runs of three where they
truncated before, **and** content the network also holds is not more than 10%
slower at the median.

**Reject** if widely-held content regresses beyond that, since the whole
difficulty is that the two cases want opposite behaviour; or if sole-source
downloads still truncate, which falsifies the diagnosis rather than the
implementation.

## Test plan

In `package retrieval_test`:

- a chunk held only by a named provider that is overdrafted at first and has
  credit after its skip expires is retrieved, rather than failing;
- a chunk whose named provider is not connected still drops it at once, so the
  expiring and non-expiring cases stay separate;
- a chunk held by an ordinary peer is retrieved with no added delay, asserted on
  elapsed time, which is the #324 regression in test form;
- the total attempt bound still holds, so a provider that never regains credit
  cannot hold a request open;
- a forwarded request is unaffected;
- the two counters move exactly when their conditions are met.

Each test to be mutation-checked. Three tests in this project have passed with
the behaviour they named removed.

## Upstream portability

The readmit path and the preferred candidate list are **fork code**, from #324
and #290. Unmodified upstream drops a refused peer at once with no readmission
at all, so upstream is worse in this case and this change extends a fork
mechanism rather than correcting an upstream one.

`maxOriginErrors`, `maxMultiplexForwards`, the skip list and the all or nothing
`joiner.ReadAt` are unmodified upstream. **No `affects-upstream` marker.**

## Rollout and rollback

A behaviour change with no setting, on by default, because the case it fixes is
one where the download fails today. Rollback is a revert. Nothing is written to
disk and nothing is announced, so there is no migration and no state to undo.

---

Generated with help of AI.
