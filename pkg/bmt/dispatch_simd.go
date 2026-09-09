// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && amd64 && !purego

package bmt

import (
	"github.com/ethersphere/bee/v2/pkg/keccak"
)

// NewPool returns a BMT pool. If SIMDOptIn() is true and the CPU exposes AVX2
// or AVX-512, a SIMD-batched pool is returned. Otherwise the goroutine-based
// pool is returned (silent fallback).
func NewPool(c *Conf) Pool {
	if SIMDOptIn() && keccak.HasSIMD() {
		return newSIMDPool(c)
	}
	return newGoroutinePool(c)
}

// NewHasher returns a standalone (non-pooled) BMT hasher.
func NewHasher() Hasher {
	if SIMDOptIn() && keccak.HasSIMD() {
		return newSIMDHasher()
	}
	return newGoroutineHasher()
}

// NewPrefixHasher returns a standalone BMT hasher with the given prefix.
func NewPrefixHasher(prefix []byte) Hasher {
	if SIMDOptIn() && keccak.HasSIMD() {
		return newSIMDPrefixHasher(prefix)
	}
	return newGoroutinePrefixHasher(prefix)
}

// NewSamplerPrefixHasher returns a prefix hasher for a caller that already
// saturates the cores with its own parallelism, such as the reserve sampler's
// hasher pool. On SIMD the SIMD hasher is returned unchanged; it is
// single-threaded per hash and has no per-section fan-out to remove. Off SIMD
// the sync goroutine hasher is returned, which hashes sections in the calling
// goroutine so it does not oversubscribe the cores the caller is already using.
// See issue #236.
func NewSamplerPrefixHasher(prefix []byte) Hasher {
	if SIMDOptIn() && keccak.HasSIMD() {
		return newSIMDPrefixHasher(prefix)
	}
	return newGoroutineSyncPrefixHasher(prefix)
}
