// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node_test

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/node"
)

// TestRadiusWithoutWaitingDoesNotWait is the whole point of issue #398. The
// waiting lookup blocks until the network storage radius arrives from peers,
// and retrieval calls it inside its per-chunk loop, so on the bench every
// chunk of every download stopped there for the first seconds after a
// restart: 324 goroutines parked in it while a 50 MB download delivered
// 524,288 bytes and gave up.
//
// The wait here never returns, so a lookup that delegates to it hangs and the
// test fails on its own deadline rather than on an assertion.
func TestRadiusWithoutWaitingDoesNotWait(t *testing.T) {
	t.Parallel()

	var called atomic.Int32
	blockForever := func() (uint8, error) {
		called.Add(1)
		select {} // never returns, exactly as the real one does before the radius arrives
	}

	lookup := node.RadiusWithoutWaiting(func() bool { return false }, blockForever)

	done := make(chan struct{})
	var (
		radius uint8
		err    error
	)
	go func() {
		defer close(done)
		radius, err = lookup()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the lookup waited for a radius it was told is not known")
	}

	if !errors.Is(err, node.ErrNetworkRadiusUnknown) {
		t.Fatalf("got error %v, want %v", err, node.ErrNetworkRadiusUnknown)
	}
	// The caller skips the neighbourhood fan-out only when the error is
	// non-nil. A zero radius reported as success would read as "every peer is
	// in the neighbourhood", since proximity is never below zero.
	if radius != 0 {
		t.Fatalf("got radius %d, want 0 beside the error", radius)
	}
	if n := called.Load(); n != 0 {
		t.Fatalf("the waiting lookup was called %d times, want 0", n)
	}
}

// TestRadiusWithoutWaitingDelegatesOnceKnown pins the other half: the
// adapter must be inert once the radius is known, or it would change what
// every retrieval does for the life of the node rather than for a few
// seconds after a restart.
func TestRadiusWithoutWaitingDelegatesOnceKnown(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("from the waiting lookup")

	t.Run("value", func(t *testing.T) {
		t.Parallel()
		lookup := node.RadiusWithoutWaiting(
			func() bool { return true },
			func() (uint8, error) { return 11, nil },
		)
		radius, err := lookup()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if radius != 11 {
			t.Fatalf("got radius %d, want the one the waiting lookup returned, 11", radius)
		}
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()
		lookup := node.RadiusWithoutWaiting(
			func() bool { return true },
			func() (uint8, error) { return 0, sentinel },
		)
		if _, err := lookup(); !errors.Is(err, sentinel) {
			t.Fatalf("got error %v, want the one the waiting lookup returned", err)
		}
	})
}

// TestRadiusWithoutWaitingRechecksEveryCall guards against caching the first
// answer. The radius is unknown only at startup, so a lookup that decided
// once would report it unknown for the life of the node and the fan-out would
// never happen again.
func TestRadiusWithoutWaitingRechecksEveryCall(t *testing.T) {
	t.Parallel()

	var known atomic.Bool
	lookup := node.RadiusWithoutWaiting(
		known.Load,
		func() (uint8, error) { return 7, nil },
	)

	if _, err := lookup(); !errors.Is(err, node.ErrNetworkRadiusUnknown) {
		t.Fatalf("before the radius is known: got %v, want %v", err, node.ErrNetworkRadiusUnknown)
	}

	known.Store(true)

	radius, err := lookup()
	if err != nil {
		t.Fatalf("after the radius is known: unexpected error %v", err)
	}
	if radius != 7 {
		t.Fatalf("after the radius is known: got radius %d, want 7", radius)
	}
}
