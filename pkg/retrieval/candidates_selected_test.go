// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval_test

import (
	"context"
	"sync"
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

// TestPreferredCandidatesSelectedCountsOncePerFlight covers the counter issue
// #299 adds, which exists because no counter that was already there can answer
// whether the preferred set reached a fetch: PreferredAttempts increments only
// after prepareCredit succeeds and so is capped by credit, and attempts plus
// overdrafts is inflated by the #324 readmit path, which keeps a peer without
// consuming the candidate.
//
// It counts flights, not attempts, so the empty case is the one that matters:
// a flight with no candidate must not move it, or "the set arrived" and "the
// set was empty" read the same.
func TestPreferredCandidatesSelectedCountsOncePerFlight(t *testing.T) {
	t.Parallel()

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		holderAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
	)

	newClient := func(t *testing.T) *retrieval.Service {
		t.Helper()
		hst := &testStorer{ChunkStore: inmemchunkstore.New()}
		if err := hst.Put(context.Background(), chunk); err != nil {
			t.Fatal(err)
		}
		holder := createRetrieval(t, holderAddr, hst, nil, nil, log.Noop,
			accountingmock.NewAccounting(), pricer, nil, false)
		holder.SetProvidersEnabled(true)

		recorder := streamtest.New(
			streamtest.WithBaseAddr(clientAddr),
			streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
				holderAddr.String(): holder.Protocol(),
			}),
		)
		c := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()},
			recorder, topologymock.NewTopologyDriver(topologymock.WithPeers(holderAddr)),
			log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
		c.SetProvidersEnabled(true)
		return c
	}

	t.Run("a flight with a candidate counts once", func(t *testing.T) {
		t.Parallel()
		client := newClient(t)
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(holderAddr))

		if _, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress); err != nil {
			t.Fatal(err)
		}
		if got := client.PreferredCandidatesSelected(t); got != 1 {
			t.Fatalf("counter=%v, want 1", got)
		}
	})

	t.Run("a flight with an empty candidate list does not count", func(t *testing.T) {
		t.Parallel()
		client := newClient(t)
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		// a set holding a peer that is not connected: preferredCandidates
		// filters it out, so the list is empty and the flight is an ordinary one
		ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(swarm.RandAddress(t)))

		if _, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress); err != nil {
			t.Fatal(err)
		}
		if got := client.PreferredCandidatesSelected(t); got != 0 {
			t.Fatalf("counter=%v, want 0: an empty candidate list is not a preferred flight", got)
		}
	})

	t.Run("no set at all does not count", func(t *testing.T) {
		t.Parallel()
		client := newClient(t)
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		if _, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress); err != nil {
			t.Fatal(err)
		}
		if got := client.PreferredCandidatesSelected(t); got != 0 {
			t.Fatalf("counter=%v, want 0", got)
		}
	})
}

// TestPreferredCandidatesSelectedCountsOnceAcrossTwoAttempts is the half of the
// "once per flight" property that a single-attempt test cannot reach, and it
// took two goes to build.
//
// A second preferred attempt does not happen because the first peer lacks the
// chunk: a miss comes back quickly and the flight falls to ordinary selection.
// It happens when the first attempt STARTS and is then slow, so the
// preferredWait timer fires, the consumed candidate is gone and the second is
// tried. So the first preferred peer here holds the chunk and answers slower
// than preferredWait.
//
// One flight, two preferred attempts, and the counter must still read one.
// Without this, moving the increment into the per-attempt branch passes the
// whole suite, which is what it did before this test existed.
func TestPreferredCandidatesSelectedCountsOnceAcrossTwoAttempts(t *testing.T) {
	t.Parallel()

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		slowAddr   = swarm.RandAddress(t)
		fastAddr   = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
	)

	stop := newStop(t)
	newPeer := func(addr swarm.Address, delay time.Duration) *retrieval.Service {
		t.Helper()
		cs := inmemchunkstore.New()
		if err := cs.Put(context.Background(), chunk); err != nil {
			t.Fatal(err)
		}
		var st retrieval.Storer = &testStorer{ChunkStore: cs}
		if delay > 0 {
			st = &slowStorer{ChunkStore: cs, delay: delay, stop: stop}
		}
		svc := createRetrieval(t, addr, st, nil, nil, log.Noop,
			accountingmock.NewAccounting(), pricer, nil, false)
		svc.SetProvidersEnabled(true)
		return svc
	}
	// BOTH are slow, deliberately. preferredCandidates sorts by closeness to
	// the chunk, not by the order they were added, so with one fast peer the
	// flight can attempt that one first, get the chunk at once and never make a
	// second attempt. With both slower than preferredWait the timer fires
	// whichever is picked first, and the second candidate is always tried.
	slow := newPeer(slowAddr, time.Second)
	fast := newPeer(fastAddr, time.Second)

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
			slowAddr.String(): slow.Protocol(),
			fastAddr.String(): fast.Protocol(),
		}),
	)
	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()},
		recorder, topologymock.NewTopologyDriver(topologymock.WithPeers(slowAddr, fastAddr)),
		log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(slowAddr, fastAddr))

	if _, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress); err != nil {
		t.Fatal(err)
	}
	// both preferred peers were attempted inside one flight
	if got := client.PreferredAttemptsForTest(t); got < 2 {
		t.Fatalf("preferred attempts=%v, want at least 2: this test needs a flight that made two", got)
	}
	if got := client.PreferredCandidatesSelected(t); got != 1 {
		t.Fatalf("counter=%v, want 1: the counter is per flight, and this flight made two preferred attempts", got)
	}
}

// TestPreferredCandidatesSelectedCountsOncePerDeduplicatedFlight is the other
// half, and the one the spec names outright: two callers of the same chunk are
// deduplicated into one flight by singleflight, so the counter must move once
// rather than once per caller. Without it, hoisting the increment out of the
// singleflight closure passes the whole suite.
func TestPreferredCandidatesSelectedCountsOncePerDeduplicatedFlight(t *testing.T) {
	t.Parallel()

	var (
		chunk      = testingc.FixtureChunk("0033")
		clientAddr = swarm.RandAddress(t)
		holderAddr = swarm.RandAddress(t)
		pricer     = pricermock.NewMockService(defaultPrice, defaultPrice)
	)

	hcs := inmemchunkstore.New()
	if err := hcs.Put(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	// the holder answers slowly, so the second caller certainly arrives while
	// the first flight is still open and is deduplicated into it
	holder := createRetrieval(t, holderAddr, &slowStorer{ChunkStore: hcs, delay: 300 * time.Millisecond, stop: newStop(t)},
		nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	holder.SetProvidersEnabled(true)

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
			holderAddr.String(): holder.Protocol(),
		}),
	)
	client := createRetrieval(t, clientAddr, &testStorer{ChunkStore: inmemchunkstore.New()},
		recorder, topologymock.NewTopologyDriver(topologymock.WithPeers(holderAddr)),
		log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	client.SetProvidersEnabled(true)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ctx = retrieval.WithPreferredPeers(ctx, retrieval.NewPreferredSet(holderAddr))

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.RetrieveChunk(ctx, chunk.Address(), swarm.ZeroAddress)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if got := client.PreferredCandidatesSelected(t); got != 1 {
		t.Fatalf("counter=%v, want 1: two deduplicated callers are one flight", got)
	}
}

// slowStorer delays a read, so a preferred attempt outlives preferredWait and
// the flight starts the next candidate.
//
// It takes a stop channel as well as honouring the context, because the second
// preferred attempt is still in flight when the flight ends: the chunk arrived
// from the first peer, so nothing cancels the second, and goleak fails the test
// on the sleeping goroutine. Closing stop at cleanup releases it.
type slowStorer struct {
	storage.ChunkStore
	delay time.Duration
	stop  <-chan struct{}
}

func (s *slowStorer) Lookup() storage.Getter { return s }

func (s *slowStorer) Cache() storage.Putter { return s.ChunkStore }

func (s *slowStorer) Get(ctx context.Context, addr swarm.Address) (swarm.Chunk, error) {
	t := time.NewTimer(s.delay)
	defer t.Stop()
	select {
	case <-t.C:
	case <-s.stop:
		return nil, context.Canceled
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.ChunkStore.Get(ctx, addr)
}

// newStop returns a channel closed when the test finishes.
func newStop(t *testing.T) <-chan struct{} {
	t.Helper()
	ch := make(chan struct{})
	t.Cleanup(func() { close(ch) })
	return ch
}
