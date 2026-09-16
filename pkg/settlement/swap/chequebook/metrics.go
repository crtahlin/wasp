// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package chequebook

import (
	m "github.com/ethersphere/bee/v2/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	ChainReads        prometheus.Counter
	ChainReadsAvoided prometheus.Counter
}

func newMetrics() metrics {
	subsystem := "chequestore"

	return metrics{
		ChainReads: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "chain_reads",
			Help:      "Chain calls made while verifying received cheques",
		}),
		ChainReadsAvoided: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "chain_reads_avoided",
			Help:      "Chain calls not made while verifying received cheques, because a stored value was used",
		}),
	}
}

// Metrics returns the cheque store's collectors.
func (s *chequeStore) Metrics() []prometheus.Collector {
	return m.PrometheusCollectorsFromFields(s.metrics)
}
