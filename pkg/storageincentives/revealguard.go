// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storageincentives

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

// A reveal owed behind a blocked nonce (#819). See
// docs/experiments/send-everywhere/spec.md, part 6.
const (
	// cancelAtRemainingBlocks: the node cancels a blocker in front of an
	// owed reveal only when at most this many blocks of the reveal phase
	// are left. Before that, the blocker may still be mined, or the
	// operator may cancel it.
	cancelAtRemainingBlocks = 20
	// minCancelMarginBlocks: with fewer blocks left, a cancellation would
	// most likely not save the reveal, and the node does not cancel.
	minCancelMarginBlocks = 4
)

// Reasons the node does not cancel a blocker in front of an owed reveal.
const (
	notCancellingLottery  = "lottery transaction"
	notCancellingBalance  = "native balance too low"
	notCancellingTooLate  = "fewer than 4 blocks left in the reveal phase"
	notCancellingNoReveal = ""
)

// cancelBackend is the part of the chain backend the last-resort cancel
// reads to check that the native balance covers it. A backend without it
// never lets the node cancel.
type cancelBackend interface {
	BalanceAt(ctx context.Context, address common.Address, block *big.Int) (*big.Int, error)
	SuggestedFeeAndTip(ctx context.Context, gasPrice *big.Int, boostPercent int) (*big.Int, *big.Int, error)
}

// guardOwedReveal runs after each reveal step that did not finish the
// round. When the round's listed reveal waits behind a blocked pending
// transaction of the node, it escalates at once (an error line and the
// gauge), and, as a last resort, with at most cancelAtRemainingBlocks and
// at least minCancelMarginBlocks blocks of the phase left, cancels every
// blocker when none of them is a lottery transaction and the native balance
// covers the cancellations, the transactions between and the reveal. The
// cancellations go first; the reveal's newest stored bytes are then sent
// again, unchanged.
func (r *Revealer) guardOwedReveal(ctx context.Context, round uint64) {
	if r.state.HasRevealed(round) {
		r.clearOwed()
		return
	}
	txs, _ := r.state.RevealTxs(round)
	if len(txs) == 0 {
		return
	}
	revealHash := txs[len(txs)-1]
	revealTx, err := r.txService.StoredTransaction(revealHash)
	if err != nil {
		return
	}
	block, err := r.backend.BlockNumber(ctx)
	if err != nil {
		return
	}
	claimStart := r.claimStart(round)
	if block >= claimStart {
		r.clearOwed()
		return
	}

	blockers := r.txService.BlockedBefore(revealTx.Nonce)
	if chainNonce, err := r.backend.NonceAt(ctx, r.state.ethAddress, nil); err == nil {
		live := blockers[:0:0]
		for _, b := range blockers {
			if b.Nonce >= chainNonce {
				live = append(live, b)
			}
		}
		blockers = live
	}
	if len(blockers) == 0 {
		r.clearOwed()
		return
	}
	first := blockers[0]
	r.metrics.RevealOwedBehindBlockedNonce.Set(1)
	r.logOwed(round, first, notCancellingNoReveal)

	left := claimStart - block
	if left > cancelAtRemainingBlocks {
		return
	}
	var toCancel []transaction.BlockedTransaction
	for _, b := range blockers {
		if st, err := r.txService.PendingState(b.Hash); err == nil && st.State == transaction.StateCancelledForReveal {
			continue
		}
		toCancel = append(toCancel, b)
	}
	if len(toCancel) == 0 {
		return
	}
	if left < minCancelMarginBlocks {
		r.logOwed(round, first, notCancellingTooLate)
		return
	}
	for _, b := range toCancel {
		if r.txService.IsLotteryTransaction(b.Hash) {
			r.logOwed(round, b, notCancellingLottery)
			return
		}
	}
	if !r.fundsCover(ctx, toCancel, revealTx) {
		r.logOwed(round, first, notCancellingBalance)
		return
	}
	for _, b := range toCancel {
		if _, err := r.txService.CancelForReveal(ctx, b.Hash, round); err != nil {
			r.logger.Error(err, "cancelling a blocked transaction to protect the reveal failed", "round", round, "tx", b.Hash, "nonce", b.Nonce)
			return
		}
	}
	if _, err := r.txService.ResendTransaction(ctx, revealHash); err != nil {
		r.logger.Error(err, "sending the reveal again after the cancellation failed", "round", round, "tx", revealHash)
	}
}

// fundsCover reports whether the node's native balance covers the
// cancellation of every blocker (CancelForRevealGas times its fee cap), the
// gas and value of every pending transaction with a nonce between the
// lowest blocker and the reveal, and the reveal's gas.
func (r *Revealer) fundsCover(ctx context.Context, blockers []transaction.BlockedTransaction, revealTx *transaction.StoredTransaction) bool {
	backend, ok := r.backend.(cancelBackend)
	if !ok {
		return false
	}
	balance, err := backend.BalanceAt(ctx, r.state.ethAddress, nil)
	if err != nil || balance == nil {
		return false
	}
	suggestedFeeCap, suggestedTip, err := backend.SuggestedFeeAndTip(ctx, nil, 0)
	if err != nil {
		return false
	}

	need := new(big.Int)
	cancelled := make(map[uint64]bool, len(blockers))
	for _, b := range blockers {
		stored, err := r.txService.StoredTransaction(b.Hash)
		if err != nil {
			return false
		}
		feeCap, _ := transaction.CancellationFees(suggestedFeeCap, suggestedTip, stored)
		need.Add(need, new(big.Int).Mul(feeCap, new(big.Int).SetUint64(transaction.CancelForRevealGas)))
		cancelled[b.Nonce] = true
	}

	// the most any one transaction at each nonce between can cost
	between := make(map[uint64]*big.Int)
	if hashes, err := r.txService.PendingTransactions(); err == nil {
		for _, h := range hashes {
			stored, err := r.txService.StoredTransaction(h)
			if err != nil || stored.Nonce <= blockers[0].Nonce || stored.Nonce >= revealTx.Nonce || cancelled[stored.Nonce] {
				continue
			}
			cost := txCost(stored)
			if cur, ok := between[stored.Nonce]; !ok || cost.Cmp(cur) > 0 {
				between[stored.Nonce] = cost
			}
		}
	}
	for _, cost := range between {
		need.Add(need, cost)
	}
	need.Add(need, gasCost(revealTx))
	return balance.Cmp(need) >= 0
}

// gasCost is a stored transaction's gas limit times its fee cap.
func gasCost(st *transaction.StoredTransaction) *big.Int {
	if st.GasFeeCap == nil {
		return new(big.Int)
	}
	return new(big.Int).Mul(st.GasFeeCap, new(big.Int).SetUint64(st.GasLimit))
}

// txCost is a stored transaction's gas cost plus its value.
func txCost(st *transaction.StoredTransaction) *big.Int {
	cost := gasCost(st)
	if st.Value != nil {
		cost.Add(cost, st.Value)
	}
	return cost
}

// logOwed logs an owed reveal behind a blocked nonce at error level, once
// per round and reason: why "" is the escalation line, any other value
// says why the node does not cancel and states the freeze risk.
func (r *Revealer) logOwed(round uint64, b transaction.BlockedTransaction, why string) {
	r.owedMu.Lock()
	for k := range r.owedLogged {
		if k.round < round {
			delete(r.owedLogged, k) // only the current round can still log
		}
	}
	key := owedKey{round: round, why: why}
	_, logged := r.owedLogged[key]
	r.owedLogged[key] = struct{}{}
	r.owedMu.Unlock()
	if logged {
		return
	}
	msg := fmt.Sprintf("reveal for round %d owed behind blocked nonce %d (%s)", round, b.Nonce, b.Reason)
	if why != notCancellingNoReveal {
		msg += fmt.Sprintf("; not cancelling: %s; the stake will be frozen if the reveal is not mined by block %d", why, r.claimStart(round)-1)
	}
	r.logger.Error(nil, msg, "round", round, "tx", b.Hash, "nonce", b.Nonce, "description", b.Description, "reason", b.Reason)
}

// clearOwed sets the gauge back to 0.
func (r *Revealer) clearOwed() {
	r.metrics.RevealOwedBehindBlockedNonce.Set(0)
}

type owedKey struct {
	round uint64
	why   string
}

// the node's chain backend lets the node cancel
var _ cancelBackend = transaction.Backend(nil)
