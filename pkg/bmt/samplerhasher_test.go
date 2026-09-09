// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bmt_test

import (
	"bytes"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/bmt"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

// TestSamplerPrefixHasherByteIdentical checks that the sampler's sync hasher
// produces exactly the same output as the ordinary per-section fan-out hasher,
// across chunk sizes and when a single hasher instance is reused across chunks
// (which is how the sampler uses it). SIMD is forced off so the comparison is
// fan-out versus sync on every platform, not SIMD versus SIMD. See issue #236.
func TestSamplerPrefixHasherByteIdentical(t *testing.T) {
	prev := bmt.SIMDOptIn()
	bmt.SetSIMDOptIn(false)
	defer bmt.SetSIMDOptIn(prev)

	prefix := testutil.RandBytesWithSeed(t, 32, seed)
	base := testutil.RandBytesWithSeed(t, 4096, seed+1)
	sizes := []int{0, 1, 31, 32, 33, 63, 64, 65, 127, 128, 500, 1000, 2048, 4095, 4096}

	// one reused sync hasher, matching the sampler's reuse via Reset
	sampler := bmt.NewSamplerPrefixHasher(prefix)

	for _, n := range sizes {
		data := base[:n]
		want, err := syncHash(bmt.NewPrefixHasher(prefix), data)
		if err != nil {
			t.Fatalf("size %d: fan-out hash: %v", n, err)
		}
		got, err := syncHash(sampler, data)
		if err != nil {
			t.Fatalf("size %d: sampler hash: %v", n, err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("size %d: sampler hasher output differs from fan-out\n got %x\nwant %x", n, got, want)
		}
	}
}
