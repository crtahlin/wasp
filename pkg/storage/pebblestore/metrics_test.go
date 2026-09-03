// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pebblestore_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/ethersphere/bee/v2/pkg/storage/pebblestore"
	"github.com/prometheus/client_golang/prometheus"
)

// writeBytesByCause gathers the pebble_write_bytes_total counter from the store's
// collectors and returns the per-cause values (flushed, compacted).
func writeBytesByCause(t *testing.T, s *pebblestore.Store) map[string]float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	for _, c := range s.Metrics() {
		reg.MustRegister(c)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != "bee_pebble_write_bytes_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			var cause string
			for _, l := range m.GetLabel() {
				if l.GetName() == "cause" {
					cause = l.GetValue()
				}
			}
			out[cause] = m.GetCounter().GetValue()
		}
	}
	return out
}

// TestWriteBytesMetric checks that the write-amplification counter added for
// issues #14/#217 reports the engine's physical write. It writes incompressible
// data, forces a flush and a full compaction, and asserts both causes are
// present and the flushed bytes are non-zero — the number a node run reads to
// tell reserve-growth compaction from churn-driven compaction.
func TestWriteBytesMetric(t *testing.T) {
	t.Parallel()

	s, err := pebblestore.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Incompressible values so the physical write reflects the data, not snappy
	// collapsing an all-zero buffer.
	rng := rand.New(rand.NewSource(1))
	val := make([]byte, 4096)
	for i := 0; i < 4000; i++ {
		_, _ = rng.Read(val)
		if err := s.DB().Set([]byte(fmt.Sprintf("k%09d", i)), val, pebble.Sync); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}
	if err := s.DB().Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	full := bytes.Repeat([]byte{0xff}, 40)
	if err := s.DB().Compact([]byte{0x00}, full, true); err != nil {
		t.Fatalf("compact: %v", err)
	}

	got := writeBytesByCause(t, s)
	if _, ok := got["flushed"]; !ok {
		t.Fatal("write_bytes_total{cause=flushed} not reported")
	}
	if _, ok := got["compacted"]; !ok {
		t.Fatal("write_bytes_total{cause=compacted} not reported")
	}
	if got["flushed"] <= 0 {
		t.Fatalf("flushed bytes = %.0f, want > 0 after writing 4000 incompressible values", got["flushed"])
	}
}
