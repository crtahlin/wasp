# Spec: recover stranded stake from any historical staking contract

Issue: [#256](https://github.com/crtahlin/wasp/issues/256). Type: feature. Area: incentives.
Risk: moves staked funds, so the runtime default is off.

## Problem

When the Swarm network replaces its staking contract, a node's stake can remain in the old,
now-retired contract. Recovering it today means running an old release, restarting to
migrate, and supplying the old contract address and ABI by hand. That is awkward for
automated deployments and discourages contract upgrades.

The primitives already exist. The staking client in `pkg/storageincentives/staking/contract.go`
implements `DepositStake`, `WithdrawStake`, `MigrateStake` (which first checks the contract is
`paused`), `GetWithdrawableStake`, and `GetPotentialStake`, and its constructor
`staking.New(owner, address, abi, bzzToken, txService, nonce, gasLimit, height)` already takes
the address and ABI as parameters. The transaction service in `pkg/transaction` sends a call to
any address. The gap is that the node wires exactly one staking client, at the current
`chainCfg.StakingAddress` (`pkg/node/node.go:1313`), and neither `pkg/config/chain.go` nor
`go-storage-incentives-abi v0.9.4` keeps any past address. Once the node's configuration
advances to the new contract, it can no longer reach the old one.

## Hypothesis

A small fork-owned catalog of historical staking deployments, plus the existing client aimed
at each of them, is enough to let a node discover and recover stranded stake, both on demand
and, on explicit opt-in, at startup. Because the recovery primitives already exist, the new
code is mostly the catalog, the wiring, the discovery and recovery orchestration with durable
state, and the API. The runtime default is off, so an upgrading node changes no behavior until
an operator opts in.

## Design

### The catalog

A fork-owned table, one set of entries per chain, next to the current chain configuration in
`pkg/config/`. Each entry:

- `ID` string, a stable deployment identifier (the storage-incentives release tag).
- `ChainID` int64.
- `Address` common.Address, the retired contract.
- `DeploymentBlock` uint64.
- `ABI` string, the minimal ABI for the query and recovery calls.
- `RecoverMethod`, an enum recording how this contract pays out, since older contracts differ:
  some release funds through `withdrawFromStake` while paused, others through `migrateStake` on
  the paused contract.

Operators never supply a raw address or ABI; they refer to the `ID`. The table is seeded from
the storage-incentives release history; the actual addresses are filled in as the entries are
added, and an entry is only added once its address and recovery method are confirmed.

### The legacy client

For each catalog entry on the node's chain, build the existing `staking.Contract` with the
entry's address and ABI, sharing the one `transactionService`, the node's owner address, the
BZZ token address, and the gas limit already used for the current client (the wiring at
`pkg/node/node.go:1313` is the reference). This yields `GetWithdrawableStake`,
`GetPotentialStake`, `WithdrawStake`, and `MigrateStake` against the old contract with no new
contract code. A slim read-only query wrapper may be added if the full client pulls in
deposit-only validation that does not apply to a legacy contract, but reuse is the default.

### Recovery state, for idempotency

Per (chain, deployment ID) recovery state is stored in the statestore, so a partly finished
recovery is visible and a restart resumes rather than repeats. States:

- `idle`: nothing started.
- `withdrawn`: funds recovered out of the legacy contract, not yet redeposited (migrate only).
- `done`: recovered, and for migrate also redeposited.

Every step re-reads on-chain state before acting, so a crash between the transaction and the
state write cannot cause a double-submit.

### Discovery, read-only

`GET /stake/legacy` builds a legacy client per catalog entry on this chain, reads the
recoverable amount and paused state of each, and returns a manifest: for each entry, the
deployment ID, the recoverable amount, whether it is paused, the recovery method, and any
in-progress recovery state. It moves nothing and needs no opt-in.

### Recovery, explicit

- `POST /stake/legacy/{id}/withdraw`: recover the stranded stake from that deployment to the
  node's wallet, using the entry's recovery method.
- `POST /stake/legacy/{id}/migrate`: recover it, then redeposit into the current contract by
  the existing deposit path (approve on the BZZ token, then `manageStake`).
- `POST /stake/legacy/recover`: sweep every entry that holds stake, in a mode given in the
  request, and return a per-deployment outcome that distinguishes full success from partial.
- `GET /stake/legacy/{id}`: report the recovery state for one deployment.

All recovery routes sit behind the existing `stakingAccessHandler` semaphore, so recovery
cannot run at the same time as another staking operation. An optional `bee stake recover`
subcommand (modeled on the `bee db` subcommands) can offer the same from the command line.

### Startup option

A configuration value `stake-recovery-on-startup` with three values:

- `migrate` (default): at startup, recover any legacy stake and move it into the current
  contract, restaking it.
- `withdraw`: at startup, recover any legacy stake to the node's wallet.
- `off`: do nothing.

The default is `migrate`, so a node that has stake stranded in a retired contract recovers it
automatically on the next start, which is the point of the feature after an upgrade. When set
to `migrate` or `withdraw`, the node runs discovery once early in startup, before
staking-dependent work, and recovers each deployment that holds stake in the chosen mode,
resuming any partial state. It is a no-op for a node with nothing stranded. It reuses the same
code as the API.

### Handling no gas, and no backend

Recovery submits transactions that cost the chain's native token for gas. The feature checks
the native balance against an estimated cost before each recovery, and:

- If the balance is too low, it submits nothing, reports the shortfall through the API, and in
  the startup path logs a clear warning naming the deployment and the amount waiting, then lets
  the node continue starting. It never fails or blocks startup because recovery could not run.
- For a migrate, if gas runs out after the withdraw, it records the `withdrawn` state, reports
  it, and resumes the redeposit once the node is funded.
- If the chain backend is unreachable, it behaves the same way: skip, report, retry, never
  crash.

## Protocol impact

None. This is chain interaction only. It changes no wire protocol, no chunk geometry, no
handshake, and no on-chain contract; it only calls existing methods on staking contracts. No
`protocol-change` label, and the protocol-freeze check is unaffected.

## Measurement

This is a feature, so it is verified by tests rather than a bench measurement, using the
existing `staking/mock` contract and a transaction service that can be told to report an
insufficient native balance or a failing send:

- Discovery returns the correct recoverable amounts and paused state across several catalog
  entries, including entries with zero stake.
- Withdraw and migrate happy paths call the right contract methods and reach `done`.
- The sweep processes several deployments and reports a per-deployment outcome.
- Each startup mode: `off` is a no-op; `migrate` and `withdraw` act; an unfunded node still
  finishes startup with a clear warning and no submitted transaction.
- Partial completion: a migrate whose redeposit fails for lack of gas records `withdrawn`,
  reports it, and on a second run resumes the redeposit without a second withdraw.

## Configuration

- `stake-recovery-on-startup`: `migrate` (default), `withdraw`, or `off`. Documented in
  `docs/config-reference.yaml`, including that the default moves staked funds automatically.
- New endpoints under `/stake/legacy`, documented in `openapi/Swarm.yaml`.

Nothing here changes on-disk layout, so no migration of the data directory is needed.

## Rollout and rollback

The default is `migrate`, so a node with stake stranded in a retired contract restakes it into
the current contract on its next start; a node with nothing stranded is unaffected. Set the
value to `off` to disable the startup behavior, or to `withdraw` to recover to the wallet
instead of restaking. Because it moves staked funds automatically, the behavior is documented
prominently, it is gas-checked and never blocks startup, and the on-chain recovery still stays
idempotent so a repeat start does not double-submit. Rollback is setting the value to `off`; no
state is left that affects normal operation.

## Upstream portability

The idea began as an upstream feature request (ethersphere/bee issue 5577). This implementation
is wasp-local because it carries its own historical-deployment catalog rather than waiting for
the upstream ABI package to expose past deployments. If upstream later adds versioned historical
deployments to that package, the catalog could be sourced from it instead. Not offered upstream
and no `affects-upstream` label; it is a client feature, not a defect in upstream code.

Generated with help of AI.
