// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	accountingmock "github.com/ethersphere/bee/v2/pkg/accounting/mock"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/p2p"
	"github.com/ethersphere/bee/v2/pkg/p2p/streamtest"
	pricermock "github.com/ethersphere/bee/v2/pkg/pricer/mock"
	"github.com/ethersphere/bee/v2/pkg/retrieval"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/inmemchunkstore"
	testingc "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	topologymock "github.com/ethersphere/bee/v2/pkg/topology/mock"
)

// laggedSpec wraps a service's protocol so it answers only after lag. Used to
// hold a request open across the moment the error budget would have run out.
func laggedSpec(svc *retrieval.Service, lag time.Duration) p2p.ProtocolSpec {
	spec := svc.Protocol()
	inner := spec.StreamSpecs[0].Handler
	spec.StreamSpecs[0].Handler = func(ctx context.Context, p p2p.Peer, s p2p.Stream) error {
		select {
		case <-time.After(lag):
		case <-ctx.Done():
			// observe the context, or the goroutine outlives the test and
			// goleak fails the package on the test's own leak
			return ctx.Err()
		}
		return inner(ctx, p, s)
	}
	return spec
}

// countingPeers is failingPeers with a counter, so a test can say how many
// ordinary peers a single flight asked.
func countingPeers(t *testing.T, addrs []swarm.Address, n *atomic.Int32) map[string]p2p.ProtocolSpec {
	t.Helper()

	specs := failingPeers(t, addrs, 0)

	// Every address shares one spec value, and its StreamSpecs slice shares one
	// backing array, so the handler must be wrapped exactly once. Wrapping per
	// map entry nests the counter once per peer and multiplies every count by
	// len(addrs), which reads as a plausible number and is not one.
	for _, spec := range specs {
		inner := spec.StreamSpecs[0].Handler
		spec.StreamSpecs[0].Handler = func(ctx context.Context, p p2p.Peer, s p2p.Stream) error {
			n.Add(1)
			return inner(ctx, p, s)
		}
		break
	}
	return specs
}

// TestPreferredDeliveryOutlivesTheErrorBudget is the defect in #438.
//
// A preferred peer is removed from the candidate list as soon as it is
// dispatched (retrieval.go:353), so ordinary misses resume spending the origin
// error budget while it is still answering. When the budget reaches zero the
// flight returns storage.ErrNotFound and the deferred close(quit) discards the
// delivery that was on its way.
//
// The provider here is the only peer that has the chunk and it answers after a
// lag longer than the budget takes to spend. Before the fix this failed in
// about 502 ms, which is preferredWait plus a sweep of the ordinary peers.
//
// There are deliberately more ordinary peers than maxOriginErrors, so that
// spending the budget and running out of peers are separate events. At exactly
// maxOriginErrors they coincide and the test no longer says which one ended the
// flight.
func TestPreferredDeliveryOutlivesTheErrorBudget(t *testing.T) {
	t.Parallel()

	const (
		ordinary = 40
		lag      = time.Second
	)

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		asked      atomic.Int32
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

	specs := countingPeers(t, emptyAddrs, &asked)
	specs[providerAddr.String()] = laggedSpec(provider, lag)

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
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(providerAddr))

	start := time.Now()
	got, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("the flight gave up after %s while the only peer holding the chunk was still answering: %v", elapsed, err)
	}
	if !got.Address().Equal(chunk.Address()) {
		t.Fatalf("got chunk %s, want %s", got.Address(), chunk.Address())
	}
	if elapsed < lag {
		t.Fatalf("returned after %s, before the provider could have answered at %s", elapsed, lag)
	}

	// The count is reported rather than bounded. Keeping the budget unspent
	// keeps ordinary selection walking, and how far it walks is the cost this
	// change adds. A bound here would be a guess; a number in the log is what a
	// later reader needs when the bench arm sizes it. See the spec.
	t.Logf("ordinary peers asked during the flight: %d of %d", asked.Load(), ordinary)
}

// TestFlightEndsWhenThePreferredPeerMisses is the other half. Holding the
// budget while a provider answers must not stop a flight ending once it has
// answered, or a hint would keep a hopeless search alive.
//
// The provider does not hold the chunk and says so after a lag. The flight must
// wait for that answer, then spend the budget on the ordinary peers as usual
// and fail.
func TestFlightEndsWhenThePreferredPeerMisses(t *testing.T) {
	t.Parallel()

	const (
		ordinary = 40
		// longer than preferredWait, so ordinary selection starts and spends
		// the budget while the provider is still quiet. At a shorter lag the
		// provider answers before the budget is touched and the test passes
		// with or without the change, proving nothing.
		lag = time.Second
	)

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
	)

	ring := peersByDistance(t, chunk.Address(), ordinary+1)
	emptyAddrs, providerAddr := ring[:ordinary], ring[ordinary]

	// the provider holds nothing, so its answer is a miss
	provider := createRetrieval(t, providerAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, nil, nil,
		log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	provider.SetProvidersEnabled(true)

	specs := failingPeers(t, emptyAddrs, 0)
	specs[providerAddr.String()] = laggedSpec(provider, lag)

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
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(providerAddr))

	start := time.Now()
	_, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("no peer had the chunk, so the retrieval should have failed")
	}
	if elapsed < lag {
		t.Fatalf("failed after %s, before the provider answered at %s, so the budget was spent under it", elapsed, lag)
	}
	t.Logf("ended after %s with %v", elapsed, err)
}

// TestBudgetUnchangedWithoutAPreferredPeer pins the claim that the change is
// inert for a download that names no provider. With no preferred set the guard
// must reduce to the one that was there before, and the flight must still give
// up on the error budget rather than walking every peer.
//
// This is the test that fails if preferredInflight is ever moved outside the
// preferred dispatch.
func TestBudgetUnchangedWithoutAPreferredPeer(t *testing.T) {
	t.Parallel()

	const ordinary = 40

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		asked      atomic.Int32
	)

	ring := peersByDistance(t, chunk.Address(), ordinary)

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(countingPeers(t, ring, &asked)),
	)

	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, recorder,
		topologymock.NewTopologyDriver(topologymock.WithPeers(ring...)), log.Noop,
		accountingmock.NewAccounting(), pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("got error %v, want %v: without a hint the flight must still end on the error budget", err, storage.ErrNotFound)
	}

	// The budget is the bound here, so the walk stops well short of the peer
	// set. Asserting this is what catches the guard being made unconditional.
	n := asked.Load()
	if int(n) > ordinary-1 {
		t.Fatalf("asked %d of %d ordinary peers, so the error budget no longer bounds a hint-less flight", n, ordinary)
	}
	t.Logf("ordinary peers asked with no hint: %d of %d", n, ordinary)
}
