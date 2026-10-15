// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	signermock "github.com/ethersphere/bee/v2/pkg/crypto/mock"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/sctx"
	storemock "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/backendmock"
	"github.com/ethersphere/bee/v2/pkg/transaction/monitormock"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

// A request with MinBaseFeeMultiple is signed with a fee cap of at least
// that many times the latest base fee plus the tip; a request without it,
// or with a fixed gas price, keeps the suggested cap (#793 test 5).
func TestSendMinBaseFeeMultiple(t *testing.T) {
	t.Parallel()

	const base, tip = 1000, 3
	suggestedCap := big.NewInt(2*base + tip)

	for _, tc := range []struct {
		name     string
		multiple uint64
		gasPrice *big.Int
		wantCap  *big.Int
	}{
		{"none", 0, nil, suggestedCap},
		{"four", 4, nil, big.NewInt(4*base + tip)},
		{"below the suggestion", 1, nil, suggestedCap},
		{"fixed gas price", 4, big.NewInt(5000), big.NewInt(5000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var signed *types.Transaction
			recipient := common.HexToAddress("0xabcd")
			svc, err := transaction.NewService(log.Noop, common.HexToAddress("0xddff"),
				backendmock.New(
					backendmock.WithSendTransactionFunc(func(context.Context, *types.Transaction) error { return nil }),
					backendmock.WithPendingNonceAtFunc(func(context.Context, common.Address) (uint64, error) { return 1, nil }),
					backendmock.WithSuggestedFeeAndTipFunc(func(_ context.Context, gasPrice *big.Int, _ int) (*big.Int, *big.Int, error) {
						if gasPrice != nil {
							return new(big.Int).Set(gasPrice), new(big.Int).Sub(gasPrice, big.NewInt(base)), nil
						}
						return new(big.Int).Set(suggestedCap), big.NewInt(tip), nil
					}),
					backendmock.WithHeaderbyNumberFunc(func(context.Context, *big.Int) (*types.Header, error) {
						return &types.Header{Number: big.NewInt(10), BaseFee: big.NewInt(base)}, nil
					}),
				),
				signermock.New(signermock.WithSignTxFunc(func(tx *types.Transaction, _ *big.Int) (*types.Transaction, error) {
					signed = tx
					return tx, nil
				})),
				storemock.NewStateStore(),
				big.NewInt(5),
				monitormock.New(monitormock.WithWatchTransactionFunc(func(common.Hash, uint64) (<-chan types.Receipt, <-chan error, error) {
					return nil, nil, nil
				})),
				0,
			)
			if err != nil {
				t.Fatal(err)
			}
			testutil.CleanupCloser(t, svc)

			ctx := context.Background()
			if tc.gasPrice != nil {
				ctx = sctx.SetGasPrice(ctx, tc.gasPrice)
			}
			request := &transaction.TxRequest{
				To:                 &recipient,
				GasLimit:           100_000,
				Value:              big.NewInt(0),
				GasPrice:           sctx.GetGasPrice(ctx),
				MinBaseFeeMultiple: tc.multiple,
			}
			if _, err := svc.Send(ctx, request, 0); err != nil {
				t.Fatal(err)
			}
			if signed == nil {
				t.Fatal("nothing signed")
			}
			if signed.GasFeeCap().Cmp(tc.wantCap) != 0 {
				t.Fatalf("fee cap %s, want %s", signed.GasFeeCap(), tc.wantCap)
			}
			if signed.GasTipCap().Cmp(big.NewInt(tip)) != 0 && tc.gasPrice == nil {
				t.Fatalf("tip %s changed, want %d", signed.GasTipCap(), tip)
			}
		})
	}
}
