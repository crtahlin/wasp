# The same defect on a second endpoint

Issue: [#449](https://github.com/crtahlin/wasp/issues/449).
Type: fix.

This is [#440](https://github.com/crtahlin/wasp/issues/440) again, on
`POST /pins/{reference}`. That spec,
[chunk-notfound](../chunk-notfound/spec.md), carries the reasoning about why a
depleted peer walk is not a server error; this one covers what is different.

## Problem

`POST /pins/{reference}` answers **500** when retrieval runs out of peers to
ask, and **404** when it spends its origin error budget. Both mean the same
thing to a caller: the content could not be fetched.

The pin handler traverses the reference through the network getter, and
`pkg/traversal/traversal.go:61` wraps a failed fetch with `%w`, so both error
identities survive to the handler, which maps only one:

```go
// pkg/api/pin.go:115-121
	if err := errors.Join(err, errTraverse); err != nil {
		logger.Error(errors.Join(err, putter.Cleanup()), "pin collection failed")
		if errors.Is(err, storage.ErrNotFound) {
			jsonhttp.NotFound(w, "pin collection failed")
			return
		}
		jsonhttp.InternalServerError(w, "pin collection failed")
```

Reproduced with a chunk store whose `Get` returns a chosen error, through
`mockstorer.NewWithChunkStore`:

| error from the getter | status |
|---|---|
| `storage.ErrNotFound` | 404 |
| `topology.ErrNotFound` | **500**, chain `traversal: failed to get root chunk ...: no peer found` |

It was found by reviewing the #440 fix, which covers `GET /chunks` only.

## The change

The same one-line change as #440: add `topology.ErrNotFound` to the condition.

## Why not translate the two identities once instead

This is the second endpoint, so the question is fair and is settled here rather
than left open.

#440's spec rejected changing what retrieval returns, because it reaches every
caller of `RetrieveChunk` and the two identities carry a real distinction: one
says the search was abandoned for want of peers, the other that it spent its
budget. That reasoning still holds.

Translating at the storer boundary instead, in `pkg/storer/netstore.go:129`,
was considered for this spec and is also rejected, for a narrower reason: it
would erase the distinction for every caller of `Download().Get()` including the
ones that do not answer HTTP at all, and the two endpoints already fixed prove
the translation belongs where a status code is chosen, not where a chunk is
fetched. **If a third endpoint turns up, that judgement should be revisited
rather than repeated a third time**, and this paragraph is the note to the
person who finds it.

## A third instance, suspected and not reproduced

The ACT download middleware builds its loadsave on the same network getter
(`pkg/api/accesscontrol.go:136`) and its `default:` arm answers 500
(`:150-151`). `pkg/accesscontrol` translates `kvs.ErrNotFound` and
`manifest.ErrNotFound` into its own `ErrNotFound` (`access.go:114`,
`history.go:117`) but nothing translates a retrieval error, so a depleted walk
while fetching an ACT history chunk would reach the `default` arm.

**This is read from the code and has not been reproduced**, so per rule 11 it is
recorded without a marker and without an issue. What would justify one: a test
that drives the ACT path with a getter returning `topology.ErrNotFound` and
observes the status. It is narrower than the other two because it needs ACT
headers on the request.

Note it is also a different shape: that arm answers 500 for **both** identities,
so it is not fixed by adding one to a condition.

## What it costs

A client that today sees 500 for this condition will see 404. Genuine internal
failures still answer 500: the change narrows what reaches that arm by one
error identity.

`openapi/Swarm.yaml` documents 404 and 500 for this endpoint without tying
either to a condition, so the published contract stays true.

## Protocol impact

None. An HTTP status code on one local endpoint.

## Tests

In `pkg/api`, mutation checked, mirroring the three that #440 shipped:

- a traversal that fails with `topology.ErrNotFound` gives 404, the case that
  fails today;
- one that fails with `storage.ErrNotFound` still gives 404;
- an unrelated error still gives 500, which is the guard against a fix that maps
  everything to 404.

## Measurement

None. A status code has no timing component, and rule 7 is for claims about how
a node performs.

## Rollout and rollback

No configuration, no migration, no on-disk change. Rollback is reverting the
merge commit.

## Upstream portability

**Verified.** `git show upstream/v2.8.2:pkg/api/pin.go` lines 115-121 carry the
same mapping, and upstream's `traversal.go` wraps with `%w` in the same place.
`upstream/v2.8.2:pkg/api/bzz.go:771` already maps both identities, so the
disagreement between endpoints is Bee's own.

The issue carries `affects-upstream`, which per rule 11 is a marker for a later
human decision and authorises nothing else.

## Files

- `pkg/api/pin.go`, the condition at `:117` and the `topology` import.
- `pkg/api/pin_test.go`, or a new test file alongside it.
- `docs/DIFFERENCES.md`: the #440 row says this endpoint still answers 500, and
  that sentence becomes false.
- `docs/UPSTREAM.md`: the #449 row gains its branch and merge commit.

Generated with help of AI.
