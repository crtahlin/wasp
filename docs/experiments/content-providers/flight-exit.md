# Do not end a flight while a request is still outstanding

Issue: [#438](https://github.com/crtahlin/wasp/issues/438).
Type: fix.

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

The two other ways out of the same loop both check that nothing is in flight,
`if inflight == 0` at `:372` and at `:391`, each followed by a `continue` when
something is. The comment on the first says so in as many words: "there is still
an inflight request, wait for it's result" (`:376`). The loop's own convention
is that a flight does not end while a request is outstanding. **The budget exit
is the exception.**

When it is taken, `return nil, storage.ErrNotFound` runs at `:472` and the
deferred `close(quit)` fires. `retrieveChunk` selects on `quit` and discards the
delivery it was about to hand back.

In this fork the case is easy to reach through the preferred path. A preferred
peer is removed from the candidate list as soon as it is dispatched (`:353`),
and ordinary misses resume spending the budget once the list is empty (`:464`).
The provider is then the only peer holding the chunk, is still answering, and
the budget runs out underneath it.

## Evidence

A test against `main` at `bb7aee0d`. A provider holds the chunk and answers
after a 3 second lag. Ordinary peers hold nothing and fail at once. Nothing else
differs between the two rows.

| ordinary peers | result |
|---|---|
| 32, which is `maxOriginErrors` | fails after **502 ms**, `storage: not found` |
| 4, so the budget is not spent | succeeds after **3.001 s**, the chunk is returned |

502 ms is `preferredWait` plus the time for 32 ordinary peers to fail: dispatch
to the provider, list empties, wait 500 ms, work through the ordinary peers,
budget reaches zero, exit, discard.

The second row is the control, and it is what makes this a measurement rather
than a story. Same provider, same lag, same code. The only change is whether the
budget is exhausted, and the delivery survives and is used. That isolates the
cause to the budget exit rather than to anything about the provider or the lag.

## The change

End the flight when the budget is spent **and** nothing is outstanding:

```go
		for errorsLeft > 0 || inflight > 0 {
```

With the budget spent, no new work may start, or the loop would spend past its
own bound. So the three arms that begin work become conditional:

```go
			case <-preemptiveTicker:
				if errorsLeft > 0 {
					retry()
				}
			case <-preferredTimerC:
				preferredTimerC = nil
				if errorsLeft > 0 {
					retry()
				}
			case <-retryC:
				if errorsLeft <= 0 {
					continue // budget spent: drain what is outstanding, start nothing
				}
```

The result arm is unchanged. A delivery that arrives returns the chunk; a
failure decrements `inflight`, and when it reaches zero with no budget left the
loop ends exactly as it does today.

This terminates. Once `errorsLeft` is zero, `inflight` only decreases, because
nothing dispatches. It cannot spin, because the only arms that fire are the
guarded ones, which fall straight back to the select.

## What it costs

**A flight whose budget is spent can now last as long as its slowest
outstanding request.** That tail is bounded, and not only by the request
context: every request carries `RetrieveChunkTimeout`, 30 seconds, applied
inside `retrieveChunk` at `:507`. So the worst case is one such timeout after
the budget is spent.

That is the honest cost and it should not be understated. The mitigating fact is
that **it is the behavior the other two exits already have.** A flight that
runs out of peers, or fails peer selection, already waits for outstanding
results rather than abandoning them, with the same 30 second bound. This change
makes the third exit agree with them rather than introducing a new hazard.

Nothing changes for a chunk no peer is answering: with `inflight == 0` the exit
condition is exactly as before, so a genuinely missing chunk fails in the same
time it does today. That is the claim measurement item 3 below exists to check.

There is no new cost to other operators. The change starts no additional
requests. It only stops discarding an answer to one already sent, so outbound
request volume is unchanged or slightly lower, since a discarded delivery today
is work already paid for and thrown away.

## Protocol impact

None. No wire message, no header, no constant in
`.github/protocol-freeze.lock`. The change is inside one requester-side loop.

## Measurement

Rule 7 applies: three runs per condition, spread reported, arms interleaved,
node state matched. The acceptance criterion is delivered bytes with a matching
checksum, not a counter.

1. **The reproducer passes.** The 32 ordinary peer row above returns the chunk
   after about the lag instead of failing at 502 ms.
2. **No regression on content the network holds.** A network-held download, with
   and without a hint, stays within the spread of the control on the unmodified
   build. This is the arm that rejected `fix/392-wait-for-credit`.
3. **A chunk nobody holds still fails in the same time.** With every peer
   failing fast and nothing outstanding, the time to `storage: not found` must
   be within the spread of the control. This is the cost claim above, measured
   rather than argued.
4. **Sole-source download rate is not worse.** The six downloads recorded in
   [dial-race.md](dial-race.md) at 2.19 to 2.96 MB/s are the baseline.

Counters on every run: `bee_retrieval_preferred_attempts`, `preferred_hits`,
`bee_retrieval_request_failure_count`, `request_success_count`,
`total_retrieved`.

## Tests

In `pkg/retrieval`, each checked by mutation: revert the change and confirm the
test fails. A test that passes both ways is this repository's usual failure
mode, so this is not optional.

- **The reproducer**, as above: a slow provider holding the chunk, exactly
  `maxOriginErrors` ordinary peers failing fast, must return the chunk. Fails
  today at 502 ms.
- **Its control**, with too few ordinary peers to spend the budget, must still
  pass. This one passes today and guards against a fix that works by breaking
  the budget entirely.
- **A chunk nobody holds still fails**, and does not wait for a timeout, when
  nothing is outstanding.
- **Nothing new is dispatched once the budget is spent.** Count the requests the
  streamer sees and assert it does not exceed the count before the change. This
  is the guard on the three conditional arms, and it must fail if any of the
  `errorsLeft > 0` tests is omitted.

## Rollout and rollback

No configuration, no migration, no on-disk change. Rollback is reverting the
merge commit.

## Upstream portability

**This defect is upstream's and is verified there, not assumed.**
`git show upstream/v2.8.2:pkg/retrieval/retrieval.go` has the same shape:

- `for errorsLeft > 0 {` at line 198, with no in-flight test
- `if inflight == 0 {` at lines 214 and 233, guarding the other two exits
- `errorsLeft--` at line 281, unconditional
- `return nil, storage.ErrNotFound` at line 286

Upstream has no preferred path, so the `len(candidates) == 0` guard this fork
added at `:464` is absent and the budget is spent on every failure. The discard
is reachable there through ordinary multiplexed retrieval, where an origin
request has several requests in flight at once, rather than through a hint.

The issue carries `affects-upstream`. Per rule 11 that label is a marker for a
later human decision and nothing more, and authorises no contact with
ethersphere.

## Files

- `pkg/retrieval/retrieval.go`, the loop condition at `:289` and the three arms
  that begin work.
- `pkg/retrieval/preferred_test.go`, or a new test file alongside it.
- `docs/DIFFERENCES.md`, since a node's retrieval behavior changes compared
  with Bee: a flight no longer discards a delivery that is already arriving.

Generated with help of AI.
