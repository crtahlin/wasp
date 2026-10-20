// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/failover"
)

// http429 is what both rate-limited endpoints of a Sepolia test node
// answered to a send (#802).
var http429 = rpc.HTTPError{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests"}

// dialled is an endpoint with no connection whose Dial counts its calls and
// returns backend, or errDown when backend is nil.
func dialled(name string, backend transaction.Backend, dials *atomic.Int32) failover.Endpoint {
	return deadEndpoint(name, func(context.Context) (transaction.Backend, error) {
		dials.Add(1)
		if backend == nil {
			return nil, errDown
		}
		return backend, nil
	})
}

// TestSendTransactionReachesAnUnconnectedEndpoint is the #802 case: the two
// connected endpoints rate-limit the send, and the third configured
// endpoint, which has no connection, must still be tried.
func TestSendTransactionReachesAnUnconnectedEndpoint(t *testing.T) {
	t.Parallel()

	var (
		r     recorder
		dials atomic.Int32
	)
	b := newBackend(t,
		r.endpoint("primary", http429),
		r.endpoint("secondary", http429),
		dialled("third", r.endpoint("third", nil).Backend, &dials),
	)
	tx := testTx()

	if err := b.SendTransaction(context.Background(), tx); err != nil {
		t.Fatalf("send: %v, want nil because the third endpoint took it", err)
	}
	if got := r.got("third"); len(got) != 1 || got[0] != tx.Hash() {
		t.Fatalf("third got %v, want exactly the transaction %s", got, tx.Hash())
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("third endpoint dialled %d times, want 1", n)
	}

	// The connection is kept: a second send that the connected endpoints
	// refuse reaches the third endpoint without dialling it again.
	if err := b.SendTransaction(context.Background(), tx); err != nil {
		t.Fatalf("second send: %v, want nil", err)
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("third endpoint dialled %d times after two sends, want 1", n)
	}
}

// TestSendTransactionTriesConnectedEndpointsFirst asserts an endpoint with
// no connection is dialled only after every connected one has failed, so a
// healthy connected endpoint later in the list is not delayed by a dial. The
// unconnected endpoint is dialled afterwards, in the background, for its
// copy (#819). Its dial is held until the send has returned: a send that
// dialled it first would not return.
func TestSendTransactionTriesConnectedEndpointsFirst(t *testing.T) {
	t.Parallel()

	var (
		r     recorder
		dials atomic.Int32
	)
	gate := make(chan struct{})
	b := newBackend(t,
		r.endpoint("primary", http429),
		deadEndpoint("secondary", func(ctx context.Context) (transaction.Backend, error) {
			dials.Add(1)
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return r.endpoint("secondary", nil).Backend, nil
		}),
		r.endpoint("third", nil),
	)
	t.Cleanup(b.Close)
	tx := testTx()

	_, err := within(t, 2*time.Second, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, b.SendTransaction(ctx, tx)
	})
	if err != nil {
		t.Fatalf("send: %v, want nil because the third endpoint took it", err)
	}
	if len(r.got("third")) != 1 {
		t.Fatalf("third endpoint got %v, want the transaction", r.got("third"))
	}
	close(gate)
	waitFor(t, 5*time.Second, func() bool { return len(r.got("secondary")) == 1 })
	if n := dials.Load(); n != 1 {
		t.Fatalf("unconnected secondary dialled %d times, want 1 for its copy", n)
	}
}

// TestSendTransactionUnconnectedEndpointDown keeps the first error when the
// unconnected endpoint cannot be dialled either.
func TestSendTransactionUnconnectedEndpointDown(t *testing.T) {
	t.Parallel()

	var (
		r     recorder
		dials atomic.Int32
	)
	b := newBackend(t,
		r.endpoint("primary", http429),
		r.endpoint("secondary", http429),
		dialled("third", nil, &dials),
	)

	err := b.SendTransaction(context.Background(), testTx())
	var httpErr rpc.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("send: %v, want the first endpoint's HTTP 429", err)
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("third endpoint dialled %d times, want 1", n)
	}
}

// TestPersistentSendDialsAnUnconnectedEndpointOnce asserts a persistent
// send, which goes round the endpoints again every pause until one takes
// the transaction, dials an unreachable endpoint in its first round only,
// not once a round.
func TestPersistentSendDialsAnUnconnectedEndpointOnce(t *testing.T) {
	defer failover.SetWrapPause(time.Millisecond)()

	var (
		r     recorder
		dials atomic.Int32
	)
	b := newBackend(t,
		r.endpoint("primary", http429),
		r.endpoint("secondary", http429),
		dialled("third", nil, &dials),
	)
	t.Cleanup(b.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.SendTransaction(persistent(ctx, time.Second), testTx()) }()

	waitFor(t, 5*time.Second, func() bool { return failover.Wraparounds(b) >= 3 })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("persistent send still running after its context ended")
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("unreachable endpoint dialled %d times over %v rounds, want 1", n, failover.Wraparounds(b)+1)
	}
}
