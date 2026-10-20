// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

type rpcAnswerError struct{ msg string }

func (a rpcAnswerError) Error() string  { return a.msg }
func (a rpcAnswerError) ErrorCode() int { return -32000 }

var _ rpc.Error = rpcAnswerError{}

// TestClassifySendAnswer keeps the answers each kind is matched on as
// fixtures (spec part 2). The go-ethereum texts are the full messages of
// go-ethereum v1.17.3's txpool and core errors, the Nethermind ones its
// result names.
func TestClassifySendAnswer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err  error
		want transaction.AnswerKind
	}{
		{nil, transaction.AnswerAccepted},
		{rpcAnswerError{"already known"}, transaction.AnswerAccepted},
		{rpcAnswerError{"known transaction: 7c1f"}, transaction.AnswerAccepted},
		{rpcAnswerError{"AlreadyKnown"}, transaction.AnswerAccepted},
		{rpcAnswerError{"nonce too low: address 0x0, tx: 7 state: 8"}, transaction.AnswerNonceUsed},
		{rpcAnswerError{"OldNonce"}, transaction.AnswerNonceUsed},
		{rpcAnswerError{"replacement transaction underpriced"}, transaction.AnswerHeldOther},
		{rpcAnswerError{"ReplacementNotAllowed"}, transaction.AnswerHeldOther},
		{rpcAnswerError{"transaction underpriced: tip needed 1, tip permitted 0"}, transaction.AnswerFeeTooLow},
		{rpcAnswerError{"FeeTooLow"}, transaction.AnswerFeeTooLow},
		{rpcAnswerError{"max fee per gas less than block base fee: address 0x0, maxFeePerGas: 1, baseFee: 7"}, transaction.AnswerFeeTooLow},
		{rpcAnswerError{"fee cap less than block base fee"}, transaction.AnswerFeeTooLow},
		{rpcAnswerError{"insufficient funds for gas * price + value: balance 0, tx cost 21000"}, transaction.AnswerDefinite},
		{rpcAnswerError{"intrinsic gas too low: gas 0, minimum needed 21000"}, transaction.AnswerDefinite},
		{rpcAnswerError{"gas limit reached"}, transaction.AnswerDefinite},
		{rpcAnswerError{"exceeds block gas limit"}, transaction.AnswerDefinite},
		{rpcAnswerError{"InsufficientFunds"}, transaction.AnswerDefinite},
		{rpcAnswerError{"GasLimitTooLow"}, transaction.AnswerDefinite},
		{rpcAnswerError{"txpool is full"}, transaction.AnswerOther},
		{rpc.HTTPError{StatusCode: http.StatusBadRequest, Status: "400 Bad Request"}, transaction.AnswerOther},
		{rpc.HTTPError{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests"}, transaction.AnswerUnreachable},
		{rpc.HTTPError{StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error"}, transaction.AnswerUnreachable},
		{context.DeadlineExceeded, transaction.AnswerUnreachable},
		{errors.New("connection refused"), transaction.AnswerUnreachable},
		{fmt.Errorf("%w: %w", transaction.ErrEndpointUnreachable, rpcAnswerError{"already known"}), transaction.AnswerUnreachable},
	} {
		if got := transaction.ClassifySendAnswer(tc.err); got != tc.want {
			t.Errorf("%v: got %v, want %v", tc.err, got, tc.want)
		}
	}
}
