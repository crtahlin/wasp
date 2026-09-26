# Content providers: operator note

Issue: [#290](https://github.com/crtahlin/wasp/issues/290). Design:
[spec.md](spec.md).

Content providers let a node that holds content be found and asked first for it.
Everything is off unless the node runs with `providers-enable: true`.

## Announcing content your node holds

1. Pin the content: `POST /pins/{reference}`.
2. Announce it with a postage batch:
   `POST /wasp/providers/{reference}` with the header `Swarm-Postage-Batch-Id`.
3. Check what the node announces: `GET /wasp/providers`.
4. Check that others can find it: `GET /wasp/providers/{reference}/lookup` on any
   node with the setting on.

Only a full node can announce. A light node refuses inbound retrieval, so it
could not serve what it announced.

Content stored with `POST /wasp/ingest` is pinned already, and the ingest can
announce it in the same call: send `Swarm-Postage-Batch-Id` with the ingest.
The response then carries `announced`, and `announceError` when the
announcement failed. A failed announcement does not fail the ingest, so check
`announced` rather than the status code, and announce later with step 2
([#503](https://github.com/crtahlin/wasp/issues/503)). If the client gives up
before the answer arrives, the content may be stored but not announced: check
`GET /wasp/providers`, or send the same ingest again, which answers 200 and
announces.

## What announcing costs

- **Stamp slots.** Every 12 hours the node writes one record and one pointer
  entry per announced reference: 4 stamp slots a day per reference, from the
  batch you named. Rewriting a lost pointer entry reuses its slot, so uploads can
  reach 10 a day while the slots stay at 4. Announcing the same reference again
  publishes it again. The content itself is not stamped: pinning keeps it without
  a stamp.
- **Encrypted references cannot be announced.** Their record would publish the
  decryption key.
- **Upload bandwidth.** Nodes that find yours ask it first for the chunks of the
  content. They pay the normal retrieval price.
- **Unpaid work.** A peer can ask for a chunk your node does not hold and get a
  fast "not held" answer, which earns nothing. Past 100 of those per second per
  peer, or 1000 per second in total, the node answers with a limit error instead.
  The store lookup has already happened by then, so the limit caps the answers,
  not the lookups.

## What announcing reveals

- **What your node holds.** A record links your node's key and network address to
  the reference, for anyone who looks it up.
- **What your node lacks.** A peer can ask for any chunk with the local-only
  header and learn, cheaply and precisely, whether your node holds it. This covers
  your reserve and cache too, not only what you announced.

## Downloading

With the setting on, a download of more than 64 chunks through `/bzz` or `/bytes`
looks up providers of its reference and tries them first. A client can also name
providers directly with the `Wasp-Providers` header (up to 8 hex overlays).

What it costs you:

- **Up to 24 extra retrievals per reference,** usually repeated at most once every
  10 minutes while downloads of it continue. Downloads that start at the same
  moment can each run one. Some of them are unpaid work for the nodes that forward
  them.
- **The provider learns what you download.** It sees that a request comes from
  your node itself, not forwarded for someone else.

## Turning it off

Set `providers-enable: false` and restart.

- **Records expire.** They stay valid until the end of the last 12-hour window the
  node wrote, at most 24 hours.
- **The node answers as stock Bee does.** Until the records expire, other nodes
  may still ask it first. It then forwards a request for a chunk it does not hold
  and charges for it, as stock Bee does.
- **Pins stay.** Remove them with `DELETE /pins/{reference}`.

## Finding out why a download stalled

A download that returns HTTP 200 with a short body, and `curl` exit 18, has
truncated. Two very different causes look identical from outside: the chunks
are genuinely gone, or your node ran out of credit with the peer holding them
and stopped asking. Both surface as `storage.ErrNotFound`.

The accounting logger tells them apart, at its `all` level. The level is off by
default, and with it off a refusal costs a verbosity check: an atomic load and
a comparison. The line and the state-store read that fills it sit behind it, so
nothing is formatted or read when nobody is looking.

Do this on the node that is **downloading**, not the one serving. The credit
decision is made by the requester.

First read the level you are on, so you can put it back. `info` is the shipped
default, but if your node runs at something else, this is your only chance to
see it:

```
GET /loggers/bm9kZS9hY2NvdW50aW5n
```

Then raise it:

```
PUT /loggers/bm9kZS9hY2NvdW50aW5n/all
```

`bm9kZS9hY2NvdW50aW5n` is base64 of `node/accounting`. **The path segment is
base64, not a URL path**, so the obvious `PUT /loggers/accounting/all` returns
400: `accounting` is ten bytes and fails base64 padding.

The name inside it is matched as an unanchored expression against the logger
tree, so `bm9kZS9hY2NvdW50aW5n` and the base64 of plain `accounting`
(`YWNjb3VudGluZw==`) both work. The full path is used here only because it says
what is being changed.

Run the download again and look for:

```
"msg"="credit refused, would overdraw"
```

Each line carries the peer, the price of the chunk, the debt the request would
create, the limit it was measured against, and each term that fed them. If
those lines are absent, credit was not the reason.

Put the level back when you are finished, to whatever the first step showed.
Restoring `info` on a node that was running at `debug` would silence ordinary
accounting messages you were relying on:

```
PUT /loggers/bm9kZS9hY2NvdW50aW5n/info
```

**Leave it raised only while you are looking.** Under sustained load against a
slow peer the node can emit one line per refused request. It costs nothing to
other nodes, since the line never leaves this machine, but it will fill a
journal.

**If you are on a build older than this change**, the V(2) entry is created
lazily, so raise the level only after the node has carried some retrieval
traffic. Raising it on a node that has served nothing yet has no effect.

**These lines contain peer overlay addresses.** If you are pasting output into
an issue, a results document or anywhere else in this repository, redact them
first: fork rule 10 forbids node addresses in a public repository, and that
rule has been breached here once already.

Generated with help of AI.
