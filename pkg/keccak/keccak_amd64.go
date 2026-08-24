// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && amd64 && !purego

package keccak

import (
	"runtime"
	"sync"
	"unsafe"
)

// scratchStackSize is the stack handed to the XKCP blob.
//
// Measured requirement is 9456 bytes for the 4-lane blob and 7528 for the
// 8-lane one, by summing every stack allocation site in each object; neither
// contains any dynamic (register-based) stack adjustment, so those sums are
// hard ceilings rather than estimates. 64 KiB leaves a wide margin and costs
// nothing per call, since the buffers are pooled.
const scratchStackSize = 64 * 1024

// scratchStacks supplies the stacks the blob runs on.
//
// These are plain byte slices, so they contain no pointers, are never scanned
// by the collector, and — unlike a goroutine stack — are never moved or grown
// underneath the code using them. That is the entire point: see #92.
var scratchStacks = sync.Pool{
	New: func() any {
		s := make([]byte, scratchStackSize)
		return &s
	},
}

//go:noescape
func keccak256x4asm(inputs *[4][]byte, outputs *[4]Hash256, scratch unsafe.Pointer)

//go:noescape
func keccak256x8asm(inputs *[8][]byte, outputs *[8]Hash256, scratch unsafe.Pointer)

func keccak256x4(inputs *[4][]byte, outputs *[4]Hash256) {
	sp := scratchStacks.Get().(*[]byte)
	// The stack grows downward, so hand over the top of the buffer.
	keccak256x4asm(inputs, outputs, unsafe.Pointer(&(*sp)[scratchStackSize-64]))
	// The assembly is //go:noescape and its frame is NO_LOCAL_POINTERS, so the
	// collector cannot see the scratch buffer while the blob is writing to it.
	// Put(sp) below happens to keep sp live, but stating the requirement beats
	// relying on that: without it, moving or removing the Put would let the
	// buffer be collected mid-call.
	runtime.KeepAlive(sp)
	scratchStacks.Put(sp)
}

func keccak256x8(inputs *[8][]byte, outputs *[8]Hash256) {
	sp := scratchStacks.Get().(*[]byte)
	keccak256x8asm(inputs, outputs, unsafe.Pointer(&(*sp)[scratchStackSize-64]))
	// The assembly is //go:noescape and its frame is NO_LOCAL_POINTERS, so the
	// collector cannot see the scratch buffer while the blob is writing to it.
	// Put(sp) below happens to keep sp live, but stating the requirement beats
	// relying on that: without it, moving or removing the Put would let the
	// buffer be collected mid-call.
	runtime.KeepAlive(sp)
	scratchStacks.Put(sp)
}
