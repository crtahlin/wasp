# Space the retries at the only peer that holds the chunk

Issue: [#343](https://github.com/crtahlin/wasp/issues/343). Diagnosis:
[truncation-cause.md](truncation-cause.md).

Code references are to `main` at `527f32b2`, base `upstream/v2.8.2`.

**Three designs have now been withdrawn on this issue**, including the first
draft of this document. They are listed at the end, because the reason each
failed is what constrains this one.

## Terms

- **Named provider**: a peer the requester was told holds the content, through
  the `Wasp-Providers` header, rather than chosen by proximity.
- **Preferred path**: the branch that tries named providers,
  `retrieval.go:274-315`. Distinct from **ordinary selection**, `:318` onward,
  which picks by proximity.
- **Skip**: a per-chunk exclusion with an expiry. A peer refused on credit is
  added to it for `overDraftRefresh`, 600 ms.
- **Sole-source content**: content no other node holds.

## Problem

**The preferred path never reads the skip list, so eight retries at the only
peer that can serve the chunk all happen within a fifth of a second, before any
of them could possibly succeed.**

`candidates[0]` is taken unconditionally (`:274-276`). The `skip` argument is
passed to `retrievePreferred` so that it can **write** to it (`preferred.go:230`),
and the only reader is ordinary selection, which feeds `fullSkip` to
`closestPeer` (`:318-320`).

So the comment at `:284-286`, which says the loop "returns to this peer once its
`overDraftRefresh` skip has expired", describes behaviour the code does not
have. The loop returns to the peer immediately, on the next retry token.

Measured, seven failing chunks across three runs:

| | Range | Median |
|---|---|---|
| Readmit window, the 8 readmissions | 0.141 to 0.832 s | **0.217 s** |
| Whole chunk, first attempt to not-found | 0.978 to 1.951 s | **1.06 s** |

Eight refusals inside 0.217 s are **eight attempts at one instant**, as far as
credit is concerned. The gate they fail is
`increasedExpectedDebt > paymentThreshold + refreshDue`, where
`refreshDue = min(elapsed_seconds, 1) * refreshRate`
(`accounting.go:324-331`). At 0.217 s the elapsed term has grown by about
976,000 units, roughly three chunks' worth at a measured price near 307,000.
Eight refusals spread across that are refused for the same reason each time.

Then, on the ninth, the `default:` arm consumes the candidate (`:299`) and the
provider is gone from the chunk for good. The chunk spends its remaining 0.84 s
on ordinary peers which, for sole-source content, cannot answer, exhausts
`maxOriginErrors` and returns `storage: not found`. `joiner.ReadAt` is all or
nothing, so one such chunk truncates the download.

## Hypothesis

Retries at a named provider refused on credit should be spaced by the skip the
code already records, so that each is a genuinely different moment.

**Predicted:** a chunk that today spends eight useless attempts in 0.217 s
instead makes one or two spaced attempts within its existing life, at least one
of which finds the gate loosened. Content the network also holds is unaffected,
because ordinary selection is untouched and runs in the same iteration as
before.

## Design

### 1. Read the skip list on the preferred path

Before taking `candidates[0]`, pass over any candidate currently skipped for
this chunk. This is the whole change. It makes the existing comment true, and it
uses a structure that is already maintained and already written to on exactly
this event.

At 600 ms of spacing and a chunk life near 1.06 s, a chunk gets **one or two**
provider attempts rather than eight. **That is fewer attempts, deliberately.**
Eight attempts at one instant are worth less than one attempt at a moment when
the gate has moved, and by 600 ms the elapsed term alone has added about
2,700,000 units, roughly nine chunks' worth.

### 2. The readmit bound stays exactly as it is

An earlier draft of this document proposed not consuming the candidate at all,
and that was wrong in a way worth recording. **`errorsLeft` does not decrement
on the preferred path**: a preferred result returns early at `:400-405` with the
comment that a miss at a preferred peer is not a peer error. So
`maxOverdraftReadmits` is the **only** bound on preferred attempts, and its own
doc comment says so: "Bounded so a peer that never regains credit cannot
livelock the request" (`:156-158`). Removing it would have created exactly that
livelock.

Keeping the bound and spacing the attempts is the smaller change and the safe
one. Whether 8 is still the right number once attempts are spaced is a question
for the measurement, not an assumption here.

### 3. A refusal that is not an overdraft must not be treated as one

`PrepareCredit` returns a lock-acquisition failure before it reaches the
overdraft check (`accounting.go:283-286`). That is not `ErrOverdraft`, so it
reaches the `default:` arm and drops the provider permanently, and it is a
transient condition that clears by itself. The bench log records **three** of
these in a single run.

This design does not change that arm, but the measurement must count the class
separately, because a chunk lost to a lock timeout would otherwise be read as a
chunk lost to credit and would fire this document's falsifier for the wrong
reason.

### 4. What must not change

- **Ordinary downloads.** Content the network holds must not get slower. This is
  a reject condition and is weighted equally with the arm the change is for:
  the first attempt at
  [#324](https://github.com/crtahlin/wasp/issues/324) regressed it about 3x and
  that is how it was caught.
- **The wire.** No protocol identifier, message or handshake change. The freeze
  file fingerprints `protocolName` and `protocolVersion`, neither of which this
  touches.
- **Forwarded requests.** A non-origin request gets `errorsLeft = 1`
  (`:236-241`) and no preferred candidates (`preferred.go:175-181`, gated on
  `origin`), so this path is untouched. Verified rather than assumed.

### 5. A counter, because the failure it replaces is silent

Count chunks where a provider was passed over because it was skipped, and
chunks where the provider was retried after a skip expired and then served the
chunk. Without the second, a change that spaces attempts and still truncates
looks exactly like one that works.

## What this risks

- **Fewer provider attempts per chunk.** If the gate does not loosen within the
  chunk's life, spacing means one or two failures instead of eight, and the
  chunk still dies. This is the main way the change does nothing, and the
  falsifier below is aimed at it.
- **A late success is worse than an early failure.** If a provider regains
  credit late in a widely-held chunk's life, `retrievePreferred` returns nil,
  the iteration takes `:304-313`, runs **no** ordinary selection, and arms
  `preferredWait` at 500 ms (`preferred.go:37`). Today that cannot happen after
  the eighth refusal because the candidate is gone. Spacing makes a late
  attempt possible, so this is a new path on the arm that must not regress, and
  the regression arm exists to find it.
- **Less lock contention, not more.** Passing over a skipped provider means
  fewer `prepareCredit` calls on that peer's lock, which should reduce the
  class in 3 rather than increase it. Stated as an expectation to check, not a
  claim.

## Protocol impact

**None.** No wire format, protocol identifier, handshake or message change.
`make protocol-freeze` must pass with the fingerprint unchanged.

## Configuration

**No new setting, and none of the three constants involved is exposed.**

`maxOverdraftReadmits` (8), `overDraftRefresh` (600 ms) and `preferredWait`
(500 ms) stay compiled in. Under rule 8 a constant becomes a setting only once a
measurement shows the value matters, and this change alters what the first of
them means, from eight attempts at an instant to eight spaced attempts, without
yet showing that eight is the wrong number.

If the measurement shows the count matters, the cost of each direction must be
stated then: raising it keeps a chunk asking a provider that may never pay,
holding the request open longer; lowering it gives up on the only holder sooner.
Both are per chunk, so both scale with file size.

## Measurement

Sole-source content is available on demand through local ingest
([#326](https://github.com/crtahlin/wasp/issues/326)), so nothing waits on a
postage batch.

**The arms cannot be interleaved in one session, and an earlier draft of this
plan said they could.** Before and after are different binaries, and there is no
runtime toggle, so a binary swap and a restart sit between them. The plan is
therefore the one this project already had to adopt after a withdrawal in
[overdraft-retry-results.md](overdraft-retry-results.md):

- **Restart cycles, not a single session.** Three cycles per build, each
  starting from a restart, with the balance with the provider read at the start
  of every run and reported. A run whose starting balance differs materially
  from its counterpart is reported separately rather than averaged in.
- **Randomised order within each cycle**, because both baselines in the
  diagnosis fall monotonically across three runs, 2,359,296 then 1,441,792 then
  917,504 in one and 1,736,704 then 1,146,880 then 917,504 in the other.
  Interleaving does not remove a monotone trend.
- **Buffer 0 named explicitly**, which is where the diagnosis ran.
- **Report the spread, not only the median**, on both arms. Three runs cannot
  resolve a 10% median difference without it, and the regression criterion
  depends on that resolution.
- **Per run**: bytes against bytes wanted, SHA-256, curl exit, starting balance,
  announced threshold at both ends, `preferred_attempts`, `preferred_hits`,
  `preferred_overdrafts`, `preferred_readmits`, the two new counters, the count
  of lock-acquisition failures from 3, `accounting_blocks_count`, and the counts
  of the `no peers left` and `sleeping to refresh overdraft balance` branches,
  which an earlier harness recorded and a later one dropped.
- **Per failing chunk that remains**: attempt count and readmit window, so a
  partial improvement is visible rather than averaged away.

**Pre-registered falsifier.** If sole-source downloads still truncate with
attempts spaced, then spacing was not the constraint and this document is wrong
in the same way as the three before it. The per-chunk readmit window is what
would show it: spaced attempts should move it from about 0.22 s toward the
chunk's whole life.

## Acceptance

**Accept** if sole-source downloads complete in three runs of three where they
truncated before, **and** content the network also holds is not more than 10%
slower at the median with the spread reported.

**Reject** if widely-held content regresses beyond that, since the whole
difficulty is that the two cases want opposite behaviour; or if sole-source
downloads still truncate, which falsifies the diagnosis rather than the
implementation.

## Test plan

In `package retrieval_test`:

- a preferred peer skipped for this chunk is passed over rather than attempted,
  asserted on the attempt count at that peer;
- a preferred peer whose skip has expired is attempted again;
- a chunk held only by a provider overdrafted at first and solvent after the
  skip expires is retrieved rather than failing;
- a chunk held by an ordinary peer is retrieved with no added delay, asserted on
  elapsed time, which is the #324 regression in test form;
- the readmit bound still terminates a chunk whose provider never regains
  credit, asserted to return rather than hang, since `errorsLeft` does not bound
  this path;
- a forwarded request is unaffected;
- the two counters move exactly when their conditions are met.

Each test to be mutation-checked. Three tests in this project have passed with
the behaviour they named removed.

## Upstream portability

`maxOverdraftReadmits` and the preferred path are **fork code**, from #324 and
#290; the constant is absent from `upstream/v2.8.2`.
[retrieval-rate.md](retrieval-rate.md) lists it among unmodified upstream code
and is wrong about that.

**Upstream is not worse here, and an earlier draft said it was.** Upstream's
ordinary path already does what this change adds: it adds a credit-refused peer
to the skip with `overDraftRefresh`, and `closestPeer` then honours that skip.
What the fork's preferred path lacks is the skip *check*, so this change brings
it in line with the behaviour upstream already has on the other path, rather
than inventing anything.

**No `affects-upstream` marker.** The gap is in fork-authored code.

## The three withdrawn designs

Recorded because each was withdrawn for a different reason and the reasons
constrain what is left.

1. **Wait for credit where the loop gives up.** Withdrawn: by then the provider
   is not in the candidate list, so there would be nothing to retry.
2. **Do not consume the candidate for a reason that expires.** Withdrawn: the
   preferred path never reads the skip, so the provider would return in about
   30 ms rather than 600 ms, and removing the bound would remove the only thing
   that terminates this path, since `errorsLeft` does not decrement on it.
3. **The credit ceiling ends the download.** Withdrawn earlier and separately:
   the run with the fewest refusals truncated earliest, and downloads fail at a
   balance of zero.

---

Generated with help of AI.
