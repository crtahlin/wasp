// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pebblestore_test

import (
	"os"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/ethersphere/bee/v2/pkg/storage/pebblestore"
	"github.com/ethersphere/bee/v2/pkg/storage/storagetest"
)

// benchOptions lets one benchmark run be repeated with the bloom filter off,
// under an identical benchmark selection.
//
// The selection matters: the sub-benchmarks share a store, so running a
// different subset changes the state each one sees and the numbers with it.
// Two runs of the same names is the only comparison that means anything, which
// is why this is an environment variable rather than a second set of
// benchmarks under different names.
func benchOptions() *pebble.Options {
	if os.Getenv("PEBBLE_NO_FILTER") == "1" {
		return &pebble.Options{}
	}
	return nil
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
	store, err := pebblestore.New(b.TempDir(), benchOptions())
	if err != nil {
		b.Fatalf("create store failed: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	storagetest.BenchmarkStore(b, store)
}

func BenchmarkBatchedStore(b *testing.B) {
	store, err := pebblestore.New(b.TempDir(), benchOptions())
	if err != nil {
		b.Fatalf("create store failed: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	storagetest.BenchmarkBatchedStore(b, store)
}
