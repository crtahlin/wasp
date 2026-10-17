// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	transactionmock "github.com/ethersphere/bee/v2/pkg/transaction/mock"
)

// refusalError is a JSON-RPC error answer to a send.
type refusalError struct{ msg string }

func (r refusalError) Error() string  { return r.msg }
func (r refusalError) ErrorCode() int { return -32000 }

var (
	_ rpc.Error = refusalError{}

	http429     = rpc.HTTPError{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests"}
	errConnDown = &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	someHash    = common.HexToHash("0xabcd")
)

func TestSendOutcomeUnknown(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		hash common.Hash
		err  error
		want bool
	}{
		{name: "sent", hash: someHash},
		{name: "HTTP 429", hash: someHash, err: http429, want: true},
		{name: "wrapped HTTP 429", hash: someHash, err: fmt.Errorf("send: %w", http429), want: true},
		{name: "connection refused", hash: someHash, err: errConnDown, want: true},
		{name: "timeout", hash: someHash, err: context.DeadlineExceeded, want: true},
		{name: "a JSON-RPC answer", hash: someHash, err: refusalError{"insufficient funds for gas * price + value"}},
		{name: "a wrapped JSON-RPC answer", hash: someHash, err: fmt.Errorf("send: %w", refusalError{"nonce too low"})},
		{name: "not broadcast", hash: someHash, err: fmt.Errorf("%w: sign", transaction.ErrNotBroadcast)},
		{name: "no hash", err: http429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := transaction.SendOutcomeUnknown(tc.hash, tc.err); got != tc.want {
				t.Fatalf("SendOutcomeUnknown(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestSendOutcomeUnknownFromService checks the classification against what
// the transaction service really returns for a failed send: the hash of the
// stored transaction together with the backend's error.
func TestSendOutcomeUnknownFromService(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		sendErr error
		want    bool
	}{
		{name: "HTTP 429", sendErr: http429, want: true},
		{name: "a JSON-RPC answer", sendErr: refusalError{"insufficient funds for gas * price + value"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newBroadcastFixture(t)
			f.sendErr = tc.sendErr
			txHash, err := f.service.Send(context.Background(), broadcastRequest(), 0)
			if err == nil || txHash == (common.Hash{}) {
				t.Fatalf("Send = %s, %v; want the stored transaction's hash and the send error", txHash, err)
			}
			if got := transaction.SendOutcomeUnknown(txHash, err); got != tc.want {
				t.Fatalf("SendOutcomeUnknown = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWaitForSentReceipt(t *testing.T) {
	t.Parallel()

	mined := &types.Receipt{TxHash: someHash, Status: types.ReceiptStatusSuccessful}
	errWait := errors.New("wait failed")

	for _, tc := range []struct {
		name     string
		sendErr  error
		waitErr  error
		wantWait bool
		wantErrs []error // each must be in the returned error; none means success
	}{
		{name: "sent", wantWait: true},
		{name: "outcome unknown, mined later", sendErr: http429, wantWait: true},
		{name: "outcome unknown, wait fails", sendErr: errConnDown, waitErr: errWait, wantWait: true, wantErrs: []error{errConnDown, errWait}},
		{name: "refused", sendErr: refusalError{"insufficient funds for gas * price + value"}, wantErrs: []error{refusalError{"insufficient funds for gas * price + value"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var waited bool
			s := transactionmock.New(transactionmock.WithWaitForReceiptFunc(func(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
				waited = true
				if tc.sendErr != nil {
					// The wait after an unknown outcome is bounded by the
					// time the service keeps the transaction's nonce.
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > transaction.AmbiguousPendingTimeout {
						t.Errorf("wait deadline %v (set %v), want at most AmbiguousPendingTimeout from now", deadline, ok)
					}
				}
				if tc.waitErr != nil {
					return nil, tc.waitErr
				}
				return mined, nil
			}))

			receipt, err := transaction.WaitForSentReceipt(context.Background(), s, someHash, tc.sendErr)
			if waited != tc.wantWait {
				t.Fatalf("waited for the receipt: %v, want %v", waited, tc.wantWait)
			}
			if len(tc.wantErrs) == 0 {
				if err != nil || receipt != mined {
					t.Fatalf("WaitForSentReceipt = %v, %v; want the receipt", receipt, err)
				}
				return
			}
			for _, want := range tc.wantErrs {
				if !errors.Is(err, want) {
					t.Fatalf("WaitForSentReceipt error %v does not contain %v", err, want)
				}
			}
		})
	}
}
