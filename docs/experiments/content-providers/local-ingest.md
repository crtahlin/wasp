# Hosting content without a stamp: local ingest

Issue: [#326](https://github.com/crtahlin/wasp/issues/326). A node puts content
into its own store, pays no postage, and serves it to anyone who asks for it.

Code references are to `main` at `a8772819`, base `upstream/v2.8.2`.

## Problem

**A node cannot host content it has not paid rent for, unless somebody else is
already paying.** Two facts, both verified:

- **Pinning needs no stamp.** `pinRootHash` reads only
  `Swarm-Redundancy-Level` (`pkg/api/pin.go:25-70`); there is no postage header.
  It stores chunks through `s.storer.NewCollection(ctx)` (`:61`), an unstamped
  putter, and those chunks are served like any others because the retrieval
  handler answers from `Lookup()`, which reads the whole chunk store.
- **But pinning only works on content the network still holds.** It traverses
  the reference and downloads each chunk (`pin.go:68-115`), committing with
  `putter.Done` (`:125`). It cannot create
  content, only adopt it.

Every path that creates content requires postage: `POST /bytes`, `/bzz`,
`/chunks` and `/soc` all carry `Swarm-Postage-Batch-Id` with
`validate:"required"`. So an operator who wants to host a file must first pay to
put it in the network, then keep paying, or pin it before the rent lapses and
hope they were in time.

**The end state is demonstrated to work, with one caveat that matters.** Content
B's postage batch expired days ago. The network answers 404 for it in about
2.3 s. The provider still holds every chunk, unstamped, because they are pinned,
and a requester that names the provider does retrieve the complete 4,194,304
bytes, SHA-256 verified, at about 264,000 B/s.

**The caveat: that was measured with the lookahead prefetch turned off**, with
`Swarm-Lookahead-Buffer-Size: 0`, which reads one chunk at a time. At the shipped
buffer the same download truncates at 31.2% of the file, because the prefetch
puts many chunks in flight and many are refused credit at once. That is
[#327](https://github.com/crtahlin/wasp/issues/327), not this issue, and the
figures are in
[overdraft-retry-results.md](overdraft-retry-results.md).

So serving unstamped pinned content works; serving it at full speed to an
ordinary client does not yet. This spec is about how the content gets there,
which is a separate and unblocked question.

## Hypothesis

A local ingest that splits content into chunks and stores them unstamped lets a
node host indefinitely at only its own disk cost. With the provider mechanism
already in place, anyone who learns the holder retrieves it normally.

**Predicted:** content ingested this way is byte-identical in address to the
same content uploaded with a stamp, is retrievable from the ingesting node by a
requester that names it, and is retrievable from nowhere else.

## Design

**One endpoint, reusing parts that already exist and are already decoupled.**

The splitter does not know about stamps: `requestPipelineFn(s storage.Putter,
encrypt bool, rLevel redundancy.Level)` takes a bare putter
(`pkg/api/api.go:900-905`). Stamping is a property of the putter, not the
pipeline. Pinning already supplies an unstamped one. So the handler is the
upload handler with the postage machinery removed:

```go
putter, err := s.storer.NewCollection(ctx)   // unstamped, as pinning uses
p := requestPipelineFn(putter, encrypt, rLevel)
reference, err := p(ctx, r.Body)
err = putter.Done(reference)                 // commit as a pinned collection
```

Nothing is pushed to the network: the pipeline writes only to the putter, and
the pusher is fed by the upload store, which a collection does not touch.

**What the spec must settle, and the first one is the serious one.**

**1. Only the node's owner may do this. Decided, not open.** An endpoint that
stores unlimited data for free is a way to fill a stranger's disk.

**But there is no authentication layer to put it behind, and the spec must not
pretend otherwise.** This node serves one API on `api-addr`, which defaults to
`127.0.0.1:1633` (`cmd/bee/cmd/cmd.go:352`). There is no token, no restricted
surface and no per-route authorisation anywhere in `pkg/api/router.go`. So
"owner-only" today means exactly "whoever can reach the API", and the protection
is the loopback binding plus the operator's own firewall.

That makes the feature flag the real control, not a convenience: the route must
not exist unless the operator has turned it on, so that a node whose API is
exposed for some other reason does not silently gain a way to be filled. The
documentation must state plainly that enabling this on a node with a
non-loopback `api-addr` hands anyone who can reach it unlimited writes, bounded
only by the cap in (2).

If a real authorisation mechanism is wanted, it is its own piece of work and
should not be invented inside this spec.

**2. A settable maximum, with warnings before it is reached, not only at it.**
A cap the operator sets, defaulting to something conservative rather than
unlimited, because there is no rent to stop a runaway script. What the spec must
define:

- the unit the cap is expressed in, chunks or bytes, and that it counts what
  this node ingested locally rather than everything it stores;
- a metric for current usage against the cap, so it can be alerted on;
- a **warning threshold below the cap**, logged at a level an operator sees, so
  a node approaching the limit says so while there is still room to act;
- the behaviour **at** the cap: the ingest is refused with an error that names
  the limit and the current usage, and the partial collection is cleaned up
  rather than left half-written;
- whether an ingest that would cross the cap is refused before it starts, which
  needs the content length, or part way through, which needs the cleanup path.

**3. Removal must be a first-class operation, not an afterthought.** Nothing
evicts this content: pinned data is never garbage collected, and there is no
expiry to do it for us. So the spec must cover:

- removing one ingested reference, which the existing unpin route already does,
  and confirming the space is actually reclaimed rather than merely unreferenced;
- listing what has been ingested locally, with sizes, so an operator can see
  what is consuming the cap and choose;
- what happens to content that is both locally ingested and pinned for another
  reason, so that removing one does not silently drop the other.

**4. It is not an upload, and must not read like one.** The reference returned
resolves for nobody else unless they ask this node. Calling the route anything
with "upload" in it would mislead. The response should say plainly that the
content is local only, and the documentation should say that losing this node
loses the content, since nothing else holds it.

## Holding and advertising are two axes, not one

The operator's framing, and it is the right one. How content arrives and whether
anyone is told about it are independent, and the code already treats them that
way.

**They are already two steps, and the split is enforced.** Announcing is its own
call, `POST /wasp/providers/{reference}`, and it refuses a reference that is not
pinned: "reference is not pinned; pin it first"
(`pkg/api/providers.go:249-259`). It takes the stamp
(`:241-247`), and it refuses encrypted references outright, because the record
would publish their decryption key (`:236-239`). So holding is free and
advertising costs, and the boundary is already drawn where it should be.

That gives a grid rather than a feature:

| How it arrived | Cost | Advertised? |
|---|---|---|
| Uploaded with a stamp | postage, ongoing | operator's choice |
| Pinned from the network | free | operator's choice |
| Ingested locally (this spec) | free | operator's choice |

**The advertisement mechanism must be a choice, not a constant.** Today there is
one: stamped records. Others are foreseen and must not be designed out, in
particular the summaries between connected peers in
[passive-sharing.md](passive-sharing.md), which cost nothing because nothing is
written to the network, and the explicit hint, which needs no advertisement at
all because the requester already names the holder
(`Wasp-Providers: <overlay>[,<overlay>...]`, comma-split at `providers.go:171`).
So the setting is "which mechanisms, if any", not "advertise yes or no", and it
should read that way from the start even while only one mechanism exists.

**Whether to advertise by default: no, and the reason is cost, not taste.**
Advertising requires a stamp today. Defaulting a pin to advertise would make
`POST /pins/{reference}`, which is free and needs no postage header, start
demanding a batch and failing without one. That is a silent change to the cost
of an existing free operation, and it would break every caller that pins without
a batch.

There is a second reason: advertising tells the network what this node holds,
which some operators will not want, and the quiet default is the one that cannot
surprise them.

**So the default is off, and it is off for a reason that may expire.** When a
mechanism exists that costs nothing, this decision should be revisited rather
than inherited. The spec should say so explicitly, so the next person reads it
as a judgement with a condition attached rather than as settled policy.

**Only the stamped mechanism is implemented now**, per the operator's
instruction. The others are named so the setting's shape is right, not built.

## What this spec does not cover

Designing a new advertisement mechanism. Only the existing stamped one is wired
up here.

**Several holders is already the data model.** `PreferredSet` holds many peers,
the hint header takes a list, and `providerSet` shares one set per content key
across downloads (`providers.go:129-134`). A requester that learns three holders
prefers all three. So nothing here needs building, and a second operator who
later pins the same content simply becomes another entry.

## What this risks

- **Disk, with no rent to bound it.** This is the cost the operator accepts, and
  the reason for (1) and (2) above.
- **No redundancy.** Content that exists only here is gone if this node is. A
  stamped upload is replicated by the neighbourhood; this is not. The
  documentation must say so, because the endpoint will feel like an upload.
- **A false sense of publication.** The content is addressable but not findable.
  Without a hint or an announcement nobody can reach it.

## Protocol impact

**None.** No wire format, protocol ID, handshake or message changes. The chunks
are ordinary content-addressed chunks with ordinary addresses, served by the
existing retrieval protocol. A stock peer asked for one of them answers exactly
as it would for any chunk it does not hold. `make protocol-freeze` must pass
with the fingerprint unchanged.

## Measurement

- **Address equivalence.** Ingest a file locally and upload the same bytes with
  a stamp on another node. The references must be identical. This is the check
  that the ingest is a real Swarm object and not a private format.
- **Absence from the network.** A second node, with no hint, must fail to
  retrieve it. Expect 404.
- **Retrieval from the holder.** The same node, naming the holder, retrieves it
  completely with a matching SHA-256. Report bytes per second beside every
  timing.
- **No waiting.** Unlike the expiry route, this is testable the moment it is
  built, at any size.
- At least three runs per condition with the spread (rule 7).

**This also gives the project a standing sole-source test bed.** Every
measurement of the provider path so far has depended either on a batch expiring,
which takes a day and leaves relay caches as a confound, or on trusting that
caches have evicted. Content ingested this way was never in the network, so no
peer can ever have cached it. That removes a confound the content-providers work
has carried from the start.

That matters immediately for
[#327](https://github.com/crtahlin/wasp/issues/327), whose primary arm is a
sole-source download. Today that arm depends on content whose batch expired, so
"no other peer served this" rests on a 404 control and an assumption about
caches. Content ingested locally makes it a property of the setup rather than
something to be argued each session.

## Acceptance

**Accept** if the reference matches a stamped upload of the same bytes, the
content is unreachable without a hint, and reachable with one, over at least
three runs. **Reject** if the addresses differ, which would mean the ingest is
not producing standard chunks.

## Test plan

Unit tests in `package api_test`:

- the reference from a local ingest equals the reference from a stamped upload
  of identical bytes, at the same redundancy level and encryption setting;
- the endpoint refuses when the feature is off, and when the caller is not the
  owner;
- the quota is enforced and returns a clear error;
- an ingested reference is listed among the node's pins and can be unpinned;
- nothing is handed to the pusher, asserted on a mock.

## Configuration

Two settings, both off or conservative by default.

The feature flag, off by default, with the documented cost of turning it on:
local storage with no rent, content that only this node holds, and no
replication, so losing the node loses the content.

The cap, which is a setting from the start rather than after a measurement. Rule
8 asks for measurement before exposing a dial, but that rule is about tuning
constants whose right value is a property of the software. This one is a
property of the operator's disk, cannot be measured centrally, and its absence
is the failure mode, so it ships as a setting with a conservative default. Its
documentation must state what raising it costs, which is disk that nothing will
reclaim, and what lowering it costs, which is ingests refused.

## Upstream portability

Upstream has no equivalent and no preferred-peer concept, so this is a fork
feature rather than an upstream defect: **no `affects-upstream` marker** under
rule 11. The handler would be fork-authored in `pkg/api`, and it reuses
`storer.NewCollection` and the pipeline unchanged, so it adds no divergence in
`pkg/storer` or `pkg/file`.

## Rollout and rollback

- Off unless the flag is set. Nothing changes for an operator who does not
  enable it.
- Rollback is a revert; content already ingested stays pinned and served, since
  it is an ordinary pinned collection.
- No migration and no on-disk format change.

---

Generated with help of AI.
