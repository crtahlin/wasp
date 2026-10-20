// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// SetActive forces the active endpoint, so a test can arrange a state that
// would otherwise need a real failure to reach.
func SetActive(b *Backend, i int) { b.active.Store(int32(i)) }

// IsTransportFailure exposes the classifier, which is the piece that decides
// whether an error means "no answer" or "an answer you did not like".
var IsTransportFailure = isTransportFailure

// SetMinAttemptTimeout replaces the floor of a per-attempt deadline for a
// test and returns a function that restores it.
func SetMinAttemptTimeout(d time.Duration) func() {
	old := minAttemptTimeout
	minAttemptTimeout = d
	return func() { minAttemptTimeout = old }
}

// SetWrapPause replaces the pause before a persistent call starts again at
// the first endpoint, and returns a function that restores it.
func SetWrapPause(d time.Duration) func() {
	old := wrapPause
	wrapPause = d
	return func() { wrapPause = old }
}

// Wraparounds reads the wrap-around counter.
func Wraparounds(b *Backend) float64 { return metricValue(b.metrics.Wraparounds) }

// SendCopies reads the copy counter for one result.
func SendCopies(b *Backend, result string) float64 {
	return metricValue(b.metrics.SendCopies.WithLabelValues(result))
}

func metricValue(m prometheus.Metric) float64 {
	var d dto.Metric
	if err := m.Write(&d); err != nil {
		return -1
	}
	return d.GetCounter().GetValue()
}

// SetCopyDialTimeout replaces the bound of a copy's or a broadcast's dial
// and returns a function that restores it.
func SetCopyDialTimeout(d time.Duration) func() {
	old := copyDialTimeout
	copyDialTimeout = d
	return func() { copyDialTimeout = old }
}
