// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package providers

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue reads a counter, which prometheus does not expose directly. It
// fails the test rather than returning zero on error, because a silent zero
// would make every "this counter did not move" assertion vacuous.
func counterValue(tb testing.TB, c prometheus.Counter) float64 {
	tb.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		tb.Fatalf("reading a counter: %v", err)
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

// SetDiscoverTimeout shortens this service's bound on one discovery or
// hinted-connect run. A test needs this rather than the injected clock, because
// context.WithTimeout reads the real one. It is per service rather than a
// package variable: a package variable written by one parallel test races every
// other test's background goroutines reading it, which is a data race that
// failed every test in this package under -race.
func (s *Service) SetDiscoverTimeout(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timeout = d
}

// DiscoverBound reports the current bound, so a test can assert it took effect.
func (s *Service) DiscoverBound() time.Duration {
	return s.discoverBound()
}

// Counts holds the discovery counters at one moment. Field names match the
// metric fields so that a reader does not have to map between them.
type Counts struct {
	ConnectsDialed           float64
	ConnectsAlreadyConnected float64
	ConnectsFailed           float64
	LookupsCompleted         float64
	LookupsServedFromCache   float64
	LookupsCanceled          float64
	DiscoveriesStarted       float64
	HintedConnectsStarted    float64
}

// Counters reports the discovery counters, for tests that need to see which
// outcome a connect or a lookup was recorded as.
func (s *Service) Counters(tb testing.TB) Counts {
	tb.Helper()
	return Counts{
		ConnectsDialed:           counterValue(tb, s.metrics.ConnectsDialed),
		ConnectsAlreadyConnected: counterValue(tb, s.metrics.ConnectsAlreadyConnected),
		ConnectsFailed:           counterValue(tb, s.metrics.ConnectsFailed),
		LookupsCompleted:         counterValue(tb, s.metrics.LookupsCompleted),
		LookupsServedFromCache:   counterValue(tb, s.metrics.LookupsServedFromCache),
		LookupsCanceled:          counterValue(tb, s.metrics.LookupsCanceled),
		DiscoveriesStarted:       counterValue(tb, s.metrics.DiscoveriesStarted),
		HintedConnectsStarted:    counterValue(tb, s.metrics.HintedConnectsStarted),
	}
}
