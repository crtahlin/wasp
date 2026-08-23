// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && amd64 && !purego

package keccak_test

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/keccak"
)

const guardByte = 0xA5

// guarded places the output array between two canary regions inside a single
// allocation, so a write past either end of the array lands in a region whose
// contents are known.
type guarded struct {
	pre  [8192]byte
	out  [4]keccak.Hash256
	post [8192]byte
}

// TestNoWritesOutsideOutputArray checks that the Keccak implementation writes
// only inside the output array it is given, and does not modify its inputs.
//
// This matters more here than for ordinary Go code. The permutation ships as a
// pre-linked binary blob (see README.md), so it is not bounds-checked by the
// compiler, not instrumented by the race detector, and not visible to any other
// tooling in the tree. Without a test like this, "the blob stays inside its
// buffers" is an assumption rather than a fact.
//
// Written while investigating issue #77, where SIMD hashing was shown to corrupt
// memory. It does not reproduce that fault — the blob passes — which is itself
// the useful result: it excludes out-of-bounds writes to the output array and
// to the input buffers as the mechanism.
func TestNoWritesOutsideOutputArray(t *testing.T) {
	t.Parallel()

	if !keccak.HasSIMD() {
		t.Skip("CPU has neither AVX2 nor AVX-512; the SIMD path is not selected")
	}

	const iterations = 2000

	for iter := range iterations {
		g := new(guarded)
		for i := range g.pre {
			g.pre[i] = guardByte
		}
		for i := range g.post {
			g.post[i] = guardByte
		}

		// Vary both the input length and how many lanes are active, so partial
		// batches with nil filler lanes are covered as well as full ones.
		n := (iter * 7) % 4097
		active := 1 + iter%4

		var inputs, before [4][]byte
		for j := range active {
			b := make([]byte, n)
			if _, err := rand.Read(b); err != nil {
				t.Fatal(err)
			}
			inputs[j] = b
			before[j] = append([]byte(nil), b...)
		}

		keccak.Keccak256x4Raw(&inputs, &g.out)

		for i, v := range g.pre {
			if v != guardByte {
				t.Fatalf("iteration %d (len %d, %d active lanes): wrote %#x %d bytes BEFORE the output array",
					iter, n, active, v, len(g.pre)-i)
			}
		}
		for i, v := range g.post {
			if v != guardByte {
				t.Fatalf("iteration %d (len %d, %d active lanes): wrote %#x %d bytes AFTER the output array",
					iter, n, active, v, i)
			}
		}
		for j := range active {
			if !bytes.Equal(inputs[j], before[j]) {
				t.Fatalf("iteration %d (len %d): lane %d input buffer was modified", iter, n, j)
			}
		}
	}
}
