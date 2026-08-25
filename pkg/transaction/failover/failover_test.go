// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/backendmock"
	"github.com/ethersphere/bee/v2/pkg/transaction/failover"
)

var errDown = &net.OpError{Op: "dial", Err: errors.New("connection refused")}

// answerErr is what an endpoint that WORKED returns when the chain says no.
var errReverted = errors.New("execution reverted")

func endpoint(name string, opts ...backendmock.Option) failover.Endpoint {
	return failover.Endpoint{Name: name, Backend: backendmock.New(opts...)}
}

func newBackend(t *testing.T, eps ...failover.Endpoint) *failover.Backend {
	t.Helper()
	b, err := failover.New(log.Noop, failover.DefaultMaxBlockLag, eps...)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestFailsOverOnTransportFailure is the basic promise: an endpoint that does
// not answer is replaced by one that does, without the caller noticing.
func TestFailsOverOnTransportFailure(t *testing.T) {
	t.Parallel()

	var primaryCalls, secondaryCalls atomic.Int32
	b := newBackend(t,
		endpoint("primary", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) {
			primaryCalls.Add(1)
			return 0, errDown
		})),
		endpoint("secondary", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) {
			secondaryCalls.Add(1)
			return 500, nil
		})),
	)

	n, err := b.BlockNumber(context.Background())
	if err != nil {
		t.Fatalf("expected the secondary to answer, got %v", err)
	}
	if n != 500 {
		t.Errorf("got block %d, want 500 from the secondary", n)
	}
	if b.Active() != "secondary" {
		t.Errorf("active endpoint is %q, expected the failover to stick", b.Active())
	}
	if primaryCalls.Load() != 1 || secondaryCalls.Load() != 1 {
		t.Errorf("calls: primary=%d secondary=%d, expected one each",
			primaryCalls.Load(), secondaryCalls.Load())
	}
}

// TestDoesNotFailOverOnAnAnswer is the property that keeps failover honest. A
// revert is a correct answer; retrying it elsewhere would both be wrong and
// hide a real contract or configuration problem.
func TestDoesNotFailOverOnAnAnswer(t *testing.T) {
	t.Parallel()

	var secondaryCalls atomic.Int32
	b := newBackend(t,
		endpoint("primary", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) {
			return 0, errReverted
		})),
		endpoint("secondary", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) {
			secondaryCalls.Add(1)
			return 500, nil
		})),
	)

	if _, err := b.BlockNumber(context.Background()); !errors.Is(err, errReverted) {
		t.Errorf("got %v, expected the answer to be returned unchanged", err)
	}
	if secondaryCalls.Load() != 0 {
		t.Error("the secondary was consulted about an answer the primary already gave; " +
			"a second opinion on a revert hides the problem instead of reporting it")
	}
	if b.Active() != "primary" {
		t.Errorf("active endpoint moved to %q on an answer", b.Active())
	}
}

// TestSendTransactionDoesNotFailOver: a lost response after the endpoint
// accepted the transaction would make a resend a duplicate, and two endpoints
// can disagree on pending nonce. The retry belongs to transaction.Service,
// which has the persisted state to do it safely.
func TestSendTransactionDoesNotFailOver(t *testing.T) {
	t.Parallel()

	var secondarySends atomic.Int32
	b := newBackend(t,
		endpoint("primary", backendmock.WithSendTransactionFunc(func(context.Context, *types.Transaction) error {
			return errDown
		})),
		endpoint("secondary", backendmock.WithSendTransactionFunc(func(context.Context, *types.Transaction) error {
			secondarySends.Add(1)
			return nil
		})),
	)

	tx := types.NewTx(&types.LegacyTx{Nonce: 1})
	if err := b.SendTransaction(context.Background(), tx); err == nil {
		t.Error("expected the transport failure to be reported to the caller")
	}
	if secondarySends.Load() != 0 {
		t.Error("the transaction was re-sent to a second endpoint; a lost response " +
			"may mean the first one accepted it, so this risks a duplicate")
	}
}

// TestAllEndpointsDown reports rather than hangs or silently returns zero.
func TestAllEndpointsDown(t *testing.T) {
	t.Parallel()

	down := backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) { return 0, errDown })
	b := newBackend(t, endpoint("a", down), endpoint("b", down), endpoint("c", down))

	_, err := b.BlockNumber(context.Background())
	if err == nil {
		t.Fatal("expected an error when every endpoint is down")
	}
	if !errors.Is(err, errDown) {
		t.Errorf("the underlying cause was lost: %v", err)
	}
}

// TestRecoversToThePreferredEndpoint. Order is the operator's preference, so
// staying on a standby after the primary returns is a slow failure of its own.
func TestRecoversToThePreferredEndpoint(t *testing.T) {
	t.Parallel()

	var primaryUp atomic.Bool
	b := newBackend(t,
		endpoint("primary", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) {
			if !primaryUp.Load() {
				return 0, errDown
			}
			return 1000, nil
		})),
		endpoint("secondary", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) {
			return 1000, nil
		})),
	)

	if _, err := b.BlockNumber(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.Active() != "secondary" {
		t.Fatalf("expected to be on the secondary, got %q", b.Active())
	}

	primaryUp.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Recover(ctx, 20*time.Millisecond)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b.Active() == "primary" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("still on %q after the primary recovered", b.Active())
}

// TestWillNotRecoverToAStaleEndpoint. A large backwards step in block height is
// the shape of a reorg to the postage listener, so an endpoint too far behind is
// skipped even though it answers.
func TestWillNotRecoverToAStaleEndpoint(t *testing.T) {
	t.Parallel()

	b := newBackend(t,
		endpoint("primary", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) {
			return 100, nil // answers, but far behind
		})),
		endpoint("secondary", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) {
			return 1000, nil
		})),
	)

	// Force the switch, then let the high-water mark be set from the secondary.
	failover.SetActive(b, 1)
	if _, err := b.BlockNumber(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Recover(ctx, 10*time.Millisecond)

	time.Sleep(300 * time.Millisecond)
	if b.Active() != "secondary" {
		t.Errorf("recovered to an endpoint %d blocks behind; the listener would see "+
			"that as a reorg", 1000-100)
	}
}

var _ transaction.Backend = (*failover.Backend)(nil)
