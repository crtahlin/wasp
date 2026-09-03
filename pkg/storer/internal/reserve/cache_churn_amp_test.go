// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package reserve_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/leveldbstore"
	"github.com/ethersphere/bee/v2/pkg/storage/pebblestore"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// TestCacheChurnAmplification is the #215 measurement, refining the #14 spike
// (TestNamespaceSplitAmplification) after reconciling the split against the
// transaction layer.
//
// The finding recorded on #14: a transaction commits one batch to one index
// store atomically, and retrievalIdx is co-written with every subsystem, so the
// reserve write amplification measured in #30 is one atomic transaction that a
// namespace split cannot reduce. The only churn a split can isolate is the cache
// LRU, and the only case the split helps is sustained cache LRU churn rewriting
// cold reserve data through the shared keyspace. This measures exactly that.
//
// Two things this models that the #14 spike did not:
//
//  1. The real cache LRU churn. cacheOrderIndex carries the access timestamp in
//     its key, so each simulated read DELETES the old order key and INSERTS a new
//     one (plus a cacheEntry overwrite) — tombstones and key movement, not a
//     same-key overwrite. That is the write pattern that drags cold keys into
//     compactions.
//  2. A settled, reserve-scale cold keyset (one 825 B aggregate key per chunk,
//     matching the per-chunk index footprint measured in #30), churn intensity
//     swept so the result is a curve, and three runs per condition with the
//     spread, per fork rule 7.
//
// It compares, under the identical workload, a single shared store (cold reserve
// keys plus cache churn together, today's layout) against two split stores (cold
// reserve alone, cache churn alone — what a cache-isolation split would give).
// split/shared below 1.0 is the amplification the split would remove.
//
// A measurement, not a unit test — gated behind an env var:
//
//	WRITEAMP=1 go test ./pkg/storer/internal/reserve/ -run TestCacheChurnAmplification -v -timeout 30m
func TestCacheChurnAmplification(t *testing.T) {
	if os.Getenv("WRITEAMP") == "" {
		t.Skip("set WRITEAMP=1 to run the #215 cache-churn amplification measurement")
	}

	const (
		reserveKeys = 100_000 // settled reserve, written once
		cacheKeys   = 10_000  // cache LRU, ~10% of the reserve
		coldVal     = 825     // per-chunk reserve index footprint, from #30
		runs        = 3       // rule 7: three runs per condition
	)
	// churn intensity: reads per cache key over the run. Each read moves that
	// key's cacheOrderIndex entry. Swept to see whether the split's benefit
	// scales with read traffic.
	churnLevels := []int{5, 20}

	orderKey := func(ts, i int) string { return fmt.Sprintf("%019d-%09d", ts, i) }

	// workload drives the identical sequence, routing cold reserve writes and the
	// cache churn (put and delete) through the supplied functions. In the shared
	// case all three point at one store; in the split case cold goes to one store
	// and the cache churn to another. The seed makes the random values
	// deterministic, so the shared and split arms of one run see identical bytes
	// and their physical-write totals are comparable; different runs use different
	// seeds so the three-run spread captures data variation, not just timing.
	//
	// Values are filled with random bytes, not zeros: real index values (chunk
	// locations, addresses, timestamps) are high-entropy and do not compress, and
	// an all-zero buffer compresses away under snappy and understates every
	// figure. A fresh random fill per write also keeps adjacent values distinct so
	// they do not compress against each other within a block.
	workload := func(reservePut, cachePut, cacheDel func(storage.Item) error, reads int, seed int64) {
		rng := rand.New(rand.NewSource(seed))
		cbuf := make([]byte, coldVal)
		ebuf := make([]byte, 16) // cacheEntry value: an access timestamp

		// 1. Fill the reserve once — the settled cold keyset.
		for i := 0; i < reserveKeys; i++ {
			_, _ = rng.Read(cbuf)
			_ = reservePut(&kv{ns: "reserveIdx", key: fmt.Sprintf("r%09d", i), val: cbuf})
		}
		// 2. Seed the cache entries at timestamp 0.
		lastTS := make([]int, cacheKeys)
		for i := 0; i < cacheKeys; i++ {
			_, _ = rng.Read(ebuf)
			_ = cachePut(&kv{ns: "cacheOrderIndex", key: orderKey(0, i), val: nil})
			_ = cachePut(&kv{ns: "cacheEntry", key: fmt.Sprintf("e%09d", i), val: ebuf})
		}
		// 3. Churn: every read deletes the old order key and inserts a new one at
		//    a fresh timestamp, then rewrites the cacheEntry — the Getter pattern.
		ts := 0
		for round := 0; round < reads; round++ {
			for i := 0; i < cacheKeys; i++ {
				ts++
				_, _ = rng.Read(ebuf)
				_ = cacheDel(&kv{ns: "cacheOrderIndex", key: orderKey(lastTS[i], i)})
				_ = cachePut(&kv{ns: "cacheOrderIndex", key: orderKey(ts, i), val: nil})
				_ = cachePut(&kv{ns: "cacheEntry", key: fmt.Sprintf("e%09d", i), val: ebuf})
				lastTS[i] = ts
			}
		}
	}

	// meanSpread reports the mean and the min/max spread of a set of ratios.
	meanSpread := func(xs []float64) (mean, lo, hi float64) {
		lo, hi = xs[0], xs[0]
		var sum float64
		for _, x := range xs {
			sum += x
			if x < lo {
				lo = x
			}
			if x > hi {
				hi = x
			}
		}
		return sum / float64(len(xs)), lo, hi
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
		measure := func(reads int, seed int64) (shared, split uint64) {
			one := newS()
			b0 := written(one)
			workload(one.Put, one.Put, one.Delete, reads, seed)
			shared = written(one) - b0

			cold, hot := newS(), newS()
			c0, h0 := written(cold), written(hot)
			workload(cold.Put, hot.Put, hot.Delete, reads, seed)
			split = (written(cold) - c0) + (written(hot) - h0)
			return
		}
		for _, reads := range churnLevels {
			ratios := make([]float64, 0, runs)
			var lastShared, lastSplit uint64
			for r := 0; r < runs; r++ {
				shared, split := measure(reads, int64(r+1))
				ratios = append(ratios, float64(split)/float64(shared))
				lastShared, lastSplit = shared, split
			}
			mean, lo, hi := meanSpread(ratios)
			t.Logf("[goleveldb churn=%d] shared=%d B split=%d B (last run)", reads, lastShared, lastSplit)
			t.Logf("[goleveldb churn=%d] split/shared = %.2f  (mean of %d; range %.2f..%.2f; below 1.0 means the split helps)",
				reads, mean, runs, lo, hi)
		}
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
		measure := func(reads int, seed int64) (shared, split uint64) {
			one := newS()
			b0 := written(one)
			workload(one.Put, one.Put, one.Delete, reads, seed)
			shared = written(one) - b0

			cold, hot := newS(), newS()
			c0, h0 := written(cold), written(hot)
			workload(cold.Put, hot.Put, hot.Delete, reads, seed)
			split = (written(cold) - c0) + (written(hot) - h0)
			return
		}
		for _, reads := range churnLevels {
			ratios := make([]float64, 0, runs)
			var lastShared, lastSplit uint64
			for r := 0; r < runs; r++ {
				shared, split := measure(reads, int64(r+1))
				ratios = append(ratios, float64(split)/float64(shared))
				lastShared, lastSplit = shared, split
			}
			mean, lo, hi := meanSpread(ratios)
			t.Logf("[pebble churn=%d] shared=%d B split=%d B (last run)", reads, lastShared, lastSplit)
			t.Logf("[pebble churn=%d] split/shared = %.2f  (mean of %d; range %.2f..%.2f; below 1.0 means the split helps)",
				reads, mean, runs, lo, hi)
		}
	})
}
