// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// SendOutcomeUnknown reports whether a transaction for which Send or
// SendWithHook returned txHash and err may still be mined (#802).
//
// That is the case when the transaction was signed and stored (a hash came
// back and the error does not wrap ErrNotBroadcast) and the send failed
// without an answer from the endpoint: a transport error, a timeout or an
// HTTP error status such as 429. The service then keeps the transaction,
// counts its nonce as used and sends it again (rebroadcastAmbiguous), so it
// can be mined later. A JSON-RPC error answer (rpc.Error) means an endpoint
// read the transaction and refused it, and is a definite failure.
func SendOutcomeUnknown(txHash common.Hash, err error) bool {
	if err == nil || txHash == (common.Hash{}) || errors.Is(err, ErrNotBroadcast) {
		return false
	}
	var answer rpc.Error
	return !errors.As(err, &answer)
}

// WaitForSentReceipt waits for the receipt of a transaction for which Send
// returned txHash and sendErr. Without a send error it is WaitForReceipt.
// When the outcome of the send is unknown (SendOutcomeUnknown), the receipt
// decides: it waits for it, at most AmbiguousPendingTimeout, the time during
// which the service keeps the transaction's nonce for it. Any other send
// error is returned at once.
//
// A caller that sends a second transaction which depends on the first, such
// as an ERC-20 approve followed by the call that spends the allowance, uses
// it so that a send which failed at the transport level but was mined later
// does not abort the operation with the first transaction already on chain.
// The caller still checks the receipt's status.
func WaitForSentReceipt(ctx context.Context, s Service, txHash common.Hash, sendErr error) (*types.Receipt, error) {
	if sendErr == nil {
		return s.WaitForReceipt(ctx, txHash)
	}
	if !SendOutcomeUnknown(txHash, sendErr) {
		return nil, sendErr
	}
	ctx, cancel := context.WithTimeout(ctx, AmbiguousPendingTimeout)
	defer cancel()
	receipt, err := s.WaitForReceipt(ctx, txHash)
	if err != nil {
		return nil, errors.Join(sendErr, err)
	}
	return receipt, nil
}
