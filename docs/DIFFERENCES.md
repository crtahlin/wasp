# Differences from upstream Bee

This document lists every way wasp differs from upstream Bee
([`ethersphere/bee`](https://github.com/ethersphere/bee)): what a node does with
no configuration, what it can be configured to do, what it exposes, and how it
is built and packaged.

- **Compared with:** Bee **v2.8.2**, the latest released version of upstream Bee
  on 2026-09-10.
- **wasp described:** `main` at `2b9233f5`, 2026-09-10. The latest wasp release is
  v0.1.3.
- **wasp's upstream base:** v2.8.2, recorded in [`.upstream-base`](../.upstream-base).

The comparison is always with the latest **released** Bee, never with upstream
`master`. An operator choosing between the two clients chooses between releases,
and `master` can carry work that is later changed or reverted. When Bee publishes
a release that wasp has not absorbed yet, the comparison moves to that release:
entries the release covers are removed, and what the release adds appears under
[Bee changes wasp does not have yet](#bee-changes-wasp-does-not-have-yet). How and
when to refresh this document is rule 13 in [`AGENTS.md`](../AGENTS.md).

How to read the tables:

- **In wasp** is the first wasp release that contains the change. `main` means it
  is merged but not yet in a release.
- **Bee defect** is `Yes` when the issue carries the `affects-upstream` label,
  which means the defect was confirmed in unmodified Bee code. A blank cell means
  the issue has no such label. It is not a claim that Bee is right.
- **Record** links the issue, or the pull request where there is no issue.

For how each change was measured, see the [experiment ledger](experiments/INDEX.md).
For every change in full, run `git log --first-parent main`.

## What stays the same

- **The wire protocol.** Protocol versions, the handshake, chunk format and network
  ID are fingerprinted in `.github/protocol-freeze.lock`, and CI checks them on
  every pull request. A wasp node and a Bee node talk to each other as two Bee
  nodes would. See
  [`protocol-compatibility.md`](agent-playbooks/protocol-compatibility.md).
- **Paths and names.** The binary is still `bee`, the Go module path is still
  `github.com/ethersphere/bee/v2`, and the configuration file, systemd unit and
  data directory are the ones Bee uses. An existing Bee data directory is used as
  it is.

## Behaviour that differs with no configuration

**Table: behaviour a wasp node shows by default, compared with Bee v2.8.2**

| Area | Bee v2.8.2 | wasp | In wasp | Bee defect | Record |
|---|---|---|---|---|---|
| Dial backoff | The dial circuit breaker never resets its backoff after a successful dial, so the backoff only grows, up to one hour. | The backoff resets after a successful dial. | v0.1.0 | Yes | [#74](https://github.com/crtahlin/wasp/issues/74) |
| Zero peers | With every known peer in backoff, the breaker refuses every dial, so a node at zero peers stays at zero until restarted. | A dial is always allowed when the node has zero peers. | v0.1.0 | Yes | [#85](https://github.com/crtahlin/wasp/issues/85) |
| Dials and shutdown | The peer-management loop waits on dials without a time limit, and a stuck dial holds up shutdown. | The wait has a limit, and a stuck dial no longer blocks shutdown. | v0.1.1, v0.1.2 | Yes | [#158](https://github.com/crtahlin/wasp/issues/158), [#167](https://github.com/crtahlin/wasp/issues/167) |
| `/readiness` | Reports ready with zero connected peers. | Requires at least one connected peer. | v0.1.0 | Yes | [#74](https://github.com/crtahlin/wasp/issues/74) |
| Logging | Log lines are written synchronously, so a stalled log reader blocks the node until it stops talking to peers. | Log lines go through a buffer of 4,096 lines, and lines are dropped when it is full. Set with `log-sink-buffer`. | v0.1.1 | Yes | [#156](https://github.com/crtahlin/wasp/issues/156) |
| SIMD hashing, safety | SIMD hashing (BMT hashing with AVX2 or AVX-512 CPU instructions) runs its assembly on the goroutine stack, which Go can move. This corrupts memory and crashed nodes within about 12 minutes under load. | The assembly runs on a separate, pooled stack. A warning is logged when SIMD hashing is on. | v0.1.0 | Yes | [#92](https://github.com/crtahlin/wasp/issues/92) |
| SIMD hashing, default | Off unless `use-simd-hashing` is set. | On where the CPU supports it. Measured 25% faster reserve sample, 92.0 s to 69.0 s over 243 runs. | v0.1.0 | | [#54](https://github.com/crtahlin/wasp/issues/54) |
| SIMD code | The machine-code blobs cannot be rebuilt from source on a current toolchain, the C wrappers write out of bounds for short lanes, and the code generator emits a stack frame that is too small. | The blobs rebuild byte for byte with `scripts/rebuild-keccak-syso.sh`, the wrappers clamp the index, and the generated frame is large enough. | v0.1.0 | Yes | [#90](https://github.com/crtahlin/wasp/issues/90), [#91](https://github.com/crtahlin/wasp/issues/91), [#89](https://github.com/crtahlin/wasp/issues/89) |
| Garbage collector | Built with the default Go 1.26 collector, Green Tea, which corrupts the heap under this workload. | Built with `GOEXPERIMENT=nogreenteagc`. This is a build setting, so it cannot be changed at run time. | v0.1.0 | Yes | [#69](https://github.com/crtahlin/wasp/issues/69) |
| Index store shutdown | Closing the store writes a key, so a store that has stopped accepting writes cannot shut down cleanly. | The unclean-shutdown marker is a file inside the store directory, removed after a clean close. Closing writes nothing to the store. | v0.1.0 | Yes | [#115](https://github.com/crtahlin/wasp/issues/115) |
| Read-only open | Opening the index store read-only fails, because the open writes a marker. | Works. The unclean-shutdown flag from the last writer is still reported. | v0.1.0 | Yes | [#122](https://github.com/crtahlin/wasp/issues/122) |
| Reserve scan | Checks the batch store once per chunk, about 4 million lookups per pass on a full reserve. | Checks once per batch. The result is the same. | v0.1.0 | Yes | [#28](https://github.com/crtahlin/wasp/issues/28) |
| Reserve size | Counted two different ways for `/status` and `/debugstorage`. | One definition serves both. | v0.1.0 | Yes | [#34](https://github.com/crtahlin/wasp/issues/34) |
| Chequebook API | Available balance returns 500 when the chain is disabled. | Returns 405, like the other chain-dependent endpoints. | v0.1.0 | | [#33](https://github.com/crtahlin/wasp/issues/33) |
| Index engine for a new data directory | goleveldb. | Pebble. An existing goleveldb store stays goleveldb, so no running node is converted. In v0.1.3 Pebble can be chosen but is not the default. | `main` | | [#185](https://github.com/crtahlin/wasp/issues/185) |
| Pull-sync during sampling | Historical syncing continues while the node computes its reserve sample, the proof of storage for a redistribution round. | Historical syncing pauses while the sample runs. Push-sync and retrieval continue. | `main` | | [#23](https://github.com/crtahlin/wasp/issues/23) |
| Advertised address behind NAT | Rebuilt on every handshake from how each peer sees the node, so it changes with every NAT port mapping. The node re-signs and re-advertises it until it can lose all its peers. | Pinned once a public address is seen, and changed only after a sustained run of handshakes shows a different public IP. No change for a node with `nat-addr` set. | `main` | | [#225](https://github.com/crtahlin/wasp/pull/225) |
| Push-sync on a node using reserve capacity doubling | Decides whether to store a pushed chunk directly from the lowered storage radius. | Decides from the committed depth, the storage radius plus the doubling, so its receipts are never shallower than the network radius. No change for a node without doubling. | `main` | | [#222](https://github.com/crtahlin/wasp/pull/222) |
| Stake in a retired staking contract | Stays stranded; recovering it needs an old release and manual contract calls. | On startup the node recovers it into the current contract and restakes, by default. Set `stake-recovery-on-startup` to `off` to disable, or `withdraw` to recover to the wallet. No change for a node with nothing stranded. | `main` | | [#256](https://github.com/crtahlin/wasp/issues/256) |

## Existing Bee settings that behave differently

**Table: Bee v2.8.2 settings whose meaning differs in wasp**

| Setting | Bee v2.8.2 | wasp | In wasp | Bee defect | Record |
|---|---|---|---|---|---|
| `blockchain-rpc-endpoint` | One endpoint. When it is unreachable for 10 minutes, the postage listener stall timeout shuts the node down. | A list, in priority order. Reads fail over to the next endpoint; transaction sends do not, because a lost response can mean the transaction was accepted. An endpoint that was down at startup is retried later, and its chain ID is checked on every connection. | v0.1.0 | Yes | [#109](https://github.com/crtahlin/wasp/issues/109), [#119](https://github.com/crtahlin/wasp/issues/119) |
| `use-simd-hashing` | Unset means off. | Unset means on where the CPU supports it (linux/amd64 with AVX2 or AVX-512). Setting it explicitly behaves as in Bee. | v0.1.0 | | [#54](https://github.com/crtahlin/wasp/issues/54) |

## Settings wasp adds

wasp turns a fixed value that measurably matters into a setting, with Bee's value
as the default (rule 8 in [`AGENTS.md`](../AGENTS.md)). Unless noted, a node that
leaves these settings unset behaves as Bee does. Every setting is described,
including what raising and lowering it costs, in
[`config-reference.yaml`](config-reference.yaml).

**Table: settings wasp adds to Bee v2.8.2, with their defaults**

| Setting | Default | What it controls | In wasp | Record |
|---|---|---|---|---|
| `storage-engine` | The engine the data directory already uses; Pebble for a new directory on `main`, goleveldb in v0.1.3. | Index store engine, `pebble` or `leveldb`. A directory stays bound to the engine it was created with. | v0.1.3 | [#187](https://github.com/crtahlin/wasp/pull/187) |
| `db-compaction-l0-trigger` | 8, as in Bee. A Pebble store uses its own default of 4 unless this is set to a value other than 8. | Level-0 files at which the index store starts compacting. | v0.1.2 | [#183](https://github.com/crtahlin/wasp/pull/183) |
| `db-write-slowdown-trigger` | 8, as in Bee. | Level-0 files at which the index store starts slowing writes. | v0.1.2 | [#183](https://github.com/crtahlin/wasp/pull/183) |
| `db-write-pause-trigger` | 12, as in Bee. | Level-0 files at which the index store stops accepting writes until compaction catches up. | v0.1.2 | [#183](https://github.com/crtahlin/wasp/pull/183) |
| `sampler-read-concurrency` | The CPU count, at least 4, as in Bee. | Chunk reads the reserve sampler keeps in progress. The sampler now reads and hashes in separate worker pools. | v0.1.0 | [#9](https://github.com/crtahlin/wasp/issues/9) |
| `sampler-sort-window` | 0, reads in address order as Bee does. | Chunks the sampler buffers and sorts into disk order before reading. | v0.1.0 | [#11](https://github.com/crtahlin/wasp/issues/11) |
| `reserve-has-concurrency` | 0, unlimited as in Bee. | Reserve lookups pull-sync may have in progress at once. | v0.1.0 | [#20](https://github.com/crtahlin/wasp/issues/20) |
| `reserve-wakeup-duration` | 15 minutes, as in Bee. | Time between runs of the reserve worker. | v0.1.0 | [#58](https://github.com/crtahlin/wasp/issues/58) |
| `reserve-batch-sweep-interval` | 0, which checks chunks against their batches on every wake-up, as Bee does. | How often the reserve checks chunks against their batches and evicts chunks whose batch is gone. | `main` | [#201](https://github.com/crtahlin/wasp/pull/201) |
| `max-reserve-capacity-doubling` | 1, as in Bee. | Largest allowed value of Bee's `reserve-capacity-doubling`. | `main` | [#222](https://github.com/crtahlin/wasp/pull/222) |
| `redistribution-sync-rate-threshold` | 0, which requires a fully settled sync rate, as Bee does. | Sync rate below which the node counts itself synced enough to enter a redistribution round. | `main` | [#22](https://github.com/crtahlin/wasp/issues/22) |
| `pullsync-max-chunks-per-second` | 250, as in Bee. | Rate this node serves to each peer that pulls from it. | v0.1.0 | [#25](https://github.com/crtahlin/wasp/issues/25) |
| `puller-max-chunks-per-second` | 1,000, as in Bee. | Total rate this node pulls from all peers. | v0.1.0 | [#26](https://github.com/crtahlin/wasp/issues/26) |
| `pullsync-recalc-interval` | 5 minutes, as in Bee. | How often the puller chooses which peers to sync from. | v0.1.0 | [#59](https://github.com/crtahlin/wasp/issues/59) |
| `kademlia-saturation-peers` | 8, as in Bee. | Connected peers per bin below which the bin keeps looking for peers. | v0.1.0 | [#148](https://github.com/crtahlin/wasp/pull/148) |
| `kademlia-over-saturation-peers` | 18, as in Bee. | Connected peers per bin above which further peers are disconnected. | v0.1.0 | [#148](https://github.com/crtahlin/wasp/pull/148) |
| `log-sink-buffer` | 4,096 lines. **Differs from Bee**, which writes synchronously; 0 restores that. | Log lines that may wait to be written before further lines are dropped. | v0.1.1 | [#156](https://github.com/crtahlin/wasp/issues/156) |
| `stake-recovery-on-startup` | `migrate`. **Moves staked funds by default:** a node with stake in a retired contract restakes it into the current contract on startup. `off` disables it; `withdraw` recovers to the wallet instead. It is a no-op for a node with nothing stranded. | Whether, and how, the node recovers stake from retired staking contracts at startup. | `main` | [#256](https://github.com/crtahlin/wasp/issues/256) |

The storer's shutdown wait is also adjustable, but only as `Options.ShutdownTimeout`
in the Go API, not as a node setting. Its default is 3 seconds, as in Bee
([#82](https://github.com/crtahlin/wasp/pull/82)).

## API endpoints wasp adds

Both groups sit in the business-debug route group, next to Bee's `/stake`
endpoints.

**Table: HTTP endpoints wasp adds to Bee v2.8.2**

| Endpoint | What it does | In wasp | Record |
|---|---|---|---|
| `GET /stake/legacy` | Lists stake the node holds in retired staking contracts. | `main` | [#256](https://github.com/crtahlin/wasp/issues/256) |
| `POST /stake/legacy?mode=withdraw\|migrate` | Recovers stake from every retired contract that holds some. | `main` | [#256](https://github.com/crtahlin/wasp/issues/256) |
| `GET /stake/legacy/{id}`, `POST /stake/legacy/{id}?mode=withdraw\|migrate` | Reports recovery progress for one retired contract, or recovers from it. | `main` | [#256](https://github.com/crtahlin/wasp/issues/256) |
| `GET /probesample/{depth}/{anchor}/{k}` | Measures the cost of a probe-based reserve sample. For benchmarking only; it does not change what the node submits in redistribution. | `main` | [#241](https://github.com/crtahlin/wasp/issues/241) |

The list of retired staking contracts in `pkg/config/legacy_staking.go` is seeded
with the verified retired deployments, the two most recent per chain on Gnosis
and Sepolia, so the `/stake/legacy` endpoints and `stake-recovery-on-startup` act
on a node that has stake stranded in one of them.

## Metrics and log lines

**Table: metrics and log lines wasp adds or removes, compared with Bee v2.8.2**

| Change | In wasp | Bee defect | Record |
|---|---|---|---|
| Adds index store level-0 depth and write-pause state. The Bee histogram for level-0 depth stops at 10, below the depth that matters. | v0.1.0 | | [#113](https://github.com/crtahlin/wasp/pull/113) |
| Adds the duration of each pass over the reserve index. | v0.1.0 | | [#137](https://github.com/crtahlin/wasp/pull/137) |
| Adds `bee_localstore_reserve_has_wait_duration`, time spent waiting for a reserve lookup slot. | v0.1.0 | | [#134](https://github.com/crtahlin/wasp/pull/134) |
| Adds the read concurrency used to the reserve sample statistics. | v0.1.0 | | [#120](https://github.com/crtahlin/wasp/pull/120) |
| Adds a log line when the index store stops accepting writes. | v0.1.2 | | [#180](https://github.com/crtahlin/wasp/pull/180) |
| Adds Pebble's physical write bytes. | `main` | | [#218](https://github.com/crtahlin/wasp/pull/218) |
| Removes four hive ping metrics that Bee declares but never records. | v0.1.0 | Yes | [#138](https://github.com/crtahlin/wasp/issues/138) |

## Version, identity and packaging

- **Version numbers.** wasp has its own version line, starting at v0.1.0. `bee
  version` prints `wasp <version> (upstream bee <base>)`.
- **Startup warning.** The node logs that it is wasp, an unofficial experimental
  build that the Swarm Foundation does not support.
- **libp2p user agent.** Peers see `bee/<base> wasp/<version> <go version>
  <os>/<arch>` instead of `bee/<version> ...`. The handshake checks only the
  protocol version and network ID, never the user agent.
- **Packages.** The `.deb` and `.rpm` packages are named `wasp`. They install the
  same binary path, systemd unit and configuration file as Bee's package, declare
  that they replace it, and carry epoch 1 so that `apt` treats them as an upgrade.
  They also upgrade an install of this project's earlier `bee-experimental` package
  ([#178](https://github.com/crtahlin/wasp/pull/178)). Reinstalling Bee's package
  rolls back.
- **Package metadata.** The licence is declared as BSD 3-Clause; Bee's release
  configuration declares GPL-3, which is wrong for Bee too. The Debian section is
  set; Bee leaves it blank ([#152](https://github.com/crtahlin/wasp/pull/152)).
- **Where releases go.** GitHub Releases and `ghcr.io/crtahlin/wasp` only. There is
  nothing on Docker Hub, Quay, Gemfury, Homebrew or Scoop, no GPG signing, no
  `stable` image tag, and no ARM container images
  ([#51](https://github.com/crtahlin/wasp/pull/51)).

## Repository and CI

These do not change the binary.

- Removed Bee's CI workflows that need Ethersphere secrets or infrastructure: the
  beekeeper integration clusters, the OpenAPI documentation preview, the Codecov
  upload and the swarm-cli version dispatch.
- Added the protocol-freeze check, a weekly workflow that merges new Bee release
  tags, a changelog built from merge commits, a check that every merge reaches the
  changelog, and checks on package upgrades after each release.
- Added a storage benchmark harness (`beebench/` and the storage engine
  evaluation tools) and made Bee's storage benchmarks run and measure what they
  claim to ([#146](https://github.com/crtahlin/wasp/issues/146)).
- Fixed tests that fail on a loaded CI runner because they wait a fixed time:
  [#99](https://github.com/crtahlin/wasp/issues/99) and
  [#128](https://github.com/crtahlin/wasp/issues/128), both also present in Bee.

## Bee changes wasp does not have yet

Compared with Bee v2.8.2: **none**. wasp's upstream base is v2.8.2, so everything
in that release is in wasp.

This section lists changes from a Bee release that wasp has not absorbed yet.
Changes on Bee's `master` that are not in a release do not belong here.

## Tried and removed

These are not differences. They are listed so that nobody looks for them in the
code. The reasons are in the [experiment ledger](experiments/INDEX.md).

- Concurrent reads in Sharky, the chunk data store: merged twice, reverted twice.
  Ten times faster in a benchmark, no measurable change on a node
  ([#8](https://github.com/crtahlin/wasp/issues/8)).
- A synchronous BMT hasher for the reserve sampler: faster in a benchmark, no
  change on a node ([#236](https://github.com/crtahlin/wasp/issues/236)).
- A lower default level-0 compaction trigger for goleveldb, and a larger goleveldb
  block cache: neither helped ([#24](https://github.com/crtahlin/wasp/issues/24),
  [#12](https://github.com/crtahlin/wasp/issues/12)). Both remain available as
  settings.
