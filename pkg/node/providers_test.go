// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/bzz"
	"github.com/ethersphere/bee/v2/pkg/p2p"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	ma "github.com/multiformats/go-multiaddr"
)

// fakeConnector is a peerConnector that records what it was asked to do. The
// peer set is a function rather than a slice so a test can make the connect
// itself change what Peers reports, which is what the ordering case needs.
type fakeConnector struct {
	peers       func() []p2p.Peer
	connect     func(context.Context, []ma.Multiaddr) (*bzz.Address, error)
	connects    int
	dialled     []ma.Multiaddr
	disconnects []swarm.Address
}

func (f *fakeConnector) Peers() []p2p.Peer {
	if f.peers == nil {
		return nil
	}
	return f.peers()
}

func (f *fakeConnector) Connect(ctx context.Context, addrs []ma.Multiaddr) (*bzz.Address, error) {
	f.connects++
	f.dialled = addrs
	return f.connect(ctx, addrs)
}

func (f *fakeConnector) Disconnect(overlay swarm.Address, _ string) error {
	f.disconnects = append(f.disconnects, overlay)
	return nil
}

func staticPeers(addrs ...swarm.Address) func() []p2p.Peer {
	return func() []p2p.Peer {
		peers := make([]p2p.Peer, 0, len(addrs))
		for _, a := range addrs {
			peers = append(peers, p2p.Peer{Address: a, FullNode: true})
		}
		return peers
	}
}

// The provider a test connects to, and an unrelated peer used to keep the peer
// set non-empty where an empty one would let a wrong comparison pass.
var (
	testProvider = swarm.MustParseHexAddress("aa00000000000000000000000000000000000000000000000000000000000000")
	testOther    = swarm.MustParseHexAddress("bb00000000000000000000000000000000000000000000000000000000000000")
)

// testUnderlay is what a record carries and what the connect must be given.
var testUnderlay = ma.StringCast("/ip4/127.0.0.1/tcp/1634")

func testProviderAddr() *bzz.Address {
	return &bzz.Address{Overlay: testProvider, Underlays: []ma.Multiaddr{testUnderlay}}
}

// Arm 2. Already connected on some underlay: the connect reports that it
// needed no dial, AND it still runs the connect and the topology
// notification.
//
// The last two assertions are what reject a short circuit returning early on
// an already-connected peer, which would skip both and is the behaviour change
// this design exists to avoid. Two other tests here happen to reject it as
// well, because their peer sets also contain the provider; these assertions
// are the ones that do it on purpose and would survive a change to those
// fixtures. See issue #382.
func TestProviderConnectAlreadyConnected(t *testing.T) {
	t.Parallel()

	f := &fakeConnector{
		peers: staticPeers(testOther, testProvider),
		connect: func(context.Context, []ma.Multiaddr) (*bzz.Address, error) {
			return &bzz.Address{Overlay: testProvider}, nil
		},
	}

	notified := 0
	kad := func(context.Context, p2p.Peer, bool) error {
		notified++
		return nil
	}

	already, err := providerConnect(t.Context(), f, kad, testProviderAddr())
	if err != nil {
		t.Fatal(err)
	}
	if !already {
		t.Error("want already connected, got a dial")
	}
	if f.connects != 1 {
		t.Errorf("want the connect to still run once, ran %d times", f.connects)
	}
	if notified != 1 {
		t.Errorf("want topology notified once, notified %d times", notified)
	}
}

// Arm 3. Not connected: reported as a dial. The peer set is deliberately
// non-empty, so that a comparison matching any peer rather than the requested
// one is caught here.
func TestProviderConnectNotConnected(t *testing.T) {
	t.Parallel()

	f := &fakeConnector{
		peers: staticPeers(testOther),
		connect: func(context.Context, []ma.Multiaddr) (*bzz.Address, error) {
			return &bzz.Address{Overlay: testProvider}, nil
		},
	}

	already, err := providerConnect(t.Context(), f, func(context.Context, p2p.Peer, bool) error { return nil }, testProviderAddr())
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Error("want a dial, got already connected")
	}
	// The record's underlays are what gets dialled. Nothing else here would
	// notice them being dropped.
	if len(f.dialled) != 1 || !f.dialled[0].Equal(testUnderlay) {
		t.Errorf("want the record's underlays dialled, got %v", f.dialled)
	}
}

// Arm 4. The peer set is read BEFORE the connect, not after. The fake's
// connect adds the provider to what Peers reports, exactly as a real connect
// does, so reading afterwards would report already-connected for a peer this
// node has just dialled.
//
// This is the only case that fails when the read moves after the connect: the
// two above use a fixed peer set and pass either way.
func TestProviderConnectReadsPeersBeforeConnecting(t *testing.T) {
	t.Parallel()

	connected := false
	f := &fakeConnector{
		peers: func() []p2p.Peer {
			if !connected {
				return nil
			}
			return []p2p.Peer{{Address: testProvider, FullNode: true}}
		},
	}
	f.connect = func(context.Context, []ma.Multiaddr) (*bzz.Address, error) {
		connected = true
		return &bzz.Address{Overlay: testProvider}, nil
	}

	already, err := providerConnect(t.Context(), f, func(context.Context, p2p.Peer, bool) error { return nil }, testProviderAddr())
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Error("want a dial: the peer was not connected when the connect began")
	}
}

// Arm 5. The overlay guard still fires, and nothing is reported as connected
// when the node behind the underlays is not the provider the record names.
func TestProviderConnectOverlayMismatch(t *testing.T) {
	t.Parallel()

	f := &fakeConnector{
		peers: staticPeers(testProvider),
		connect: func(context.Context, []ma.Multiaddr) (*bzz.Address, error) {
			return &bzz.Address{Overlay: testOther}, nil
		},
	}

	notified := 0
	kad := func(context.Context, p2p.Peer, bool) error {
		notified++
		return nil
	}

	already, err := providerConnect(t.Context(), f, kad, testProviderAddr())
	if !errors.Is(err, errProviderOverlay) {
		t.Fatalf("want %v, got %v", errProviderOverlay, err)
	}
	if already {
		t.Error("a mismatch must not report an already-connected provider")
	}
	if len(f.disconnects) != 1 || !f.disconnects[0].Equal(testOther) {
		t.Errorf("want the wrong peer disconnected, got %v", f.disconnects)
	}
	if notified != 0 {
		t.Error("topology must not be notified for a mismatched overlay")
	}
}

// Arm 6. When p2p decides already-connected itself, the early return is kept:
// no topology notification, no error. This pins the behaviour that shipped, so
// that folding this branch into the new classification cannot pass unnoticed.
func TestProviderConnectErrAlreadyConnected(t *testing.T) {
	t.Parallel()

	f := &fakeConnector{
		peers: staticPeers(),
		connect: func(context.Context, []ma.Multiaddr) (*bzz.Address, error) {
			return nil, p2p.ErrAlreadyConnected
		},
	}

	notified := 0
	kad := func(context.Context, p2p.Peer, bool) error {
		notified++
		return nil
	}

	already, err := providerConnect(t.Context(), f, kad, testProviderAddr())
	if err != nil {
		t.Fatal(err)
	}
	if !already {
		t.Error("ErrAlreadyConnected must report that no dial was needed")
	}
	if notified != 0 {
		t.Error("the early return must not notify topology")
	}
}

// A provider the topology refuses is disconnected again rather than left
// holding a connection slot, and the error says where it came from.
//
// Nothing else here covers this path: without it, deleting the Disconnect or
// returning the bare error both pass the whole file.
func TestProviderConnectTopologyRefuses(t *testing.T) {
	t.Parallel()

	f := &fakeConnector{
		peers: staticPeers(testOther),
		connect: func(context.Context, []ma.Multiaddr) (*bzz.Address, error) {
			return &bzz.Address{Overlay: testProvider}, nil
		},
	}

	refused := errors.New("bin full")
	var gotPeer p2p.Peer
	var gotForce bool
	kad := func(_ context.Context, p p2p.Peer, force bool) error {
		gotPeer, gotForce = p, force
		return refused
	}

	already, err := providerConnect(t.Context(), f, kad, testProviderAddr())
	if !errors.Is(err, refused) {
		t.Fatalf("want the topology error wrapped, got %v", err)
	}
	if err.Error() == refused.Error() {
		t.Error("want the error to say it came from topology")
	}
	if already {
		t.Error("a refused provider must not report an already-connected peer")
	}
	if len(f.disconnects) != 1 || !f.disconnects[0].Equal(testProvider) {
		t.Errorf("want the refused peer disconnected, got %v", f.disconnects)
	}
	// A provider is dialled as a full node and without forcing past a full
	// bin, which is the whole reason the topology gets a say.
	if !gotPeer.FullNode {
		t.Error("want the provider offered to topology as a full node")
	}
	if gotForce {
		t.Error("want the connection not forced past a full bin")
	}
}

// A connect that fails is neither a dial nor an already-connected peer, so the
// caller's counter must not move either way.
func TestProviderConnectError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("dial failed")
	f := &fakeConnector{
		peers: staticPeers(testProvider),
		connect: func(context.Context, []ma.Multiaddr) (*bzz.Address, error) {
			return nil, wantErr
		},
	}

	already, err := providerConnect(t.Context(), f, func(context.Context, p2p.Peer, bool) error { return nil }, testProviderAddr())
	if !errors.Is(err, wantErr) {
		t.Fatalf("want %v, got %v", wantErr, err)
	}
	if already {
		t.Error("a failed connect must not report an already-connected provider")
	}
}
