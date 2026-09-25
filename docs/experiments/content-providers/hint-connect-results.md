# Results: a hinted download connects to its provider before it starts

Issue: [#499](https://github.com/crtahlin/wasp/issues/499). Spec:
[hint-connect.md](hint-connect.md). Merged in `9872dce6` (#506); wait bound
raised to 20 s in `0094c313` (#513).

Measured on the bench. The provider is `stake-1`, a full node with a public
address on v0.1.4, holding content from `POST /wasp/ingest` at redundancy level
0, never stamped and never pushed, 100,000 bytes per object, a fresh random
object for every run. Requesters run the build under test: `bench-1`, a full
node, and throwaway fresh ultra-light nodes started on the `bench-1` host for
each run (`--full-node=false --swap-enable=false`, about 35 peers), which have
never met the provider. Every download sends `Swarm-Cache: false`. Harness:
`validate.py`, outside this repository.

## With the 20 s bound, build `0.1.4-main-2026-09-26-0094c313`, 2026-09-26

**Table: #499 conditions, three runs each, 20 s wait bound**

| Run | Condition | Result |
|---|---|---|
| 1 | fresh, lookup then hint | 200 in 25.7 s, checksum matches; dialled through the record; preferred hits 26 |
| 1 | unannounced, fresh, hint | 404 in 4.4 s |
| 1 | bench-1 disconnected, hint only | 200 in 0.4 s, checksum matches; dialled from the address book; preferred hits 26 |
| 1 | bench-1 connected, hint | 200 in 0.2 s, checksum matches; already connected; preferred hits 26 |
| 2 | fresh, lookup then hint | 200 in 25.7 s, checksum matches; dialled through the record; preferred hits 26 |
| 2 | unannounced, fresh, hint | 404 in 4.1 s |
| 2 | bench-1 disconnected, hint only | 200 in 0.5 s, checksum matches; dialled from the address book; preferred hits 26 |
| 2 | bench-1 connected, hint | 200 in 0.2 s, checksum matches; already connected; preferred hits 26 |
| 3 | fresh, lookup then hint | 200 in 23.8 s, checksum matches; dialled from the address book; preferred hits 26 |
| 3 | unannounced, fresh, hint | 404 in 4.0 s |
| 3 | bench-1 disconnected, hint only | 200 in 0.4 s, checksum matches; dialled from the address book; preferred hits 26 |
| 3 | bench-1 connected, hint | 200 in 0.1 s, checksum matches; already connected; preferred hits 26 |

Every row the spec asks for passes in 3 of 3 runs:

- **A fresh requester reaches the provider on its first hinted request**, after
  one lookup, with no manual connect. In runs 1 and 2 the address came from the
  provider record, which is the new path; in run 3 the fresh node had already
  learned the address from peer gossip and dialled it from the address book.
- **A disconnected requester** needs no lookup and no manual connect: 0.4 to
  0.5 s.
- **Content that was never announced** answers 404 with the new message naming
  "1 had no known address and no provider record".
- **The connected control** shows no added latency: 0.1 to 0.2 s.

The fresh-requester runs take about 24 to 26 s because a connect from a fresh
ultra-light node takes about 10.5 s, measured with a plain `POST /connect`,
independent of this change. That is tracked in #511.

## With the original 10 s bound, build `cba69e16`, 2026-09-25

**Table: #499 conditions, three runs each, 10 s wait bound**

| Run | Condition | Result |
|---|---|---|
| 1 | fresh, lookup then hint | 200 in 24.2 s, checksum matches; dialled from the address book; preferred hits 26 |
| 1 | unannounced, fresh, hint | 404 in 4.1 s |
| 1 | bench-1 disconnected, hint only | 200 in 0.4 s, checksum matches; dialled from the address book; preferred hits 26 |
| 1 | bench-1 connected, hint | 200 in 0.2 s, checksum matches; already connected; preferred hits 26 |
| 2 | fresh, lookup then hint | 200 in 24.6 s, checksum matches; dialled through the record; preferred hits 26 |
| 2 | unannounced, fresh, hint | 404 in 5.7 s |
| 2 | bench-1 disconnected, hint only | 200 in 1.0 s, checksum matches; dialled from the address book; preferred hits 26 |
| 2 | bench-1 connected, hint | 200 in 0.2 s, checksum matches; already connected; preferred hits 26 |
| 3 | fresh, lookup then hint | 200 in 24.9 s, checksum matches; dialled from the address book; preferred hits 26 |
| 3 | unannounced, fresh, hint | 200 in 24.7 s; dialled from the address book; preferred hits 26 |
| 3 | bench-1 disconnected, hint only | 200 in 0.4 s, checksum matches; dialled from the address book; preferred hits 26 |
| 3 | bench-1 connected, hint | 200 in 0.2 s, checksum matches; already connected; preferred hits 26 |

The rows passed here too, but not for the reason the design gives: the 10 s
wait ended before the 10.5 s connect landed, and the download succeeded only
because the root chunk's own retries outlasted it. In run 3 of the
never-announced row, the fresh node had learned the provider's address from
gossip and fetched the content, which is correct behaviour for a hint naming a
reachable provider. The same margin failed outright for #498, which is why the
bound was raised (addendum in the spec).

Generated with help of AI.
