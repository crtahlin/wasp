// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node_test

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/node"
)

// TestOptionalIntKeepsUnsetDistinctFromZero pins the conversion between a
// zero-means-unset configuration value and the pointer kademlia's options use.
//
// The failure this prevents is severe and silent. Kademlia treats a nil option
// as "use my default"; a non-nil pointer to zero is a saturation limit of zero,
// which means every bin is saturated, so the node connects to nobody and
// reports itself healthy while doing it. An operator who simply does not set
// the flag must never reach that state.
//
// See issue #61.
func TestOptionalIntKeepsUnsetDistinctFromZero(t *testing.T) {
	t.Parallel()

	for _, unset := range []int{0, -1, -18} {
		if got := node.OptionalInt(unset); got != nil {
			t.Fatalf("OptionalInt(%d) returned a pointer to %d; kademlia would take that as a real limit "+
				"rather than as no preference, and a limit of zero saturates every bin", unset, *got)
		}
	}

	for _, set := range []int{1, 8, 18, 64} {
		got := node.OptionalInt(set)
		if got == nil {
			t.Fatalf("OptionalInt(%d) returned nil; the setting would be silently ignored", set)
		}
		if *got != set {
			t.Fatalf("OptionalInt(%d) returned %d", set, *got)
		}
	}
}
