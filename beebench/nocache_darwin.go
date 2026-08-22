// Copyright 2026 The bee-experimental Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin

package beebench

import (
	"os"

	"golang.org/x/sys/unix"
)

// dropCache asks the kernel not to cache this file's pages.
//
// Nothing calls this today — every openShards call site passes nocache=false.
// See nocache_linux.go for how the two platforms diverge if it is enabled.
//
// F_NOCACHE is darwin's nearest equivalent to O_DIRECT. Note that it is NOT
// reliable for the unaligned reads this harness performs — the shard slot size
// of 4201 bytes is not a multiple of the 4096-byte page size, so every chunk
// read spans two pages and stays cached in practice. Building a corpus larger
// than RAM remains the only dependable way to force reads to the device on
// macOS. See the methodology notes in the storage-layer briefing.
func dropCache(f *os.File) error {
	_, err := unix.FcntlInt(f.Fd(), unix.F_NOCACHE, 1)
	return err
}
