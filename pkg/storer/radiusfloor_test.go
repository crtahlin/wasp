// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer_test

import (
	"sync"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/spinlock"
	"github.com/ethersphere/bee/v2/pkg/storer"
)

// depthStub is a storer.DepthReporter whose reading at each call a test
// chooses. The reserve worker calls it once per check on a doubled node, so
// it also records the radius each check started from, which gives the tests
// a timeline by check rather than by wall clock.
type depthStub struct {
	mu    sync.Mutex
	st    *storer.DB
	calls int
	radii []uint8
	fn    func(call int) (storer.DepthReading, bool)
}

func (d *depthStub) CommittedDepths() (storer.DepthReading, bool) {
	d.mu.Lock()
	d.calls++
	call := d.calls
	if d.st != nil {
		d.radii = append(d.radii, d.st.StorageRadius())
	}
	fn := d.fn
	d.mu.Unlock()
	return fn(call)
}

func (d *depthStub) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// timeline returns the radius each recorded check started from.
func (d *depthStub) timeline() []uint8 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]uint8(nil), d.radii...)
}

// waitCalls waits until the worker has run at least n checks.
func (d *depthStub) waitCalls(t *testing.T, n int) {
	t.Helper()
	if err := spinlock.Wait(10*time.Second, func() bool { return d.count() >= n }); err != nil {
		t.Fatalf("reserve worker ran %d checks, want at least %d", d.count(), n)
	}
}

// robust is a reading every rule accepts: 5 of 5 neighbours at n and 20 of
// 20 peers at m, taken now.
func robust(n, m uint8) storer.DepthReading {
	return storer.DepthReading{
		Neighbourhood: n, Network: m,
		Neighbours: 5, NeighboursAgreeing: 5,
		Peers: 20, PeersAgreeing: 20,
		At: time.Now(),
	}
}

func constant(r storer.DepthReading, ok bool) func(int) (storer.DepthReading, bool) {
	return func(int) (storer.DepthReading, bool) {
		if ok {
			r.At = time.Now()
		}
		return r, ok
	}
}

const (
	floorCapacity = 1024
	floorStart    = 3
	// warmupChecks is how many first checks see a non-zero sync rate, as
	// on a node that has just started, so that the floor can settle before
	// the decrease condition first holds.
	warmupChecks = 2
)

type floorCase struct {
	doubling  int
	start     uint8
	fill      map[uint8]int
	fn        func(call int) (storer.DepthReading, bool)
	noStub    bool
	configure func(*storer.DB, *followSyncer)
	opts      func(*storer.Options)
}

// startFloorCase opens a store, fills it, wires the stub and starts the
// worker at c.start.
func startFloorCase(t *testing.T, c floorCase) (*storer.DB, *depthStub, reserveFill) {
	t.Helper()
	st, fill := newFloorStorer(t, floorCapacity, c.doubling, c.opts)
	for bin, n := range c.fill {
		fill.put(bin, n)
	}
	stub := &depthStub{st: st, fn: c.fn}
	syncer := &followSyncer{}
	syncer.st.Store(st)
	if !c.noStub {
		st.SetDepthReporter(stub)
		if c.doubling > 0 {
			syncer.rate = func() float64 {
				if stub.count() <= warmupChecks {
					return 1
				}
				return 0
			}
		}
	}
	if c.configure != nil {
		c.configure(st, syncer)
	}
	start := c.start
	if start == 0 {
		start = floorStart
	}
	startFloorWorker(t, st, syncer, start)
	return st, stub, fill
}

// underHalf is 400 chunks at or above bin 3, under the threshold of 512,
// and few in bin 2, so the fit check lets a decrease through.
func underHalf() map[uint8]int { return map[uint8]int{3: 200, 4: 200, 2: 20} }

// lowerFull is as underHalf with bin 2 full, so the fit check defers.
func lowerFull() map[uint8]int { return map[uint8]int{3: 200, 4: 200, 2: floorCapacity - 400} }

func allAt(t *testing.T, timeline []uint8, want uint8) {
	t.Helper()
	for i, r := range timeline {
		if r != want {
			t.Fatalf("radius timeline %v: check %d started at %d, want %d throughout", timeline, i+1, r, want)
		}
	}
}

// Test 2: a doubled node does not decrease below its neighbourhood depth.
func TestDoubledNodeDoesNotDecreaseBelowNeighbourhoodDepth(t *testing.T) {
	t.Parallel()

	t.Run("floor", func(t *testing.T) {
		t.Parallel()
		// d = 1, N = M = 4: the floor is 3, the start radius.
		st, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: underHalf(), fn: constant(robust(4, 4), true)})
		stub.waitCalls(t, warmupChecks+7)
		allAt(t, stub.timeline(), floorStart)
		if st.RadiusDecreaseDeferred(storer.DeferBelowNeighbourhoodDepth) == 0 {
			t.Fatal("below_neighbourhood_depth deferral not counted")
		}
		if st.RadiusFloorGauge() != floorStart {
			t.Fatalf("radius_floor %v, want %d", st.RadiusFloorGauge(), floorStart)
		}
	})

	t.Run("no reporter", func(t *testing.T) {
		t.Parallel()
		st, _, _ := startFloorCase(t, floorCase{doubling: 1, fill: underHalf(), noStub: true, fn: constant(robust(4, 4), true)})
		if err := spinlock.Wait(10*time.Second, func() bool { return st.StorageRadius() < floorStart }); err != nil {
			t.Fatalf("radius %d without a reporter, want it lowered as before", st.StorageRadius())
		}
	})
}

// Test 3: a genuine decrease. N and M fall by one; the floor settles after
// two checks and the radius drops one step, not two.
func TestDoubledNodeFollowsAFallingDepth(t *testing.T) {
	t.Parallel()
	const change = 5
	fn := func(call int) (storer.DepthReading, bool) {
		if call < change {
			return robust(4, 4), true
		}
		return robust(3, 3), true
	}
	st, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: underHalf(), fn: fn})
	stub.waitCalls(t, change+8)
	tl := stub.timeline()
	// Check change computes floor 2 for the first time and keeps 3; check
	// change+1 settles 2 and decreases; check change+2 starts at 2.
	allAt(t, tl[:change+1], floorStart)
	allAt(t, tl[change+1:], floorStart-1)
	if st.RadiusFloorGauge() != floorStart-1 {
		t.Fatalf("radius_floor %v, want %d", st.RadiusFloorGauge(), floorStart-1)
	}
}

// Test 4: the network leads. N stays, M falls by one: the floor follows
// min(N, M) and the decrease goes ahead.
func TestDoubledNodeFollowsTheNetworkDepth(t *testing.T) {
	t.Parallel()
	const change = 5
	fn := func(call int) (storer.DepthReading, bool) {
		if call < change {
			return robust(4, 4), true
		}
		return robust(4, 3), true
	}
	_, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: underHalf(), fn: fn})
	stub.waitCalls(t, change+8)
	tl := stub.timeline()
	allAt(t, tl[:change+1], floorStart)
	allAt(t, tl[change+1:], floorStart-1)
}

// Test 5: a reading that is not robust gives no floor, and the decrease goes
// ahead when the fit check allows it.
func TestDepthReadingRobustness(t *testing.T) {
	t.Parallel()

	two := robust(4, 4)
	two.Neighbours, two.NeighboursAgreeing = 2, 2

	split := robust(4, 4)
	split.Neighbours, split.NeighboursAgreeing = 6, 3

	cases := []struct {
		name string
		fn   func(int) (storer.DepthReading, bool)
	}{
		{"two neighbours", constant(two, true)},
		{"no majority", constant(split, true)},
		{"16 minutes old", func(int) (storer.DepthReading, bool) {
			r := robust(4, 4)
			r.At = time.Now().Add(-16 * time.Minute)
			return r, true
		}},
		{"no reading", constant(storer.DepthReading{}, false)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			st, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: underHalf(), fn: c.fn})
			if err := spinlock.Wait(10*time.Second, func() bool { return st.StorageRadius() < floorStart }); err != nil {
				t.Fatalf("radius timeline %v: no decrease, want one without a robust reading", stub.timeline())
			}
			if n := st.RadiusDecreaseDeferred(storer.DeferBelowNeighbourhoodDepth); n != 0 {
				t.Fatalf("below_neighbourhood_depth counted %v times without a robust reading", n)
			}
		})
	}

	// The network depth is used only when it is robust itself: a low M
	// from two peers does not lower the floor below N - d.
	t.Run("network not robust", func(t *testing.T) {
		t.Parallel()
		r := robust(4, 2)
		r.Peers, r.PeersAgreeing = 2, 2
		_, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: underHalf(), fn: constant(r, true)})
		stub.waitCalls(t, warmupChecks+7)
		allAt(t, stub.timeline(), floorStart)
	})
}

// Test 6: settling the floor.
func TestRadiusFloorSettles(t *testing.T) {
	t.Parallel()

	// Floor 3 settles first. Then the reading alternates between floors 2
	// and 4 at each check, which never moves the settled floor; two
	// consecutive checks at floor 4 then settle it and the node lifts.
	t.Run("alternating", func(t *testing.T) {
		t.Parallel()
		fn := func(call int) (storer.DepthReading, bool) {
			switch {
			case call <= 4:
				return robust(4, 4), true
			case call <= 10 && call%2 == 1:
				return robust(3, 3), true
			default:
				return robust(5, 5), true
			}
		}
		st, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: underHalf(), fn: fn})
		stub.waitCalls(t, 14)
		tl := stub.timeline()
		// Calls 10 and 11 both give floor 4: check 11 settles and lifts.
		allAt(t, tl[:11], floorStart)
		allAt(t, tl[11:], floorStart+1)
		if st.RadiusLiftTotal() != 1 {
			t.Fatalf("radius_lift_total %v, want 1", st.RadiusLiftTotal())
		}
	})

	// One check without a robust reading keeps the settled floor; two
	// consecutive ones clear it, and the decrease goes ahead.
	t.Run("missing reading", func(t *testing.T) {
		t.Parallel()
		fn := func(call int) (storer.DepthReading, bool) {
			if call == 5 || call >= 8 {
				return storer.DepthReading{}, false
			}
			return robust(4, 4), true
		}
		st, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: underHalf(), fn: fn})
		stub.waitCalls(t, 11)
		tl := stub.timeline()
		// Check 9 is the second without a reading: it clears the floor
		// and decreases, so check 10 starts lower.
		allAt(t, tl[:9], floorStart)
		if tl[9] >= floorStart {
			t.Fatalf("radius timeline %v: no decrease after two checks without a reading", tl)
		}
		if st.RadiusFloorGauge() != 0 {
			t.Fatalf("radius_floor %v after the floor cleared, want 0", st.RadiusFloorGauge())
		}
	})
}

// Test 7: the lift.
func TestRadiusLiftToFloor(t *testing.T) {
	t.Parallel()

	fill := map[uint8]int{3: 100, 4: 100, 2: 20}
	size := 220

	t.Run("one step", func(t *testing.T) {
		t.Parallel()
		// d = 1, N = 5: floor 4, one above the radius.
		st, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: fill, fn: constant(robust(5, 5), true)})
		stub.waitCalls(t, 8)
		tl := stub.timeline()
		// Check 2 settles and lifts.
		allAt(t, tl[:2], floorStart)
		allAt(t, tl[2:], floorStart+1)
		if st.RadiusLiftTotal() != 1 {
			t.Fatalf("radius_lift_total %v, want 1", st.RadiusLiftTotal())
		}
		if st.ReserveSize() != size {
			t.Fatalf("reserve size %d after the lift, want %d: nothing evicted", st.ReserveSize(), size)
		}
		if _, increases := st.RadiusState(); increases == 0 {
			t.Fatal("the lift is not counted as an increase in RadiusState")
		}
	})

	t.Run("waits for a sample", func(t *testing.T) {
		t.Parallel()
		st, stub, _ := startFloorCase(t, floorCase{
			doubling: 1, fill: fill, fn: constant(robust(5, 5), true),
			configure: func(st *storer.DB, _ *followSyncer) { st.SampleStartedForTest() },
		})
		stub.waitCalls(t, 8)
		allAt(t, stub.timeline(), floorStart)
		if st.RadiusLiftTotal() != 0 {
			t.Fatalf("radius_lift_total %v while sampling, want 0", st.RadiusLiftTotal())
		}
		st.SampleDoneForTest()
		if err := spinlock.Wait(10*time.Second, func() bool { return st.StorageRadius() == floorStart+1 }); err != nil {
			t.Fatalf("radius %d after the sample ended, want %d", st.StorageRadius(), floorStart+1)
		}
	})

	t.Run("two steps", func(t *testing.T) {
		t.Parallel()
		// N = 6: floor 5, two above the radius.
		st, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: fill, fn: constant(robust(6, 6), true)})
		stub.waitCalls(t, 8)
		tl := stub.timeline()
		allAt(t, tl[:2], floorStart)
		allAt(t, tl[2:3], floorStart+1)
		allAt(t, tl[3:], floorStart+2)
		if st.RadiusLiftTotal() != 2 {
			t.Fatalf("radius_lift_total %v, want 2", st.RadiusLiftTotal())
		}
		if st.ReserveSize() != size {
			t.Fatalf("reserve size %d after the lifts, want %d", st.ReserveSize(), size)
		}
	})
}

// Test 8: without doubling the reporter is ignored; the fit check applies.
func TestNoFloorWithoutDoubling(t *testing.T) {
	t.Parallel()

	t.Run("no floor, no lift", func(t *testing.T) {
		t.Parallel()
		st, stub, _ := startFloorCase(t, floorCase{doubling: 0, fill: underHalf(), fn: constant(robust(10, 10), true)})
		if err := spinlock.Wait(10*time.Second, func() bool { return st.StorageRadius() < floorStart }); err != nil {
			t.Fatalf("radius %d with d = 0 and a high floor, want it lowered", st.StorageRadius())
		}
		if st.RadiusLiftTotal() != 0 || st.RadiusDecreaseDeferred(storer.DeferBelowNeighbourhoodDepth) != 0 {
			t.Fatalf("lift %v, floor deferrals %v with d = 0, want 0 and 0",
				st.RadiusLiftTotal(), st.RadiusDecreaseDeferred(storer.DeferBelowNeighbourhoodDepth))
		}
		if stub.count() != 0 {
			t.Fatalf("reporter read %d times with d = 0, want 0", stub.count())
		}
	})

	t.Run("fit check", func(t *testing.T) {
		t.Parallel()
		st, _, _ := startFloorCase(t, floorCase{doubling: 0, fill: lowerFull(), fn: constant(robust(10, 10), true)})
		if err := spinlock.Wait(10*time.Second, func() bool { return st.RadiusDecreaseDeferred(storer.DeferLowerRadiusFull) >= 3 }); err != nil {
			t.Fatal("lower_radius_full not counted with d = 0")
		}
		if r := st.StorageRadius(); r != floorStart {
			t.Fatalf("radius %d, want %d", r, floorStart)
		}
	})
}

// Test 9: the fit check boundary, and its bin scan.
func TestFitCheckBoundary(t *testing.T) {
	t.Parallel()

	const limit = floorCapacity - floorCapacity/16 // 960

	cases := []struct {
		name     string
		lower    int
		deferred bool
	}{
		{"at the margin", limit - 400, true},
		{"one under", limit - 400 - 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			// 50 chunks in bin 1 must not count: only bin r - 1 does.
			fill := map[uint8]int{3: 200, 4: 200, 2: c.lower, 1: 50}
			st, _, _ := startFloorCase(t, floorCase{doubling: 0, fill: fill, noStub: true})
			if c.deferred {
				if err := spinlock.Wait(10*time.Second, func() bool { return st.RadiusDecreaseDeferred(storer.DeferLowerRadiusFull) >= 3 }); err != nil {
					t.Fatal("lower_radius_full not counted at the margin")
				}
				if r := st.StorageRadius(); r != floorStart {
					t.Fatalf("radius %d at the margin, want %d", r, floorStart)
				}
				if g := st.ReserveSizeAtLowerRadiusGauge(); g != limit {
					t.Fatalf("reserve_size_at_lower_radius %v, want %d", g, limit)
				}
			} else {
				if err := spinlock.Wait(10*time.Second, func() bool { return st.StorageRadius() < floorStart }); err != nil {
					t.Fatalf("radius %d one chunk under the margin, want a decrease", st.StorageRadius())
				}
				if n := st.RadiusDecreaseDeferred(storer.DeferLowerRadiusFull); n != 0 {
					t.Fatalf("lower_radius_full counted %v times one under the margin", n)
				}
			}
			if st.LowerBinScans() == 0 {
				t.Fatal("no bin scan on the decrease path")
			}
		})
	}

	// Above the threshold the decrease condition does not hold, and the
	// fit check scans nothing. A doubled node with no robust reading uses
	// the reporter only as a check counter here.
	t.Run("no scan off the decrease path", func(t *testing.T) {
		t.Parallel()
		fill := map[uint8]int{3: 300, 4: 300, 2: 20}
		st, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: fill, fn: constant(storer.DepthReading{}, false)})
		stub.waitCalls(t, warmupChecks+6)
		if n := st.LowerBinScans(); n != 0 {
			t.Fatalf("%d bin scans with the decrease condition false, want 0", n)
		}
	})
}

// Test 10: the order of the gates.
func TestRadiusFloorOrder(t *testing.T) {
	t.Parallel()

	stale := func(st *storer.DB, _ *followSyncer) { st.SetPostageSyncHealth(&healthMock{stale: true}) }

	t.Run("stall gate first", func(t *testing.T) {
		t.Parallel()
		st, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: lowerFull(), fn: constant(robust(4, 4), true), configure: stale})
		stub.waitCalls(t, warmupChecks+6)
		allAt(t, stub.timeline(), floorStart)
		for _, reason := range []string{storer.DeferBelowNeighbourhoodDepth, storer.DeferLowerRadiusFull, storer.DeferNotStarted, storer.DeferNotAtRadius, storer.DeferSettling} {
			if n := st.RadiusDecreaseDeferred(reason); n != 0 {
				t.Fatalf("deferral %s counted %v times behind the stall gate, want 0", reason, n)
			}
		}
	})

	t.Run("lift behind the stall gate", func(t *testing.T) {
		t.Parallel()
		st, _, _ := startFloorCase(t, floorCase{doubling: 1, fill: underHalf(), fn: constant(robust(5, 5), true), configure: stale})
		if err := spinlock.Wait(10*time.Second, func() bool { return st.StorageRadius() == floorStart+1 }); err != nil {
			t.Fatalf("radius %d while the stall gate holds, want the lift to %d", st.StorageRadius(), floorStart+1)
		}
	})

	t.Run("floor before fit", func(t *testing.T) {
		t.Parallel()
		st, stub, _ := startFloorCase(t, floorCase{doubling: 1, fill: lowerFull(), fn: constant(robust(4, 4), true)})
		stub.waitCalls(t, warmupChecks+6)
		if st.RadiusDecreaseDeferred(storer.DeferBelowNeighbourhoodDepth) == 0 {
			t.Fatal("below_neighbourhood_depth not counted")
		}
		if n := st.RadiusDecreaseDeferred(storer.DeferLowerRadiusFull); n != 0 {
			t.Fatalf("lower_radius_full counted %v times on checks the floor deferred", n)
		}
		if n := st.LowerBinScans(); n != 0 {
			t.Fatalf("%d bin scans on checks the floor deferred, want 0", n)
		}
	})

	t.Run("settle window after both", func(t *testing.T) {
		t.Parallel()
		// Floor 2 allows the decrease from 3, the fit check passes, and the
		// puller has only just acted on radius 3.
		st, stub, _ := startFloorCase(t, floorCase{
			doubling: 1, fill: underHalf(), fn: constant(robust(3, 3), true),
			opts:      func(o *storer.Options) { o.RadiusDecreaseSettle = time.Hour },
			configure: func(_ *storer.DB, s *followSyncer) { s.fresh = func() bool { return true } },
		})
		stub.waitCalls(t, warmupChecks+6)
		allAt(t, stub.timeline(), floorStart)
		if st.RadiusDecreaseDeferred(storer.DeferSettling) == 0 {
			t.Fatal("puller_settling not counted after the floor and the fit check passed")
		}
		if st.LowerBinScans() == 0 {
			t.Fatal("the fit check did not run before the settle window")
		}
	})
}

// Test 11, wiring: a decrease the fit check allows, then the puller fills
// the lower radius over capacity and the unreserve loop raises the radius
// back. That is a flap the fit check misses, and it is counted.
func TestRadiusFlapCounted(t *testing.T) {
	t.Parallel()
	// The minimum radius keeps the worker from going on below 2 while the
	// test fills bin 2.
	st, _, fill := startFloorCase(t, floorCase{
		doubling: 0, fill: map[uint8]int{3: 200, 4: 200, 2: 100}, noStub: true,
		opts: func(o *storer.Options) { o.MinimumStorageRadius = floorStart - 1 },
	})
	if err := spinlock.Wait(10*time.Second, func() bool { return st.StorageRadius() == floorStart-1 }); err != nil {
		t.Fatalf("radius %d, want a decrease to %d", st.StorageRadius(), floorStart-1)
	}
	fill.put(floorStart-1, 600)
	if err := spinlock.Wait(10*time.Second, func() bool { return st.RadiusFlapTotal() >= 1 }); err != nil {
		t.Fatalf("radius_flap_total %v after the increase back to %d, want 1", st.RadiusFlapTotal(), floorStart)
	}
	if err := spinlock.Wait(10*time.Second, func() bool { return st.RadiusDecreaseDeferred(storer.DeferLowerRadiusFull) >= 2 }); err != nil {
		t.Fatal("lower_radius_full not counted after the increase back")
	}
	if r, n := st.StorageRadius(), st.RadiusFlapTotal(); r != floorStart || n != 1 {
		t.Fatalf("radius %d, radius_flap_total %v; want %d and 1", r, n, floorStart)
	}
}
