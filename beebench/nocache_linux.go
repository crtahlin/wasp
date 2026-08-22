// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package beebench

import (
	"os"

	"golang.org/x/sys/unix"
)

// dropCache evicts this file's pages from the page cache.
//
// NOT EQUIVALENT TO THE DARWIN VERSION, and the difference is in kind rather
// than spelling. F_NOCACHE sets a persistent flag on the descriptor, so future
// reads bypass the cache. POSIX_FADV_DONTNEED is a ONE-SHOT eviction of pages
// already resident; subsequent reads repopulate the cache normally. A caller
// that opens with nocache and then reads in a loop gets cold reads on darwin
// and warm reads on linux after the first pass.
//
// Nothing calls this today — every openShards call site passes nocache=false —
// so the difference is currently latent. Anyone enabling it must size the
// corpus larger than RAM rather than relying on the hint.
//
// Linux has no F_NOCACHE. POSIX_FADV_DONTNEED is the equivalent intent: it asks
// the kernel to drop the cached pages for the range, so subsequent reads reach
// the device. Unlike O_DIRECT it imposes no alignment requirement, which suits
// this harness because the shard slot size of 4201 bytes is deliberately not a
// multiple of the page size.
//
// It is advisory. Pages that are dirty or otherwise pinned may survive, so a
// corpus larger than RAM is still the dependable way to force device reads.
func dropCache(f *os.File) error {
	return unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
}
