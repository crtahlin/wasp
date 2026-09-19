// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package providers

import (
	"github.com/prometheus/client_golang/prometheus"

	m "github.com/ethersphere/bee/v2/pkg/metrics"
)

// metrics makes discovery observable. Before these counters the package had
// none, and no success logging either: the only log line on the lookup path
// reports a failure, while a cache hit and a completed lookup both return
// silently. A working lookup and a lookup that never ran were therefore
// indistinguishable from outside the node, which made every obvious way of
// measuring issue #369 unable to fail. See
// docs/experiments/content-providers/discovery-lifetime.md.
type metrics struct {
	// all metrics fields must be exported
	// to be able to return them by Metrics()
	// using reflection

	// DiscoveriesStarted counts discovery goroutines that began work. It is
	// the denominator that separates "the lookup completed" from "no
	// discovery ran at all".
	DiscoveriesStarted prometheus.Counter
	// HintedConnectsStarted is the same denominator for the hinted path, which
	// performs no lookup and so would otherwise have none.
	HintedConnectsStarted prometheus.Counter
	// LookupsCompleted counts lookups that actually read the network and
	// returned without their context being canceled. A cache hit does NOT
	// increment it, and neither does a key that is not a plain reference:
	// counting either here would make the cache indistinguishable from the
	// work it saves.
	LookupsCompleted prometheus.Counter
	// LookupsServedFromCache counts lookups answered from the window cache.
	LookupsServedFromCache prometheus.Counter
	// LookupsCanceled counts lookups cut off by their context. Before #369
	// this was every lookup on erasure-coded content.
	LookupsCanceled prometheus.Counter

	// ConnectsDialed counts connect procedures that ran to completion,
	// handshake and topology included. It does NOT prove a dial opened a new
	// connection: the already-connected short-circuit in libp2p is keyed on
	// the remote address rather than the peer, so a connection to the same
	// peer on a different underlay falls through to a connect that succeeds
	// against the open one. Attributing a connection to discovery needs this
	// counter ordered against the peer set, which is what the measurement
	// does.
	ConnectsDialed prometheus.Counter
	// ConnectsAlreadyConnected counts connects short-circuited because this
	// node was already connected to that peer on that address.
	ConnectsAlreadyConnected prometheus.Counter
	// ConnectsFailed counts connects that returned an error. The connection
	// breaker behind it is node-wide, so a rise here can be caused by a
	// failed dial to an unrelated peer.
	ConnectsFailed prometheus.Counter
}

func newMetrics() metrics {
	subsystem := "providers"

	return metrics{
		DiscoveriesStarted: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "discoveries_started",
			Help:      "Number of provider discovery runs started.",
		}),
		HintedConnectsStarted: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "hinted_connects_started",
			Help:      "Number of hinted provider connect runs started.",
		}),
		LookupsCompleted: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "lookups_completed",
			Help:      "Number of provider lookups that read the network and were not canceled.",
		}),
		LookupsServedFromCache: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "lookups_served_from_cache",
			Help:      "Number of provider lookups answered from the window cache.",
		}),
		LookupsCanceled: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "lookups_canceled",
			Help:      "Number of provider lookups cut off by their context.",
		}),
		ConnectsDialed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "connects_dialed",
			Help:      "Number of connects to a provider that ran to completion.",
		}),
		ConnectsAlreadyConnected: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "connects_already_connected",
			Help:      "Number of connects to a provider short-circuited as already connected.",
		}),
		ConnectsFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "connects_failed",
			Help:      "Number of connects to a provider that returned an error.",
		}),
	}
}

// Metrics returns the service's collectors.
func (s *Service) Metrics() []prometheus.Collector {
	return m.PrometheusCollectorsFromFields(s.metrics)
}
