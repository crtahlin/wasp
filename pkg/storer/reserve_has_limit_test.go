// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// TestReserveHasSlotsUnboundedIsNil asserts that an unbounded setting produces
// no semaphore at all.
//
// The dangerous near-miss is a channel of capacity zero, which is not
// "unbounded" but "nobody may ever proceed" — an unbuffered channel with no
// receiver blocks the first caller for ever. That would deadlock pullsync on
// every node running the default.
func TestReserveHasSlotsUnboundedIsNil(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, -1} {
		if got := storer.ReserveHasSlots(n); got != nil {
			t.Fatalf("ReserveHasSlots(%d) returned a semaphore of capacity %d, want nil (unbounded)",
				n, cap(got))
		}
	}

	if got := storer.ReserveHasSlots(4); cap(got) != 4 {
		t.Fatalf("ReserveHasSlots(4) has capacity %d, want 4", cap(got))
	}
}

// TestAcquireSlotBounds asserts the gate never lets more callers through at
// once than it was given slots.
func TestAcquireSlotBounds(t *testing.T) {
	t.Parallel()

	const (
		limit   = 3
		callers = 40
	)

	var (
		limiter = storer.ReserveHasSlots(limit)
		quit    = make(chan struct{})
		inside  atomic.Int64
		peak    atomic.Int64
		wg      sync.WaitGroup
	)

	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			release, err := storer.AcquireSlot(limiter, quit)
			if err != nil {
				t.Error(err)
				return
			}
			defer release()

			now := inside.Add(1)
			for {
				high := peak.Load()
				if now <= high || peak.CompareAndSwap(high, now) {
					break
				}
			}
			// Long enough that callers genuinely overlap. Without it they
			// could serialise by scheduling accident and the test would pass
			// against a gate that bounds nothing.
			time.Sleep(time.Millisecond)
			inside.Add(-1)
		}()
	}
	wg.Wait()

	if got := peak.Load(); got > limit {
		t.Fatalf("%d callers were inside the gate at once, limit is %d", got, limit)
	}
	if got := peak.Load(); got < 2 {
		t.Fatalf("peak concurrency was %d, so the callers never overlapped and "+
			"this test proves nothing about the bound", got)
	}
}

// TestAcquireSlotUnboundedNeverBlocks asserts a nil semaphore lets everyone
// through, since that is the default every node runs.
func TestAcquireSlotUnboundedNeverBlocks(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	releases := make([]func(), 0, 100)
	for i := range 100 {
		release, err := storer.AcquireSlot(nil, quit)
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		// Held, not released, so a semaphore hiding behind the nil would run
		// out of slots and block rather than passing this test.
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
	}
}

// TestAcquireSlotAbandonsWaitOnQuit asserts a caller waiting for a slot gives
// up when the store shuts down.
//
// ReserveHas has no context to cancel. Without this escape a reserve that had
// stopped answering would leave every waiting caller blocked on a slot that
// never frees, and the node could not be shut down.
func TestAcquireSlotAbandonsWaitOnQuit(t *testing.T) {
	t.Parallel()

	limiter := storer.ReserveHasSlots(1)
	quit := make(chan struct{})

	held, err := storer.AcquireSlot(limiter, quit)
	if err != nil {
		t.Fatal(err)
	}
	defer held()

	result := make(chan error, 1)
	go func() {
		_, err := storer.AcquireSlot(limiter, quit)
		result <- err
	}()

	select {
	case err := <-result:
		t.Fatalf("second caller returned %v while the only slot was held; it should have waited", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(quit)

	select {
	case err := <-result:
		if !errors.Is(err, storer.ErrDBQuit) {
			t.Fatalf("waiting caller returned %v, want %v", err, storer.ErrDBQuit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting caller did not give up after the store quit; shutdown would hang here")
	}
}

// TestReserveHasConcurrencyOptionReachesStore asserts the setting is applied
// rather than parsed and forgotten.
//
// Every other test here exercises the gate directly, so all of them would pass
// against a store that never built a semaphore.
func TestReserveHasConcurrencyOptionReachesStore(t *testing.T) {
	t.Parallel()

	for _, want := range []int{0, 1, 16} {
		opts := dbTestOps(swarm.RandAddress(t), 1000, nil, nil, time.Second)
		opts.ReserveHasConcurrency = want

		st, err := diskStorer(t, opts)()
		if err != nil {
			t.Fatal(err)
		}

		if got := st.ReserveHasLimit(); got != want {
			t.Fatalf("configured a bound of %d, store applied %d", want, got)
		}
	}
}
