// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package accounting

import (
	"fmt"
	"math/big"
	"time"
)

// AccrualMode selects how the refresh allowance that raises a peer's overdraft
// limit is granted inside the first second after a refreshment.
type AccrualMode int

const (
	// AccrualStep grants nothing until a full second has elapsed and then a
	// whole refreshRate. This is upstream's behaviour and the default.
	AccrualStep AccrualMode = iota
	// AccrualContinuous grants the allowance in proportion to the milliseconds
	// elapsed, capped at what a peer running stock Bee tolerates.
	AccrualContinuous
)

// ParseAccrualMode turns the configured string into a mode. An unknown value
// is an error rather than a silent fall back to the default, because a
// misspelled setting that quietly does nothing is the failure this refuses.
func ParseAccrualMode(s string) (AccrualMode, error) {
	switch s {
	case "", "step":
		return AccrualStep, nil
	case "continuous":
		return AccrualContinuous, nil
	default:
		return AccrualStep, fmt.Errorf("invalid refresh allowance accrual %q, want step or continuous", s)
	}
}

// SetAccrualMode selects how the refresh allowance is granted. See #359.
//
// A fork-authored setter rather than another argument to NewAccounting, for
// the reason given on SetProviderCredit: upstream's constructor signature
// stays untouched and the next sync sees an added file and an added method.
func (a *Accounting) SetAccrualMode(m AccrualMode) {
	a.accrual = m
}

// refreshDue returns the allowance that raises this peer's overdraft limit
// above the threshold it announced, for the time since its last refreshment.
//
// Upstream computes this as min(elapsed/1000, 1) * refreshRate, integer
// division, so the allowance is zero for 999 ms and then a whole refreshRate.
// It is documented as a rate, "accounting units refreshed per second"
// (pkg/node/node.go, the refreshRate constant), and #353 measured that 94.5 per cent of refusals on
// the bench fall in the window where it reads zero. See #359.
//
// The step behaviour is the default and is unchanged. The continuous mode
// accrues inside the first second instead, and is capped: see safeAccrualCap.
func (a *Accounting) refreshDue(accountingPeer *accountingPeer, now time.Time) *big.Int {
	elapsedMillis := elapsedSinceRefresh(accountingPeer, now)

	if a.accrual != AccrualContinuous || elapsedMillis >= 1000 {
		// The step mode, and the continuous mode at or after one second: a
		// whole allowance, uncapped, exactly as today.
		//
		// The cap must not apply at or after one second. It would otherwise
		// hold the limit below what the peer already tolerates forever rather
		// than letting it reach threshold plus a full refreshRate, which is
		// strictly worse than doing nothing. A failed refreshment leaves
		// refreshTimestampMilliseconds at zero, which lands here, and
		// gate-terms-measured.md records real peers sitting in exactly that
		// state.
		if elapsedMillis < 1000 {
			return new(big.Int)
		}
		return new(big.Int).Set(a.refreshRate)
	}

	due := new(big.Int).Mul(a.refreshRate, big.NewInt(elapsedMillis))
	due.Div(due, big.NewInt(1000))

	maxAccrual := a.safeAccrualCap(accountingPeer)
	if maxAccrual.Sign() <= 0 {
		// payment-tolerance-percent: 0 is legal, and pkg/node rejects only
		// negative values. With it there is no headroom to accrue into, and
		// accruing anyway would put the limit above what the peer tolerates.
		return new(big.Int)
	}
	if due.Cmp(maxAccrual) > 0 {
		return maxAccrual
	}
	return due
}

// elapsedSinceRefresh is the time since this peer's last refreshment, in
// milliseconds, never negative.
//
// The clamp is new behavior rather than a restatement, and how far the clock
// must step back before it matters differs by mode.
//
// In the step mode, and in upstream, integer division truncates toward zero,
// so a backwards step of 1 to 999 ms already gives zero; only a step of a
// second or more makes the term negative and puts the overdraft limit BELOW
// the threshold the peer announced. In the continuous mode a single
// millisecond is enough, because that path multiplies by the elapsed
// milliseconds before dividing.
//
// So a test for the step mode must use a step of at least 1,000 ms or it pins
// nothing, while the continuous mode is exposed to the ordinary case of a
// small clock correction. An earlier version of this comment claimed the
// 1,000 ms floor for both, which understated the clamp.
func elapsedSinceRefresh(accountingPeer *accountingPeer, now time.Time) int64 {
	elapsed := now.UnixMilli() - accountingPeer.refreshTimestampMilliseconds
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// safeAccrualCap is the most this node may add to a peer's announced threshold
// inside the first second without risking the peer disconnecting it.
//
// Inside that window this node must assume the peer's own step has not fired,
// so the only headroom it can rely on is the tolerance the peer allows above
// the threshold it announced. The peer announces a threshold and nothing else,
// its tolerance is not on the wire, so this node's own tolerance stands in for
// it. **That substitution is the weakest point in the design** and is what the
// measurement checks rather than assumes.
//
// Minus one unit, because the peer's own check disconnects at equality
// (nextBalance >= disconnectLimit) while this one admits it. At the shipped
// defaults that is 25 per cent of 13,500,000 less one, so 3,374,999, and a
// capped limit of 16,874,999 against the 16,875,000 at which a stock peer
// disconnects. Stating the cap as 16,875,000 asserts the exact value the minus
// one exists to avoid.
func (a *Accounting) safeAccrualCap(accountingPeer *accountingPeer) *big.Int {
	maxAccrual := percentOf(a.paymentTolerance, accountingPeer.paymentThreshold)
	return maxAccrual.Sub(maxAccrual, big.NewInt(1))
}
