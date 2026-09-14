// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/storer"
)

func TestEffectiveReserveCapacity(t *testing.T) {
	def := storer.DefaultReserveCapacity
	for _, tc := range []struct {
		configured uint64
		doubling   int
		want       int
	}{
		{0, 0, def},               // unset -> default
		{0, 1, 2 * def},           // unset, doubling composes on the default
		{2097152, 0, 2097152},     // configured base
		{2097152, 1, 2 * 2097152}, // configured base composes with doubling
		{8388608, 0, 8388608},
	} {
		if got := effectiveReserveCapacity(tc.configured, tc.doubling); got != tc.want {
			t.Errorf("effectiveReserveCapacity(%d,%d)=%d, want %d", tc.configured, tc.doubling, got, tc.want)
		}
	}
}
