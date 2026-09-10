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
	statestoremock "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/staking"
	stakingmock "github.com/ethersphere/bee/v2/pkg/storageincentives/staking/mock"
)

// factoryFor returns a client factory that maps each legacy address to a
// prepared mock client.
func factoryFor(clients map[common.Address]staking.Contract) staking.ContractFactory {
	return func(address common.Address, _ abi.ABI) staking.Contract {
		return clients[address]
	}
}

func newService(t *testing.T, deployments []config.LegacyStakingDeployment, clients map[common.Address]staking.Contract, current staking.Contract, state storage.StateStorer) staking.LegacyStakeService {
	t.Helper()
	return newServiceGas(t, deployments, clients, current, state, nil)
}

func newServiceGas(t *testing.T, deployments []config.LegacyStakingDeployment, clients map[common.Address]staking.Contract, current staking.Contract, state storage.StateStorer, nativeBalance func(ctx context.Context) (*big.Int, error)) staking.LegacyStakeService {
	t.Helper()
	svc, err := staking.NewLegacyStakeServiceWithFactory(deployments, "[]", factoryFor(clients), current, state, 100, nativeBalance)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

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
	clients := map[common.Address]staking.Contract{
		addrWith: stakingmock.New(
			stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return big.NewInt(5), nil }),
			stakingmock.WithPaused(func(context.Context) (bool, error) { return true, nil }),
		),
		addrEmpty: stakingmock.New(
			stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return big.NewInt(0), nil }),
			stakingmock.WithPaused(func(context.Context) (bool, error) { return false, nil }),
		),
		addrErr: stakingmock.New(
			stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return nil, errors.New("backend down") }),
			stakingmock.WithPaused(func(context.Context) (bool, error) { return false, nil }),
		),
	}

	svc := newService(t, deployments, clients, nil, statestoremock.NewStateStore())
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
	if s := byID["unreachable"]; s.RecoverableStake != nil || s.Error == "" {
		t.Fatalf("unreachable: expected an error and no amount, got %+v", s)
	}
}

func TestLegacyStakeDiscoverEmptyCatalog(t *testing.T) {
	t.Parallel()

	svc := newService(t, nil, nil, nil, statestoremock.NewStateStore())
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
	if _, err := staking.NewLegacyStakeServiceWithFactory(deployments, "[]", factory, nil, statestoremock.NewStateStore(), 100, nil); err == nil {
		t.Fatal("expected an error for a deployment with an unparseable ABI")
	}
}

func TestLegacyStakeRecoverNoGas(t *testing.T) {
	t.Parallel()

	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	deployments := []config.LegacyStakingDeployment{{ID: "d1", ChainID: 100, Address: addr, RecoverMethod: config.RecoverByWithdraw}}
	clients := map[common.Address]staking.Contract{
		addr: stakingmock.New(
			stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return big.NewInt(9), nil }),
			stakingmock.WithWithdrawStake(func(context.Context) (common.Hash, error) {
				t.Fatal("must not submit a transaction when there is no gas")
				return common.Hash{}, nil
			}),
		),
	}
	// Native balance is zero: no gas.
	svc := newServiceGas(t, deployments, clients, nil, statestoremock.NewStateStore(),
		func(context.Context) (*big.Int, error) { return big.NewInt(0), nil })

	_, err := svc.Recover(context.Background(), "d1", staking.RecoverModeWithdraw)
	if !errors.Is(err, staking.ErrInsufficientGas) {
		t.Fatalf("expected ErrInsufficientGas, got %v", err)
	}
	// Nothing recovered means the state is untouched, so a funded retry works.
	st, _ := svc.Status(context.Background(), "d1")
	if st.Phase != "" {
		t.Fatalf("expected no persisted phase after a no-gas refusal, got %q", st.Phase)
	}
}

func TestLegacyStakeRecoverWithdraw(t *testing.T) {
	t.Parallel()

	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	wtx := common.HexToHash("0xaaaa")
	deployments := []config.LegacyStakingDeployment{{ID: "d1", ChainID: 100, Address: addr, RecoverMethod: config.RecoverByWithdraw}}
	clients := map[common.Address]staking.Contract{
		addr: stakingmock.New(
			stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return big.NewInt(9), nil }),
			stakingmock.WithWithdrawStake(func(context.Context) (common.Hash, error) { return wtx, nil }),
		),
	}
	svc := newService(t, deployments, clients, nil, statestoremock.NewStateStore())

	res, err := svc.Recover(context.Background(), "d1", staking.RecoverModeWithdraw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Phase != "done" || res.WithdrawTx != wtx || res.Recovered.Cmp(big.NewInt(9)) != 0 || res.DepositTx != (common.Hash{}) {
		t.Fatalf("unexpected result %+v", res)
	}
	// Second call is idempotent: already done.
	res2, err := svc.Recover(context.Background(), "d1", staking.RecoverModeWithdraw)
	if err != nil || !res2.AlreadyDone {
		t.Fatalf("expected already-done, got %+v err %v", res2, err)
	}
}

func TestLegacyStakeRecoverMigrate(t *testing.T) {
	t.Parallel()

	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	mtx := common.HexToHash("0xbbbb")
	dtx := common.HexToHash("0xcccc")
	deployments := []config.LegacyStakingDeployment{{ID: "d1", ChainID: 100, Address: addr, RecoverMethod: config.RecoverByMigrate}}
	clients := map[common.Address]staking.Contract{
		addr: stakingmock.New(
			stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return big.NewInt(12), nil }),
			stakingmock.WithMigrateStake(func(context.Context) (common.Hash, error) { return mtx, nil }),
		),
	}
	current := stakingmock.New(
		stakingmock.WithDepositStake(func(_ context.Context, amount *big.Int) (common.Hash, error) {
			if amount.Cmp(big.NewInt(12)) != 0 {
				t.Fatalf("redeposit amount = %s, want 12", amount)
			}
			return dtx, nil
		}),
	)
	svc := newService(t, deployments, clients, current, statestoremock.NewStateStore())

	res, err := svc.Recover(context.Background(), "d1", staking.RecoverModeMigrate)
	if err != nil {
		t.Fatal(err)
	}
	if res.Phase != "done" || res.WithdrawTx != mtx || res.DepositTx != dtx || res.Recovered.Cmp(big.NewInt(12)) != 0 {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestLegacyStakeMigratePartialResume(t *testing.T) {
	t.Parallel()

	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recoverCalls := 0
	depositCalls := 0
	deployments := []config.LegacyStakingDeployment{{ID: "d1", ChainID: 100, Address: addr, RecoverMethod: config.RecoverByMigrate}}
	clients := map[common.Address]staking.Contract{
		addr: stakingmock.New(
			stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return big.NewInt(4), nil }),
			stakingmock.WithMigrateStake(func(context.Context) (common.Hash, error) {
				recoverCalls++
				return common.HexToHash("0xbbbb"), nil
			}),
		),
	}
	current := stakingmock.New(
		stakingmock.WithDepositStake(func(context.Context, *big.Int) (common.Hash, error) {
			depositCalls++
			if depositCalls == 1 {
				return common.Hash{}, errors.New("insufficient funds for gas")
			}
			return common.HexToHash("0xcccc"), nil
		}),
	)
	state := statestoremock.NewStateStore()
	svc := newService(t, deployments, clients, current, state)

	// First attempt: recover to wallet succeeds, redeposit fails for gas.
	if _, err := svc.Recover(context.Background(), "d1", staking.RecoverModeMigrate); err == nil {
		t.Fatal("expected redeposit to fail on the first attempt")
	}
	st, _ := svc.Status(context.Background(), "d1")
	if st.Phase != "withdrawn" || st.Amount.Cmp(big.NewInt(4)) != 0 {
		t.Fatalf("expected withdrawn state with amount 4, got %+v", st)
	}

	// Second attempt: resumes at redeposit, does not recover again.
	res, err := svc.Recover(context.Background(), "d1", staking.RecoverModeMigrate)
	if err != nil {
		t.Fatal(err)
	}
	if res.Phase != "done" {
		t.Fatalf("expected done, got %+v", res)
	}
	if recoverCalls != 1 {
		t.Fatalf("legacy recover called %d times, want exactly 1 (no double withdraw)", recoverCalls)
	}
	if depositCalls != 2 {
		t.Fatalf("deposit called %d times, want 2", depositCalls)
	}
}

func TestLegacyStakeRecoverZero(t *testing.T) {
	t.Parallel()

	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	deployments := []config.LegacyStakingDeployment{{ID: "d1", ChainID: 100, Address: addr, RecoverMethod: config.RecoverByWithdraw}}
	clients := map[common.Address]staking.Contract{
		addr: stakingmock.New(
			stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return big.NewInt(0), nil }),
			stakingmock.WithWithdrawStake(func(context.Context) (common.Hash, error) {
				t.Fatal("must not attempt to recover zero stake")
				return common.Hash{}, nil
			}),
		),
	}
	svc := newService(t, deployments, clients, nil, statestoremock.NewStateStore())
	res, err := svc.Recover(context.Background(), "d1", staking.RecoverModeWithdraw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Phase != "done" || res.Recovered.Sign() != 0 || res.WithdrawTx != (common.Hash{}) {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestLegacyStakeUnknownDeployment(t *testing.T) {
	t.Parallel()

	svc := newService(t, nil, nil, nil, statestoremock.NewStateStore())
	if _, err := svc.Recover(context.Background(), "nope", staking.RecoverModeWithdraw); !errors.Is(err, staking.ErrUnknownLegacyDeployment) {
		t.Fatalf("expected ErrUnknownLegacyDeployment, got %v", err)
	}
	if _, err := svc.Status(context.Background(), "nope"); !errors.Is(err, staking.ErrUnknownLegacyDeployment) {
		t.Fatalf("expected ErrUnknownLegacyDeployment, got %v", err)
	}
}

func TestLegacyStakeRecoverAll(t *testing.T) {
	t.Parallel()

	a1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	a2 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	deployments := []config.LegacyStakingDeployment{
		{ID: "d1", ChainID: 100, Address: a1, RecoverMethod: config.RecoverByWithdraw},
		{ID: "d2", ChainID: 100, Address: a2, RecoverMethod: config.RecoverByWithdraw},
	}
	clients := map[common.Address]staking.Contract{
		a1: stakingmock.New(
			stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return big.NewInt(3), nil }),
			stakingmock.WithWithdrawStake(func(context.Context) (common.Hash, error) { return common.HexToHash("0x01"), nil }),
		),
		a2: stakingmock.New(
			stakingmock.WithGetStake(func(context.Context) (*big.Int, error) { return big.NewInt(0), nil }),
		),
	}
	svc := newService(t, deployments, clients, nil, statestoremock.NewStateStore())

	results, err := svc.RecoverAll(context.Background(), staking.RecoverModeWithdraw)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Phase != "done" || r.Error != "" {
			t.Fatalf("deployment %s: unexpected %+v", r.DeploymentID, r)
		}
	}
}
