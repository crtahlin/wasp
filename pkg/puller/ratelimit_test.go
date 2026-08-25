// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package puller_test

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/puller"
)

// TestDefaultMaxChunksPerSecond pins the default at the value the constant held
// before it became configurable.
//
// The point of making a tuning constant configurable is that nothing changes
// until an operator asks for it. A default that quietly drifted would turn a
// configuration change into a behaviour change for every node that upgraded,
// which is the opposite of the intent. See issue #26 and rule 8 in AGENTS.md.
func TestDefaultMaxChunksPerSecond(t *testing.T) {
	t.Parallel()

	if puller.DefaultMaxChunksPerSecond != 1000 {
		t.Errorf("default inbound sync rate is %d, was 1000 before it became "+
			"configurable; changing the default is a behaviour change for every "+
			"node, not a configuration change", puller.DefaultMaxChunksPerSecond)
	}
}
