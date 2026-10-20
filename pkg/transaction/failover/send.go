// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

// copyDialTimeout bounds the dial of an endpoint without a connection before
// a copy or a broadcast is sent through it.
var copyDialTimeout = 5 * time.Second

// SendTransaction sends tx through the active endpoint. When that send fails at
// the transport level, the same signed transaction is sent through each other
// endpoint in order until one takes it (#772).
//
// Sending the same bytes again cannot create a second transaction: they have
// the same hash and the same nonce, and a chain includes at most one
// transaction per sender and nonce. The nonce is inside the signed bytes, so
// another endpoint's view of the pending nonce is never consulted. The active
// endpoint does not change; a write failure alone is not a reason to move
// reads.
//
// Once an endpoint has taken the transaction, copies of the same bytes go to
// every other configured endpoint in the background, for every send, so one
// endpoint that keeps a transaction to itself does not decide whether it is
// mined (#819). An "already known" answer from the active endpoint counts as
// taken: the endpoint holds the bytes. Any other answer from it is returned
// as it is, with no copies.
//
// A persistent send (#783) also goes round every endpoint again, after a
// pause, until one takes the transaction or the caller's context ends. An
// ordinary send makes one pass: SendWithHook holds the service lock across
// the send, and a send that kept going round would hold up every other
// transaction of the node.
func (b *Backend) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	_, persist := transaction.PersistentCalls(ctx)
	attempt := copyAttempt(ctx)

	i, ep, live := b.current()
	actx, cancel := b.attemptContext(ctx, i)
	first := live.SendTransaction(actx, tx)
	cancel()
	if first == nil {
		b.sendCopies(tx, i, attempt)
		return nil
	}
	if !isTransportFailure(first) {
		if transaction.ClassifySendAnswer(first) == transaction.AnswerAccepted {
			b.logger.Debug("transaction already known to the active endpoint; sending copies", "endpoint", ep.name, "tx", tx.Hash())
			b.sendCopies(tx, i, attempt)
			return nil
		}
		return first
	}
	b.logger.Warning("send transaction failed at the transport level; sending the same transaction through the next endpoint",
		"endpoint", ep.name, "tx", tx.Hash(), "error", first)

	order := make([]int, 0, len(b.endpoints))
	for j := 1; j < len(b.endpoints); j++ {
		order = append(order, (i+j)%len(b.endpoints))
	}
	for dial := true; ; dial = false {
		k, done, err := b.resend(ctx, tx, order, first, dial)
		if done {
			if err == nil {
				b.sendCopies(tx, k, attempt)
			}
			return err
		}
		if !persist || ctx.Err() != nil {
			return first
		}
		b.metrics.Wraparounds.Inc()
		b.logger.Warning("send transaction failed at every endpoint; sending the same transaction through them again from the first",
			"tx", tx.Hash(), "error", first)
		if !pause(ctx) {
			return first
		}
		order = order[:0]
		for j := range b.endpoints {
			order = append(order, j)
		}
	}
}

// copyAttempt is the bound of one copy: the persistent bound when ctx
// carries one, and minAttemptTimeout otherwise.
func copyAttempt(ctx context.Context) time.Duration {
	if attempt, ok := transaction.PersistentCalls(ctx); ok {
		return attempt
	}
	return minAttemptTimeout
}

// resend sends tx through the endpoints in order. It reports done with the
// index of the endpoint that took it and a nil error, or done with the answer
// to return, or not done when every endpoint failed at the transport level or
// the caller's context ended (with the context's error).
//
// When dial is set, endpoints with no connection are tried last: each is
// dialled, and sent to when the dial works (#802). A read skips them and
// leaves them to Recover, which only probes endpoints ahead of the active
// one; for a send that every connected endpoint failed, a configured
// endpoint not tried is a transaction that may be lost, which is worth the
// cost of a dial. SendTransaction sets dial only for the first round, so a
// persistent send that goes round again every second does not dial an
// unreachable endpoint every second. A connection made here is kept, and
// the endpoint is then an ordinary connected one for reads and copies too.
// See docs/experiments/ambiguous-send-receipt-wait/spec.md.
func (b *Backend) resend(ctx context.Context, tx *types.Transaction, order []int, first error, dial bool) (int, bool, error) {
	var unconnected []int
	for _, k := range order {
		if b.endpoints[k].live.Load() == nil {
			unconnected = append(unconnected, k)
			continue
		}
		if k, done, err := b.resendTo(ctx, tx, k, first); done || err != nil {
			return k, done, err
		}
	}
	if !dial {
		return 0, false, nil
	}
	for _, k := range unconnected {
		if k, done, err := b.resendTo(ctx, tx, k, first); done || err != nil {
			return k, done, err
		}
	}
	return 0, false, nil
}

// resendTo sends tx through the endpoint at index k, dialling it first when
// it has no connection. It returns as resend does; not done with a nil error
// means the next endpoint is to be tried.
func (b *Backend) resendTo(ctx context.Context, tx *types.Transaction, k int, first error) (int, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	ep := b.endpoints[k]
	name := ep.name
	actx, cancel := b.attemptContext(ctx, k)
	defer cancel()
	var c transaction.Backend
	if live := ep.live.Load(); live != nil {
		c = live.Backend
	} else {
		if c = b.connect(actx, ep); c == nil {
			b.metrics.SendResent.WithLabelValues("failed").Inc()
			b.logger.Warning("could not connect to a blockchain rpc endpoint to send the transaction through it",
				"endpoint", name, "tx", tx.Hash())
			return 0, false, nil
		}
	}
	err := c.SendTransaction(actx, tx)
	if err == nil {
		b.metrics.SendResent.WithLabelValues("accepted").Inc()
		b.logger.Info("transaction accepted by another endpoint", "endpoint", name, "tx", tx.Hash())
		return k, true, nil
	}
	if isTransportFailure(err) {
		b.metrics.SendResent.WithLabelValues("failed").Inc()
		b.logger.Warning("send transaction failed at the transport level", "endpoint", name, "tx", tx.Hash(), "error", err)
		return 0, false, nil
	}
	var rpcErr rpc.Error
	if !errors.As(err, &rpcErr) {
		b.metrics.SendResent.WithLabelValues("answered").Inc()
		b.logger.Warning("another endpoint refused the transaction", "endpoint", name, "tx", tx.Hash(), "error", err)
		return k, true, err
	}
	switch transaction.ClassifySendAnswer(err) {
	case transaction.AnswerAccepted:
		b.metrics.SendResent.WithLabelValues("known").Inc()
		b.logger.Info("transaction already known to another endpoint", "endpoint", name, "tx", tx.Hash())
		return k, true, nil
	case transaction.AnswerNonceUsed:
		// This transaction or another with its nonce is mined; whether it
		// was this one is for the transaction service to find out, so the
		// send stays ambiguous, as the first error made it.
		b.metrics.SendResent.WithLabelValues("answered").Inc()
		return k, true, first
	default:
		b.metrics.SendResent.WithLabelValues("answered").Inc()
		b.logger.Warning("another endpoint refused the transaction", "endpoint", name, "tx", tx.Hash(), "error", err)
		return k, true, err
	}
}

// sendCopies sends tx, which the endpoint at index except took, to every other
// configured endpoint in the background, each attempt bounded by attempt. An
// endpoint without a connection is dialled first, bounded by
// copyDialTimeout; a connection made this way is kept and never changes the
// active endpoint. The caller does not wait for the copies and their results
// change nothing for it; they are counted and logged. Close ends the copies
// still under way, and once Close has begun no copy is started.
func (b *Backend) sendCopies(tx *types.Transaction, except int, attempt time.Duration) {
	b.copiesMu.Lock()
	defer b.copiesMu.Unlock()
	if b.closed.Err() != nil {
		return
	}
	for k, ep := range b.endpoints {
		if k == except {
			continue
		}
		b.copies.Go(func() {
			c := b.dialForSend(b.closed, ep)
			if c == nil {
				b.metrics.SendCopies.WithLabelValues("failed").Inc()
				b.logger.Debug("could not connect to an endpoint to send it a copy of the transaction",
					"endpoint", ep.name, "tx", tx.Hash())
				return
			}
			ctx, cancel := context.WithTimeout(b.closed, attempt)
			defer cancel()
			err := c.SendTransaction(ctx, tx)
			result := copyResult(err)
			b.metrics.SendCopies.WithLabelValues(result).Inc()
			if result == "answered" {
				b.logger.Warning("another endpoint refused a copy of a sent transaction",
					"endpoint", ep.name, "tx", tx.Hash(), "error", err)
				return
			}
			b.logger.Debug("sent a copy of the transaction to another endpoint",
				"endpoint", ep.name, "tx", tx.Hash(), "result", result, "error", err)
		})
	}
}

// dialForSend returns the endpoint's connection, dialling it first, bounded
// by copyDialTimeout, when it has none. It returns nil when the endpoint
// cannot be reached.
func (b *Backend) dialForSend(ctx context.Context, ep *endpoint) transaction.Backend {
	if c := ep.live.Load(); c != nil {
		return c.Backend
	}
	dctx, cancel := context.WithTimeout(ctx, copyDialTimeout)
	defer cancel()
	return b.connect(dctx, ep)
}

// copyResult names the outcome of a copy for the counter.
func copyResult(err error) string {
	if err == nil {
		return "accepted"
	}
	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) {
		if transaction.ClassifySendAnswer(err) == transaction.AnswerAccepted {
			return "known"
		}
		return "answered"
	}
	return "failed"
}

// Broadcast sends tx to every configured endpoint concurrently, dialling one
// without a connection first (bounded by copyDialTimeout), each send bounded
// by minAttemptTimeout and by ctx, and waits for all of them. It returns each
// endpoint's answer, in the configured order. A transport failure or a failed
// dial is returned wrapped with transaction.ErrEndpointUnreachable. It never
// changes the active endpoint. The watchdog and the resend use it (#819).
func (b *Backend) Broadcast(ctx context.Context, tx *types.Transaction) []transaction.SendAnswer {
	answers := make([]transaction.SendAnswer, len(b.endpoints))
	var wg sync.WaitGroup
	for k, ep := range b.endpoints {
		answers[k].Endpoint = ep.name
		wg.Go(func() {
			c := b.dialForSend(ctx, ep)
			if c == nil {
				answers[k].Err = fmt.Errorf("%w: could not connect", transaction.ErrEndpointUnreachable)
				return
			}
			sctx, cancel := context.WithTimeout(ctx, minAttemptTimeout)
			defer cancel()
			err := c.SendTransaction(sctx, tx)
			if err != nil && isTransportFailure(err) {
				err = fmt.Errorf("%w: %w", transaction.ErrEndpointUnreachable, err)
			}
			answers[k].Err = err
		})
	}
	wg.Wait()
	return answers
}

var _ transaction.Broadcaster = (*Backend)(nil)
