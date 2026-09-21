// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval_test

import (
	"bytes"
	"context"
	"errors"
	"sort"
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

// TestFirstOverdraftKeepsTheFirstTime pins the retention clock: it must report
// when the peer was FIRST refused credit for the chunk, not the latest refusal.
// Resetting on every call would make the deadline unreachable and retain a peer
// for ever, which is the livelock the withdrawn #392 design had.
func TestFirstOverdraftKeepsTheFirstTime(t *testing.T) {
	t.Parallel()

	since := make(map[string]time.Time)
	peer := swarm.RandAddress(t)
	base := time.Now()

	got := retrieval.FirstOverdraft(since, peer, base)
	if !got.Equal(base) {
		t.Fatalf("first call: got %v, want %v", got, base)
	}
	// a later refusal must not move the clock forward
	later := retrieval.FirstOverdraft(since, peer, base.Add(time.Hour))
	if !later.Equal(base) {
		t.Fatalf("second call: got %v, want the first time %v", later, base)
	}
	// a different peer keeps its own clock
	other := swarm.RandAddress(t)
	o := retrieval.FirstOverdraft(since, other, base.Add(time.Minute))
	if !o.Equal(base.Add(time.Minute)) {
		t.Fatalf("other peer: got %v, want its own first time", o)
	}
}

// TestPreferredRetainedBeyondReadmitCount is the #392 case. A provider that is
// the only holder is refused credit more times than maxOverdraftReadmits allows
// but well within providerCreditWait, and must still be asked as a preferred
// peer rather than dropped from the chunk.
//
// The refusal count is deliberately above maxOverdraftReadmits: at or below it
// the test passes against the pre-#392 count-based retention and pins nothing.
func TestPreferredRetainedBeyondReadmitCount(t *testing.T) {
	t.Parallel()

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		holderAddr = swarm.RandAddress(t)
		emptyAddr  = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		credits    atomic.Int32
	)

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

	// an ordinary peer with an empty topology, so it answers not-found rather
	// than trying to forward
	empty := createRetrieval(t, emptyAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, nil,
		topologymock.NewTopologyDriver(), log.Noop, accountingmock.NewAccounting(), pricer, nil, false)

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
			holderAddr.String(): spec,
			emptyAddr.String():  empty.Protocol(),
		}),
	)

	const refusals = 20 // above maxOverdraftReadmits, below what 5s allows
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

	ctx, cancel := context.WithTimeout(context.Background(), retrieval.ProviderCreditWait+10*time.Second)
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
	if localOnlyAsks.Load() == 0 {
		t.Fatal("the holder was never asked as a preferred peer; it was dropped from the chunk")
	}
}

// TestPreferredRetentionExpires is the other half of #392: retention must end.
// A provider that never regains credit has to be dropped from the chunk so the
// request can conclude, rather than being retried until the caller gives up.
//
// The window is shortened on this client only, through a per-service field, so
// the test does not wait the shipped duration and cannot race a parallel test.
func TestPreferredRetentionExpires(t *testing.T) {
	t.Parallel()

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		holderAddr = swarm.RandAddress(t)
		otherAddr  = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		credits    atomic.Int32
	)

	// the OTHER peer has the chunk, so once the holder is dropped the request
	// can still finish and we can tell "dropped" from "hung"
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

	// the preferred holder is refused credit for ever
	var acc *accountingmock.Service
	acc = accountingmock.NewAccounting(
		accountingmock.WithPrepareCreditFunc(func(peer swarm.Address, price uint64, originated bool) (accounting.Action, error) {
			if peer.Equal(holderAddr) {
				credits.Add(1)
				return nil, accounting.ErrOverdraft
			}
			return acc.MakeCreditAction(peer, price), nil
		}),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(holderAddr, otherAddr)), log.Noop, acc, pricer, nil, false)
	client.SetProvidersEnabled(true)
	client.SetProviderCreditWait(300 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(holderAddr))

	start := time.Now()
	got, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	if err != nil {
		t.Fatalf("retention did not expire, the request never fell back: %v", err)
	}
	if !got.Address().Equal(chunk.Address()) {
		t.Fatalf("got chunk %s, want %s", got.Address(), chunk.Address())
	}
	// it must not have spun on the holder for the whole deadline
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v, want the holder dropped soon after the 300ms window", elapsed)
	}
}

// peersByDistance returns n random addresses sorted from the closest to the
// given chunk address to the furthest. Normal peer selection always takes the
// closest peer it has not skipped, so ordering the peers lets a test decide
// which peer a request reaches first and which it reaches last.
func peersByDistance(t *testing.T, chunkAddr swarm.Address, n int) []swarm.Address {
	t.Helper()

	addrs := make([]swarm.Address, n)
	for i := range addrs {
		addrs[i] = swarm.RandAddress(t)
	}
	sort.SliceStable(addrs, func(i, j int) bool {
		cmp, err := swarm.DistanceCmp(chunkAddr, addrs[i], addrs[j])
		if err != nil {
			t.Fatal(err)
		}
		return cmp == 1
	})
	return addrs
}

// failingPeers registers one retrieval service under every given address. It
// has an empty store and an empty topology, so it can neither answer from its
// own store nor forward, and every request to it fails. A lag above zero
// delays each answer, which spaces the requester's rounds far enough apart for
// a short retention window to expire between them.
func failingPeers(t *testing.T, addrs []swarm.Address, lag time.Duration) map[string]p2p.ProtocolSpec {
	t.Helper()

	empty := createRetrieval(t, swarm.RandAddress(t), &testStorer{ChunkStore: inmemchunkstore.New()}, nil,
		topologymock.NewTopologyDriver(), log.Noop, accountingmock.NewAccounting(),
		pricermock.NewMockService(defaultPrice, defaultPrice), nil, false)

	spec := empty.Protocol()
	if lag > 0 {
		inner := spec.StreamSpecs[0].Handler
		spec.StreamSpecs[0].Handler = func(ctx context.Context, p p2p.Peer, s p2p.Stream) error {
			select {
			case <-time.After(lag):
			case <-ctx.Done():
				return ctx.Err()
			}
			return inner(ctx, p, s)
		}
	}

	specs := make(map[string]p2p.ProtocolSpec, len(addrs))
	for _, a := range addrs {
		specs[a.String()] = spec
	}
	return specs
}

// TestPreferredRetentionStopsAskingAfterTheWindow is the other half of #392:
// retention has to end. A provider that keeps being refused credit must stop
// being a candidate once its window has passed. If it never stops, it is asked
// again on every round for the whole life of the request, and the error budget,
// which only resumes once no candidate is left, can never end the search.
//
// What this asserts is how many times the provider is asked, not whether the
// request succeeds. An overdraft falls through to normal selection at once, so
// the chunk arrives either way and the outcome on its own pins nothing.
func TestPreferredRetentionStopsAskingAfterTheWindow(t *testing.T) {
	t.Parallel()

	const (
		misses = 6
		lag    = 50 * time.Millisecond
		window = 10 * time.Millisecond
	)

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		credits    atomic.Int32
	)

	// This ordering makes the request spend one round on each empty peer before
	// the holder answers it, and never reach the provider by normal selection.
	ring := peersByDistance(t, chunk.Address(), misses+2)
	emptyAddrs, holderAddr, providerAddr := ring[:misses], ring[misses], ring[misses+1]

	st := &testStorer{ChunkStore: inmemchunkstore.New()}
	if err := st.Put(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	holder := createRetrieval(t, holderAddr, st, nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)

	specs := failingPeers(t, emptyAddrs, lag)
	specs[holderAddr.String()] = holder.Protocol()

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(specs),
	)

	// the provider is refused credit for ever
	var acc *accountingmock.Service
	acc = accountingmock.NewAccounting(
		accountingmock.WithPrepareCreditFunc(func(peer swarm.Address, price uint64, originated bool) (accounting.Action, error) {
			if peer.Equal(providerAddr) {
				credits.Add(1)
				return nil, accounting.ErrOverdraft
			}
			return acc.MakeCreditAction(peer, price), nil
		}),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(ring...)), log.Noop, acc, pricer, nil, false)
	client.SetProvidersEnabled(true)
	client.SetProviderCreditWait(window)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(providerAddr))

	got, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !got.Address().Equal(chunk.Address()) {
		t.Fatalf("got chunk %s, want %s", got.Address(), chunk.Address())
	}
	// One ask opens the window and the next one finds it closed and drops the
	// peer, so two is what this run should produce. The request runs misses+1
	// rounds, so a client that never drops the provider asks it about that many
	// times, which is what the upper bound separates this from.
	if n := credits.Load(); n < 1 || n > 3 {
		t.Fatalf("the provider was asked for credit %d times, want it dropped after the %v window", n, window)
	}
}

// TestErrorBudgetSurvivesWhileAProviderRemains is the second half of #392. The
// per-chunk error budget exists to end a hopeless search among ordinary peers.
// While a provider is still a candidate for the chunk the search is not
// hopeless, only unfunded, and spending the budget on ordinary misses ends the
// download before the provider's credit arrives.
//
// The provider here is refused credit more times than the budget allows
// ordinary errors, so a client that spends the budget gives up first and a
// client that keeps it gets the chunk. The empty peers are one per unit of
// budget: with fewer of them the budget cannot be exhausted at all and the
// test would pass either way.
func TestErrorBudgetSurvivesWhileAProviderRemains(t *testing.T) {
	t.Parallel()

	const (
		// one empty peer per unit of the origin error budget
		ordinary = 32
		// more refusals than that, so the two behaviours separate
		refusals = 40
	)

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		credits    atomic.Int32
	)

	// the provider is the furthest peer, so normal selection works through
	// every empty peer before it reaches the provider
	ring := peersByDistance(t, chunk.Address(), ordinary+1)
	emptyAddrs, providerAddr := ring[:ordinary], ring[ordinary]

	st := &testStorer{ChunkStore: inmemchunkstore.New()}
	if err := st.Put(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	provider := createRetrieval(t, providerAddr, st, nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	provider.SetProvidersEnabled(true)

	specs := failingPeers(t, emptyAddrs, 0)
	specs[providerAddr.String()] = provider.Protocol()

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(specs),
	)

	var acc *accountingmock.Service
	acc = accountingmock.NewAccounting(
		accountingmock.WithPrepareCreditFunc(func(peer swarm.Address, price uint64, originated bool) (accounting.Action, error) {
			if peer.Equal(providerAddr) && credits.Add(1) <= refusals {
				return nil, accounting.ErrOverdraft
			}
			return acc.MakeCreditAction(peer, price), nil
		}),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(ring...)), log.Noop, acc, pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(providerAddr))

	got, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	if err != nil {
		t.Fatalf("the budget was spent on ordinary misses while the provider was still a candidate: %v", err)
	}
	if !got.Address().Equal(chunk.Address()) {
		t.Fatalf("got chunk %s, want %s", got.Address(), chunk.Address())
	}
	if n := credits.Load(); int(n) <= refusals {
		t.Fatalf("the provider was asked for credit %d times, want more than the %d refusals", n, refusals)
	}
}
