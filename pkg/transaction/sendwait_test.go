// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/bee/v2/pkg/log"
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

	errHTTP429Wrapped = fmt.Errorf("send: %w", http429)
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
		{name: "a JSON-RPC answer", hash: someHash, err: refusalError{"txpool is full"}, want: true},
		{name: "a wrapped JSON-RPC answer", hash: someHash, err: fmt.Errorf("send: %w", refusalError{"nonce too low"}), want: true},
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
		{name: "a JSON-RPC answer", sendErr: refusalError{"txpool is full"}, want: true},
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
		{name: "refused for a while, mined later", sendErr: refusalError{"txpool is full"}, wantWait: true},
		{name: "not broadcast", sendErr: fmt.Errorf("%w: sign", transaction.ErrNotBroadcast), wantErrs: []error{transaction.ErrNotBroadcast}},
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

			receipt, err := s.WaitForSentReceipt(context.Background(), someHash, tc.sendErr)
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

// TestWaitForSentReceiptLogsTheHash asserts the warning an operator needs
// before retrying: when the wait after a send error ends without a
// receipt, the transaction's hash is logged at warning level. A
// transaction known to be replaced (ErrTransactionCancelled) is not
// reported as possibly mined.
func TestWaitForSentReceiptLogsTheHash(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		waitErr error
		wantLog bool
	}{
		{name: "the wait ends", waitErr: context.DeadlineExceeded, wantLog: true},
		{name: "the caller gave up", waitErr: context.Canceled, wantLog: true},
		{name: "cancelled by another transaction", waitErr: transaction.ErrTransactionCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			logger := log.NewLogger("test", log.WithSink(&buf), log.WithVerbosity(log.VerbosityWarning))
			s := transactionmock.New(
				transactionmock.WithWaitForReceiptFunc(func(context.Context, common.Hash) (*types.Receipt, error) {
					return nil, tc.waitErr
				}),
				transactionmock.WithStoredTransactionFunc(func(common.Hash) (*transaction.StoredTransaction, error) {
					return &transaction.StoredTransaction{Description: "Postage batch creation"}, nil
				}),
			)

			_, err := transaction.WaitForSentReceipt(context.Background(), s, logger, someHash, errHTTP429Wrapped)
			if !errors.Is(err, tc.waitErr) {
				t.Fatalf("error %v does not contain %v", err, tc.waitErr)
			}
			out := buf.String()
			logged := strings.Contains(out, "may still be mined") && strings.Contains(out, someHash.String())
			if logged != tc.wantLog {
				t.Fatalf("warning with the hash logged: %v, want %v; log: %q", logged, tc.wantLog, out)
			}
			if tc.wantLog && (!strings.Contains(out, `"level"="warning"`) || !strings.Contains(out, "Postage batch creation")) {
				t.Fatalf("want a warning naming the transaction's description; log: %q", out)
			}
		})
	}
}
