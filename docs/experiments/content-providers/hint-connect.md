# A hinted download connects to its provider before it starts

Issue: [#499](https://github.com/crtahlin/wasp/issues/499).
Type: fix.

## Problem

A download that names a provider in `Wasp-Providers` reaches that provider only
if the requester is already connected to it, or holds a **current** address for
it in its own address book. Otherwise the first request answers 404 with no
preferred attempt made, and nothing says why.

Measured on 2026-09-25 on v0.1.4, `stake-1` and `bench-1` in both roles plus a
fresh ultra-light requester, with never-stamped content from `POST /wasp/ingest`
(details in the comments on #499 and #498):

| Situation | Result |
|---|---|
| Hint, requester never connected to the provider | 404 in 2.5 s, `preferred_attempts` +0; `hinted_connects_started` +2, `connects_dialed` 0, `connects_failed` 0 |
| Same, after `POST /connect` to the provider's address | 200, checksum matches, `preferred_hits` +1,242 |
| Lookup then hint, the requester's address book holding a **stale** address (the provider's public IP had changed) | 404 in 3 s, `connects_failed` +1 per run, although the lookup record carried the **current** address |
| Same content, after `POST /connect` to the address from the record | 200, checksum matches, 2.2 s |
| Fresh requester, lookup then hint | Try 1: 404, the dial never started. Try 2, 20 s later: 200, because peer gossip had delivered the address meanwhile |

In every failing row, connecting by hand to an address the requester could
already have had fixed it.

## Hypothesis

Two gaps, both in `ConnectHints` (`pkg/providers/providers.go:411-446`) and its
caller `withProviders` (`pkg/api/providers.go:71-106`).

1. **The address comes only from the address book.** `ConnectHints` resolves
   each hinted overlay with `Options.Resolve`, the address book, and skips an
   overlay it cannot resolve, at debug level. A dial to a stale address fails and
   is not retried. Yet a provider that announced has a **verified record** whose
   underlays are signed by its own key, and `Lookup` returns it. Only `Discover`
   dials record addresses, and #498 makes `Discover` unreachable for ingested
   content.
2. **The download does not wait for the dial.** `ConnectHints` runs in the
   background and `withProviders` returns at once, so the root chunk is fetched
   by `joiner.New` straight away. #435 (`preferred-rebuild.md`) already lets a
   chunk use a provider that connects **during** its flight, which is why a fast
   dial succeeds. A lookup (1.4 to 1.7 s measured) plus a dial outlasts the root
   chunk's retry budget (404 in about 2.3 s), so the rebuild cannot rescue the
   record path.

## Design

### 1. Resolve from the address book, then from the provider record

For each hinted overlay the requester is not connected to:

1. resolve it from the address book and dial, as today
2. if it is not in the address book, **or that dial fails**, look up the content
   key and dial the underlays of the verified record whose overlay is the hinted
   one

The lookup runs **at most once per request**, shared by all hinted overlays, and
uses the existing lookup cache (`lookupCacheTTL`, 10 minutes). So the documented
two-step flow, `GET /wasp/providers/{ref}/lookup` then a hinted download, costs
one network lookup in total: the download finds the records in the cache.

The fallback needs a content key. `/bytes` and `/bzz` downloads have one;
`/chunks` and `/feeds` pass none (`withProviders(r, nil)`), so for them only
step 1 applies, as today.

Addresses from records are used only to dial, as `spec.md` already requires
(`spec.md:190-192`): the address book is never written from a record, and a
successful dial stores the address the handshake verified, as any connection
does. So a stale address-book entry is replaced by a successful dial through
the record, without writing the record into the address book.

Only records for an overlay **named in the hint** are dialled. A hint is a
request to use those providers; it is not a request to connect to every
provider the lookup finds. The overlay-only rule for the header
(`spec.md:130-133`) is unchanged.

### 2. Wait, with a time limit, for the first named provider to connect

`ConnectHints` gains a return value: a channel closed as soon as **one** named
provider is connected (already connected, or dialled successfully), or when
every attempt has finished without success. `withProviders` waits on it for at
most `hintConnectWait`, **10 seconds**, before returning. The remaining dials
carry on in the background, bounded by `discoverBound()` as today.

- A request whose named provider is already connected returns after that one
  dial, and no lookup runs. **Corrected during review:** the address book dials
  for all named overlays run at once, before any record fallback, so a dead
  address named first cannot hold up a reachable provider named second, which
  a one-at-a-time loop did.
- A request without the header is unaffected: nothing waits.
- `/chunks` and `/feeds` also call `withProviders`, so a hinted request there
  waits too, with only the address book as a source, and so do HEAD requests on
  `/bytes` and `/bzz`. (Added during review; the earlier text named only
  `/bytes` and `/bzz`.)
- `hintConnectWait` is a constant, as `discoverAfterChunks` is. It is not
  exposed as an option until a measurement shows it matters (rule 8).

The cost is on a download that names a provider it cannot reach: it now takes
up to 10 s longer before falling back to ordinary retrieval, or before its 404.

### 3. Say why a hinted download failed

When a download that carried `Wasp-Providers` ends in 404, the message says what
happened to the named providers, from what step 1 recorded:

```
not found; of the 1 named providers, 0 were connected, 1 had no known address and no provider record, 0 could not be dialled, 0 were still being tried
```

A download without the header keeps the current empty 404 body. The counts are
per request and describe the named providers only.

**Corrected during review of the implementation.** The wording above replaces
an earlier one, "no named provider could be used: ...", which contradicted
itself when a provider had connected but did not deliver, and which could not
count dials still running after the wait timed out. On `/bzz`, a missing root
ends at the manifest path's own 404, "address not found or incorrect", so the
outcome is appended to that message rather than replacing it.

### Not changed

- `GET /wasp/providers/{ref}/lookup` does not dial. With step 1 it no longer
  needs to: the hinted download that follows uses the cached records.
- Discovery without a hint (`Discover`, the 64-chunk trigger) is untouched; #498
  covers when it should run.
- Announcing in the same call as ingest is split into #503.

## Protocol impact

**None.** No message, stream or constant in `.github/protocol-freeze.lock`
changes. The node dials addresses it already dials today through `Discover`,
from records it already verifies, with `forceConnection=false` as before, so a
full Kademlia bin can still refuse the connection. `make protocol-freeze` must
report the surface unchanged.

## Tests

In `pkg/providers` and `pkg/api`, mutation checked. Each mutation below must
break a named test; a mutation that does not compile proves nothing and is
redone.

- **Unknown overlay, announced content: the record address is dialled.**
  `Resolve` fails, the lookup returns a record for the hinted overlay, and
  `Connect` is called with that record's address. Fails today: nothing is
  dialled.
- **Stale address: the record address is dialled after the failed dial.**
  `Resolve` returns an address, `Connect` fails on it, and succeeds on the
  record's address.
- **Only hinted overlays are dialled.** A record for another overlay is not.
- **Already connected: no lookup.** The channel closes at once and `Lookup` is
  not called.
- **No content key: no lookup.** `/chunks` and `/feeds` keep today's behaviour.
- **One lookup per request** for several hinted overlays that all need it.
- **`withProviders` waits for the connection, and only up to the limit.** With a
  fake whose channel closes after a delay, the handler does not start the
  download before it; with one that never closes, it returns after the limit,
  shortened in the test.
- **The 404 message** names the outcome for a hinted request and stays empty
  without the header.

Mutations: resolve from the address book only; skip the fallback after a failed
dial; dial every record instead of the hinted ones; return from `withProviders`
without waiting; wait without a limit; drop the message.

## Measurement

The failure is sole-source content that cannot be fetched on the first hinted
request, so the measurement repeats the rows in *Problem* on a build with the
fix, **three runs per condition** (rule 7), each with a fresh random object
ingested on the provider:

1. **fresh requester**, a new node that has never met the provider: lookup, then
   one hinted download, no manual connect
2. **disconnected requester**, `DELETE /peers/{provider}` first: one hinted
   download of announced content, no lookup call and no manual connect
3. **unannounced content, requester not connected**: one hinted download, which
   must still answer 404, now with the message naming "no known address and no
   provider record"
4. **control, requester already connected**: time to first byte must not grow

Success: rows 1 and 2 answer 200 with a matching checksum on the first request
in every run, with `preferred_hits` near the chunk count; row 4 shows no added
latency. A negative result is a first request that still answers 404 in row 1 or
2, which would mean the address source or the wait is wrong.

The stale-address case cannot be recreated on demand, since it needs a public IP
change. It is covered by the unit test, and by reading `connects_failed` beside
a successful download if it recurs.

Results go into `hint-connect-results.md`.

## Rollout and rollback

No configuration. It applies to downloads that carry `Wasp-Providers`, on nodes
with `providers-enable` on. Rollback is reverting the merge; the behaviour
returns to the background dial from the address book only.

## Upstream portability

Not applicable. `Wasp-Providers`, provider records and `ConnectHints` exist only
in wasp, so the issue carries no `affects-upstream` label.

## Files

- `pkg/providers/providers.go`: `ConnectHints` gains the record fallback, the
  once-per-request lookup and the done channel.
- `pkg/providers/metrics.go`: a counter for dials whose address came from a
  record, beside the existing connect counters.
- `pkg/api/providers.go`: the `Providers` interface, the bounded wait in
  `withProviders`, and the per-request outcome carried in `providerHint`.
- `pkg/api/bzz.go`: the 404 message in `downloadHandler` and the manifest path.
- tests in `pkg/providers` and `pkg/api`.
- `docs/DIFFERENCES.md`, the `Wasp-Providers` row; `docs/experiments/INDEX.md`.

Generated with help of AI.

## Addendum, 2026-09-25: the wait bound is raised to 20 s

Node validation of #498, which shares `hintConnectWait`, found that a connect
from a fresh ultra-light requester to the provider takes about 10.5 s (#511).
The hinted path's own validation passed from fresh requesters in 3 of 3 runs,
but only because the root chunk's retries outlasted the connection, not because
the 10 s wait covered it. The bound goes to 20 s for both paths; see the
addendum in [`lookup-on-miss.md`](lookup-on-miss.md). Documentation that says
"up to 10 s" is updated with it.
