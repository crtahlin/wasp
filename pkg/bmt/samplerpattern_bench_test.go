// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bmt_test

// Measurement harness for issue #236. The reserve sampler runs GOMAXPROCS
// chunk-level hashers concurrently, so the cores are already saturated by the
// caller. This benchmark reproduces that by running GOMAXPROCS parallel
// workers, each repeatedly hashing a 4 KB chunk with its own hasher, and
// compares the ordinary per-section fan-out hasher against the sampler's sync
// hasher. Run at constrained GOMAXPROCS to model weaker machines:
//
//	for p in 1 2 4 8; do GOMAXPROCS=$p go test ./pkg/bmt/ -run '^$' \
//	  -bench BenchmarkSamplerPattern -benchmem -benchtime 2000x; done

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/bmt"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

func BenchmarkSamplerPattern(b *testing.B) {
	// Force the goroutine variants so this compares fan-out against sync on any
	// platform, which is the choice the non-SIMD nodes actually face.
	prev := bmt.SIMDOptIn()
	bmt.SetSIMDOptIn(false)
	defer bmt.SetSIMDOptIn(prev)

	prefix := testutil.RandBytesWithSeed(b, 32, seed)
	data := testutil.RandBytesWithSeed(b, 4096, seed+1)

	b.Run("fanout", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			h := bmt.NewPrefixHasher(prefix)
			for pb.Next() {
				if _, err := syncHash(h, data); err != nil {
					b.Fatal(err)
				}
			}
		})
	})

	b.Run("sync", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			h := bmt.NewSamplerPrefixHasher(prefix)
			for pb.Next() {
				if _, err := syncHash(h, data); err != nil {
					b.Fatal(err)
				}
			}
		})
	})
}
