// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command writebench measures index-store write throughput for the two
// selectable storage engines, goleveldb and Pebble, on real hardware.
//
// It exists because write throughput is the one axis of the storage-engine A/B
// (issue #185) that a live node cannot show: a storer only writes as fast as
// pull-sync feeds it, which is network-bound, so the engine's own write ceiling
// stays hidden. This harness removes the network and drives the write path
// directly, at the reserve's ingest shape (several small index entries per
// chunk, committed per chunk), through each engine's own Batch/Commit path so
// the durability behaviour matches what a node does.
//
// It never touches a running node's data. It creates its own empty store in a
// scratch directory that must be empty, refuses any path that looks like a bee
// datadir, and removes what it wrote on exit unless asked to keep it. Point it
// at a scratch directory on the same disk as the node for representative I/O,
// not at the node's datadir.
//
// Usage:
//
//	writebench -engine pebble -path /var/lib/bench-writebench -chunks 500000
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
	"github.com/syndtr/goleveldb/leveldb/opt"

	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/leveldbstore"
	"github.com/ethersphere/bee/v2/pkg/storage/pebblestore"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "writebench:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		engine     = flag.String("engine", "leveldb", "storage engine: leveldb or pebble")
		path       = flag.String("path", "", "scratch directory for the store; must be empty and not a bee datadir")
		chunks     = flag.Int("chunks", 200000, "number of chunks to simulate")
		entries    = flag.Int("entries-per-chunk", 4, "index entries written per chunk")
		valueSize  = flag.Int("value-size", 40, "bytes per index entry value")
		commitEch  = flag.Int("commit-every", 1, "chunks per batch commit")
		cacheBytes = flag.Int("cache-bytes", 64<<20, "block cache capacity, matching the node")
		bufBytes   = flag.Int("buffer-bytes", 64<<20, "write buffer / memtable size, matching the node")
		l0Trigger  = flag.Int("l0-trigger", 4, "level-0 compaction trigger (pebble L0CompactionThreshold)")
		openFiles  = flag.Int("open-files", 512, "open files limit, matching the node")
		seed       = flag.Int64("seed", 1, "PRNG seed for reproducibility")
		keep       = flag.Bool("keep", false, "keep the scratch store instead of deleting it")
	)
	flag.Parse()

	if *path == "" {
		return fmt.Errorf("-path is required (a scratch directory, never the node datadir)")
	}
	if err := guardScratchPath(*path); err != nil {
		return err
	}

	st, closeStore, err := openStore(*engine, *path, *cacheBytes, *bufBytes, *l0Trigger, *openFiles)
	if err != nil {
		return err
	}
	defer func() {
		_ = closeStore()
		if !*keep {
			_ = os.RemoveAll(*path)
		}
	}()

	ctx := context.Background()
	rng := rand.New(rand.NewSource(*seed))
	value := make([]byte, *valueSize)

	commitLatencies := make([]time.Duration, 0, *chunks/max(*commitEch, 1))
	start := time.Now()

	batch := st.Batch(ctx)
	inBatch := 0
	for c := 0; c < *chunks; c++ {
		for e := 0; e < *entries; e++ {
			rng.Read(value)
			it := &kvItem{
				ns:  fmt.Sprintf("idx%d", e),
				id:  fmt.Sprintf("%016x%016x", rng.Uint64(), rng.Uint64()),
				val: append([]byte(nil), value...),
			}
			if err := batch.Put(it); err != nil {
				return fmt.Errorf("batch put: %w", err)
			}
		}
		inBatch++
		if inBatch >= *commitEch {
			t := time.Now()
			if err := batch.Commit(); err != nil {
				return fmt.Errorf("commit: %w", err)
			}
			commitLatencies = append(commitLatencies, time.Since(t))
			batch = st.Batch(ctx)
			inBatch = 0
		}
	}
	if inBatch > 0 {
		t := time.Now()
		if err := batch.Commit(); err != nil {
			return fmt.Errorf("final commit: %w", err)
		}
		commitLatencies = append(commitLatencies, time.Since(t))
	}

	elapsed := time.Since(start)
	report(*engine, *chunks, *entries, elapsed, commitLatencies)
	return nil
}

// guardScratchPath refuses anything that could be a live node datadir, and
// requires the target to be empty, so the harness cannot open or overwrite a
// node's Swarm data.
func guardScratchPath(p string) error {
	for _, marker := range []string{"keys", "localstore", "statestore", "CURRENT", ".storage-engine"} {
		if _, err := os.Stat(filepath.Join(p, marker)); err == nil {
			return fmt.Errorf("refusing to run: %q looks like a bee datadir (found %q)", p, marker)
		}
	}
	entries, err := os.ReadDir(p)
	if err == nil && len(entries) > 0 {
		return fmt.Errorf("refusing to run: scratch path %q is not empty", p)
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat scratch path: %w", err)
	}
	return nil
}

func openStore(engine, path string, cacheBytes, bufBytes, l0Trigger, openFiles int) (storage.BatchStore, func() error, error) {
	switch engine {
	case "leveldb":
		o := &opt.Options{
			BlockCacheCapacity:     cacheBytes,
			WriteBuffer:            bufBytes,
			OpenFilesCacheCapacity: openFiles,
			CompactionL0Trigger:    l0Trigger,
		}
		s, _, err := leveldbstore.New(path, o)
		if err != nil {
			return nil, nil, fmt.Errorf("open leveldb: %w", err)
		}
		return s, s.Close, nil
	case "pebble":
		o := &pebble.Options{
			Cache:                 pebble.NewCache(int64(cacheBytes)),
			MemTableSize:          uint64(bufBytes),
			L0CompactionThreshold: l0Trigger,
			MaxOpenFiles:          openFiles,
		}
		o.EnsureDefaults()
		for i := range o.Levels {
			o.Levels[i].FilterPolicy = bloom.FilterPolicy(10)
		}
		s, err := pebblestore.New(path, o)
		if err != nil {
			return nil, nil, fmt.Errorf("open pebble: %w", err)
		}
		return s, s.Close, nil
	default:
		return nil, nil, fmt.Errorf("unknown engine %q (want leveldb or pebble)", engine)
	}
}

func report(engine string, chunks, entries int, elapsed time.Duration, lat []time.Duration) {
	writes := chunks * entries
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		idx := int(p * float64(len(lat)-1))
		return lat[idx]
	}
	fmt.Printf("engine=%s chunks=%d entries_per_chunk=%d index_writes=%d\n", engine, chunks, entries, writes)
	fmt.Printf("elapsed=%.2fs chunks_per_s=%.0f index_writes_per_s=%.0f\n",
		elapsed.Seconds(), float64(chunks)/elapsed.Seconds(), float64(writes)/elapsed.Seconds())
	fmt.Printf("commit_latency_ms p50=%.3f p99=%.3f max=%.3f commits=%d\n",
		float64(pct(0.50).Microseconds())/1000, float64(pct(0.99).Microseconds())/1000,
		float64(pct(1.0).Microseconds())/1000, len(lat))
}

// kvItem is a synthetic storage.Item: a small keyed value standing in for one
// reserve index entry.
type kvItem struct {
	ns  string
	id  string
	val []byte
}

func (i *kvItem) Namespace() string        { return i.ns }
func (i *kvItem) ID() string               { return i.id }
func (i *kvItem) Marshal() ([]byte, error) { return i.val, nil }
func (i *kvItem) Unmarshal(b []byte) error { i.val = append([]byte(nil), b...); return nil }
func (i *kvItem) String() string           { return i.ns + "/" + i.id }
func (i *kvItem) Clone() storage.Item {
	if i == nil {
		return nil
	}
	return &kvItem{ns: i.ns, id: i.id, val: append([]byte(nil), i.val...)}
}
