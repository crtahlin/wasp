# Cut the CPU cost of validating pulled chunks, with results identical to today

Issue: [#810](https://github.com/crtahlin/wasp/issues/810).

## Problem

A node that fills its reserve spends most of its CPU on checking the chunks it
receives, not on storing them. On an 8-thread host filling 10 doubling-1 nodes
at once, all 8 threads were busy, iowait was 0 % and the network carried about
164 Mbit/s of a measured 0.92 Gbit/s. The CPU, not the disk or the network, set
the fill rate.

A 60 s CPU profile of one of those nodes (build `7158c0ed9`, about 970 stored
chunks per second at 0.9 of a CPU, heap well below its memory limit):

**Table: CPU share by work, one filling node, 60 s CPU profile**

| Work | Code path | Share of CPU |
|---|---|---|
| Stamp owner recovery | `postage.RecoverBatchOwner` → `crypto.Recover` | 31 % |
| Single-owner chunk (SOC) owner recovery in pullsync | `soc.FromChunkValidate` → `crypto.Recover` | 9 % |
| The same SOC recovered a second time | `Reserve.Put` → `storage.ChunkType` → `soc.Valid` | 7 % |
| "Do we already have it" lookups for offered chunks | `DB.ReserveHas` → pebble `Get` | 10 % |
| Reserve writes, network, TLS and the rest | | 43 % |
| **Total** | | **100 %** |

Counters on the same node over 30 s: 8,617 chunks offered per second, 999
wanted, 988 delivered and 905 added to the reserve. Almost every delivered chunk
is kept, so the cost is per stored chunk, not waste on rejected chunks. Each
chunk is offered by about 8.6 peers, which is why the lookups for offers cost as
much as they do.

`crypto.Recover` (`pkg/crypto/signer.go`) recovers the public key with the
pure-Go decred secp256k1 code, reached through btcec. A micro-benchmark of one
recovery on an Apple M5 Pro, one process per condition:

**Table: one public-key recovery, Apple M5 Pro**

| Implementation | Time per recovery |
|---|---|
| `crypto.Recover` (decred, pure Go) | 83.0 µs |
| go-ethereum `crypto/secp256k1.RecoverPubkey` (vendored libsecp256k1, cgo) | 14.7 µs |

That benchmark has not been repeated on the bench CPU yet; the ratio there is a
hypothesis.

A separate profile of a node on the same host during a high-load period showed
garbage collection at 27 % of CPU while the heap was close to the node's
`GOMEMLIMIT`. That is a configuration matter for the operator of that host,
tracked outside this experiment, and is not part of this change.

### Why the double check happens

pullsync (`pkg/pullsync/pullsync.go`, the delivery loop) checks every delivered
chunk with `cac.Valid`, and when that fails with `soc.FromChunkValidate`, which
recovers the SOC owner. It then passes the same chunk to
`ReservePutter().Put`. `Reserve.Put` (`pkg/storer/internal/reserve/reserve.go`)
calls `storage.ChunkType`, which runs `cac.Valid` again and then `soc.Valid`,
and `soc.Valid` is exactly `FromChunkValidate(ch) == nil`. So every SOC is
recovered twice and every content-addressed chunk (CAC) is BMT-hashed twice. The
pushsync handler (`pkg/pushsync/pushsync.go`) has the same pattern.

## Hypothesis

Three changes together bring the CPU per stored chunk to about 55 to 60 % of
today, which on a CPU-bound host means about 1.6 to 1.8 times the fill rate per
CPU:

- passing the chunk type that pullsync and pushsync already established, instead
  of computing it again (about 7 %);
- recovering public keys with libsecp256k1 where the build allows it (about a
  third of the CPU, if the 5.7x ratio holds on the bench CPU);
- a cache in front of `ReserveHas`, only if measurement shows it pays.

The estimate comes from one profile. The precedent that matters is
[`sampler-bmt-hashing`](../sampler-bmt-hashing/results.md): a 1.3 to 2.2x gain in
a micro-benchmark and no change at all on a node. Only the node measurement in
this spec decides.

**The requirement for every part is that results are byte-identical to the
current code**: the same signatures accepted and rejected, the same recovered
keys, the same errors, the same stored chunk types and the same reserve index
entries. A part that cannot show that is not merged.

## Design

### Part A: one validation per chunk on the two verified paths

- `storage.ChunkType` does not change. It is also the integrity check in
  `chunkstore.Verify` and the reserve repair, and it feeds
  `MakeSampleUsingChunks` and the probe sample. Teaching it to trust a marker
  would let any future caller skip verification without noticing.
- `Reserve.Put` becomes a thin wrapper around an internal
  `put(ctx, ch, verified swarm.ChunkType)`. `Put` passes
  `swarm.ChunkTypeUnspecified`, which means "compute the type as today".
- A new method `ReservePutVerified(ctx, ch, t)` on the storer's reserve interface
  (and its mock) calls `put` with the given type. Only two callers use it:
  - pullsync, at the point where a delivered chunk is appended to the list of
    chunks to store, with the type established by its `cac.Valid` /
    `soc.FromChunkValidate` check on that same chunk;
  - the pushsync handler, inside its store step, with the type from its own
    `cac.Valid` / `soc.FromChunkValidate` check.
- Every other path that stores into the reserve (the pusher for local uploads,
  promotion of held chunks, `bee db import`) keeps calling `Put` and keeps the
  full check.
- **Shadow check.** When `put` receives a verified type it recomputes
  `storage.ChunkType` for 1 in 1,000 chunks (every 1,000th call, by an atomic
  counter, so the rate does not depend on a random source), and for every chunk
  when the binary
  runs under the race detector or `testing.Testing()` is true. On a mismatch it
  uses the recomputed type, logs at error level and increments
  `wasp_reserve_chunk_type_shadow_mismatch_total`. On a real node this keeps a
  running check at 0.1 % of the saved cost.

### Part B: libsecp256k1 behind a build tag

`pkg/crypto` gets an internal `recoverHash(sig65, hash32)`, used by `Recover` and
`RecoverEIP712`, in two files:

- `recover_nocgo.go`, build tag `!cgo || gofuzz`: today's decred code, moved
  without change.
- `recover_cgo.go`, build tag `cgo && !gofuzz`: libsecp256k1 through
  `github.com/ethereum/go-ethereum/crypto/secp256k1`, which vendors the C
  sources, so no system library is needed. go-ethereum is already a dependency;
  `go.mod` does not change.

The cgo path, step by step:

1. The 65-byte signature is checked as today. The last byte `v` is decred's
   compact-signature header. decred accepts 27 to 34: bit 2 marks a "compressed"
   key and does not change the recovered point, and the recovery id is
   `(v - 27) & 3`. Any `v` outside 27 to 34 goes straight to the decred path.
2. A **new** 65-byte buffer `r || s || recid` is built. The caller's slice is
   never written; stamp and SOC signatures are stored slices, and changing them
   would change the stamp's hash.
3. `secp256k1.RecoverPubkey(hash, buf)` returns `0x04 || X || Y`. The first byte
   is checked to be `0x04`.
4. The result is `&ecdsa.PublicKey{Curve: btcec.S256(), X, Y}`, decred's curve,
   the same object today's code returns. `ecdsa.PublicKey.Equal` compares the
   curve, so returning go-ethereum's curve would change results.
5. **Any libsecp256k1 rejection falls back to the decred path and returns its
   result.** The set of rejected signatures and the error values are therefore
   identical by construction. Rejections are rare, and in that case the cost is
   what it is today.

The two implementations reject the same inputs for recovery ids 0 to 3, as read
from their sources (decred v4.4.1 `ecdsa/signature.go`, libsecp256k1
`modules/recovery/main_impl.h`): r or s equal to 0 or at least the group order N,
the overflow bit with r at least P minus N, an x that is not on the curve, and a
result at infinity. Both reduce the hash modulo N, and neither rejects high-S
signatures. With the fallback in step 5, the only results the tests have to rule
out are "both accept, different keys" and "libsecp256k1 accepts where decred
rejects".

### cgo gate

Building with `CGO_ENABLED=1` changes more than the signature library. Three
dependencies have cgo code that the release builds exclude today:

- go-ethereum `crypto/secp256k1`: the intended change.
- pebble `internal/manual` and `internal/cache` (v1.1.5): memtable buffers and
  block-cache values are allocated with C `calloc` and released with `free`,
  instead of Go memory. No build tag keeps pebble on the Go path. Memory leaves
  the Go heap, so the node's resident memory, heap figures and garbage collection
  change, and a value slice kept after pebble releases it would become a
  use-after-free instead of a harmless extra reference.
- DataDog/zstd: used by pebble only when zstd compression is configured; the
  default is Snappy.

The `netgo` and `osusergo` build tags keep name resolution and user lookup in Go,
as they are today.

The test suite already runs with cgo enabled, so pebble's C allocator is already
exercised in CI; release nodes have never run it. Before any cgo build is
measured on a node or released:

1. **Audit.** Every `Unmarshal` of an item stored in pebble (all `storage.Item`
   implementations in `pkg/storer`, `pkg/storage` and elsewhere) copies its input
   and keeps no reference to it, and no `Get` or iterator value is used after it
   is closed. Any defect found is fixed in its own pull request with a test.
2. **Tests with cgo.** `pkg/storer/...` and `pkg/storage/...` with
   `CGO_ENABLED=1`, with and without `-race`; timing-sensitive tests 15 times.
3. **Soak.** On `bench-1`, 24 h each, one after the other: `main` built pure Go
   and `main` built with cgo, on copies of the same data. Recorded: resident
   memory, `go_memstats_heap_inuse_bytes`, garbage-collection cycles per second,
   OS threads, CPU per stored chunk, restarts, error log lines, and a full
   chunkstore verify. Pass: no errors, no restarts, 0 verify mismatches, memory
   within the node's configured limits.

### Part C: cgo Linux release builds

After the gate passes:

- Makefile: a `binary-cgo` target with `CGO_ENABLED=1`, `-tags netgo,osusergo`
  and `-linkmode external -extldflags '-static'` appended to the existing
  `LDFLAGS`, so the `-s -w -X ...` version stamping stays. `GOEXPERIMENT=nogreenteagc`
  stays.
- `.goreleaser.yml`: the `linux` and `linux-slim` builds are each split into a
  cgo build (amd64 and arm64, with `CC` set per architecture to `gcc` or
  `aarch64-linux-gnu-gcc`) and a pure-Go build (386 and arm v7). Every
  `archives`, `dockers` and `nfpms` reference is updated. The release workflow
  installs `gcc-aarch64-linux-gnu` and `libc6-dev-arm64-cross`.
- darwin, windows, linux 386 and linux arm v7 release builds stay pure Go.
- `Dockerfile` and `Dockerfile.dev` build with `binary-cgo` on Linux, so the
  images match the Linux release.
- CI checks: both cgo binaries are statically linked (`file`); `go version -m`
  shows `CGO_ENABLED=1`, `-tags=netgo,osusergo` and `GOEXPERIMENT=nogreenteagc`;
  the binary starts in the `scratch` image on amd64 and on arm64; `upx` works on
  both slim binaries; `goreleaser release --snapshot --clean` succeeds; the
  protocol fingerprint is unchanged.
- A plain `go build` on a machine with a C compiler already gets the cgo path
  today. This is stated in the release notes, with the memory change.

### Part D: a cache for `ReserveHas`, measured first

This part may be dropped.

1. **Measure first**, on `bench-1` during a fill: the share of `ReserveHas` calls
   that return true, and how much of its cost is the metrics wrapper in
   `DB.ReserveHas` (duration capture and label lookups) rather than pebble. If the
   wrapper dominates, that is fixed instead and no cache is built.
2. **If a cache is built:** a bounded set of keys that are known to be present,
   keyed by the `BatchRadiusItem` key. A miss always reads pebble, so the only
   risk is a stale "present", which would make the node skip a chunk it no longer
   has. Every path that deletes a `BatchRadiusItem` must therefore remove the key:
   - the replace branches inside `Reserve.Put`;
   - `RemoveChunkWithItem`, used by chunk replacement, eviction, batch expiry and
     the migrations;
   - `RemoveChunkMetaData` (pinned-chunk eviction);
   - `DeleteCorruptedChunkMetadata` (startup recovery);
   - `Reserve.Reset`.

   The cache is created empty after migrations and recovery have run, so the
   startup paths cannot leave stale keys. A key is inserted only after the
   reserve transaction returns without error, while the batch lock is still
   held. Removal happens after the eviction transaction returns, whether or not it
   failed, and `Reset` clears the whole cache.

## Protocol impact

None on the wire. No message, header, encoding or constant in
`.github/protocol-freeze.lock` changes, and peers cannot tell the difference.
Validation results are required to be byte-identical. Stored chunk types feed the
reserve sample, which is why every part carries an equivalence test against the
current code and the issue carries `consensus-risk`.

## Measurement

Rule 7 applies: at least three runs per condition, with the spread, per unit of
work.

### Correctness (required for merge, 0 differences)

- **Part B equivalence**, in-package tests that run both implementations in one
  binary:
  - random keys and messages signed with `Signer.Sign` (headers 27 and 28), plus
    compact signatures with the compressed flag (31 and 32) and with forced
    overflow-bit headers. The normal test run uses 10,000, so it stays fast under
    the race detector in CI; 1,000,000 run in the scheduled job and once before
    merge, selected by an environment variable;
  - every header value from 0 to 255 on valid signatures;
  - edge vectors at the hash level: r or s equal to 0, 1, N-1, N, N+1 and
    2^256-1; r at least P-N with and without the overflow bit; high-S (s replaced
    by N-s with the recovery id flipped); an x that is not on the curve; hashes 0,
    N and 2^256-1; a result at infinity, built by choosing k, R = kG, r = R.x, any
    s and hash e = s·k mod N;
  - the existing fixed vectors in `pkg/crypto/signer_test.go` and
    `pkg/soc/soc_test.go`, unchanged;
  - the input slices are byte-identical after `Recover`;
  - `FuzzRecoverEquivalence`, run with `CGO_ENABLED=1` pinned so it cannot
    silently compare decred with itself, seeded with the edge vectors. The fuzzer
    cannot see inside C code, so the structured vectors carry most of the weight;
  - every stamp and SOC signature from a copy of the `bench-1` reserve through both
    paths, plus mutants of each (each of the 65 bytes changed, every header 0 to
    255), with the counts reported.
- **Part A**: for a CAC, a SOC, a dispersed replica, an invalid SOC, a SOC
  overwrite with the same and a newer stamp, and a different chunk at the same
  stamp index, the index entries after `Put` and after `ReservePutVerified` are
  byte-identical (`batchRadius`, `chunkBin` including `ChunkType`, stamp index,
  chunk stamp, retrieval index). A wrong verified type is corrected by the shadow
  check and counted.
- **Part D**, if built: each delete path followed by `Has` returns false; a
  randomized model test of puts, evictions, expiries, replacements and resets
  compares cached and uncached `Has` after every step; a race-detector test with
  concurrent puts, evictions and lookups.
- **Mutation checks** for each new test, including: dropping the `& 3`; dropping
  the 27 to 34 range check; writing into the caller's slice; returning
  go-ethereum's curve; skipping the decred fallback; passing a wrong type from
  pullsync; skipping the shadow check; dropping one cache removal.
- **CI** gains `CGO_ENABLED=0` test and lint runs for `pkg/crypto`, `pkg/soc`,
  `pkg/postage`, `pkg/bzz` and `pkg/pushsync`, because the normal test jobs run
  with cgo and would otherwise never test or lint the pure-Go file.

### Replay test on real deliveries

Real pullsync deliveries (chunk data, stamp, batch) recorded on `bench-1` are fed
through `main` and through the branch into fresh stores, in pure-Go and cgo
builds. Every namespace is compared byte for byte, and the outcome of
`ValidStamp` and `Put` for each chunk is compared, error included. The recording
includes SOC overwrites, dispersed replicas and invalid chunks. Pass: 0
differences.

### Effect on a node

- **Primary measure: CPU seconds per stored chunk**, process CPU ticks divided by
  reserve growth. The fill rate alone cannot show a gain, because the puller caps
  each node at 1,000 chunks per second.
- Two throwaway full nodes on `bench-1` in one neighbourhood, fresh data
  directories, arms in the order A B A B A B, 900 s each. A is `main`, B is the
  branch. For Part B, both arms are built with cgo, so the arm difference is the
  library alone, and one pure-Go `main` arm is added as the cost baseline.
- Also recorded per arm: resident memory, heap in use, garbage-collection cycles
  per second, OS threads (each cgo call can make the scheduler start extra
  threads). One extra run with the puller cap raised shows the throughput.
- The recovery micro-benchmark is repeated on the bench CPU, each condition in
  its own process, 5 runs.
- On the reserves the arms filled: a full chunkstore verify with 0 mismatches,
  the shadow counter at 0, and the sampler harness with pinned inputs giving the
  same sample for reserves filled by A and by B.

**Pass rule.** B's CPU per stored chunk is lower than A's by more than the spread
of the A runs, and every correctness check is at 0.

**Negative result, stated in advance.** If B is not lower by more than the spread,
the result is `neutral`, the change is not shipped, and the finding is recorded
in the ledger, as `sampler-bmt-hashing` was.

## Rollout and rollback

- Parts A, B and D are code changes with no on-disk or wire effect. Rollback is
  the previous build.
- Part B only takes effect in cgo builds. A pure-Go build behaves exactly as
  today.
- Order on real nodes, each step a separate operator decision: one staked node
  first, then an unstaked host that is filling, then the main fleet in stages.
- The cgo Linux release (Part C) happens only after the gate has passed and the
  staked node and the filling host have run a cgo build without problems. Until
  then, cgo builds are built for the fleet only.

## Upstream portability

- Part A and Part D are plain Go in `pkg/pullsync`, `pkg/pushsync` and
  `pkg/storer` and port to Bee as they are. The double check in `Reserve.Put` is
  upstream's design, not a defect, so the issue does not carry `affects-upstream`.
- Part B ports as two files and a build tag; Bee also builds releases with
  `CGO_ENABLED=0`, so Bee would need Part C's build change and the same cgo gate
  to benefit.

## Configuration

No new flags. Additions:

- the metric `wasp_reserve_chunk_type_shadow_mismatch_total`, with a row in
  `docs/DIFFERENCES.md`;
- the shadow-check rate, 1 in 1,000, as a code constant. It is a safety check,
  not a tuning constant: raising it costs a little CPU, lowering it only reduces
  the check. It becomes a flag only if a measurement shows a reason (rule 8);
- the cgo Linux build, with the Dockerfile change and release notes, as packaging
  rows in `docs/DIFFERENCES.md`.

Generated with help of AI.
