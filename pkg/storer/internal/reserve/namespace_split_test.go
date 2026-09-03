// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package reserve_test

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/leveldbstore"
	"github.com/ethersphere/bee/v2/pkg/storage/pebblestore"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// kv is a minimal storage.Item with a controllable namespace, key and value,
// for the namespace-split measurement (#14).
type kv struct {
	ns  string
	key string
	val []byte
}

func (k *kv) Namespace() string        { return k.ns }
func (k *kv) ID() string               { return k.key }
func (k *kv) Marshal() ([]byte, error) { return k.val, nil }
func (k *kv) Unmarshal(b []byte) error { k.val = append([]byte(nil), b...); return nil }
func (k *kv) Clone() storage.Item      { c := *k; return &c }
func (k *kv) String() string           { return k.ns + "/" + k.key }

// TestNamespaceSplitAmplification is the #14 spike. It tests the hypothesis that
// mixing high-churn and low-churn namespaces in one index store amplifies the
// cold keys, so splitting by write pattern would cut write amplification.
//
// Workload: cold keys (written once, like a settled reserve index) interleaved
// with hot keys (rewritten every round, like cache churn). It measures the total
// physical write of a single shared store versus two split stores (cold vs hot)
// under the identical workload, on both engines. If the shared store writes
// materially more, the split is justified; if not, an LSM already isolates the
// namespaces by key range and #14 is not worth its migration.
//
// A measurement, not a unit test — gated behind an env var:
//
//	WRITEAMP=1 go test ./pkg/storer/internal/reserve/ -run TestNamespaceSplitAmplification -v -timeout 20m
func TestNamespaceSplitAmplification(t *testing.T) {
	if os.Getenv("WRITEAMP") == "" {
		t.Skip("set WRITEAMP=1 to run the #14 namespace-split spike")
	}
	const (
		coldKeys = 100_000
		hotKeys  = 10_000
		rounds   = 10
		coldVal  = 800
		hotVal   = 200
	)

	// run drives the identical mixed workload, routing cold and hot writes
	// through the supplied functions.
	run := func(coldPut, hotPut func(storage.Item) error) {
		cbuf := make([]byte, coldVal)
		hbuf := make([]byte, hotVal)
		per := coldKeys / rounds
		for r := 0; r < rounds; r++ {
			for i := 0; i < per; i++ {
				_ = coldPut(&kv{ns: "reserveIdx", key: fmt.Sprintf("c%09d", r*per+i), val: cbuf})
			}
			for i := 0; i < hotKeys; i++ {
				_ = hotPut(&kv{ns: "cacheEntry", key: fmt.Sprintf("h%09d", i), val: hbuf})
			}
		}
	}

	t.Run("goleveldb", func(t *testing.T) {
		newS := func() *leveldbstore.Store {
			s, _, err := leveldbstore.New(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		}
		written := func(s *leveldbstore.Store) uint64 {
			_ = s.DB().CompactRange(util.Range{})
			var st leveldb.DBStats
			_ = s.DB().Stats(&st)
			return st.IOWrite
		}

		one := newS()
		b0 := written(one)
		run(one.Put, one.Put)
		baseline := written(one) - b0

		cold, hot := newS(), newS()
		c0, h0 := written(cold), written(hot)
		run(cold.Put, hot.Put)
		split := (written(cold) - c0) + (written(hot) - h0)

		t.Logf("[goleveldb] shared store    = %d B physical write", baseline)
		t.Logf("[goleveldb] split (cold+hot) = %d B physical write", split)
		t.Logf("[goleveldb] split / shared   = %.2f (below 1.0 means the split helps)", float64(split)/float64(baseline))
	})

	t.Run("pebble", func(t *testing.T) {
		full := bytes.Repeat([]byte{0xff}, 40)
		newS := func() *pebblestore.Store {
			s, err := pebblestore.New(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		}
		written := func(s *pebblestore.Store) uint64 {
			_ = s.DB().Flush()
			_ = s.DB().Compact([]byte{0x00}, full, true)
			m := s.DB().Metrics()
			var w uint64
			for i := range m.Levels {
				w += m.Levels[i].BytesFlushed + m.Levels[i].BytesCompacted
			}
			return w
		}

		one := newS()
		b0 := written(one)
		run(one.Put, one.Put)
		baseline := written(one) - b0

		cold, hot := newS(), newS()
		c0, h0 := written(cold), written(hot)
		run(cold.Put, hot.Put)
		split := (written(cold) - c0) + (written(hot) - h0)

		t.Logf("[pebble] shared store    = %d B physical write", baseline)
		t.Logf("[pebble] split (cold+hot) = %d B physical write", split)
		t.Logf("[pebble] split / shared   = %.2f (below 1.0 means the split helps)", float64(split)/float64(baseline))
	})
}
