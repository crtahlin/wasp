// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && amd64 && !purego

package keccak

// Sum256x4 computes 4 Keccak-256 hashes in parallel using AVX2.
//
// All non-nil inputs MUST have the same length; nil (or zero-length) lanes
// are allowed as partial-batch fillers whose output must be ignored. See the
// package doc for the rationale.
//
// "Ignored" understates it once any active lane reaches the 136-byte rate. The
// wrapper absorbs in lockstep and permutes all lanes together, so a filler lane
// is permuted without having absorbed anything and its digest is not the
// empty-message digest, nor any other defined value. Do not compare it, cache
// it, or carry it forward.
//
// Only compiled on linux/amd64 (the only platform where the XKCP .syso is
// linkable). Call sites that may run on other platforms must be gated on the
// same build tag or on keccak.HasSIMD() at runtime.
func Sum256x4(inputs [4][]byte) [4]Hash256 {
	var outputs [4]Hash256
	var inputsCopy [4][]byte
	copy(inputsCopy[:], inputs[:])
	keccak256x4(&inputsCopy, &outputs)
	return outputs
}

// Sum256x8 computes 8 Keccak-256 hashes in parallel using AVX-512.
// Must only be called on AVX-512-capable hardware.
//
// All non-nil inputs MUST have the same length; nil (or zero-length) lanes
// are allowed as partial-batch fillers whose output must be ignored. See the
// package doc for the rationale.
//
// "Ignored" understates it once any active lane reaches the 136-byte rate. The
// wrapper absorbs in lockstep and permutes all lanes together, so a filler lane
// is permuted without having absorbed anything and its digest is not the
// empty-message digest, nor any other defined value. Do not compare it, cache
// it, or carry it forward.
//
// Only compiled on linux/amd64 (the only platform where the XKCP .syso is
// linkable). Call sites that may run on other platforms must be gated on the
// same build tag or on keccak.HasAVX512() at runtime.
func Sum256x8(inputs [8][]byte) [8]Hash256 {
	var outputs [8]Hash256
	var inputsCopy [8][]byte
	copy(inputsCopy[:], inputs[:])
	keccak256x8(&inputsCopy, &outputs)
	return outputs
}
