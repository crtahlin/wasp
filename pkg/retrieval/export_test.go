// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval

import (
	"context"
	"time"

	"github.com/ethersphere/bee/v2/pkg/p2p"
	"github.com/ethersphere/bee/v2/pkg/ratelimit"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

func (s *Service) Handler(ctx context.Context, p p2p.Peer, stream p2p.Stream) error {
	return s.handler(ctx, p, stream)
}

func (s *Service) ClosestPeer(addr swarm.Address, skipPeers []swarm.Address, allowUpstream bool) (swarm.Address, error) {
	return s.closestPeer(addr, skipPeers, allowUpstream)
}

var (
	ErrNotHeldLocally = errNotHeldLocally
	ErrLocalOnlyLimit = errLocalOnlyLimit
	DemoteAfterMisses = demoteAfterMisses
	Fingerprint       = fingerprint
)

// ErrSkipped returns the peers on the service-wide error skip list for chunk.
func (s *Service) ErrSkipped(chunk swarm.Address) []swarm.Address {
	return s.errSkip.ChunkPeers(chunk)
}

// SetMissLimiters replaces the local-only miss limiters.
func (s *Service) SetMissLimiters(peer, node *ratelimit.Limiter) {
	s.peerMissLimiter = peer
	s.nodeMissLimiter = node
}

func (p *PreferredSet) Hit(peer swarm.Address)    { p.hit(peer) }
func (p *PreferredSet) Miss(peer swarm.Address)   { p.miss(peer) }
func (p *PreferredSet) Demote(peer swarm.Address) { p.demote(peer) }

func (p *PreferredSet) SetNow(f func() time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = f
}
