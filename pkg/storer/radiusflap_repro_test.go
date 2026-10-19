// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/postage"
	batchstore "github.com/ethersphere/bee/v2/pkg/postage/batchstore/mock"
	postagetesting "github.com/ethersphere/bee/v2/pkg/postage/testing"
	chunk "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// floorWakeup is the reserve worker check interval in the #822 tests.
const floorWakeup = 50 * time.Millisecond

// followSyncer is a syncer with a sync rate of 0 whose acted-on radius is
// always the store's current radius, an hour ago, so the #696 settle window
// never defers. rate and fresh, when set, override the rate and make the
// acted-on time now. On a tree without #696 ActedOnRadius is never called.
type followSyncer struct {
	st    atomic.Pointer[storer.DB]
	rate  func() float64
	fresh func() bool
}

func (s *followSyncer) SyncRate() float64 {
	if s.rate != nil {
		return s.rate()
	}
	return 0
}

func (s *followSyncer) Start(context.Context) {}

func (s *followSyncer) ActedOnRadius() (uint8, time.Time, bool) {
	st := s.st.Load()
	if st == nil {
		return 0, time.Time{}, false
	}
	since := time.Now().Add(-time.Hour)
	if s.fresh != nil && s.fresh() {
		since = time.Now()
	}
	return st.StorageRadius(), since, true
}

// reserveFill puts chunks into the reserve at a chosen proximity order to the
// store's address, all under one batch.
type reserveFill struct {
	tb    testing.TB
	st    *storer.DB
	base  swarm.Address
	batch *postage.Batch
}

func (f reserveFill) put(bin uint8, n int) {
	f.tb.Helper()
	putter := f.st.ReservePutter()
	for range n {
		ch := chunk.GenerateTestRandomChunkAt(f.tb, f.base, int(bin)).WithStamp(postagetesting.MustNewBatchStamp(f.batch.ID))
		if err := putter.Put(context.Background(), ch); err != nil {
			f.tb.Fatal(err)
		}
	}
}

// newFloorStorer opens an in-memory store of the given capacity and capacity
// doubling, with eviction of at least one chunk at a time.
func newFloorStorer(tb testing.TB, capacity, doubling int, configure func(*storer.Options)) (*storer.DB, reserveFill) {
	tb.Helper()
	bs := batchstore.New()
	batch := postagetesting.MustNewBatch()
	if err := bs.Save(batch); err != nil {
		tb.Fatal(err)
	}
	base := swarm.RandAddress(tb)
	opts := dbTestOps(base, capacity, bs, nil, floorWakeup)
	opts.ReserveMinEvictCount = 1
	opts.ReserveCapacityDoubling = doubling
	if configure != nil {
		configure(opts)
	}
	st, err := memStorer(tb, opts)()
	if err != nil {
		tb.Fatal(err)
	}
	return st, reserveFill{tb: tb, st: st, base: base, batch: batch}
}

func startFloorWorker(tb testing.TB, st *storer.DB, s storer.Syncer, radius uint8) {
	tb.Helper()
	readyC := make(chan struct{})
	st.StartReserveWorker(context.Background(), s, networkRadiusFunc(radius), readyC)
	select {
	case <-readyC:
	case <-time.After(30 * time.Second):
		tb.Fatal("start reserve worker timeout")
	}
}

// TestRadiusDoesNotFlapIntoAFullLowerRadius reproduces the flap in #822.
// The reserve is full at radius 3: under half the capacity at or above bin
// 3, the rest in bin 2, left from an earlier stay at radius 2. The decrease
// condition holds, so the worker lowers the radius to 2; the puller then
// fetches a few missing bin 2 chunks, the reserve goes over capacity, and
// the unreserve loop, finding nothing below bin 2, raises the radius back
// to 3. Without the fit check this repeats at every check.
func TestRadiusDoesNotFlapIntoAFullLowerRadius(t *testing.T) {
	t.Parallel()

	const (
		capacity = 1024
		start    = 3
	)
	st, fill := newFloorStorer(t, capacity, 0, nil)
	fill.put(3, 200)
	fill.put(4, 200)
	fill.put(2, capacity-400)

	syncer := &followSyncer{}
	syncer.st.Store(st)
	startFloorWorker(t, st, syncer, start)

	// The test plays the puller: whenever the radius drops, it fetches 20
	// more chunks of the bin that came into the radius.
	timeline := []uint8{start}
	decreases := 0
	last := uint8(start)
	for deadline := time.Now().Add(20 * floorWakeup); time.Now().Before(deadline); {
		if r := st.StorageRadius(); r != last {
			timeline = append(timeline, r)
			if r < last {
				decreases++
				fill.put(r, 20)
			}
			last = r
		}
		time.Sleep(time.Millisecond)
	}
	if decreases > 0 {
		t.Fatalf("radius timeline %v: %d decreases into a lower radius that is already full, want none", timeline, decreases)
	}
	if st.RadiusDecreaseDeferred("lower_radius_full") == 0 {
		t.Fatal("lower_radius_full deferral not counted")
	}
}
