// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover

import (
	"context"
	"errors"
	"strings"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
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
func (b *Backend) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	i, ep, live := b.current()
	actx, cancel := b.attemptContext(ctx, i)
	first := live.SendTransaction(actx, tx)
	cancel()
	if first == nil || !isTransportFailure(first) {
		return first
	}
	b.logger.Warning("send transaction failed at the transport level; sending the same transaction through the next endpoint",
		"endpoint", ep.name, "tx", tx.Hash(), "error", first)

	for j := 1; j < len(b.endpoints); j++ {
		if ctx.Err() != nil {
			return first
		}
		k := (i + j) % len(b.endpoints)
		c := b.endpoints[k].live.Load()
		if c == nil {
			continue
		}
		name := b.endpoints[k].name
		actx, cancel := b.attemptContext(ctx, k)
		err := c.SendTransaction(actx, tx)
		cancel()
		if err == nil {
			b.metrics.SendResent.WithLabelValues("accepted").Inc()
			b.logger.Info("transaction accepted by another endpoint", "endpoint", name, "tx", tx.Hash())
			return nil
		}
		if isTransportFailure(err) {
			b.metrics.SendResent.WithLabelValues("failed").Inc()
			b.logger.Warning("send transaction failed at the transport level", "endpoint", name, "tx", tx.Hash(), "error", err)
			continue
		}
		var rpcErr rpc.Error
		if !errors.As(err, &rpcErr) {
			return err
		}
		switch classifySendAnswer(err) {
		case answerKnown:
			b.metrics.SendResent.WithLabelValues("known").Inc()
			b.logger.Info("transaction already known to another endpoint", "endpoint", name, "tx", tx.Hash())
			return nil
		case answerNonceTaken:
			// This transaction or another with its nonce is mined; whether it
			// was this one is for the transaction service to find out, so the
			// send stays ambiguous, as the first error made it.
			return first
		default:
			return err
		}
	}
	return first
}
