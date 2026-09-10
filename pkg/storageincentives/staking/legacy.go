// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package staking

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/config"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

// LegacyStakeStatus is one entry of the legacy-stake discovery manifest: how
// much of this node's stake sits in a retired staking contract and how it can
// be recovered. See docs/experiments/stake-recovery.
type LegacyStakeStatus struct {
	DeploymentID     string
	Address          common.Address
	RecoverableStake *big.Int
	Paused           bool
	RecoverMethod    config.LegacyStakingRecoverMethod
	// Error is set, and the amount left nil, when this deployment could not be
	// read (for example the chain backend was unreachable). Discovery reports it
	// per deployment rather than failing the whole manifest.
	Error string
}

// LegacyStakeService discovers stake a node has left in retired staking
// contracts. Discovery is read-only and moves no funds.
type LegacyStakeService interface {
	Discover(ctx context.Context) ([]LegacyStakeStatus, error)
}

// contractFactory builds a staking client for one legacy deployment. It is a
// field so tests can supply mock clients.
type contractFactory func(address common.Address, contractABI abi.ABI) Contract

type legacyEntry struct {
	cfg    config.LegacyStakingDeployment
	client Contract
}

type legacyStakeService struct {
	entries []legacyEntry
}

// NewLegacyStakeService builds a discovery service over the given retired
// deployments, one staking client per deployment sharing the node's owner
// address, the BZZ token address, and the transaction service. An entry with no
// ABI of its own falls back to the current chain's staking ABI. It returns an
// error if any deployment's ABI does not parse, so a misconfigured catalog is
// caught at startup rather than at recovery time.
func NewLegacyStakeService(
	owner common.Address,
	bzzTokenAddress common.Address,
	transactionService transaction.Service,
	gasLimit uint64,
	deployments []config.LegacyStakingDeployment,
	fallbackABI string,
) (LegacyStakeService, error) {
	factory := func(address common.Address, contractABI abi.ABI) Contract {
		return New(owner, address, contractABI, bzzTokenAddress, transactionService, common.Hash{}, gasLimit, 0)
	}
	return newLegacyStakeService(deployments, fallbackABI, factory)
}

func newLegacyStakeService(deployments []config.LegacyStakingDeployment, fallbackABI string, factory contractFactory) (LegacyStakeService, error) {
	entries := make([]legacyEntry, 0, len(deployments))
	for _, d := range deployments {
		raw := d.ABI
		if raw == "" {
			raw = fallbackABI
		}
		parsed, err := abi.JSON(strings.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("legacy staking deployment %q: parse abi: %w", d.ID, err)
		}
		entries = append(entries, legacyEntry{cfg: d, client: factory(d.Address, parsed)})
	}
	return &legacyStakeService{entries: entries}, nil
}

func (s *legacyStakeService) Discover(ctx context.Context) ([]LegacyStakeStatus, error) {
	out := make([]LegacyStakeStatus, 0, len(s.entries))
	for _, e := range s.entries {
		status := LegacyStakeStatus{
			DeploymentID:  e.cfg.ID,
			Address:       e.cfg.Address,
			RecoverMethod: e.cfg.RecoverMethod,
		}
		stake, err := e.client.GetPotentialStake(ctx)
		if err != nil {
			status.Error = fmt.Sprintf("read stake: %v", err)
			out = append(out, status)
			continue
		}
		paused, err := e.client.Paused(ctx)
		if err != nil {
			status.Error = fmt.Sprintf("read paused: %v", err)
			out = append(out, status)
			continue
		}
		status.RecoverableStake = stake
		status.Paused = paused
		out = append(out, status)
	}
	return out, nil
}
