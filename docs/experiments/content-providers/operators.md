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

## What announcing costs

- **Stamp slots.** Every 12 hours the node writes one record and one pointer
  entry per announced reference, and sometimes rewrites a lost pointer entry. That
  is about 4 to 7 stamped chunks a day per reference, from the batch you named.
  The content itself is not stamped: pinning keeps it without a stamp.
- **Upload bandwidth.** Nodes that find yours ask it first for the chunks of the
  content. They pay the normal retrieval price.
- **Unpaid work, limited.** A peer can ask for a chunk your node does not hold
  and get a fast "not held" answer, which earns nothing. The node answers at most
  100 of those per second per peer and 1000 per second in total.

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

- **Up to 24 extra retrievals per new reference,** some of them unpaid work for
  the nodes that forward them.
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

Generated with help of AI.
