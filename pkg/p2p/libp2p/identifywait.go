// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package libp2p

import (
	"context"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	ma "github.com/multiformats/go-multiaddr"
)

// identifyWaiter is the part of libp2p's identify service that reports when
// identify has finished for a connection.
type identifyWaiter interface {
	IdentifyWait(network.Conn) <-chan struct{}
}

// identifyWaiterOf returns the identify service of a libp2p host, or nil when
// the host does not expose one.
func identifyWaiterOf(h any) identifyWaiter {
	withIDs, ok := h.(interface{ IDService() identify.IDService })
	if !ok {
		return nil
	}
	ids := withIDs.IDService()
	if ids == nil {
		return nil
	}
	return ids
}

// waitIdentified returns the remote peer's addresses once identify has
// finished for conn, or when ctx is done, whichever comes first.
//
// It replaces waiting for an address to appear. identify keeps only public
// addresses from a peer that connected over a public address, so a peer
// behind NAT that advertises none leaves its peerstore entry empty for good,
// and waiting for an address meant waiting for the whole timeout before the
// caller's fallback to the connection's remote address. Once identify has
// finished, no address is coming, and an empty result is final. See #511.
func waitIdentified(ctx context.Context, ids identifyWaiter, ps peerstore.Peerstore, conn network.Conn) []ma.Multiaddr {
	peerID := conn.RemotePeer()
	if addrs := ps.Addrs(peerID); len(addrs) > 0 {
		return addrs
	}
	select {
	case <-ids.IdentifyWait(conn):
	case <-ctx.Done():
	}
	return ps.Addrs(peerID)
}
