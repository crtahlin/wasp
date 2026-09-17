// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval

import (
	"github.com/prometheus/client_golang/prometheus"

	m "github.com/ethersphere/bee/v2/pkg/metrics"
)

type metrics struct {
	// all metrics fields must be exported
	// to be able to return them by Metrics()
	// using reflection

	RequestCounter        prometheus.Counter
	RequestSuccessCounter prometheus.Counter
	RequestFailureCounter prometheus.Counter
	RequestDurationTime   prometheus.Histogram
	RequestAttempts       prometheus.Histogram
	PeerRequestCounter    prometheus.Counter
	TotalRetrieved        prometheus.Counter
	InvalidChunkRetrieved prometheus.Counter
	ChunkPrice            prometheus.Summary
	TotalErrors           prometheus.Counter
	ChunkRetrieveTime     prometheus.Histogram

	// content providers (wasp)
	PreferredAttempts prometheus.Counter
	PreferredHits     prometheus.Counter
	PreferredMisses   prometheus.Counter
	// PreferredOverdrafts counts preferred attempts refused for credit. Until
	// #324 this branch incremented nothing, so an operator could not see a
	// provider being refused; accounting_blocks_count is node-wide and cannot
	// attribute a refusal to one peer.
	PreferredOverdrafts prometheus.Counter
	// PreferredReadmits counts preferred peers kept for a later attempt after
	// an overdraft, rather than dropped for that chunk.
	PreferredReadmits prometheus.Counter
	LocalOnlyMisses   prometheus.Counter
	LocalOnlyLimited  prometheus.Counter
}

func newMetrics() metrics {
	subsystem := "retrieval"

	return metrics{
		RequestCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "request_count",
			Help:      "Number of requests to retrieve chunks.",
		}),
		RequestSuccessCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "request_success_count",
			Help:      "Number of requests which succeeded to retrieve chunk.",
		}),
		RequestFailureCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "request_failure_count",
			Help:      "Number of requests which failed to retrieve chunk.",
		}),
		RequestDurationTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "request_duration_time",
			Help:      "Histogram for time taken to complete retrieval request",
		}),
		RequestAttempts: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "request_attempts",
			Help:      "Histogram for total retrieval attempts pre each request.",
		}),
		PeerRequestCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "peer_request_count",
			Help:      "Number of request to single peer.",
		}),
		TotalRetrieved: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "total_retrieved",
			Help:      "Total chunks retrieved.",
		}),
		InvalidChunkRetrieved: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "invalid_chunk_retrieved",
			Help:      "Invalid chunk retrieved from peer.",
		}),
		ChunkPrice: prometheus.NewSummary(prometheus.SummaryOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "chunk_price",
			Help:      "The price of the chunk that was paid.",
		}),
		TotalErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "total_errors",
			Help:      "Total number of errors while retrieving chunk.",
		}),
		ChunkRetrieveTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "retrieve_chunk_time",
			Help:      "Histogram for time taken to retrieve a chunk.",
		},
		),
		PreferredAttempts: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "preferred_attempts",
			Help:      "Retrieval attempts sent to a preferred peer with the local-only header.",
		}),
		PreferredHits: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "preferred_hits",
			Help:      "Chunks delivered by a preferred peer.",
		}),
		PreferredMisses: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "preferred_misses",
			Help:      "Preferred attempts that did not deliver the chunk.",
		}),
		PreferredOverdrafts: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "preferred_overdrafts",
			Help:      "Preferred attempts refused because the peer could not be credited.",
		}),
		PreferredReadmits: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "preferred_readmits",
			Help:      "Preferred peers kept for a later attempt after an overdraft.",
		}),
		LocalOnlyMisses: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "local_only_misses",
			Help:      "Local-only requests from peers answered with a miss.",
		}),
		LocalOnlyLimited: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "local_only_limited",
			Help:      "Local-only misses answered with the limit error.",
		}),
	}
}

func (s *Service) Metrics() []prometheus.Collector {
	return m.PrometheusCollectorsFromFields(s.metrics)
}

// StatusMetrics exposes metrics that are exposed on the status protocol.
func (s *Service) StatusMetrics() []prometheus.Collector {
	return []prometheus.Collector{
		s.metrics.RequestAttempts,
		s.metrics.ChunkRetrieveTime,
		s.metrics.RequestDurationTime,
	}
}
