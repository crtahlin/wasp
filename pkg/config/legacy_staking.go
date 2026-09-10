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
// It is deliberately empty until an entry's address and recovery method are
// confirmed from the storage-incentives release history. Adding an entry with a
// wrong address or method would send a recovery transaction to the wrong place,
// so an entry is added only once verified against the on-chain deployment. The
// discovery and recovery code works over whatever is listed here, so an empty
// list simply means the node reports no recoverable legacy stake.
var legacyStakingDeployments = []LegacyStakingDeployment{
	// Example shape (do not enable until the address is confirmed):
	// {
	// 	ID:              "storage-incentives-<tag>",
	// 	ChainID:         Mainnet.ChainID,
	// 	Address:         common.HexToAddress("0x..."),
	// 	DeploymentBlock: 0,
	// 	RecoverMethod:   RecoverByMigrate,
	// },
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
