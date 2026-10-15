// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/backendmock"
	"github.com/ethersphere/bee/v2/pkg/transaction/failover"
)

// persistent marks a context the way the storage lottery does (#783).
func persistent(ctx context.Context, attempt time.Duration) context.Context {
	return transaction.WithPersistentCalls(ctx, attempt)
}

// within runs f and fails the test if it has not returned within d. The
// context passed to f is cancelled when the guard fires, so a call blocked on
// a silent endpoint is released and the test ends with a message rather than
// a hang.
func within[T any](t *testing.T, d time.Duration, f func(context.Context) (T, error)) (T, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type res struct {
		v   T
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := f(ctx)
		done <- res{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-time.After(d):
		cancel()
		<-done
		t.Fatalf("call still running after %v", d)
		var zero T
		return zero, nil
	}
}

// TestPersistentCallMovesPastASilentEndpoint is the case that lost a round on
// mainnet: the first endpoint accepts the request and never answers, and the
// caller has no deadline. A persistent call bounds the attempt and asks the
// next endpoint.
func TestPersistentCallMovesPastASilentEndpoint(t *testing.T) {
	t.Parallel()

	b := newBackend(t,
		endpoint("first", backendmock.WithBlockNumberFunc(hang)),
		endpoint("second", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) { return 7, nil })),
		endpoint("third", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) { return 8, nil })),
	)

	n, err := within(t, 5*time.Second, func(ctx context.Context) (uint64, error) {
		return b.BlockNumber(persistent(ctx, 50*time.Millisecond))
	})
	if err != nil {
		t.Fatalf("got error %v, want the second endpoint's answer", err)
	}
	if n != 7 {
		t.Fatalf("got block %d, want 7 from the second endpoint", n)
	}
}

// TestPersistentCallGoesRoundAgain checks that a persistent call does not stop
// after the last endpoint: every endpoint fails once, then the first answers.
// The same failures end an ordinary call, as before.
func TestPersistentCallGoesRoundAgain(t *testing.T) {
	defer failover.SetWrapPause(time.Millisecond)()

	newRound := func() *failover.Backend {
		var firstCalls atomic.Int32
		return newBackend(t,
			endpoint("first", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) {
				if firstCalls.Add(1) == 1 {
					return 0, errDown
				}
				return 9, nil
			})),
			endpoint("second", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) { return 0, errDown })),
			endpoint("third", backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) { return 0, errDown })),
		)
	}

	b := newRound()
	n, err := within(t, 5*time.Second, func(ctx context.Context) (uint64, error) {
		return b.BlockNumber(persistent(ctx, time.Second))
	})
	if err != nil || n != 9 {
		t.Fatalf("persistent call: got %d, %v; want 9 from the first endpoint on the second round", n, err)
	}
	if got := failover.Wraparounds(b); got != 1 {
		t.Fatalf("wrap-around counter is %v, want 1", got)
	}
	if b.Active() != "first" {
		t.Fatalf("active endpoint is %q, want first after going round", b.Active())
	}

	ordinary := newRound()
	if _, err := ordinary.BlockNumber(context.Background()); err == nil {
		t.Fatal("ordinary call succeeded; it should make one pass and report every endpoint down")
	}
	if got := failover.Wraparounds(ordinary); got != 0 {
		t.Fatalf("ordinary call went round %v times, want 0", got)
	}
}

// TestPersistentCallStopsWhenTheContextEnds checks that going round ends with
// the caller's context, not before, and that the pause keeps it from spinning.
func TestPersistentCallStopsWhenTheContextEnds(t *testing.T) {
	defer failover.SetWrapPause(50 * time.Millisecond)()

	var calls atomic.Int32
	down := backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) {
		calls.Add(1)
		return 0, errDown
	})
	b := newBackend(t, endpoint("first", down), endpoint("second", down), endpoint("third", down))

	const lifetime = 400 * time.Millisecond
	start := time.Now()
	_, err := within(t, 5*time.Second, func(ctx context.Context) (uint64, error) {
		ctx, cancel := context.WithTimeout(ctx, lifetime)
		defer cancel()
		return b.BlockNumber(persistent(ctx, time.Second))
	})
	took := time.Since(start)
	if err == nil {
		t.Fatal("got an answer from endpoints that are all down")
	}
	if took < lifetime-50*time.Millisecond {
		t.Fatalf("returned after %v, before the context's %v ended", took, lifetime)
	}
	// One round of three calls per pause, plus the first round.
	if limit := int32(3 * (int(lifetime/(50*time.Millisecond)) + 2)); calls.Load() > limit {
		t.Fatalf("made %d calls in %v, want at most %d: the pause is not applied", calls.Load(), lifetime, limit)
	}
}

// sendRecorder records the transactions each endpoint saw and lets an
// endpoint answer or stay silent.
type sendRecorder struct {
	mu   sync.Mutex
	sent map[string][]common.Hash
	// released records endpoints whose silent send ended.
	released map[string]bool
}

func (r *sendRecorder) record(name string, tx *types.Transaction) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sent == nil {
		r.sent = map[string][]common.Hash{}
	}
	r.sent[name] = append(r.sent[name], tx.Hash())
}

func (r *sendRecorder) answering(name string, result error) failover.Endpoint {
	return endpoint(name, backendmock.WithSendTransactionFunc(func(_ context.Context, tx *types.Transaction) error {
		r.record(name, tx)
		return result
	}))
}

func (r *sendRecorder) silent(name string) failover.Endpoint {
	return endpoint(name, backendmock.WithSendTransactionFunc(func(ctx context.Context, tx *types.Transaction) error {
		r.record(name, tx)
		<-ctx.Done()
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.released == nil {
			r.released = map[string]bool{}
		}
		r.released[name] = true
		return ctx.Err()
	}))
}

func (r *sendRecorder) got(name string) []common.Hash {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]common.Hash(nil), r.sent[name]...)
}

func (r *sendRecorder) wasReleased(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.released[name]
}

// TestPersistentSendMovesPastASilentEndpoint is the commit that hung on
// mainnet: the active endpoint takes the send and never answers. A persistent
// send bounds the attempt and the next endpoint takes the same transaction.
func TestPersistentSendMovesPastASilentEndpoint(t *testing.T) {
	t.Parallel()

	var r sendRecorder
	b := newBackend(t, r.silent("first"), r.answering("second", nil), r.answering("third", nil))
	t.Cleanup(b.Close)
	tx := testTx()

	_, err := within(t, 5*time.Second, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, b.SendTransaction(persistent(ctx, 50*time.Millisecond), tx)
	})
	if err != nil {
		t.Fatalf("send: %v, want nil because the second endpoint took it", err)
	}
	if got := r.got("second"); len(got) == 0 || got[0] != tx.Hash() {
		t.Fatalf("second got %v, want %s", got, tx.Hash())
	}
}

// TestPersistentSendCopiesToEveryEndpoint checks that once the active endpoint
// accepts a persistent send, every other endpoint gets the same transaction,
// that an "already known" answer counts as known, and that the caller does not
// wait for an endpoint that stays silent.
func TestPersistentSendCopiesToEveryEndpoint(t *testing.T) {
	t.Parallel()

	var r sendRecorder
	b := newBackend(t,
		r.answering("first", nil),
		r.answering("second", answer{"already known"}),
		r.silent("third"),
		r.answering("fourth", nil),
	)
	t.Cleanup(b.Close)
	tx := testTx()

	// Close ends the copy still waiting on the silent endpoint, which also
	// releases a send that wrongly waits for its copies.
	sent := make(chan error, 1)
	go func() { sent <- b.SendTransaction(persistent(context.Background(), time.Hour), tx) }()
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("send: %v, want nil because the first endpoint took it", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send still waiting after 2s: it must not wait for the copies")
	}

	waitFor(t, 5*time.Second, func() bool {
		return len(r.got("second")) == 1 && len(r.got("third")) == 1 && len(r.got("fourth")) == 1 &&
			failover.SendCopies(b, "known") == 1 && failover.SendCopies(b, "accepted") == 1
	})
	for _, name := range []string{"first", "second", "third", "fourth"} {
		if got := r.got(name); len(got) != 1 || got[0] != tx.Hash() {
			t.Fatalf("%s got %v, want exactly %s", name, got, tx.Hash())
		}
	}
}

// TestPersistentSendCopiesStopOnClose checks that a copy waiting on a silent
// endpoint ends when the backend is closed, so no copy outlives it.
func TestPersistentSendCopiesStopOnClose(t *testing.T) {
	t.Parallel()

	var r sendRecorder
	b := newBackend(t, r.answering("first", nil), r.silent("second"))
	tx := testTx()

	if err := b.SendTransaction(persistent(context.Background(), time.Hour), tx); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(r.got("second")) == 1 })
	closed := make(chan struct{})
	go func() {
		b.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close still waiting after 5s: the copy to the silent endpoint was not ended")
	}
	if !r.wasReleased("second") {
		t.Fatal("Close returned before the copy to the silent endpoint ended")
	}
}

// TestPersistentSendAnAnswerIsNotRetried checks that an answer from the active
// endpoint is returned as it is and that no copies are sent: an answer is the
// chain refusing the transaction, not an endpoint failing.
func TestPersistentSendAnAnswerIsNotRetried(t *testing.T) {
	t.Parallel()

	var r sendRecorder
	refused := answer{"insufficient funds for gas * price + value"}
	b := newBackend(t, r.answering("first", refused), r.answering("second", nil))
	t.Cleanup(b.Close)

	err := b.SendTransaction(persistent(context.Background(), time.Second), testTx())
	if !errors.Is(err, refused) {
		t.Fatalf("send: got %v, want the first endpoint's answer", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := r.got("second"); len(got) != 0 {
		t.Fatalf("second got %v after an answer, want nothing", got)
	}
}

// TestPersistentSendGoesRoundAgain checks that a persistent send does not stop
// after the last endpoint: every endpoint fails at the transport level once,
// then the first takes the transaction on the second round. The same failures
// end an ordinary send with the first error, as before.
func TestPersistentSendGoesRoundAgain(t *testing.T) {
	defer failover.SetWrapPause(time.Millisecond)()

	newRound := func() (*failover.Backend, *sendRecorder) {
		r := &sendRecorder{}
		var firstCalls atomic.Int32
		b := newBackend(t,
			endpoint("first", backendmock.WithSendTransactionFunc(func(_ context.Context, tx *types.Transaction) error {
				r.record("first", tx)
				if firstCalls.Add(1) == 1 {
					return http500
				}
				return nil
			})),
			r.answering("second", http500),
			r.answering("third", http500),
		)
		t.Cleanup(b.Close)
		return b, r
	}

	b, r := newRound()
	tx := testTx()
	_, err := within(t, 5*time.Second, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, b.SendTransaction(persistent(ctx, time.Second), tx)
	})
	if err != nil {
		t.Fatalf("persistent send: %v, want nil because the first endpoint took it on the second round", err)
	}
	if got := r.got("first"); len(got) != 2 || got[1] != tx.Hash() {
		t.Fatalf("first got %v, want the same transaction twice", got)
	}
	if got := failover.Wraparounds(b); got != 1 {
		t.Fatalf("wrap-around counter is %v, want 1", got)
	}

	ordinary, _ := newRound()
	if err := ordinary.SendTransaction(context.Background(), tx); err == nil {
		t.Fatal("ordinary send succeeded; it should make one pass and return the first error")
	}
}
