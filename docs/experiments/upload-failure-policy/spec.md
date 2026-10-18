# Keep a direct upload's failed chunks, retry them, and report them

Issue: [#806](https://github.com/crtahlin/wasp/issues/806). This spec covers
phase 1 only. Phases 2 (erasure-coded uploads that succeed with a bounded,
reported loss) and 3 (reserve parities) are described in the issue and get
their own specs after phase 1 is measured.

Terms used throughout:

- **Direct upload**: an upload with `Swarm-Deferred-Upload: false` and no
  `Swarm-Pin`. The node pushes each chunk to the network while it reads the
  request body, and keeps nothing locally.
- **Deferred upload**: the default. The node stores the chunks locally first,
  answers, and the pusher pushes them afterwards.
- **Receipt**: the signed answer of the node that stored a pushed chunk.
- **Shallow receipt**: a receipt from a node whose address is too far from the
  chunk's address to be in the chunk's neighbourhood (`checkReceipt` in
  `pkg/pushsync`). It does not prove that the nodes responsible for the chunk
  have it.
- **Attempt**: one call of `PushChunkToClosest` for one chunk. Inside one
  attempt, push-sync already tries several peers, and gives up after 32
  failed sends (`maxPushErrors`).
- **Failed set**: the chunks of one upload that used up their attempts
  without a receipt from their neighbourhood.

## Problem

An operator's uploads see many shallow receipts, and long direct uploads fail
at the very end. Read on `origin/main` 82c67848f, in code that is unchanged
from Bee v2.8.2 (`git diff upstream/v2.8.2 origin/main` is empty for
`pkg/storer/netstore.go`, `pkg/pusher`, `pkg/api/bytes.go`, `pkg/api/tag.go`,
`pkg/steward` and `pkg/storer/internal/upload`):

1. **The first failed chunk ends the upload, but the client hears it last.**
   `DB.DirectUpload` (`pkg/storer/netstore.go:23-81`) starts one errgroup
   goroutine per chunk. A shallow receipt or "no peers"
   (`topology.ErrNotFound`) sends the chunk again (`:62-66`); any other error,
   for example `pushsync.ErrNoPush`, ends the goroutine and cancels the group.
   `Put` returns nil as soon as the goroutine is started (`:73`), so the
   splitter keeps reading, hashing and stamping the rest of the body, and the
   error appears only at `putter.Done`: `500 done split failed`
   (`pkg/api/bytes.go:144-150`, `pkg/api/bzz.go:327-333`). After the
   cancellation the remaining chunks are mostly not pushed, so the rest of the
   body goes by quickly, but the client still has to send all of it.
2. **Nothing is left to resume from.** The root reference is computed by then
   but not returned. A direct upload has no tag (`bytes.go:59`, `bzz.go:95`
   create one only for deferred or pinned uploads), so nothing records what
   was pushed. `PUT /stewardship/{ref}` needs every chunk locally
   (`steward.Reupload`), which a direct upload never has. The client sends the
   whole body again, and the node pushes again, and pays push-sync fees again
   for, every chunk that had already arrived. Postage is not lost when the
   client re-uses the same batch: the stamper re-uses a chunk's slot in a
   batch (`pkg/postage/stamper.go:47-56`).
3. **"No peers" is retried without any limit.** On a light node with no
   suitable peer, a direct upload waits forever (the pusher sleeps 5 s between
   tries, `pkg/pusher/pusher.go:137-142`).
4. **Six shallow receipts count as synced, silently.** The pusher counts
   shallow receipts per chunk (`attempts`, `pkg/pusher/inflight.go:50-71`).
   At the sixth (`DefaultRetryCount = 6`, `pusher.go:68`), `pushDirect`
   returns nil (`:336-341`) and `pushDeferred` reports the chunk as synced
   (`:282-289`), although no receipt from the neighbourhood arrived. No
   counter, tag field or log line above debug level reports it. The count is
   kept per chunk address across all uploads and is cleared only on that sixth
   receipt, so a chunk that got some shallow receipts in an earlier upload
   reaches six sooner in the next.
5. **A missing batch is reported as success in a direct upload** (reasoned from
   the code, not yet observed). In `pushDirect`, `ok, err := s.batchExist.Exists(...)`
   re-uses the function's `err`; when the batch does not exist, `ok` is false
   and `err` is nil, the function returns nil (`pusher.go:317-326`), and the
   deferred send puts nil on `op.Err`. The chunk is not pushed and the upload
   reports 201. Upstream v2.8.2 has the same lines. Phase 1 adds a test that
   settles this; the issue gets the `affects-upstream` label only if the test
   confirms it (rule 11).
6. **A chunk already in flight is reported as delivered.** When the same
   chunk with the same batch is already being pushed, the pusher answers a
   direct upload's request with nil at once (`pusher.go:227-235`). Within one
   upload this is harmless, because the first push's error still reaches the
   same errgroup. Across two concurrent uploads, the second one reports
   success for a chunk whose only push may still fail.
7. **Deferred uploads never give up and never report a loss.** `pushDeferred`
   retries every push error forever. A chunk whose batch has gone is dropped
   with a warning at log level 1 and counted nowhere (`pusher.go:257-266`,
   `uploadstore.Report` with `ChunkCouldNotSync`). The tag has no field for
   either case (`TagItem`, `pkg/storer/internal/upload/uploadstore.go:129-138`).

The bench harness notes record the effect from the client's side: a direct
upload answered 201 while the network held only part of the content, and the
download then stopped at 13.6 of 16.8 MB.

## Hypothesis

Most chunks that fail in a direct upload fail because, at that moment, this
node cannot reach the chunk's neighbourhood: the peers that would answer are
on push-sync's per-chunk skip list for 5 minutes after a failed send
(`skiplistDur`, `pkg/pushsync/pushsync.go:50`), out of credit, or not
connected. The same chunk is often pushable a few minutes later. So:

- giving each chunk a bounded number of attempts, and moving on, lets the
  other chunks of the upload continue;
- retrying the failed chunks after the body is read, at least 5 minutes after
  their last failure, recovers most of them;
- returning the root reference, the tag and the failed set lets the client
  finish the rest without re-sending the body.

The phase-1 measurement checks the mechanism with injected faults. Whether
real failures are transient enough for the final retry to recover them is a
separate question, answered only on a real network (see Measurement).

## Design

### 1. Policy

A new request header, `Swarm-Upload-Failure-Policy`, and a new node setting,
`upload-failure-policy`, with three values:

| Value | What a direct upload does |
|---|---|
| `retry-at-end` (default) | Each chunk gets a bounded number of attempts. A chunk that uses them up goes into the failed set, and the upload continues. After the body is read, the node retries the failed set for at most `upload-final-retry-duration`. It answers 201 if the set is then empty, and an error that names the reference, the tag and the remaining failures if not. |
| `fail-fast` | The same per-chunk attempts. The first chunk that uses them up ends the upload: the node stops reading the body and answers at once. |
| `bee` | Exactly today's code path, `DB.DirectUpload()` unchanged, as in Bee v2.8.2: no tag, no counters, no budget, the first error at the end. |

The header wins over the setting. An unknown value is refused with 400 at
the request, and at start for the setting. The policy applies to the direct
sessions created through `newStamperPutter` and `newStampedPutter`. The header
is read on `POST /bytes`, `POST /bzz` (single file and collection),
`POST /chunks` and `POST /soc/{owner}/{id}`; the other API paths that use a
direct session (`/chunks/stream`, feeds, access control) use the node setting.
For a deferred or pinned upload the header is ignored: those go through the
upload store and the pusher, covered by section 7.

**Why keep `bee`.** It gives the bench a control arm inside the same build,
so a comparison measures the policy and not two binaries. It also gives an
operator a way back to Bee's behaviour without a rollback. Its cost is one
extra branch in `newStamperPutter`; the code it selects is already there,
because `steward.Reupload` and the content providers keep calling
`DirectUpload()` in phase 1.

### 2. Per-chunk outcomes in a new direct session

A new method on the storer, beside `DirectUpload()`:

```go
// DirectUploadWithPolicy returns a direct upload session that keeps going
// when a chunk fails, records failed chunks under tagID, and retries them
// according to the policy.
DirectUploadWithPolicy(tagID uint64, opts FailureOptions) PutterSession
```

`DirectUpload()` is not changed, so `steward.Reupload` and the content
providers keep today's behaviour in phase 1.

Each `Put` starts one goroutine, under the same `directUploadLimiter` (128
chunks at a time, `pusher.ConcurrentPushes`), that runs the chunk's attempts:

- **Budget: 6 attempts per chunk** (`directChunkAttempts`). Six matches the
  pusher's shallow-receipt count today, so a chunk that only ever gets shallow
  receipts is treated the same number of times as now. Each attempt already
  tries up to 32 peers inside push-sync, so six attempts is up to 192 failed
  sends for one chunk.
- **Backoff between attempts: 1, 2, 4, 8 and 16 s**, 31 s in total. That gives
  the connection manager and the credit refresh (600 ms,
  `overDraftRefresh`) time to act, without holding a slot of the 128 for
  long. The goroutine keeps its limiter slot while it waits. That is
  deliberate: if many chunks fail, the upload slows down instead of starting
  more pushes into a network that is not taking them.
- **What counts as a failed attempt:** `ErrShallowReceipt`,
  `topology.ErrNotFound`, `ErrNoPush`, `ErrWarmup` and any other push error.
  "No peers" now counts against the budget, which ends the endless wait of
  problem 3.
- **What ends the chunk at once, without the budget:** the batch does not
  exist or is not usable (the whole upload then fails, whatever the policy,
  with 422 "batch not usable", because no further chunk can be pushed with
  it); the node shuts down (`ErrDBQuit`); the request is cancelled before the
  body is read (section 5).
- **The six-shallow rule** keeps Bee's default and becomes visible: when every
  attempt of a chunk ended with a shallow receipt, and
  `upload-shallow-receipt-policy` is `accept` (default), the chunk counts as
  synced and the tag's `shallow` counter goes up. With `fail`, the chunk goes
  into the failed set. A chunk whose attempts ended in a mix of shallow
  receipts and other errors goes into the failed set under either policy.
- **Duplicates.** Within one session, a second `Put` of an address already in
  progress waits for the first one's outcome and does not push again (a file
  with repeated content produces such duplicates). Across sessions, the new
  session asks the pusher for a strict answer (a new `Op.Strict` field): the
  pusher then answers `pusher.ErrInFlight` instead of nil for a chunk already
  being pushed. The session waits the next backoff and asks again; this does
  not count as an attempt, and is bounded by the policy's time limits. That
  ends problem 6 for the new path.
- **Shallow receipts are counted per session**, not in the pusher's global
  map: for a strict op, `pushDirect` returns every `ErrShallowReceipt` as it
  is, and the session counts. The global map stays for `DirectUpload()` and
  for deferred uploads.
- **The missing batch.** For a strict op, `pushDirect` returns
  `postage.ErrNotFound` when the batch does not exist, instead of nil. The
  `bee` path is left as it is.

A chunk that uses up its budget is written to the failed-chunk store
(section 3) and counted as failed. Then:

- with `retry-at-end`, the goroutine ends and releases its slot; the upload
  continues;
- with `fail-fast`, the session is marked failed. Every later `Put`, including
  one waiting for a limiter slot, returns `ErrUploadFailed` at once.

### 3. The failed-chunk store

Failed chunks are kept in the index store, in a new namespace,
`uploadFailedChunk`, one item per chunk:

| Field | Contents |
|---|---|
| key | tag ID, chunk address |
| `Data` | the chunk's span and data, at most 4,104 bytes |
| `Stamp` | the stamp the chunk was uploaded with (113 bytes marshalled) |
| `Reason` | the last error class: `no-push`, `no-peers`, `shallow`, `unconfirmed`, `other` |
| `Attempts` | attempts made so far, inline and in retries |
| `LastAttempt` | Unix time of the last attempt |

The data is in the item itself and not in the chunk store (sharky). An older
build that does not know the namespace then leaves only index-store entries
behind after a rollback, never a sharky slot with a reference count that
nothing releases.

- **Bound: 262,144 chunks for the whole node** (`maxFailedChunks`), about
  1.1 GB of index store when full. That is about 1 percent of the roughly
  25 million chunks of a 100 GB upload. When the store is full, an upload that needs to add a
  chunk fails at once, whatever the policy, with 507 "failed-chunk store is
  full; delete old tags". Existing failed sets are kept.
- **Removal:** when the chunk is pushed in a retry; on `DELETE /tags/{id}`,
  which removes the tag's failed set; and on a sweep at start and every hour
  that removes items whose batch no longer exists, because such a chunk can
  never be pushed with its stamp.
- **A retry re-uses the stored stamp.** It needs no new postage slot and no
  access to the batch's stamp issuer.

### 4. `retry-at-end`: the final pass, and the answer

When the body is read and the reference is known, `Done(ref)`:

1. waits for every chunk goroutine to finish (each is bounded by its budget);
2. if the failed set is empty, finishes as today;
3. otherwise runs the **final pass**: rounds over the failed set, one attempt
   per chunk per round, 128 at a time. A chunk is tried again only when its
   last attempt is at least 5 minutes old, the push-sync skip-list duration,
   so the peers that refused it are offered it again. The pass ends when the
   set is empty or after `upload-final-retry-duration` (default 30 minutes,
   so 6 rounds), whichever is first.

The final pass runs under a context derived from the request with
`context.WithoutCancel` and bounded by the duration. A client that
disconnects during the pass does not stop it; the result is in the tag. A
client whose request timeout is shorter than the final pass should create the
tag first (`POST /tags`), send it in `Swarm-Tag`, and read the outcome there.

The answer:

- **Success:** 201 with the reference as today, plus the headers
  `Swarm-Failed-Chunks: 0` and `Swarm-Shallow-Chunks: <n>`. If any chunk
  was in the failed set during the upload, the tag is kept and returned in
  `Swarm-Tag`; otherwise a tag the node created for the upload is deleted, so
  a successful upload leaves nothing behind, as today.
- **Failure:** 500, the status a client sees today, with the headers
  `Swarm-Tag`, `Swarm-Failed-Chunks` and `Swarm-Shallow-Chunks`, and this body:

```json
{
  "code": 500,
  "message": "upload incomplete: 12 chunks not delivered",
  "reference": "<root reference>",
  "tag": 42,
  "failed": 12
}
```

The reference is the one a successful upload would have returned: the
splitter has computed it, and nothing about the content changes. Retrieving
it before the retry succeeds may fail on the missing chunks.

On this error path the handler must not run `putter.Cleanup` through
`cleanupOnErrWriter`, which would delete the failed set. The new session's
`Cleanup` keeps the failed set whenever the session has a reference, and
removes it only when the upload ended before the reference existed (the
client went away during the body; nothing could be resumed then).

### 5. `fail-fast`: the early answer

When the session is marked failed, the next `Put` returns `ErrUploadFailed`.
The error goes up through the pipeline's store writer
(`pkg/file/pipeline/store/store.go:34`), the splitter stops, and the handler
answers on its existing error path, before the body is read. No change to
the HTTP server is needed.

Details:

- **Status 502**, with the same body as section 4 without `reference`, which
  does not exist yet, and with the headers `Swarm-Tag` and
  `Swarm-Failed-Chunks`. 502 tells the client that the node could not deliver
  to the network, which is different from a fault of the node itself.
- **`cleanupOnErrWriter`** calls `Cleanup` when the error status is written,
  before the header goes out. Today that waits for every chunk goroutine
  (`eg.Wait()`). The new session's `Cleanup` does not wait for pushes: it
  cancels the session context, so each goroutine stops waiting for its push
  at once. The pusher's own push of a chunk goes on under the pusher's context
  and its result is discarded (`op.Err` has a buffer of one, so the pusher
  never blocks). A chunk whose outcome was not known goes into the failed
  set with reason `unconfirmed`; the session still holds its data in memory.
  Writing at most 128 items locally keeps the answer within seconds of the
  failure.
- **The rest of the body.** The handler returns without reading the rest.
  Go's HTTP/1.1 server then discards at most 256 KiB of the unread body and
  closes the connection after the response. A client that reads the response
  while it is still sending, such as curl, sees the 502 and stops sending. A
  client that only reads after sending everything may see a reset connection
  instead of the answer. For such clients the spec recommends creating the tag
  first (`POST /tags`) and sending it in `Swarm-Tag`: after a broken
  connection, `GET /tags/{id}` shows `state: failed` and the counters. The
  bench measures both kinds of client.
- **The failed set after `fail-fast`** is kept until `DELETE /tags/{id}` or
  the batch sweep, so that `GET /tags/{id}/failed` can show what failed and
  why. `POST /tags/{id}/retry` refuses it with 409: the upload stopped before
  the reference existed, so retrying those chunks would not complete
  anything. Re-sending the body with the same batch costs no new postage slots.

There is no failure-rate rule ("more than N failures in a minute") in phase 1.
With 6 attempts and 31 s of backoff, the first chunk that meets a network that
takes nothing uses up its budget in well under a minute when push-sync fails
fast, so a rate rule would rarely answer sooner and would add two more
tuning values. The bench records the time from the fault to the answer; if it
is too long, the rate rule is the follow-up.

### 6. Tags: a tag for every direct upload, and three counters

A direct upload under `retry-at-end` or `fail-fast` gets a tag: the one in
`Swarm-Tag` if given, otherwise a new one (section 4 says when a new one is
deleted again). The session keeps the tag's counters in memory and writes them
at most once per second and at the end.

The tag response gains four fields:

| Field | Meaning |
|---|---|
| `failed` | Chunks in the tag's failed set now. Falls as retries succeed. |
| `retrying` | Chunks that had at least one failed attempt and are being tried now, inline or in a retry. Direct uploads only in phase 1 (see section 7). |
| `shallow` | Chunks counted as synced only by the six-shallow rule. |
| `state` | `uploading`, `retrying`, `done` or `failed`, derived when the tag is read (below). |

`state` is derived, not stored: `uploading` while the tag has no address;
`retrying` while `retrying` is above 0; `failed` when the address is set and
`failed` is above 0 with no chunk left to try; `done` when the address is set
and `synced + seen` equals `split`.

For a direct upload, the session also fills `split` (chunks put), `sent`
(chunks with at least one attempt) and `synced` (chunks with a receipt, or
accepted as shallow), so the tag shows progress as it does for a deferred
upload. `stored` counts chunks the node kept itself because it is closest
(`ErrWantSelf`), as in a deferred upload.

The counters `failed`, `retrying` and `shallow` are stored in a new
namespace, `tagFailureItem`, keyed by tag ID. `TagItem` is not changed: its
marshalled size is fixed (`tagItemSize`), so a new field there would need a
migration, and an older build would refuse the item.

**New endpoints:**

- `GET /tags/{id}/failed?offset=0&limit=1000`: the failed set, ordered by
  chunk address. `limit` is at most 10,000. Each entry has `address`,
  `reason`, `attempts` and `lastAttempt`; the response also has `total`. 404
  for an unknown tag.
- `POST /tags/{id}/retry`: starts one retry of the tag's failed set in the
  background, with the final-pass rules of section 4 and the same time bound,
  and answers 202 with the tag. Progress is in `GET /tags/{id}`. 409 while a
  retry of the same tag is running, or when the tag has no reference
  (`fail-fast`); 404 for an unknown tag. When the set is empty at the end,
  the tag's `state` is `done`.

A retry is on the tag and not on `PUT /stewardship/{ref}` (the
`only-failed=true` option considered in the plan): stewardship takes a
reference and expects every chunk locally, while the failed set belongs to a
tag and holds only the missing chunks.

### 7. The pusher and deferred uploads

The changes in `pkg/pusher`, for deferred uploads and for strict direct ops:

- **Shallow rule.** At the sixth shallow receipt of a deferred chunk, with
  `upload-shallow-receipt-policy: accept`, the chunk is reported as synced, as
  in Bee, and the tag's `shallow` counter goes up. With `fail`, the chunk goes
  into the tag's failed set and leaves the upload store.
- **Missing batch.** As today the chunk is dropped, because it can never be
  pushed with its stamp, but the tag's `failed` counter goes up and the
  address goes into the failed set with reason `batch-gone` and no data. The
  warning moves from log level 1 to the default level.
- **Give-up.** A new setting, `deferred-upload-give-up-after`, 0 by default,
  which means never, as in Bee. When it is above 0, a push that fails for a
  chunk whose upload item is older than that (the existing `Uploaded` time)
  moves the chunk into the tag's failed set with reason `given-up`, and
  removes it from the upload store. The tag can then say `done` or `failed`
  instead of staying unfinished. `POST /tags/{id}/retry` works on such a set
  as on a direct upload's.
- **`retrying` for deferred uploads stays 0 in phase 1.** Counting chunks
  with a failed attempt needs a mark on each upload item, which is a change of
  its fixed on-disk format and so a migration. Progress of a deferred upload
  stays `split - seen - synced - failed`, as clients compute it now.

The new storer call the pusher uses is `ReportFailed(ctx, chunk, reason)`,
beside `Report`, so the existing `ChunkState` values keep their meaning.

### 8. Metrics and log lines

- `bee_storer_direct_upload_attempts_total{result="synced|shallow|failed|in_flight"}`
- `bee_storer_failed_chunks`, a gauge of items in the failed-chunk store
- `bee_pusher_shallow_accepted_total`, chunks counted as synced by the
  six-shallow rule, direct and deferred
- `bee_pusher_deferred_given_up_total`
- A warning when an upload ends with failed chunks: tag, count, reference.
  A warning when the failed-chunk store is full. No peer addresses.

### Code, tests and documents that change with it

- `pkg/storer`: `DirectUploadWithPolicy`, the failed-chunk store, the tag
  failure item, `ReportFailed`, the hourly sweep.
- `pkg/pusher`: `Op.Strict`, `ErrInFlight`, the shallow policy, the give-up
  check, the missing-batch error for strict ops.
- `pkg/api`: the header, the two endpoints, the tag fields, the answer
  bodies, the policy branch in `newStamperPutter` and `newStampedPutter`, and
  the handlers' error paths (sections 4 and 5).
- `cmd/bee/cmd` and `pkg/node`: the four settings.
- `pkg/storer/mock`: the new method.
- `openapi/Swarm.yaml`, `openapi/SwarmCommon.yaml`: below.
- `docs/config-reference.yaml` and `docs/DIFFERENCES.md`: below.

### OpenAPI changes

- **`SwarmCommon.yaml`:** a parameter `SwarmUploadFailurePolicy` (header
  `swarm-upload-failure-policy`, enum `retry-at-end`, `fail-fast`, `bee`, no
  default in the schema because the node setting decides); response headers
  `SwarmFailedChunks` and `SwarmShallowChunks`; `NewTagResponse` gains
  `failed`, `retrying`, `shallow` and `state`; new schemas
  `UploadIncompleteResponse` (`code`, `message`, `reference`, `tag`,
  `failed`) and `FailedChunksResponse` (`total`, `chunks` with `address`,
  `reason`, `attempts`, `lastAttempt`).
- **`Swarm.yaml`:** the parameter on `POST /bytes`, `POST /bzz`,
  `POST /chunks` and `POST /soc/{owner}/{id}`; the 201 headers; a 500
  response with `UploadIncompleteResponse` and a 502 for `fail-fast`; 507 for
  a full failed-chunk store; the paths `/tags/{id}/failed` (GET) and
  `/tags/{id}/retry` (POST). Each affected endpoint notes the divergence from
  Bee, as the API description requires.
- A minor version step in each file: the changes only add fields, values and
  paths.

### Not changed

- `DB.DirectUpload()`, and so `steward.Reupload` and the content providers.
  Moving them to the new session is a follow-up once phase 1 is measured.
- Push-sync: `maxPushErrors`, `skiplistDur`, the shallow-receipt tolerance,
  the receipt check.
- The redundancy pipeline and every reference: the same body with the same
  settings gives the same reference under every policy.
- Pinned direct uploads, which already go through the upload store.

## Protocol impact

None. Nothing under `pkg/p2p/`, `pkg/swarm/` or `pkg/config/` changes, and no
constant in `.github/protocol-freeze.lock` is touched. No push-sync message
changes. The node sends the same push-sync requests, with the same chunks and
stamps; only how often and when it sends a chunk again changes. A stock peer
cannot tell a retry from a first push. `make protocol-freeze` must stay clean.

What peers do see is more pushes of failing chunks. Today a chunk that fails
with `ErrNoPush` gets one attempt in a direct upload; under `retry-at-end` it
gets up to 6 inline plus up to 6 in the final pass. Each push is paid for by
this node through accounting, and the number is bounded by the failed set,
not by the file size.

## Measurement

### Unit tests (required for merge)

Each with a mutation check: break the behaviour, confirm the test fails with a
message, not a hang (`experiment-lifecycle.md`, "Mutation checks").

- **Budget:** a chunk whose push always fails gets exactly 6 attempts with the
  backoff schedule (an injected clock, no sleeping); one that succeeds at the
  fourth attempt is synced and not failed. Mutations: no limit; limit off by
  one; no backoff.
- **Other chunks continue:** in `retry-at-end`, one permanently failing chunk
  among 1,000 does not stop the others; every other chunk is pushed. Mutation:
  cancel the session on the first failure.
- **Failed set and store:** the failed chunk's data and stamp are stored and
  read back identical; removed on a successful retry, on `DELETE /tags/{id}`
  and by the sweep for a missing batch; the node-wide bound refuses with 507.
  Mutations: skip the delete; skip the bound check.
- **Final pass:** a chunk that fails 6 times and then succeeds is pushed in
  the final pass and the upload answers 201; a chunk is not retried within
  5 minutes of its last attempt; the pass stops at the duration; a client
  disconnect does not stop it. Mutations: retry at once; no time bound; pass
  under the request context.
- **Answers:** the failure body carries the reference the success path would
  return (compared with a fault-free run of the same body), the tag, and the
  count; the failed set survives the error path (the `cleanupOnErrWriter`
  case). Mutation: call the old `Cleanup`.
- **`fail-fast`:** with a fake push that fails every chunk after chunk 100 of
  a 100,000-chunk body, the handler answers 502 after reading less than
  2 percent of the body; in-flight chunks are in the failed set as
  `unconfirmed`. Mutation: `Put` keeps returning nil after the session failed.
- **`bee`:** the same body and fault as Bee v2.8.2 give the same status and
  message, and no tag. Mutation: route `bee` to the new session.
- **Shallow:** six shallow receipts give `synced` and `shallow: 1` under
  `accept`, and a failed entry under `fail`, for direct and deferred.
  Mutations: swap the policies; drop the counter.
- **Missing batch (problem 5):** first, a test on the unchanged `pushDirect`
  that shows whether a missing batch is reported as success; this settles the
  upstream question. Then: a strict op returns `postage.ErrNotFound`, and the
  upload answers 422.
- **In flight (problem 6):** two sessions put the same chunk; the second gets
  `ErrInFlight`, not nil, and reports synced only after a receipt.
- **Deferred give-up:** at 0 nothing gives up; above 0 a failing chunk older
  than the setting moves to the failed set and the tag says `failed`.
- **Tags:** the four fields, the derived `state`, and an older tag with no
  failure item reads as zeros.
- `-race` on `pkg/storer`, `pkg/pusher` and `pkg/api`.

### Bench (after merge, before the result is recorded)

On the bench-1 host only, one timed test at a time, the rchash launch agent
paused as the bench rules require. The faulting build never runs on a staked
node: the uploads come from a throwaway full node on that host, on the test
network, with a fresh data directory and its own batch and chequebook,
removed afterwards.

**Fault injection.** A wrapper around the push syncer, compiled only with the
build tag `faultinject` and never part of a release build. It reads one
environment variable and selects chunks by a hash of their address, so a run
can be repeated exactly:

- `mode=nopush`: the attempt returns `ErrNoPush`;
- `mode=shallow`: it returns `ErrShallowReceipt`;
- `mode=notfound`: it returns `topology.ErrNotFound`;
- `fraction=<f>` or `count=<n>`: which chunks;
- `fail-attempts=<k>`: fail only the first k attempts of a chunk, then pass
  it through (a transient fault); without it, the fault is permanent;
- `until=<duration>`: no faults after this time from start (a fault that
  ends, for the retry endpoint).

**Arms.** File sizes: 256 MiB (65,536 data chunks) for the functional arms,
2 GiB for the timing arms. Three runs per condition. Every upload is checked
afterwards with `GET /stewardship/{ref}` and a download compared by checksum,
because a 201 alone is not proof.

| Arm | Policy | Fault | Pass rule (fixed now) |
|---|---|---|---|
| A1 control | `bee` | 50 chunks, `nopush`, permanent | 500 `done split failed` after the whole body, no reference, no tag: today's behaviour reproduced. |
| A2 transient | `retry-at-end` | 0.1 percent, `nopush`, `fail-attempts=8` | 201 in every run; `failed: 0`; at least one chunk recovered in the final pass (log and tag show `retrying` above 0 during the run); content retrievable and checksum equal. |
| A3 permanent, then resumed | `retry-at-end`, `upload-final-retry-duration: 10m` | 50 chunks, `nopush`, `until` after the final pass | 500 with the reference of a fault-free upload of the same file, the tag, and `failed: 50`; `GET /tags/{id}/failed` lists exactly those 50 addresses; after `until`, `POST /tags/{id}/retry` gives `state: done`; then retrievable and checksum equal. |
| A4 fail-fast | `fail-fast` | from 10 percent of the body on, every chunk, `nopush` | Answer (502) within 10 s of the node's first "budget used up" log line in every run; the request ends before 25 percent of the body is sent; tested with curl and with a Go `net/http` client, and the result for each client recorded. |
| A5 overhead | `retry-at-end` against `bee`, alternating | none | Median upload time of `retry-at-end` within the run spread of `bee`, and at most 5 percent above it. |
| A6 shallow | `retry-at-end`, shallow policy `accept`, then `fail` | 50 chunks, `shallow`, permanent | `accept`: 201 and `shallow: 50`; `fail`: 500 and `failed: 50`. |
| A7 no peers | `retry-at-end` | every chunk, `notfound`, for 2 minutes | The upload does not wait forever: it ends, with 201 if `until` falls before the final pass ends, otherwise with 500 and the failed set. |
| A8 deferred give-up | deferred, `deferred-upload-give-up-after: 10m` | 50 chunks, `nopush`, permanent | Tag `state: failed`, `failed: 50` after 10 to 15 minutes; with the setting at 0 the same run still retries after 30 minutes (control). |

**A negative result** is any of: an arm that misses its pass rule; overhead
above 5 percent; a client in A4 that never learns the outcome even through
the tag; a reference in A3 that differs from the fault-free one.

**What the bench does not show.** Injected faults show that the mechanism
works. They do not show that real failures are transient enough for the final
pass to recover them, which is the hypothesis behind the 30-minute default.
That needs counts from real uploads: final-pass recoveries against chunks
still failed, per upload. Collecting them on a production node is the
operator's decision and not part of this spec's pass rules.

## Rollout and rollback

- **On upgrade**, direct uploads use `retry-at-end`. An upload with no
  failure behaves as before, apart from a tag that exists while it runs, the
  two new response headers, and a 422 instead of a false 201 when the batch is
  missing.
- **Back to Bee's behaviour without a rollback:** set
  `upload-failure-policy: bee`, or send the header per request.
- **Binary rollback:** the previous build ignores the new namespaces
  (`uploadFailedChunk`, `tagFailureItem`). No existing item changes format,
  so no migration either way. Failed sets left in the index store are not
  removed by the older build; delete the tags with failures
  (`DELETE /tags/{id}`) before a rollback to reclaim that space, at most about
  1.1 GB.

## Upstream portability

The files this changes are unchanged from Bee v2.8.2, so
`scripts/export-patch.sh` should replay the series onto upstream with no
conflict. Ethersphere would need to accept three things: a direct upload
that keeps going after a chunk fails (a change in what the API promises), a
small local store for failed chunks, and new tag fields and endpoints. The
fix for a missing batch in `pushDirect` (problem 5) is a separate one-line
change that ports alone, if the phase-1 test confirms the defect; only then
does the issue get the `affects-upstream` label and a row in
`docs/UPSTREAM.md`.

## Configuration

Four new settings. Rule 8 asks for a measured constant to become a setting
with Bee's value as default. Two of these are new behaviour with no Bee value;
two keep Bee's behaviour as their default.

| Setting | Default | Raising or enabling it | Lowering or disabling it |
|---|---|---|---|
| `upload-failure-policy` | `retry-at-end` (operator decision, 2026-10-18) | `fail-fast`: the client learns about a failure within seconds; nothing completes after a failure, and the client re-sends the body (no new postage with the same batch). | `bee`: Bee's behaviour, with problems 1 to 6. |
| `upload-final-retry-duration` | 30 minutes, 6 rounds at the 5-minute skip-list interval | More chunks recovered, if failures are transient; the request is held longer; up to one more push per failed chunk every 5 minutes, which peers serve and this node pays for. | Fewer recovered; the client gets the failed set sooner. 0 skips the final pass. Other nodes pay less. |
| `upload-shallow-receipt-policy` | `accept`, as Bee | `fail`: shallow-only chunks are reported and kept for retry instead of counted as synced; an upload in an area with many shallow receipts may end with failures where Bee reports success. | Not applicable. |
| `deferred-upload-give-up-after` | 0, never give up, as Bee | A deferred upload ends with a definite outcome; a chunk that would have succeeded after that time is moved to the failed set and needs a retry. Peers stop receiving endless pushes of chunks that do not land. | 0: Bee's endless retry. |

Not settings in phase 1, because nothing is measured yet: 6 attempts per
chunk, the 1 to 16 s backoff, the 5-minute spacing of the final pass, and the
262,144-chunk failed store. Each becomes a setting only after a measurement
shows it matters, in its own issue.

## `docs/DIFFERENCES.md` rows this adds

In *Behaviour that differs with no configuration*:

| Area | Bee v2.8.2 | wasp | In wasp | Bee defect | Record |
|---|---|---|---|---|---|
| Direct upload with a failing chunk | The first chunk that fails ends the upload, but the client hears it only after sending the whole body: 500, no reference, nothing kept. "No peers" is retried without limit. | Each chunk gets 6 attempts; failed chunks are kept and retried for up to 30 minutes after the body is read; a failure answer names the reference, the tag and the count, and `POST /tags/{id}/retry` finishes the upload. `upload-failure-policy: bee` restores Bee's behaviour. | `main` | | [#806](https://github.com/crtahlin/wasp/issues/806) |
| Direct upload tags | A direct upload has no tag. | A direct upload has a tag while it runs; it is kept and returned when any chunk failed. | `main` | | [#806](https://github.com/crtahlin/wasp/issues/806) |
| Direct upload with a missing batch | Reported as success without pushing (to be confirmed by the phase-1 test). | 422 "batch not usable". | `main` | to be decided by the test | [#806](https://github.com/crtahlin/wasp/issues/806) |

In *Settings wasp adds*: one row each for `upload-failure-policy`,
`upload-final-retry-duration`, `upload-shallow-receipt-policy` and
`deferred-upload-give-up-after`, with the defaults and costs of the
Configuration table.

In *API endpoints wasp adds*:

| Endpoint | What it does | In wasp | Record |
|---|---|---|---|
| `GET /tags/{id}/failed` | Lists the chunks of an upload that were not delivered, with the reason and the attempts. | `main` | [#806](https://github.com/crtahlin/wasp/issues/806) |
| `POST /tags/{id}/retry` | Pushes only the undelivered chunks of an upload again, in the background. | `main` | [#806](https://github.com/crtahlin/wasp/issues/806) |

Plus, in the same table or beside it, the header
`Swarm-Upload-Failure-Policy`, the response headers `Swarm-Failed-Chunks` and
`Swarm-Shallow-Chunks`, and the tag fields `failed`, `retrying`, `shallow`
and `state`.

In *Metrics and log lines*: the four metrics and two warnings of section 8.
