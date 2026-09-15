// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/accounting"
	accountingmock "github.com/ethersphere/bee/v2/pkg/accounting/mock"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/p2p"
	"github.com/ethersphere/bee/v2/pkg/p2p/protobuf"
	"github.com/ethersphere/bee/v2/pkg/p2p/streamtest"
	pricermock "github.com/ethersphere/bee/v2/pkg/pricer/mock"
	"github.com/ethersphere/bee/v2/pkg/ratelimit"
	"github.com/ethersphere/bee/v2/pkg/retrieval"
	pb "github.com/ethersphere/bee/v2/pkg/retrieval/pb"
	"github.com/ethersphere/bee/v2/pkg/storage/inmemchunkstore"
	testingc "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	topologymock "github.com/ethersphere/bee/v2/pkg/topology/mock"
)

// providerFixture is a client that prefers a provider, and a holder that has
// the chunk and serves it through normal retrieval.
type providerFixture struct {
	chunk          swarm.Chunk
	clientAddr     swarm.Address
	providerAddr   swarm.Address
	holderAddr     swarm.Address
	provider       *retrieval.Service
	client         *retrieval.Service
	recorder       *streamtest.Recorder
	clientAcc      accounting.Interface
	providerAcc    accounting.Interface
	providerStorer *testStorer
}

func newProviderFixture(t *testing.T, providerEnabled bool, providerStreamer p2p.Streamer, providerTopology *topologymock.Option) *providerFixture {
	t.Helper()

	f := &providerFixture{
		chunk:          testingc.FixtureChunk("0033"),
		clientAddr:     swarm.RandAddress(t),
		providerAddr:   swarm.RandAddress(t),
		holderAddr:     swarm.RandAddress(t),
		clientAcc:      accountingmock.NewAccounting(),
		providerAcc:    accountingmock.NewAccounting(),
		providerStorer: &testStorer{ChunkStore: inmemchunkstore.New()},
	}
	pricer := pricermock.NewMockService(defaultPrice, defaultPrice)

	holderStorer := &testStorer{ChunkStore: inmemchunkstore.New()}
	if err := holderStorer.Put(context.Background(), f.chunk); err != nil {
		t.Fatal(err)
	}
	holder := createRetrieval(t, f.holderAddr, holderStorer, nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)

	provTopology := topologymock.NewTopologyDriver()
	if providerTopology != nil {
		provTopology = topologymock.NewTopologyDriver(*providerTopology)
	}
	f.provider = createRetrieval(t, f.providerAddr, f.providerStorer, providerStreamer, provTopology, log.Noop, f.providerAcc, pricer, nil, false)
	f.provider.SetProvidersEnabled(providerEnabled)

	f.recorder = streamtest.New(
		streamtest.WithBaseAddr(f.clientAddr),
		streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
			f.providerAddr.String(): f.provider.Protocol(),
			f.holderAddr.String():   holder.Protocol(),
		}),
	)
	clientTopology := topologymock.NewTopologyDriver(topologymock.WithPeers(f.providerAddr, f.holderAddr))
	f.client = createRetrieval(t, f.clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, f.recorder, clientTopology, log.Noop, f.clientAcc, pricer, nil, false)
	f.client.SetProvidersEnabled(true)

	return f
}

func (f *providerFixture) retrieve(t *testing.T, preferred ...swarm.Address) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(preferred...))

	got, err := f.client.RetrieveChunk(ctx, f.chunk.Address(), swarm.ZeroAddress)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data(), f.chunk.Data()) {
		t.Fatal("retrieved data differs from the chunk")
	}
}

// deliveries returns the delivery messages a peer wrote on its recorded
// retrieval streams, in the order the streams were opened.
func deliveries(t *testing.T, recorder *streamtest.Recorder, peer swarm.Address) []*pb.Delivery {
	t.Helper()

	records, err := recorder.Records(peer, "retrieval", "1.4.0", "retrieval")
	if err != nil {
		t.Fatal(err)
	}
	var out []*pb.Delivery
	for _, record := range records {
		messages, err := protobuf.ReadMessages(bytes.NewReader(record.Out()), func() protobuf.Message { return new(pb.Delivery) })
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			out = append(out, m.(*pb.Delivery))
		}
	}
	return out
}

func zeroBalance(t *testing.T, acc accounting.Interface, peer swarm.Address) {
	t.Helper()
	if b, err := acc.Balance(peer); err == nil && b.Sign() != 0 {
		t.Fatalf("balance with %s is %s, want 0", peer, b)
	}
}

// TestLocalOnlyMissFallsBack tests that a provider without the chunk answers a
// local-only miss without forwarding or charging, and that the requester then
// retrieves the chunk normally without putting the provider on the error skip
// list.
func TestLocalOnlyMissFallsBack(t *testing.T) {
	t.Parallel()

	f := newProviderFixture(t, true, nil, nil)
	f.retrieve(t, f.providerAddr)

	got := deliveries(t, f.recorder, f.providerAddr)
	if len(got) != 1 {
		t.Fatalf("provider wrote %d deliveries, want 1", len(got))
	}
	if got[0].Err != retrieval.ErrNotHeldLocally.Error() {
		t.Fatalf("provider answered %q, want %q", got[0].Err, retrieval.ErrNotHeldLocally.Error())
	}
	if len(got[0].Data) != 0 {
		t.Fatal("provider delivered data on a miss")
	}

	if swarm.ContainsAddress(f.client.ErrSkipped(f.chunk.Address()), f.providerAddr) {
		t.Fatal("a local-only miss put the provider on the error skip list")
	}

	zeroBalance(t, f.clientAcc, f.providerAddr)
	zeroBalance(t, f.providerAcc, f.clientAddr)
}

// TestLocalOnlyHit tests that a provider holding the chunk serves it and is
// paid its price, and that no other peer is asked.
func TestLocalOnlyHit(t *testing.T) {
	t.Parallel()

	f := newProviderFixture(t, true, nil, nil)
	if err := f.providerStorer.Put(context.Background(), f.chunk); err != nil {
		t.Fatal(err)
	}
	f.retrieve(t, f.providerAddr)

	got := deliveries(t, f.recorder, f.providerAddr)
	if len(got) != 1 || !bytes.Equal(got[0].Data, f.chunk.Data()) {
		t.Fatalf("provider did not deliver the chunk: %v", got)
	}
	if _, err := f.recorder.Records(f.holderAddr, "retrieval", "1.4.0", "retrieval"); !errors.Is(err, streamtest.ErrRecordsNotFound) {
		t.Fatalf("holder was asked, want only the provider: %v", err)
	}

	b, err := f.clientAcc.Balance(f.providerAddr)
	if err != nil {
		t.Fatal(err)
	}
	if b.Int64() != -int64(defaultPrice) {
		t.Fatalf("client balance with provider is %s, want %d", b, -int64(defaultPrice))
	}
}

// TestLocalOnlyDisabledForwards tests that a provider with the setting off
// ignores the local-only header and forwards, as a stock node does.
func TestLocalOnlyDisabledForwards(t *testing.T) {
	t.Parallel()

	var (
		chunk = testingc.FixtureChunk("0033")
		// a forwarder only picks a peer closer to the chunk than itself; the
		// chunk's own address is closer than any other
		upstreamAddr = chunk.Address()
		pricer       = pricermock.NewMockService(defaultPrice, defaultPrice)
	)
	upstreamStorer := &testStorer{ChunkStore: inmemchunkstore.New()}
	if err := upstreamStorer.Put(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	upstream := createRetrieval(t, upstreamAddr, upstreamStorer, nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	providerRecorder := streamtest.New(streamtest.WithProtocols(upstream.Protocol()))
	// the forwarder skips the peer that asked, and the mock ignores a fixed
	// closest peer whenever something is skipped, so list the peer instead
	forwardTo := topologymock.WithPeers(upstreamAddr)

	f := newProviderFixture(t, false, providerRecorder, &forwardTo)
	f.retrieve(t, f.providerAddr)

	got := deliveries(t, f.recorder, f.providerAddr)
	if len(got) != 1 || !bytes.Equal(got[0].Data, f.chunk.Data()) {
		t.Fatalf("provider with the setting off did not forward and deliver: %v", got)
	}
	if _, err := providerRecorder.Records(upstreamAddr, "retrieval", "1.4.0", "retrieval"); err != nil {
		t.Fatalf("provider did not forward: %v", err)
	}
}

// TestLocalOnlyLimit tests that past the miss limit a provider answers with the
// limit error, still without forwarding or charging.
func TestLocalOnlyLimit(t *testing.T) {
	t.Parallel()

	f := newProviderFixture(t, true, nil, nil)
	f.provider.SetMissLimiters(ratelimit.New(time.Hour, 1), ratelimit.New(time.Hour, 100))

	// singleflight merges only concurrent retrievals, so the second one asks
	// the provider again
	f.retrieve(t, f.providerAddr)
	f.retrieve(t, f.providerAddr)

	got := deliveries(t, f.recorder, f.providerAddr)
	if len(got) != 2 {
		t.Fatalf("provider wrote %d deliveries, want 2", len(got))
	}
	if got[0].Err != retrieval.ErrNotHeldLocally.Error() {
		t.Fatalf("first answer %q, want %q", got[0].Err, retrieval.ErrNotHeldLocally.Error())
	}
	if got[1].Err != retrieval.ErrLocalOnlyLimit.Error() {
		t.Fatalf("second answer %q, want %q", got[1].Err, retrieval.ErrLocalOnlyLimit.Error())
	}
	zeroBalance(t, f.providerAcc, f.clientAddr)
}

// TestPreferredOnlyConnected tests that a preferred peer that is not a
// connected full node is never asked, even though it would answer.
func TestPreferredOnlyConnected(t *testing.T) {
	t.Parallel()

	var (
		chunk        = testingc.FixtureChunk("0033")
		clientAddr   = swarm.RandAddress(t)
		strangerAddr = swarm.RandAddress(t)
		holderAddr   = swarm.RandAddress(t)
		pricer       = pricermock.NewMockService(defaultPrice, defaultPrice)
	)

	newHolder := func(addr swarm.Address) *retrieval.Service {
		st := &testStorer{ChunkStore: inmemchunkstore.New()}
		if err := st.Put(context.Background(), chunk); err != nil {
			t.Fatal(err)
		}
		return createRetrieval(t, addr, st, nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	}
	holder := newHolder(holderAddr)
	// the stranger holds the chunk and has a handler, but is not connected
	stranger := newHolder(strangerAddr)
	stranger.SetProvidersEnabled(true)

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
			strangerAddr.String(): stranger.Protocol(),
			holderAddr.String():   holder.Protocol(),
		}),
	)
	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(holderAddr)), log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(strangerAddr))
	if _, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress); err != nil {
		t.Fatal(err)
	}

	if _, err := recorder.Records(strangerAddr, "retrieval", "1.4.0", "retrieval"); !errors.Is(err, streamtest.ErrRecordsNotFound) {
		t.Fatalf("a preferred peer that is not connected was asked: %v", err)
	}
}

// TestPreferredMissStopsTimer tests that a preferred attempt that misses before
// preferredWait does not leave its timer to start a second normal attempt.
func TestPreferredMissStopsTimer(t *testing.T) {
	t.Parallel()

	var (
		chunk        = testingc.FixtureChunk("0033")
		clientAddr   = swarm.RandAddress(t)
		providerAddr = swarm.RandAddress(t)
		holder1      = swarm.RandAddress(t)
		holder2      = swarm.RandAddress(t)
		pricer       = pricermock.NewMockService(defaultPrice, defaultPrice)
	)

	// a holder that answers after 600 ms: after the 500 ms preferred timer,
	// before the 1 s preemptive attempt
	slowHolder := func(addr swarm.Address) p2p.ProtocolSpec {
		st := &testStorer{ChunkStore: inmemchunkstore.New()}
		if err := st.Put(context.Background(), chunk); err != nil {
			t.Fatal(err)
		}
		spec := createRetrieval(t, addr, st, nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false).Protocol()
		handler := spec.StreamSpecs[0].Handler
		spec.StreamSpecs[0].Handler = func(ctx context.Context, p p2p.Peer, s p2p.Stream) error {
			time.Sleep(600 * time.Millisecond)
			return handler(ctx, p, s)
		}
		return spec
	}

	provider := createRetrieval(t, providerAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	provider.SetProvidersEnabled(true)

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
			providerAddr.String(): provider.Protocol(),
			holder1.String():      slowHolder(holder1),
			holder2.String():      slowHolder(holder2),
		}),
	)
	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(providerAddr, holder1, holder2)), log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(providerAddr))
	if _, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress); err != nil {
		t.Fatal(err)
	}

	attempts := 0
	for _, h := range []swarm.Address{holder1, holder2} {
		if records, err := recorder.Records(h, "retrieval", "1.4.0", "retrieval"); err == nil {
			attempts += len(records)
		}
	}
	if attempts != 1 {
		t.Fatalf("%d normal attempts, want 1: the missed preferred attempt's timer started another", attempts)
	}
}

// TestPreferredInvalidChunkDemotes tests that a provider that delivers data
// not matching the chunk address is dropped at once, and that the chunk is
// then retrieved normally.
func TestPreferredInvalidChunkDemotes(t *testing.T) {
	t.Parallel()

	f := newProviderFixture(t, true, nil, nil)
	bad := swarm.NewChunk(f.chunk.Address(), []byte("these bytes are not the chunk"))
	if err := f.providerStorer.Put(context.Background(), bad); err != nil {
		t.Fatal(err)
	}

	set := retrieval.NewPreferredSet(f.providerAddr)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	got, err := f.client.RetrieveChunk(retrieval.WithPreferredPeers(ctx, set), f.chunk.Address(), swarm.ZeroAddress)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data(), f.chunk.Data()) {
		t.Fatal("retrieved data differs from the chunk")
	}
	if swarm.ContainsAddress(set.Peers(), f.providerAddr) {
		t.Fatal("a provider that delivered an invalid chunk was not dropped")
	}
}

func TestPreferredSetDemotion(t *testing.T) {
	t.Parallel()

	peer := swarm.RandAddress(t)
	now := time.Now()

	set := retrieval.NewPreferredSet(peer)
	set.SetNow(func() time.Time { return now })

	for range retrieval.DemoteAfterMisses - 1 {
		set.Miss(peer)
	}
	if !swarm.ContainsAddress(set.Peers(), peer) {
		t.Fatal("peer dropped before the miss limit")
	}

	set.Hit(peer)
	for range retrieval.DemoteAfterMisses - 1 {
		set.Miss(peer)
	}
	if !swarm.ContainsAddress(set.Peers(), peer) {
		t.Fatal("a hit did not reset the miss count")
	}

	set.Miss(peer)
	if swarm.ContainsAddress(set.Peers(), peer) {
		t.Fatal("peer not dropped at the miss limit")
	}

	now = now.Add(11 * time.Minute)
	if !swarm.ContainsAddress(set.Peers(), peer) {
		t.Fatal("dropped peer did not come back after the drop period")
	}

	set.Demote(peer)
	if swarm.ContainsAddress(set.Peers(), peer) {
		t.Fatal("peer not dropped at once")
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()

	a, b := swarm.RandAddress(t), swarm.RandAddress(t)

	if retrieval.Fingerprint(nil) != "" {
		t.Fatal("an empty set must have no fingerprint")
	}
	if retrieval.Fingerprint([]swarm.Address{a, b}) != retrieval.Fingerprint([]swarm.Address{b, a}) {
		t.Fatal("fingerprint depends on order")
	}
	if retrieval.Fingerprint([]swarm.Address{a}) == retrieval.Fingerprint([]swarm.Address{a, b}) {
		t.Fatal("different sets share a fingerprint")
	}
}
