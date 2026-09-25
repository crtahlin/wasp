# A download that cannot fetch its root chunk looks up providers

Issue: [#498](https://github.com/crtahlin/wasp/issues/498).
Type: fix.

## Problem

Automatic discovery is triggered on a download's 64th getter call
(`discoverAfterChunks`, `pkg/api/providers.go`). Content that was never in the
network, which is what `POST /wasp/ingest` produces, cannot give a requester
even its root chunk, so the download makes **one** getter call and ends. The
trigger is unreachable at any file size.

Measured on 2026-09-25 on v0.1.4, with never-stamped content ingested and
announced on one node: a plain `GET /bytes/<ref>` with no hint answered 404 in
**all 12 runs** across both directions between `stake-1` and `bench-1`, in 2.2 to
2.5 s, with `preferred_attempts` unchanged. A lookup for the same references
found the provider **every time**, in 1.4 to 1.7 s. So the provider was
findable and was never looked for.

The correction on #498 settles the severity: content that was never stamped is
the feature's primary case, not an edge of it.

## Hypothesis

The 64-call threshold is right for what it was written for: content the network
partly holds, where a lookup is speculative and costs other nodes unpaid work
(`spec.md:246-254`). A download that **cannot fetch its root chunk** is a
different case. Without a provider it will answer 404; a lookup is then the only
thing that can make it succeed, so its cost is paid only by a download that is
failing anyway.

## Design

**Decided on #498, option A**
([comment](https://github.com/crtahlin/wasp/issues/498#issuecomment-5829463239)).

When a `/bytes` or `/bzz` download cannot fetch its root chunk (`joiner.New`, or
the manifest read for `/bzz`, returns not found), and all of the following hold:

- content providers are on
- the request carried **no** `Wasp-Providers` header (a hinted download already
  connects to its named providers, per #499)
- the reference is a plain, unencrypted one, so it has a content key

then, once per request:

1. look up the providers of the reference and connect to the verified ones, as
   `Discover` does, adding them to the request's preferred set
2. wait, up to the same 10 s limit as #499 (`hintConnectWait`), for the first
   provider to connect, or for the lookup to end without one
3. if a provider connected, fetch the root chunk **once more**; the preferred set
   now holds a connected provider, so the retry asks it first

Otherwise, or if the retry also misses, answer 404 as today.

`Discover` gains the same kind of return value as `ConnectHints`: a run whose
`Done` closes when the first provider is connected or the run has ended. The
64th-call trigger ignores it, as it does today.

### What must not regress

- **Content the network holds** fetches its root chunk and never reaches this
  path. The 64-call threshold, and everything the spec says about it, is
  unchanged.
- **A reference nobody holds** now costs one lookup before its 404: the slot
  reads, each with a 2 s deadline (`spec.md:238-239`), bounded by the 10 s wait.
  The empty result is cached for 10 minutes (`lookupCacheTTL`), so repeating the
  request costs no further lookup.
- **Hinted downloads** keep the #499 path and do not look up a second time.

### Not changed

- The threshold itself. Lowering it cannot help: the reachable count is 1.
- `/chunks` and `/feeds`, which pass no content key.

## Protocol impact

**None.** The lookup and the dial already exist and run through `Discover`; this
only adds one more place that starts them. No message, stream or constant in
`.github/protocol-freeze.lock` changes. `make protocol-freeze` must report the
surface unchanged.

## Tests

In `pkg/api` and `pkg/providers`, mutation checked. Each mutation below must
break a named test; a mutation that does not compile proves nothing and is
redone.

- **A download that misses its root and has a provider succeeds.** A fake whose
  discovery makes the content available and reports a connection: the first
  fetch misses, the lookup runs once, the retry answers 200.
- **Network-held content never looks up.** A download whose root is present does
  not call discovery at all. This is the regression guard for the threshold.
- **A reference nobody holds answers 404 after one lookup**, and a second request
  is served from the lookup cache (checked in `pkg/providers`).
- **A hinted download does not look up on a miss**; #499 already connected its
  named providers.
- **Encrypted references do not look up.**
- **`Discover`'s run** closes `Done` at the first connection, and when the lookup
  ends empty.

Mutations: look up on every download, not only on a miss; skip the retry;
retry without waiting; look up for a hinted download; look up for an encrypted
reference.

## Measurement

Three runs per condition (rule 7), a fresh random object each run, ingested with
`POST /wasp/ingest` and announced, the requester not connected to the provider
at the start (`DELETE /peers/{provider}`), and no hint:

1. **announced, never-stamped content, plain `GET /bytes/<ref>`**: must answer
   200 with a matching checksum on the first request, `preferred_hits` near the
   chunk count; today it answers 404 in every run
2. **the same through `/bzz`** for an ingested collection
3. **a reference nobody holds**: the 404 latency, against today's 0.9 to 4.3 s
   (#435), and a second request not adding a lookup
   (`bee_providers_lookups_completed` unchanged, `lookups_served_from_cache` +1)
4. **control, content the network holds**: `bee_providers_lookups_completed`
   unchanged by the download

A negative result is row 1 still answering 404, or row 4 showing a lookup.

Results go into `lookup-on-miss-results.md`.

## Rollout and rollback

No configuration. It applies on nodes with `providers-enable` on. Rollback is
reverting the merge.

## Upstream portability

Not applicable. Provider records and discovery exist only in wasp, so the issue
carries no `affects-upstream` label.

## Files

- `pkg/api/bzz.go`: the retry in `downloadHandler` and in the `/bzz` manifest
  read.
- `pkg/api/providers.go`: the helper that runs discovery and waits, and the
  `Providers` interface.
- `pkg/providers/providers.go`: `Discover` returns its run.
- tests in `pkg/api` and `pkg/providers`.
- `docs/DIFFERENCES.md`, the automatic discovery row; `docs/experiments/INDEX.md`.

Generated with help of AI.

## Addendum, 2026-09-25: the wait bound is raised to 20 s

**The first node validation was negative for the fresh-requester case.** On
the bench, three runs per condition, with `stake-1` on v0.1.4 as the provider and
throwaway ultra-light requesters built from `main` at `cba69e16`:

| Condition | Result |
|---|---|
| plain `GET /bytes/<ref>`, fresh ultra-light requester | 404 in 3 of 3, about 12.4 s |
| plain `GET /bzz/<ref>/` of an ingested collection, fresh ultra-light requester | 404 in 3 of 3, 17 to 24 s |
| plain `GET /bytes/<ref>`, bench-1, a full node, disconnected from the provider | 200 in about 4 s |
| a reference nobody holds, twice | 404 in 3.6 to 3.9 s after one lookup; the repeat served from the lookup cache, as specified |

In every failing run the lookup completed and discovery had started its dial,
but no connection was counted. The cause is the connect itself: a plain
`POST /connect` from a fresh ultra-light node to the provider took 10.61, 10.54
and 10.65 s on three separate nodes, against 0.23 s from bench-1. With a 10 s
bound the miss path gave up about half a second before the connection landed,
and it retries only when a provider is connected. Why that connect takes 10.5 s
is unmodified upstream code, tracked in #511.

**The change:** `hintConnectWait` goes from 10 s to **20 s**. It is the same
constant #499 uses, and the hinted path had the same margin: it succeeded from
fresh requesters only because the root chunk's own retries outlasted the
connection. The wait still ends at the first connection, or as soon as the
lookup ends with no provider, so the longer bound costs time only where a
provider was found but connects slowly or not at all. A reference nobody holds
is unaffected.

20 s is two times the measured connect, not a tuned value. It stays a constant
(rule 8); if #511 removes the 10.5 s delay, the bound can come back down with a
measurement to justify it.

The measurement above is repeated with the change, and the results go into
`lookup-on-miss-results.md` together with this negative run.
