// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package beebench

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/storage/leveldbstore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// Measures how deep level 0 gets under sustained writes, at the compaction
// trigger wasp ships against the goleveldb default. See issue #24 and
// docs/experiments/compaction-l0-trigger/spec.md.
//
// goleveldb governs level 0 with three thresholds. wasp sets only the first:
//
//	CompactionL0Trigger     4 by default, 8 in wasp   compaction starts
//	WriteL0SlowdownTrigger  8, left at the default    writes sleep 1ms each
//	WriteL0PauseTrigger    12, left at the default    writes BLOCK
//
// Raising the first to 8 means compaction does not begin until writes are
// already being throttled, leaving four files of headroom before writes stop
// rather than eight.
//
// This is a synthetic measurement, and its limits should be stated plainly. It
// exercises the index store directly, so it answers "does the trigger change
// how deep level 0 gets under equivalent write load". It does NOT answer "does
// a real node reach that load", which needs a funded bench node driving uploads
// or pullsync ingest — bench-1 currently has neither, which is why this exists.

var (
	l0Entries  = flag.Int("l0.entries", 1<<20, "retrievalIdx entries to pre-populate per arm")
	l0Writers  = flag.Int("l0.writers", 4, "concurrent writers")
	l0Duration = flag.Duration("l0.duration", 60*time.Second, "how long to write for")
	l0Rate     = flag.Int("l0.rate", 0, "cap aggregate writes per second (0 = unlimited)")
	l0Sweep    = flag.Bool("l0.sweep", false, "run the pause-threshold sweep")
)

type l0Result struct {
	trigger     int
	peakDepth   float64
	atSlowdown  int // samples with depth >= 8
	atPause     int // samples with depth >= 12
	pausedSeen  bool
	writeDelays float64
	delaySecs   float64
	l0Comps     float64
	writes      uint64
	samples     int
}

// sample reads the store's own collectors. These exist as of the level-0
// instrumentation; before it, the only source was a histogram whose buckets
// ended at 10, which cannot distinguish a healthy store from a blocked one.
func sample(tb testing.TB, st *leveldbstore.Store) (depth, paused, delays, delaySecs, l0comp float64) {
	tb.Helper()
	reg := prometheus.NewPedanticRegistry()
	for _, c := range st.Metrics() {
		if err := reg.Register(c); err != nil {
			tb.Fatalf("register collector: %v", err)
		}
	}
	families, err := reg.Gather()
	if err != nil {
		tb.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			v := m.GetGauge().GetValue()
			if m.GetGauge() == nil {
				v = m.GetCounter().GetValue()
			}
			label := ""
			for _, l := range m.GetLabel() {
				label = l.GetValue()
			}
			switch f.GetName() {
			case "bee_leveldb_level_tables":
				if label == "0" {
					depth = v
				}
			case "bee_leveldb_write_paused":
				paused = v
			case "bee_leveldb_write_delay_total":
				delays = v
			case "bee_leveldb_write_delay_seconds_total":
				delaySecs = v
			case "bee_leveldb_compactions_total":
				if label == "level0" {
					l0comp = v
				}
			}
		}
	}
	return
}

func runArm(t *testing.T, trigger int) l0Result {
	t.Helper()

	dir := filepath.Join(t.TempDir(), fmt.Sprintf("l0-trigger-%d", trigger))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Everything except the trigger matches what pkg/storer opens the index
	// store with, so the trigger is the only variable between arms.
	st, _, err := leveldbstore.New(dir, &opt.Options{
		BlockCacheCapacity:  64 << 20,
		WriteBuffer:         64 << 20,
		CompactionL0Trigger: trigger,
		Filter:              filter.NewBloomFilter(64),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	t.Logf("trigger=%d: pre-populating %d entries", trigger, *l0Entries)
	b := st.Batch(t.Context())
	for i := 0; i < *l0Entries; i++ {
		_ = b.Put(&retrItem{Address: addrOf(i), Timestamp: uint64(i), RefCnt: 1})
		if i%50_000 == 0 {
			if err := b.Commit(); err != nil {
				t.Fatal(err)
			}
			b = st.Batch(t.Context())
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}

	res := l0Result{trigger: trigger}
	_, _, baseDelays, baseSecs, baseComp := sample(t, st)

	var (
		writes  atomic.Uint64
		stop    atomic.Bool
		wg      sync.WaitGroup
		sampler sync.WaitGroup
	)

	sampler.Add(1)
	go func() {
		defer sampler.Done()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for !stop.Load() {
			<-tick.C
			depth, paused, _, _, _ := sample(t, st)
			res.samples++
			if depth > res.peakDepth {
				res.peakDepth = depth
			}
			if depth >= 8 {
				res.atSlowdown++
			}
			if depth >= 12 {
				res.atPause++
			}
			if paused == 1 {
				res.pausedSeen = true
			}
		}
	}()

	// Sustained writes past the end of the pre-populated key range, so every
	// write is an insert rather than an overwrite, as pullsync ingest would be.
	for w := 0; w < *l0Writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// Pace to the requested aggregate rate, so a run can ask "does the
			// stall appear at a rate a node could actually reach" rather than
			// only "does it appear when the store is driven as hard as the
			// machine allows".
			var perBatch time.Duration
			if *l0Rate > 0 {
				perBatch = time.Duration(float64(time.Second) * 500 * float64(*l0Writers) / float64(*l0Rate))
			}
			next := time.Now()
			for n := 0; !stop.Load(); n++ {
				if perBatch > 0 {
					next = next.Add(perBatch)
					if d := time.Until(next); d > 0 {
						time.Sleep(d)
					}
				}
				bat := st.Batch(t.Context())
				for k := 0; k < 500; k++ {
					_ = bat.Put(&retrItem{
						Address:   addrOf(*l0Entries + (w*1_000_000_000 + n*500 + k)),
						Timestamp: uint64(n),
						RefCnt:    1,
					})
				}
				if err := bat.Commit(); err != nil {
					return
				}
				writes.Add(500)
			}
		}(w)
	}

	time.Sleep(*l0Duration)
	stop.Store(true)
	wg.Wait()
	sampler.Wait()

	_, _, endDelays, endSecs, endComp := sample(t, st)
	res.writeDelays = endDelays - baseDelays
	res.delaySecs = endSecs - baseSecs
	res.l0Comps = endComp - baseComp
	res.writes = writes.Load()
	return res
}

// TestL0PauseThreshold answers the question that decides whether issue #24 is
// reachable in the field: at what sustained write rate does level 0 first reach
// the pause trigger?
//
// It matters because the unlimited run drives ~483,000 index writes per second,
// which is ~1.98 GB/s of chunk-equivalent ingest. A node on a 1 Gbit/s link can
// receive about 30,500 chunks/sec, so that run is roughly 16x beyond line rate
// and ~400x beyond realistic pullsync. Reproducing a stall there says little
// about whether a node ever gets near it.
//
// Run with -l0.sweep to enable; it is slow by construction.
func TestL0PauseThreshold(t *testing.T) {
	if !*l0Sweep {
		t.Skip("enable with -l0.sweep")
	}

	type point struct {
		rate   int
		peak   float64
		paused bool
		got    uint64
	}
	var points []point

	// Chosen to bracket what a node can actually ingest: 1 Gbit/s line rate is
	// ~30,500 chunks/sec, realistic pullsync is low thousands.
	for _, rate := range []int{2_000, 10_000, 30_000, 100_000, 300_000} {
		*l0Rate = rate
		t.Run(fmt.Sprintf("rate_%d", rate), func(t *testing.T) {
			r := runArm(t, 8) // the shipped trigger
			points = append(points, point{rate, r.peakDepth, r.pausedSeen, r.writes})
		})
	}

	t.Log("")
	t.Log("Table — Level-0 depth against sustained write rate, CompactionL0Trigger=8")
	t.Logf("  %-14s %-14s %-12s %-10s %s", "target w/s", "achieved w/s", "peak depth", "paused", "GB/s equivalent")
	for _, p := range points {
		achieved := float64(p.got) / l0Duration.Seconds()
		t.Logf("  %-14d %-14.0f %-12.0f %-10v %.2f",
			p.rate, achieved, p.peak, p.paused, achieved*4096/1e9)
	}
	t.Log("")

	var lowest *point
	for i := range points {
		if points[i].paused {
			lowest = &points[i]
			break
		}
	}
	if lowest == nil {
		t.Logf("RESULT: no tested rate up to %d writes/sec reached the pause trigger. "+
			"On this hardware the stall needs more than a node can ingest, which argues "+
			"issue #24 is not reachable in the field and its priority should be revisited.",
			points[len(points)-1].rate)
		return
	}
	gbs := float64(lowest.rate) * 4096 / 1e9
	t.Logf("RESULT: the lowest tested rate that paused writes is %d writes/sec, "+
		"which is %.2f GB/s of chunk-equivalent ingest. Compare against ~30,500 "+
		"chunks/sec for a saturated 1 Gbit/s link before treating this as reachable.",
		lowest.rate, gbs)
}

func TestL0DepthUnderSustainedWrites(t *testing.T) {
	arms := []int{8, 4} // 8 is what wasp ships; 4 is the goleveldb default
	results := make([]l0Result, 0, len(arms))

	for _, trigger := range arms {
		t.Run(fmt.Sprintf("trigger_%d", trigger), func(t *testing.T) {
			results = append(results, runArm(t, trigger))
		})
	}

	t.Log("")
	t.Log("Table — Level-0 depth under sustained writes, by CompactionL0Trigger")
	t.Logf("  writers=%d duration=%s prepopulated=%d entries",
		*l0Writers, *l0Duration, *l0Entries)
	t.Log("")
	t.Logf("  %-9s %-11s %-13s %-11s %-8s %-13s %-11s %s",
		"trigger", "peak depth", "samples >=8", "samples >=12", "paused", "write delays", "delay secs", "writes")
	for _, r := range results {
		t.Logf("  %-9d %-11.0f %-13d %-11d %-8v %-13.0f %-11.2f %d",
			r.trigger, r.peakDepth, r.atSlowdown, r.atPause, r.pausedSeen,
			r.writeDelays, r.delaySecs, r.writes)
	}
	t.Log("")

	if len(results) != 2 {
		t.Fatalf("expected two arms, got %d", len(results))
	}
	shipped, proposed := results[0], results[1]

	// The control must show the effect, or there is nothing to compare. Saying
	// so is the point: an experiment whose control cannot fail proves nothing,
	// and this is the risk the spec flagged for exactly this measurement.
	if shipped.peakDepth < 8 {
		t.Errorf("INCONCLUSIVE: the shipped trigger never drove level 0 past %d "+
			"(peak %.0f), so this load does not reach the regime the issue is about. "+
			"Raise -l0.writers, -l0.duration or -l0.entries, or accept that the "+
			"mechanism needs a heavier write rate than this machine produces.",
			8, shipped.peakDepth)
		return
	}

	// A falsified hypothesis is a result, not a broken harness, so it is
	// reported rather than failed. The INCONCLUSIVE case above is different:
	// there the measurement itself did not work, and that is a failure.
	if proposed.peakDepth >= shipped.peakDepth {
		t.Logf("RESULT: the proposed trigger did not reduce peak level-0 depth "+
			"(%.0f against %.0f). The constant is not the binding constraint at this "+
			"load; compaction throughput is, which points at issues #23 and #29 "+
			"rather than at this value.", proposed.peakDepth, shipped.peakDepth)
		if shipped.pausedSeen && proposed.pausedSeen {
			t.Logf("RESULT: both arms reached the pause trigger and blocked writes, " +
				"so the stall reproduces here but is indifferent to the trigger.")
		}
		return
	}
	t.Logf("RESULT: the proposed trigger reduced peak level-0 depth from %.0f to %.0f.",
		shipped.peakDepth, proposed.peakDepth)
}
