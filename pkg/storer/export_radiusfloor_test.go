// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer

// Radius floor test hooks (#822).

const (
	DeferBelowNeighbourhoodDepth = deferBelowNeighbourhoodDepth
	DeferLowerRadiusFull         = deferLowerRadiusFull
)

// RadiusFloorGauge returns bee_localstore_radius_floor.
func (db *DB) RadiusFloorGauge() float64 { return metricValue(db.metrics.RadiusFloor) }

// RadiusLiftTotal returns bee_localstore_radius_lift_total.
func (db *DB) RadiusLiftTotal() float64 { return metricValue(db.metrics.RadiusLift) }

// RadiusFlapTotal returns bee_localstore_radius_flap_total.
func (db *DB) RadiusFlapTotal() float64 { return metricValue(db.metrics.RadiusFlap) }

// ReserveSizeAtLowerRadiusGauge returns
// bee_localstore_reserve_size_at_lower_radius.
func (db *DB) ReserveSizeAtLowerRadiusGauge() float64 {
	return metricValue(db.metrics.ReserveSizeAtLowerRadius)
}

// LowerBinScans returns how many bin scans the fit check has made.
func (db *DB) LowerBinScans() int64 { return db.lowerBinScans.Load() }
