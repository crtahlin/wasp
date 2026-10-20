// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/bee/v2/pkg/metrics"
	"github.com/ethersphere/bee/v2/pkg/transaction/backendmock"
	"github.com/ethersphere/bee/v2/pkg/transaction/failover"
	"github.com/prometheus/client_golang/prometheus"
)

// answer is a JSON-RPC error answer, as the rpc client returns it.
type answer struct{ msg string }

func (a answer) Error() string  { return a.msg }
func (a answer) ErrorCode() int { return -32000 }

var _ rpc.Error = answer{}

// http500 is the failure seen on mainnet (#772): the endpoint's broker
// answered with an HTTP 500 around a JSON-RPC error.
var http500 = rpc.HTTPError{
	StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error",
	Body: []byte(`{"jsonrpc":"2.0","error":{"code":-32701,"message":"broker: node error"}}`),
}

// recorder records which endpoint got which transaction hash.
type recorder struct {
	mu   sync.Mutex
	sent map[string][]common.Hash
}

func (r *recorder) endpoint(name string, result error) failover.Endpoint {
	return endpoint(name, backendmock.WithSendTransactionFunc(func(_ context.Context, tx *types.Transaction) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.sent == nil {
			r.sent = map[string][]common.Hash{}
		}
		r.sent[name] = append(r.sent[name], tx.Hash())
		return result
	}))
}

func (r *recorder) got(name string) []common.Hash {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent[name]
}

func testTx() *types.Transaction { return types.NewTx(&types.LegacyTx{Nonce: 7, Gas: 21000}) }

// TestSendTransactionSendsTheSameBytesToTheNextEndpoint is the case that lost
// a won claim on mainnet: the active endpoint answers HTTP 500, another
// endpoint is healthy. Once the second endpoint has taken the transaction,
// the others get copies of the same bytes in the background (#819).
func TestSendTransactionSendsTheSameBytesToTheNextEndpoint(t *testing.T) {
	t.Parallel()

	var r recorder
	b := newBackend(t, r.endpoint("primary", http500), r.endpoint("secondary", nil), r.endpoint("third", nil))
	t.Cleanup(b.Close)
	tx := testTx()

	if err := b.SendTransaction(context.Background(), tx); err != nil {
		t.Fatalf("send: %v, want nil because the second endpoint took it", err)
	}
	if got := r.got("secondary"); len(got) != 1 || got[0] != tx.Hash() {
		t.Fatalf("secondary got %v, want exactly the same transaction %s", got, tx.Hash())
	}
	waitFor(t, 5*time.Second, func() bool { return len(r.got("third")) == 1 && len(r.got("primary")) == 2 })
	for _, name := range []string{"primary", "third"} {
		for _, h := range r.got(name) {
			if h != tx.Hash() {
				t.Fatalf("%s got %s, want only the same transaction %s", name, h, tx.Hash())
			}
		}
	}
	if b.Active() != "primary" {
		t.Fatalf("active endpoint is %s; a failed write must not move reads", b.Active())
	}
}

// TestSendTransactionAnswers checks what an answer from the second endpoint,
// after the first failed at the transport level, means. A taken
// transaction is copied to the other endpoints, so the third gets it; an
// answer that is returned sends no copy.
func TestSendTransactionAnswers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		secondary error
		wantErr   error // nil means success
		wantThird bool  // whether the third endpoint gets the transaction
	}{
		{name: "already known counts as sent", secondary: answer{"already known"}, wantThird: true},
		{name: "Nethermind AlreadyKnown counts as sent", secondary: answer{"AlreadyKnown"}, wantThird: true},
		{name: "nonce too low keeps the first error", secondary: answer{"nonce too low: next nonce 8, tx nonce 7"}, wantErr: http500},
		{name: "Nethermind OldNonce keeps the first error", secondary: answer{"OldNonce"}, wantErr: http500},
		{name: "replacement underpriced is returned", secondary: answer{"replacement transaction underpriced"}, wantErr: answer{"replacement transaction underpriced"}},
		{name: "insufficient funds is returned", secondary: answer{"insufficient funds for gas * price + value"}, wantErr: answer{"insufficient funds for gas * price + value"}},
		{name: "a transport failure moves on", secondary: errDown, wantThird: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var r recorder
			b := newBackend(t, r.endpoint("primary", http500), r.endpoint("secondary", tc.secondary), r.endpoint("third", nil))
			t.Cleanup(b.Close)
			err := b.SendTransaction(context.Background(), testTx())

			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("send: %v, want nil", err)
			case tc.wantErr != nil && (err == nil || err.Error() != tc.wantErr.Error()):
				t.Fatalf("send: %v, want %v", err, tc.wantErr)
			}
			if tc.wantThird {
				waitFor(t, 5*time.Second, func() bool { return len(r.got("third")) == 1 })
				return
			}
			time.Sleep(50 * time.Millisecond)
			if n := len(r.got("third")); n != 0 {
				t.Fatalf("third endpoint asked %d times, want 0", n)
			}
		})
	}
}

// TestSendTransactionAllEndpointsDown keeps today's result when nothing takes
// the transaction: the first error, each endpoint asked once.
func TestSendTransactionAllEndpointsDown(t *testing.T) {
	t.Parallel()

	var r recorder
	b := newBackend(t, r.endpoint("primary", http500), r.endpoint("secondary", errDown), r.endpoint("third", errDown))

	err := b.SendTransaction(context.Background(), testTx())
	var httpErr rpc.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("send: %v, want the first endpoint's HTTP 500", err)
	}
	for _, name := range []string{"primary", "secondary", "third"} {
		if n := len(r.got(name)); n != 1 {
			t.Fatalf("%s asked %d times, want 1", name, n)
		}
	}
}

// TestSendTransactionAnAnswerIsNotRetried: an endpoint that answered, even
// with an error, worked. Nothing else is asked.
func TestSendTransactionAnAnswerIsNotRetried(t *testing.T) {
	t.Parallel()

	var r recorder
	b := newBackend(t, r.endpoint("primary", answer{"insufficient funds for gas * price + value"}), r.endpoint("secondary", nil))

	if err := b.SendTransaction(context.Background(), testTx()); err == nil {
		t.Fatal("send succeeded, want the primary's answer")
	}
	if n := len(r.got("secondary")); n != 0 {
		t.Fatalf("secondary asked %d times after an answer, want 0", n)
	}
}

// TestSendTransactionStopsWhenTheCallerGaveUp: an ended context is itself
// classified as a transport failure, and must not run through the list.
func TestSendTransactionStopsWhenTheCallerGaveUp(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var secondary atomic.Int32
	b := newBackend(t,
		endpoint("primary", backendmock.WithSendTransactionFunc(func(context.Context, *types.Transaction) error {
			cancel()
			return context.DeadlineExceeded
		})),
		endpoint("secondary", backendmock.WithSendTransactionFunc(func(context.Context, *types.Transaction) error {
			secondary.Add(1)
			return nil
		})),
	)

	if err := b.SendTransaction(ctx, testTx()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("send: %v, want the first error", err)
	}
	if n := secondary.Load(); n != 0 {
		t.Fatalf("secondary asked %d times after the caller gave up, want 0", n)
	}
}

// pkg/node registers the chain backend's metrics through metrics.Collector
// only, so the backend must satisfy it, and its collectors must register into
// one registry without a conflict (#779).
var _ metrics.Collector = (*failover.Backend)(nil)

func TestMetricsRegisterOnce(t *testing.T) {
	t.Parallel()

	b := newBackend(t, endpoint("primary"), endpoint("secondary"))
	reg := prometheus.NewRegistry()
	for _, c := range b.Metrics() {
		if err := reg.Register(c); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	if err := reg.Register(b.Metrics()[0]); err == nil {
		t.Fatal("a second registration of the same collector succeeded; the registry check this test relies on is not working")
	}
}
