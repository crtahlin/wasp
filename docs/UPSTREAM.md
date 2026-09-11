# Bee-origin issues

Every wasp issue that also applies to upstream Bee, that is, one carrying the
`affects-upstream` label, which means the defect or gap was confirmed in
unmodified Bee code. This is the set a maintainer could one day offer upstream.

By the fork rules this file does not link to the Bee repository, and nothing here
authorises contacting Bee or opening anything there; the `affects-upstream`
label is a marker for a later human decision and nothing more. The issues and
commits below are wasp's own.

- **Status** is `done` when the change is merged on `main`, with the ledger's
  outcome added where it has one (`validated` measured on a real node,
  `neutral` no measurable effect). `not planned` means the issue was closed
  without a wasp change. `(via #N)` or `(bundled with #N)` means the fix
  landed inside another issue's pull request.
- **Branch** is the fork branch the work was built on. Fork branches are never
  deleted, so each one still exists. A dash means the fix shared another issue's
  branch.
- **Commit** links the merge commit on `main`.

Derived from the experiment ledger ([`experiments/INDEX.md`](experiments/INDEX.md))
and the git history on 2026-09-11, against the upstream base
`v2.8.2`. Two issues (#73, #76) were closed not planned. Wasp issue numbers can
collide with upstream Bee pull-request numbers in the shared history, so the
commits here were resolved from fork-only merges, not by issue number alone.

**Table: wasp issues that also apply to upstream Bee, with how each was handled**

| Issue | Title | Status | Branch | Commit |
|---|---|---|---|---|
| [#21](https://github.com/crtahlin/wasp/issues/21) | Improve peer discovery and reduce aggressive pruning | done (via #148) | `feat/148-configurable-saturation` | [`af9f5fe6`](https://github.com/crtahlin/wasp/commit/af9f5fe6) |
| [#22](https://github.com/crtahlin/wasp/issues/22) | Scale the isFullySynced threshold by reserve capacity doubling | done (via #223) | `feat/223-sync-rate-threshold` | [`b9e572aa`](https://github.com/crtahlin/wasp/commit/b9e572aa) |
| [#24](https://github.com/crtahlin/wasp/issues/24) | Revert CompactionL0Trigger to the LevelDB default | done, neutral | `exp/24-l0-depth-bench` | [`8db48cb9`](https://github.com/crtahlin/wasp/commit/8db48cb9) |
| [#28](https://github.com/crtahlin/wasp/issues/28) | countWithinRadius performs a full O(n) reserve scan every wake-up | done, validated | `fix/28-batch-exists-cache` | [`3d1d3d21`](https://github.com/crtahlin/wasp/commit/3d1d3d21) |
| [#34](https://github.com/crtahlin/wasp/issues/34) | Reserve size within radius is computed two different ways | done | `fix/34-reserve-size-two-ways` | [`7f1da3d3`](https://github.com/crtahlin/wasp/commit/7f1da3d3) |
| [#35](https://github.com/crtahlin/wasp/issues/35) | Add descriptions to OpenAPI schema properties | done (via #192) | - | [`e999eaae`](https://github.com/crtahlin/wasp/commit/e999eaae) |
| [#36](https://github.com/crtahlin/wasp/issues/36) | Fix typos and parameter naming in the OpenAPI spec | done (via #192) | - | [`e999eaae`](https://github.com/crtahlin/wasp/commit/e999eaae) |
| [#69](https://github.com/crtahlin/wasp/issues/69) | fix(build): Green Tea GC in Go 1.26 corrupts the heap — build with GOEXPERIMENT=nogreenteagc | done, validated | `fix/69-disable-greenteagc` | [`9702319c`](https://github.com/crtahlin/wasp/commit/9702319c) |
| [#73](https://github.com/crtahlin/wasp/issues/73) | Node segfaults in goleveldb's block-cache buffer pool after the Green Tea fix | not planned | - | - |
| [#74](https://github.com/crtahlin/wasp/issues/74) | Node reports ready with zero peers and never recovers — p2p dial breaker stays tripped | done, validated | `fix/74-breaker-backoff-reset` | [`22dfbdd5`](https://github.com/crtahlin/wasp/commit/22dfbdd5) |
| [#76](https://github.com/crtahlin/wasp/issues/76) | Node dies with concurrent map writes in peerRegistry.addStream under load | not planned | - | - |
| [#77](https://github.com/crtahlin/wasp/issues/77) | SIMD BMT hasher segfaults in hashLeavesBatch — nil node dereference | done | `fix/77-warn-on-simd` | [`f18520b2`](https://github.com/crtahlin/wasp/commit/f18520b2) |
| [#79](https://github.com/crtahlin/wasp/issues/79) | Flaky CI: pkg/api hangs on macOS, TestDebugInfo/disk fails on Windows | done | `fix/79-async-test-deadlines` | [`bb6bbb73`](https://github.com/crtahlin/wasp/commit/bb6bbb73) |
| [#85](https://github.com/crtahlin/wasp/issues/85) | Node with zero peers cannot self-recover: breaker blocks the bootnode fallback | done, validated | `fix/74-breaker-never-isolates` | [`2e3016b7`](https://github.com/crtahlin/wasp/commit/2e3016b7) |
| [#89](https://github.com/crtahlin/wasp/issues/89) | XKCP generator emits a stack frame smaller than the code it generates needs | done | `fix/89-generated-frame-too-small` | [`4b57e508`](https://github.com/crtahlin/wasp/commit/4b57e508) |
| [#90](https://github.com/crtahlin/wasp/issues/90) | keccak .syso blobs cannot be rebuilt from source; build fails on modern gcc | done, validated | `fix/90-rebuildable-keccak-blobs` | [`c950d7b6`](https://github.com/crtahlin/wasp/commit/c950d7b6) |
| [#91](https://github.com/crtahlin/wasp/issues/91) | XKCP go_wrapper writes out of bounds when a lane is shorter than the longest | done, validated | `fix/91-xkcp-oob-write` | [`b5691b68`](https://github.com/crtahlin/wasp/commit/b5691b68) |
| [#92](https://github.com/crtahlin/wasp/issues/92) | SIMD assembly stub runs foreign code on the goroutine stack and passes Go pointers to it | done, validated | `fix/92-scratch-stack` | [`8fe665dd`](https://github.com/crtahlin/wasp/commit/8fe665dd) |
| [#99](https://github.com/crtahlin/wasp/issues/99) | TestGetter cancellation waits are bounded at scheduling-latency timescale | done, validated | `fix/replicas-getter-timing-flake` | [`1e389b93`](https://github.com/crtahlin/wasp/commit/1e389b93) |
| [#109](https://github.com/crtahlin/wasp/issues/109) | blockchain-rpc-endpoint accepts only one endpoint, and a 10-minute outage shuts the node down | done, validated | `feat/109-rpc-failover` | [`d5d36cb1`](https://github.com/crtahlin/wasp/commit/d5d36cb1) |
| [#114](https://github.com/crtahlin/wasp/issues/114) | goleveldb races on close when a writer is paused at the L0 trigger | done (bundled with #115) | `fix/115-close-without-writing` | [`ac7f7365`](https://github.com/crtahlin/wasp/commit/ac7f7365) |
| [#115](https://github.com/crtahlin/wasp/issues/115) | Store.Close writes, so a stalled index store cannot shut down cleanly | done | `fix/115-close-without-writing` | [`ac7f7365`](https://github.com/crtahlin/wasp/commit/ac7f7365) |
| [#122](https://github.com/crtahlin/wasp/issues/122) | leveldbstore cannot open a database read-only | done | `fix/readonly-open` | [`1e89e4b6`](https://github.com/crtahlin/wasp/commit/1e89e4b6) |
| [#128](https://github.com/crtahlin/wasp/issues/128) | TestPostageAccessHandler deadlocks when its 100ms sleep is not enough | done | `fix/128-postage-test-deadlock` | [`fdcd84a6`](https://github.com/crtahlin/wasp/commit/fdcd84a6) |
| [#138](https://github.com/crtahlin/wasp/issues/138) | Four hive ping metrics are declared but never observed | done | `fix/138-dead-hive-metrics` | [`261e452f`](https://github.com/crtahlin/wasp/commit/261e452f) |
| [#146](https://github.com/crtahlin/wasp/issues/146) | Seven storage benchmarks fail on Go 1.26, and the rest depend on which selection is run | done | `fix/146-storage-benchmark-harness` | [`e915041e`](https://github.com/crtahlin/wasp/commit/e915041e) |
| [#156](https://github.com/crtahlin/wasp/issues/156) | A blocked log sink deadlocks peer connectivity: node goes deaf and never recovers | done | `fix/156-log-sink-backpressure` | [`de4cfdca`](https://github.com/crtahlin/wasp/commit/de4cfdca) |
| [#158](https://github.com/crtahlin/wasp/issues/158) | kademlia's manage loop waits on dials without a bound | done | `fix/158-bounded-connector-wait` | [`14ae5e67`](https://github.com/crtahlin/wasp/commit/14ae5e67) |
| [#160](https://github.com/crtahlin/wasp/issues/160) | The chunkstore benchmarks read addresses that could never exist | done | `fix/160-chunkstore-benchmark-addresses` | [`b792d653`](https://github.com/crtahlin/wasp/commit/b792d653) |
| [#162](https://github.com/crtahlin/wasp/issues/162) | ReadRandomMissing measures range rejection, not a missing-key lookup | done | `fix/162-missing-key-interleaved` | [`f810581a`](https://github.com/crtahlin/wasp/commit/f810581a) |
| [#167](https://github.com/crtahlin/wasp/issues/167) | kademlia's bounded dial wait ignores shutdown | done | `fix/167-shutdown-aware-connector-wait` | [`c5feed0b`](https://github.com/crtahlin/wasp/commit/c5feed0b) |
| [#173](https://github.com/crtahlin/wasp/issues/173) | The store read benchmarks charge harness overhead to the engine | done | `fix/173-store-read-benchmark-overhead` | [`fcb110f0`](https://github.com/crtahlin/wasp/commit/fcb110f0) |
| [#176](https://github.com/crtahlin/wasp/issues/176) | A write-stalled index store has no in-process recovery | done | `fix/176-write-pause-log-line` | [`150d644c`](https://github.com/crtahlin/wasp/commit/150d644c) |

Generated with help of AI.
