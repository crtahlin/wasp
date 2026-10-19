// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/log"
)

// Test 11: an unreserve increase to a radius the worker decreased from 10
// minutes earlier is a flap; 61 minutes later it is not (#822).
func TestRadiusFlapWindow(t *testing.T) {
	t.Parallel()

	t0 := time.Unix(1_000_000, 0)
	cases := []struct {
		after time.Duration
		want  float64
	}{
		{10 * time.Minute, 1},
		{61 * time.Minute, 0},
	}
	for _, c := range cases {
		t.Run(c.after.String(), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			db := &DB{metrics: newMetrics(), logger: log.NewLogger("test", log.WithSink(&buf), log.WithVerbosity(log.VerbosityInfo))}
			db.noteRadiusDecrease(9, t0)
			db.noteRadiusIncrease(8, t0.Add(c.after)) // another radius: never a flap
			db.noteRadiusIncrease(9, t0.Add(c.after))
			if got := metricValue(db.metrics.RadiusFlap); got != c.want {
				t.Fatalf("radius_flap_total %v, want %v", got, c.want)
			}
			warned := strings.Contains(buf.String(), "reserve radius increased again within an hour of a decrease from it")
			if warned != (c.want == 1) {
				t.Fatalf("warning logged %v, want %v; log:\n%s", warned, c.want == 1, buf.String())
			}
		})
	}
}
