// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package leveldbstore_test

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/storage/leveldbstore"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// TestReadOnlyOpen covers a mode that was completely unusable: New wrote the
// dirty-shutdown marker unconditionally, and that write fails with
// "leveldb: read-only mode", so every read-only open returned an error. Close
// had the same problem, deleting the marker.
//
// Read-only opens are how inspection tools and benchmarks look at a live
// node's store without disturbing it.
func TestReadOnlyOpen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create it read-write first; a read-only open of a non-existent database
	// is a different failure.
	st, _, err := leveldbstore.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Put([]byte("k"), []byte("v"), nil); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	ro, dirty, err := leveldbstore.New(dir, &opt.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only open failed: %v", err)
	}
	if dirty {
		t.Error("a cleanly closed database was reported dirty")
	}
	v, err := ro.DB().Get([]byte("k"), nil)
	if err != nil {
		t.Fatalf("reading through a read-only store: %v", err)
	}
	if string(v) != "v" {
		t.Errorf("got %q, want %q", v, "v")
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("closing a read-only store failed: %v", err)
	}
}

// TestReadOnlyOpenDoesNotClaimTheDatabase: a reader must not mark the database
// as in use, or a later writer would think the previous one crashed.
func TestReadOnlyOpenDoesNotClaimTheDatabase(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	st, _, err := leveldbstore.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	ro, _, err := leveldbstore.New(dir, &opt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening read-write must not report the previous shutdown as unclean.
	rw, dirty, err := leveldbstore.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rw.Close() })
	if dirty {
		t.Error("a read-only open left the database looking uncleanly shut down, " +
			"which would send the next start into recovery for no reason")
	}
}
