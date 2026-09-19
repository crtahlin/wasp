// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package providers

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue reads a counter, which prometheus does not expose directly.
func counterValue(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

// SetNow replaces the service's clock.
func (s *Service) SetNow(f func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = f
}

// RunOnce runs one pass of the announcement loop.
func (s *Service) RunOnce(ctx context.Context) {
	s.runOnce(ctx)
}

var WindowStart = windowStart

// SetDiscoverTimeout shortens the bound on one discovery or hinted-connect
// run and returns a function restoring it. A test needs this rather than the
// service's injected clock, because context.WithTimeout reads the real one.
func SetDiscoverTimeout(d time.Duration) func() {
	old := discoverTimeout
	discoverTimeout = d
	return func() { discoverTimeout = old }
}

// Counts holds the discovery counters at one moment.
type Counts struct {
	Dialled          float64
	AlreadyConnected float64
	Failed           float64
	LookupsCompleted float64
	LookupsFromCache float64
	LookupsCancelled float64
	Discoveries      float64
}

// Counters reports the discovery counters, for tests that need to see which
// outcome a connect or a lookup was recorded as.
func (s *Service) Counters() Counts {
	return Counts{
		Dialled:          counterValue(s.metrics.ConnectsDialled),
		AlreadyConnected: counterValue(s.metrics.ConnectsAlreadyConnected),
		Failed:           counterValue(s.metrics.ConnectsFailed),
		LookupsCompleted: counterValue(s.metrics.LookupsCompleted),
		LookupsFromCache: counterValue(s.metrics.LookupsServedFromCache),
		LookupsCancelled: counterValue(s.metrics.LookupsCancelled),
		Discoveries:      counterValue(s.metrics.DiscoveriesStarted),
	}
}
