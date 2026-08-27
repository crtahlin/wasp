// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pebblestore_test

import (
	"os"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/pebblestore"
	"github.com/ethersphere/bee/v2/pkg/storage/storagetest"
)

// benchOptions lets one benchmark run be repeated with the bloom filter off.
//
// It is an environment variable rather than a second set of benchmarks under
// different names so that both runs report under the same benchmark names and
// can be compared directly, for instance with benchstat.
func benchOptions() *pebble.Options {
	if os.Getenv("PEBBLE_NO_FILTER") == "1" {
		return &pebble.Options{}
	}
	return nil
}

// newBenchStore gives one sub-benchmark a store of its own.
//
// The suite used to share a single store, which made each sub-benchmark's
// numbers a function of what the sub-benchmarks before it had written, and so
// of which -bench selection was run. See issue #146.
func newBenchStore(b *testing.B) *pebblestore.Store {
	b.Helper()

	store, err := pebblestore.New(b.TempDir(), benchOptions())
	if err != nil {
		b.Fatalf("create store failed: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })
	return store
}

// The conformance suite is the existing shared one, not a new one written for
// this store. That is the whole point: passing the same tests leveldbstore
// passes is what "Pebble satisfies what this codebase asks of a store" means.
// A bespoke suite would only prove the store agrees with itself.

func TestStore(t *testing.T) {
	t.Parallel()

	store, err := pebblestore.New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	storagetest.TestStore(t, store)
}

func TestBatchedStore(t *testing.T) {
	t.Parallel()

	store, err := pebblestore.New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	storagetest.TestBatchedStore(t, store)
}

func BenchmarkStore(b *testing.B) {
	storagetest.BenchmarkStore(b, func(b *testing.B) storage.Store {
		b.Helper()

		return newBenchStore(b)
	})
}

func BenchmarkBatchedStore(b *testing.B) {
	storagetest.BenchmarkBatchedStore(b, func(b *testing.B) storage.BatchStore {
		b.Helper()

		return newBenchStore(b)
	})
}
