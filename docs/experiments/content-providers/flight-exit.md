# Do not spend the error budget while a provider is still answering

Issue: [#438](https://github.com/crtahlin/wasp/issues/438).
Type: fix.

**An earlier draft of this spec proposed a broader change and it was unsafe.**
Review showed it would hold a forwarder open for tens of seconds on a chunk
nobody holds, and that its central claim was false. Both are recorded under
"Alternatives" rather than removed, because the broad version is the obvious
idea and the next person will have it too.

## Problem

A chunk retrieval can give up while a request it already sent is still being
answered, and then throw the answer away. The chunk is reported as
`storage: not found` even though a peer was in the middle of delivering it.

The flight loop ends on the origin error budget, and that condition is checked
with no test for outstanding work:

```go
// pkg/retrieval/retrieval.go:289
		for errorsLeft > 0 {
```

When it is taken, `return nil, storage.ErrNotFound` runs at `:472` and the
deferred `close(quit)` at `:255` fires. `retrieveChunk` selects on `quit`
(`:500-504`) and discards the delivery it was about to hand back.

The case is easy to reach through the preferred path. A preferred peer is
removed from the candidate list as soon as it is dispatched (`:353`), and
ordinary misses resume spending the budget once the list is empty (`:464`). The
provider is then the only peer holding the chunk, is still answering, and the
budget runs out underneath it.

## Evidence

Unit tests in `pkg/retrieval` with mock streamers, not a bench run, so the
timings are deterministic rather than subject to a network. Three runs per arm
per rule 7. A provider holds the chunk and answers after a 3 second lag;
ordinary peers hold nothing and fail at once.

| ordinary peers | runs | result |
|---|---|---|
| 40, more than `maxOriginErrors` | 3 | fails after **501.94 / 501.71 / 501.41 ms**, `storage: not found` |
| 4, too few to spend the budget | 3 | succeeds after **3.0006 / 3.0005 / 3.0012 s**, chunk returned |

Spread is 0.53 ms on the first arm and 0.75 ms on the second.

502 ms decomposes exactly: after a preferred dispatch the loop `continue`s at
`:361` without calling `retry()`, so nothing drives ordinary selection until
`preferredTimerC` fires at `preferredWait`, which is 500 ms
(`pkg/retrieval/preferred.go:37`). The remaining 2 ms is a sweep of 40 mock
peers. No other constant is in range: `overDraftRefresh` is 600 ms and
`preemptiveInterval` is 1 s.

The second arm is the control. Same provider, same lag, same code; only whether
the budget is exhausted differs. An earlier draft used 32 ordinary peers, which
is exactly `maxOriginErrors`, so budget exhaustion and peer exhaustion coincided
and the result depended on which was tested first. Forty separates them.

## The change

Do not spend the error budget while a request to a preferred peer is still
outstanding. That is the same rule the fork already applies at `:464`, extended
from "a candidate is in the list" to "a candidate is in the list or one is still
answering".

```go
		inflight := 0
		// outstanding requests to preferred peers, see wasp #438
		preferredInflight := 0
```

```go
					if err == nil {
						candidates = candidates[1:]
						inflight++
						preferredInflight++
```

```go
				if res.preferred {
					preferredInflight--
					s.preferredResult(preferredSet, res)
```

```go
				if len(candidates) == 0 && preferredInflight == 0 {
					errorsLeft--
				}
```

The loop condition is untouched, so every exit behaves as it does today.

### It is provably inert without a hint

`preferredPeers` is only populated for an origin request with providers on
(`:201-202`, guarded by `if origin && s.providers.Load()`), a preferred peer is
only dispatched from a non-empty candidate list, and `preferredInflight` only
moves on that dispatch. So on a forwarder, and on any origin download carrying
no hint and with nothing discovered, `preferredInflight` is zero for the life of
the flight and the guard reduces to the existing `len(candidates) == 0`.

That matters beyond tidiness. `docs/DIFFERENCES.md:157` states the fork's
guarantee for this loop: "With no candidate left, both changes behave exactly as
before, so a download carrying no hint is unaffected: the code paths they touch
are both inside a test for a candidate being present." This change keeps that
invariant. The broad alternative below breaks it.

### Validation

Prototyped and run before this spec was written, because the previous two
designs for this area were specified without being run and both were wrong.

- The failing arm above, 40 ordinary peers, returns the chunk after 3.0012 /
  3.0014 / 3.0009 s, three of three, where it failed at 502 ms before.
- The control arm still passes.
- The whole of `pkg/retrieval` passes, and passes under `-race`.

## What it costs

**A hinted download can now wait for a provider that accepts the stream and
never answers.** That is the real cost and it is bounded, measured rather than
argued: with a provider whose lag exceeds the per-request timeout, the call
returned after **30.001 s** with `no peer found`, which is
`RetrieveChunkTimeout` (`:157`, 30 seconds) applied inside `retrieveChunk` at
`:507`. One such timeout, once, at the end of a flight.

Two limits keep that narrow:

- It is reachable only on a download that named a provider or discovered one.
  Everything else is inert, as above.
- It replaces a failure. The download that pays this today returns nothing at
  all, so the comparison is 30 seconds against a wrong answer, not against a
  fast right one.

A chunk that genuinely does not exist is unaffected whenever no preferred
request is outstanding, which is every hint-less download and every forwarder.

**No new cost to other operators.** The change starts no additional requests and
does not extend a forwarder's flight, because `preferredInflight` cannot be
non-zero on a forwarder. Outbound request volume is unchanged.

## Alternatives

**Change the loop condition to `for errorsLeft > 0 || inflight > 0`.** This was
the first draft. It is wrong, in two ways that only appear when traced:

- **It holds a forwarder open on a missing chunk.** The multiplexer at `:402-407`
  raises the budget and queues retries when the peer is within radius:
  `for ; forwards > 0; forwards--` runs `retry()` and `errorsLeft++`. A
  forwarder starting at `errorsLeft = 1` can dispatch three peers, have three
  fail, and reach `errorsLeft == 0` with two still outstanding. Today that
  returns not-found at once. Under the broad change it waits, up to
  `RetrieveChunkTimeout` and beyond, because `FullClose()` carries its own
  30 second `closeDeadline` (`pkg/p2p/libp2p/stream.go`) in a defer that runs
  before the reporting one. On a forwarder the flight **is** the requesting
  peer's stream (`:672`), so that peer's slot is held too. This repository has
  already paid once for chunk fetches left waiting: `docs/DIFFERENCES.md:54` records
  #398 as 324 goroutines left waiting in retrieval.
- **Its stated benefit was false.** The draft claimed the discard became
  "structurally impossible". The success return at `:443`,
  `return res.chunk, nil`, has no in-flight test, so `close(quit)` still fires
  with siblings outstanding on every successful multiplexed retrieval. That
  claim is withdrawn here and in the issue.

The narrow change avoids both because it never alters when the loop may exit,
only when the budget is spent, and only while a provider is answering.

## Protocol impact

None. No wire message, no header, no constant in
`.github/protocol-freeze.lock`. The change is inside one requester-side loop.

## Measurement

Rule 7: three runs per condition, spread reported, arms interleaved, node state
matched. Acceptance is delivered bytes with a matching checksum.

1. **No regression on content the network holds**, hinted and unhinted, within
   the spread of the control on the unmodified build. This is the arm that
   rejected `fix/392-wait-for-credit`.
2. **Sole-source download rate not worse** than the six runs in
   [dial-race.md](dial-race.md), 2.19 to 2.96 MB/s.
3. **A reference nobody holds still fails quickly** with a hint naming a peer
   that does not have it, rather than waiting out a timeout per chunk.

Counters each run: `bee_retrieval_preferred_attempts`, `preferred_hits`,
`request_failure_count`, `request_success_count`, `total_retrieved`.

## Tests

In `pkg/retrieval`, each checked by mutation: revert the change and confirm the
test fails. A test that passes both ways is this repository's usual failure mode.

- **The reproducer**: a slow provider holding the chunk, 40 ordinary peers
  failing fast, must return the chunk. Fails today at 502 ms.
- **Its control**, 4 ordinary peers, must still pass. Guards against a fix that
  works by disabling the budget.
- **A preferred black hole is bounded**: a provider that accepts the stream and
  never answers must end the flight at about `RetrieveChunkTimeout`, not hang.
  Note the test's own lag goroutine must observe the stream context or `goleak`
  will fail the package on the test's own leak rather than on anything in the
  code, which is what happened while prototyping this.
- **An ordinary black hole is unaffected**: with no hint, a peer that never
  answers must not change when the budget is spent.
- **Hint-less behavior is byte-identical**: a download with no preferred set
  spends the budget exactly as before. This must fail if `preferredInflight` is
  ever incremented outside the preferred dispatch.

## Rollout and rollback

No configuration, no migration, no on-disk change. Rollback is reverting the
merge commit.

## Upstream portability

**The defect is upstream's, and this fix does not repair the upstream case.**
Both halves matter.

Verified in `git show upstream/v2.8.2:pkg/retrieval/retrieval.go`:

- `for errorsLeft > 0 {` at line 198, with no in-flight test
- `if inflight == 0 {` at lines 214 and 233, guarding the other two exits
- `errorsLeft--` at line 281, unconditional
- `return nil, storage.ErrNotFound` at line **287**

Upstream has no preferred path, so its budget is spent on every failure and the
discard is reachable through ordinary multiplexed retrieval. This fix keys on
`preferredInflight`, which upstream does not have, so it closes the case this
fork can reach and leaves upstream's untouched. Closing upstream's would need
something like the broad change, which the Alternatives section shows is not
safe as drafted.

The issue carries `affects-upstream`. Per rule 11 that is a marker for a later
human decision and authorises no contact with ethersphere.

## Files

- `pkg/retrieval/retrieval.go`: the declaration near `:281`, the preferred
  dispatch at `:353-354`, the result arm at `:431`, and the guard at `:464`.
- `pkg/retrieval/preferred_test.go`, or a new test file alongside it.
- `docs/DIFFERENCES.md`: a new row for the behavior change, and the top-of-file
  "wasp described" commit and date. The existing `Wasp-Providers` row at `:157`
  needs its scope checked rather than rewritten: its guarantee that a hint-less
  download is unaffected still holds, and the new guard should be named there as
  a third path that is inside a test for preferred activity.
- `docs/UPSTREAM.md`: a row for #438, per rule 14, since the issue carries
  `affects-upstream` and the file has none.

Generated with help of AI.
