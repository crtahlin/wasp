// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package leveldbstore_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/leveldbstore"
	"github.com/ethersphere/bee/v2/pkg/storage/storagetest"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

func TestStore(t *testing.T) {
	t.Parallel()

	store, _, err := leveldbstore.New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storagetest.TestStore(t, store)
}

// newBenchStore gives one sub-benchmark a store of its own.
//
// The suite used to share a single store, which made each sub-benchmark's
// numbers a function of what the sub-benchmarks before it had written, and so
// of which -bench selection was run. See issue #146.
func newBenchStore(b *testing.B) *leveldbstore.Store {
	b.Helper()

	st, _, err := leveldbstore.New("", &opt.Options{
		Compression: opt.SnappyCompression,
	})
	if err != nil {
		b.Fatalf("create store failed: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })
	return st
}

func BenchmarkStore(b *testing.B) {
	storagetest.BenchmarkStore(b, func(b *testing.B) storage.Store {
		b.Helper()

		return newBenchStore(b)
	})
}

func TestBatchedStore(t *testing.T) {
	t.Parallel()

	st, _, err := leveldbstore.New("", nil)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	storagetest.TestBatchedStore(t, st)
}

func BenchmarkBatchedStore(b *testing.B) {
	storagetest.BenchmarkBatchedStore(b, func(b *testing.B) storage.BatchStore {
		b.Helper()

		return newBenchStore(b)
	})
}

func TestDirtyMarker(t *testing.T) {
	t.Parallel()

	t.Run("clean on first open", func(t *testing.T) {
		t.Parallel()
		st, dirty, err := leveldbstore.New(t.TempDir(), nil)
		if err != nil {
			t.Fatalf("open failed: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if dirty {
			t.Fatal("expected clean on first open, got dirty")
		}
	})

	t.Run("clean after clean close", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		st, _, err := leveldbstore.New(dir, nil)
		if err != nil {
			t.Fatalf("first open failed: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close failed: %v", err)
		}

		st, dirty, err := leveldbstore.New(dir, nil)
		if err != nil {
			t.Fatalf("second open failed: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if dirty {
			t.Fatal("expected clean after clean close, got dirty")
		}
	})

	t.Run("dirty after crash", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		// Simulate a previous session that crashed: open the raw leveldb
		// and write the dirty marker directly, then close without deleting it.
		db, err := leveldb.OpenFile(dir, nil)
		if err != nil {
			t.Fatalf("raw open failed: %v", err)
		}
		if err := db.Put([]byte(".store-dirty-shutdown"), []byte{}, nil); err != nil {
			t.Fatalf("write dirty key failed: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("raw close failed: %v", err)
		}

		// Now open via leveldbstore — should detect the marker as dirty.
		st, dirty, err := leveldbstore.New(dir, nil)
		if err != nil {
			t.Fatalf("open failed: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if !dirty {
			t.Fatal("expected dirty after crash, got clean")
		}
	})
}

// TestCloseWritesNothingToTheDatabase pins the property the whole change exists
// for.
//
// The marker used to be a key inside the database, deleted on close. That made
// shutdown depend on the store being writable at exactly the moment it is least
// likely to be: with writes paused at the level-0 trigger, Close blocked in
// putRec, the node could not exit, an operator killed it, and the next start
// ran recovery over a marker the node had never been given the chance to clear.
// One stall became three failures, the last of which looked like an unrelated
// crash.
//
// Close can only be write-free if there is nothing in the database to delete,
// so that is what is asserted here rather than the absence of a blocked call.
// See issue #115.
func TestCloseWritesNothingToTheDatabase(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	st, _, err := leveldbstore.New(dir, nil)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}

	has, err := st.DB().Has([]byte(".store-dirty-shutdown"), nil)
	if err != nil {
		t.Fatalf("has failed: %v", err)
	}
	if has {
		t.Fatal("the store wrote a dirty marker into the database; " +
			"close would then need a write, which is what a stalled store cannot do")
	}

	marker := filepath.Join(dir, ".dirty-shutdown")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no marker file at %s: %v; nothing records that a writer holds this store", marker, err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker still present after a clean close: %v; every later start would recover needlessly", err)
	}
}

// TestDirtyMarkerFileSurvivesACrash asserts the file marker does the job the
// database key used to do.
func TestDirtyMarkerFileSurvivesACrash(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	st, _, err := leveldbstore.New(dir, nil)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	// Close the database directly, leaving the marker behind. This is what a
	// killed process looks like from the next start's point of view.
	if err := st.DB().Close(); err != nil {
		t.Fatalf("raw close failed: %v", err)
	}

	st, dirty, err := leveldbstore.New(dir, nil)
	if err != nil {
		t.Fatalf("second open failed: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if !dirty {
		t.Fatal("expected dirty after a close that skipped the store, got clean")
	}
}

// TestLegacyDirtyKeyIsMigrated asserts a store written by an earlier build is
// read correctly and then stops carrying the old key.
//
// Without the migration the key would sit in every existing store for ever,
// reporting dirty on every open, and every start would run recovery.
func TestLegacyDirtyKeyIsMigrated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatalf("raw open failed: %v", err)
	}
	if err := db.Put([]byte(".store-dirty-shutdown"), []byte{}, nil); err != nil {
		t.Fatalf("write legacy key failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("raw close failed: %v", err)
	}

	st, dirty, err := leveldbstore.New(dir, nil)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	if !dirty {
		t.Fatal("a legacy key means the previous writer did not close cleanly; reported clean")
	}

	has, err := st.DB().Has([]byte(".store-dirty-shutdown"), nil)
	if err != nil {
		t.Fatalf("has failed: %v", err)
	}
	if has {
		t.Fatal("legacy key still present after open; it would report dirty for ever")
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	st, dirty, err = leveldbstore.New(dir, nil)
	if err != nil {
		t.Fatalf("third open failed: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if dirty {
		t.Fatal("expected clean after a clean close of a migrated store, got dirty")
	}
}

// TestReadOnlyOpenLeavesNoMarker asserts a reader neither claims the store nor
// clears someone else's claim.
func TestReadOnlyOpenLeavesNoMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	st, _, err := leveldbstore.New(dir, nil)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	ro, dirty, err := leveldbstore.New(dir, &opt.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only open failed: %v", err)
	}
	if dirty {
		t.Fatal("expected clean, got dirty")
	}
	if _, err := os.Stat(filepath.Join(dir, ".dirty-shutdown")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a read-only open created a marker: %v; it cannot shut down uncleanly and must not claim the store", err)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("read-only close failed: %v", err)
	}
}

// TestInMemoryStoreHasNoMarker asserts the in-memory store neither writes a
// marker nor reports one, since it cannot outlive the process.
func TestInMemoryStoreHasNoMarker(t *testing.T) {
	t.Parallel()

	st, dirty, err := leveldbstore.New("", nil)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	if dirty {
		t.Fatal("an in-memory store has no previous session to have been dirty")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
}

// TestFreshStoreAfterDirectoryRemovalIsClean asserts that deleting the store
// directory really does start over.
//
// This is why the marker lives inside the directory rather than beside it. A
// sibling file would outlive the store it describes, so the operator's usual
// repair — remove the directory and let the node rebuild it — would produce a
// brand-new store that reports an unclean shutdown on its first open, and every
// start would run recovery over nothing.
func TestFreshStoreAfterDirectoryRemovalIsClean(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	dir := filepath.Join(base, "indexstore")

	st, _, err := leveldbstore.New(dir, nil)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	// Leave the marker set, as a killed process would.
	if err := st.DB().Close(); err != nil {
		t.Fatalf("raw close failed: %v", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove store failed: %v", err)
	}

	st, dirty, err := leveldbstore.New(dir, nil)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if dirty {
		t.Fatal("a store created from nothing reported an unclean shutdown; " +
			"the marker outlived the store it described")
	}
}
