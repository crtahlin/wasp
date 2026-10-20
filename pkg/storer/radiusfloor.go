// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer

import (
	"sync"
	"time"

	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// DepthReporter is implemented by salud. It reports the committed depths the
// neighbours and the network play at, as the last salud pass computed them (#822).
type DepthReporter interface {
	CommittedDepths() (DepthReading, bool) // false before the first pass
}

// DepthReading is one salud pass's view of the committed depths (#822).
type DepthReading struct {
	Neighbourhood, Network         uint8     // most common committed depth
	Neighbours, NeighboursAgreeing int       // neighbours that answered; of them, at Neighbourhood
	Peers, PeersAgreeing           int       // all peers that answered; of them, at Network
	At                             time.Time // when the pass finished
}

// Reasons a radius decrease is deferred by the floor and the fit check (#822).
const (
	deferBelowNeighbourhoodDepth = "below_neighbourhood_depth"
	deferLowerRadiusFull         = "lower_radius_full"
)

const (
	// floorMinPeers is how many peers must have answered for a depth to be
	// used.
	floorMinPeers = 3
	// floorMaxAge is how old a reading may be: three salud passes at their
	// maximum interval of 5 minutes.
	floorMaxAge = 15 * time.Minute
	// fitMarginDivisor sets the free share of the capacity a decrease needs at
	// the lower radius: capacity/fitMarginDivisor.
	fitMarginDivisor = 16
	// flapWindow is how soon after a decrease from a radius an increase back
	// to it counts as a flap.
	flapWindow = time.Hour
)

// SetDepthReporter gives the reserve worker salud's committed-depth reading,
// from which a doubled node takes its radius floor (#822). Call it before
// StartReserveWorker. The worker ignores it when the capacity is not doubled.
// A nil reporter keeps today's behaviour.
func (db *DB) SetDepthReporter(r DepthReporter) {
	if r == nil {
		return
	}
	db.depthReporter.Store(&r)
}

// depthRobust reports whether a most common depth can be used: at least
// floorMinPeers answered and more than half of them agree.
func depthRobust(answered, agreeing int) bool {
	return answered >= floorMinPeers && 2*agreeing > answered
}

// floorValue is a floor computed at one check; ok is false for "no floor".
type floorValue struct {
	floor uint8
	ok    bool
}

// floorState is the reserve worker's floor bookkeeping. Only the reserve
// worker goroutine touches it.
type floorState struct {
	last     floorValue // computed at the previous check
	seen     bool       // a previous check computed one
	settled  floorValue // acted on
	reading  DepthReading
	readable bool
}

// computeFloor returns the floor the reading gives now, or ok false.
func (db *DB) computeFloor(reading DepthReading, has bool, now time.Time) floorValue {
	if !has || now.Sub(reading.At) > floorMaxAge {
		return floorValue{}
	}
	if !depthRobust(reading.Neighbours, reading.NeighboursAgreeing) {
		return floorValue{}
	}
	depth := reading.Neighbourhood
	if depthRobust(reading.Peers, reading.PeersAgreeing) {
		depth = min(depth, reading.Network)
	}
	f := int(depth) - db.reserveOptions.capacityDoubling
	f = max(f, int(db.reserveOptions.minimumRadius))
	return floorValue{floor: uint8(f), ok: true}
}

// updateFloor computes the floor at this check and settles it once two
// consecutive checks computed the same value, "no floor" included. It returns
// the settled floor. Without doubling or a reporter there is never a floor.
func (db *DB) updateFloor(now time.Time) floorValue {
	if db.reserveOptions.capacityDoubling == 0 {
		return floorValue{}
	}
	p := db.depthReporter.Load()
	if p == nil {
		return floorValue{}
	}
	fs := &db.floor
	reading, has := (*p).CommittedDepths()
	fs.reading, fs.readable = reading, has
	cur := db.computeFloor(reading, has, now)
	if fs.seen && cur == fs.last && cur != fs.settled {
		old := fs.settled
		fs.settled = cur
		db.metrics.RadiusFloor.Set(float64(cur.floor))
		db.logger.Info("reserve radius floor changed",
			"old", old.floor, "new", cur.floor,
			"neighbourhood_depth", reading.Neighbourhood, "network_depth", reading.Network,
			"neighbours", reading.Neighbours)
	}
	fs.last, fs.seen = cur, true
	return fs.settled
}

// liftToFloor raises the radius by one step when a doubled node sits below
// its settled floor and no sample runs. Nothing is evicted: the bin below
// becomes out of radius and goes first at the next unreserve. It reports
// whether it lifted.
func (db *DB) liftToFloor(radius uint8, f floorValue) bool {
	if db.reserveOptions.capacityDoubling == 0 || !f.ok || radius >= f.floor {
		return false
	}
	// The lift is a radius step, so it holds evictionMu as the unreserve
	// loop's step does and checks for a sample under it: a sample that
	// registers meanwhile reads the radius only after the step (#649).
	db.evictionMu.Lock()
	defer db.evictionMu.Unlock()
	if db.IsSampling() {
		return false
	}
	radius++
	if err := db.reserve.SetRadius(radius); err != nil {
		db.logger.Error(err, "reserve set radius")
	}
	db.metrics.StorageRadius.Set(float64(radius))
	db.metrics.RadiusLift.Inc()
	db.logger.Info("reserve radius raised to the neighbourhood depth floor",
		"radius", radius, "floor", f.floor,
		"neighbourhood_depth", db.floor.reading.Neighbourhood, "network_depth", db.floor.reading.Network)
	return true
}

// floorDeferral reports whether the floor blocks a decrease from radius.
func (db *DB) floorDeferral(radius uint8, f floorValue) bool {
	if db.reserveOptions.capacityDoubling == 0 || !f.ok || radius-1 >= f.floor {
		return false
	}
	db.metrics.RadiusDecreaseDeferred.WithLabelValues(deferBelowNeighbourhoodDepth).Inc()
	db.logger.Debug("reserve radius decrease deferred",
		"radius", radius, "reason", deferBelowNeighbourhoodDepth, "floor", f.floor,
		"neighbourhood_depth", db.floor.reading.Neighbourhood, "network_depth", db.floor.reading.Network)
	return true
}

// heldAtLowerRadius returns the reserve chunks held at radius-1: count, the
// chunks at or above radius, plus those in bin radius-1.
func (db *DB) heldAtLowerRadius(radius uint8, count int) (int, error) {
	db.lowerBinScans.Add(1)
	held := count
	err := db.reserve.IterateBin(radius-1, 0, func(swarm.Address, uint64, []byte, []byte) (bool, error) {
		select {
		case <-db.quit:
			return true, ErrDBQuit
		default:
		}
		held++
		return false, nil
	})
	return held, err
}

// fitDeferral reports whether a decrease from radius must wait because the
// chunks already held at radius-1 leave less than capacity/16 free.
func (db *DB) fitDeferral(radius uint8, count int) (bool, error) {
	held, err := db.heldAtLowerRadius(radius, count)
	if err != nil {
		return false, err
	}
	db.metrics.ReserveSizeAtLowerRadius.Set(float64(held))
	capacity := db.reserve.Capacity()
	if held < capacity-capacity/fitMarginDivisor {
		return false, nil
	}
	db.metrics.RadiusDecreaseDeferred.WithLabelValues(deferLowerRadiusFull).Inc()
	db.logger.Debug("reserve radius decrease deferred",
		"radius", radius, "reason", deferLowerRadiusFull, "held_lower", held, "capacity", capacity)
	return true, nil
}

// flapTracker remembers when the reserve worker last decreased from each
// radius, so an unreserve increase back to it can be counted as a flap.
type flapTracker struct {
	mu   sync.Mutex
	from map[uint8]time.Time
}

// noteRadiusDecrease records a decrease from radius at now.
func (db *DB) noteRadiusDecrease(from uint8, now time.Time) {
	db.flaps.mu.Lock()
	defer db.flaps.mu.Unlock()
	if db.flaps.from == nil {
		db.flaps.from = make(map[uint8]time.Time)
	}
	db.flaps.from[from] = now
}

// noteRadiusIncrease counts a flap when the unreserve loop raises the radius
// to one the reserve worker decreased from within flapWindow. It changes no
// decision.
func (db *DB) noteRadiusIncrease(to uint8, now time.Time) {
	db.flaps.mu.Lock()
	at, ok := db.flaps.from[to]
	db.flaps.mu.Unlock()
	if !ok {
		return
	}
	since := now.Sub(at)
	if since > flapWindow {
		return
	}
	db.metrics.RadiusFlap.Inc()
	db.logger.Warning("reserve radius increased again within an hour of a decrease from it",
		"radius", to, "since_decrease", since)
}
