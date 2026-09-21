// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
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

// TestPreferredOverdraftRetried tests that a preferred peer refused credit for
// a chunk is asked again once its credit clears, rather than dropped for that
// chunk. Before #324 the candidate was consumed before the credit check, so a
// transient 600 ms overdraft permanently removed the only holder of the chunk
// and the download stopped.
func TestPreferredOverdraftRetried(t *testing.T) {
	t.Parallel()

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		holderAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		calls      atomic.Int32
	)

	st := &testStorer{ChunkStore: inmemchunkstore.New()}
	if err := st.Put(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	holder := createRetrieval(t, holderAddr, st, nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	holder.SetProvidersEnabled(true)

	// Count the asks that carry the local-only header. That header is sent only
	// on the preferred path, so it is what separates "asked again as a
	// preferred peer after the overdraft cleared" from "dropped from the
	// preferred set and reached later by ordinary peer selection". Asserting
	// only that the chunk arrived does not discriminate, because the holder is
	// an ordinary peer of the client too.
	var localOnlyAsks atomic.Int32
	spec := holder.Protocol()
	inner := spec.StreamSpecs[0].Handler
	spec.StreamSpecs[0].Handler = func(ctx context.Context, p p2p.Peer, s p2p.Stream) error {
		if _, ok := s.Headers()[retrieval.LocalOnlyHeader]; ok {
			localOnlyAsks.Add(1)
		}
		return inner(ctx, p, s)
	}

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
			holderAddr.String(): spec,
		}),
	)

	// refuse the first two credit attempts with an overdraft, then allow it
	var acc *accountingmock.Service
	acc = accountingmock.NewAccounting(
		accountingmock.WithPrepareCreditFunc(func(peer swarm.Address, price uint64, originated bool) (accounting.Action, error) {
			if calls.Add(1) <= 2 {
				return nil, accounting.ErrOverdraft
			}
			return acc.MakeCreditAction(peer, price), nil
		}),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(holderAddr)), log.Noop, acc, pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(holderAddr))

	got, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	if err != nil {
		t.Fatalf("retrieve after an overdraft: %v", err)
	}
	if !got.Address().Equal(chunk.Address()) {
		t.Fatalf("got chunk %s, want %s", got.Address(), chunk.Address())
	}
	if n := calls.Load(); n < 3 {
		t.Fatalf("credit was attempted %d times, want the peer retried after the overdraft", n)
	}
	// the peer must have been asked with the local-only header, i.e. as a
	// preferred peer, not merely reached by normal selection
	if n := localOnlyAsks.Load(); n == 0 {
		t.Fatal("the peer was never asked as a preferred peer after the overdraft; " +
			"it was dropped for this chunk and only reached by ordinary selection")
	}
}

// TestPreferredNonOverdraftNotRetried tests that a refusal which will not clear
// by itself drops the peer for that chunk instead of being retried. Retrying a
// peer that is not connected would spin until the request deadline.
func TestPreferredNonOverdraftNotRetried(t *testing.T) {
	t.Parallel()

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		holderAddr = swarm.RandAddress(t)
		otherAddr  = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		calls      atomic.Int32
	)

	st := &testStorer{ChunkStore: inmemchunkstore.New()}
	if err := st.Put(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	other := createRetrieval(t, otherAddr, st, nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
			otherAddr.String(): other.Protocol(),
		}),
	)

	// the preferred peer is always refused for a reason that never clears
	notConnected := errors.New("connection not initialized yet")
	var acc *accountingmock.Service
	acc = accountingmock.NewAccounting(
		accountingmock.WithPrepareCreditFunc(func(peer swarm.Address, price uint64, originated bool) (accounting.Action, error) {
			if peer.Equal(holderAddr) {
				calls.Add(1)
				return nil, notConnected
			}
			return acc.MakeCreditAction(peer, price), nil
		}),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(holderAddr, otherAddr)), log.Noop, acc, pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(holderAddr))

	// normal selection still finds the chunk at the other peer
	if _, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress); err != nil {
		t.Fatalf("retrieve fell back to normal selection: %v", err)
	}
	if n := calls.Load(); n > 1 {
		t.Fatalf("a refusal that cannot clear was retried %d times, want 1", n)
	}
}

// TestHasPreferredPeersDistinguishesAbsentFromNil pins the Go semantics the
// helper rests on: a typed nil stored under the key satisfies the type
// assertion, so PreferredPeers returns nil for both "never attached" and
// "deliberately switched off" while HasPreferredPeers tells them apart.
//
// It is NOT a discriminating test against the naive nil check in production
// terms, because no reachable path today builds a suppressed context that
// reaches the wrapper: the one at the Discover call is handed straight to
// Discover. It records what the helper means, which is why #299 keeps it.
func TestHasPreferredPeersDistinguishesAbsentFromNil(t *testing.T) {
	t.Parallel()

	bare := context.Background()
	if retrieval.PreferredPeers(bare) != nil {
		t.Fatal("a bare context carried a set")
	}
	if retrieval.HasPreferredPeers(bare) {
		t.Fatal("a bare context reported carrying a set")
	}

	suppressed := retrieval.WithPreferredPeers(context.Background(), nil)
	if retrieval.PreferredPeers(suppressed) != nil {
		t.Fatal("a deliberately nil set did not read as nil")
	}
	if !retrieval.HasPreferredPeers(suppressed) {
		t.Fatal("a deliberately nil set was indistinguishable from none at all")
	}

	set := retrieval.NewPreferredSet(swarm.RandAddress(t))
	carrying := retrieval.WithPreferredPeers(context.Background(), set)
	if retrieval.PreferredPeers(carrying) != set {
		t.Fatal("a set did not come back")
	}
	if !retrieval.HasPreferredPeers(carrying) {
		t.Fatal("a context carrying a set reported otherwise")
	}
}

// TestPreferredSurvivesMoreOverdraftsThanReadmits is the #392 case: content
// that only the preferred peer holds, where that peer is out of credit for
// longer than maxOverdraftReadmits allows.
//
// Before #392 the chunk was lost. Each overdraft fell through to ordinary
// selection, which cannot succeed because no other peer has the chunk, and
// each of those failures spent one of the allowed errors and one of the
// readmits. The peer was dropped from the chunk once the readmits ran out, and
// the download failed with the only holder connected and willing.
//
// The refusal count here is deliberately above maxOverdraftReadmits. At or
// below it the test passes against the unfixed code and pins nothing.
func TestPreferredSurvivesMoreOverdraftsThanReadmits(t *testing.T) {
	t.Parallel()

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		holderAddr = swarm.RandAddress(t)
		emptyAddr  = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		credits    atomic.Int32
	)

	// the only node that has the chunk
	st := &testStorer{ChunkStore: inmemchunkstore.New()}
	if err := st.Put(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	holder := createRetrieval(t, holderAddr, st, nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	holder.SetProvidersEnabled(true)

	var localOnlyAsks atomic.Int32
	spec := holder.Protocol()
	inner := spec.StreamSpecs[0].Handler
	spec.StreamSpecs[0].Handler = func(ctx context.Context, p p2p.Peer, s p2p.Stream) error {
		if _, ok := s.Headers()[retrieval.LocalOnlyHeader]; ok {
			localOnlyAsks.Add(1)
		}
		return inner(ctx, p, s)
	}

	// an ordinary peer that does not have it, so ordinary selection fails and
	// the chunk is genuinely sole-source
	// an empty topology so that it answers not-found rather than trying to
	// forward, which is what a peer at the edge of the network does
	empty := createRetrieval(t, emptyAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, nil,
		topologymock.NewTopologyDriver(), log.Noop, accountingmock.NewAccounting(), pricer, nil, false)

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
			holderAddr.String(): spec,
			emptyAddr.String():  empty.Protocol(),
		}),
	)

	// refuse the holder more often than the readmit cap allows, then relent
	const refusals = 20
	var acc *accountingmock.Service
	acc = accountingmock.NewAccounting(
		accountingmock.WithPrepareCreditFunc(func(peer swarm.Address, price uint64, originated bool) (accounting.Action, error) {
			if peer.Equal(holderAddr) && credits.Add(1) <= refusals {
				return nil, accounting.ErrOverdraft
			}
			return acc.MakeCreditAction(peer, price), nil
		}),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(holderAddr, emptyAddr)), log.Noop, acc, pricer, nil, false)
	client.SetProvidersEnabled(true)

	// each overdraft costs one overDraftRefresh wait, so the deadline has to
	// allow for all of them: the point of the change is that it waits
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(holderAddr))

	got, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	if err != nil {
		t.Fatalf("sole-source chunk lost after %d overdrafts: %v", refusals, err)
	}
	if !got.Address().Equal(chunk.Address()) {
		t.Fatalf("got chunk %s, want %s", got.Address(), chunk.Address())
	}
	if n := credits.Load(); int(n) <= refusals {
		t.Fatalf("the holder was asked for credit %d times, want more than the %d refusals", n, refusals)
	}
	// it must have been served as a preferred peer rather than stumbled upon
	// by ordinary selection, which is what the local-only header distinguishes
	if localOnlyAsks.Load() == 0 {
		t.Fatal("the holder was never asked as a preferred peer; it was dropped from the chunk")
	}
}
