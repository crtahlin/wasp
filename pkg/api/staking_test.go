// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/api"
	"github.com/ethersphere/bee/v2/pkg/bigint"
	"github.com/ethersphere/bee/v2/pkg/config"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp/jsonhttptest"
	"github.com/ethersphere/bee/v2/pkg/sctx"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/staking"
	stakingContractMock "github.com/ethersphere/bee/v2/pkg/storageincentives/staking/mock"
)

func TestDepositStake(t *testing.T) {
	t.Parallel()

	txHash := common.HexToHash("0x1234")
	minStake := big.NewInt(100000000000000000).String()
	depositStake := func(amount string) string {
		return fmt.Sprintf("/stake/%s", amount)
	}

	t.Run("ok", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithDepositStake(func(ctx context.Context, stakedAmount *big.Int) (common.Hash, error) {
				return txHash, nil
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodPost, depositStake(minStake), http.StatusOK)
	})

	t.Run("with invalid stake amount", func(t *testing.T) {
		t.Parallel()

		invalidMinStake := big.NewInt(0).String()
		contract := stakingContractMock.New(
			stakingContractMock.WithDepositStake(func(ctx context.Context, stakedAmount *big.Int) (common.Hash, error) {
				return common.Hash{}, staking.ErrInsufficientStakeAmount
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodPost, depositStake(invalidMinStake), http.StatusBadRequest,
			jsonhttptest.WithExpectedJSONResponse(&jsonhttp.StatusResponse{Code: http.StatusBadRequest, Message: "insufficient stake amount"}))
	})

	t.Run("out of funds", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithDepositStake(func(ctx context.Context, stakedAmount *big.Int) (common.Hash, error) {
				return common.Hash{}, staking.ErrInsufficientFunds
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodPost, depositStake(minStake), http.StatusBadRequest)
		jsonhttptest.WithExpectedJSONResponse(&jsonhttp.StatusResponse{Code: http.StatusBadRequest, Message: "out of funds"})
	})

	t.Run("internal error", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithDepositStake(func(ctx context.Context, stakedAmount *big.Int) (common.Hash, error) {
				return common.Hash{}, fmt.Errorf("some error")
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodPost, depositStake(minStake), http.StatusInternalServerError)
		jsonhttptest.WithExpectedJSONResponse(&jsonhttp.StatusResponse{Code: http.StatusInternalServerError, Message: "cannot stake"})
	})

	t.Run("update height fails after deposit", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithDepositStake(func(ctx context.Context, stakedAmount *big.Int) (common.Hash, error) {
				return txHash, nil
			}),
			stakingContractMock.WithUpdateHeight(func(ctx context.Context) (common.Hash, bool, error) {
				return common.Hash{}, false, fmt.Errorf("update height failed")
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodPost, depositStake(minStake), http.StatusOK,
			jsonhttptest.WithExpectedJSONResponse(&api.StakeTransactionReponse{TxHash: txHash.String()}))
	})

	t.Run("gas limit header", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithDepositStake(func(ctx context.Context, stakedAmount *big.Int) (common.Hash, error) {
				gasLimit := sctx.GetGasLimit(ctx)
				if gasLimit != 2000000 {
					t.Fatalf("want 2000000, got %d", gasLimit)
				}
				return txHash, nil
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{
			StakingContract: contract,
		})

		jsonhttptest.Request(t, ts, http.MethodPost, depositStake(minStake), http.StatusOK,
			jsonhttptest.WithRequestHeader(api.GasLimitHeader, "2000000"),
		)
	})
}

func TestGetStakeCommitted(t *testing.T) {
	t.Parallel()

	t.Run("ok", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithGetStake(func(ctx context.Context) (*big.Int, error) {
				return big.NewInt(1), nil
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodGet, "/stake", http.StatusOK,
			jsonhttptest.WithExpectedJSONResponse(&api.GetStakeResponse{StakedAmount: bigint.Wrap(big.NewInt(1))}))
	})

	t.Run("with error", func(t *testing.T) {
		t.Parallel()

		contractWithError := stakingContractMock.New(
			stakingContractMock.WithGetStake(func(ctx context.Context) (*big.Int, error) {
				return big.NewInt(0), fmt.Errorf("get stake failed")
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contractWithError})
		jsonhttptest.Request(t, ts, http.MethodGet, "/stake", http.StatusInternalServerError,
			jsonhttptest.WithExpectedJSONResponse(&jsonhttp.StatusResponse{Code: http.StatusInternalServerError, Message: "get staked amount failed"}))
	})
}

func TestGetStakeWithdrawable(t *testing.T) {
	t.Parallel()

	t.Run("ok", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithGetStake(func(ctx context.Context) (*big.Int, error) {
				return big.NewInt(1), nil
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodGet, "/stake/withdrawable", http.StatusOK,
			jsonhttptest.WithExpectedJSONResponse(&api.GetWithdrawableResponse{WithdrawableAmount: bigint.Wrap(big.NewInt(1))}))
	})

	t.Run("with error", func(t *testing.T) {
		t.Parallel()

		contractWithError := stakingContractMock.New(
			stakingContractMock.WithGetStake(func(ctx context.Context) (*big.Int, error) {
				return big.NewInt(0), fmt.Errorf("get stake failed")
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contractWithError})
		jsonhttptest.Request(t, ts, http.MethodGet, "/stake/withdrawable", http.StatusInternalServerError,
			jsonhttptest.WithExpectedJSONResponse(&jsonhttp.StatusResponse{Code: http.StatusInternalServerError, Message: "get staked amount failed"}))
	})
}

func Test_stakingDepositHandler_invalidInputs(t *testing.T) {
	t.Parallel()

	client, _, _, _ := newTestServer(t, testServerOptions{})

	tests := []struct {
		name   string
		amount string
		want   jsonhttp.StatusResponse
	}{{
		name:   "amount - invalid value",
		amount: "a",
		want: jsonhttp.StatusResponse{
			Code:    http.StatusBadRequest,
			Message: "invalid path params",
			Reasons: []jsonhttp.Reason{
				{
					Field: "amount",
					Error: "invalid value",
				},
			},
		},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			jsonhttptest.Request(t, client, http.MethodPost, "/stake/"+tc.amount, tc.want.Code,
				jsonhttptest.WithExpectedJSONResponse(tc.want),
			)
		})
	}
}

func TestWithdrawStake(t *testing.T) {
	t.Parallel()

	txHash := common.HexToHash("0x1234")

	t.Run("ok", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithWithdrawStake(func(ctx context.Context) (common.Hash, error) {
				return txHash, nil
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodDelete, "/stake/withdrawable", http.StatusOK, jsonhttptest.WithExpectedJSONResponse(
			&api.StakeTransactionReponse{TxHash: txHash.String()}))
	})

	t.Run("with invalid stake amount", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithWithdrawStake(func(ctx context.Context) (common.Hash, error) {
				return common.Hash{}, staking.ErrInsufficientStake
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodDelete, "/stake/withdrawable", http.StatusBadRequest,
			jsonhttptest.WithExpectedJSONResponse(&jsonhttp.StatusResponse{Code: http.StatusBadRequest, Message: "insufficient stake to withdraw"}))
	})

	t.Run("internal error", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithWithdrawStake(func(ctx context.Context) (common.Hash, error) {
				return common.Hash{}, fmt.Errorf("some error")
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodDelete, "/stake/withdrawable", http.StatusInternalServerError)
		jsonhttptest.WithExpectedJSONResponse(&jsonhttp.StatusResponse{Code: http.StatusInternalServerError, Message: "cannot withdraw stake"})
	})

	t.Run("gas limit header", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithWithdrawStake(func(ctx context.Context) (common.Hash, error) {
				gasLimit := sctx.GetGasLimit(ctx)
				if gasLimit != 2000000 {
					t.Fatalf("want 2000000, got %d", gasLimit)
				}
				return txHash, nil
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{
			StakingContract: contract,
		})

		jsonhttptest.Request(t, ts, http.MethodDelete, "/stake/withdrawable", http.StatusOK,
			jsonhttptest.WithRequestHeader(api.GasLimitHeader, "2000000"),
		)
	})
}

func TestMigrateStake(t *testing.T) {
	t.Parallel()

	txHash := common.HexToHash("0x1234")

	t.Run("ok", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithMigrateStake(func(ctx context.Context) (common.Hash, error) {
				return txHash, nil
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodDelete, "/stake", http.StatusOK, jsonhttptest.WithExpectedJSONResponse(
			&api.StakeTransactionReponse{TxHash: txHash.String()}))
	})

	t.Run("with invalid stake amount", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithMigrateStake(func(ctx context.Context) (common.Hash, error) {
				return common.Hash{}, staking.ErrInsufficientStake
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodDelete, "/stake", http.StatusBadRequest,
			jsonhttptest.WithExpectedJSONResponse(&jsonhttp.StatusResponse{Code: http.StatusBadRequest, Message: "insufficient stake to migrate"}))
	})

	t.Run("internal error", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithMigrateStake(func(ctx context.Context) (common.Hash, error) {
				return common.Hash{}, fmt.Errorf("some error")
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{StakingContract: contract})
		jsonhttptest.Request(t, ts, http.MethodDelete, "/stake", http.StatusInternalServerError)
		jsonhttptest.WithExpectedJSONResponse(&jsonhttp.StatusResponse{Code: http.StatusInternalServerError, Message: "cannot withdraw stake"})
	})

	t.Run("gas limit header", func(t *testing.T) {
		t.Parallel()

		contract := stakingContractMock.New(
			stakingContractMock.WithMigrateStake(func(ctx context.Context) (common.Hash, error) {
				gasLimit := sctx.GetGasLimit(ctx)
				if gasLimit != 2000000 {
					t.Fatalf("want 2000000, got %d", gasLimit)
				}
				return txHash, nil
			}),
		)
		ts, _, _, _ := newTestServer(t, testServerOptions{
			StakingContract: contract,
		})

		jsonhttptest.Request(t, ts, http.MethodDelete, "/stake", http.StatusOK,
			jsonhttptest.WithRequestHeader(api.GasLimitHeader, "2000000"),
		)
	})
}

func TestLegacyStake(t *testing.T) {
	t.Parallel()

	t.Run("manifest", func(t *testing.T) {
		t.Parallel()

		addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
		legacy := stakingContractMock.NewLegacyStakeService(stakingContractMock.WithDiscover(func(context.Context) ([]staking.LegacyStakeStatus, error) {
			return []staking.LegacyStakeStatus{
				{DeploymentID: "d1", Address: addr, RecoverableStake: big.NewInt(7), Paused: true, RecoverMethod: config.RecoverByMigrate},
			}, nil
		}))
		ts, _, _, _ := newTestServer(t, testServerOptions{LegacyStake: legacy})
		jsonhttptest.Request(t, ts, http.MethodGet, "/stake/legacy", http.StatusOK,
			jsonhttptest.WithExpectedJSONResponse(&api.LegacyStakeResponse{
				Deployments: []api.LegacyStakeEntryResponse{
					{DeploymentID: "d1", Address: addr.String(), RecoverableStake: bigint.Wrap(big.NewInt(7)), Paused: true, RecoverMethod: "migrate"},
				},
			}))
	})

	t.Run("none configured", func(t *testing.T) {
		t.Parallel()

		ts, _, _, _ := newTestServer(t, testServerOptions{})
		jsonhttptest.Request(t, ts, http.MethodGet, "/stake/legacy", http.StatusOK,
			jsonhttptest.WithExpectedJSONResponse(&api.LegacyStakeResponse{Deployments: []api.LegacyStakeEntryResponse{}}))
	})
}

func TestLegacyStakeRecover(t *testing.T) {
	t.Parallel()

	t.Run("withdraw ok", func(t *testing.T) {
		t.Parallel()
		legacy := stakingContractMock.NewLegacyStakeService(stakingContractMock.WithRecover(func(_ context.Context, id string, mode staking.RecoverMode) (staking.RecoverResult, error) {
			return staking.RecoverResult{DeploymentID: id, Mode: mode, Recovered: big.NewInt(9), WithdrawTx: common.HexToHash("0xaa"), Phase: "done"}, nil
		}))
		ts, _, _, _ := newTestServer(t, testServerOptions{LegacyStake: legacy})
		jsonhttptest.Request(t, ts, http.MethodPost, "/stake/legacy/d1?mode=withdraw", http.StatusOK,
			jsonhttptest.WithExpectedJSONResponse(&api.LegacyRecoverResponse{
				DeploymentID: "d1", Mode: "withdraw", Recovered: bigint.Wrap(big.NewInt(9)),
				WithdrawTx: common.HexToHash("0xaa").String(), Phase: "done",
			}))
	})

	t.Run("missing mode", func(t *testing.T) {
		t.Parallel()
		legacy := stakingContractMock.NewLegacyStakeService()
		ts, _, _, _ := newTestServer(t, testServerOptions{LegacyStake: legacy})
		jsonhttptest.Request(t, ts, http.MethodPost, "/stake/legacy/d1", http.StatusBadRequest,
			jsonhttptest.WithExpectedJSONResponse(&jsonhttp.StatusResponse{Code: http.StatusBadRequest, Message: "query parameter mode must be withdraw or migrate"}))
	})

	t.Run("unknown deployment", func(t *testing.T) {
		t.Parallel()
		legacy := stakingContractMock.NewLegacyStakeService(stakingContractMock.WithRecover(func(context.Context, string, staking.RecoverMode) (staking.RecoverResult, error) {
			return staking.RecoverResult{}, staking.ErrUnknownLegacyDeployment
		}))
		ts, _, _, _ := newTestServer(t, testServerOptions{LegacyStake: legacy})
		jsonhttptest.Request(t, ts, http.MethodPost, "/stake/legacy/nope?mode=migrate", http.StatusNotFound,
			jsonhttptest.WithExpectedJSONResponse(&jsonhttp.StatusResponse{Code: http.StatusNotFound, Message: "unknown legacy staking deployment"}))
	})

	t.Run("status", func(t *testing.T) {
		t.Parallel()
		legacy := stakingContractMock.NewLegacyStakeService(stakingContractMock.WithStatus(func(_ context.Context, id string) (staking.RecoverState, error) {
			return staking.RecoverState{Phase: "withdrawn", Amount: big.NewInt(4)}, nil
		}))
		ts, _, _, _ := newTestServer(t, testServerOptions{LegacyStake: legacy})
		jsonhttptest.Request(t, ts, http.MethodGet, "/stake/legacy/d1", http.StatusOK,
			jsonhttptest.WithExpectedJSONResponse(&api.LegacyStatusResponse{DeploymentID: "d1", Phase: "withdrawn", Amount: bigint.Wrap(big.NewInt(4))}))
	})

	t.Run("sweep", func(t *testing.T) {
		t.Parallel()
		legacy := stakingContractMock.NewLegacyStakeService(stakingContractMock.WithRecoverAll(func(_ context.Context, mode staking.RecoverMode) ([]staking.RecoverResult, error) {
			return []staking.RecoverResult{
				{DeploymentID: "d1", Mode: mode, Recovered: big.NewInt(3), Phase: "done"},
			}, nil
		}))
		ts, _, _, _ := newTestServer(t, testServerOptions{LegacyStake: legacy})
		jsonhttptest.Request(t, ts, http.MethodPost, "/stake/legacy?mode=migrate", http.StatusOK,
			jsonhttptest.WithExpectedJSONResponse(&api.LegacyRecoverAllResponse{
				Recoveries: []api.LegacyRecoverResponse{
					{DeploymentID: "d1", Mode: "migrate", Recovered: bigint.Wrap(big.NewInt(3)), Phase: "done"},
				},
			}))
	})
}
