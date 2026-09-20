// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval_test

import (
	"context"
	"testing"

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
