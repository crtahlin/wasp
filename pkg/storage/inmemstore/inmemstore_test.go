// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package inmemstore_test

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/storage"
	inmem "github.com/ethersphere/bee/v2/pkg/storage/inmemstore"
	"github.com/ethersphere/bee/v2/pkg/storage/storagetest"
)

func TestStore(t *testing.T) {
	t.Parallel()

	storagetest.TestStore(t, inmem.New())
}

// Each sub-benchmark gets a store of its own: sharing one made every
// benchmark's numbers depend on what the benchmarks before it had written, and
// so on which -bench selection was run. See issue #146.
func BenchmarkStore(b *testing.B) {
	storagetest.BenchmarkStore(b, func(*testing.B) storage.Store {
		return inmem.New()
	})
}

func TestBatchedStore(t *testing.T) {
	t.Parallel()

	storagetest.TestBatchedStore(t, inmem.New())
}

func BenchmarkBatchedStore(b *testing.B) {
	storagetest.BenchmarkBatchedStore(b, func(*testing.B) storage.BatchStore {
		return inmem.New()
	})
}
