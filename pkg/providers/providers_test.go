// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package providers_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/bzz"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/postage"
	postagemock "github.com/ethersphere/bee/v2/pkg/postage/mock"
	"github.com/ethersphere/bee/v2/pkg/providers"
	"github.com/ethersphere/bee/v2/pkg/soc"
	statestoremock "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// network is an in-memory stand-in for the Swarm network. A new version of a
// chunk replaces the old one, so rewritten slots are visible.
type network struct {
	mu     sync.Mutex
	chunks map[string]swarm.Chunk
	// onPut, when set, runs once after the next upload
	onPut func()
}

func (n *network) setOnPut(f func()) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.onPut = f
}

func newNetwork() *network {
	return &network{chunks: make(map[string]swarm.Chunk)}
}

func (n *network) Get(_ context.Context, addr swarm.Address) (swarm.Chunk, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, ok := n.chunks[addr.ByteString()]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return ch, nil
}

func (n *network) RetrieveChunk(ctx context.Context, addr, _ swarm.Address) (swarm.Chunk, error) {
	return n.Get(ctx, addr)
}

func (n *network) has(addr swarm.Address) bool {
	_, err := n.Get(context.Background(), addr)
	return err == nil
}

func (n *network) delete(addr swarm.Address) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.chunks, addr.ByteString())
}

func (n *network) session() providers.PutterSession { return &session{n: n} }

type session struct{ n *network }

func (s *session) Put(_ context.Context, ch swarm.Chunk) error {
	s.n.mu.Lock()
	s.n.chunks[ch.Address().ByteString()] = ch
	hook := s.n.onPut
	s.n.onPut = nil
	s.n.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (s *session) Done(swarm.Address) error { return nil }
func (s *session) Cleanup() error           { return nil }

// clock is a settable time source shared by the services of one test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// adder records the overlays a discovery adds.
type adder struct {
	mu    sync.Mutex
	peers []swarm.Address
}

func (a *adder) Add(peers ...swarm.Address) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.peers = append(a.peers, peers...)
}

func (a *adder) list() []swarm.Address {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.peers)
}

var batch = bytes.Repeat([]byte{1}, 32)

func newService(t *testing.T, n *network, nd node, c *clock, connect func(context.Context, *bzz.Address) error) *providers.Service {
	t.Helper()
	return newServiceWith(t, n, n, nd, c, connect)
}

// newServiceWith builds a service whose lookups read local, which stands for
// the node's own store in front of the network, and whose read-backs and
// uploads use n, the network itself.
func newServiceWith(t *testing.T, local, n *network, nd node, c *clock, connect func(context.Context, *bzz.Address) error) *providers.Service {
	t.Helper()

	svc, err := providers.New(providers.Options{
		Logger:    log.Noop,
		NetworkID: networkID,
		Overlay:   nd.addr.Overlay,
		Signer:    nd.signer,
		Getter:    local,
		Fetcher:   n,
		Uploader:  n.session,
		Stamper: func([]byte) (postage.Stamper, func() error, error) {
			return postagemock.NewStamper(), func() error { return nil }, nil
		},
		Address: func() (*bzz.Address, error) { return nd.addr, nil },
		Connect: connect,
		Store:   statestoremock.NewStateStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetNow(c.now)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// midWindow returns a time one hour into window w.
func midWindow(w uint64) time.Time {
	return providers.WindowStart(w).Add(time.Hour)
}

func slotAddress(t *testing.T, k []byte, w uint64, owner []byte) swarm.Address {
	t.Helper()
	s, err := providers.IndexSigner(k)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := s.EthereumAddress()
	if err != nil {
		t.Fatal(err)
	}
	addr, err := soc.CreateAddress(providers.SlotID(k, w, providers.SlotFor(owner)), idx.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func recordAddress(t *testing.T, k, owner []byte, w uint64) swarm.Address {
	t.Helper()
	addr, err := providers.RecordAddress(k, owner, w)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestAnnounceAndLookup(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b, d := newNode(t, 1), newNode(t, 1), newNode(t, 1)
	pa := newService(t, n, a, c, nil)
	pd := newService(t, n, d, c, nil)
	reader := newService(t, n, b, c, nil)
	k := swarm.RandAddress(t).Bytes()

	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}
	if err := pd.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	records, err := reader.Lookup(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	overlays := make([]swarm.Address, 0, len(records))
	for _, r := range records {
		overlays = append(overlays, r.Address.Overlay)
	}
	if len(records) != 2 || !swarm.ContainsAddress(overlays, a.addr.Overlay) || !swarm.ContainsAddress(overlays, d.addr.Overlay) {
		t.Fatalf("lookup found %v, want both providers", overlays)
	}
}

func TestLookupCachesEmptyResults(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)
	pa := newService(t, n, a, c, nil)
	reader := newService(t, n, b, c, nil)
	k := swarm.RandAddress(t).Bytes()

	if records, _ := reader.Lookup(context.Background(), k); len(records) != 0 {
		t.Fatalf("found %d providers before any announcement", len(records))
	}
	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}
	if records, _ := reader.Lookup(context.Background(), k); len(records) != 0 {
		t.Fatal("an empty result was not reused within the cache period")
	}

	c.set(c.now().Add(11 * time.Minute))
	if records, _ := reader.Lookup(context.Background(), k); len(records) != 1 {
		t.Fatalf("found %d providers after the cache period, want 1", len(records))
	}
}

func TestAnnounceWritesNextWindowAhead(t *testing.T) {
	t.Parallel()

	n := newNetwork()
	c := &clock{t: providers.WindowStart(1001).Add(-30 * time.Minute)}
	a := newNode(t, 1)
	pa := newService(t, n, a, c, nil)
	k := swarm.RandAddress(t).Bytes()

	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}
	for _, w := range []uint64{1000, 1001} {
		if !n.has(recordAddress(t, k, a.owner, w)) {
			t.Errorf("no record for window %d", w)
		}
		if !n.has(slotAddress(t, k, w, a.owner)) {
			t.Errorf("no pointer slot for window %d", w)
		}
	}

	announced, err := pa.Announced()
	if err != nil {
		t.Fatal(err)
	}
	if len(announced) != 1 || !slices.Equal(announced[0].Written, []uint64{1000, 1001}) {
		t.Fatalf("announced %+v, want windows 1000 and 1001 written", announced)
	}
}

func TestLoopWritesNewWindow(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a := newNode(t, 1)
	pa := newService(t, n, a, c, nil)
	k := swarm.RandAddress(t).Bytes()

	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	c.set(midWindow(1001))
	pa.RunOnce(context.Background())

	if !n.has(recordAddress(t, k, a.owner, 1001)) {
		t.Fatal("the loop did not write the new window")
	}
	announced, err := pa.Announced()
	if err != nil {
		t.Fatal(err)
	}
	if len(announced) != 1 || !slices.Equal(announced[0].Written, []uint64{1001}) {
		t.Fatalf("announced %+v, want only window 1001 kept", announced)
	}
}

func TestReadBackRewritesLostEntry(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a := newNode(t, 1)
	pa := newService(t, n, a, c, nil)
	k := swarm.RandAddress(t).Bytes()

	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	slot := slotAddress(t, k, 1000, a.owner)
	n.delete(slot)

	c.set(c.now().Add(2 * time.Minute))
	pa.RunOnce(context.Background())

	ch, err := n.Get(context.Background(), slot)
	if err != nil {
		t.Fatalf("lost pointer entry was not written again: %v", err)
	}
	owners, err := providers.ParseSlot(ch, k, 1000, providers.SlotFor(a.owner))
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || !bytes.Equal(owners[0], a.owner) {
		t.Fatalf("slot lists %x, want the provider", owners)
	}
}

// TestReadBackIgnoresLocalCopy tests that the read-back asks the network, not
// the node's own store, which can still hold an entry the network has lost.
func TestReadBackIgnoresLocalCopy(t *testing.T) {
	t.Parallel()

	n, local, c := newNetwork(), newNetwork(), &clock{t: midWindow(1000)}
	a := newNode(t, 1)
	pa := newServiceWith(t, local, n, a, c, nil)
	k := swarm.RandAddress(t).Bytes()

	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	slot := slotAddress(t, k, 1000, a.owner)
	ch, err := n.Get(context.Background(), slot)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.session().Put(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	n.delete(slot)

	c.set(c.now().Add(2 * time.Minute))
	pa.RunOnce(context.Background())

	if !n.has(slot) {
		t.Fatal("the read-back trusted the local copy and did not write the entry again")
	}
}

// TestLookupSkipsJunkEntries tests that a lookup returns only providers whose
// records verify, whatever else the open index lists.
func TestLookupSkipsJunkEntries(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)
	pa := newService(t, n, a, c, nil)
	reader := newService(t, n, b, c, nil)
	k := swarm.RandAddress(t).Bytes()

	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	// another slot lists owners that have no record
	junk := [][]byte{swarm.RandAddress(t).Bytes()[:20], swarm.RandAddress(t).Bytes()[:20]}
	i := (providers.SlotFor(a.owner) + 1) % providers.Slots
	ch, err := providers.NewSlotChunk(k, 1000, i, junk)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.session().Put(context.Background(), ch); err != nil {
		t.Fatal(err)
	}

	records, err := reader.Lookup(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !bytes.Equal(records[0].Owner, a.owner) {
		t.Fatalf("lookup returned %d records, want only the provider's", len(records))
	}
}

func TestEncryptedReferenceRefused(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	pa := newService(t, n, newNode(t, 1), c, nil)

	// 64 bytes: a reference followed by its decryption key
	k := append(swarm.RandAddress(t).Bytes(), swarm.RandAddress(t).Bytes()...)
	if err := pa.Announce(context.Background(), k, batch); err == nil {
		t.Fatal("an encrypted reference was announced; its record would publish the key")
	}
	if _, err := providers.NewSlotChunk(k, 1000, 0, nil); !errors.Is(err, providers.ErrInvalidRecord) {
		t.Fatalf("a pointer slot for an encrypted reference: got %v, want ErrInvalidRecord", err)
	}
	if records, err := pa.Lookup(context.Background(), k); err != nil || len(records) != 0 {
		t.Fatalf("lookup of an encrypted reference: %d records, error %v", len(records), err)
	}
}

// TestWithdrawDuringPublish tests that a withdrawal made while the loop is
// publishing a new window is not undone by the loop's save.
func TestWithdrawDuringPublish(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	pa := newService(t, n, newNode(t, 1), c, nil)
	k := swarm.RandAddress(t).Bytes()

	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	c.set(midWindow(1001))
	n.setOnPut(func() {
		if err := pa.Withdraw(k); err != nil {
			t.Error(err)
		}
	})
	pa.RunOnce(context.Background())

	announced, err := pa.Announced()
	if err != nil {
		t.Fatal(err)
	}
	if len(announced) != 0 {
		t.Fatal("a key withdrawn during the loop's publish was announced again")
	}
}

// TestSaveKeepsNewerBatch tests that the loop does not write an older batch
// over a newer announcement of the same key.
func TestSaveKeepsNewerBatch(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	pa := newService(t, n, newNode(t, 1), c, nil)
	k := swarm.RandAddress(t).Bytes()
	newer := bytes.Repeat([]byte{2}, 32)

	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	c.set(midWindow(1001))
	n.setOnPut(func() {
		if err := pa.Announce(context.Background(), k, newer); err != nil {
			t.Error(err)
		}
	})
	pa.RunOnce(context.Background())

	announced, err := pa.Announced()
	if err != nil {
		t.Fatal(err)
	}
	if len(announced) != 1 || !bytes.Equal(announced[0].BatchID, newer) {
		t.Fatalf("announced %+v, want the newer batch kept", announced)
	}
}

func TestWithdraw(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	pa := newService(t, n, newNode(t, 1), c, nil)
	k := swarm.RandAddress(t).Bytes()

	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}
	if err := pa.Withdraw(k); err != nil {
		t.Fatal(err)
	}
	announced, err := pa.Announced()
	if err != nil {
		t.Fatal(err)
	}
	if len(announced) != 0 {
		t.Fatalf("still announcing %d keys after withdrawing", len(announced))
	}
}

func TestDiscover(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	var mu sync.Mutex
	var dialed []swarm.Address
	connect := func(_ context.Context, addr *bzz.Address) error {
		mu.Lock()
		defer mu.Unlock()
		dialed = append(dialed, addr.Overlay)
		return nil
	}

	pa := newService(t, n, a, c, connect)
	reader := newService(t, n, b, c, connect)
	k := swarm.RandAddress(t).Bytes()
	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	found := &adder{}
	reader.Discover(context.Background(), k, found)
	self := &adder{}
	pa.Discover(context.Background(), k, self)

	// Close waits for the discoveries to finish
	_ = reader.Close()
	_ = pa.Close()

	if got := found.list(); len(got) != 1 || !got[0].Equal(a.addr.Overlay) {
		t.Fatalf("discovery added %v, want the provider", got)
	}
	if got := self.list(); len(got) != 0 {
		t.Fatalf("a provider discovered itself: %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 1 || !dialed[0].Equal(a.addr.Overlay) {
		t.Fatalf("dialed %v, want the provider once", dialed)
	}
}
