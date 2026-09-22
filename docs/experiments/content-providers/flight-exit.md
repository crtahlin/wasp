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

The loop condition is untouched. The exit *conditions* are therefore unchanged,
but which exit a flight leaves through can change, and so can the error the
caller sees. That is covered under "What it costs" and is not a detail.

### Why this terminates, in two lines

`candidates` never grows: `:340` rotates it, `:348` and `:353` shrink it. The
decrement at `:464` requires `len(candidates) == 0`, and a preferred dispatch
requires `len(candidates) > 0`. So once the budget has been spent even once, no
further preferred dispatch is possible, and `preferredInflight` can only fall to
zero and never rise again.

It follows that `errorsLeft` can never reach zero while a preferred request is
outstanding, which makes the `storage.ErrNotFound` exit at `:472` unreachable
with a delivery in flight. That is the whole defect, closed by construction
rather than by timing.

The loop does not spin while it waits: once peers deplete, `:376` continues
without calling `retry()` and the loop idles on `preemptiveTicker` at 1 Hz.

### Correction, at implementation: the span hoist below is NOT done

> Review of the implementation showed the hoist is a bad trade and it was
> dropped. `safe.Go` **recovers** a panic at the top of the goroutine. Moving
> the tracer call outside it does not make that panic harmless, it moves it into
> the flight loop, which runs in a `singleflight` goroutine with no recover
> anywhere above it, so an unrecovered panic there ends the process. The stall
> it was meant to prevent is also milder than stated below: `singleflight`
> cancels the flight context once the last caller has gone, so a lost report
> leaks a goroutine for the caller's lifetime rather than forever. Trading a
> bounded leak for a crash is the wrong way round. The section below is kept
> because the window it describes is real and someone will propose this again.
>
> Which test items were implemented is recorded in the pull request rather than
> guessed at here: the preferred black hole bound and the ordinary black hole
> were not written, and the request count is asserted rather than only logged,
> which reverses what this spec asked for and is the better call.

### One window has to be closed for the counter to be safe

`preferredInflight` is only sound if every dispatch eventually reports. It does,
because `retrieveChunk` sends from a `defer` (`:492-505`), with one exception:
the goroutine runs tracer code **before** entering it, and `safe.Go` recovers a
panic at the top of the goroutine, so a failure there reports nothing at all.

```go
	safe.Go(s.logger, "retrieval-retrieve-preferred", func() {
		span, _, ctx := s.tracer.FollowSpanFromContext(spanCtx, ...)
		defer span.End()
		s.retrieveChunk(ctx, quit, chunkAddr, peer, resultC, action, span, localOnlyHeaders(), true)
	})
```

That window is narrow and it is not new, but this change makes it consequential.
Today a lost report leaves `inflight` high, and the two `inflight == 0` exits
already stall on it. Add the budget guard and `errorsLeft` stops falling too, so
nothing ends the flight, and the flight context carries no deadline:
`singleflight` builds it as `context.WithCancel(withoutCancel(ctx))`, which
strips the deadline along with the cancellation.

So the change includes hoisting the span out of the goroutine, leaving
`retrieveChunk` as the only statement inside it:

```go
	span, _, ctx := s.tracer.FollowSpanFromContext(spanCtx, ...)
	safe.Go(s.logger, "retrieval-retrieve-preferred", func() {
		defer span.End()
		s.retrieveChunk(ctx, quit, chunkAddr, peer, resultC, action, span, localOnlyHeaders(), true)
	})
```

Three lines, no behavior change on any path that does not panic, and it removes
the only way the counter can be left high inside a live flight. The equivalent
window on the ordinary dispatch at `:417` is left alone: it is pre-existing, it
is not made worse here, and widening this change to cover it would be the kind
of scope creep the alternatives section already rejected once.

### It is provably inert without a hint

`preferredPeers` is only populated for an origin request with providers on
(`:200-204`, guarded by `if origin && s.providers.Load()` at `:200`), a
preferred peer is only dispatched from a non-empty candidate list, and
`preferredInflight` only moves on that dispatch. So on a forwarder, and on any
origin download carrying
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
- The whole of `pkg/retrieval` passes, and passes under `-race`, with the span
  hoist applied as well as the guard.

## What it costs

**A hinted download can now wait for a provider that accepts the stream and
never answers.** That is the real cost and it is bounded, measured rather than
argued: with a provider whose lag exceeds the per-request timeout, the call
returned after **30.001 s** with `no peer found`, which is
`RetrieveChunkTimeout` (`:157`, 30 seconds) applied inside `retrieveChunk` at
`:507`. One such timeout, once, at the end of a flight.

That bound is **per chunk**, not per download. A flight is one chunk, so a
download whose provider stops answering pays it on each chunk still in flight,
in parallel.

Two limits keep it narrow:

- It is reachable only on a download that named a provider or discovered one.
  Everything else is inert, as above.
- It replaces a failure. The download that pays this today returns nothing at
  all, so the comparison is 30 seconds against a wrong answer, not against a
  fast right one.

### It does raise outbound requests, and other operators pay for that

An earlier draft of this section claimed "no new cost to other operators". **That
was wrong** and is withdrawn. It reasoned only about forwarders.

Keeping the budget unspent keeps the loop alive, and `retry()` at `:468` keeps
dispatching. `closestPeer` with `origin` true returns early at `:603-605`
without the "closer than me" filter, and each peer asked is skipped forever for
that chunk at `:415`, so the walk continues across the **whole connected set**
instead of stopping at `maxOriginErrors`. It fires whenever a provider takes
longer than `preferredWait` to answer and the ordinary peers all miss, which is
the sole-source hinted download this change exists to fix.

This is the same amplification `docs/DIFFERENCES.md:157` already records for the
existing `len(candidates) == 0` guard, and it is explicit that the cost is not
ours:

> the number of ordinary peers a single chunk may be asked for is no longer
> capped at the error budget of 32 ... on a node with 150 peers that is roughly
> a fourfold rise in outbound retrieval requests for that chunk, and each one
> costs the peer that receives it a forward attempt into the network

The black-hole measurement above corroborates it rather than contradicting it,
and reading its error properly is what surfaced this: it returned
**`no peer found`**, which is `topology.ErrNotFound`, reachable only from `:374`
after every eligible peer has been asked and skipped. The sweep happened. The
first draft quoted that result and did not read it.

Whether the sweep should be capped in its own right is an open question already
recorded against [#392](https://github.com/crtahlin/wasp/issues/392), and this
change makes it more pressing without settling it. Measurement item 2 below
exists to size it.

### The error a caller sees can change, and one endpoint turns 404 into 500

Because the flight now leaves through peer depletion rather than the budget, the
error changes from `storage.ErrNotFound` (`:472`) to `topology.ErrNotFound`
(`:374`). `pkg/storer/netstore.go:107` passes it through unwrapped, and
`pkg/api/chunk.go:264-274` maps only `storage.ErrNotFound` to 404:

```go
		if errors.Is(err, storage.ErrNotFound) {
			jsonhttp.NotFound(w, "chunk not found")
			return
		}
		jsonhttp.InternalServerError(w, "read chunk failed")
```

So a hinted `GET /chunks/{addr}` whose provider accepts the stream and never
answers returns **500 where it returns 404 today**. `pkg/api/bzz.go:785` maps
both, so `/bzz` and `/bytes` are unaffected.

This exit is already reachable today, on any download whose peers deplete before
its budget does, so the change widens an existing case rather than creating one.
That makes the API mapping its own defect rather than this one's, and it is
filed separately. This spec's obligation is to state the change, test for it,
and record it in `docs/DIFFERENCES.md`.

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
2. **Size the peer sweep.** This is the arm that matters most, because it is the
   cost other operators pay and it is currently only reasoned about. Record
   ordinary requests per chunk on a sole-source hinted download, before and
   after, and report the ratio rather than a pass or fail. If it is far above
   the fourfold `docs/DIFFERENCES.md:157` already quotes, the sweep needs a cap
   before this ships.
3. **Sole-source download rate not worse** than the six runs in
   [dial-race.md](dial-race.md), 2.19 to 2.96 MB/s.
4. **A reference nobody holds still fails quickly** with a hint naming a peer
   that does not have it, rather than waiting out a timeout per chunk.

Counters each run: `bee_retrieval_preferred_attempts`, `preferred_hits`,
`request_failure_count`, `request_success_count`, `total_retrieved`, and, for
item 2, `bee_retrieval_request_attempts` (the `totalRetrieveAttempts` histogram
observed at `:218`) and `bee_retrieval_peer_request_count`.

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
- **The ordinary request count per flight is asserted**, not just the outcome.
  That is the quantity this change moves, and no existing test in the package
  measures it, so nothing would catch the sweep growing.
- **The error identity is pinned.** A flight leaving through peer depletion
  returns `topology.ErrNotFound`, not `storage.ErrNotFound`. Asserting which one
  makes the 404-to-500 change on `GET /chunks` visible to a later reader rather
  than a surprise.

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
- `pkg/retrieval/preferred.go`: hoisting the span out of the dispatch goroutine
  at `:256-263`.
- `pkg/retrieval/preferred_test.go`, or a new test file alongside it.
- `docs/DIFFERENCES.md`: a new row covering both the retrieval change and the
  error a caller can now see, `topology.ErrNotFound` where it was
  `storage.ErrNotFound`, with the 404-to-500 effect on `GET /chunks` named
  explicitly. Also the top-of-file
  "wasp described" commit and date. The existing `Wasp-Providers` row at `:157`
  needs its scope checked rather than rewritten: its guarantee that a hint-less
  download is unaffected still holds, and the new guard should be named there as
  a third path that is inside a test for preferred activity.
- `docs/UPSTREAM.md`: a row for #438, per rule 14, since the issue carries
  `affects-upstream` and the file has none.

Generated with help of AI.
