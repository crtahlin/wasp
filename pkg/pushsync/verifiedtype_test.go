// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pushsync_test

import (
	"context"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/crypto"
	cryptomock "github.com/ethersphere/bee/v2/pkg/crypto/mock"
	"github.com/ethersphere/bee/v2/pkg/p2p/streamtest"
	"github.com/ethersphere/bee/v2/pkg/soc"
	"github.com/ethersphere/bee/v2/pkg/storage"
	testingc "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/topology"
	"github.com/ethersphere/bee/v2/pkg/topology/mock"
)

// TestHandlerStoresVerifiedChunkType checks that the pushsync handler stores
// a pushed chunk through the verified path with the type its own validation
// found, for a content addressed and a single owner chunk, and that the type
// agrees with storage.ChunkType (wasp #810).
func TestHandlerStoresVerifiedChunkType(t *testing.T) {
	t.Parallel()

	receiptSigner := cryptomock.New(cryptomock.WithSignFunc(func(addr []byte) ([]byte, error) {
		key, _ := crypto.GenerateSecp256k1Key()
		return crypto.NewDefaultSigner(key).Sign(addr)
	}))

	cac := testingc.FixtureChunk("7000")
	key, err := crypto.GenerateSecp256k1Key()
	if err != nil {
		t.Fatal(err)
	}
	sch, err := soc.New(make([]byte, swarm.HashSize), testingc.FixtureChunk("0033")).Sign(crypto.NewDefaultSigner(key))
	if err != nil {
		t.Fatal(err)
	}
	sch = sch.WithStamp(testingc.FixtureChunk("0033").Stamp())

	pivotNode := swarm.MustParseHexAddress("0000000000000000000000000000000000000000000000000000000000000000")
	closestPeer := swarm.MustParseHexAddress("8000000000000000000000000000000000000000000000000000000000000000")

	psPeer, peerStorer, _ := createGsocPushSyncNode(t, closestPeer, defaultPrices, nil, nil, receiptSigner, mock.WithClosestPeerErr(topology.ErrWantSelf))
	recorder := streamtest.New(streamtest.WithProtocols(psPeer.Protocol()), streamtest.WithBaseAddr(pivotNode))
	psPivot, _, _ := createGsocPushSyncNode(t, pivotNode, defaultPrices, recorder, nil, receiptSigner, mock.WithClosestPeer(closestPeer))

	for _, tc := range []struct {
		name  string
		chunk swarm.Chunk
		want  swarm.ChunkType
	}{
		{"content addressed", cac, swarm.ChunkTypeContentAddressed},
		{"single owner", sch, swarm.ChunkTypeSingleOwner},
	} {
		if computed := storage.ChunkType(tc.chunk); computed != tc.want {
			t.Fatalf("%s: test chunk has type %v, want %v", tc.name, computed, tc.want)
		}
		if _, err := psPivot.PushChunkToClosest(context.Background(), tc.chunk); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !peerStorer.hasChunk(t, tc.chunk.Address()) {
			t.Fatalf("%s: chunk not stored on the peer", tc.name)
		}
		got, ok := peerStorer.verifiedType(tc.chunk.Address())
		if !ok {
			t.Fatalf("%s: not stored through the verified path", tc.name)
		}
		if got != tc.want {
			t.Fatalf("%s: verified type %v, want %v", tc.name, got, tc.want)
		}
		recorder.Reset()
	}
}
