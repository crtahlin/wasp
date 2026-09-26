// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package libp2p

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	ma "github.com/multiformats/go-multiaddr"
)

// fakeConn is a connection that only knows its remote peer; waitIdentified
// needs nothing else.
type fakeConn struct {
	network.Conn
	peer libp2ppeer.ID
}

func (c fakeConn) RemotePeer() libp2ppeer.ID { return c.peer }

// fakeIdentify reports identify as finished when done is closed.
type fakeIdentify struct{ done chan struct{} }

func (f fakeIdentify) IdentifyWait(network.Conn) <-chan struct{} { return f.done }

func newPeer(t *testing.T) libp2ppeer.ID {
	t.Helper()
	k, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := libp2ppeer.IDFromPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newPeerstore(t *testing.T) peerstore.Peerstore {
	t.Helper()
	ps, err := pstoremem.NewPeerstore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

// TestWaitIdentifiedNoAddressAnsweredAtOnce: a peer behind NAT whose identify
// finished without a usable address is answered at once, not after the whole
// timeout. Before #511 the responder waited the full 10 s here.
func TestWaitIdentifiedNoAddressAnsweredAtOnce(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	close(done)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	got := waitIdentified(ctx, fakeIdentify{done: done}, newPeerstore(t), fakeConn{peer: newPeer(t)})
	if len(got) != 0 {
		t.Fatalf("got addresses %v for a peer that has none", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %v for a peer whose identify had finished", elapsed)
	}
}

// TestWaitIdentifiedBounded: when identify has not finished, the wait still
// ends with the context.
func TestWaitIdentifiedBounded(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	got := waitIdentified(ctx, fakeIdentify{done: make(chan struct{})}, newPeerstore(t), fakeConn{peer: newPeer(t)})
	if len(got) != 0 {
		t.Fatalf("got addresses %v", got)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("returned after %v, want about the 200 ms bound", elapsed)
	}
}

// TestWaitIdentifiedReturnsAddresses: addresses in the peerstore are
// returned, whether they were there before identify or arrived with it.
func TestWaitIdentifiedReturnsAddresses(t *testing.T) {
	t.Parallel()

	id := newPeer(t)
	addr, err := ma.NewMultiaddr("/ip4/1.2.3.4/tcp/1634")
	if err != nil {
		t.Fatal(err)
	}

	addr2, err := ma.NewMultiaddr("/ip4/5.6.7.8/tcp/1634")
	if err != nil {
		t.Fatal(err)
	}
	ps := newPeerstore(t)
	ps.AddAddrs(id, []ma.Multiaddr{addr, addr2}, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	got := waitIdentified(ctx, fakeIdentify{done: make(chan struct{})}, ps, fakeConn{peer: id})
	// all stored addresses, not just the first the stream would replay
	if len(got) != 2 {
		t.Fatalf("got %v, want both stored addresses", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %v to return an address already stored", elapsed)
	}

	late := newPeerstore(t)
	done := make(chan struct{})
	go func() {
		late.AddAddr(id, addr, time.Hour)
		close(done)
	}()
	got = waitIdentified(context.Background(), fakeIdentify{done: done}, late, fakeConn{peer: id})
	if len(got) != 1 || !got[0].Equal(addr) {
		t.Fatalf("got %v, want the address identify reported", got)
	}
}

// TestWaitIdentifiedAddressBeforeIdentify: an address that reaches the
// peerstore while identify is still running is returned at once, as the old
// address wait did, so the change is never slower than before.
func TestWaitIdentifiedAddressBeforeIdentify(t *testing.T) {
	t.Parallel()

	id := newPeer(t)
	addr, err := ma.NewMultiaddr("/ip4/1.2.3.4/tcp/1634")
	if err != nil {
		t.Fatal(err)
	}
	ps := newPeerstore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		time.Sleep(100 * time.Millisecond)
		ps.AddAddr(id, addr, time.Hour)
	}()
	start := time.Now()
	got := waitIdentified(ctx, fakeIdentify{done: make(chan struct{})}, ps, fakeConn{peer: id})
	if len(got) != 1 || !got[0].Equal(addr) {
		t.Fatalf("got %v, want the address that arrived", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %v; an address arriving before identify finished must end the wait", elapsed)
	}
}
