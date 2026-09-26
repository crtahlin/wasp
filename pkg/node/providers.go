// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/addressbook"
	"github.com/ethersphere/bee/v2/pkg/bzz"
	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/p2p"
	"github.com/ethersphere/bee/v2/pkg/p2p/libp2p"
	"github.com/ethersphere/bee/v2/pkg/postage"
	"github.com/ethersphere/bee/v2/pkg/providers"
	"github.com/ethersphere/bee/v2/pkg/retrieval"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/topology/kademlia"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// errProviderOverlay is returned when the node behind a provider's underlays
// is not the provider the record names.
var errProviderOverlay = errors.New("provider overlay mismatch")

// peerConnector is the part of p2p.Service a provider connect uses. It is an
// interface so the connect can be tested; the concrete libp2p service
// satisfies it.
type peerConnector interface {
	Connect(ctx context.Context, addrs []ma.Multiaddr) (*bzz.Address, error)
	Disconnect(overlay swarm.Address, reason string) error
	Peers() []p2p.Peer
}

// connectedOverlay reports whether the node currently holds a connection to
// overlay.
//
// Peers() allocates and sorts, which peer.go calls too heavy for the dial hot
// path, and p2p.Service exposes no per-overlay lookup to use instead. This is
// not that path: it runs once per provider connect, bounded by
// providers.lookupCandidates.
func connectedOverlay(p peerConnector, overlay swarm.Address) bool {
	for _, peer := range p.Peers() {
		if peer.Address.Equal(overlay) {
			return true
		}
	}
	return false
}

// providerConnect connects to one provider and reports whether the node was
// already connected to it before the connect ran.
//
// p2ps.Connect returns p2p.ErrAlreadyConnected for a peer whose bzz handshake
// has finished, on any underlay and whichever side opened the connection
// (#522; before that it matched on the remote address, see #382). The peer
// set is still read here, immediately before the connect, and the connect
// itself is left exactly as it was: every error path, the overlay guard and
// the topology notification all still run, in the same order.
//
// This over-reports dials, and by more than the gap between two statements.
// Peers() lists peers whose bzz handshake has finished, because that is when
// addIfNotExists writes the registry, while a transport connection exists
// earlier. A peer whose connection is up but whose handshake is still
// running therefore reads as absent here, and the connect then needs no dial,
// and is counted as a dial. The window is the length of that concurrent
// setup, and it is likeliest exactly when both nodes learn of each other at
// once, which is the discovery case.
//
// These counters are a diagnostic rather than an accounting record, and that
// caveat belongs with them rather than only here: see ConnectsDialed.
func providerConnect(
	ctx context.Context,
	p2ps peerConnector,
	kadConnected func(context.Context, p2p.Peer, bool) error,
	addr *bzz.Address,
) (alreadyConnected bool, err error) {
	wasConnected := connectedOverlay(p2ps, addr.Overlay)

	got, err := p2ps.Connect(ctx, addr.Underlays)
	if errors.Is(err, p2p.ErrAlreadyConnected) {
		// A success that needed no dial, reported by p2p itself. Returning
		// early here is the behaviour that shipped and is kept unchanged.
		//
		// Note it does not check the overlay, unlike the path below: the
		// address this branch returns carries whatever overlay we have
		// registered for that peer id, which need not be the one the record
		// names. Kademlia does guard that on the same error
		// (kademlia.go:1099). Preserved rather than fixed here because it is
		// pre-existing and reaches only the counter, since Discover adds the
		// record's overlay to the set before connecting either way.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !got.Overlay.Equal(addr.Overlay) {
		_ = p2ps.Disconnect(got.Overlay, "provider overlay mismatch")
		return false, errProviderOverlay
	}
	if err := kadConnected(ctx, p2p.Peer{Address: got.Overlay, FullNode: true}, false); err != nil {
		_ = p2ps.Disconnect(got.Overlay, "provider not accepted by topology")
		return false, fmt.Errorf("topology: %w", err)
	}
	return wasConnected, nil
}

// newProvidersService builds the content-providers service from the node's
// parts. See docs/experiments/content-providers/spec.md.
func newProvidersService(
	logger log.Logger,
	networkID uint64,
	overlay swarm.Address,
	nonce []byte,
	signer crypto.Signer,
	localStore *storer.DB,
	retrievalService *retrieval.Service,
	post postage.Service,
	batchStore postage.Storer,
	stamperStore storage.Store,
	p2ps *libp2p.Service,
	kad *kademlia.Kad,
	book addressbook.Interface,
	stateStore storage.StateStorer,
) (*providers.Service, error) {
	return providers.New(providers.Options{
		Logger:    logger,
		NetworkID: networkID,
		Overlay:   overlay,
		Signer:    signer,
		Getter:    localStore.Download(false),
		Fetcher:   retrievalService,
		Uploader:  func() providers.PutterSession { return localStore.DirectUpload() },
		// the same checks the API makes before stamping from a batch
		Stamper: func(batchID []byte) (postage.Stamper, func() error, error) {
			exists, err := batchStore.Exists(batchID)
			if err != nil {
				return nil, nil, fmt.Errorf("batch exists: %w", err)
			}
			issuer, save, err := post.GetStampIssuer(batchID)
			if err != nil {
				return nil, nil, fmt.Errorf("stamp issuer: %w", err)
			}
			if !exists || !post.IssuerUsable(issuer) {
				return nil, nil, postage.ErrNotUsable
			}
			return postage.NewStamper(stamperStore, issuer, signer), save, nil
		},
		// a fresh address with at most MaxUnderlays underlays, public ones
		// first, and no chequebook, which a record does not need to reveal
		Address: func() (*bzz.Address, error) {
			underlays, err := p2ps.Addresses()
			if err != nil {
				return nil, fmt.Errorf("underlays: %w", err)
			}
			public := make([]ma.Multiaddr, 0, len(underlays))
			for _, u := range underlays {
				if manet.IsPublicAddr(u) {
					public = append(public, u)
				}
			}
			if len(public) > 0 {
				underlays = public
			}
			if len(underlays) > providers.MaxUnderlays {
				underlays = underlays[:providers.MaxUnderlays]
			}
			return bzz.NewAddress(signer, underlays, overlay, networkID, nonce, time.Now().Unix(), common.Address{})
		},
		// dial a provider without forcing past a full bin, and keep the
		// connection only if the topology accepts the peer
		Connect: func(ctx context.Context, addr *bzz.Address) (bool, error) {
			return providerConnect(ctx, p2ps, kad.Connected, addr)
		},
		// the dial is checked against the overlay in Connect, so an address
		// the handshake has not verified yet is good enough to try
		Resolve: func(o swarm.Address) (*bzz.Address, error) {
			addr, _, err := book.Get(o)
			return addr, err
		},
		Store: stateStore,
	})
}
