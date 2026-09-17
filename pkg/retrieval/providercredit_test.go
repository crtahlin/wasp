// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval_test

import (
	"context"
	"sync"
	"testing"

	accountingmock "github.com/ethersphere/bee/v2/pkg/accounting/mock"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/p2p"
	"github.com/ethersphere/bee/v2/pkg/p2p/protobuf"
	"github.com/ethersphere/bee/v2/pkg/p2p/streamtest"
	pricermock "github.com/ethersphere/bee/v2/pkg/pricer/mock"
	"github.com/ethersphere/bee/v2/pkg/retrieval"
	"github.com/ethersphere/bee/v2/pkg/retrieval/pb"
	"github.com/ethersphere/bee/v2/pkg/storage/inmemchunkstore"
	testingc "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// grantRecorder records every grant the handler asks for.
type grantRecorder struct {
	mu    sync.Mutex
	peers []swarm.Address
	full  []bool
}

func (g *grantRecorder) GrantProviderCredit(peer swarm.Address, fullNode bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.peers = append(g.peers, peer)
	g.full = append(g.full, fullNode)
}

func (g *grantRecorder) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.peers)
}

func (g *grantRecorder) fullNodeAt(i int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.full[i]
}

// serveWithHeaders runs one retrieval against a holder that has the chunk, with
// the given stream headers, and returns what the holder's grant recorder saw.
func serveWithHeaders(t *testing.T, providersOn bool, headers p2p.Headers) *grantRecorder {
	t.Helper()

	chunk := testingc.FixtureChunk("0033")
	clientAddr := swarm.RandAddress(t)
	holderAddr := swarm.RandAddress(t)
	pricer := pricermock.NewMockService(defaultPrice, defaultPrice)

	st := &testStorer{ChunkStore: inmemchunkstore.New()}
	if err := st.Put(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}

	grants := &grantRecorder{}
	holder := createRetrieval(t, holderAddr, st, nil, nil, log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	holder.SetProvidersEnabled(providersOn)
	holder.SetProviderCreditor(grants)

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
			holderAddr.String(): holder.Protocol(),
		}),
	)

	// Drive the holder's handler through a stream carrying the headers, which
	// is what the preferred path does. Going through a full preferred-set
	// download would exercise the same hook with much more machinery in the
	// way.
	stream, err := recorder.NewStream(context.Background(), holderAddr, headers, "retrieval", "1.4.0", "retrieval")
	if err != nil {
		t.Fatal(err)
	}
	w := protobuf.NewWriter(stream)
	if err := w.WriteMsgWithContext(context.Background(), &pb.Request{Addr: chunk.Address().Bytes()}); err != nil {
		t.Fatal(err)
	}

	// The handler runs on the other side of the recorder, so wait for its
	// answer rather than racing it.
	r := protobuf.NewReader(stream)
	var d pb.Delivery
	if err := r.ReadMsgWithContext(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Err != "" {
		t.Fatalf("the holder refused the request: %s", d.Err)
	}
	_ = stream.FullClose()

	return grants
}

// TestProviderCreditGrantedOnLocalOnlyHit is the hook this change adds. The
// handler read the local-only header only on a miss, and the hit path is where
// a provider actually serves and credit is consumed.
func TestProviderCreditGrantedOnLocalOnlyHit(t *testing.T) {
	t.Parallel()

	grants := serveWithHeaders(t, true, p2p.Headers{retrieval.LocalOnlyHeader: []byte{1}})
	if n := grants.count(); n != 1 {
		t.Fatalf("%d grants for one local-only hit, want 1", n)
	}
}

// TestProviderCreditNotGrantedOnOrdinaryHit. An ordinary retrieval is not a
// provider request and must buy nothing.
func TestProviderCreditNotGrantedOnOrdinaryHit(t *testing.T) {
	t.Parallel()

	grants := serveWithHeaders(t, true, nil)
	if n := grants.count(); n != 0 {
		t.Fatalf("an ordinary hit granted credit %d times, want 0", n)
	}
}

// TestProviderCreditNeedsProvidersEnabled. The feature gate is off by default
// and everything hangs off it.
func TestProviderCreditNeedsProvidersEnabled(t *testing.T) {
	t.Parallel()

	grants := serveWithHeaders(t, false, p2p.Headers{retrieval.LocalOnlyHeader: []byte{1}})
	if n := grants.count(); n != 0 {
		t.Fatalf("credit was granted with providers-enable off, %d times", n)
	}
}

// TestProviderCreditPassesNodeType. The grant decision must come from the
// request rather than from the accounting record, because Connect sets that
// field from a goroutine and may not have run when the first request arrives.
// This checks the value reaches accounting at all; the decision itself is
// tested in pkg/accounting.
func TestProviderCreditPassesNodeType(t *testing.T) {
	t.Parallel()

	grants := serveWithHeaders(t, true, p2p.Headers{retrieval.LocalOnlyHeader: []byte{1}})
	if grants.count() != 1 {
		t.Fatalf("%d grants, want 1", grants.count())
	}
	if !grants.fullNodeAt(0) {
		t.Fatal("the handler reported the peer as a light node; the grant decision would be skipped for a full node")
	}
}

// TestProviderCreditNotGrantedOnLocalOnlyMiss. A miss is answered before the
// grant is reached, so the miss path cannot buy credit. Without that, any peer
// could ask for chunks this node does not have and still be granted.
func TestProviderCreditNotGrantedOnLocalOnlyMiss(t *testing.T) {
	t.Parallel()

	clientAddr := swarm.RandAddress(t)
	holderAddr := swarm.RandAddress(t)
	pricer := pricermock.NewMockService(defaultPrice, defaultPrice)

	// The holder is given an EMPTY store, so the request is a miss.
	grants := &grantRecorder{}
	holder := createRetrieval(t, holderAddr, &testStorer{ChunkStore: inmemchunkstore.New()}, nil, nil,
		log.Noop, accountingmock.NewAccounting(), pricer, nil, false)
	holder.SetProvidersEnabled(true)
	holder.SetProviderCreditor(grants)

	recorder := streamtest.New(
		streamtest.WithBaseAddr(clientAddr),
		streamtest.WithPeerProtocols(map[string]p2p.ProtocolSpec{
			holderAddr.String(): holder.Protocol(),
		}),
	)

	chunk := testingc.FixtureChunk("0033")
	stream, err := recorder.NewStream(context.Background(), holderAddr,
		p2p.Headers{retrieval.LocalOnlyHeader: []byte{1}}, "retrieval", "1.4.0", "retrieval")
	if err != nil {
		t.Fatal(err)
	}
	w := protobuf.NewWriter(stream)
	if err := w.WriteMsgWithContext(context.Background(), &pb.Request{Addr: chunk.Address().Bytes()}); err != nil {
		t.Fatal(err)
	}
	// The holder answers the miss and resets the stream, so a read error here
	// is the expected outcome rather than a failure.
	r := protobuf.NewReader(stream)
	var d pb.Delivery
	_ = r.ReadMsgWithContext(context.Background(), &d)
	_ = stream.FullClose()

	if n := grants.count(); n != 0 {
		t.Fatalf("a local-only MISS granted credit %d times; the miss path must not buy credit", n)
	}
}
