// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pebblestore

import (
	"strconv"

	m "github.com/ethersphere/bee/v2/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// statsCollector exposes Pebble's own statistics as Prometheus series, parallel
// to the ones leveldbstore exposes, so a node on either engine can be compared
// on the same dashboards. Without this a Pebble-backed index store would export
// no storage metrics at all, and the storage-engine A/B (issue #185) turns on
// exactly these numbers — level-0 depth over time, compaction progress, and
// whether writes are stalling.
//
// Collected at scrape time, like the leveldbstore collector, so a level-0 spike
// that starts and clears between scrapes is not missed.
//
// Note the write-stall signal differs from goleveldb's. goleveldb exposes a live
// `WritePaused` boolean; Pebble 1.1.5 has no aggregate write-stall counter on its
// Metrics, so the stall signal here is the level-0 file count — when it sits at
// the configured stop-writes threshold, writes are blocked. The storer derives a
// stalled boolean from that count against the threshold it configured.
type statsCollector struct {
	store *Store

	levelFiles     *prometheus.Desc
	levelSize      *prometheus.Desc
	compactions    *prometheus.Desc
	compactionDebt *prometheus.Desc
}

func newStatsCollector(store *Store) *statsCollector {
	const subsystem = "pebble"
	fq := func(name string) string {
		return prometheus.BuildFQName(m.Namespace, subsystem, name)
	}
	return &statsCollector{
		store: store,
		levelFiles: prometheus.NewDesc(fq("level_files"),
			"Number of SST files at each level. Level 0 is the one that gates writes: "+
				"Pebble starts compacting at L0CompactionThreshold and blocks writes at "+
				"L0StopWritesThreshold. A level-0 count pinned at the stop threshold is a "+
				"stalled node, the Pebble equivalent of goleveldb's write_paused.",
			[]string{"level"}, nil),
		levelSize: prometheus.NewDesc(fq("level_size_bytes"),
			"Total size of SST files at each level, in bytes.",
			[]string{"level"}, nil),
		compactions: prometheus.NewDesc(fq("compactions_total"),
			"Cumulative compactions performed, by what triggered them.",
			[]string{"kind"}, nil),
		compactionDebt: prometheus.NewDesc(fq("compaction_debt_bytes"),
			"Estimated bytes that compaction still needs to rewrite to reach a stable "+
				"shape. A growing value means compaction is falling behind the write rate, "+
				"which is what precedes a stall.",
			nil, nil),
	}
}

func (c *statsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.levelFiles
	ch <- c.levelSize
	ch <- c.compactions
	ch <- c.compactionDebt
}

func (c *statsCollector) Collect(ch chan<- prometheus.Metric) {
	// Metrics is cheap and never errors; a closed DB simply reports zeros. There
	// is no closed-DB error to guard the way leveldbstore's Stats has.
	stats := c.store.db.Metrics()

	for level := range stats.Levels {
		l := &stats.Levels[level]
		ch <- prometheus.MustNewConstMetric(c.levelFiles, prometheus.GaugeValue,
			float64(l.NumFiles), strconv.Itoa(level))
		ch <- prometheus.MustNewConstMetric(c.levelSize, prometheus.GaugeValue,
			float64(l.Size), strconv.Itoa(level))
	}

	for kind, n := range map[string]int64{
		"default":      stats.Compact.DefaultCount,
		"delete_only":  stats.Compact.DeleteOnlyCount,
		"elision_only": stats.Compact.ElisionOnlyCount,
		"move":         stats.Compact.MoveCount,
		"read":         stats.Compact.ReadCount,
		"rewrite":      stats.Compact.RewriteCount,
		"multilevel":   stats.Compact.MultiLevelCount,
	} {
		ch <- prometheus.MustNewConstMetric(c.compactions, prometheus.CounterValue,
			float64(n), kind)
	}

	ch <- prometheus.MustNewConstMetric(c.compactionDebt, prometheus.GaugeValue,
		float64(stats.Compact.EstimatedDebt))
}

// Metrics returns the collectors this store exposes, satisfying the same
// m.Collector interface leveldbstore does, so a Pebble index store is wired into
// the node's registry with no change at the call site.
func (s *Store) Metrics() []prometheus.Collector {
	return []prometheus.Collector{newStatsCollector(s)}
}

// Level0Files reports the number of level-0 SST files, the value that gates
// writes. Both store engines expose this so the storer can watch write-stall
// risk without knowing which engine it holds. See issue #185.
func (s *Store) Level0Files() int {
	return int(s.db.Metrics().Levels[0].NumFiles)
}
