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
		Connect: func(ctx context.Context, addr *bzz.Address) error {
			got, err := p2ps.Connect(ctx, addr.Underlays)
			if errors.Is(err, p2p.ErrAlreadyConnected) {
				return nil
			}
			if err != nil {
				return err
			}
			if !got.Overlay.Equal(addr.Overlay) {
				_ = p2ps.Disconnect(got.Overlay, "provider overlay mismatch")
				return errProviderOverlay
			}
			if err := kad.Connected(ctx, p2p.Peer{Address: got.Overlay, FullNode: true}, false); err != nil {
				_ = p2ps.Disconnect(got.Overlay, "provider not accepted by topology")
				return fmt.Errorf("topology: %w", err)
			}
			return nil
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
