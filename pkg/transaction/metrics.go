// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	m "github.com/ethersphere/bee/v2/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	// OldestPendingAge is the age, since it was signed, of the pending
	// transaction with the lowest nonce at or above the chain nonce.
	OldestPendingAge prometheus.Gauge
	// PendingResent counts the watchdog's broadcasts by outcome.
	PendingResent *prometheus.CounterVec
	// Replacements counts the replacements the watchdog signed, by reason.
	Replacements *prometheus.CounterVec
	// BlockedNonces is the number of pending transactions the watchdog
	// marked blocked.
	BlockedNonces prometheus.Gauge
	// CancelledForReveal counts the transactions the node cancelled to
	// protect a reveal.
	CancelledForReveal prometheus.Counter
}

func newMetrics() metrics {
	subsystem := "transaction"
	return metrics{
		OldestPendingAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "oldest_pending_age_seconds",
			Help:      "Age of the pending transaction with the lowest nonce at or above the chain nonce; 0 when there is none.",
		}),
		PendingResent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "pending_resent_total",
			Help:      "Endpoint answers to the pending-transaction watchdog's broadcasts, by result.",
		}, []string{"result"}),
		Replacements: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "replacements_total",
			Help:      "Same-nonce replacements with raised fees the pending-transaction watchdog signed, by reason.",
		}, []string{"reason"}),
		BlockedNonces: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "blocked_nonces",
			Help:      "Pending transactions that cannot be mined as they are; each holds every later transaction of the node.",
		}),
		CancelledForReveal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "cancelled_for_reveal_total",
			Help:      "Blocked transactions the node cancelled to protect a storage-lottery reveal.",
		}),
	}
}

// Metrics returns the service's collectors for registration. pkg/node
// registers them once.
func (t *transactionService) Metrics() []prometheus.Collector {
	return m.PrometheusCollectorsFromFields(t.metrics)
}
