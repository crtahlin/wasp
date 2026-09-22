// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accountingmock "github.com/ethersphere/bee/v2/pkg/accounting/mock"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/p2p"
	"github.com/ethersphere/bee/v2/pkg/p2p/streamtest"
	pricermock "github.com/ethersphere/bee/v2/pkg/pricer/mock"
	"github.com/ethersphere/bee/v2/pkg/retrieval"
	"github.com/ethersphere/bee/v2/pkg/storage/inmemchunkstore"
	testingc "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	topologymock "github.com/ethersphere/bee/v2/pkg/topology/mock"
)

// onNthRequest runs do once, when the ordinary peers have been asked n times.
//
// Timers were the obvious way to say "after the flight has started" and they
// are the wrong one: the flight spends its error budget as fast as the peers
// answer, so a wall-clock delay races the walk and the test either passes for
// the wrong reason or flakes. Counting requests makes the moment deterministic.
func onNthRequest(specs map[string]p2p.ProtocolSpec, n int32, do func()) {
	var (
		seen atomic.Int32
		once sync.Once
	)

	// Every address shares one spec value and one StreamSpecs backing array,
	// so the handler is wrapped exactly once. Wrapping per map entry would
	// nest the counter once per peer, which is the trap countingPeers records.
	for _, spec := range specs {
		inner := spec.StreamSpecs[0].Handler
		spec.StreamSpecs[0].Handler = func(ctx context.Context, p p2p.Peer, s p2p.Stream) error {
			if seen.Add(1) >= n {
				once.Do(do)
			}
			return inner(ctx, p, s)
		}
		break
	}
}

// TestProviderConnectingLateStillServesTheChunk is wasp #435.
//
// A chunk's preferred candidate list is built once, before the retry loop, and
// keeps only peers already connected in Kademlia. A hinted download starts its
// first chunk's flight before the dial the same request began has landed,
// measured on the bench at 0.38 s against a 2.34 s request, so that list is
// empty and stays empty for the chunk's whole retry budget. For sole-source
// content the first chunk is the root, so the download returns nothing while
// the provider sits connected and holding it.
//
// Here the provider holds the chunk and nobody else does, and it enters the
// connected set only after the walk has begun. Without the rebuild the flight
// never asks it and fails.
func TestProviderConnectingLateStillServesTheChunk(t *testing.T) {
	t.Parallel()

	const ordinary = 40

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
	)

	ring := peersByDistance(t, chunk.Address(), ordinary+1)
	emptyAddrs, providerAddr := ring[:ordinary], ring[ordinary]

	st := &testStorer{ChunkStore: inmemchunkstore.New()}
	if err := st.Put(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	provider := createRetrieval(t, providerAddr, st, nil, nil, log.Noop,
		accountingmock.NewAccounting(), pricer, nil, false)
	provider.SetProvidersEnabled(true)

	// The provider is deliberately absent from the topology, so the flight
	// starts with an empty candidate list exactly as a hinted download does
	// before its dial lands.
	driver := topologymock.NewTopologyDriver(topologymock.WithPeers(emptyAddrs...))

	// onNthRequest must wrap the ORDINARY peers' shared spec, so it is applied
	// before the provider's own spec joins the map. Map iteration order is
	// random, so wrapping afterwards picks an arbitrary entry and the counter
	// silently attaches to the provider instead, which made this test flake.
	specs := failingPeers(t, emptyAddrs, 0)
	onNthRequest(specs, 2, func() { driver.AddPeers(providerAddr) })
	specs[providerAddr.String()] = provider.Protocol()

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(specs),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		driver, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(providerAddr))

	got, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	if err != nil {
		t.Fatalf("the flight never asked the only peer holding the chunk, which was connected before it gave up: %v", err)
	}
	if !got.Address().Equal(chunk.Address()) {
		t.Fatalf("got chunk %s, want %s", got.Address(), chunk.Address())
	}
}

// TestProviderDiscoveredMidFlightStillServesTheChunk is the discovery half.
//
// Discover adds providers to a download's preferred set part way through, by
// design. preferredPeers is a snapshot taken before the flight, so a rebuild
// from it cannot see them; this is what makes reading preferredSet.Peers()
// rather than the snapshot load bearing, and it is the assertion that fails if
// someone rebuilds from the cheaper value.
//
// The provider is connected from the start here. Only its membership of the
// preferred set changes, which is the one variable under test.
func TestProviderDiscoveredMidFlightStillServesTheChunk(t *testing.T) {
	t.Parallel()

	const ordinary = 40

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
	)

	ring := peersByDistance(t, chunk.Address(), ordinary+1)
	emptyAddrs, providerAddr := ring[:ordinary], ring[ordinary]

	st := &testStorer{ChunkStore: inmemchunkstore.New()}
	if err := st.Put(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	provider := createRetrieval(t, providerAddr, st, nil, nil, log.Noop,
		accountingmock.NewAccounting(), pricer, nil, false)
	provider.SetProvidersEnabled(true)

	// Empty at the start, which is what a download with no hint carries, and
	// what withProviders attaches to most origin requests.
	set := retrieval.NewPreferredSet()

	// Wrapped before the provider's spec joins the map, for the reason given
	// in the test above.
	specs := failingPeers(t, emptyAddrs, 0)
	onNthRequest(specs, 2, func() { set.Add(providerAddr) })
	specs[providerAddr.String()] = provider.Protocol()

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(specs),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(ring...)), log.Noop,
		accountingmock.NewAccounting(), pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, set)

	got, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	if err != nil {
		t.Fatalf("a provider added to the set during the flight was never asked: %v", err)
	}
	if !got.Address().Equal(chunk.Address()) {
		t.Fatalf("got chunk %s, want %s", got.Address(), chunk.Address())
	}
}

// TestRebuildDoesNotOfferTheSamePeerTwice is the termination guard.
//
// A preferred miss returns through the res.preferred arm before both
// errorsLeft-- and errSkip.Add, so it spends no error budget and is recorded in
// neither of those lists. A rebuild that could re-offer a peer it has already
// tried would therefore loop with no exit but the request context, which is
// why the earlier design for this issue was withdrawn.
//
// The provider is connected and in the set throughout and does NOT hold the
// chunk, so every attempt misses. The flight must still end, and must ask it
// once, not once per rebuild.
func TestRebuildDoesNotOfferTheSamePeerTwice(t *testing.T) {
	t.Parallel()

	const ordinary = 40

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		asked      atomic.Int32
	)

	ring := peersByDistance(t, chunk.Address(), ordinary+1)
	emptyAddrs, providerAddr := ring[:ordinary], ring[ordinary]

	// The provider holds nothing, like every other peer here.
	provider := createRetrieval(t, providerAddr, &testStorer{ChunkStore: inmemchunkstore.New()},
		nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	provider.SetProvidersEnabled(true)

	specs := failingPeers(t, emptyAddrs, 0)
	providerSpec := provider.Protocol()
	inner := providerSpec.StreamSpecs[0].Handler
	providerSpec.StreamSpecs[0].Handler = func(ctx context.Context, p p2p.Peer, s p2p.Stream) error {
		asked.Add(1)
		return inner(ctx, p, s)
	}
	specs[providerAddr.String()] = providerSpec

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(specs),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(ring...)), log.Noop,
		accountingmock.NewAccounting(), pricer, nil, false)
	client.SetProvidersEnabled(true)

	// Short enough that a loop shows up as a timeout rather than as a test
	// that runs until the package deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(providerAddr))

	_, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	if err == nil {
		t.Fatal("a chunk nobody holds was retrieved")
	}
	if ctx.Err() != nil {
		t.Fatal("the flight did not end on its own: the rebuild kept re-offering a peer it had already tried")
	}

	// One dispatch, not one per rebuild. skip.Forever records a dispatched
	// peer before it goes out, and the offered set keeps it out regardless, so
	// anything above one means both guards have gone.
	if n := asked.Load(); n != 1 {
		t.Fatalf("asked the provider %d times, want exactly once", n)
	}
}

// TestRebuildDoesNotRaiseThePerChunkCap is the cost guard, and it is the test
// this change most needed.
//
// maxPreferredAttempts bounds the FIRST build of the candidate list, because
// preferredCandidates truncates to it. Rebuilding until the preferred set runs
// out quietly turns that bound into "however many providers are known":
// measured at six preferred attempts for a six-peer set against two without
// the rebuild. Every one of those is an outbound local-only request that the
// PROVIDER pays for, in a handler invocation, a miss-limiter slot and a debit
// attempt, which is the cost rule 8 asks to be stated for the operator who is
// not the one choosing it.
//
// Nobody holds the chunk, so every attempt misses and the flight keeps trying
// until its guards stop it. The cap is what must stop it.
func TestRebuildDoesNotRaiseThePerChunkCap(t *testing.T) {
	t.Parallel()

	const (
		ordinary  = 40
		providers = 6
	)

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		asked      atomic.Int32
	)

	ring := peersByDistance(t, chunk.Address(), ordinary+providers)
	emptyAddrs, providerAddrs := ring[:ordinary], ring[ordinary:]

	specs := failingPeers(t, emptyAddrs, 0)
	for _, pa := range providerAddrs {
		p := createRetrieval(t, pa, &testStorer{ChunkStore: inmemchunkstore.New()},
			nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
		p.SetProvidersEnabled(true)

		spec := p.Protocol()
		inner := spec.StreamSpecs[0].Handler
		spec.StreamSpecs[0].Handler = func(ctx context.Context, peer p2p.Peer, s p2p.Stream) error {
			asked.Add(1)
			return inner(ctx, peer, s)
		}
		specs[pa.String()] = spec
	}

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(specs),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(ring...)), log.Noop,
		accountingmock.NewAccounting(), pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(providerAddrs...))

	if _, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress); err == nil {
		t.Fatal("a chunk nobody holds was retrieved")
	}
	if ctx.Err() != nil {
		t.Fatal("the flight did not end on its own")
	}

	if n := int(asked.Load()); n > retrieval.MaxPreferredAttemptsForTest {
		t.Fatalf("asked %d providers for one chunk, want at most %d: the rebuild must not raise "+
			"the per-chunk cap, because every extra attempt is an outbound request the provider pays for",
			n, retrieval.MaxPreferredAttemptsForTest)
	}
}

// TestRebuildCounterCountsRebuildsNotFlights pins what the counter means.
//
// Its Help string said "flights" once, and it is not: one flight can raise it
// more than once, up to the per-chunk cap. An operator dividing it by a flight
// count would get a ratio above one and conclude something was wrong.
func TestRebuildCounterCountsRebuildsNotFlights(t *testing.T) {
	t.Parallel()

	const ordinary = 40

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
	)

	ring := peersByDistance(t, chunk.Address(), ordinary+1)
	emptyAddrs, providerAddr := ring[:ordinary], ring[ordinary]

	st := &testStorer{ChunkStore: inmemchunkstore.New()}
	if err := st.Put(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	provider := createRetrieval(t, providerAddr, st, nil, nil, log.Noop,
		accountingmock.NewAccounting(), pricer, nil, false)
	provider.SetProvidersEnabled(true)

	driver := topologymock.NewTopologyDriver(topologymock.WithPeers(emptyAddrs...))

	specs := failingPeers(t, emptyAddrs, 0)
	onNthRequest(specs, 2, func() { driver.AddPeers(providerAddr) })
	specs[providerAddr.String()] = provider.Protocol()

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(specs),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		driver, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	client.SetProvidersEnabled(true)

	if before := client.PreferredRebuildsForTest(t); before != 0 {
		t.Fatalf("the counter started at %v, want 0", before)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(providerAddr))

	if _, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress); err != nil {
		t.Fatal(err)
	}

	// One flight, and the rebuild is what found the provider, so the counter
	// must have moved. Without this the increment can be deleted and nothing
	// notices.
	if got := client.PreferredRebuildsForTest(t); got < 1 {
		t.Fatalf("the counter reads %v after a flight the rebuild served, want at least 1", got)
	}
}

// TestNoPreferredSetLeavesTheWalkUnchanged is the guard for everybody else.
//
// withProviders attaches a preferred set to most origin downloads, usually
// empty, so the rebuild must cost an unhinted download nothing and must not
// change how far its walk goes. Not all: it returns early when providers are
// disabled, and /pins, /soc and the access-control paths reach retrieval as
// origin with no set, which is why the nil test in the loop is load bearing.
// A forwarder never reaches this path at all, because origin is false for it,
// and no test here exercises that.
func TestNoPreferredSetLeavesTheWalkUnchanged(t *testing.T) {
	t.Parallel()

	const ordinary = 40

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		asked      atomic.Int32
	)

	ring := peersByDistance(t, chunk.Address(), ordinary)
	specs := countingPeers(t, ring, &asked)

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(specs),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(ring...)), log.Noop,
		accountingmock.NewAccounting(), pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// An empty set, which is what every origin download carries when no hint
	// was given and discovery has found nothing.
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet())

	_, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	if err == nil {
		t.Fatal("a chunk nobody holds was retrieved")
	}
	if ctx.Err() != nil {
		t.Fatal("the flight did not end on its own")
	}

	// The error budget, not the peer count: with no preferred candidate ever
	// present the budget is never held, so the walk stops at maxOriginErrors
	// exactly as it does with no providers at all.
	if n := asked.Load(); int(n) != maxOriginErrorsForTest {
		t.Fatalf("asked %d peers, want %d: an empty preferred set must not change the walk",
			n, maxOriginErrorsForTest)
	}
}
