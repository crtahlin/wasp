// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package inmemchunkstore_test

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/storage"
	inmem "github.com/ethersphere/bee/v2/pkg/storage/inmemchunkstore"
	"github.com/ethersphere/bee/v2/pkg/storage/storagetest"
)

func TestChunkStore(t *testing.T) {
	t.Parallel()

	storagetest.TestChunkStore(t, inmem.New())
}

func BenchmarkChunkStore(b *testing.B) {
	storagetest.RunChunkStoreBenchmarkTests(b, func(*testing.B) storage.ChunkStore {
		return inmem.New()
	})
}
