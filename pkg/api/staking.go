// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"errors"
	"math/big"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/bigint"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/staking"
	"github.com/gorilla/mux"
)

func (s *Service) stakingAccessHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.stakingSem.TryAcquire(1) {
			s.logger.Debug("staking access: simultaneous on-chain operations not supported")
			s.logger.Error(nil, "staking access: simultaneous on-chain operations not supported")
			jsonhttp.TooManyRequests(w, "simultaneous on-chain operations not supported")
			return
		}
		defer s.stakingSem.Release(1)

		h.ServeHTTP(w, r)
	})
}

type getStakeResponse struct {
	StakedAmount *bigint.BigInt `json:"stakedAmount"`
}

type getWithdrawableResponse struct {
	WithdrawableAmount *bigint.BigInt `json:"withdrawableAmount"`
}
type stakeTransactionReponse struct {
	TxHash string `json:"txHash"`
}

func (s *Service) stakingDepositHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("post_stake_deposit").Build()

	paths := struct {
		Amount *big.Int `map:"amount" validate:"required"`
	}{}
	if response := s.mapStructure(mux.Vars(r), &paths); response != nil {
		response("invalid path params", logger, w)
		return
	}

	txHash, err := s.stakingContract.DepositStake(r.Context(), paths.Amount)
	if err != nil {
		if errors.Is(err, staking.ErrInsufficientStakeAmount) {
			logger.Debug("insufficient stake amount", "minimum_stake", staking.MinimumStakeAmount, "error", err)
			logger.Error(nil, "insufficient stake amount")
			jsonhttp.BadRequest(w, "insufficient stake amount")
			return
		}
		if errors.Is(err, staking.ErrNotImplemented) {
			logger.Debug("not implemented", "error", err)
			logger.Error(nil, "not implemented")
			jsonhttp.NotImplemented(w, "not implemented")
			return
		}
		if errors.Is(err, staking.ErrInsufficientFunds) {
			logger.Debug("out of funds", "error", err)
			logger.Error(nil, "out of funds")
			jsonhttp.BadRequest(w, "out of funds")
			return
		}
		logger.Debug("deposit failed", "error", err)
		logger.Error(nil, "deposit failed")
		jsonhttp.InternalServerError(w, "cannot stake")
		return
	}

	// if the deposit is successful, we should update the height of the node in the staking contract
	// this is done to make sure that the node is participating in the redistribution game with the correct height
	// if the node has started with insufficient stake
	tx, updated, err := s.stakingContract.UpdateHeight(r.Context())
	if err != nil {
		logger.Debug("update height failed", "error", err)
		logger.Error(nil, "update height failed")
	} else if updated {
		logger.Warning("reserve capacity doubling updated after stake deposit. Node will be FROZEN for ~2 rounds.", "transaction", tx)
	}

	jsonhttp.OK(w, stakeTransactionReponse{
		TxHash: txHash.String(),
	})
}

func (s *Service) getPotentialStake(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("get_stake").Build()

	stakedAmount, err := s.stakingContract.GetPotentialStake(r.Context())
	if err != nil {
		logger.Debug("get staked amount failed", "overlayAddr", s.overlay, "error", err)
		logger.Error(nil, "get staked amount failed")
		jsonhttp.InternalServerError(w, "get staked amount failed")
		return
	}

	jsonhttp.OK(w, getStakeResponse{StakedAmount: bigint.Wrap(stakedAmount)})
}

func (s *Service) getWithdrawableStakeHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("get_stake").Build()

	withdrawableAmount, err := s.stakingContract.GetWithdrawableStake(r.Context())
	if err != nil {
		logger.Debug("get staked amount failed", "overlayAddr", s.overlay, "error", err)
		logger.Error(nil, "get staked amount failed")
		jsonhttp.InternalServerError(w, "get staked amount failed")
		return
	}

	jsonhttp.OK(w, getWithdrawableResponse{WithdrawableAmount: bigint.Wrap(withdrawableAmount)})
}

func (s *Service) withdrawStakeHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("withdraw_stake").Build()

	txHash, err := s.stakingContract.WithdrawStake(r.Context())
	if err != nil {
		if errors.Is(err, staking.ErrInsufficientStake) {
			logger.Debug("insufficient stake", "overlayAddr", s.overlay, "error", err)
			logger.Error(nil, "insufficient stake")
			jsonhttp.BadRequest(w, "insufficient stake to withdraw")
			return
		}
		logger.Debug("withdraw stake failed", "error", err)
		logger.Error(nil, "withdraw stake failed")
		jsonhttp.InternalServerError(w, "cannot withdraw stake")
		return
	}

	jsonhttp.OK(w, stakeTransactionReponse{TxHash: txHash.String()})
}

type legacyStakeEntryResponse struct {
	DeploymentID     string         `json:"deploymentId"`
	Address          string         `json:"address"`
	RecoverableStake *bigint.BigInt `json:"recoverableStake,omitempty"`
	Paused           bool           `json:"paused"`
	RecoverMethod    string         `json:"recoverMethod"`
	Error            string         `json:"error,omitempty"`
}

type legacyStakeResponse struct {
	Deployments []legacyStakeEntryResponse `json:"deployments"`
}

// legacyStakeHandler reports, per known retired staking contract on this chain,
// how much of the node's stake is recoverable and how. It is read-only and
// moves no funds. See docs/experiments/stake-recovery.
func (s *Service) legacyStakeHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("get_stake_legacy").Build()

	resp := legacyStakeResponse{Deployments: []legacyStakeEntryResponse{}}
	if s.legacyStake == nil {
		jsonhttp.OK(w, resp)
		return
	}

	statuses, err := s.legacyStake.Discover(r.Context())
	if err != nil {
		logger.Debug("legacy stake discovery failed", "error", err)
		logger.Error(nil, "legacy stake discovery failed")
		jsonhttp.InternalServerError(w, "legacy stake discovery failed")
		return
	}

	for _, st := range statuses {
		entry := legacyStakeEntryResponse{
			DeploymentID:  st.DeploymentID,
			Address:       st.Address.String(),
			Paused:        st.Paused,
			RecoverMethod: string(st.RecoverMethod),
			Error:         st.Error,
		}
		if st.RecoverableStake != nil {
			entry.RecoverableStake = bigint.Wrap(st.RecoverableStake)
		}
		resp.Deployments = append(resp.Deployments, entry)
	}

	jsonhttp.OK(w, resp)
}

type legacyRecoverResponse struct {
	DeploymentID string         `json:"deploymentId"`
	Mode         string         `json:"mode"`
	Recovered    *bigint.BigInt `json:"recovered,omitempty"`
	WithdrawTx   string         `json:"withdrawTx,omitempty"`
	DepositTx    string         `json:"depositTx,omitempty"`
	Phase        string         `json:"phase"`
	AlreadyDone  bool           `json:"alreadyDone,omitempty"`
	Error        string         `json:"error,omitempty"`
}

type legacyRecoverAllResponse struct {
	Recoveries []legacyRecoverResponse `json:"recoveries"`
}

type legacyStatusResponse struct {
	DeploymentID string         `json:"deploymentId"`
	Phase        string         `json:"phase"`
	Amount       *bigint.BigInt `json:"amount,omitempty"`
}

func parseRecoverMode(r *http.Request) (staking.RecoverMode, bool) {
	switch r.URL.Query().Get("mode") {
	case string(staking.RecoverModeWithdraw):
		return staking.RecoverModeWithdraw, true
	case string(staking.RecoverModeMigrate):
		return staking.RecoverModeMigrate, true
	default:
		return "", false
	}
}

func legacyRecoverResult(res staking.RecoverResult) legacyRecoverResponse {
	out := legacyRecoverResponse{
		DeploymentID: res.DeploymentID,
		Mode:         string(res.Mode),
		Phase:        res.Phase,
		AlreadyDone:  res.AlreadyDone,
		Error:        res.Error,
	}
	if res.Recovered != nil {
		out.Recovered = bigint.Wrap(res.Recovered)
	}
	if res.WithdrawTx != (common.Hash{}) {
		out.WithdrawTx = res.WithdrawTx.String()
	}
	if res.DepositTx != (common.Hash{}) {
		out.DepositTx = res.DepositTx.String()
	}
	return out
}

// legacyStakeRecoverHandler recovers stranded stake from one retired contract,
// either to the wallet (mode=withdraw) or into the current contract
// (mode=migrate). It moves funds. See docs/experiments/stake-recovery.
func (s *Service) legacyStakeRecoverHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("post_stake_legacy_recover").Build()

	if s.legacyStake == nil {
		jsonhttp.NotFound(w, "no legacy staking deployments configured")
		return
	}
	paths := struct {
		ID string `map:"id" validate:"required"`
	}{}
	if response := s.mapStructure(mux.Vars(r), &paths); response != nil {
		response("invalid path params", logger, w)
		return
	}
	mode, ok := parseRecoverMode(r)
	if !ok {
		jsonhttp.BadRequest(w, "query parameter mode must be withdraw or migrate")
		return
	}

	res, err := s.legacyStake.Recover(r.Context(), paths.ID, mode)
	if errors.Is(err, staking.ErrUnknownLegacyDeployment) {
		jsonhttp.NotFound(w, "unknown legacy staking deployment")
		return
	}
	if errors.Is(err, staking.ErrInsufficientGas) {
		logger.Debug("insufficient native balance for gas", "deployment", paths.ID, "error", err)
		logger.Error(nil, "insufficient native balance for gas")
		jsonhttp.BadRequest(w, "insufficient native balance for gas")
		return
	}
	if err != nil {
		logger.Debug("recover legacy stake failed", "deployment", paths.ID, "error", err)
		logger.Error(nil, "recover legacy stake failed")
		jsonhttp.InternalServerError(w, "recover legacy stake failed")
		return
	}
	jsonhttp.OK(w, legacyRecoverResult(res))
}

// legacyStakeRecoverAllHandler sweeps every retired contract that holds stake,
// in the given mode, reporting a per-deployment outcome.
func (s *Service) legacyStakeRecoverAllHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("post_stake_legacy_recover_all").Build()

	if s.legacyStake == nil {
		jsonhttp.OK(w, legacyRecoverAllResponse{Recoveries: []legacyRecoverResponse{}})
		return
	}
	mode, ok := parseRecoverMode(r)
	if !ok {
		jsonhttp.BadRequest(w, "query parameter mode must be withdraw or migrate")
		return
	}

	results, err := s.legacyStake.RecoverAll(r.Context(), mode)
	if err != nil {
		logger.Debug("sweep legacy stake failed", "error", err)
		logger.Error(nil, "sweep legacy stake failed")
		jsonhttp.InternalServerError(w, "sweep legacy stake failed")
		return
	}
	resp := legacyRecoverAllResponse{Recoveries: make([]legacyRecoverResponse, 0, len(results))}
	for _, res := range results {
		resp.Recoveries = append(resp.Recoveries, legacyRecoverResult(res))
	}
	jsonhttp.OK(w, resp)
}

// legacyStakeStatusHandler reports the persisted recovery progress of one
// retired deployment.
func (s *Service) legacyStakeStatusHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("get_stake_legacy_status").Build()

	if s.legacyStake == nil {
		jsonhttp.NotFound(w, "no legacy staking deployments configured")
		return
	}
	paths := struct {
		ID string `map:"id" validate:"required"`
	}{}
	if response := s.mapStructure(mux.Vars(r), &paths); response != nil {
		response("invalid path params", logger, w)
		return
	}

	st, err := s.legacyStake.Status(r.Context(), paths.ID)
	if errors.Is(err, staking.ErrUnknownLegacyDeployment) {
		jsonhttp.NotFound(w, "unknown legacy staking deployment")
		return
	}
	if err != nil {
		logger.Debug("legacy stake status failed", "deployment", paths.ID, "error", err)
		logger.Error(nil, "legacy stake status failed")
		jsonhttp.InternalServerError(w, "legacy stake status failed")
		return
	}
	resp := legacyStatusResponse{DeploymentID: paths.ID, Phase: st.Phase}
	if st.Amount != nil {
		resp.Amount = bigint.Wrap(st.Amount)
	}
	jsonhttp.OK(w, resp)
}

func (s *Service) migrateStakeHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("migrate_stake").Build()

	txHash, err := s.stakingContract.MigrateStake(r.Context())
	if err != nil {
		if errors.Is(err, staking.ErrInsufficientStake) {
			logger.Debug("insufficient stake", "overlayAddr", s.overlay, "error", err)
			logger.Error(nil, "insufficient stake")
			jsonhttp.BadRequest(w, "insufficient stake to migrate")
			return
		}
		if errors.Is(err, staking.ErrNotPaused) {
			logger.Debug("contract is not paused", "error", err)
			logger.Error(nil, "contract is not paused")
			jsonhttp.BadRequest(w, "contract is not paused")
			return
		}
		logger.Debug("migrate stake failed", "error", err)
		logger.Error(nil, "migrate stake failed")
		jsonhttp.InternalServerError(w, "cannot migrate stake")
		return
	}

	jsonhttp.OK(w, stakeTransactionReponse{TxHash: txHash.String()})
}
