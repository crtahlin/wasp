// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// lotteryDescriptions are the descriptions the storage incentives give their
// commit, reveal and claim; a stored transaction with one of them is a
// lottery transaction.
var lotteryDescriptions = []string{"commit transaction", "reveal transaction", "claim win transaction"}

// SetLotteryContract implements Service.
func (t *transactionService) SetLotteryContract(address common.Address) {
	t.lotteryContract.Store(&address)
}

// IsLotteryTransaction implements Service. A transaction to the
// redistribution contract is a lottery transaction whatever its marks say,
// because a transaction stored by an earlier build carries no
// SelfReplacing flag; a transaction marked SelfReplacing, or described as a
// commit, reveal or claim, is one too. An unknown transaction counts as one,
// so that it is never cancelled automatically.
func (t *transactionService) IsLotteryTransaction(txHash common.Hash) bool {
	stored, err := t.StoredTransaction(txHash)
	if err != nil {
		return true
	}
	if addr := t.lotteryContract.Load(); addr != nil && stored.To != nil && *stored.To == *addr {
		return true
	}
	if stored.SelfReplacing {
		return true
	}
	for _, d := range lotteryDescriptions {
		if strings.HasPrefix(stored.Description, d) {
			return true
		}
	}
	return false
}

// CancelForRevealGas is the gas limit of a cancellation that protects a
// reveal: a plain transfer.
const CancelForRevealGas = MinGasLimit

// CancellationFees returns the fee cap and tip of a cancellation of blocker:
// each the larger of the suggestion and the blocker's value plus
// ReplacementBumpPercent, and the fee cap never below the tip.
func CancellationFees(suggestedFeeCap, suggestedTip *big.Int, blocker *StoredTransaction) (feeCap, tip *big.Int) {
	feeCap = bigMax(suggestedFeeCap, bumped(blocker.GasFeeCap))
	tip = bigMax(suggestedTip, bumped(blocker.GasTipCap))
	return bigMax(feeCap, tip), tip
}

// CancelForReveal implements Service. The cancellation is a zero-value
// transfer to the node's own address at the blocker's nonce, its fee cap and
// tip each the larger of the current suggestion and the blocker's value
// plus ReplacementBumpPercent, so that an endpoint that still holds the
// blocker accepts it as a replacement. It is stored, with Replaces set to
// the blocker and CancelReason CancelReasonReveal, and the blocker marked
// CancelledForReveal, before it is sent (storeAndBroadcast); it is sent
// with copies to every endpoint and then watched like any other entry. The
// nonce is not released: the entry is removed only when the chain nonce
// passes it.
func (t *transactionService) CancelForReveal(ctx context.Context, txHash common.Hash, round uint64) (common.Hash, error) {
	t.lock.Lock()
	defer t.lock.Unlock()

	stored, err := t.StoredTransaction(txHash)
	if err != nil {
		return common.Hash{}, notBroadcast(err)
	}
	var blockedReason string
	if p, err := t.getPending(txHash); err == nil {
		blockedReason = p.BlockedReason
	}

	gasFeeCap, gasTipCap, err := t.backend.SuggestedFeeAndTip(ctx, nil, 0)
	if err != nil {
		return common.Hash{}, notBroadcast(err)
	}
	gasFeeCap, gasTipCap = CancellationFees(gasFeeCap, gasTipCap, stored)

	signedTx, err := t.signer.SignTx(types.NewTx(&types.DynamicFeeTx{
		Nonce:     stored.Nonce,
		ChainID:   t.chainID,
		To:        &t.sender,
		Value:     big.NewInt(0),
		Gas:       MinGasLimit,
		GasTipCap: gasTipCap,
		GasFeeCap: gasFeeCap,
		Data:      []byte{},
	}), t.chainID)
	if err != nil {
		return common.Hash{}, notBroadcast(err)
	}

	markBlocker := func(common.Hash) error {
		stored.CancelledForReveal = true
		return t.store.Put(storedTransactionKey(txHash), *stored)
	}
	newHash, err := t.storeAndBroadcast(ctx, signedTx, storeMeta{
		description:  fmt.Sprintf("cancellation to protect the reveal of round %d", round),
		replaces:     txHash,
		cancelReason: CancelReasonReveal,
	}, markBlocker)
	if newHash == (common.Hash{}) {
		return newHash, err
	}
	t.metrics.CancelledForReveal.Inc()
	t.logger.Error(nil, fmt.Sprintf("cancelled transaction %s at nonce %d to protect the reveal of round %d", txHash, stored.Nonce, round),
		"tx", txHash, "cancellation", newHash, "nonce", stored.Nonce, "round", round,
		"description", stored.Description, "blocked_reason", blockedReason)
	return newHash, err
}
