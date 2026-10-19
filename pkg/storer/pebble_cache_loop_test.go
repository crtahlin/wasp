// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestPebbleIndexStoreOpenCloseLoop opens and closes the index store through
// initStore many times in one process, as tests and reopening helpers do. With
// pebble's invariants build tag (and without -race) every cache gets a
// finalizer that exits the process when the cache is collected with references
// left, so a kept reference fails this test binary there. Without the tag the
// loop still exercises the path.
func TestPebbleIndexStoreOpenCloseLoop(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	for i := range 20 {
		opts := defaultOptions()
		opts.StorageEngine = EnginePebble
		store, err := initStore(filepath.Join(base, fmt.Sprint(i)), opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// Give the finalizers a chance to run before the test ends.
	for range 5 {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
}
