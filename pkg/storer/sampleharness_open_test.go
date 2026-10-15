// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build samplerharness

package storer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/log"
	chunk "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// makeNodeDataDir writes a store the way a node lays out its data
// directory, <dir>/localstore, with chunks in its reserve, and closes it.
func makeNodeDataDir(t *testing.T, chunks int) string {
	t.Helper()
	dir := t.TempDir()
	opts := dbTestOps(swarm.RandAddress(t), 1000, nil, nil, time.Second)
	st, err := storer.New(context.Background(), filepath.Join(dir, "localstore"), opts)
	if err != nil {
		t.Fatal(err)
	}
	putter := st.ReservePutter()
	for range chunks {
		if err := putter.Put(context.Background(), chunk.GenerateTestRandomChunk()); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSamplerHarnessOpensNodeLayout checks the two defects found on the
// bench: the harness opened SAMPLER_DATA_DIR itself, creating an empty
// store next to the node's, and opened stores with the package tests' 4
// sharky shards instead of the node's 32.
func TestSamplerHarnessOpensNodeLayout(t *testing.T) {
	if got := storer.SharkyShardLimit(); got != 32 {
		t.Fatalf("the harness build opens stores with %d sharky shards, want the node's 32", got)
	}

	dir := makeNodeDataDir(t, 20)
	for _, open := range []string{dir, filepath.Join(dir, "localstore")} {
		db, closeStore, err := openHarnessStore(context.Background(), log.Noop, open, "")
		if err != nil {
			t.Fatalf("open %s: %v", open, err)
		}
		if n := db.ReserveSize(); n != 20 {
			t.Fatalf("open %s: reserve holds %d chunks, want the 20 stored", open, n)
		}
		closeStore()
	}
	if _, err := os.Stat(filepath.Join(dir, "localstore", "localstore")); !os.IsNotExist(err) {
		t.Fatalf("a nested localstore was created (%v)", err)
	}

	// A directory without a store is refused, and nothing is created in it.
	empty := t.TempDir()
	if _, _, err := openHarnessStore(context.Background(), log.Noop, empty, ""); err == nil {
		t.Fatal("opened a directory with no store, want an error")
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Fatalf("refusing an empty directory left %d entries in it", len(entries))
	}

	// A store with an empty reserve is refused.
	if _, _, err := openHarnessStore(context.Background(), log.Noop, makeNodeDataDir(t, 0), ""); err == nil {
		t.Fatal("opened a store with an empty reserve, want an error")
	}

	// A store with another shard count is refused before it is opened.
	odd := makeNodeDataDir(t, 5)
	shard := filepath.Join(odd, "localstore", "sharky", "shard_031")
	if err := os.Remove(shard); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openHarnessStore(context.Background(), log.Noop, odd, ""); err == nil {
		t.Fatal("opened a store with 31 shards, want an error")
	}
	if _, err := os.Stat(shard); !os.IsNotExist(err) {
		t.Fatalf("the refused store was opened: its missing shard is back (%v)", err)
	}
}
