# Content providers: mixed-version test

Issue: [#290](https://github.com/crtahlin/wasp/issues/290). Pull request:
[#293](https://github.com/crtahlin/wasp/pull/293). Spec: [spec.md](spec.md), section
Protocol impact.

**Result: pass.**
- Every case delivered the same bytes as a reference download.
- No node blocklisted another.
- No log recorded an allowance error or a panic.
- A stock v2.8.2 node that receives the `wasp-local-only` header ignores it, and
  serves or forwards as it always has.

This is a compatibility test, not a performance measurement. It makes no claim about
speed.

## Setup

Run on `bench-2` on 2026-09-15, beside that host's own node, which was not
touched. The three test nodes were separate processes with their own data
directories, all on mainnet:

| Node | Build | Mode | `providers-enable` |
|---|---|---|---|
| S | stock bee `2.8.2-7e703f49`, the official release binary, checksum verified | full node | not available |
| P (provider) | wasp `0.1.3-89a09b6b` (upstream bee v2.8.2) | full node | on |
| R (requester) | wasp `0.1.3-89a09b6b` (upstream bee v2.8.2) | full node | on |

- **Settlement:** SWAP was off, so only pseudosettle settled the accounts.
- **Addresses:** each node had a fixed public `nat-addr`, and `allow-private-cidrs`
  was on, so the nodes could dial each other directly.
- **Peers when the test started:** S 55, P 30, R 75.
- **Direct connections:** R to P, R to S, and P to S all completed their handshakes.
- **Content:** a public site on Swarm mainnet, found through its ENS name: its
  index page and three of its assets, from 74 KB to 210 KB.
- **"Holds":** a node holds a file when it fetched it once with caching on.
  Local-only answers come from anything a node holds, cache included.
- **Reference result:** every download is compared, by status and SHA-256, with
  the same path fetched through the bench's own node.

## Results

| Case | Requester | Asked first (header) | Holds the file? | Result |
|---|---|---|---|---|
| T3 hit | R (wasp) | P (wasp) | yes | pass, identical |
| T3 miss | R (wasp) | P (wasp) | no | pass, identical, fell back |
| T1a | P (wasp) | S (stock) | yes | pass, identical |
| T1b | P (wasp) | S (stock) | no, so S forwards | pass, identical |
| T2 | S (stock) | none | n/a | pass, identical |

Counters after the run:

| Counter | P | R |
|---|---|---|
| `bee_retrieval_preferred_attempts` | 107 | 110 |
| `bee_retrieval_preferred_hits` | 62 | 42 |
| `bee_retrieval_preferred_misses` | 16 | 66 |
| `bee_retrieval_local_only_misses` | 68 | 0 |
| `bee_retrieval_local_only_limited` | 0 | 0 |

What the counters show:
- **R's misses match P's answers.** R's 66 preferred misses match P's 68 fast
  "not held" answers. Each miss cost P a lookup, not a forward, and none reached
  the miss limit.
- **P's attempts at S came from cases T1a and T1b.** S does not know the header:
  it served the chunks it held, and forwarded the rest.
- **Some misses are feed lookups.** They include feed index probes for updates
  that do not exist yet, which is expected (spec, phase 3).

The blocklist on each node listed none of the other test nodes. Every log showed
no panic, no "failed to meet expectation for allowance" and no blocklist line.

## Two things this test found in upstream code

Neither comes from this change. Both are recorded here because a later run of this
test will meet them again.

1. **A stock node that churns its address gets locked out.** In a first attempt,
   S ran without a fixed `nat-addr` and minted a new signed address 794 times in
   about ten minutes.
   - Upstream mints with `timestamp := max(now, lastTS+1)`, so the timestamp ran
     ahead of the clock.
   - Every peer rejects a handshake more than 60 s in the future, so both wasp
     nodes failed to connect with `check timestamp: bzz: timestamp in future`.
   - Details and code references are in a comment on
     [#221](https://github.com/crtahlin/wasp/issues/221#issuecomment-5679255944).
   - Wasp nodes are not affected, because #225 keeps their address stable.
   - With a fixed `nat-addr`, S minted once and every handshake succeeded.
2. **`GET /rchash` panics when storage incentives are off.**
   - The handler dereferences a nil storage-incentives agent. The HTTP server
     recovers, and the connection closes with no body.
   - Seen on stock v2.8.2 and on wasp alike.
   - The response would not have helped here anyway: it carries a hash and
     proofs, not the sampled chunk addresses.
   - Not yet filed. It needs an issue with the `affects-upstream` label after the
     unmodified upstream handler has been read.

## How to repeat it

Start three nodes as above, each with its own ports and data directory, then:

1. Fetch the reference results through a separate node.
2. Connect the three nodes with `POST /connect`.
3. Let P fetch the index page and S fetch the second asset.
4. Run the five cases, each with the header and `Swarm-Cache: false`.
5. Compare status and SHA-256, and read the counters, blocklists and logs.

Fresh full nodes fill their reserves quickly, about 1 GB each in ten minutes, so
use a disk watchdog on a small disk and delete the data directories afterwards.

Generated with help of AI.
