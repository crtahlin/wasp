// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer

import (
	"testing"
)

// TestIndexStoreOptionsDefaults pins that a node started with no overrides opens
// the index store with the level-0 triggers it has always used: compaction at
// 8, slowdown at 8, pause at 12. The values are made explicit here rather than
// left to goleveldb's own defaults, so this is also the guard that exposing them
// as configuration changed no node's behaviour. See issue #176.
func TestIndexStoreOptionsDefaults(t *testing.T) {
	t.Parallel()

	got := indexStoreOptions(defaultOptions())

	if got.CompactionL0Trigger != 8 {
		t.Errorf("default CompactionL0Trigger = %d, want 8", got.CompactionL0Trigger)
	}
	if got.WriteL0SlowdownTrigger != 8 {
		t.Errorf("default WriteL0SlowdownTrigger = %d, want 8", got.WriteL0SlowdownTrigger)
	}
	if got.WriteL0PauseTrigger != 12 {
		t.Errorf("default WriteL0PauseTrigger = %d, want 12", got.WriteL0PauseTrigger)
	}
}

// TestIndexStoreOptionsThreadsTriggers checks that a configured value reaches the
// goleveldb options unchanged. goleveldb does not expose the triggers back once
// the database is open, so this seam is where "the option reaches the store" is
// verified.
func TestIndexStoreOptionsThreadsTriggers(t *testing.T) {
	t.Parallel()

	o := defaultOptions()
	o.LdbCompactionL0Trigger = 4
	o.LdbWriteSlowdownTrigger = 16
	o.LdbWritePauseTrigger = 24

	got := indexStoreOptions(o)

	if got.CompactionL0Trigger != 4 {
		t.Errorf("CompactionL0Trigger = %d, want 4", got.CompactionL0Trigger)
	}
	if got.WriteL0SlowdownTrigger != 16 {
		t.Errorf("WriteL0SlowdownTrigger = %d, want 16", got.WriteL0SlowdownTrigger)
	}
	if got.WriteL0PauseTrigger != 24 {
		t.Errorf("WriteL0PauseTrigger = %d, want 24", got.WriteL0PauseTrigger)
	}

	// The refactor that introduced indexStoreOptions must not have dropped the
	// other options along the way.
	if got.Filter == nil {
		t.Error("bloom filter was dropped")
	}
	if got.OpenFilesCacheCapacity != int(o.LdbOpenFilesLimit) {
		t.Errorf("OpenFilesCacheCapacity = %d, want %d", got.OpenFilesCacheCapacity, o.LdbOpenFilesLimit)
	}
	if got.BlockCacheCapacity != int(o.LdbBlockCacheCapacity) {
		t.Errorf("BlockCacheCapacity = %d, want %d", got.BlockCacheCapacity, o.LdbBlockCacheCapacity)
	}
}

// TestIndexStoreOptionsZeroMeansGoleveldbDefault pins the quirk that makes the
// wasp defaults have to be the literal values rather than 0: goleveldb reads a
// trigger of 0 as "use my own default". A configured 0 is passed through
// faithfully, and goleveldb then resolves it to 4/8/12, which is a different
// configuration from the wasp default of 8/8/12. If a future refactor ever made
// the wasp defaults 0, this is what would catch it.
func TestIndexStoreOptionsZeroMeansGoleveldbDefault(t *testing.T) {
	t.Parallel()

	o := defaultOptions()
	o.LdbCompactionL0Trigger = 0
	o.LdbWriteSlowdownTrigger = 0
	o.LdbWritePauseTrigger = 0

	got := indexStoreOptions(o)

	// Passed through as 0, not silently substituted.
	if got.CompactionL0Trigger != 0 {
		t.Errorf("CompactionL0Trigger = %d, want the configured 0 passed through", got.CompactionL0Trigger)
	}

	// goleveldb's own getters resolve 0 to its defaults, which is the behaviour
	// an operator who sets 0 is opting into.
	if v := got.GetCompactionL0Trigger(); v != 4 {
		t.Errorf("goleveldb resolves a 0 CompactionL0Trigger to %d, want its default 4", v)
	}
	if v := got.GetWriteL0SlowdownTrigger(); v != 8 {
		t.Errorf("goleveldb resolves a 0 WriteL0SlowdownTrigger to %d, want its default 8", v)
	}
	if v := got.GetWriteL0PauseTrigger(); v != 12 {
		t.Errorf("goleveldb resolves a 0 WriteL0PauseTrigger to %d, want its default 12", v)
	}
}
