# Announce in the same call as a local ingest

Issue: [#503](https://github.com/crtahlin/wasp/issues/503). Split out of
[#499](https://github.com/crtahlin/wasp/issues/499), where the operator
accepted it on 2026-09-25.
Type: feat.

## Problem

Serving never-stamped content by discovery takes two calls on the provider:
`POST /wasp/ingest`, then `POST /wasp/providers/{ref}` with
`Swarm-Postage-Batch-Id`. Content that is ingested but never announced has no
provider record, so a requester can reach its provider only if it already has
the provider's address. In the stampless handoff walkthrough on 2026-09-26 the
second call was the step most easily left out, and a missing announcement
shows up only later, on the requester, as a 404.

## Design

### The request

`POST /wasp/ingest` accepts an optional `Swarm-Postage-Batch-Id` header. When
it is present, the node announces the reference after the ingest succeeds, in
the same request, by calling the same `providers.Announce` the announce
endpoint calls. The batch stamps **only the provider record**, never the
content, exactly as the announce endpoint does today. Without the header,
nothing changes.

For a collection (`Swarm-Collection: true`) the reference announced is the
manifest root, which is the reference the response returns and the one a
download of the collection names.

### Refused before the body is read

A request that asks for an announcement which can never succeed is refused
before the body is read, so nothing is stored:

| Condition | Answer | Same as the announce endpoint |
|---|---|---|
| `providers-enable` is off | 403, "content providers are off" | yes |
| The node is not a full node | 400, "only a full node can announce content" | yes |
| `Swarm-Encrypt: true` | 400, "encrypted references cannot be announced" | yes, checked earlier because the reference is not known yet |
| A malformed batch ID | 400, from header validation | yes |

The local-ingest checks (`local-ingest-enable`, the limit) run as today.

### The response when the ingest succeeds

The ingest's own status is kept: 201 for new content, 200 for content already
held. The announcement's outcome is added to the body:

```json
{
  "reference": "…",
  "chunks": 12,
  "soleSource": true,
  "announced": true
}
```

When the announcement fails, `announced` is `false` and `announceError`
carries the same message the announce endpoint would give: "batch not usable
yet or does not exist", "batch with id not found", or "announce failed". The
node logs the failure as a warning. The content stays stored, and the caller
can announce later with `POST /wasp/providers/{ref}`. Failing the whole
request instead would report as failed an ingest that succeeded and that the
caller cannot easily undo.

Both fields are absent when no batch was given, so existing clients see the
same body as today.

A client that reads only the status code will not see a failed announcement.
That is the cost of keeping the ingest's status, and the reason `announced` is
a boolean at the top level of the body rather than nested.

### Content already held

A 200 duplicate is announced as well. `Announce` on a reference that is already
announced publishes the record for the current window again and saves the
announcement again, which is what a repeated `POST /wasp/providers/{ref}` does
today.

### Not changed

- The announce, withdraw, list and lookup endpoints.
- The provider record format and what it is stamped with.
- The pin check the announce endpoint makes. Ingested content is an ordinary
  pinning collection, so the check always passes here, and `Announce` is called
  directly rather than through the announce handler.

## Protocol impact

**None.** The record, its stamping and the path that publishes it are
unchanged. `make protocol-freeze` must report the surface unchanged.

## Tests

In `pkg/api`, mutation checked:

- **Ingest with a batch** answers 201 with `announced: true`, and the
  reference is in `GET /wasp/providers`.
- **Collection with a batch** announces the manifest root.
- **Duplicate ingest with a batch** answers 200 with `announced: true`.
- **Announcement fails** (unusable batch, unknown batch): 201, content stored,
  `announced: false`, `announceError` as listed above.
- **Refused before the body**: providers off, light node, encrypted, each with
  nothing stored and the status above.
- **No batch**: the body has neither field, and `Announce` is not called.

Mutations: skip the announce call; fail the request when the announcement
fails; announce with no batch given; drop the encrypted check.

## Measurement

On the bench, `stake-1` as provider on a build with the change, three runs
with fresh content:

- One call with a usable batch returns the reference and `announced: true`,
  the reference is listed in `GET /wasp/providers`, and a fresh requester
  finds the provider through `GET /wasp/providers/{ref}/lookup` and downloads
  the content with a plain GET.
- A batch that does not exist leaves the content ingested, answers 201 with
  `announced: false`, and a later `POST /wasp/providers/{ref}` with a usable
  batch announces it.

A negative result is a stored ingest reported as failed, or content stamped.
Checking that nothing was stamped: the batch's utilization rises by the record
chunk only, not by the content's chunk count.

## Rollout and rollback

No configuration. A client that does not send the header sees no change.
Rollback is reverting the merge.

## Upstream portability

Not applicable. Local ingest and provider records are this fork's own.

## Files

- `pkg/api/localingest.go`: the header, the early refusals, the announce
  call and the response fields.
- `pkg/api/localingest_test.go`: tests.
- `openapi/Swarm.yaml`: the header and the two response fields.
- `docs/DIFFERENCES.md`, `docs/experiments/INDEX.md`.
- The operator documentation in `docs/experiments/content-providers/operators.md`,
  which describes the two-call flow.

Generated with help of AI.
