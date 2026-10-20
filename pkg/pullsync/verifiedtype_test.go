// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pullsync_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/ethersphere/bee/v2/pkg/p2p/streamtest"
	"github.com/ethersphere/bee/v2/pkg/storage"
	testingc "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/storer"
	mock "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// TestIncoming_VerifiedChunkType checks that pullsync stores each delivered
// chunk through the verified path with the type its own validation found,
// and that the type agrees with storage.ChunkType (wasp #810).
func TestIncoming_VerifiedChunkType(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tChunks := make([]swarm.Chunk, 0, 6)
		for i := range 6 {
			ch := testingc.GenerateTestRandomChunk()
			if i%2 == 1 {
				ch = testingc.GenerateTestRandomSoChunk(t, ch)
			}
			tChunks = append(tChunks, ch)
		}
		tResults := make([]*storer.BinC, len(tChunks))
		for i, c := range tChunks {
			stampHash, err := c.Stamp().Hash()
			if err != nil {
				t.Fatal(err)
			}
			tResults[i] = &storer.BinC{
				Address:   c.Address(),
				BatchID:   c.Stamp().BatchID(),
				BinID:     uint64(i + 1),
				StampHash: stampHash,
			}
		}

		var (
			ps, _              = newPullSync(t, nil, 10, mock.WithSubscribeResp(tResults, nil), mock.WithChunks(tChunks...))
			recorder           = streamtest.New(streamtest.WithProtocols(ps.Protocol()))
			psClient, clientDb = newPullSync(t, recorder, 0)
		)

		if _, _, err := psClient.Sync(context.Background(), swarm.ZeroAddress, 0, 0); err != nil {
			t.Fatal(err)
		}

		haveChunks(t, clientDb, tChunks...)
		for i, c := range tChunks {
			want := swarm.ChunkTypeContentAddressed
			if i%2 == 1 {
				want = swarm.ChunkTypeSingleOwner
			}
			if computed := storage.ChunkType(c); computed != want {
				t.Fatalf("chunk %d: test chunk has type %v, want %v", i, computed, want)
			}
			got, ok := clientDb.VerifiedType(c.Address())
			if !ok {
				t.Fatalf("chunk %d: not stored through the verified path", i)
			}
			if got != want {
				t.Fatalf("chunk %d: verified type %v, want %v", i, got, want)
			}
		}
	})
}
