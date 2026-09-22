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

// maxOriginErrorsForTest mirrors maxOriginErrors in the package under test,
// which is unexported. A flight that is not holding the budget asks this many
// ordinary peers before giving up.
const maxOriginErrorsForTest = 32

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

	// Pin the count, do not just log it. Keeping the budget unspent keeps
	// ordinary selection walking, and how far it walks is the cost this change
	// adds; docs/DIFFERENCES.md quotes this number. An unasserted number is one
	// nothing catches going wrong, including the counting harness itself, which
	// depends on failingPeers sharing one spec across every address.
	if n := asked.Load(); int(n) != ordinary {
		t.Fatalf("asked %d of %d ordinary peers, want all of them: holding the budget should let the walk reach every peer, and the figure quoted in docs/DIFFERENCES.md depends on it", n, ordinary)
	}
}

// TestBudgetResumesOnceThePreferredPeerHasAnswered is the safety half, and it
// is the test that pins the decrement rather than the guard.
//
// Holding the budget must end when the provider does. If preferredInflight is
// incremented and never decremented, a hinted flight can never spend the budget
// again and walks the whole connected set on every chunk. Deleting the
// decrement passes every other test in this package, so without this one that
// half of the change is unpinned.
//
// The lag here is deliberately SHORTER than preferredWait. The provider answers
// before ordinary selection has started, so no budget is spent under it, and
// what the flight does afterwards is therefore a statement about the decrement
// alone. With the decrement the budget bounds the walk and the flight ends on
// it, with storage.ErrNotFound. Without it the walk runs to depletion and the
// flight ends with topology.ErrNotFound instead.
func TestBudgetResumesOnceThePreferredPeerHasAnswered(t *testing.T) {
	t.Parallel()

	const (
		ordinary = 40
		lag      = 100 * time.Millisecond
	)

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
		asked      atomic.Int32
	)

	ring := peersByDistance(t, chunk.Address(), ordinary+1)
	emptyAddrs, providerAddr := ring[:ordinary], ring[ordinary]

	// the provider holds nothing, so its answer is a miss
	provider := createRetrieval(t, providerAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, nil, nil,
		log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
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
	_, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("no peer had the chunk, so the retrieval should have failed")
	}
	// storage.ErrNotFound is the budget exit. topology.ErrNotFound, "no peer
	// found", is the depletion exit and means the budget was never spent again
	// after the provider answered.
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("ended with %v after %s, want %v: the error budget did not resume once the provider had answered",
			err, elapsed, storage.ErrNotFound)
	}
	if n := asked.Load(); int(n) >= ordinary {
		t.Fatalf("asked %d of %d ordinary peers, so the walk ran to depletion rather than stopping on the budget", n, ordinary)
	} else {
		t.Logf("ordinary peers asked after the provider answered: %d of %d", n, ordinary)
	}
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
	// Exactly the error budget, not merely fewer than all of them. A loose
	// bound here would let a large rise in outbound requests through unnoticed,
	// and this is the figure docs/DIFFERENCES.md compares the hinted case with.
	if n := asked.Load(); n != maxOriginErrorsForTest {
		t.Fatalf("asked %d of %d ordinary peers with no hint, want %d, the origin error budget", n, ordinary, maxOriginErrorsForTest)
	}
}
