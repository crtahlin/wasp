// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows

package leveldbstore

import (
	"errors"
	"os"
)

// syncDir makes a directory entry durable.
//
// Creating a file writes the file's data and the directory entry that names
// it. Syncing the file covers the first; only syncing the directory covers the
// second. Without it the dirty marker can be lost by exactly the unclean
// shutdown it exists to record, which reports the store as clean when it is
// not.
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
