// Copyright 2026 The bee-experimental Authors. All rights reserved.
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
