// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	storer "github.com/ethersphere/bee/v2/pkg/storer"
)

// recordingCloser stands in for the store's own closer, so a test can say
// whether Close actually closed it rather than whether Close returned.
type recordingCloser struct {
	closed atomic.Bool
}

func (c *recordingCloser) Close() error {
	c.closed.Store(true)
	return nil
}

// TestCloseWaitsForTheStoreToClose is wasp #428.
//
// Each drain used to wait a hardcoded five seconds while Close waited
// ShutdownTimeout, three by default, and the store was closed only after both
// drains returned. So a drain lasting between the two made Close return while
// dbCloser.Close had not run: the store stayed open, the unclean-shutdown
// marker stayed, and the next start replayed the write-ahead log.
//
// The budget here is deliberately shorter than the work held in flight, which
// is the shape of the defect. What must hold afterwards is not that the drain
// finished, it did not, but that the store was closed anyway and that Close
// said so.
func TestCloseWaitsForTheStoreToClose(t *testing.T) {
	t.Parallel()

	const budget = 200 * time.Millisecond

	closer := &recordingCloser{}
	db := storer.NewForCloseTest(t, closer, budget)

	// One unit of background work that outlasts the budget.
	release := storer.HoldInFlight(db)
	defer release()

	start := time.Now()
	err := db.Close()
	elapsed := time.Since(start)

	if !closer.closed.Load() {
		t.Fatalf("Close returned after %s without closing the store; that is the defect, "+
			"the unclean-shutdown marker stays and the next start replays the log", elapsed)
	}
	if err == nil {
		t.Fatal("a drain that did not finish should be reported, so an operator can see it")
	}
	if !strings.Contains(err.Error(), "storer closed") {
		t.Fatalf("error %q should say the store WAS closed, since it was; "+
			"the old message read as though it had not been", err)
	}
	if elapsed < budget {
		t.Fatalf("Close returned after %s, inside the %s budget, so it cannot have waited "+
			"for the drain it was supposed to bound", elapsed, budget)
	}
}

// TestCloseCleanShutdownIsPrompt pins the other side: with nothing in flight
// Close must not wait out the budget, or every shutdown pays for the fix.
func TestCloseCleanShutdownIsPrompt(t *testing.T) {
	t.Parallel()

	const budget = 2 * time.Second

	closer := &recordingCloser{}
	db := storer.NewForCloseTest(t, closer, budget)

	start := time.Now()
	err := db.Close()
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a clean shutdown reported %v", err)
	}
	if !closer.closed.Load() {
		t.Fatal("the store was not closed")
	}
	if elapsed >= budget {
		t.Fatalf("a clean shutdown took %s, the whole %s budget", elapsed, budget)
	}
}

// TestCloseIsIdempotent guards the quit channel. quitOnce is shared with
// TriggerQuit, so a second Close must not panic on a closed channel.
func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	closer := &recordingCloser{}
	db := storer.NewForCloseTest(t, closer, time.Second)

	if err := db.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}


// orderingCloser records whether any background work was still in flight at
// the moment the store was closed. That is #399's property, and review of the
// #428 fix found nothing in the repository pinned it: an implementation that
// closed the store BEFORE the drains passed every test in this package.
type orderingCloser struct {
	inFlight  func() bool
	closed    atomic.Bool
	racedWork atomic.Bool
}

func (c *orderingCloser) Close() error {
	if c.inFlight() {
		c.racedWork.Store(true)
	}
	c.closed.Store(true)
	return nil
}

// TestCloseClosesTheStoreAfterTheDrains pins the ordering #399 introduced,
// which #428 must preserve rather than quietly trade away.
//
// The store must not be closed while reserve work is still running. Here the
// work finishes before the drain window expires, so a correct Close waits for
// it and closes afterwards; one that closes first, as upstream does, records
// the race.
func TestCloseClosesTheStoreAfterTheDrains(t *testing.T) {
	t.Parallel()

	var working atomic.Bool
	closer := &orderingCloser{inFlight: working.Load}

	db := storer.NewForCloseTest(t, closer, 2*time.Second)

	working.Store(true)
	release := storer.HoldInFlight(db)
	go func() {
		time.Sleep(150 * time.Millisecond)
		working.Store(false)
		release()
	}()

	if err := db.Close(); err != nil {
		t.Fatalf("the drain had time to finish, so Close should not report one: %v", err)
	}
	if !closer.closed.Load() {
		t.Fatal("the store was not closed")
	}
	if closer.racedWork.Load() {
		t.Fatal("the store was closed while reserve work was still in flight, which is the " +
			"pebble segfault #399 exists to prevent")
	}
}

// TestCloseCancelsTheCacheLimiter pins the force-close of cache goroutines
// that outlast the drain. Review found deleting that call left every test
// passing.
func TestCloseCancelsTheCacheLimiter(t *testing.T) {
	t.Parallel()

	closer := &recordingCloser{}
	db := storer.NewForCloseTest(t, closer, 100*time.Millisecond)

	release := storer.HoldCacheWork(db)
	defer release()

	_ = db.Close()

	if !storer.CacheLimiterCancelled(db) {
		t.Fatal("a cache drain that timed out must force the limiter closed, or the " +
			"goroutines it bounds keep running against a closed store")
	}
}
