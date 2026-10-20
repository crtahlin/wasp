// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/safe"
)

// defaultAmbiguousRebroadcastInterval is how often an ambiguous transaction is
// sent again (#772).
const defaultAmbiguousRebroadcastInterval = 30 * time.Second

// ambiguousRebroadcastOverride replaces the interval when non-zero, in
// nanoseconds. Atomic so tests can shorten it while other tests run.
var ambiguousRebroadcastOverride atomic.Int64

func ambiguousRebroadcastInterval() time.Duration {
	if d := ambiguousRebroadcastOverride.Load(); d > 0 {
		return time.Duration(d)
	}
	return defaultAmbiguousRebroadcastInterval
}

// rebroadcastAmbiguous sends an ambiguous transaction's stored bytes again on
// each interval, so that a send that failed on every endpoint still reaches
// the network, and the nonce it holds does not block the transactions after
// it (#772). The bytes are unchanged, so at most one copy can be mined.
//
// It stops when the transaction is no longer ambiguous, two intervals before
// its ambiguity times out (after that its nonce may be given to a new
// transaction, which a late copy of these bytes could then displace), when
// the backend knows it, when a newer stored pending transaction has its nonce
// (a replacement or a cancel, which re-sending the original could defeat),
// when its nonce is used on chain, or when the service closes. It never takes
// t.lock, which SendWithHook holds across a network send.
func (t *transactionService) rebroadcastAmbiguous(txHash common.Hash) {
	t.wg.Go(func() {
		safe.Run(t.logger, "transaction-rebroadcast-ambiguous", func() {
			interval := ambiguousRebroadcastInterval()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-t.ctx.Done():
					return
				case <-ticker.C:
				}
				if !t.keepRebroadcasting(txHash, interval) {
					return
				}
				// keepRebroadcasting can take a while; check the age again
				// right before the bytes go out.
				if !t.inRebroadcastWindow(txHash, interval) {
					return
				}
				ctx, cancel := context.WithTimeout(t.ctx, interval)
				err := t.RebroadcastTransaction(ctx, txHash)
				cancel()
				if err != nil {
					t.logger.Debug("re-broadcast of an ambiguous transaction failed", "tx", txHash, "error", err)
					continue
				}
				t.logger.Info("ambiguous transaction re-broadcast", "tx", txHash)
			}
		})
	})
}

// inRebroadcastWindow reports whether txHash is still ambiguous and at least
// two intervals away from AmbiguousPendingTimeout.
func (t *transactionService) inRebroadcastWindow(txHash common.Hash, interval time.Duration) bool {
	if t.recentAmbiguousTransaction(txHash) == nil {
		return false
	}
	stored, err := t.StoredTransaction(txHash)
	if err != nil {
		return false
	}
	return rebroadcastWindowOpen(time.Unix(stored.Created, 0), interval, time.Now())
}

// rebroadcastWindowOpen reports whether a transaction created at created may
// still be sent again at now: until two intervals before
// AmbiguousPendingTimeout, so that no copy goes out after its nonce may have
// been given to another transaction.
func rebroadcastWindowOpen(created time.Time, interval time.Duration, now time.Time) bool {
	return now.Sub(created) < AmbiguousPendingTimeout-2*interval
}

// newerThan reports whether a is a later transaction than b for the same
// nonce: created later, or in the same second with a higher fee cap, as a
// replacement has.
func newerThan(a, b *StoredTransaction) bool {
	if a.Created != b.Created {
		return a.Created > b.Created
	}
	return a.GasFeeCap != nil && b.GasFeeCap != nil && a.GasFeeCap.Cmp(b.GasFeeCap) > 0
}

func (t *transactionService) keepRebroadcasting(txHash common.Hash, timeout time.Duration) bool {
	if !t.inRebroadcastWindow(txHash, timeout) {
		return false
	}
	stored, err := t.StoredTransaction(txHash)
	if err != nil {
		return false
	}
	pending, err := t.PendingTransactions()
	if err != nil {
		return true
	}
	for _, h := range pending {
		if h == txHash {
			continue
		}
		if other, err := t.StoredTransaction(h); err == nil && other.Nonce == stored.Nonce && newerThan(other, stored) {
			return false
		}
	}
	ctx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()
	state, err := t.TransactionStatus(ctx, txHash)
	if err != nil {
		// The backend could not be asked; that is when a re-broadcast is
		// most likely needed.
		return true
	}
	return state == TxNotFound
}
