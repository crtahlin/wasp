// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover

import (
	m "github.com/ethersphere/bee/v2/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	// SendResent counts sends of a transaction to a further endpoint after
	// the active one failed at the transport level, by outcome: accepted,
	// known (already in that endpoint's pool), answered (any other answer,
	// returned to the caller), or failed (a transport failure there too).
	SendResent *prometheus.CounterVec
}

func newMetrics() metrics {
	subsystem := "failover"
	return metrics{
		SendResent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "send_resent_total",
			Help:      "Sends of a signed transaction to a further endpoint after a transport-level failure, by result.",
		}, []string{"result"}),
	}
}

// Metrics returns the backend's collectors for registration.
func (b *Backend) Metrics() []prometheus.Collector {
	return m.PrometheusCollectorsFromFields(b.metrics)
}
