// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package staking_test

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/config"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/staking"
	stakingmock "github.com/ethersphere/bee/v2/pkg/storageincentives/staking/mock"
)

func TestLegacyStakeDiscover(t *testing.T) {
	t.Parallel()

	addrWith := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addrEmpty := common.HexToAddress("0x2222222222222222222222222222222222222222")
	addrErr := common.HexToAddress("0x3333333333333333333333333333333333333333")

	deployments := []config.LegacyStakingDeployment{
		{ID: "with-stake", ChainID: 100, Address: addrWith, RecoverMethod: config.RecoverByMigrate},
		{ID: "empty", ChainID: 100, Address: addrEmpty, RecoverMethod: config.RecoverByWithdraw},
		{ID: "unreachable", ChainID: 100, Address: addrErr, RecoverMethod: config.RecoverByMigrate},
	}

	// A factory that returns a different mock per legacy address.
	factory := staking.ContractFactory(func(address common.Address, _ abi.ABI) staking.Contract {
		switch address {
		case addrWith:
			return stakingmock.New(
				stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return big.NewInt(5), nil }),
				stakingmock.WithPaused(func(context.Context) (bool, error) { return true, nil }),
			)
		case addrEmpty:
			return stakingmock.New(
				stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return big.NewInt(0), nil }),
				stakingmock.WithPaused(func(context.Context) (bool, error) { return false, nil }),
			)
		default:
			return stakingmock.New(
				stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return nil, errors.New("backend down") }),
				stakingmock.WithPaused(func(context.Context) (bool, error) { return false, nil }),
			)
		}
	})

	svc, err := staking.NewLegacyStakeServiceWithFactory(deployments, "[]", factory)
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d statuses, want 3", len(got))
	}

	byID := map[string]staking.LegacyStakeStatus{}
	for _, s := range got {
		byID[s.DeploymentID] = s
	}

	if s := byID["with-stake"]; s.RecoverableStake == nil || s.RecoverableStake.Cmp(big.NewInt(5)) != 0 || !s.Paused || s.RecoverMethod != config.RecoverByMigrate || s.Error != "" {
		t.Fatalf("with-stake: unexpected %+v", s)
	}
	if s := byID["empty"]; s.RecoverableStake == nil || s.RecoverableStake.Sign() != 0 || s.Paused || s.Error != "" {
		t.Fatalf("empty: unexpected %+v", s)
	}
	// A deployment that could not be read is reported with an error and no
	// amount, rather than failing the whole manifest.
	if s := byID["unreachable"]; s.RecoverableStake != nil || s.Error == "" {
		t.Fatalf("unreachable: expected an error and no amount, got %+v", s)
	}
}

func TestLegacyStakeDiscoverEmptyCatalog(t *testing.T) {
	t.Parallel()

	factory := staking.ContractFactory(func(common.Address, abi.ABI) staking.Contract {
		t.Fatal("factory must not be called for an empty catalog")
		return nil
	})

	svc, err := staking.NewLegacyStakeServiceWithFactory(nil, "[]", factory)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d statuses, want 0", len(got))
	}
}

func TestLegacyStakeServiceBadABI(t *testing.T) {
	t.Parallel()

	deployments := []config.LegacyStakingDeployment{
		{ID: "bad", ChainID: 100, Address: common.HexToAddress("0x4444444444444444444444444444444444444444"), ABI: "not json"},
	}
	factory := staking.ContractFactory(func(common.Address, abi.ABI) staking.Contract { return nil })

	if _, err := staking.NewLegacyStakeServiceWithFactory(deployments, "[]", factory); err == nil {
		t.Fatal("expected an error for a deployment with an unparseable ABI")
	}
}
