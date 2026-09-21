# Keeping a provider on a chunk it is the only source for

Issue: [#392](https://github.com/crtahlin/wasp/issues/392). Type: fix.

**This replaces [wait-for-credit.md](wait-for-credit.md), which is withdrawn.**
That design inferred from one failed ordinary retrieval that the network did not
hold a chunk, which nothing on the wire supports, and it could livelock. The
measured truncation it chased turned out to have a different cause, recorded
below, and what remains after that cause is removed is a narrower defect.

## Problem

A chunk whose only holder is a content provider is abandoned when that provider
is briefly out of credit, even though the provider is connected, holds the
chunk, and will regain credit within a second.

`retrieval.go` keeps a preferred peer across an overdraft up to
`maxOverdraftReadmits`, which is 8. On the ninth the `default:` branch runs and
`candidates = candidates[1:]` drops the provider from that chunk for the rest of
the flight. The chunk then has no source at all: ordinary peers never had it.
Because `joiner.ReadAt` is all-or-nothing over a read unit, one lost chunk
truncates the whole download.

Measured on the bench, one 4 MiB sole-source download: overdrafts exceeded
readmits by **214**, which is the number of chunks that hit the cap and lost
their only holder.

### What this is not

**Most of the truncation previously attributed to this was a flat chequebook.**
The requester's chequebook had 0.7623 BZZ deposited and 0.000004 BZZ available,
so every cheque failed with "chequebook out of funds", 250 times in six hours,
each failure imposing a 10 second settlement backoff. All debt clearing fell
back to the time allowance at 4,500,000 units/s. Funding it changed the same
50 MB download, on the same build, from 17m29s to **16.8 s**, and made a 4 MiB
download that had failed 5 runs of 5 from a fresh peer relationship complete.

So this spec is about the residue: with the chequebook funded, **50 MB still
truncated on 1 run of 2** (23,592,960 bytes, then a clean 52,428,800). The
defect is real but it is a reliability defect at large sizes, not the throughput
cliff it was mistaken for. Any rate claim measured before 2026-09-21 on this
bench should be re-checked against `cp290/liquidity-gate.sh` before being reused.

## Hypothesis

An overdraft is a statement about the requester's funds, not about the peer. A
provider that is merely unfunded is still the only known holder, and dropping it
converts a delay into a permanent failure. Retaining it, while leaving ordinary
selection completely untouched, should remove the residual truncation without
changing anything about content the network holds.

## Design

Three changes, all in the preferred path.

**1. An overdraft never drops a preferred candidate.** Today the readmit cap
falls through to `default:`, which consumes the candidate. Split the cases so
that an overdraft and a refusal that cannot clear by itself are handled
separately: only the latter drops the peer, which is what
[#324](https://github.com/crtahlin/wasp/issues/324) established and what
`TestPreferredNonOverdraftNotRetried` pins.

**2. Provider retention gets its own bound, in wall-clock time.** Replace the
`maxOverdraftReadmits` count with a deadline: a provider stays a candidate for
that chunk until `providerCreditWait` has elapsed since the first overdraft on
it, then it is dropped exactly as today. A count cannot express the thing that
matters, which is how long credit takes to arrive; the observed settlement
cadence is roughly one refreshment per second and one cheque per 1.2 s, so eight
tries can expire in well under a second while the money is still on its way.

Proposed value **5 s**, with the spec required to justify it against the
measured cadence rather than pick it. It bounds the added delay per chunk in the
worst case, and the request context continues to bound the download.

**3. The error budget is not spent on a chunk that still has a provider.**
`errorsLeft` exists to end a hopeless search among ordinary peers. While a
verified provider is retained for the chunk the search is not hopeless, so
ordinary failures should not count against it. With no candidate left it behaves
exactly as now.

**Ordinary selection is untouched.** The first overdraft still falls through to
it immediately, which is the behaviour measured at three times faster on content
the network also holds, and no path is made unreachable. This is the specific
defect that made the withdrawn design livelock, and the replacement must be read
with that in mind.

### Also: make the provider's own "I do not hold it" usable

`errNotHeldLocally` (`preferred.go:48`) is the one true statement a provider
makes about a chunk, and it is flattened into a string at `retrieval.go:609`, so
the requester cannot `errors.Is` it and treats a genuine miss exactly like a
timeout. Carry it back recoverably so a provider that says it does not have the
chunk is dropped at once rather than retained for the full wait.

This says nothing about the network, only about one peer, which is its correct
scope and is precisely where the withdrawn design overreached.

## Protocol impact

None. No wire message, constant or version changes; `pb.Delivery` keeps its
shape and the recoverable error is produced by matching the existing string at
the requester rather than by adding a field. `make protocol-freeze` passes
unchanged and the `protocol-change` label is not applied.

## Measurement

All runs gated by `cp290/liquidity-gate.sh`, which refuses to measure when the
chequebook cannot issue a cheque. Three runs per condition with the spread, arms
interleaved, and **every condition repeated from a fresh peer relationship**,
where the announced threshold is still 13,500,000.

**Arm 1, the residual truncation.** 50 MB sole-source, hinted. Passes when all
three runs deliver 52,428,800 bytes with a matching SHA-256. The control is the
current build, which delivered 1 of 2.

**Arm 2, no regression on content the network holds.** 16 MiB uploaded normally
with postage, fetched with no hint, three runs before and after. Passes when the
wall-clock rate is within the spread of the control. **A failure here is a
reject even if arm 1 passes**, and it is the arm the withdrawn design failed.

**Arm 3, a hopeless search still ends quickly.** A reference no node holds,
fetched with no hint and with a hint naming a peer that does not have it. Passes
when both fail in a time comparable to the control rather than after the
retention deadline.

**Arm 4, a provider that says no is dropped at once.** A hinted download for
content the provider does not hold. Passes when it falls back to ordinary
selection without waiting, which is what change 3 above buys.

**What a negative result looks like.** Arm 1 still truncating means the residual
cause is not candidate retention, and the next suspect is the read unit being
all-or-nothing in `joiner.ReadAt` rather than anything in accounting.

## Rollout and rollback

No configuration; `providerCreditWait` is a constant unless the measurement shows
it needs to be a dial, per rule 8. Rollback is `git revert` of the merge commit.

## Upstream portability

The preferred set, the candidate list and `maxOverdraftReadmits` are all this
fork's (#299, #324), so there is no upstream counterpart and the issue is not
tagged `affects-upstream`.

## Files

- `pkg/retrieval/retrieval.go`, the overdraft switch and the error budget.
- `pkg/retrieval/preferred.go`, the retention deadline and the recoverable miss.
- `pkg/retrieval/preferred_test.go`, cover: an overdraft never drops a candidate;
  a non-overdraft refusal still does; the deadline eventually does; a stated miss
  drops at once; the budget is untouched while a candidate remains.
- `docs/DIFFERENCES.md`, a row, since this changes what a node does.
