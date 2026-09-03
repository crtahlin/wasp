// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package reserve_test

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/sharky"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/leveldbstore"
	"github.com/ethersphere/bee/v2/pkg/storage/pebblestore"
	chunk "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/reserve"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/transaction"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	kademlia "github.com/ethersphere/bee/v2/pkg/topology/mock"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

type ampDirFS struct{ base string }

func (d *ampDirFS) Open(path string) (fs.File, error) {
	return os.OpenFile(filepath.Join(d.base, path), os.O_RDWR|os.O_CREATE, 0o644)
}

func dirBytes(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// writeNChunks builds a reserve on the given index store and writes n chunks
// through the real reserve write path (six index writes per chunk plus the
// sharky blob), returning the reserve so the caller can read engine stats.
func writeNChunks(t *testing.T, dir string, st storage.BatchStore, n int) {
	t.Helper()
	sharkyPath := filepath.Join(dir, "sharky")
	if err := os.MkdirAll(sharkyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	shk, err := sharky.New(&ampDirFS{sharkyPath}, 32, swarm.SocMaxChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shk.Close() })

	baseAddr := swarm.RandAddress(t)
	ts := transaction.NewStorage(shk, st)
	r, err := reserve.New(baseAddr, ts, n*2, kademlia.NewTopologyDriver(), log.Noop)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := 0; i < n; i++ {
		ch := chunk.GenerateTestRandomChunkAt(t, baseAddr, i%int(swarm.MaxPO))
		if err := r.Put(ctx, ch); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
}

func report(t *testing.T, engine string, n int, ioWrite uint64, idxSize int64) {
	t.Helper()
	const logicalBytes = 825 // per chunk, from the static trace in #30
	perChunkWrite := float64(ioWrite) / float64(n)
	perChunkFootprint := float64(idxSize) / float64(n)
	t.Logf("[%s] chunks=%d", engine, n)
	t.Logf("[%s] logical index writes = %d B/chunk (six writes, static #30)", engine, logicalBytes)
	t.Logf("[%s] physical write        = %d B -> %.0f B/chunk", engine, ioWrite, perChunkWrite)
	t.Logf("[%s] on-disk footprint     = %d B -> %.0f B/chunk", engine, idxSize, perChunkFootprint)
	t.Logf("[%s] WRITE AMPLIFICATION   = %.1fx (physical write / logical), footprint %.0f B vs 4096 B payload",
		engine, perChunkWrite/float64(logicalBytes), perChunkFootprint)
}

// TestReserveWriteAmplification quantifies the cumulative write amplification of
// the reserve write path (issue #30): how many bytes the index engine physically
// writes (including compaction rewrites) per chunk stored, against the ~825 B of
// logical index writes the path issues per chunk (the static six-write trace).
// It runs both engines and forces a full compaction so the figure is the total
// write to reach a fully-compacted state.
//
// A measurement, not a unit test — gated behind an env var. Run with:
//
//	WRITEAMP=1 go test ./pkg/storer/internal/reserve/ -run TestReserveWriteAmplification -v -timeout 20m
func TestReserveWriteAmplification(t *testing.T) {
	if os.Getenv("WRITEAMP") == "" {
		t.Skip("set WRITEAMP=1 to run the #30 write-amplification measurement")
	}
	const n = 100_000

	t.Run("goleveldb", func(t *testing.T) {
		dir := t.TempDir()
		idxPath := filepath.Join(dir, "idx")
		st, _, err := leveldbstore.New(idxPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })

		_ = st.DB().CompactRange(util.Range{})
		var before leveldb.DBStats
		_ = st.DB().Stats(&before)

		writeNChunks(t, dir, st, n)

		_ = st.DB().CompactRange(util.Range{})
		var after leveldb.DBStats
		_ = st.DB().Stats(&after)

		report(t, "goleveldb", n, after.IOWrite-before.IOWrite, dirBytes(idxPath))
	})

	t.Run("pebble", func(t *testing.T) {
		dir := t.TempDir()
		idxPath := filepath.Join(dir, "idx")
		st, err := pebblestore.New(idxPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })

		full := bytes.Repeat([]byte{0xff}, 40)
		written := func() uint64 {
			m := st.DB().Metrics()
			var w uint64
			for i := range m.Levels {
				w += m.Levels[i].BytesFlushed + m.Levels[i].BytesCompacted
			}
			return w
		}

		_ = st.DB().Compact([]byte{0x00}, full, true)
		before := written()

		writeNChunks(t, dir, st, n)

		_ = st.DB().Flush()
		_ = st.DB().Compact([]byte{0x00}, full, true)
		report(t, "pebble", n, written()-before, dirBytes(idxPath))
	})
}
