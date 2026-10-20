// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// ErrRebuiltHashDiffers is returned by ResendTransaction when a stored
// transaction without its signed bytes, rebuilt from its stored fields, does
// not have its original hash. Nothing is sent.
var ErrRebuiltHashDiffers = errors.New("transaction rebuilt from its stored fields has a different hash")

// ResendResult is what ResendTransaction did. See
// docs/experiments/send-everywhere/spec.md, part 5.
type ResendResult struct {
	// Mined: the transaction itself was mined, in BlockNumber.
	Mined       bool
	BlockNumber uint64
	// NonceUsed: another transaction used the nonce; NonceUsedBy is its
	// hash when this node stored it, zero when it did not.
	NonceUsed   bool
	NonceUsedBy common.Hash
	// State and Answers, when the nonce was not used yet and the stored
	// bytes were sent: the transaction's state (pending, overdue or
	// blocked) and each endpoint's answer.
	State   string
	Answers []SendAnswer
}

// ResendTransaction sends the stored signed bytes again, unchanged, to
// every endpoint, on the caller's context. A freshly suggested fee is never
// used. When the nonce is used already, nothing is sent and the result says
// by what.
func (t *transactionService) ResendTransaction(ctx context.Context, txHash common.Hash) (*ResendResult, error) {
	stored, err := t.StoredTransaction(txHash)
	if err != nil {
		return nil, err
	}
	chainNonce, err := t.backend.NonceAt(ctx, t.sender, nil)
	if err != nil {
		return nil, fmt.Errorf("reading the chain nonce: %w", err)
	}
	if chainNonce > stored.Nonce {
		if receipt, err := t.backend.TransactionReceipt(ctx, txHash); err == nil && receipt != nil {
			res := &ResendResult{Mined: true}
			if receipt.BlockNumber != nil {
				res.BlockNumber = receipt.BlockNumber.Uint64()
			}
			return res, nil
		}
		for _, other := range t.storedAtNonce(stored.Nonce, txHash) {
			if receipt, err := t.backend.TransactionReceipt(ctx, other); err == nil && receipt != nil {
				return &ResendResult{NonceUsed: true, NonceUsedBy: other}, nil
			}
		}
		return &ResendResult{NonceUsed: true}, nil
	}

	tx, err := t.signedTransaction(txHash, stored)
	if err != nil {
		return nil, err
	}
	answers := t.broadcast(ctx, tx)
	for _, a := range answers {
		if a.Kind() == AnswerAccepted {
			// an endpoint holds the bytes: the transaction can be mined
			// again, so it is no longer blocked
			err := t.updatePending(txHash, func(p *pendingTransaction) { p.Blocked, p.BlockedReason = false, "" })
			if err != nil && !isNotFound(err) {
				t.logger.Error(err, "clearing a blocked mark failed", "tx", txHash)
			}
			break
		}
	}
	state, err := t.PendingState(txHash)
	if err != nil {
		return nil, err
	}
	return &ResendResult{State: state.State, Answers: answers}, nil
}

// signedTransaction returns the stored signed bytes as a transaction, or,
// for a record stored without them, the transaction rebuilt from its stored
// fields, the fee cap and tip included. Signing is deterministic (RFC 6979),
// so a rebuilt transaction has the original hash; when it does not,
// ErrRebuiltHashDiffers.
func (t *transactionService) signedTransaction(txHash common.Hash, stored *StoredTransaction) (*types.Transaction, error) {
	if len(stored.Raw) > 0 {
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(stored.Raw); err != nil {
			return nil, err
		}
		if tx.Hash() != txHash {
			return nil, fmt.Errorf("stored transaction %s decodes to hash %s", txHash, tx.Hash())
		}
		return tx, nil
	}
	signed, err := t.signer.SignTx(types.NewTx(&types.DynamicFeeTx{
		Nonce:     stored.Nonce,
		ChainID:   t.chainID,
		To:        stored.To,
		Value:     stored.Value,
		Gas:       stored.GasLimit,
		GasTipCap: stored.GasTipCap,
		GasFeeCap: stored.GasFeeCap,
		Data:      stored.Data,
	}), t.chainID)
	if err != nil {
		return nil, err
	}
	if signed.Hash() != txHash {
		return nil, fmt.Errorf("%w: stored %s, rebuilt %s", ErrRebuiltHashDiffers, txHash, signed.Hash())
	}
	return signed, nil
}

// broadcast sends tx to every endpoint the backend has and returns each
// answer; a backend that cannot broadcast gets one send, whose error is the
// only answer.
func (t *transactionService) broadcast(ctx context.Context, tx *types.Transaction) []SendAnswer {
	if b, ok := t.backend.(Broadcaster); ok {
		return b.Broadcast(ctx, tx)
	}
	return []SendAnswer{{Endpoint: "backend", Err: t.backend.SendTransaction(ctx, tx)}}
}
