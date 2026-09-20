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
	// handshake and topology included, against a provider this node did not
	// already hold.
	//
	// The split from ConnectsAlreadyConnected is decided by the caller
	// reading its peer set immediately before the connect, not by the error
	// the connect returns: p2p.ErrAlreadyConnected is keyed on the remote
	// address rather than the peer, so a provider held on another underlay
	// comes back as a plain success. See issue #382.
	//
	// It is a diagnostic rather than an accounting record. The peer set
	// reflects peers whose bzz handshake has finished, while the connect
	// short-circuits as soon as a transport connection exists, so a peer
	// connecting concurrently can be counted here even though no dial was
	// needed. That window is the length of a concurrent connection setup,
	// not an instant.
	ConnectsDialed prometheus.Counter
	// ConnectsAlreadyConnected counts connects to a provider this node was
	// already connected to, on any underlay, and so needed no dial. Carries
	// the same caveat as ConnectsDialed.
	ConnectsAlreadyConnected prometheus.Counter
	// ConnectsFailed counts connects that returned an error other than one
	// abandoned by shutdown or by the run's own timeout. Those are excluded
	// deliberately: one shutdown would otherwise add a failure for every
	// record left in the run, and this counter is read as evidence that
	// providers cannot be reached. The connection breaker behind it is also
	// node-wide, so a rise here can be caused by a failed dial to an
	// unrelated peer.
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
			Help:      "Number of connects to a provider that ran to completion and were not already connected.",
		}),
		ConnectsAlreadyConnected: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "connects_already_connected",
			Help:      "Number of connects to a provider that needed no dial because it was already connected.",
		}),
		ConnectsFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: m.Namespace,
			Subsystem: subsystem,
			Name:      "connects_failed",
			Help:      "Number of connects to a provider that failed, excluding ones abandoned by shutdown or by the run's own timeout.",
		}),
	}
}

// Metrics returns the service's collectors.
func (s *Service) Metrics() []prometheus.Collector {
	return m.PrometheusCollectorsFromFields(s.metrics)
}
