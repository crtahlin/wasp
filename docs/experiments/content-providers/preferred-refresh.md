# Refreshing the preferred candidates of a chunk in flight

Issue: [#435](https://github.com/crtahlin/wasp/issues/435).
Type: fix.

**The issue proposed the wrong fix and this spec does not implement it.** #435
suggested waiting for the hinted dial before retrieval begins, on the reasoning
that the download finishes before the dial lands. Measured, the dial lands after
**0.22 seconds** while the failing request runs for **3.1 seconds**. Waiting
would work by accident. The defect is elsewhere and is described below. The
issue is corrected rather than followed.

## The defect

`candidates` is computed once per flight, before the retry loop, and is never
refreshed inside it:

```go
// pkg/retrieval/retrieval.go:232
		candidates := s.preferredCandidates(preferredPeers, chunkAddr, s.errSkip.ChunkPeers(chunkAddr))
```

`preferredCandidates` keeps only peers already in the connected set
(`pkg/retrieval/preferred.go:202`). A hinted download starts the dial to its
provider in a background goroutine and returns at once
(`pkg/providers/providers.go:411-445`, `pkg/api/providers.go:100-103`), so the
first chunk is issued while the provider is still unconnected. Its candidate
list is empty, and it stays empty for that chunk's whole retry budget even after
the provider connects a fifth of a second later.

For sole-source content the first chunk is the root of the reference. It spends
its budget on ordinary peers that never held it, returns not found, and the API
answers 404. Nothing else is attempted, because without the root there is
nothing to attempt.

The same gap applies to discovery. `Discover` adds providers to the preferred
set part way through a download (`pkg/api/providers.go:115-119`, after
`discoverAfterChunks`). Chunks already in flight cannot see them either.

## Evidence

Five runs, `cp290/t435-dialtime.sh`. Each ingests fresh sole-source content on
the provider, disconnects the provider from the requester, then starts a poller
and issues one hinted download. The poller samples the requester's peer list
every 100 ms and records when the provider appears.

| run | request | dial landed |
|---|---|---|
| 1 | 404, 36 bytes, 3.127 s | 0.22 s |
| 2 | 404, 36 bytes, 3.099 s | 0.22 s |
| 3 | 404, 36 bytes, 3.075 s | 0.22 s |
| 4 | 404, 36 bytes, 3.116 s | 0.22 s |
| 5 | 404, 36 bytes, 3.100 s | 0.22 s |

The poller and the request run concurrently in the same run, so this is not two
measurements laid side by side. **The provider was connected for about 2.9 of
the request's 3.1 seconds and was not used.** That is what rules out the
explanation in #435: there was ample time, and the request had a connected
provider holding every chunk it needed.

Five of five, with no spread worth reporting at 0.22 s.

### What this does not establish

In the three trials in [dial-race.md](dial-race.md), the failing run recorded 14
preferred attempts with zero hits. If no chunk ever saw a candidate, there
should have been none at all. Fourteen attempts with `maxPreferredAttempts = 2`
means at least seven chunks reached a non-empty list and still did not produce a
hit. That is not explained here. It does not affect the change below, whose
acceptance test is whether the download completes, but it should not be written
up as understood when it is not.

## The change

Refresh the candidate list from the preferred set when it runs out, inside the
retry loop, instead of only once before it.

```go
			case <-retryC:
				if len(candidates) == 0 {
					candidates = s.preferredCandidates(preferredPeers, chunkAddr, dropped(...))
				}
```

The chunk then picks up a provider that became connected, or was discovered,
after its flight began.

### The hazard this has to avoid

A peer that fails is dropped from the list by a plain slice trim, and **nothing
records that it was dropped**:

```go
// pkg/retrieval/retrieval.go:348, the default arm
						candidates = candidates[1:]
						retry()
						continue
```

`skip` and `s.errSkip` are consulted when the list is built, but this arm writes
to neither. A naive refresh would therefore re-add the peer that was just
dropped, on every retry, and a provider failing fast would spin against the
error budget instead of falling through to ordinary selection.

So the refresh needs a set of peers already dropped **for this chunk in this
flight**, and must exclude them. That set is local to the flight, like
`overdraftSince` (`:287`) already is, and costs one map.

Note this is not the overdraft path. An overdraft keeps the peer and rotates it
to the back (`:341`), so it is still in the list and the refresh never sees an
empty list on its account. Only the `default` arm drops, and after
[#392](https://github.com/crtahlin/wasp/issues/392) that arm is reached by a
peer that is not connected or whose 30 second retention window has passed.

### Why not wait for the dial

Waiting before retrieval, which is what #435 proposed, was considered and is
rejected:

- It blocks every hinted request by up to the bound, including the common case
  where the provider is already connected and the correct wait is zero.
- It does nothing for discovery, which adds providers mid-download by design.
- It needs a new constant, and rule 8 asks for a measurement before a dial is
  exposed. The refresh needs none.
- It treats a symptom. The chunk would still be unable to use a peer that
  connects one millisecond after its flight starts.

The refresh subsumes it: a provider connecting at 0.22 s is picked up by the
root chunk's next retry, which on the measured timings leaves about 2.9 seconds
of budget.

### Why this is not the change that was withdrawn

`fix/392-wait-for-credit` was abandoned for waiting on credit inside the
per-chunk loop, which livelocked and cost a measured 2 to 3 times on content the
network also holds ([wait-for-credit.md](wait-for-credit.md)). This change waits
for nothing. It recomputes a list from state that is already in memory and
returns immediately, whether or not a candidate is found.

It is also inert unless content providers are in play. With no hint and no
discovery, `preferredPeers` is empty, `preferredCandidates` returns an empty
list every time, and the only cost is the length check.

## What it costs

One topology lookup per preferred peer per refresh, and only on a retry that
found the list empty. `connectedFullNode` is a single `ClosestPeer` call
(`pkg/retrieval/preferred.go:222-228`). A hint is capped at
`maxProviderHints = 8` (`pkg/api/providers.go:31`), so the worst case is eight
lookups on a retry, against a retry budget of `maxOriginErrors = 32`.

The cost falls on downloads that have a preferred set and are retrying, which is
the case this is meant to help. A download with no preferred set pays one
integer comparison.

## Protocol impact

None. No wire message, no header, no constant in
`.github/protocol-freeze.lock`. The change is inside one requester-side loop and
is not observable by a peer except as a retrieval request it would have received
later anyway.

## Measurement

Rule 7 applies: three runs per condition, spread reported, arms interleaved,
node state matched.

The acceptance criterion is **delivered bytes with a matching checksum**, not a
counter.

1. **The failing case must pass.** `cp290/t313-dialrace.sh` unchanged: disconnect
   the provider, then one hinted download of fresh sole-source content. Today
   this returns 404 in 3 of 3. It must return 4,194,304 bytes with a matching
   checksum in 3 of 3.
2. **No regression on content the network holds.** A network-held download,
   hinted and unhinted, must stay within the spread of the control on the
   unmodified build. This is the arm that rejected `fix/392-wait-for-credit` and
   it is the one that matters most.
3. **A reference nobody holds must still fail fast.** With a hint naming a peer
   that does not have the content, the download must fail in seconds rather than
   spinning to the request deadline. This is the hazard above, measured rather
   than argued.
4. **An unresolvable hint costs nothing.** A hint naming an overlay not in the
   address book must not change the download's timing against the control.
5. **Already-connected provider unchanged.** The six downloads recorded in
   [dial-race.md](dial-race.md) at 2.19 to 2.96 MB/s are the baseline; the rate
   must not fall.

Counters to record on every run: `bee_retrieval_preferred_attempts`,
`preferred_hits`, `preferred_overdrafts`, `preferred_readmits` and their
difference, and `bee_retrieval_request_failure_count`.

## Tests

Unit tests in `pkg/retrieval`, each checked by mutation: break the change and
confirm the test fails. A test that passes both ways is this repository's usual
failure mode.

- A preferred peer that is not connected when the flight starts, and becomes
  connected during it, is used. This is the defect and it must fail without the
  change.
- A preferred peer dropped by the `default` arm is **not** re-added by a
  refresh. This is the hazard and it must fail if the dropped set is omitted.
- A download with no preferred set behaves exactly as before.
- An overdrafted peer is not double counted: rotation still puts it at the back
  and the refresh does not duplicate it in the list.

## Rollout and rollback

No configuration, no migration, no on-disk change. Rollback is reverting the
merge commit.

## Upstream portability

Not applicable. `pkg/retrieval/preferred.go`, `pkg/providers` and
`pkg/api/providers.go` do not exist in `upstream/v2.8.2`, checked with
`git ls-tree`. The preferred path is this fork's own, added by
[#290](https://github.com/crtahlin/wasp/issues/290).
`pkg/retrieval/retrieval.go` is modified here relative to upstream already.
**No `affects-upstream` marker.**

## Files

- `pkg/retrieval/retrieval.go`, the flight loop at `:232` and the drop arm at
  `:348`.
- `pkg/retrieval/preferred_test.go`, or a new test file alongside it.
- `docs/DIFFERENCES.md`, since this changes what a node does compared with Bee
  only inside fork-only code; the existing `Wasp-Providers` row at `:157` says
  the named overlays are "tried first, with normal retrieval as the fallback",
  which is not true today for a provider that is not yet connected and becomes
  true with this change.

Generated with help of AI.
