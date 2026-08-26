// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package leveldbstore

// syncDir does nothing on Windows.
//
// There is no directory-fsync equivalent: opening a directory as a file and
// calling FlushFileBuffers on it fails with "Access is denied", which is what
// the portable version of this did until CI ran it. NTFS makes directory
// metadata durable through its own journal rather than through an explicit
// flush by the writer, so there is nothing for a caller to do here.
//
// The marker file itself is still synced on every platform. Only the extra
// guarantee about the directory entry is unavailable.
func syncDir(string) error { return nil }
