// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !linux && !darwin

package beebench

import "os"

// dropCache is a no-op on platforms with no page-cache hint available.
//
// Benchmarks that rely on reaching the device will silently measure the page
// cache here, so treat results from such a platform as software-overhead
// numbers only.
func dropCache(*os.File) error { return nil }
