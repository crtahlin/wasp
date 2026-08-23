// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && amd64 && !purego

package keccak

// Keccak256x4Raw exposes the raw assembly entry point to the external test
// package, so a test can control the exact memory surrounding the output array
// and detect an out-of-bounds write by the XKCP blob.
//
// Sum256x4 cannot be used for this: it returns the output array by value, so
// the caller has no control over where it lives.
func Keccak256x4Raw(inputs *[4][]byte, outputs *[4]Hash256) { keccak256x4(inputs, outputs) }
