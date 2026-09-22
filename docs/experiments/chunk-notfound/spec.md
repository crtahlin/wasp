# A depleted peer walk is not a server error

Issue: [#440](https://github.com/crtahlin/wasp/issues/440).
Type: fix.

## Problem

`GET /chunks/{address}` answers **500** when retrieval runs out of peers to ask.
`GET /bzz` and `GET /bytes` answer **404** for the same condition.

Retrieval has two ways of giving up and they return different errors. Exhausting
the peer walk returns `topology.ErrNotFound`, whose text is "no peer found"
(`pkg/topology/topology.go:19`), from `pkg/retrieval/retrieval.go:374`. Spending
the origin error budget returns `storage.ErrNotFound` from `:489`.
`pkg/storer/netstore.go:107` passes either through unwrapped, and the chunk
handler maps only the second:

```go
// pkg/api/chunk.go:264-274
	chunk, err := s.storer.Download(cache).Get(r.Context(), address)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			jsonhttp.NotFound(w, "chunk not found")
			return
		}
		logger.Error(nil, "read chunk failed")
		jsonhttp.InternalServerError(w, "read chunk failed")
```

The download handler maps both:

```go
// pkg/api/bzz.go:785
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, topology.ErrNotFound) {
			jsonhttp.NotFound(w, nil)
```

So the two endpoints disagree about the same underlying condition.

### Why 500 is the wrong answer

"No peer could be found to ask for this chunk" is a statement about the network,
not about this node malfunctioning. A 500 tells a caller to retry against a
server it should treat as broken, and tells an operator to go looking in the
logs for a fault that did not happen. It also forces any client that talks to
both endpoints to special-case one of them.

### How it is reached

Whenever the peer walk is exhausted before the origin error budget is spent. A
node with few connected peers reaches it routinely, and a node with many reaches
it when a chunk's eligible peers are all skipped for that chunk.

It was observed while measuring [#438](https://github.com/crtahlin/wasp/issues/438):
a retrieval whose only holder accepted the stream and never answered ended with
"no peer found" after the walk completed. Through `/chunks` that is a 500.

Note that #438's merged change makes this exit **more** reachable for a hinted
download, because holding the error budget while a provider answers means the
walk is more often what ends the flight. It does not create the case.

## The change

Map `topology.ErrNotFound` to 404 at the chunk endpoint, exactly as `bzz.go`
already does:

```go
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, topology.ErrNotFound) {
```

One condition and one import.

### Why not change what retrieval returns

Considered and rejected. Making retrieval return a single error identity would
reach every caller of `RetrieveChunk`, not just this endpoint, and the two
identities carry a real distinction that a future diagnostic may want: one says
the search was abandoned for want of peers, the other that it spent its budget.
The defect is that one endpoint fails to translate them, not that they exist.

## What it costs

**A client that today sees 500 for this condition will see 404.** That is the
point of the change, and it is the only behavior an operator or a client can
observe.

Anything that treats 500 as "this node is unhealthy" stops firing on a condition
that was never about this node. Nothing that reads 404 as "the chunk is not
retrievable right now" is misled, because that is exactly what happened.

Genuine internal failures still answer 500: the change narrows what reaches the
`InternalServerError` arm by one error identity and leaves the rest.

`openapi/Swarm.yaml:1768-1773` already documents both 404 and 500 for this
endpoint without saying which condition produces which, so the published
contract stays true and needs no edit.

## Protocol impact

None. No wire message, no header, nothing in
`.github/protocol-freeze.lock`. This is an HTTP status code on one local
endpoint.

## Tests

In `pkg/api`, mutation checked: revert the change and confirm the test fails.

- **A retrieval that fails with `topology.ErrNotFound` gives 404.** Feasible
  with the existing harness: `mockstorer.NewWithChunkStore` takes a chunk store,
  so a stub whose `Get` returns that error wires straight in. This is the test
  that fails today.
- **A retrieval that fails with `storage.ErrNotFound` still gives 404**, so the
  existing behavior is pinned rather than assumed.
- **An unrelated error still gives 500.** This is the guard against a fix that
  maps everything to 404, and it must fail if the condition is replaced by an
  unconditional `NotFound`.

## Measurement

None on the bench. This is a status code on one endpoint with no timing or
throughput component, and rule 7 exists for claims about how a node performs.
The unit tests above are the evidence.

## Rollout and rollback

No configuration, no migration, no on-disk change. Rollback is reverting the
merge commit.

## Upstream portability

**The defect is upstream's and is verified there.**
`git show upstream/v2.8.2:pkg/api/chunk.go` lines 257-265 carry the same
mapping, with only `storage.ErrNotFound` reaching 404, and upstream's retrieval
returns `topology.ErrNotFound` from the same peer-depletion exit.

The issue carries `affects-upstream`. Per rule 11 that is a marker for a later
human decision and authorises no contact with ethersphere.

## Files

- `pkg/api/chunk.go`, the condition at `:266` and the `topology` import.
- `pkg/api/chunk_test.go`.
- `docs/DIFFERENCES.md`: a node answers a request differently from Bee, so the
  behavior table gains a row.
- `docs/UPSTREAM.md`: the #440 row gains its branch and merge commit.

Generated with help of AI.
