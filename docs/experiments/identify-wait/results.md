# Results: answer a peer behind NAT without waiting 10 s for addresses

Issue: [#511](https://github.com/crtahlin/wasp/issues/511). Spec: [spec.md](spec.md).
Merged in `a20f4812` (#516).

Measured on 2026-09-26, with the operator's approval, on the `stake-1` host,
which has a public address. The responder was a throwaway full node, separate
from `stake-1`'s own node: payments off, its own data directory, its own ports
and throttled pull sync. It ran v0.1.4 first and then the build with the fix, on
the same data directory, and was measured only after `/readiness` returned 200.
It was removed afterwards; `stake-1`'s node was not touched and stayed healthy.

The requesters were fresh ultra-light nodes on the `bench-1` host, behind NAT,
with about 35 peers each: a new node for every run, each making one
`POST /connect` to the responder's IPv4 address.

**Table: connect time from a fresh node behind NAT to a public full node**

| Responder build | Run 1 | Run 2 | Run 3 |
|---|---|---|---|
| v0.1.4 (`68f8a2ea`), where this code path is unmodified upstream | 10.48 s | 11.28 s | 10.51 s |
| `0.1.4-main-2026-09-26b-a20f4812`, with the fix | 0.56 s | 0.33 s | 0.29 s |

All six connects answered 200. The difference is the 10 s address wait, and
nothing else in the path changed.

**Two failed attempts came first, and they were errors in the harness, not in
the node:**

1. The first attempt passed a list-valued setting, `blockchain-rpc-endpoint`,
   as a single flag, so the throwaway node had no RPC endpoint and exited.
2. The second measured before the node was ready. A fresh full node first
   catches up on postage events, which took 30 minutes here, and until then its
   inbound handler does not answer handshakes, so every dial timed out.

Both lessons are now in `docs/agent-playbooks/test-bench.md`.

**Unit and in-process evidence:** `TestConnectEmptyPeerstoreSkipsAddressbookAndReacher`
connects to a responder whose peerstore has no address for the dialler. It takes
0.22 s with the fix and 10.0 s with the identify path disabled.

**Follow-up worth measuring:** the provider wait bound was raised from 10 s to
20 s (#513) because of this delay. With #511 in place, a smaller bound may be
enough; that needs its own measurement before changing.

Generated with help of AI.
