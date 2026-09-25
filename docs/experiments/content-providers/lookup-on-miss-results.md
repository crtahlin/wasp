# Results: a download that cannot fetch its root chunk looks up providers

Issue: [#498](https://github.com/crtahlin/wasp/issues/498). Spec:
[lookup-on-miss.md](lookup-on-miss.md), with its addendum. Merged in
`cba69e16` (#509); wait bound raised to 20 s in `0094c313` (#513).

Measured on the bench. The provider is `stake-1`, a full node with a public
address on v0.1.4, holding content from `POST /wasp/ingest` at redundancy level
0, never stamped and never pushed, 100,000 bytes per object, a fresh random
object for every run. Requesters run the build under test: `bench-1`, a full
node, and throwaway fresh ultra-light nodes started on the `bench-1` host for
each run (`--full-node=false --swap-enable=false`, about 35 peers), which have
never met the provider. Every download sends `Swarm-Cache: false`. Harness:
`validate.py`, outside this repository.

## With the 20 s bound, build `0.1.4-main-2026-09-26-0094c313`, 2026-09-26

**Table: #498 conditions, three runs each, 20 s wait bound**

| Run | Condition | Result |
|---|---|---|
| 1 | fresh, plain GET | 200 in 28.6 s, checksum matches; dialled the provider the lookup found; preferred hits 26 |
| 1 | fresh, /bzz collection | 200 in 19.2 s; dialled the provider the lookup found; preferred hits 4 |
| 1 | nobody holds, twice | first 404 in 3.8 s, one lookup; second 404 in 2.8 s, served from the lookup cache |
| 2 | fresh, plain GET | 200 in 30.3 s, checksum matches; dialled the provider the lookup found; preferred hits 26 |
| 2 | fresh, /bzz collection | 200 in 19.2 s; dialled the provider the lookup found; preferred hits 4 |
| 2 | nobody holds, twice | first 404 in 3.6 s, one lookup; second 404 in 2.8 s, served from the lookup cache |
| 3 | fresh, plain GET | 200 in 32.9 s, checksum matches; dialled the provider the lookup found; preferred hits 26 |
| 3 | fresh, /bzz collection | 200 in 19.1 s; dialled the provider the lookup found; preferred hits 4 |
| 3 | nobody holds, twice | first 404 in 3.6 s, one lookup; second 404 in 2.8 s, served from the lookup cache |

- **A plain download of announced, never-stamped content now succeeds on the
  first request** from a requester that has never met the provider, through
  `/bytes` and through `/bzz` for an ingested collection, 3 of 3 each. On v0.1.4
  the same download answered 404 in all 12 runs of the dry run.
- **A reference nobody holds** costs one lookup before its 404, 3.6 to 3.8 s,
  and a repeat is served from the lookup cache, as the spec requires.
- `/bzz` shows 4 preferred hits, against 26 for a 100,000-byte `/bytes`
  object, because the page served is the collection's small index document.

## With the original 10 s bound, build `cba69e16`, 2026-09-25: negative

**Table: #498 conditions, three runs each, 10 s wait bound**

| Run | Condition | Result |
|---|---|---|
| 1 | fresh, plain GET | 404 in 12.3 s |
| 1 | fresh, /bzz collection | 404 in 24.2 s |
| 1 | nobody holds, twice | first 404 in 3.6 s, one lookup; second 404 in 5.3 s, served from the lookup cache |
| 2 | fresh, plain GET | 404 in 12.5 s |
| 2 | fresh, /bzz collection | 404 in 17.3 s |
| 2 | nobody holds, twice | first 404 in 3.6 s, one lookup; second 404 in 2.6 s, served from the lookup cache |
| 3 | fresh, plain GET | 404 in 12.3 s |
| 3 | fresh, /bzz collection | 404 in 17.5 s |
| 3 | nobody holds, twice | first 404 in 3.9 s, one lookup; second 404 in 2.9 s, served from the lookup cache |

The miss path failed in 6 of 6 fresh-requester runs. The lookup completed and
the dial started, but a connect from a fresh ultra-light node takes about 10.5 s
(10.61, 10.54 and 10.65 s on three nodes with a plain `POST /connect`, against
0.23 s from `bench-1`), so the 10 s wait ended just before it landed and the
root was not retried. A full-node requester, `bench-1`, succeeded in about 4 s
in a separate check. The bound was raised to 20 s (spec addendum, #512 and
#513), and the cause of the 10.5 s is tracked in #511.

**Not measured:** the spec's control of content the network holds, which would
need a stamped upload. It is covered by `TestProvidersNoLookupWhenRootPresent`,
and by the design: such content fetches its root and never reaches the lookup.

Generated with help of AI.
