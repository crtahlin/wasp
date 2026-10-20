// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

// sendAnswer is what an endpoint's answer to a re-sent transaction means.
type sendAnswer int

const (
	answerOther      sendAnswer = iota + 1 // an answer about the transaction; return it
	answerKnown                            // the transaction is already in that endpoint's pool
	answerNonceTaken                       // a transaction with this nonce is already mined
)

// classifySendAnswer reads a JSON-RPC error answer to eth_sendRawTransaction.
// The go-ethereum and Erigon messages are matched as text, the Nethermind
// result names as written, both without regard to case. See
// docs/experiments/send-failover-identical-bytes/spec.md.
func classifySendAnswer(err error) sendAnswer {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "already known"),
		strings.Contains(msg, "known transaction"),
		strings.Contains(msg, "alreadyknown"):
		return answerKnown
	case strings.Contains(msg, "nonce too low"),
		strings.Contains(msg, "oldnonce"):
		return answerNonceTaken
	}
	return answerOther
}

// SendTransaction sends tx through the active endpoint. When that send fails at
// the transport level, the same signed transaction is sent through each other
// connected endpoint in order until one takes it (#772).
//
// Sending the same bytes again cannot create a second transaction: they have
// the same hash and the same nonce, and a chain includes at most one
// transaction per sender and nonce. The nonce is inside the signed bytes, so
// another endpoint's view of the pending nonce is never consulted. The active
// endpoint does not change; a write failure alone is not a reason to move
// reads.
//
// A persistent send (#783) also goes round every endpoint again, after a
// pause, until one takes the transaction or the caller's context ends; and
// once one has taken it, copies of the same bytes go to every other connected
// endpoint in the background, so one endpoint that keeps a transaction to
// itself does not decide whether it is mined.
func (b *Backend) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	attempt, persist := transaction.PersistentCalls(ctx)

	i, ep, live := b.current()
	actx, cancel := b.attemptContext(ctx, i)
	first := live.SendTransaction(actx, tx)
	cancel()
	if first == nil {
		if persist {
			b.sendCopies(tx, i, attempt)
		}
		return nil
	}
	if !isTransportFailure(first) {
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
			if err == nil && persist {
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
	switch classifySendAnswer(err) {
	case answerKnown:
		b.metrics.SendResent.WithLabelValues("known").Inc()
		b.logger.Info("transaction already known to another endpoint", "endpoint", name, "tx", tx.Hash())
		return k, true, nil
	case answerNonceTaken:
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
// connected endpoint in the background, each attempt bounded by attempt. The
// caller does not wait for them and their results change nothing for it; they
// are counted and logged. Close ends the copies still under way, and once
// Close has begun no copy is started.
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
		c := ep.live.Load()
		if c == nil {
			continue
		}
		name := ep.name
		b.copies.Go(func() {
			ctx, cancel := context.WithTimeout(b.closed, attempt)
			defer cancel()
			err := c.SendTransaction(ctx, tx)
			result := copyResult(err)
			b.metrics.SendCopies.WithLabelValues(result).Inc()
			if result == "answered" {
				b.logger.Warning("another endpoint refused a copy of a sent transaction",
					"endpoint", name, "tx", tx.Hash(), "error", err)
				return
			}
			b.logger.Debug("sent a copy of the transaction to another endpoint",
				"endpoint", name, "tx", tx.Hash(), "result", result, "error", err)
		})
	}
}

// copyResult names the outcome of a copy for the counter.
func copyResult(err error) string {
	if err == nil {
		return "accepted"
	}
	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) {
		if classifySendAnswer(err) == answerKnown {
			return "known"
		}
		return "answered"
	}
	return "failed"
}
