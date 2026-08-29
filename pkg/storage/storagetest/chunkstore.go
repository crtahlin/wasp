// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storagetest

import (
	"context"
	"errors"
	"testing"

	postagetesting "github.com/ethersphere/bee/v2/pkg/postage/testing"
	storage "github.com/ethersphere/bee/v2/pkg/storage"
	chunktest "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// TestChunkStore runs a correctness test suite on a given ChunkStore.
func TestChunkStore(t *testing.T, st storage.ChunkStore) {
	t.Helper()

	testChunks := chunktest.GenerateTestRandomChunks(50)

	t.Run("put chunks", func(t *testing.T) {
		for _, ch := range testChunks {
			err := st.Put(context.TODO(), ch)
			if err != nil {
				t.Fatalf("failed putting new chunk: %v", err)
			}
		}
	})

	t.Run("put existing chunks", func(t *testing.T) {
		for _, ch := range testChunks {
			err := st.Put(context.TODO(), ch)
			if err != nil {
				t.Fatalf("failed putting new chunk: %v", err)
			}
		}
	})

	t.Run("get chunks", func(t *testing.T) {
		for _, ch := range testChunks {
			readCh, err := st.Get(context.TODO(), ch.Address())
			if err != nil {
				t.Fatalf("failed getting chunk: %v", err)
			}
			if !readCh.Equal(ch) {
				t.Fatal("read chunk doesn't match")
			}
		}
	})

	t.Run("get non-existing chunk", func(t *testing.T) {
		stamp := postagetesting.MustNewStamp()
		ch := chunktest.GenerateTestRandomChunk().WithStamp(stamp)

		_, err := st.Get(context.TODO(), ch.Address())
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("expected error %v", storage.ErrNotFound)
		}
	})

	t.Run("has chunks", func(t *testing.T) {
		for _, ch := range testChunks {
			exists, err := st.Has(context.TODO(), ch.Address())
			if err != nil {
				t.Fatalf("failed getting chunk: %v", err)
			}
			if !exists {
				t.Fatalf("chunk not found: %s", ch.Address())
			}
		}
	})

	t.Run("iterate chunks", func(t *testing.T) {
		count := 0
		err := st.Iterate(context.TODO(), func(_ swarm.Chunk) (bool, error) {
			count++
			return false, nil
		})
		if err != nil {
			t.Fatalf("unexpected error while iteration: %v", err)
		}
		if count != 50 {
			t.Fatalf("unexpected no of chunks, exp: %d, found: %d", 50, count)
		}
	})

	t.Run("delete chunks", func(t *testing.T) {
		for idx, ch := range testChunks {
			// Delete all even numbered indexes along with 0
			if idx%2 == 0 {
				err := st.Delete(context.TODO(), ch.Address())
				if err != nil {
					t.Fatalf("failed deleting chunk: %v", err)
				}
				_, err = st.Get(context.TODO(), ch.Address())
				if err != nil {
					t.Fatalf("expected no error, found: %v", err)
				}
				// delete twice as it was put twice
				err = st.Delete(context.TODO(), ch.Address())
				if err != nil {
					t.Fatalf("failed deleting chunk: %v", err)
				}
			}
		}
	})

	t.Run("check deleted chunks", func(t *testing.T) {
		for idx, ch := range testChunks {
			if idx%2 == 0 {
				// Check even numbered indexes are deleted
				_, err := st.Get(context.TODO(), ch.Address())
				if !errors.Is(err, storage.ErrNotFound) {
					t.Fatalf("expected storage not found error found: %v", err)
				}
				found, err := st.Has(context.TODO(), ch.Address())
				if err != nil {
					t.Fatalf("unexpected error in Has: %v", err)
				}
				if found {
					t.Fatal("expected chunk to not be found")
				}
			} else {
				// Check rest of the entries are intact
				readCh, err := st.Get(context.TODO(), ch.Address())
				if err != nil {
					t.Fatalf("failed getting chunk: %v", err)
				}
				if !readCh.Equal(ch) {
					t.Fatal("read chunk doesn't match")
				}
				exists, err := st.Has(context.TODO(), ch.Address())
				if err != nil {
					t.Fatalf("failed getting chunk: %v", err)
				}
				if !exists {
					t.Fatalf("chunk not found: %s", ch.Address())
				}
			}
		}
	})

	t.Run("iterate chunks after delete", func(t *testing.T) {
		count := 0
		err := st.Iterate(context.TODO(), func(_ swarm.Chunk) (bool, error) {
			count++
			return false, nil
		})
		if err != nil {
			t.Fatalf("unexpected error while iteration: %v", err)
		}
		if count != 25 {
			t.Fatalf("unexpected no of chunks, exp: %d, found: %d", 25, count)
		}
	})
}

// RunChunkStoreBenchmarkTests provides a benchmark suite for a
// storage.ChunkStore.
//
// newStore is a constructor rather than a store, so that each sub-benchmark
// gets one of its own. Sharing a single store made every number depend on
// which other sub-benchmarks the -bench selection happened to run first, which
// is how issue #146 produced a reading that would not reproduce.
func RunChunkStoreBenchmarkTests(b *testing.B, newStore func(b *testing.B) storage.ChunkStore) {
	b.Helper()

	b.Run("WriteSequential", func(b *testing.B) {
		BenchmarkChunkStoreWriteSequential(b, newStore(b))
	})
	b.Run("WriteRandom", func(b *testing.B) {
		BenchmarkChunkStoreWriteRandom(b, newStore(b))
	})
	b.Run("ReadSequential", func(b *testing.B) {
		BenchmarkChunkStoreReadSequential(b, newStore(b))
	})
	b.Run("ReadRandom", func(b *testing.B) {
		BenchmarkChunkStoreReadRandom(b, newStore(b))
	})
	b.Run("ReadRandomMissing", func(b *testing.B) {
		BenchmarkChunkStoreReadRandomMissing(b, newStore(b))
	})
	b.Run("ReadReverse", func(b *testing.B) {
		BenchmarkChunkStoreReadReverse(b, newStore(b))
	})
	b.Run("ReadRedHot", func(b *testing.B) {
		BenchmarkChunkStoreReadHot(b, newStore(b))
	})
	b.Run("IterateSequential", func(b *testing.B) {
		BenchmarkChunkStoreIterateSequential(b, newStore(b))
	})
	b.Run("IterateReverse", func(b *testing.B) {
		BenchmarkChunkStoreIterateReverse(b, newStore(b))
	})
	b.Run("DeleteRandom", func(b *testing.B) {
		BenchmarkChunkStoreDeleteRandom(b, newStore(b))
	})
	b.Run("DeleteSequential", func(b *testing.B) {
		BenchmarkChunkStoreDeleteSequential(b, newStore(b))
	})
}

// The generators below are sized from *datasetSize rather than b.N. Before the
// loop runs, b.N reads as 1, so a generator sized from it holds a single key
// and every operation would hit the same address.

func BenchmarkChunkStoreWriteSequential(b *testing.B, s storage.Putter) {
	b.Helper()

	eb := newEntryBlocks(b, newSequentialEntryGenerator, nil)
	resetBenchmark(b)
	doWriteChunk(b, s, eb)
}

func BenchmarkChunkStoreWriteRandom(b *testing.B, s storage.Putter) {
	b.Helper()

	eb := newEntryBlocks(b, newFullRandomEntryGenerator, nil)
	resetBenchmark(b)
	doWriteChunk(b, s, eb)
}

func BenchmarkChunkStoreReadSequential(b *testing.B, s storage.ChunkStore) {
	b.Helper()

	populateChunks(b, s)
	g := newRoundKeyGenerator(newSequentialKeyGenerator(*datasetSize))
	resetBenchmark(b)
	doReadChunk(b, s, g, false)
}

func BenchmarkChunkStoreReadRandom(b *testing.B, s storage.ChunkStore) {
	b.Helper()

	populateChunks(b, s)
	g := newRoundKeyGenerator(newRandomKeyGenerator(*datasetSize))
	resetBenchmark(b)
	doReadChunk(b, s, g, false)
}

func BenchmarkChunkStoreReadRandomMissing(b *testing.B, s storage.ChunkStore) {
	b.Helper()

	// Populated even though every address looked up is absent from it. A miss
	// in an empty store is a different operation with a different cost, and it
	// is not the one this benchmark is named for.
	populateChunks(b, s)
	g := newRoundKeyGenerator(newRandomMissingKeyGenerator(*datasetSize))
	resetBenchmark(b)
	doReadChunk(b, s, g, true)
}

func BenchmarkChunkStoreReadReverse(b *testing.B, db storage.ChunkStore) {
	b.Helper()

	populateChunks(b, db)
	g := newRoundKeyGenerator(newReversedKeyGenerator(newSequentialKeyGenerator(*datasetSize)))
	resetBenchmark(b)
	doReadChunk(b, db, g, false)
}

func BenchmarkChunkStoreReadHot(b *testing.B, s storage.ChunkStore) {
	b.Helper()

	populateChunks(b, s)
	k := maxInt(*datasetSize/100, 1)
	g := newRoundKeyGenerator(newRandomKeyGenerator(k))
	resetBenchmark(b)
	doReadChunk(b, s, g, false)
}

func BenchmarkChunkStoreIterateSequential(b *testing.B, s storage.ChunkStore) {
	b.Helper()

	populateChunks(b, s)
	resetBenchmark(b)

	var counter int
	_ = s.Iterate(context.Background(), func(c swarm.Chunk) (stop bool, err error) {
		counter++
		if counter > b.N {
			return true, nil
		}
		return false, nil
	})
	if counter == 0 {
		b.Fatal("iterate visited no chunks, so this benchmark timed an empty store")
	}
}

func BenchmarkChunkStoreIterateReverse(b *testing.B, s storage.ChunkStore) {
	b.Helper()

	b.Skip("not implemented")
}

func BenchmarkChunkStoreDeleteRandom(b *testing.B, s storage.ChunkStore) {
	b.Helper()

	eb := newEntryBlocks(b, newFullRandomEntryGenerator, func(g entryGenerator) {
		writeChunkDataset(b, s, g)
	})
	resetBenchmark(b)
	doDeleteChunk(b, s, eb)
}

func BenchmarkChunkStoreDeleteSequential(b *testing.B, s storage.ChunkStore) {
	b.Helper()

	eb := newEntryBlocks(b, newSequentialEntryGenerator, func(g entryGenerator) {
		writeChunkDataset(b, s, g)
	})
	resetBenchmark(b)
	doDeleteChunk(b, s, eb)
}
