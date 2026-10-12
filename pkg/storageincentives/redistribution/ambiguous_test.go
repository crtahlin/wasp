// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package redistribution_test

import (
	"context"
	"errors"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/sctx"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/redistribution"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	transactionMock "github.com/ethersphere/bee/v2/pkg/transaction/mock"
)

var errSendReset = errors.New("connection reset")

// ambiguousContract returns a contract whose send returns a hash together
// with a transport error, as the transaction service does for a send that may
// still reach the network (#772). receipt decides what waiting finds.
func ambiguousContract(t *testing.T, waits *atomic.Int32, receipt func(context.Context) (*types.Receipt, error)) redistribution.Contract {
	t.Helper()
	txHash := common.HexToHash("a772")
	return redistribution.New(
		swarm.NewAddress(common.HexToHash("cbd").Bytes()),
		common.HexToAddress("abcd"),
		log.Noop,
		transactionMock.New(
			transactionMock.WithSendFunc(func(context.Context, *transaction.TxRequest, int) (common.Hash, error) {
				return txHash, errSendReset
			}),
			transactionMock.WithWaitForReceiptFunc(func(ctx context.Context, h common.Hash) (*types.Receipt, error) {
				waits.Add(1)
				if h != txHash {
					return nil, errors.New("waited for the wrong transaction")
				}
				return receipt(ctx)
			}),
		),
		common.HexToAddress("ffff"),
		redistributionContractABI,
		0,
	)
}

func gasCtx() context.Context { return sctx.SetGasPrice(context.Background(), big.NewInt(100)) }

// A claim whose send failed ambiguously, and which is then mined, is a
// successful claim: the case that lost a won round on mainnet.
func TestClaimWaitsOnAnAmbiguousSend(t *testing.T) {
	t.Parallel()

	var waits atomic.Int32
	c := ambiguousContract(t, &waits, func(context.Context) (*types.Receipt, error) {
		return &types.Receipt{Status: 1}, nil
	})
	if _, err := c.Claim(gasCtx(), randChunkInclusionProofs(t)); err != nil {
		t.Fatalf("claim: %v, want nil because the transaction was mined", err)
	}
	if waits.Load() != 1 {
		t.Fatalf("waited %d times, want 1", waits.Load())
	}
}

func TestCommitWaitsOnAnAmbiguousSend(t *testing.T) {
	t.Parallel()

	var waits atomic.Int32
	c := ambiguousContract(t, &waits, func(context.Context) (*types.Receipt, error) {
		return &types.Receipt{Status: 1}, nil
	})
	if _, err := c.Commit(gasCtx(), common.Hex2Bytes("hash"), 0, nil); err != nil {
		t.Fatalf("commit: %v, want nil because the transaction was mined", err)
	}
}

// When nothing is mined before the phase ends, the claim reports both the
// send error and the end of the wait.
func TestClaimOnAnAmbiguousSendEndsWithItsContext(t *testing.T) {
	t.Parallel()

	var waits atomic.Int32
	c := ambiguousContract(t, &waits, func(ctx context.Context) (*types.Receipt, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(gasCtx(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Claim(ctx, randChunkInclusionProofs(t))
	if !errors.Is(err, errSendReset) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("claim: %v, want both the send error and the deadline", err)
	}
}

// A reveal keeps failing at once: the revealer checks it every block and
// replaces it with raised fees, which a wait would hold up.
func TestRevealDoesNotWaitOnAnAmbiguousSend(t *testing.T) {
	t.Parallel()

	var waits atomic.Int32
	c := ambiguousContract(t, &waits, func(context.Context) (*types.Receipt, error) {
		return &types.Receipt{Status: 1}, nil
	})
	if _, err := c.Reveal(gasCtx(), 9, common.Hex2Bytes("hash"), common.Hex2Bytes("nonce"), nil); !errors.Is(err, errSendReset) {
		t.Fatalf("reveal: %v, want the send error at once", err)
	}
	if waits.Load() != 0 {
		t.Fatalf("reveal waited %d times, want 0", waits.Load())
	}
}

// A send that never reached the network returns at once, as before.
func TestClaimDoesNotWaitWhenNotBroadcast(t *testing.T) {
	t.Parallel()

	var waits atomic.Int32
	c := redistribution.New(
		swarm.NewAddress(common.HexToHash("cbd").Bytes()),
		common.HexToAddress("abcd"),
		log.Noop,
		transactionMock.New(
			// A hash with ErrNotBroadcast does not happen today; the error
			// alone must decide, whatever comes with it.
			transactionMock.WithSendFunc(func(context.Context, *transaction.TxRequest, int) (common.Hash, error) {
				return common.HexToHash("b772"), transaction.ErrNotBroadcast
			}),
			transactionMock.WithWaitForReceiptFunc(func(context.Context, common.Hash) (*types.Receipt, error) {
				waits.Add(1)
				return &types.Receipt{Status: 1}, nil
			}),
		),
		common.HexToAddress("ffff"),
		redistributionContractABI,
		0,
	)
	if _, err := c.Claim(gasCtx(), randChunkInclusionProofs(t)); !errors.Is(err, transaction.ErrNotBroadcast) {
		t.Fatalf("claim: %v, want ErrNotBroadcast", err)
	}
	if waits.Load() != 0 {
		t.Fatalf("waited %d times for a transaction that was never broadcast", waits.Load())
	}
}
