// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer_test

import (
	"context"
	"testing"
	"time"

	chunk "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// TestSubscribeBinOutOfRangeNeverDelivers measures what #643 reasoned from
// code: a subscription to a bin the reserve cannot hold (40 >= MaxBins)
// delivers nothing, however many chunks are put, so a pull-sync request for
// it would wait until its context ends.
func TestSubscribeBinOutOfRangeNeverDelivers(t *testing.T) {
	t.Parallel()

	baseAddr := swarm.RandAddress(t)
	db, err := memStorer(t, dbTestOps(baseAddr, 1000, nil, nil, time.Second))()
	if err != nil {
		t.Fatal(err)
	}

	// Low bins only: since Bee v2.8.3 the reserve accepts only valid chunks,
	// and a valid random chunk in a high bin takes about 2^bin attempts. The
	// chunks are made before the subscription, whose wait is timed.
	chunks := make([]swarm.Chunk, 0, 8*3)
	for po := range 8 {
		for range 3 {
			chunks = append(chunks, chunk.GenerateTestRandomChunkAt(t, baseAddr, po))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	binC, unsub, errC := db.SubscribeBin(ctx, 40, 0)
	defer unsub()

	putter := db.ReservePutter()
	for _, ch := range chunks {
		if err := putter.Put(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case c, ok := <-binC:
		if ok {
			t.Fatalf("bin 40 delivered chunk %s, want nothing", c.Address)
		}
		t.Fatal("bin 40 subscription closed before its context ended")
	case err := <-errC:
		if ctx.Err() == nil {
			t.Fatalf("bin 40 subscription failed early: %v", err)
		}
	case <-ctx.Done():
	}

	// Control: the same reserve delivers on a valid bin, so the silence above
	// is not an empty reserve.
	ctrl, unsubCtrl, _ := db.SubscribeBin(context.Background(), 0, 0)
	defer unsubCtrl()
	select {
	case <-ctrl:
	case <-time.After(5 * time.Second):
		t.Fatal("bin 0 delivered nothing, want the control to receive a chunk")
	}
}
