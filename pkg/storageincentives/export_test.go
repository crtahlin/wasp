// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storageincentives

import "context"

// IsPlaying runs the agent's play check with its retries (#773).
func (a *Agent) IsPlaying(ctx context.Context, depth uint8) (bool, error) {
	return a.isPlaying(ctx, depth)
}

var (
	NewEvents           = newEvents
	SampleChunk         = sampleChunk
	MakeInclusionProofs = makeInclusionProofs
	SampleFlightKey     = sampleFlightKey
)
