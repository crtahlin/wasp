// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pricing

import (
	"context"
	"math/big"
	"sync"
	"time"

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
// The caller sends its OWN value and nothing else, and gets back the error of
// its own send. Two reasons, both learned the hard way.
//
// An error from someone else's send must not reach this caller. init returns
// this error from ConnectIn, and libp2p disconnects the peer on any non-nil
// return from a connect handler (pkg/p2p/libp2p/libp2p.go:693-696). A caller
// that looped would hand init a failure that had nothing to do with the connect
// announcement, and the peer would be dropped for it.
//
// And a caller must not loop while holding a lock. notifyPaymentThresholdUpgrade
// in pkg/accounting announces while holding the per-peer lock, and PrepareDebit
// blocks on that same lock, so every extra send is another five seconds of
// stalled chunk deliveries to that peer. Looping would have made the very
// regression this work exists to remove worse rather than better.
//
// So anything queued behind this send is handed to a fresh goroutine, which
// carries no caller's lock and no caller's context.
func (s *Service) announceSerialised(ctx context.Context, peer swarm.Address, paymentThreshold *big.Int) error {
	v := s.announce.begin(peer, paymentThreshold)
	if v == nil {
		// Coalesced into a send already in flight. That sender will deliver
		// this value, so there is nothing to report here: an error from a send
		// this caller did not make is not this caller's to return.
		return nil
	}

	handedOff := false
	defer func() {
		if !handedOff {
			// Release the send slot even if sendAnnouncement panics, so one
			// bad send cannot wedge every later announcement to this peer.
			if next := s.announce.next(peer); next != nil {
				s.drain(peer, next)
			}
		}
	}()

	err := s.sendAnnouncement(ctx, peer, v)

	if next := s.announce.next(peer); next != nil {
		handedOff = true
		go s.drain(peer, next)
	}
	return err
}

// drain sends the values queued behind an announcement, until none is left.
// It runs without any caller's lock and on its own context, because the caller
// that queued the work may be holding the per-peer accounting lock.
func (s *Service) drain(peer swarm.Address, v *big.Int) {
	for v != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := s.sendAnnouncement(ctx, peer, v); err != nil {
			s.logger.Debug("announcing queued payment threshold", "error", err, "peer_address", peer, "value", v)
		}
		cancel()
		v = s.announce.next(peer)
	}
}
