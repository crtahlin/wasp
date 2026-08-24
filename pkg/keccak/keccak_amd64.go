// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && amd64 && !purego

package keccak

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

// scratchStackSize is the stack handed to the XKCP blob.
//
// The blobs' worst-case requirement is 11484 bytes for the 4-lane object and
// 12229 for the 8-lane one. Those figures are the sum of every static stack
// allocation site in the object — the immediates of all 11 `sub $imm,%rsp`
// instructions, plus 8 bytes per push, plus the worst-case slack of each
// alignment mask — and they are ceilings rather than estimates because neither
// object contains a single register-based stack adjustment, so no path can
// consume more than the sum of what is written in the instruction stream.
// Summing across functions rather than along the deepest call chain makes the
// bound loose, which is the direction it should be loose in.
//
// 64 KiB leaves better than five times that headroom and costs nothing per
// call, since the buffers are pooled. canaryLen of it is the canary.
const scratchStackSize = 64 * 1024

// The blob's stack grows downward from the high end of the buffer, so anything
// that outgrows it writes over the low end first. Poison the low canaryLen
// bytes and check them after every call: that converts an overflow from silent
// corruption of neighbouring heap objects — exactly the failure #92 was — into
// a panic naming the cause.
//
// This is not hypothetical maintenance. The .s and .syso files are generated,
// and issue #92 records that regenerating them from a newer XKCP is expected;
// a future blob that needs more stack would otherwise fail exactly as the
// original bug did, and cost the same weeks to find.
const canaryLen = 64

var canary = func() (c [canaryLen]byte) {
	for i := range c {
		c[i] = 0x5a
	}
	return
}()

// scratchStacks supplies the stacks the blob runs on.
//
// These are plain byte slices, so they contain no pointers, are never scanned
// by the collector, and — unlike a goroutine stack — are never moved or grown
// underneath the code using them. That is the entire point: see #92.
var scratchStacks = sync.Pool{
	New: func() any {
		s := make([]byte, scratchStackSize)
		copy(s, canary[:])
		return &s
	},
}

//go:noescape
func keccak256x4asm(inputs *[4][]byte, outputs *[4]Hash256, scratch unsafe.Pointer) uintptr

//go:noescape
func keccak256x8asm(inputs *[8][]byte, outputs *[8]Hash256, scratch unsafe.Pointer) uintptr

// scratchTop is where the blob's stack pointer starts. The stack grows
// downward, so hand over the top of the buffer.
const scratchTop = scratchStackSize - canaryLen

func keccak256x4(inputs *[4][]byte, outputs *[4]Hash256) {
	sp := scratchStacks.Get().(*[]byte)
	buf := *sp
	skew := keccak256x4asm(inputs, outputs, unsafe.Pointer(&buf[scratchTop]))
	// runtime.KeepAlive is defensive documentation rather than the operative
	// mechanism: the compiler spills sp into its own frame, inside its pointer
	// map, and holds it across the call, so the backing array is reachable
	// throughout. Stating the requirement beats relying on that, because the
	// requirement is real — the assembly is //go:noescape, so nothing in the
	// blob's use of the buffer keeps it alive.
	checkScratch(buf, skew)
	runtime.KeepAlive(sp)
	scratchStacks.Put(sp)
}

func keccak256x8(inputs *[8][]byte, outputs *[8]Hash256) {
	sp := scratchStacks.Get().(*[]byte)
	buf := *sp
	skew := keccak256x8asm(inputs, outputs, unsafe.Pointer(&buf[scratchTop]))
	checkScratch(buf, skew)
	runtime.KeepAlive(sp)
	scratchStacks.Put(sp)
}

// checkScratch verifies the two properties the scratch-stack scheme depends on
// and that no test of hash output can observe, because the digests are correct
// either way.
//
// skew is the difference between the two independently saved copies of the
// goroutine stack pointer; see the assembly. Non-zero means the blob violated
// the SysV ABI.
//
// The buffer is deliberately not returned to the pool on either failure. Both
// mean memory outside the buffer may already have been written, so the process
// is not in a state worth continuing from, and a poisoned buffer must not be
// handed to the next caller as if it were clean.
func checkScratch(buf []byte, skew uintptr) {
	if skew != 0 {
		panic(fmt.Sprintf("keccak: SIMD blob moved the stack pointer by %d bytes "+
			"and did not put it back; it violated the SysV ABI (see issue #92)", int64(skew)))
	}
	if *(*[canaryLen]byte)(unsafe.Pointer(&buf[0])) != canary {
		panic(fmt.Sprintf("keccak: SIMD blob overflowed its %d-byte scratch stack "+
			"and corrupted the heap below it (see issue #92)", scratchStackSize))
	}
}
