// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer_test

import (
	"testing"

	storer "github.com/ethersphere/bee/v2/pkg/storer"
)

// TestWritePauseEdge pins the property behind issue #176's log line: the store
// pause state is reported once on each transition, not on every tick it holds.
// A warning on every tick would bury the signal; a warning on neither edge is
// the silence the issue is about.
func TestWritePauseEdge(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		prev, cur bool
		want      storer.WritePauseChange
	}{
		{"stays clear", false, false, storer.WritePauseUnchanged},
		{"enters pause", false, true, storer.WritePauseEntered},
		{"stays paused", true, true, storer.WritePauseUnchanged},
		{"leaves pause", true, false, storer.WritePauseLeft},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := storer.WritePauseEdge(tc.prev, tc.cur); got != tc.want {
				t.Fatalf("edge(%v -> %v) = %v, want %v", tc.prev, tc.cur, got, tc.want)
			}
		})
	}
}
