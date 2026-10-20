// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/failover"
)

// These tests cover part 1 and part 2 of
// docs/experiments/send-everywhere/spec.md (#819).

// TestUnmarkedSendIsCopiedToEveryEndpoint is the withdrawal of the incident:
// an ordinary send, not a lottery one, that the active endpoint accepts. The
// same bytes reach every other endpoint.
func TestUnmarkedSendIsCopiedToEveryEndpoint(t *testing.T) {
	t.Parallel()

	var r sendRecorder
	b := newBackend(t, r.answering("first", nil), r.answering("second", nil), r.answering("third", nil))
	t.Cleanup(b.Close)
	tx := testTx()

	if err := b.SendTransaction(context.Background(), tx); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		return len(r.got("second")) == 1 && len(r.got("third")) == 1
	})
	for _, name := range []string{"first", "second", "third"} {
		if got := r.got(name); len(got) != 1 || got[0] != tx.Hash() {
			t.Fatalf("%s got %v, want exactly %s", name, got, tx.Hash())
		}
	}
	if got := failover.SendCopies(b, "accepted"); got != 2 {
		t.Fatalf("accepted copies: %v, want 2", got)
	}
}

// TestCopiesAfterAlreadyKnown (spec test 3): the active endpoint answers
// "already known", the expected answer when the endpoint that holds a
// transaction gets it again. The send counts as taken and every other
// endpoint gets the same bytes.
func TestCopiesAfterAlreadyKnown(t *testing.T) {
	t.Parallel()

	for _, known := range []string{"already known", "AlreadyKnown", "known transaction: 0xabc"} {
		t.Run(known, func(t *testing.T) {
			t.Parallel()

			var r sendRecorder
			b := newBackend(t, r.answering("first", answer{known}), r.answering("second", nil), r.answering("third", nil))
			t.Cleanup(b.Close)
			tx := testTx()

			if err := b.SendTransaction(context.Background(), tx); err != nil {
				t.Fatalf("send: %v, want nil because the active endpoint holds the transaction", err)
			}
			waitFor(t, 5*time.Second, func() bool {
				return len(r.got("second")) == 1 && len(r.got("third")) == 1
			})
		})
	}
}

// TestNonceTooLowFromTheActiveEndpointSendsNoCopies: an answer other than
// "already known" is returned unchanged and sends no copy.
func TestNonceTooLowFromTheActiveEndpointSendsNoCopies(t *testing.T) {
	t.Parallel()

	var r sendRecorder
	refused := answer{"nonce too low: next nonce 8, tx nonce 7"}
	b := newBackend(t, r.answering("first", refused), r.answering("second", nil))
	t.Cleanup(b.Close)

	if err := b.SendTransaction(context.Background(), testTx()); !errors.Is(err, refused) {
		t.Fatalf("send: %v, want the active endpoint's answer", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := r.got("second"); len(got) != 0 {
		t.Fatalf("second got %v, want no copy", got)
	}
}

// TestCopiesDialAnUnconnectedEndpoint (spec test 4): an endpoint with no
// connection is dialled in the background and gets the copy; the caller
// returns before the dial ends; the active endpoint does not change.
func TestCopiesDialAnUnconnectedEndpoint(t *testing.T) {
	t.Parallel()

	var (
		r     sendRecorder
		dials atomic.Int32
	)
	gate := make(chan struct{})
	b := newBackend(t,
		r.answering("first", nil),
		deadEndpoint("second", func(ctx context.Context) (transaction.Backend, error) {
			dials.Add(1)
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return r.answering("second", nil).Backend, nil
		}),
	)
	t.Cleanup(b.Close)
	tx := testTx()

	_, err := within(t, 2*time.Second, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, b.SendTransaction(ctx, tx)
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return dials.Load() == 1 })
	if got := r.got("second"); len(got) != 0 {
		t.Fatalf("second got %v before its dial ended", got)
	}
	close(gate)
	waitFor(t, 5*time.Second, func() bool { return len(r.got("second")) == 1 })
	if b.Active() != "first" {
		t.Fatalf("active endpoint is %q, want first: a copy's dial must not move it", b.Active())
	}
}

// TestCopyDialThatHangsEnds (spec test 4): a dial that hangs ends at
// copyDialTimeout, and Close does not wait for it longer than that.
func TestCopyDialThatHangsEnds(t *testing.T) {
	defer failover.SetCopyDialTimeout(50 * time.Millisecond)()

	var (
		r     sendRecorder
		ended atomic.Bool
	)
	b := newBackend(t,
		r.answering("first", nil),
		deadEndpoint("second", func(ctx context.Context) (transaction.Backend, error) {
			<-ctx.Done()
			ended.Store(true)
			return nil, ctx.Err()
		}),
	)
	t.Cleanup(b.Close)

	if err := b.SendTransaction(context.Background(), testTx()); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitFor(t, 2*time.Second, ended.Load)
	waitFor(t, 2*time.Second, func() bool { return failover.SendCopies(b, "failed") == 1 })
}

// TestUnmarkedSendDoesNotGoRoundAgain (spec test 5, guard): every endpoint
// fails at the transport level. An ordinary send makes one pass and does not
// start again at the first endpoint.
func TestUnmarkedSendDoesNotGoRoundAgain(t *testing.T) {
	defer failover.SetWrapPause(time.Millisecond)()

	var r sendRecorder
	b := newBackend(t, r.answering("first", http500), r.answering("second", http500), r.answering("third", http500))
	t.Cleanup(b.Close)

	_, err := within(t, 5*time.Second, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, b.SendTransaction(ctx, testTx())
	})
	if err == nil {
		t.Fatal("send succeeded with every endpoint down")
	}
	if got := failover.Wraparounds(b); got != 0 {
		t.Fatalf("wrap-around counter is %v, want 0 for an unmarked send", got)
	}
	for _, name := range []string{"first", "second", "third"} {
		if n := len(r.got(name)); n != 1 {
			t.Fatalf("%s asked %d times, want 1", name, n)
		}
	}
}

// TestBroadcastReturnsEveryAnswer (spec part 2): a broadcast reaches every
// configured endpoint, a connected one and an unconnected one alike, and
// returns each answer in the configured order; a transport failure and a
// failed dial are unreachable; the active endpoint does not change.
func TestBroadcastReturnsEveryAnswer(t *testing.T) {
	t.Parallel()

	var (
		r     sendRecorder
		dials atomic.Int32
	)
	b := newBackend(t,
		r.answering("accepts", nil),
		r.answering("knows", answer{"already known"}),
		r.answering("refuses", answer{"insufficient funds for gas * price + value"}),
		r.answering("down", http500),
		dialled("unconnected", r.answering("unconnected", nil).Backend, &dials),
		dialled("undialable", nil, &dials),
	)
	t.Cleanup(b.Close)
	tx := testTx()

	answers, _ := within(t, 5*time.Second, func(ctx context.Context) ([]transaction.SendAnswer, error) {
		return b.Broadcast(ctx, tx), nil
	})
	want := []struct {
		name string
		kind transaction.AnswerKind
	}{
		{"accepts", transaction.AnswerAccepted},
		{"knows", transaction.AnswerAccepted},
		{"refuses", transaction.AnswerDefinite},
		{"down", transaction.AnswerUnreachable},
		{"unconnected", transaction.AnswerAccepted},
		{"undialable", transaction.AnswerUnreachable},
	}
	if len(answers) != len(want) {
		t.Fatalf("got %d answers, want %d", len(answers), len(want))
	}
	for i, w := range want {
		if answers[i].Endpoint != w.name || answers[i].Kind() != w.kind {
			t.Fatalf("answer %d: %s %v (%v), want %s %v", i, answers[i].Endpoint, answers[i].Kind(), answers[i].Err, w.name, w.kind)
		}
	}
	if !errors.Is(answers[3].Err, transaction.ErrEndpointUnreachable) || !errors.Is(answers[5].Err, transaction.ErrEndpointUnreachable) {
		t.Fatalf("unreachable answers do not wrap ErrEndpointUnreachable: %v, %v", answers[3].Err, answers[5].Err)
	}
	if got := r.got("unconnected"); len(got) != 1 || got[0] != tx.Hash() {
		t.Fatalf("unconnected endpoint got %v, want the transaction", got)
	}
	if b.Active() != "accepts" {
		t.Fatalf("active endpoint is %q, want accepts", b.Active())
	}
}
