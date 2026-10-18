// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction_test

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	signermock "github.com/ethersphere/bee/v2/pkg/crypto/mock"
	"github.com/ethersphere/bee/v2/pkg/log"
	storemock "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/backendmock"
	"github.com/ethersphere/bee/v2/pkg/transaction/monitormock"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

// TestEstimateErrorGasLimit checks the gas limit a transaction gets when gas
// estimation fails. A request with FallbackOnEstimateError (the
// chequebook's, by operator decision, as in Bee v2.8.3) gets
// gas-limit-fallback, never below its minimum; one without it (the lottery
// calls) keeps falling back to its minimum.
func TestEstimateErrorGasLimit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		fallback uint64 // gas-limit-fallback; 0 is the default, 900,000
		minimum  uint64
		flag     bool
		want     uint64
	}{
		{"chequebook, default fallback", 0, 175_000, true, transaction.FallbackGasLimit},
		{"chequebook, fallback set", 1_500_000, 175_000, true, 1_500_000},
		{"chequebook, fallback below the minimum", 100_000, 175_000, true, 175_000},
		{"lottery call, default fallback", 0, 800_000, false, 800_000},
		{"lottery call, fallback set", 1_500_000, 800_000, false, 800_000},
		{"no minimum", 1_200_000, 0, false, 1_200_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sender := common.HexToAddress("0xddff")
			recipient := common.HexToAddress("0xabcd")
			chainID := big.NewInt(5)
			var gotGas uint64
			signer := signermock.New(
				signermock.WithSignTxFunc(func(tx *types.Transaction, _ *big.Int) (*types.Transaction, error) {
					gotGas = tx.Gas()
					return tx, nil
				}),
				signermock.WithEthereumAddressFunc(func() (common.Address, error) { return sender, nil }),
			)
			service, err := transaction.NewService(log.Noop, sender,
				backendmock.New(
					backendmock.WithSendTransactionFunc(func(context.Context, *types.Transaction) error { return nil }),
					backendmock.WithEstimateGasFunc(func(context.Context, ethereum.CallMsg) (uint64, error) {
						return 0, errors.New("estimate failure")
					}),
					backendmock.WithPendingNonceAtFunc(func(context.Context, common.Address) (uint64, error) { return 1, nil }),
					backendmock.WithSuggestedFeeAndTipFunc(func(context.Context, *big.Int, int) (*big.Int, *big.Int, error) {
						return big.NewInt(3), big.NewInt(2), nil
					}),
				),
				signer,
				storemock.NewStateStore(),
				chainID,
				monitormock.New(monitormock.WithWatchTransactionFunc(func(common.Hash, uint64) (<-chan types.Receipt, <-chan error, error) {
					return nil, nil, nil
				})),
				tc.fallback,
			)
			if err != nil {
				t.Fatal(err)
			}
			testutil.CleanupCloser(t, service)

			_, err = service.Send(context.Background(), &transaction.TxRequest{
				To:                      &recipient,
				Data:                    []byte{1, 2, 3},
				Value:                   big.NewInt(0),
				MinEstimatedGasLimit:    tc.minimum,
				FallbackOnEstimateError: tc.flag,
			}, 0)
			if err != nil {
				t.Fatal(err)
			}
			if gotGas != tc.want {
				t.Fatalf("gas limit %d, want %d", gotGas, tc.want)
			}
		})
	}
}
