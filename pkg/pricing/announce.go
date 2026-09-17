// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pricing

import (
	"context"
	"math/big"
	"sync"

	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// The payment threshold announcement carries an absolute value, not a delta,
// and the receiver stores whatever arrives. There is no sequence number and no
// acknowledgement, and the wire surface is frozen (rule 6), so nothing can be
// added to the message to order it. Every announcement also opens a fresh
// stream, so two announcements to one peer can be delivered in either order and
// the peer keeps the last one it processes, for good, with no later correction.
//
// That matters because three separate places announce to the same peer:
//
//   - init, from the pricing protocol's own ConnectIn and ConnectOut handlers,
//     sending the node-wide threshold when a peer connects;
//   - notifyPaymentThresholdUpgrade in pkg/accounting, when a peer has repaid
//     enough cumulative debt;
//   - the per-peer provider grant (#327).
//
// A stale lower value landing last is the damaging case: the peer credits
// itself to a number this node has moved on from, the node keeps whatever cost
// it accepted for the higher one, and nothing ever corrects it.
//
// Ordering is therefore the sender's job, and this is where it is done, rather
// than in any one caller, so that every caller gets it without knowing about
// the others.
//
// The rule is call order wins. At most one announcement is in flight per peer.
// A value arriving while one is in flight replaces any other value waiting
// behind it rather than queueing after it, because the threshold is absolute
// and only the last one called is meaningful. The sender loops until no newer
// value is waiting.
//
// What this can promise is call order: a value announced after another reaches
// the peer after it, or not at all. It cannot know which value is
// semantically newest and must not try. Making it monotonic, refusing to
// announce below a value already sent, would be wrong: after a reconnect the
// peer's accounting is reset and the lower node-wide value is the correct one
// to send.
//
// So a caller that needs its value to win has to be called last, and the
// provider grant is, structurally: init runs when the connection is
// established, while a grant needs the peer to have opened a retrieval stream
// and been served from the local store.

// announceState is the per-peer serialisation state for threshold
// announcements.
type announceState struct {
	sending bool     // an announcement to this peer is in flight
	pending *big.Int // the newest value waiting behind it, nil if none
}

// announceSerial holds the per-peer announcement state for a Service.
type announceSerial struct {
	mu    sync.Mutex
	peers map[string]*announceState
}

func newAnnounceSerial() *announceSerial {
	return &announceSerial{peers: make(map[string]*announceState)}
}

// begin registers an intent to announce paymentThreshold to peer. It returns
// the value the caller should send, or nil when another caller is already
// sending to that peer and has taken this value to send after it.
func (a *announceSerial) begin(peer swarm.Address, paymentThreshold *big.Int) *big.Int {
	a.mu.Lock()
	defer a.mu.Unlock()

	st, ok := a.peers[peer.ByteString()]
	if !ok {
		st = &announceState{}
		a.peers[peer.ByteString()] = st
	}

	if st.sending {
		// Replace rather than queue: the announcement is absolute, so an
		// older value waiting to be sent is not worth sending.
		st.pending = new(big.Int).Set(paymentThreshold)
		return nil
	}

	st.sending = true
	return new(big.Int).Set(paymentThreshold)
}

// next is called by the sending caller after each send. It returns the next
// value to send, or nil when there is none and the caller is done sending.
func (a *announceSerial) next(peer swarm.Address) *big.Int {
	a.mu.Lock()
	defer a.mu.Unlock()

	st, ok := a.peers[peer.ByteString()]
	if !ok {
		return nil
	}

	if st.pending == nil {
		st.sending = false
		// Nothing outstanding for this peer, so do not keep the entry. A node
		// sees many peers over its lifetime and this map would otherwise only
		// ever grow.
		delete(a.peers, peer.ByteString())
		return nil
	}

	v := st.pending
	st.pending = nil
	return v
}

// announceSerialised sends paymentThreshold to peer, serialised against any
// other announcement to the same peer.
//
// When no announcement to that peer is in flight, the caller sends, and keeps
// sending while newer values arrive, so the error of the first send is returned
// as before. When one is in flight, the value is left for that sender to pick
// up and this returns nil at once: the caller's value will be sent, and
// reporting an error for a send it did not make would be wrong.
func (s *Service) announceSerialised(ctx context.Context, peer swarm.Address, paymentThreshold *big.Int) error {
	v := s.announce.begin(peer, paymentThreshold)
	if v == nil {
		return nil
	}

	var firstErr error
	for v != nil {
		err := s.sendAnnouncement(ctx, peer, v)
		if firstErr == nil {
			firstErr = err
		}
		v = s.announce.next(peer)
	}
	return firstErr
}
