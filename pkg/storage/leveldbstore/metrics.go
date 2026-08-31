// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package leveldbstore

import (
	"errors"
	"strconv"

	m "github.com/ethersphere/bee/v2/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/syndtr/goleveldb/leveldb"
)

// statsCollector exposes goleveldb's own statistics with metric types that can
// answer questions about them.
//
// The existing `leveldb_stats` HistogramVec in pkg/storer already observes most
// of these values, but it is a single histogram shared by every statistic and
// declares no buckets, so it inherits prometheus.DefBuckets — which top out at
// 10. Level-0 table count matters at 4, 8 and 12 (compaction start, write
// slowdown, write pause), so the one value that distinguishes "healthy" from
// "writes are blocked" lands in the same +Inf bucket as everything else above
// ten. Byte counters and cache sizes are worse still. See issue #24.
//
// Collecting at scrape time rather than on a ticker matters here too. The
// previous sampling loop reads every 15 seconds, so a level-0 spike that starts
// and clears between samples is invisible — and a spike is exactly the event
// being looked for.
type statsCollector struct {
	db *leveldb.DB

	levelTables   *prometheus.Desc
	levelSize     *prometheus.Desc
	writeDelays   *prometheus.Desc
	writeDelaySec *prometheus.Desc
	writePaused   *prometheus.Desc
	compactions   *prometheus.Desc
}

func newStatsCollector(db *leveldb.DB) *statsCollector {
	const subsystem = "leveldb"
	fq := func(name string) string {
		return prometheus.BuildFQName(m.Namespace, subsystem, name)
	}
	return &statsCollector{
		db: db,
		levelTables: prometheus.NewDesc(fq("level_tables"),
			"Number of SST files at each level. Level 0 is the one that gates writes: "+
				"goleveldb starts compacting at CompactionL0Trigger, slows writes at "+
				"WriteL0SlowdownTrigger, and blocks them at WriteL0PauseTrigger.",
			[]string{"level"}, nil),
		levelSize: prometheus.NewDesc(fq("level_size_bytes"),
			"Total size of SST files at each level, in bytes.",
			[]string{"level"}, nil),
		writeDelays: prometheus.NewDesc(fq("write_delay_total"),
			"Cumulative number of times a write was delayed because level 0 was too deep. "+
				"Cumulative, so unlike a sampled gauge this cannot miss a short episode.",
			nil, nil),
		writeDelaySec: prometheus.NewDesc(fq("write_delay_seconds_total"),
			"Cumulative time writes spent delayed because level 0 was too deep.",
			nil, nil),
		writePaused: prometheus.NewDesc(fq("write_paused"),
			"1 while writes are blocked waiting for compaction, 0 otherwise. This is the "+
				"state that presents as a stalled node: the waiting writer has no timeout.",
			nil, nil),
		compactions: prometheus.NewDesc(fq("compactions_total"),
			"Cumulative compactions performed, by what triggered them.",
			[]string{"kind"}, nil),
	}
}

func (c *statsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.levelTables
	ch <- c.levelSize
	ch <- c.writeDelays
	ch <- c.writeDelaySec
	ch <- c.writePaused
	ch <- c.compactions
}

func (c *statsCollector) Collect(ch chan<- prometheus.Metric) {
	stats := new(leveldb.DBStats)
	// A closed database is not an error worth reporting on every scrape; emit
	// nothing and let the absence of the series speak.
	if err := c.db.Stats(stats); err != nil {
		if !errors.Is(err, leveldb.ErrClosed) {
			ch <- prometheus.NewInvalidMetric(c.levelTables, err)
		}
		return
	}

	for level, count := range stats.LevelTablesCounts {
		ch <- prometheus.MustNewConstMetric(c.levelTables, prometheus.GaugeValue,
			float64(count), strconv.Itoa(level))
	}
	for level, size := range stats.LevelSizes {
		ch <- prometheus.MustNewConstMetric(c.levelSize, prometheus.GaugeValue,
			float64(size), strconv.Itoa(level))
	}

	ch <- prometheus.MustNewConstMetric(c.writeDelays, prometheus.CounterValue,
		float64(stats.WriteDelayCount))
	ch <- prometheus.MustNewConstMetric(c.writeDelaySec, prometheus.CounterValue,
		stats.WriteDelayDuration.Seconds())

	paused := 0.0
	if stats.WritePaused {
		paused = 1
	}
	ch <- prometheus.MustNewConstMetric(c.writePaused, prometheus.GaugeValue, paused)

	for kind, n := range map[string]uint32{
		"memtable":   stats.MemComp,
		"level0":     stats.Level0Comp,
		"non_level0": stats.NonLevel0Comp,
		"seek":       stats.SeekComp,
	} {
		ch <- prometheus.MustNewConstMetric(c.compactions, prometheus.CounterValue,
			float64(n), kind)
	}
}

// Metrics returns the collectors this store exposes.
func (s *Store) Metrics() []prometheus.Collector {
	return []prometheus.Collector{newStatsCollector(s.db)}
}

// Level0Files reports the number of level-0 SST files, the value that gates
// writes. Both store engines expose this so the storer can watch write-stall
// risk without knowing which engine it holds. See issue #185.
func (s *Store) Level0Files() int {
	stats := new(leveldb.DBStats)
	if err := s.db.Stats(stats); err != nil || len(stats.LevelTablesCounts) == 0 {
		return 0
	}
	return stats.LevelTablesCounts[0]
}
