// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package staking

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/config"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

// ErrUnknownLegacyDeployment is returned when a caller names a deployment that
// is not in the catalog for this chain.
var ErrUnknownLegacyDeployment = errors.New("unknown legacy staking deployment")

// ErrInsufficientGas is returned when the node has no native balance to pay for
// the recovery transactions. No transaction is submitted, and any persisted
// progress is left untouched so the recovery resumes once the node is funded.
var ErrInsufficientGas = errors.New("insufficient native balance for gas")

// RecoverMode is how stranded stake is recovered.
type RecoverMode string

const (
	// RecoverModeWithdraw recovers the stake to the node's wallet.
	RecoverModeWithdraw RecoverMode = "withdraw"
	// RecoverModeMigrate recovers the stake and redeposits it into the current
	// staking contract.
	RecoverModeMigrate RecoverMode = "migrate"
)

// Recovery phases, persisted so a partly finished recovery resumes rather than
// repeats.
const (
	phaseIdle      = ""          // nothing done yet
	phaseWithdrawn = "withdrawn" // recovered to wallet, migrate not yet redeposited
	phaseDone      = "done"      // fully recovered (and redeposited, for migrate)
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
	// Phase is the persisted recovery phase for this deployment: empty, or
	// "withdrawn" (a migrate recovered to the wallet but has not yet redeposited),
	// or "done".
	Phase string
	// Error is set, and the amount left nil, when this deployment could not be
	// read. Discovery reports it per deployment rather than failing the whole
	// manifest.
	Error string
}

// RecoverState is the persisted progress of one deployment's recovery.
type RecoverState struct {
	Phase  string   `json:"phase"`
	Amount *big.Int `json:"amount,omitempty"`
}

// RecoverResult reports the outcome of recovering one deployment.
type RecoverResult struct {
	DeploymentID string
	Mode         RecoverMode
	Recovered    *big.Int
	WithdrawTx   common.Hash
	DepositTx    common.Hash
	Phase        string
	AlreadyDone  bool
	// Error is set, for a sweep, when this deployment failed; the sweep
	// continues with the rest rather than stopping.
	Error string
}

// LegacyStakeService discovers and recovers stake a node has left in retired
// staking contracts. Discovery is read-only; recovery moves funds and is only
// ever an explicit action.
type LegacyStakeService interface {
	Discover(ctx context.Context) ([]LegacyStakeStatus, error)
	Recover(ctx context.Context, deploymentID string, mode RecoverMode) (RecoverResult, error)
	RecoverAll(ctx context.Context, mode RecoverMode) ([]RecoverResult, error)
	Status(ctx context.Context, deploymentID string) (RecoverState, error)
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
	current Contract
	state   storage.StateStorer
	chainID int64
	// nativeBalance reads the node's native-token balance, used to refuse a
	// recovery gracefully when there is no gas. Nil disables the check.
	nativeBalance func(ctx context.Context) (*big.Int, error)
}

// NewLegacyStakeService builds a service over the given retired deployments,
// one staking client per deployment sharing the node's owner address, the BZZ
// token address, and the transaction service. The current contract is used to
// redeposit when recovering in migrate mode, and the state store persists
// per-deployment recovery progress. An entry with no ABI of its own falls back
// to the current chain's staking ABI. It returns an error if any deployment's
// ABI does not parse, so a misconfigured catalog is caught at startup.
func NewLegacyStakeService(
	owner common.Address,
	bzzTokenAddress common.Address,
	transactionService transaction.Service,
	gasLimit uint64,
	deployments []config.LegacyStakingDeployment,
	fallbackABI string,
	current Contract,
	state storage.StateStorer,
	chainID int64,
	nativeBalance func(ctx context.Context) (*big.Int, error),
) (LegacyStakeService, error) {
	factory := func(address common.Address, contractABI abi.ABI) Contract {
		return New(owner, address, contractABI, bzzTokenAddress, transactionService, common.Hash{}, gasLimit, 0)
	}
	return newLegacyStakeService(deployments, fallbackABI, factory, current, state, chainID, nativeBalance)
}

func newLegacyStakeService(deployments []config.LegacyStakingDeployment, fallbackABI string, factory contractFactory, current Contract, state storage.StateStorer, chainID int64, nativeBalance func(ctx context.Context) (*big.Int, error)) (LegacyStakeService, error) {
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
	return &legacyStakeService{entries: entries, current: current, state: state, chainID: chainID, nativeBalance: nativeBalance}, nil
}

func (s *legacyStakeService) stateKey(id string) string {
	return fmt.Sprintf("legacy-stake-recovery/%d/%s", s.chainID, id)
}

func (s *legacyStakeService) loadState(id string) RecoverState {
	var st RecoverState
	if s.state == nil {
		return st
	}
	if err := s.state.Get(s.stateKey(id), &st); err != nil {
		// A missing key means no recovery has started; any other error is
		// treated the same, since the on-chain state is re-read before acting.
		return RecoverState{}
	}
	return st
}

func (s *legacyStakeService) saveState(id string, st RecoverState) error {
	if s.state == nil {
		return nil
	}
	return s.state.Put(s.stateKey(id), st)
}

func (s *legacyStakeService) entryByID(id string) (legacyEntry, bool) {
	for _, e := range s.entries {
		if e.cfg.ID == id {
			return e, true
		}
	}
	return legacyEntry{}, false
}

func (s *legacyStakeService) Discover(ctx context.Context) ([]LegacyStakeStatus, error) {
	out := make([]LegacyStakeStatus, 0, len(s.entries))
	for _, e := range s.entries {
		status := LegacyStakeStatus{
			DeploymentID:  e.cfg.ID,
			Address:       e.cfg.Address,
			RecoverMethod: e.cfg.RecoverMethod,
			Phase:         s.loadState(e.cfg.ID).Phase,
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

// recoverToWallet calls the retired contract's payout method, chosen by the
// deployment's recovery method, moving its stake to the node's wallet.
func (s *legacyStakeService) recoverToWallet(ctx context.Context, e legacyEntry) (common.Hash, error) {
	if e.cfg.RecoverMethod == config.RecoverByMigrate {
		return e.client.MigrateStake(ctx)
	}
	return e.client.WithdrawStake(ctx)
}

// ensureGas refuses a recovery when the node has no native balance to pay for
// the transaction, so no transaction is submitted and no state is changed. A
// nil balance reader disables the check.
func (s *legacyStakeService) ensureGas(ctx context.Context) error {
	if s.nativeBalance == nil {
		return nil
	}
	bal, err := s.nativeBalance(ctx)
	if err != nil {
		return fmt.Errorf("read native balance: %w", err)
	}
	if bal == nil || bal.Sign() == 0 {
		return ErrInsufficientGas
	}
	return nil
}

func (s *legacyStakeService) Recover(ctx context.Context, id string, mode RecoverMode) (RecoverResult, error) {
	e, ok := s.entryByID(id)
	if !ok {
		return RecoverResult{}, ErrUnknownLegacyDeployment
	}
	res := RecoverResult{DeploymentID: id, Mode: mode}
	st := s.loadState(id)
	if st.Phase == phaseDone {
		res.AlreadyDone = true
		res.Phase = phaseDone
		res.Recovered = st.Amount
		return res, nil
	}

	// Step one, recover to the wallet, unless a previous migrate already did so.
	if st.Phase != phaseWithdrawn {
		amount, err := e.client.GetPotentialStake(ctx)
		if err != nil {
			return res, fmt.Errorf("read stake: %w", err)
		}
		if amount.Sign() == 0 {
			st = RecoverState{Phase: phaseDone, Amount: big.NewInt(0)}
			_ = s.saveState(id, st)
			res.Phase = phaseDone
			res.Recovered = big.NewInt(0)
			return res, nil
		}
		if err := s.ensureGas(ctx); err != nil {
			return res, err
		}
		tx, err := s.recoverToWallet(ctx, e)
		if err != nil {
			return res, fmt.Errorf("recover to wallet: %w", err)
		}
		res.WithdrawTx = tx
		res.Recovered = amount
		if mode == RecoverModeWithdraw {
			st = RecoverState{Phase: phaseDone, Amount: amount}
			if err := s.saveState(id, st); err != nil {
				return res, fmt.Errorf("save state: %w", err)
			}
			res.Phase = phaseDone
			return res, nil
		}
		st = RecoverState{Phase: phaseWithdrawn, Amount: amount}
		if err := s.saveState(id, st); err != nil {
			return res, fmt.Errorf("save state: %w", err)
		}
	} else {
		res.Recovered = st.Amount
	}

	// Step two, migrate only: redeposit the recovered amount into the current
	// contract. If this fails the state stays at "withdrawn", so a later call
	// resumes here without recovering again.
	if err := s.ensureGas(ctx); err != nil {
		res.Phase = phaseWithdrawn
		return res, err
	}
	depTx, err := s.current.DepositStake(ctx, st.Amount)
	if err != nil {
		res.Phase = phaseWithdrawn
		return res, fmt.Errorf("redeposit: %w", err)
	}
	res.DepositTx = depTx
	st.Phase = phaseDone
	if err := s.saveState(id, st); err != nil {
		return res, fmt.Errorf("save state: %w", err)
	}
	res.Phase = phaseDone
	return res, nil
}

func (s *legacyStakeService) RecoverAll(ctx context.Context, mode RecoverMode) ([]RecoverResult, error) {
	out := make([]RecoverResult, 0, len(s.entries))
	for _, e := range s.entries {
		r, err := s.Recover(ctx, e.cfg.ID, mode)
		if err != nil {
			r.DeploymentID = e.cfg.ID
			r.Mode = mode
			r.Error = err.Error()
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *legacyStakeService) Status(ctx context.Context, id string) (RecoverState, error) {
	if _, ok := s.entryByID(id); !ok {
		return RecoverState{}, ErrUnknownLegacyDeployment
	}
	return s.loadState(id), nil
}
