// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer_test

import (
	"context"
	"testing"
	"time"

	chunk "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/prometheus/client_golang/prometheus"
)

// TestReserveScanIsTimed asserts that a pass over the reserve index records its
// duration, and that the two kinds of pass are recorded separately.
//
// The point of the metric is to answer whether the scan this issue complains
// about is expensive, and to make the split that follows comparable against the
// combined pass it replaces. Both of those need the observations to actually
// arrive under the right label. A histogram that is declared and never observed
// reads as "the scan takes no time", which is the wrong answer rather than a
// missing one. See issue #28.
func TestReserveScanIsTimed(t *testing.T) {
	t.Parallel()

	baseAddr := swarm.RandAddress(t)
	opts := dbTestOps(baseAddr, 1000, nil, nil, time.Second)
	opts.ValidStamp = func(ch swarm.Chunk) (swarm.Chunk, error) { return ch, nil }

	db, err := diskStorer(t, opts)()
	if err != nil {
		t.Fatal(err)
	}

	putter := db.ReservePutter()
	for po := range 4 {
		for range 10 {
			ch := chunk.GenerateValidRandomChunkAt(t, baseAddr, po).WithBatch(3, 2, false)
			if err := putter.Put(context.Background(), ch); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The combined pass, which counts and reconciles in one iteration.
	if _, err := db.CountWithinRadius(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The count-only pass, reached through DebugInfo.
	if _, err := db.DebugInfo(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"combined", "count"} {
		if got := histogramCount(t, db.ReserveScanDuration(), kind); got == 0 {
			t.Fatalf("no %q pass was timed; the histogram exists but nothing observes it, "+
				"which reports the scan as taking no time rather than as unmeasured", kind)
		}
	}
}

// histogramCount reports the number of observations recorded against one label
// value of a histogram vector.
func histogramCount(t *testing.T, vec *prometheus.HistogramVec, label string) uint64 {
	t.Helper()

	reg := prometheus.NewRegistry()
	if err := reg.Register(vec); err != nil {
		t.Fatal(err)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, pair := range metric.GetLabel() {
				if pair.GetValue() == label {
					return metric.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	return 0
}
