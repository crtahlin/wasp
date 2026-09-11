// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import "github.com/ethereum/go-ethereum/common"

// LegacyStakingRecoverMethod names how a retired staking contract pays its
// stake back. Older contracts differ: some release funds through a withdraw
// call while paused, others through a migrate call on the paused contract.
type LegacyStakingRecoverMethod string

const (
	// RecoverByWithdraw recovers by calling withdrawFromStake on the paused
	// contract.
	RecoverByWithdraw LegacyStakingRecoverMethod = "withdraw"
	// RecoverByMigrate recovers by calling migrateStake on the paused contract.
	RecoverByMigrate LegacyStakingRecoverMethod = "migrate"
)

// LegacyStakingDeployment describes one retired staking contract from which a
// node may still need to recover stake. Operators refer to a deployment by its
// stable ID, never by a raw address or ABI. See docs/experiments/stake-recovery.
type LegacyStakingDeployment struct {
	// ID is a stable identifier, the storage-incentives release tag of the
	// deployment. Several release tags may map to the same address.
	ID string
	// ChainID is the chain this deployment lives on.
	ChainID int64
	// Address is the retired contract's address.
	Address common.Address
	// DeploymentBlock is the block the contract was deployed at.
	DeploymentBlock uint64
	// ABI is the contract ABI. When empty the caller falls back to the current
	// chain's staking ABI, which is correct when the retired contract shares
	// the method shapes the recovery uses (stakes, withdrawableStake, paused,
	// withdrawFromStake, migrateStake).
	ABI string
	// RecoverMethod is how this contract pays its stake back.
	RecoverMethod LegacyStakingRecoverMethod
}

// legacyStakingDeployments holds the known retired staking contracts per chain.
//
// Each address is a staking address that go-storage-incentives-abi pinned at
// the named version and that a later release replaced; the ID is that version.
// Every entry was verified on chain (2026-09-11): the contract exists, reports
// paused() == true, and carries the migrateStake, withdrawFromStake, stakes and
// withdrawableStake methods, so a node's stake is recovered by calling
// migrateStake on the paused contract (RecoverByMigrate). An entry is added
// only after that check, because a wrong address or method would send a
// recovery transaction to the wrong place.
//
// The recovery reuses the current chain's staking ABI (the ABI field is left
// empty), which is correct because these contracts share the method shapes the
// recovery uses. DeploymentBlock is left 0; it is informational and the
// discovery and recovery paths read current on-chain state rather than scanning
// from it.
//
// Deliberately NOT listed: the oldest staking contracts, Gnosis
// 0x781c6D1f0eaE6F1Da1F604c6cDCcdB8B76428ba7 and Sepolia
// 0x41379955a216968996D10614B74b31AA48a0624A (pinned v0.6.2 to v0.9.0). They are
// paused but expose neither migrateStake nor withdrawFromStake, so they predate
// this recovery mechanism and cannot be recovered by it; recording them here as
// recoverable would be wrong.
var legacyStakingDeployments = []LegacyStakingDeployment{
	// Gnosis mainnet (chain 100).
	{
		ID:            "gnosis-abi-v0.9.2",
		ChainID:       Mainnet.ChainID,
		Address:       common.HexToAddress("0x445B848e16730988F871c4a09aB74526d27c2Ce8"),
		RecoverMethod: RecoverByMigrate,
	},
	{
		ID:            "gnosis-abi-v0.9.1",
		ChainID:       Mainnet.ChainID,
		Address:       common.HexToAddress("0xBe212EA1A4978a64e8f7636Ae18305C38CA092Bd"),
		RecoverMethod: RecoverByMigrate,
	},
	// Sepolia testnet (chain 11155111).
	{
		ID:            "sepolia-abi-v0.9.2",
		ChainID:       Testnet.ChainID,
		Address:       common.HexToAddress("0x4353A36f4376A273a65595Acd9bE6c63D90fC352"),
		RecoverMethod: RecoverByMigrate,
	},
	{
		ID:            "sepolia-abi-v0.9.1",
		ChainID:       Testnet.ChainID,
		Address:       common.HexToAddress("0x5CF39e699b601c2EBc3e25b19Fd4102d8366b56F"),
		RecoverMethod: RecoverByMigrate,
	},
}

// LegacyStakingDeployments returns the retired staking contracts known for the
// given chain, in the order they should be checked.
func LegacyStakingDeployments(chainID int64) []LegacyStakingDeployment {
	var out []LegacyStakingDeployment
	for _, d := range legacyStakingDeployments {
		if d.ChainID == chainID {
			out = append(out, d)
		}
	}
	return out
}
