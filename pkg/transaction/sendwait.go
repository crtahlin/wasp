// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/log"
)

// SendOutcomeUnknown reports whether a transaction for which Send or
// SendWithHook returned txHash and err may still be mined (#802).
//
// That is the case for every send error after the transaction was signed
// and stored: a hash came back and the error does not wrap ErrNotBroadcast.
// The service then keeps the transaction, counts its nonce as used and
// sends it again (rebroadcastAmbiguous), whatever the error was. A JSON-RPC
// error answer is included: with several endpoints, an earlier endpoint
// may have taken the transaction before the answer came from another one,
// and a refusal can be temporary (a full pool, a rate limit inside an HTTP
// 200). See docs/experiments/ambiguous-send-receipt-wait/spec.md.
func SendOutcomeUnknown(txHash common.Hash, err error) bool {
	return err != nil && txHash != (common.Hash{}) && !errors.Is(err, ErrNotBroadcast)
}

// WaitForSentReceipt waits for the receipt of a transaction for which Send
// returned txHash and sendErr, using s.WaitForReceipt. Without a send error
// it is WaitForReceipt. When the outcome of the send is unknown
// (SendOutcomeUnknown), the receipt decides: it waits for it, at most
// AmbiguousPendingTimeout, the time during which the service keeps the
// transaction's nonce for it. When that wait ends without a receipt (the
// bound or the caller's context), it logs the transaction's hash at
// warning level, so that an operator can check it on chain before sending
// the operation again. A send error before the broadcast is returned at
// once.
//
// The caller still checks the receipt's status. Service.WaitForSentReceipt
// calls it with the service's logger.
func WaitForSentReceipt(ctx context.Context, s Service, logger log.Logger, txHash common.Hash, sendErr error) (*types.Receipt, error) {
	if sendErr == nil {
		return s.WaitForReceipt(ctx, txHash)
	}
	if !SendOutcomeUnknown(txHash, sendErr) {
		return nil, sendErr
	}
	wctx, cancel := context.WithTimeout(ctx, AmbiguousPendingTimeout)
	defer cancel()
	receipt, err := s.WaitForReceipt(wctx, txHash)
	if err != nil {
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			// An answer about the transaction, such as
			// ErrTransactionCancelled: its nonce was used by another.
			return nil, errors.Join(sendErr, err)
		}
		var description string
		if stored, serr := s.StoredTransaction(txHash); serr == nil {
			description = stored.Description
		}
		logger.Warning("transaction may still be mined; check it on chain before sending it again",
			"tx", txHash, "description", description, "error", sendErr)
		return nil, errors.Join(sendErr, err)
	}
	return receipt, nil
}

// WaitForSentReceipt implements Service.
func (t *transactionService) WaitForSentReceipt(ctx context.Context, txHash common.Hash, sendErr error) (*types.Receipt, error) {
	return WaitForSentReceipt(ctx, t, t.logger, txHash, sendErr)
}
