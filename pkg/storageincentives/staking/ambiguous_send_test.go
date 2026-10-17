// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package staking_test

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/staking"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	transactionMock "github.com/ethersphere/bee/v2/pkg/transaction/mock"
)

// TestDepositStakeAfterAmbiguousApprove is the stake deposit's form of
// #802: the approve's send fails at the transport level (HTTP 429), so the
// transaction service returns its hash with the error, and the approve's
// receipt decides whether the deposit goes ahead.
func TestDepositStakeAfterAmbiguousApprove(t *testing.T) {
	t.Parallel()

	owner := common.HexToAddress("abcd")
	stakingAddress := common.HexToAddress("ffff")
	bzzToken := common.HexToAddress("eeee")
	nonce := common.BytesToHash(make([]byte, 32))
	approveHash := common.HexToHash("abb0")
	depositHash := common.HexToHash("c3a7")
	amount := big.NewInt(100000000000000000)
	http429 := rpc.HTTPError{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests"}

	for _, tc := range []struct {
		name          string
		approveStatus uint64
		wantErr       error // nil means the deposit is sent and succeeds
	}{
		{name: "mined approve, deposit sent", approveStatus: types.ReceiptStatusSuccessful},
		{name: "reverted approve, no deposit", approveStatus: types.ReceiptStatusFailed, wantErr: transaction.ErrTransactionReverted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var deposits atomic.Int32
			contract := staking.New(owner, stakingAddress, stakingContractABI, bzzToken,
				transactionMock.New(
					transactionMock.WithSendFunc(func(_ context.Context, request *transaction.TxRequest, _ int) (common.Hash, error) {
						switch *request.To {
						case bzzToken:
							return approveHash, http429
						case stakingAddress:
							deposits.Add(1)
							return depositHash, nil
						}
						return common.Hash{}, errors.New("sent to wrong contract")
					}),
					transactionMock.WithWaitForReceiptFunc(func(_ context.Context, txHash common.Hash) (*types.Receipt, error) {
						switch txHash {
						case approveHash:
							return &types.Receipt{TxHash: approveHash, Status: tc.approveStatus}, nil
						case depositHash:
							return &types.Receipt{TxHash: depositHash, Status: types.ReceiptStatusSuccessful}, nil
						}
						return nil, errors.New("unknown tx hash")
					}),
					transactionMock.WithCallFunc(func(_ context.Context, request *transaction.TxRequest) ([]byte, error) {
						switch *request.To {
						case bzzToken:
							return amount.FillBytes(make([]byte, 32)), nil
						case stakingAddress:
							return getPotentialStakeResponse(t, big.NewInt(0)), nil
						}
						return nil, errors.New("unexpected call")
					}),
				),
				nonce, 0, stakingHeight,
			)

			txHash, err := contract.DepositStake(context.Background(), amount)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("DepositStake: %v, want the deposit made once the approve is mined", err)
				}
				if txHash != depositHash || deposits.Load() != 1 {
					t.Fatalf("deposit tx %s sent %d times, want %s once", txHash, deposits.Load(), depositHash)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("DepositStake: %v, want %v", err, tc.wantErr)
			}
			if n := deposits.Load(); n != 0 {
				t.Fatalf("%d deposits sent after a reverted approve, want 0", n)
			}
		})
	}
}
