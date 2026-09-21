// Copyright 2023 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer

import (
	"context"
	"math/big"

	"github.com/ethersphere/bee/v2/pkg/storer/internal/events"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/reserve"
	"github.com/prometheus/client_golang/prometheus"
)

func (db *DB) Reserve() *reserve.Reserve {
	return db.reserve
}

func (db *DB) Events() *events.Subscriber {
	return db.events
}

func ReplaceSharkyShardLimit(val int) {
	sharkyNoOfShards = val
}

func (db *DB) WaitForBgCacheWorkers() (unblock func()) {
	for range defaultBgCacheWorkers {
		db.cacheLimiter.sem <- struct{}{}
	}
	return func() {
		for range defaultBgCacheWorkers {
			<-db.cacheLimiter.sem
		}
	}
}

func DefaultOptions() *Options {
	return defaultOptions()
}

// CountWithinRadius exposes the reserve scan so a test can assert how many
// batch-existence lookups it makes.
func (db *DB) CountWithinRadius(ctx context.Context) (int, error) {
	return db.countWithinRadius(ctx)
}

// CountChunksWithinRadius exposes the cheap within-radius count the frequent
// wake-up uses, so a test can assert it agrees with the combined scan. See #28.
func (db *DB) CountChunksWithinRadius() (int, error) {
	return db.countChunksWithinRadius()
}

// MaxSamplerSortWindow exposes the read-ordering window cap so a test can
// assert an oversized setting is clamped to it rather than honoured.
const MaxSamplerSortWindow = maxSamplerSortWindow

// AcquireSlot exposes the ReserveHas gate so its bounding and its shutdown
// escape can be tested without standing up a store.
func AcquireSlot(limiter chan struct{}, quit chan struct{}) (func(), error) {
	return acquireSlot(limiter, quit)
}

// ReserveHasSlots exposes the semaphore constructor so a test can assert that
// an unbounded setting really produces no semaphore rather than one of size
// zero, which would block every caller for ever.
func ReserveHasSlots(n int) chan struct{} { return reserveHasSlots(n) }

// ReserveHasLimit reports the bound the store was built with, 0 when the
// lookups are unbounded. Used to assert the option reaches the store rather
// than being read into a field nothing consults.
func (db *DB) ReserveHasLimit() int { return cap(db.reserveHasLimiter) }

// ReserveScanDuration exposes the scan-duration histogram so a test can assert
// a pass was actually timed, rather than that the code compiles.
func (db *DB) ReserveScanDuration() *prometheus.HistogramVec {
	return db.metrics.ReserveScanDuration
}

// Write-pause edge classification, exposed so its once-per-transition
// behaviour can be tested without driving a real store to its pause trigger.
type WritePauseChange = writePauseChange

const (
	WritePauseUnchanged = writePauseUnchanged
	WritePauseEntered   = writePauseEntered
	WritePauseLeft      = writePauseLeft
)

func WritePauseEdge(prev, cur bool) WritePauseChange { return writePauseEdge(prev, cur) }

// Windowed-proof test hooks (#273).

var (
	WindowDepthIncrement = windowDepthIncrement
	DeriveWindowAnchor   = deriveWindowAnchor
)

const (
	MinWindowCount          = minWindowCount
	MaxWindowDepthIncrement = maxWindowDepthIncrement
)

// ReserveSampleWithWindow drives the shared sampler with an explicit window
// predicate so tests can exercise the filtering path directly, independent of
// the reserve size that WindowedSample uses to size the window.
func (db *DB) ReserveSampleWithWindow(
	ctx context.Context,
	anchor []byte,
	committedDepth uint8,
	consensusTime uint64,
	minBatchBalance *big.Int,
	inWindow func([]byte) bool,
) (Sample, error) {
	return db.reserveSample(ctx, anchor, committedDepth, consensusTime, minBatchBalance, inWindow)
}

// LocalIngestPublished is the last usage figure handed to the local ingest
// gauge. A test uses it to check that the number an operator sees tracks the
// total on every path that moves it, not only on the ingest that happened to
// set it last. See issue #326.
func (db *DB) LocalIngestPublished() uint64 {
	db.localIngest.mu.Lock()
	defer db.localIngest.mu.Unlock()

	return db.localIngest.published
}

// TriggerQuit closes the shutdown signal without tearing the store down, so a
// test can assert the reserve scans stop on shutdown. It shares Close's guard,
// so a later Close does not double-close the channel. See issue #399.
func (db *DB) TriggerQuit() { db.quitOnce.Do(func() { close(db.quit) }) }

// EvictExpiredBatches and Unreserve expose the two reserve-worker operations
// that #407 gave a shutdown exit, so a test can run them directly rather than
// through the worker's triggers.
func (db *DB) EvictExpiredBatches(ctx context.Context) error { return db.evictExpiredBatches(ctx) }

func (db *DB) Unreserve(ctx context.Context) error { return db.unreserve(ctx) }
