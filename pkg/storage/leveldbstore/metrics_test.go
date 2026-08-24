// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package leveldbstore_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/storage/leveldbstore"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// gather collects every metric the store exposes into a lookup keyed by metric
// name, with each sample's label set flattened onto it.
func gather(t *testing.T, st *leveldbstore.Store) map[string]map[string]float64 {
	t.Helper()

	reg := prometheus.NewPedanticRegistry()
	for _, c := range st.Metrics() {
		if err := reg.Register(c); err != nil {
			t.Fatalf("register collector: %v", err)
		}
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	out := make(map[string]map[string]float64)
	for _, f := range families {
		samples := make(map[string]float64)
		for _, mtr := range f.GetMetric() {
			key := ""
			for _, l := range mtr.GetLabel() {
				key = l.GetValue()
			}
			samples[key] = value(mtr)
		}
		out[f.GetName()] = samples
	}
	return out
}

func value(m *dto.Metric) float64 {
	if g := m.GetGauge(); g != nil {
		return g.GetValue()
	}
	return m.GetCounter().GetValue()
}

// TestLevel0DepthIsReadableAboveTen is the point of this collector.
//
// The pre-existing leveldb_stats HistogramVec declares no buckets, so it uses
// prometheus.DefBuckets, which end at 10. Level-0 depth matters at 4, 8 and 12
// — compaction start, write slowdown, write pause — so under that metric a
// database with writes blocked at 12 is indistinguishable from one sitting
// comfortably at 11, or 40. See issue #24.
//
// Compaction is disabled here by setting the trigger absurdly high, so level 0
// grows without bound and the depth can be checked well past the point the
// histogram stops resolving.
func TestLevel0DepthIsReadableAboveTen(t *testing.T) {
	t.Parallel()

	st, _, err := leveldbstore.New(t.TempDir(), &opt.Options{
		// Never compact, so L0 files accumulate.
		CompactionL0Trigger: 1 << 20,
		// Rotate the memtable often, so each small write batch becomes a file.
		WriteBuffer: 1 << 10,
		// Both of these must be raised alongside the trigger, or this test
		// hangs: writes block at WriteL0PauseTrigger waiting for a compaction
		// that the raised trigger never starts. That is issue #24 in miniature,
		// and TestWritesBlockWhenLevel0OutrunsCompaction below reproduces it on
		// purpose.
		WriteL0SlowdownTrigger: 1 << 20,
		WriteL0PauseTrigger:    1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	db := st.DB()
	for i := 0; i < 400; i++ {
		key := []byte(fmt.Sprintf("k%06d", i))
		if err := db.Put(key, make([]byte, 512), nil); err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately no CompactRange here: it merges level 0 into level 1, which
	// is the opposite of what this test needs. The writes above overflow the
	// 1 KiB write buffer repeatedly, and each rotation lands a file in level 0.

	got := gather(t, st)

	tables, ok := got["bee_leveldb_level_tables"]
	if !ok {
		t.Fatalf("bee_leveldb_level_tables was not exposed; got %v", keys(got))
	}
	l0, ok := tables["0"]
	if !ok {
		t.Fatalf("no sample for level 0; got levels %v", tables)
	}
	if l0 <= 0 {
		t.Fatalf("level 0 depth reported as %v, expected the files just written", l0)
	}
	t.Logf("level 0 depth reported as %v", l0)
}

// TestWritePausedIsExposed covers the field the previous sampling loop dropped
// entirely. It is the one that says, directly, that writes are blocked waiting
// on compaction — the state that presents as a stalled node.
func TestWritePausedIsExposed(t *testing.T) {
	t.Parallel()

	st, _, err := leveldbstore.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	got := gather(t, st)
	paused, ok := got["bee_leveldb_write_paused"]
	if !ok {
		t.Fatalf("bee_leveldb_write_paused was not exposed; got %v", keys(got))
	}
	if v := paused[""]; v != 0 {
		t.Errorf("an idle store reports writes paused (%v); the metric is inverted or misread", v)
	}
}

// TestMetricFamiliesArePresent guards the set as a whole. A collector that
// silently stops emitting a family is indistinguishable from a healthy database
// on a dashboard, which is how a metric gap goes unnoticed.
func TestMetricFamiliesArePresent(t *testing.T) {
	t.Parallel()

	st, _, err := leveldbstore.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// An empty database has no levels, so the per-level families are legitimately
	// absent. Put something on disk first, or this asserts the wrong thing.
	if err := st.DB().Put([]byte("k"), make([]byte, 64), nil); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}

	got := gather(t, st)
	for _, want := range []string{
		"bee_leveldb_level_tables",
		"bee_leveldb_level_size_bytes",
		"bee_leveldb_write_delay_total",
		"bee_leveldb_write_delay_seconds_total",
		"bee_leveldb_write_paused",
		"bee_leveldb_compactions_total",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing metric family %q", want)
		}
	}

	if kinds := got["bee_leveldb_compactions_total"]; len(kinds) != 4 {
		t.Errorf("expected four compaction kinds, got %v", kinds)
	}
}

// TestCollectAfterCloseIsQuiet checks the shutdown path. Collect runs on every
// scrape, including during shutdown, and must not panic or report a closed
// database as a scrape error.
func TestCollectAfterCloseIsQuiet(t *testing.T) {
	t.Parallel()

	st, _, err := leveldbstore.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reg := prometheus.NewPedanticRegistry()
	for _, c := range st.Metrics() {
		if err := reg.Register(c); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gathering from a closed store reported an error: %v", err)
	}
	if len(families) != 0 {
		t.Errorf("a closed store emitted %d metric families, expected none", len(families))
	}
}

// TestWritesBlockWhenLevel0OutrunsCompaction reproduces the failure behind issue
// #24, in miniature and in about a second.
//
// goleveldb governs level 0 with three thresholds: compaction starts at
// CompactionL0Trigger, writes are slowed at WriteL0SlowdownTrigger, and writes
// BLOCK at WriteL0PauseTrigger. A blocked writer waits in compTriggerWait, which
// has no timeout — its only exits are a compaction error or the database
// closing.
//
// Here compaction is disabled outright, which is the limiting case of setting
// the trigger too high relative to the other two. wasp ships
// CompactionL0Trigger: 8 against the default pause at 12, so it has four files
// of headroom rather than eight; this test removes the headroom entirely to show
// what the end of it looks like.
//
// The point for the metrics is the assertion at the end: write_paused reports
// this state, and nothing in the previous instrumentation did.
func TestWritesBlockWhenLevel0OutrunsCompaction(t *testing.T) {
	t.Parallel()

	st, _, err := leveldbstore.New(t.TempDir(), &opt.Options{
		CompactionL0Trigger: 1 << 20, // never compact
		WriteBuffer:         1 << 10, // rotate often, so files pile up fast
		// Slowdown and pause left at their defaults, 8 and 12.
	})
	if err != nil {
		t.Fatal(err)
	}
	db := st.DB()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5000; i++ {
			if err := db.Put([]byte(fmt.Sprintf("k%06d", i)), make([]byte, 512), nil); err != nil {
				return // closed underneath us at cleanup; expected
			}
		}
	}()

	t.Cleanup(func() {
		// Close the underlying database rather than the Store, and the reason is
		// a finding in its own right: Store.Close deletes the dirty-shutdown
		// marker, that delete is a write, and it blocks on exactly the pause this
		// test creates. Confirmed from a goroutine dump — Close sits in
		// leveldb.(*DB).putRec. So a node whose writes are paused cannot shut
		// down cleanly either; the marker survives and the next start runs
		// recovery. Worth knowing when reading #24.
		//
		// leveldb.Close does not write. It closes closeC, which releases the
		// blocked writer with ErrClosed and frees the file handles. Waiting for
		// that writer matters on Windows, where TempDir cleanup fails while any
		// handle is still open.
		_ = db.Close()
		<-done
	})

	// Wait for the pause to be observable rather than assuming it happens within
	// some interval. An earlier version inferred "blocked" from "the writer has
	// not finished in three seconds", which held on a fast machine and failed on
	// a Windows runner that had only reached depth 5 by then. That is a latency
	// assertion wearing a liveness assertion's clothes — the same mistake as
	// issue #99.
	var (
		got    map[string]map[string]float64
		paused float64
		depth  float64
	)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatal("writes completed with compaction disabled; the pause trigger never " +
				"engaged, so this test is no longer reproducing what it claims to")
		default:
		}

		got = gather(t, st)
		paused = got["bee_leveldb_write_paused"][""]
		depth = got["bee_leveldb_level_tables"]["0"]
		if paused == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if paused != 1 {
		t.Fatalf("writes never paused within the deadline (level 0 depth reached %v, "+
			"pause trigger is 12); either compaction ran despite being disabled, or "+
			"write_paused does not report the state it exists for", depth)
	}

	if depth := got["bee_leveldb_level_tables"]["0"]; depth < 12 {
		t.Errorf("level 0 depth reads %v, expected at least the pause trigger of 12", depth)
	} else {
		t.Logf("level 0 depth %v, writes paused", depth)
	}
}

func keys(m map[string]map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
