// Copyright 2021 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var StoredTransactionKey = storedTransactionKey

var Bumped = bumped

var NewerThan = newerThan

// NewServiceWithoutWatchdog builds a service whose watchdog does not run
// on its own; a test runs its checks with CheckPending.
func WithoutWatchdog() Option {
	return func(t *transactionService) { t.noWatchdog = true }
}

// WithClock replaces the service's clock, which dates stored transactions
// and ages them for the watchdog.
func WithClock(now func() time.Time) Option {
	return func(t *transactionService) { t.now = now }
}

// CheckPending runs one watchdog check.
func CheckPending(ctx context.Context, s Service) { s.(*transactionService).checkPending(ctx) }

// CheckInterval and OverdueAge report the watchdog's bounds.
func CheckInterval(s Service) time.Duration { return s.(*transactionService).checkInterval() }
func OverdueAge(s Service) time.Duration    { return s.(*transactionService).overdueAge() }

// ServiceMetrics returns the service's collectors.
func ServiceMetrics(s Service) []prometheus.Collector { return s.(*transactionService).Metrics() }

const (
	OverdueBlocks = overdueBlocks
	CheckBlocks   = checkBlocks
	MaxFeeRaise   = maxFeeRaise
)

// WatchdogMetricSet holds the service's watchdog metrics.
type WatchdogMetricSet struct {
	OldestPendingAge   prometheus.Gauge
	BlockedNonces      prometheus.Gauge
	PendingResent      *prometheus.CounterVec
	Replacements       *prometheus.CounterVec
	CancelledForReveal prometheus.Counter
}

// WatchdogMetrics returns the service's watchdog metrics.
func WatchdogMetrics(s Service) WatchdogMetricSet {
	m := s.(*transactionService).metrics
	return WatchdogMetricSet{m.OldestPendingAge, m.BlockedNonces, m.PendingResent, m.Replacements, m.CancelledForReveal}
}

// StoredTransactionPrefix is the state store prefix of stored transactions.
const StoredTransactionPrefix = storedTransactionPrefix
