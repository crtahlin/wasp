// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package redistribution_test

import (
	"context"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/sctx"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/redistribution"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	transactionMock "github.com/ethersphere/bee/v2/pkg/transaction/mock"
)

// TestLotteryGasLimits checks the gas settings of the three lottery calls
// after the merge of Bee v2.8.3: every call estimates with at least 800,000
// gas (Bee v2.8.3 raised it from 500,000), a configured gas limit above the
// estimate still wins, and only the reveal carries wasp's fee floor of four
// times the base fee (#793).
func TestLotteryGasLimits(t *testing.T) {
	t.Parallel()

	const configured = 1_500_000

	var (
		mu   sync.Mutex
		sent = map[string]*transaction.TxRequest{}
	)
	txHash := common.HexToHash("cafe")
	contract := redistribution.New(
		swarm.NewAddress(common.HexToHash("cbd").Bytes()),
		common.HexToAddress("abcd"),
		log.Noop,
		transactionMock.New(
			transactionMock.WithSendFunc(func(_ context.Context, request *transaction.TxRequest, _ int) (common.Hash, error) {
				mu.Lock()
				defer mu.Unlock()
				sent[request.Description] = request
				return txHash, nil
			}),
			transactionMock.WithWaitForReceiptFunc(func(context.Context, common.Hash) (*types.Receipt, error) {
				return &types.Receipt{Status: 1}, nil
			}),
		),
		common.HexToAddress("ffff"),
		redistributionContractABI,
		configured,
	)

	ctx := sctx.SetGasPrice(context.Background(), big.NewInt(100))
	if _, err := contract.Commit(ctx, common.Hex2Bytes("hash"), 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := contract.Reveal(ctx, 8, common.Hex2Bytes("hash"), common.Hex2Bytes("nonce"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := contract.Claim(ctx, redistribution.ChunkInclusionProofs{}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		description string
		feeMultiple uint64
	}{
		{"commit transaction", 0},
		{"reveal transaction", redistribution.RevealBaseFeeMultiple},
		{"claim win transaction", 0},
	} {
		r, ok := sent[tc.description]
		if !ok {
			t.Fatalf("%s: not sent", tc.description)
		}
		if r.MinEstimatedGasLimit != 800_000 {
			t.Errorf("%s: minimum estimate %d, want 800000", tc.description, r.MinEstimatedGasLimit)
		}
		if r.GasLimit != configured {
			t.Errorf("%s: gas limit %d, want the configured %d", tc.description, r.GasLimit, configured)
		}
		if r.MinBaseFeeMultiple != tc.feeMultiple {
			t.Errorf("%s: fee floor multiple %d, want %d", tc.description, r.MinBaseFeeMultiple, tc.feeMultiple)
		}
	}
}
