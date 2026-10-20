// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/safe"
)

// The pending-transaction watchdog. See
// docs/experiments/send-everywhere/spec.md, part 6.
const (
	// overdueBlocks: a pending transaction signed more than this many
	// blocks ago is overdue and sent again to every endpoint.
	overdueBlocks = 12
	// checkBlocks: the watchdog checks every this many blocks.
	checkBlocks = 6
	// maxFeeRaise: a replacement's fee cap is never above this many times
	// the original's.
	maxFeeRaise = 4
	// overdueLogRepeat and blockedLogRepeat space the repeated error lines
	// of one entry.
	overdueLogRepeat = 10 * time.Minute
	blockedLogRepeat = time.Hour
)

// BlockedReasonFeeRaiseLimit is the reason of an entry blocked because its
// next fee raise would pass maxFeeRaise.
const BlockedReasonFeeRaiseLimit = "fee raise limit reached"

var errFeeRaiseLimit = errors.New(BlockedReasonFeeRaiseLimit)

// Pending-transaction states, as GET /transactions shows them.
const (
	StatePending            = "pending"
	StateOverdue            = "overdue"
	StateBlocked            = "blocked"
	StateMined              = "mined"
	StateCancelledForReveal = "cancelled_for_reveal"
)

// PendingState is a stored transaction's state for the API.
type PendingState struct {
	State         string
	BlockedReason string
}

// BlockedTransaction is a pending entry the watchdog marked blocked.
type BlockedTransaction struct {
	Hash        common.Hash
	Nonce       uint64
	Reason      string
	Description string
}

// watchdogState is what the watchdog remembers between checks, for its log
// lines.
type watchdogState struct {
	mu            sync.Mutex
	overdueLogged map[common.Hash]time.Time
	blockedLogged map[common.Hash]time.Time
	resent        map[common.Hash]bool
}

func newWatchdogState() watchdogState {
	return watchdogState{
		overdueLogged: make(map[common.Hash]time.Time),
		blockedLogged: make(map[common.Hash]time.Time),
		resent:        make(map[common.Hash]bool),
	}
}

// settled forgets an entry whose nonce the chain passed, and says so when
// the watchdog had sent it again.
func (w *watchdogState) settled(t *transactionService, txHash common.Hash) {
	w.mu.Lock()
	resent := w.resent[txHash]
	delete(w.resent, txHash)
	delete(w.overdueLogged, txHash)
	delete(w.blockedLogged, txHash)
	w.mu.Unlock()
	if resent {
		t.logger.Info("a pending transaction the watchdog sent again is settled: its nonce is used", "tx", txHash)
	}
}

// due reports whether a line for txHash in logged is due, and records it.
func (w *watchdogState) due(logged map[common.Hash]time.Time, txHash common.Hash, now time.Time, repeat time.Duration) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, ok := logged[txHash]; ok && now.Sub(last) < repeat {
		return false
	}
	logged[txHash] = now
	return true
}

func (w *watchdogState) markResent(txHash common.Hash) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.resent[txHash] = true
}

// checkInterval is checkBlocks at the measured block time.
func (t *transactionService) checkInterval() time.Duration {
	return checkBlocks * t.blockTime()
}

// overdueAge is overdueBlocks at the measured block time.
func (t *transactionService) overdueAge() time.Duration {
	return overdueBlocks * t.blockTime()
}

// startWatchdog runs the watchdog until the service closes. It never holds
// t.lock while it sends.
func (t *transactionService) startWatchdog() {
	t.wg.Go(func() {
		safe.Run(t.logger, "transaction-watchdog", func() {
			for {
				interval := t.checkInterval()
				timer := time.NewTimer(interval)
				select {
				case <-t.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				case <-t.watchdogKick:
					timer.Stop()
				}
				ctx, cancel := context.WithTimeout(t.ctx, interval)
				t.checkPending(ctx)
				cancel()
			}
		})
	})
}

// checkPending is one watchdog check.
func (t *transactionService) checkPending(ctx context.Context) {
	chainNonce, err := t.backend.NonceAt(ctx, t.sender, nil)
	if err != nil {
		t.logger.Debug("watchdog: chain nonce unavailable", "error", err)
		return
	}
	entries, err := t.pendingEntries()
	if err != nil {
		t.logger.Debug("watchdog: pending transactions unavailable", "error", err)
		return
	}

	// Settled nonces: part 3's rule removes them.
	live := entries[:0:0]
	for _, e := range entries {
		if e.stored.Nonce < chainNonce {
			if err := t.deletePending(e.hash); err != nil {
				t.logger.Error(err, "error while unregistering transaction as pending", "tx", e.hash)
			}
			t.watchdog.settled(t, e.hash)
			continue
		}
		live = append(live, e)
	}

	now := t.now()
	blocked := 0
	for _, e := range live {
		if e.pending.Blocked {
			blocked++
		}
	}
	t.metrics.BlockedNonces.Set(float64(blocked))
	if len(live) > 0 {
		t.metrics.OldestPendingAge.Set(now.Sub(time.Unix(live[0].stored.Created, 0)).Seconds())
	} else {
		t.metrics.OldestPendingAge.Set(0)
	}

	var baseFee *big.Int
	baseFeeRead := false
	for _, e := range newestPerNonce(live) {
		if e.pending.Blocked {
			t.logBlocked(e, now)
			continue
		}
		age := now.Sub(time.Unix(e.stored.Created, 0))
		if age <= t.overdueAge() {
			continue
		}
		if !baseFeeRead {
			baseFeeRead = true
			if header, err := t.backend.HeaderByNumber(ctx, nil); err == nil && header != nil {
				baseFee = header.BaseFee
			}
		}
		t.resendOverdue(ctx, e, age, baseFee)
		if ctx.Err() != nil {
			return
		}
	}
}

// resendOverdue broadcasts an overdue entry's stored bytes and acts on the
// answers, in the spec's order: any accepted, nothing more; any nonce used,
// nothing; a fee answer or a fee cap below the base fee, a replacement; a
// definite answer and none accepted, blocked; anything else, try again at
// the next check.
func (t *transactionService) resendOverdue(ctx context.Context, e pendingEntry, age time.Duration, baseFee *big.Int) {
	tx, err := t.signedTransaction(e.hash, e.stored)
	if err != nil {
		t.logger.Debug("watchdog: no signed bytes to send again", "tx", e.hash, "error", err)
		return
	}
	answers := t.broadcast(ctx, tx)
	t.watchdog.markResent(e.hash)

	var accepted, nonceUsed, feeTooLow, definite bool
	var definiteReason string
	for _, a := range answers {
		kind := a.Kind()
		label := kind.String()
		if kind == AnswerHeldOther {
			label = AnswerOther.String()
		}
		t.metrics.PendingResent.WithLabelValues(label).Inc()
		switch kind {
		case AnswerAccepted:
			accepted = true
		case AnswerNonceUsed:
			nonceUsed = true
		case AnswerFeeTooLow:
			feeTooLow = true
		case AnswerDefinite:
			if !definite {
				definiteReason = a.Err.Error()
			}
			definite = true
		}
	}
	belowBaseFee := baseFee != nil && e.stored.GasFeeCap != nil && e.stored.GasFeeCap.Cmp(baseFee) < 0

	if t.watchdog.due(t.watchdog.overdueLogged, e.hash, t.now(), overdueLogRepeat) {
		t.logger.Error(nil, "pending transaction not mined; sending it to every endpoint again",
			"tx", e.hash, "nonce", e.stored.Nonce, "description", e.stored.Description,
			"age_blocks", int64(age/t.blockTime()), "answers", answerSummary(answers))
	}

	switch {
	case accepted, nonceUsed:
		return
	case feeTooLow || belowBaseFee:
		if e.stored.SelfReplacing {
			// the revealer replaces its own reveals (#793)
			return
		}
		reason := "fee_too_low"
		if !feeTooLow {
			reason = "below_base_fee"
		}
		t.raiseFee(ctx, e, reason)
	case definite:
		t.markBlocked(e, definiteReason)
	}
}

// answerSummary is a short form of every endpoint's answer for a log line.
func answerSummary(answers []SendAnswer) string {
	parts := make([]string, 0, len(answers))
	for _, a := range answers {
		s := a.Endpoint + ": " + a.Kind().String()
		if a.Err != nil && a.Kind() != AnswerAccepted {
			s += " (" + a.Err.Error() + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

// raiseFee replaces an entry with raised fees, within maxFeeRaise of its
// original's fee cap; past that, the entry is blocked.
func (t *transactionService) raiseFee(ctx context.Context, e pendingEntry, reason string) {
	_, orig, err := t.original(e.hash)
	if err != nil || orig.GasFeeCap == nil {
		return
	}
	limit := new(big.Int).Mul(orig.GasFeeCap, big.NewInt(maxFeeRaise))
	newHash, err := t.replaceWithin(ctx, e.hash, limit)
	switch {
	case errors.Is(err, errFeeRaiseLimit):
		t.markBlocked(e, BlockedReasonFeeRaiseLimit)
		return
	case err != nil && newHash == (common.Hash{}):
		t.logger.Error(err, "watchdog: replacing a pending transaction with raised fees failed", "tx", e.hash)
		return
	}
	t.metrics.Replacements.WithLabelValues(reason).Inc()
	t.logger.Warning("watchdog: replaced a pending transaction with raised fees", "tx", e.hash, "replacement", newHash, "nonce", e.stored.Nonce, "reason", reason)
}

// replaceWithin signs a replacement of txHash with the same recipient,
// value, data and gas limit at the same nonce, the fee cap and tip each the
// larger of the current suggestion and the stored value plus
// ReplacementBumpPercent; errFeeRaiseLimit when the fee cap would pass
// limit. The replacement is stored, with Replaces set, under t.lock before
// it is sent; it is sent to every endpoint outside the lock.
func (t *transactionService) replaceWithin(ctx context.Context, txHash common.Hash, limit *big.Int) (common.Hash, error) {
	t.lock.Lock()
	stored, err := t.StoredTransaction(txHash)
	if err != nil {
		t.lock.Unlock()
		return common.Hash{}, err
	}
	gasFeeCap, gasTipCap, err := t.backend.SuggestedFeeAndTip(ctx, nil, stored.GasTipBoost)
	if err != nil {
		t.lock.Unlock()
		return common.Hash{}, err
	}
	gasFeeCap = bigMax(gasFeeCap, bumped(stored.GasFeeCap))
	gasTipCap = bigMax(gasTipCap, bumped(stored.GasTipCap))
	gasFeeCap = bigMax(gasFeeCap, gasTipCap)
	if gasFeeCap.Cmp(limit) > 0 {
		t.lock.Unlock()
		return common.Hash{}, errFeeRaiseLimit
	}
	signedTx, err := t.signer.SignTx(types.NewTx(&types.DynamicFeeTx{
		Nonce:     stored.Nonce,
		ChainID:   t.chainID,
		To:        stored.To,
		Value:     stored.Value,
		Gas:       stored.GasLimit,
		GasTipCap: gasTipCap,
		GasFeeCap: gasFeeCap,
		Data:      stored.Data,
	}), t.chainID)
	if err != nil {
		t.lock.Unlock()
		return common.Hash{}, err
	}
	err = t.storeTransaction(signedTx, storeMeta{
		boost:         stored.GasTipBoost,
		description:   replacementDescription(stored.Description),
		replaces:      txHash,
		selfReplacing: stored.SelfReplacing,
	}, pendingTransaction{})
	t.lock.Unlock()
	if err != nil {
		return common.Hash{}, err
	}
	newHash := signedTx.Hash()
	t.waitForPendingTx(newHash)
	t.broadcast(ctx, signedTx)
	return newHash, nil
}

// markBlocked marks an entry blocked with the endpoint's answer.
func (t *transactionService) markBlocked(e pendingEntry, reason string) {
	err := t.updatePending(e.hash, func(p *pendingTransaction) { p.Blocked, p.BlockedReason = true, reason })
	if err != nil {
		if !isNotFound(err) {
			t.logger.Error(err, "watchdog: marking a pending transaction blocked failed", "tx", e.hash)
		}
		return
	}
	e.pending.Blocked, e.pending.BlockedReason = true, reason
	t.logBlocked(e, t.now())
}

// logBlocked logs a blocked entry once when it is blocked and then once an
// hour.
func (t *transactionService) logBlocked(e pendingEntry, now time.Time) {
	if !t.watchdog.due(t.watchdog.blockedLogged, e.hash, now, blockedLogRepeat) {
		return
	}
	t.logger.Error(nil, "pending transaction cannot be mined; free its nonce with DELETE /transactions/{hash}",
		"tx", e.hash, "nonce", e.stored.Nonce, "description", e.stored.Description, "reason", e.pending.BlockedReason)
}

// PendingState implements Service.
func (t *transactionService) PendingState(txHash common.Hash) (PendingState, error) {
	stored, err := t.StoredTransaction(txHash)
	if err != nil {
		return PendingState{}, err
	}
	p, err := t.getPending(txHash)
	if err != nil {
		if !isNotFound(err) {
			return PendingState{}, err
		}
		if stored.CancelledForReveal {
			return PendingState{State: StateCancelledForReveal}, nil
		}
		return PendingState{}, nil
	}
	switch {
	case stored.CancelledForReveal:
		return PendingState{State: StateCancelledForReveal}, nil
	case p.Mined:
		return PendingState{State: StateMined}, nil
	case p.Blocked:
		return PendingState{State: StateBlocked, BlockedReason: p.BlockedReason}, nil
	case t.now().Sub(time.Unix(stored.Created, 0)) > t.overdueAge():
		return PendingState{State: StateOverdue}, nil
	}
	return PendingState{State: StatePending}, nil
}

// BlockedNonce implements Service.
func (t *transactionService) BlockedNonce() (uint64, string, bool) {
	entries, err := t.pendingEntries()
	if err != nil {
		return 0, "", false
	}
	for _, e := range entries {
		if e.pending.Blocked {
			return e.stored.Nonce, e.pending.BlockedReason, true
		}
	}
	return 0, "", false
}

// BlockedBefore implements Service.
func (t *transactionService) BlockedBefore(nonce uint64) []BlockedTransaction {
	entries, err := t.pendingEntries()
	if err != nil {
		return nil
	}
	var out []BlockedTransaction
	for _, e := range entries {
		if e.pending.Blocked && e.stored.Nonce < nonce {
			out = append(out, BlockedTransaction{Hash: e.hash, Nonce: e.stored.Nonce, Reason: e.pending.BlockedReason, Description: e.stored.Description})
		}
	}
	return out
}
