// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package accounting

import (
	"context"
	"math/big"

	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// Granting a larger payment threshold to one peer at a time, for #327.
//
// A provider serves a few per cent of a download and then runs out of credit
// with the requester. The quantity that gates this is the threshold the
// PROVIDER announces, which the requester stores as paymentThreshold and tests
// in PrepareCredit. So the lever is entirely on the provider's side, and
// AnnouncePaymentThreshold is the mechanism rather than a step in it.
//
// The grant is made once per connection, on the first local-only hit from a
// full-node peer, and is never lowered. It ends when the connection ends,
// because Connect already resets the peer ledger.
//
// Why there is no decay, which two earlier designs had: every hazard it
// created came from lowering. An announcement carries an absolute value with no
// ordering, so a decay could overtake a raise; an unacknowledged failure cannot
// be told from a delivered one, so rolling back was ambiguous; and the release
// gate could never fire, because it waited on a ghost balance that only ever
// increases and is reset only by Connect. Removing the lowering removes all
// four. It costs little, because the exposure is per connection anyway and the
// sustained rate is set by how fast debt clears, not by the window.

// GrantProviderCredit raises the payment threshold this node announces to a
// peer that has asked it as a provider, once per connection.
//
// It is called from the retrieval handler on the path where a local-only
// request was answered from this node's own store, so the peer is downloading
// something this node holds. Calling it again for the same connection does
// nothing, which is what makes one slot per peer follow from the rule rather
// than needing to be enforced separately.
//
// It does no I/O while holding a lock. The announcement is made after every
// lock is released, because announcing under the per-peer lock stalls every
// concurrent PrepareDebit for that peer for as long as the stream takes, up to
// five seconds, and under a lookahead prefetch that is many requests at once.
func (a *Accounting) GrantProviderCredit(peer swarm.Address, fullNode bool) {
	if a.providerThreshold == nil || a.providerThreshold.Sign() == 0 {
		return // the operator has not turned this on
	}

	// Light peers are excluded. Upstream deliberately gives them a tenth of the
	// threshold and they clear debt at a tenth of the rate, so the same grant
	// would be ten to forty times the exposure intended for them and would hold
	// its slot ten times as long. fullNode comes from the request rather than
	// from the accounting record, because Connect sets that field from a
	// goroutine and may not have run yet.
	if !fullNode {
		return
	}

	accountingPeer := a.getAccountingPeer(peer)

	delta, ok := a.providerGrantDelta(accountingPeer)
	if !ok {
		return
	}

	if !a.reserveProviderBudget(delta) {
		a.metrics.ProviderGrantsRefused.Inc()
		return
	}

	announce, ok := a.applyProviderGrant(accountingPeer, delta)
	if !ok {
		// Another call granted while the budget was being reserved. Give the
		// reservation back rather than counting it twice.
		a.releaseProviderBudget(delta)
		return
	}

	a.metrics.ProviderGrants.Inc()

	// Outside every lock. A failed announcement changes nothing: the error
	// cannot tell "the peer did not get it" from "the peer got it and the
	// transport failed afterwards", and the same ambiguity is already resolved
	// conservatively for the two fields above.
	if err := a.pricing.AnnouncePaymentThreshold(context.Background(), peer, announce); err != nil {
		a.logger.Debug("announcing provider payment threshold", "error", err, "peer_address", peer, "value", announce)
	}
}

// providerGrantDelta reports how much this peer's threshold would have to rise
// to reach the configured provider value, and whether a grant should be made at
// all. It holds the per-peer lock and nothing else.
func (a *Accounting) providerGrantDelta(accountingPeer *accountingPeer) (*big.Int, bool) {
	accountingPeer.lock.Lock()
	defer accountingPeer.lock.Unlock()

	if !accountingPeer.connected {
		return nil, false // the record is not ready; Connect runs in a goroutine
	}
	if accountingPeer.providerGrant != nil {
		return nil, false // already granted on this connection
	}

	// Take the larger of the configured value and whatever the peer already
	// has, so a peer the growth path has already carried above the provider
	// value is left alone.
	delta := new(big.Int).Sub(a.providerThreshold, accountingPeer.paymentThresholdForPeer)
	if delta.Sign() <= 0 {
		return nil, false
	}
	return delta, true
}

// applyProviderGrant raises the peer's threshold and the disconnect limit
// derived from it, records the delta, and returns the value to announce. It
// holds the per-peer lock and does no I/O.
func (a *Accounting) applyProviderGrant(accountingPeer *accountingPeer, delta *big.Int) (*big.Int, bool) {
	accountingPeer.lock.Lock()
	defer accountingPeer.lock.Unlock()

	if !accountingPeer.connected || accountingPeer.providerGrant != nil {
		return nil, false
	}

	// Mutate in place. The growth path reassigns this pointer while Connect and
	// NotifyPaymentThreshold set it in place, so a value held across a lock
	// would alias one writer and detach from the other.
	raised := new(big.Int).Add(accountingPeer.paymentThresholdForPeer, delta)
	accountingPeer.paymentThresholdForPeer.Set(raised)

	// The disconnect limit is always recomputed from the threshold, never
	// remembered, so that a growth firing in between is not discarded.
	accountingPeer.disconnectLimit.Set(percentOf(100+a.paymentTolerance, accountingPeer.paymentThresholdForPeer))

	accountingPeer.providerGrant = new(big.Int).Set(delta)

	return new(big.Int).Set(accountingPeer.paymentThresholdForPeer), true
}

// reserveProviderBudget takes delta from the budget if it fits. It holds only
// the budget lock, and never a per-peer lock, so the two orders can never meet.
func (a *Accounting) reserveProviderBudget(delta *big.Int) bool {
	a.providerBudgetMu.Lock()
	defer a.providerBudgetMu.Unlock()

	next := new(big.Int).Add(a.providerBudgetUsed, delta)
	if next.Cmp(a.providerBudget) > 0 {
		return false
	}
	a.providerBudgetUsed.Set(next)
	a.metrics.ProviderBudgetUsed.Set(bigToFloat(a.providerBudgetUsed))
	return true
}

// releaseProviderBudget gives delta back. Callers must not hold a per-peer
// lock.
func (a *Accounting) releaseProviderBudget(delta *big.Int) {
	a.providerBudgetMu.Lock()
	defer a.providerBudgetMu.Unlock()

	a.providerBudgetUsed.Sub(a.providerBudgetUsed, delta)
	if a.providerBudgetUsed.Sign() < 0 {
		a.providerBudgetUsed.SetInt64(0)
	}
	a.metrics.ProviderBudgetUsed.Set(bigToFloat(a.providerBudgetUsed))
}

// clearProviderGrant drops a peer's grant and returns what it held, so the
// caller can give the budget back without holding the per-peer lock. It must be
// called with the per-peer lock held.
func clearProviderGrant(accountingPeer *accountingPeer) *big.Int {
	grant := accountingPeer.providerGrant
	accountingPeer.providerGrant = nil
	return grant
}

func bigToFloat(v *big.Int) float64 {
	f, _ := new(big.Float).SetInt(v).Float64()
	return f
}

// SetProviderCredit turns the per-peer provider grant on, with the threshold to
// announce and the total extra credit that may be granted across connections
// holding a grant at one time.
//
// A fork-authored setter rather than two more arguments to NewAccounting, so
// that upstream's constructor signature is untouched and the next sync sees an
// added file and an added method. Both zero leaves the behaviour off, which is
// the default.
func (a *Accounting) SetProviderCredit(threshold, budget *big.Int) {
	if threshold == nil || budget == nil {
		return
	}
	a.providerBudgetMu.Lock()
	defer a.providerBudgetMu.Unlock()
	a.providerThreshold = new(big.Int).Set(threshold)
	a.providerBudget = new(big.Int).Set(budget)
}
