// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pseudosettle

import (
	m "github.com/ethersphere/bee/v2/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	// all metrics fields must be exported
	// to be able to return them by Metrics()
	// using reflection
	TotalReceivedPseudoSettlements  prometheus.Counter
	TotalSentPseudoSettlements      prometheus.Counter
	ReceivedPseudoSettlements       prometheus.Counter
	SentPseudoSettlements           prometheus.Counter
	ReceivedPseudoSettlementsErrors prometheus.Counter
	SentPseudoSettlementsErrors     prometheus.Counter

	// wasp #444, bench only: which term bound peerAllowance. If the debt binds
	// far more often than the rate, raising the grant rate changes nothing and
	// the experiment ends here.
	AllowanceRateBound prometheus.Counter
	AllowanceDebtBound prometheus.Counter
	AllowanceTooSoon   prometheus.Counter
}

func newMetrics() metrics {
	subsystem := "pseudosettle"

	return metrics{
		TotalReceivedPseudoSettlements: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "total_received_pseudosettlements",
			Help:      "Amount of time settlements received from peers (income of the node)",
		}),
		TotalSentPseudoSettlements: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "total_sent_pseudosettlements",
			Help:      "Amount of  of time settlements sent to peers (costs paid by the node)",
		}),
		AllowanceRateBound: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "allowance_rate_bound",
			Help:      "Grants where elapsed times the refresh rate was the smaller term",
		}),
		AllowanceDebtBound: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "allowance_debt_bound",
			Help:      "Grants where the peer's debt was the smaller term",
		}),
		AllowanceTooSoon: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "allowance_too_soon",
			Help:      "Refreshment requests refused for arriving within the same second",
		}),
		ReceivedPseudoSettlements: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "received_pseudosettlements",
			Help:      "Number of time settlements received from peers",
		}),
		SentPseudoSettlements: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "sent_pseudosettlements",
			Help:      "Number of time settlements sent to peers",
		}),
		ReceivedPseudoSettlementsErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "received_pseudosettlements_errors",
			Help:      "Errors of time settlements received from peers",
		}),
		SentPseudoSettlementsErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "sent_pseudosettlements_errorss",
			Help:      "Errors of time settlements sent to peers",
		}),
	}
}

func (s *Service) Metrics() []prometheus.Collector {
	return m.PrometheusCollectorsFromFields(s.metrics)
}
