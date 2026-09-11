// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer_test

import (
	"context"
	"testing"
	"time"

	postagetesting "github.com/ethersphere/bee/v2/pkg/postage/testing"
	chunk "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

func TestValidReserveProofMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode  string
		valid bool
	}{
		{storer.ReserveProofModeClassic, true},
		{storer.ReserveProofModeWindowed, true},
		{"", false},
		{"Windowed", false},
		{"fast", false},
	} {
		if got := storer.ValidReserveProofMode(tc.mode); got != tc.valid {
			t.Fatalf("ValidReserveProofMode(%q) = %v, want %v", tc.mode, got, tc.valid)
		}
	}
}

func TestWindowDepthIncrement(t *testing.T) {
	t.Parallel()
	const floor = storer.MinWindowCount
	const capInc = storer.MaxWindowDepthIncrement

	// At or below the floor the window must not shrink: increment 0.
	for _, n := range []uint64{0, 1, floor / 2, floor} {
		if got := storer.WindowDepthIncrement(n); got != 0 {
			t.Fatalf("WindowDepthIncrement(%d) = %d, want 0 (reserve at/below floor)", n, got)
		}
	}

	// Above the floor the increment grows but keeps the window above the floor:
	// n >> increment must stay at least min.
	for _, n := range []uint64{floor + 1, floor * 4, floor * 100, floor << 20} {
		inc := storer.WindowDepthIncrement(n)
		if inc > capInc {
			t.Fatalf("WindowDepthIncrement(%d) = %d exceeds cap %d", n, inc, capInc)
		}
		if inc > 0 && (n>>inc) < uint64(floor) {
			t.Fatalf("WindowDepthIncrement(%d) = %d leaves window %d below floor %d", n, inc, n>>inc, floor)
		}
		// One more bit would either break the floor or exceed the cap.
		if inc < capInc && (n>>(inc+1)) >= uint64(floor) {
			t.Fatalf("WindowDepthIncrement(%d) = %d is not maximal", n, inc)
		}
	}
}

func TestDeriveWindowAnchor(t *testing.T) {
	t.Parallel()
	base := swarm.RandAddress(t)
	anchor := swarm.RandAddressAt(t, base, 8).Bytes()

	for _, committedDepth := range []uint8{0, 1, 5, 8, 12} {
		wa := storer.DeriveWindowAnchor(anchor, committedDepth)

		// The window sits inside the neighbourhood: it shares at least the top
		// committedDepth bits with the anchor.
		if p := swarm.Proximity(wa, anchor); p < committedDepth {
			t.Fatalf("committedDepth %d: window anchor proximity %d, want >= %d", committedDepth, p, committedDepth)
		}

		// Deterministic in the anchor.
		wa2 := storer.DeriveWindowAnchor(anchor, committedDepth)
		if !swarm.NewAddress(wa).Equal(swarm.NewAddress(wa2)) {
			t.Fatalf("committedDepth %d: derivation not deterministic", committedDepth)
		}
	}

	// A different anchor gives a different window position.
	other := swarm.RandAddressAt(t, base, 8).Bytes()
	if swarm.NewAddress(storer.DeriveWindowAnchor(anchor, 8)).Equal(swarm.NewAddress(storer.DeriveWindowAnchor(other, 8))) {
		t.Fatal("distinct anchors produced the same window position")
	}
}

// TestReserveSampleWindowPredicate exercises the shared sampler's window filter
// on a real reserve: an accept-all predicate must match classic, and a
// restrictive predicate must return only in-window chunks and read fewer of them.
func TestReserveSampleWindowPredicate(t *testing.T) {
	t.Parallel()

	const chunkCountPerPO = 10
	const maxPO = 10

	baseAddr := swarm.RandAddress(t)
	opts := dbTestOps(baseAddr, 1000, nil, nil, time.Second)
	opts.ValidStamp = func(ch swarm.Chunk) (swarm.Chunk, error) { return ch, nil }
	st, err := memStorer(t, opts)()
	if err != nil {
		t.Fatal(err)
	}

	timeVar := uint64(time.Now().UnixNano())
	putter := st.ReservePutter()
	for po := range maxPO {
		for range chunkCountPerPO {
			ch := chunk.GenerateValidRandomChunkAt(t, baseAddr, po).WithBatch(3, 2, false)
			ch = ch.WithStamp(postagetesting.MustNewStampWithTimestamp(timeVar - 1))
			if err := putter.Put(context.Background(), ch); err != nil {
				t.Fatal(err)
			}
		}
	}

	var (
		radius uint8 = 3
		anchor       = swarm.RandAddressAt(t, baseAddr, int(radius)).Bytes()
	)

	classic, err := st.ReserveSample(context.TODO(), anchor, radius, timeVar, nil)
	if err != nil {
		t.Fatal(err)
	}

	acceptAll, err := st.ReserveSampleWithWindow(context.TODO(), anchor, radius, timeVar, nil, func([]byte) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(acceptAll.Items) != len(classic.Items) {
		t.Fatalf("accept-all window: %d items, classic %d", len(acceptAll.Items), len(classic.Items))
	}
	for i := range classic.Items {
		if !acceptAll.Items[i].TransformedAddress.Equal(classic.Items[i].TransformedAddress) {
			t.Fatalf("accept-all window item %d differs from classic", i)
		}
	}

	// A window one bit deeper than the radius: keep only chunks that also match
	// the window anchor at radius+1. Every returned chunk must satisfy it, and
	// the sampler must have iterated no more than classic did.
	windowDepth := radius + 1
	windowAnchor := storer.DeriveWindowAnchor(anchor, radius)
	inWindow := func(addr []byte) bool {
		return swarm.Proximity(addr, windowAnchor) >= windowDepth
	}
	windowed, err := st.ReserveSampleWithWindow(context.TODO(), anchor, radius, timeVar, nil, inWindow)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range windowed.Items {
		if !inWindow(it.ChunkAddress.Bytes()) {
			t.Fatalf("windowed sample returned a chunk outside the window: %s", it.ChunkAddress)
		}
	}
	if windowed.Stats.TotalIterated > classic.Stats.TotalIterated {
		t.Fatalf("windowed iterated %d chunks, more than classic %d", windowed.Stats.TotalIterated, classic.Stats.TotalIterated)
	}
}

// TestWindowedSampleSmallReserveIsClassic checks that on a reserve below the
// window floor, WindowedSample degenerates to the classic sample.
func TestWindowedSampleSmallReserveIsClassic(t *testing.T) {
	t.Parallel()

	const chunkCountPerPO = 10
	const maxPO = 10 // 100 chunks, well below MinWindowCount

	baseAddr := swarm.RandAddress(t)
	opts := dbTestOps(baseAddr, 1000, nil, nil, time.Second)
	opts.ValidStamp = func(ch swarm.Chunk) (swarm.Chunk, error) { return ch, nil }
	st, err := memStorer(t, opts)()
	if err != nil {
		t.Fatal(err)
	}

	timeVar := uint64(time.Now().UnixNano())
	putter := st.ReservePutter()
	for po := range maxPO {
		for range chunkCountPerPO {
			ch := chunk.GenerateValidRandomChunkAt(t, baseAddr, po).WithBatch(3, 2, false)
			ch = ch.WithStamp(postagetesting.MustNewStampWithTimestamp(timeVar - 1))
			if err := putter.Put(context.Background(), ch); err != nil {
				t.Fatal(err)
			}
		}
	}

	var (
		radius uint8 = 3
		anchor       = swarm.RandAddressAt(t, baseAddr, int(radius)).Bytes()
	)

	classic, err := st.ReserveSample(context.TODO(), anchor, radius, timeVar, nil)
	if err != nil {
		t.Fatal(err)
	}
	windowed, err := st.WindowedSample(context.TODO(), anchor, radius, timeVar, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(windowed.Items) != len(classic.Items) {
		t.Fatalf("small reserve: windowed %d items, classic %d, expected equal", len(windowed.Items), len(classic.Items))
	}
	for i := range classic.Items {
		if !windowed.Items[i].TransformedAddress.Equal(classic.Items[i].TransformedAddress) {
			t.Fatalf("small reserve: windowed item %d differs from classic", i)
		}
	}
}
