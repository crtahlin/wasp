// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval

import (
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// providerCreditor grants a peer a larger payment threshold because it is
// asking this node as a provider (#327).
//
// Declared here rather than added to accounting.Interface so that the next
// upstream sync sees an added file and an added method on the concrete type,
// not a changed interface that every implementation has to follow.
type providerCreditor interface {
	// GrantProviderCredit raises the threshold announced to peer, once per
	// connection. It is safe to call on every request: a second call for the
	// same connection does nothing.
	GrantProviderCredit(peer swarm.Address, fullNode bool)
}

// SetProviderCreditor wires in the accounting side of the per-peer provider
// threshold. Without it the handler grants nothing, which is the default.
func (s *Service) SetProviderCreditor(c providerCreditor) {
	s.providerCredit = c
}

// grantProviderCredit is called from the retrieval handler on the path where a
// local-only request was answered from this node's own store, so the peer is
// downloading content this node holds.
//
// The handler reads the local-only header only inside the not-found branch
// today, because until now nothing on the hit path needed to know. The hit path
// is exactly where the provider serves and credit is consumed, so that is where
// this belongs.
func (s *Service) grantProviderCredit(p peerInfo, localOnly bool) {
	if !localOnly || s.providerCredit == nil || !s.providers.Load() {
		return
	}
	s.providerCredit.GrantProviderCredit(p.address, p.fullNode)
}

// peerInfo is the little the grant needs from the request. fullNode is taken
// from the request rather than from the accounting record, because the record's
// copy is set by Connect, which runs in a goroutine and may not have run yet.
type peerInfo struct {
	address  swarm.Address
	fullNode bool
}
