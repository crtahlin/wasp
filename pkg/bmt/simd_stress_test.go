// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bmt_test

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/bmt"
	"github.com/ethersphere/bee/v2/pkg/keccak"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// simdActive reports whether opting in actually selects the SIMD implementation
// here. On any platform other than linux/amd64 with AVX2 or AVX-512, NewHasher
// silently falls back to the goroutine hasher, so these tests would otherwise
// pass while exercising nothing at all. Callers skip rather than pretend they
// covered something.
func simdActive() bool { return keccak.HasSIMD() }

// TestSIMDPrefixHasherConcurrent mimics the reserve sampler in
// pkg/storer/sample.go: every worker builds its own prefix hasher from the
// anchor and hashes a stream of chunks of every legal length. That is the
// workload rchash drives on a live node, and it had no test.
func TestSIMDPrefixHasherConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test; skipped under -short")
	}
	if !simdActive() {
		t.Skip("SIMD hasher not active on this platform; nothing to stress")
	}

	prev := bmt.SIMDOptIn()
	bmt.SetSIMDOptIn(true)
	t.Cleanup(func() { bmt.SetSIMDOptIn(prev) })

	const workers = 64
	const iters = 2000

	anchor := make([]byte, swarm.HashSize)
	if _, err := rand.Read(anchor); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := range workers {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			hasher := bmt.NewPrefixHasher(anchor)
			span := make([]byte, bmt.SpanSize)
			for i := range iters {
				n := (seed*7919 + i*131) % (swarm.ChunkSize + 1)
				data := make([]byte, n)
				if _, err := rand.Read(data); err != nil {
					errs <- err
					return
				}
				hasher.Reset()
				hasher.SetHeader(span)
				if _, err := hasher.Write(data); err != nil {
					errs <- err
					return
				}
				if got := len(hasher.Sum(nil)); got != swarm.HashSize {
					errs <- fmt.Errorf("digest length %d for input of %d bytes, want %d", got, n, swarm.HashSize)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// TestSIMDMatchesScalarUnderContention checks that the SIMD path computes the
// same digest as the scalar path while many goroutines contend for CPU. A
// mismatch here would not be an ordinary logic bug: it would mean vector state
// is not surviving context switches, which is worth distinguishing from a
// crash because it corrupts data silently.
func TestSIMDMatchesScalarUnderContention(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test; skipped under -short")
	}
	if !simdActive() {
		t.Skip("SIMD hasher not active on this platform; nothing to compare")
	}

	prev := bmt.SIMDOptIn()
	t.Cleanup(func() { bmt.SetSIMDOptIn(prev) })

	const workers = 32
	const iters = 1500
	var mismatches atomic.Int64

	anchor := make([]byte, swarm.HashSize)
	if _, err := rand.Read(anchor); err != nil {
		t.Fatal(err)
	}

	sum := func(simd bool, anchor, span, data []byte) []byte {
		bmt.SetSIMDOptIn(simd)
		h := bmt.NewPrefixHasher(anchor)
		h.Reset()
		h.SetHeader(span)
		_, _ = h.Write(data)
		return h.Sum(nil)
	}

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			span := make([]byte, bmt.SpanSize)
			for i := range iters {
				n := (seed*7919 + i*131) % (swarm.ChunkSize + 1)
				data := make([]byte, n)
				if _, err := rand.Read(data); err != nil {
					return
				}
				if !bytes.Equal(sum(true, anchor, span, data), sum(false, anchor, span, data)) {
					mismatches.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()

	if m := mismatches.Load(); m > 0 {
		t.Fatalf("%d SIMD/scalar digest mismatches — the SIMD path is computing wrong answers", m)
	}
}
