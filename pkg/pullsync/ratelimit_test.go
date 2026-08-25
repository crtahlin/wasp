// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pullsync_test

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/pullsync"
)

// TestDefaultMaxChunksPerSecond pins the per-peer default at the value the
// constant held before it became configurable. See issue #25.
func TestDefaultMaxChunksPerSecond(t *testing.T) {
	t.Parallel()

	if pullsync.DefaultMaxChunksPerSecond != 250 {
		t.Errorf("default per-peer chunk rate is %d, was 250 before it became "+
			"configurable; changing the default is a behaviour change for every "+
			"node, not a configuration change", pullsync.DefaultMaxChunksPerSecond)
	}
}
