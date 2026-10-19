// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !invariants

// These tests count references by driving the count below zero on purpose,
// which pebble's invariants build reports as a leak when the cache is
// collected. TestPebbleIndexStoreOpenCloseLoop covers that build instead.

package storer

import (
	"os"
	"path/filepath"
	"testing"
)

// unrefPanics drops one reference to the cache and reports whether pebble
// panicked on it, which it does when the count goes below zero.
func unrefPanics(unref func()) (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	unref()
	return false
}

// TestOpenPebbleIndexStoreReleasesCacheReference pins that after the pebble
// index store is opened and closed, the only reference left to the block cache
// is one the test took itself. The test adds that reference before Open, drops
// it after Close, and then checks that one more drop goes below zero, which
// pebble reports with a panic. If openPebbleIndexStore kept the reference that
// pebbleIndexStoreOptions creates, that extra drop would succeed instead (#815).
func TestOpenPebbleIndexStoreReleasesCacheReference(t *testing.T) {
	t.Parallel()

	o := pebbleIndexStoreOptions(defaultOptions())
	if o.Cache == nil {
		t.Fatal("default options created no block cache; this test needs one")
	}
	o.Cache.Ref() // the test's own reference, so the cache outlives Close

	dir := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := openPebbleIndexStore(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if unrefPanics(o.Cache.Unref) {
		t.Fatal("dropping the test's reference went below zero: a reference was released twice")
	}
	if !unrefPanics(o.Cache.Unref) {
		t.Fatal("cache still referenced after Close and the test's own release: the creator reference was kept")
	}
}

// TestOpenPebbleIndexStoreErrorReleasesCacheReference covers the error path:
// when the store cannot be opened, the creator reference is dropped too.
func TestOpenPebbleIndexStoreErrorReleasesCacheReference(t *testing.T) {
	t.Parallel()

	o := pebbleIndexStoreOptions(defaultOptions())
	o.Cache.Ref()

	// A regular file where the directory should be makes pebble.Open fail.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openPebbleIndexStore(file, o); err == nil {
		t.Fatal("expected open to fail on a regular file")
	}

	if unrefPanics(o.Cache.Unref) {
		t.Fatal("dropping the test's reference went below zero: a reference was released twice")
	}
	if !unrefPanics(o.Cache.Unref) {
		t.Fatal("cache still referenced after a failed open and the test's own release: the creator reference was kept")
	}
}
