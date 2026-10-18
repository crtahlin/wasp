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

// TestReservePutterComputesChunkType checks that ReservePutter, used by every
// caller that has not validated the chunk itself, does not trust a chunk type
// it was not given: puts of content addressed and single owner chunks never
// reach the verified-type shadow check (wasp #810).
func TestReservePutterComputesChunkType(t *testing.T) {
	t.Parallel()

	baseAddr := swarm.RandAddress(t)
	db, err := memStorer(t, dbTestOps(baseAddr, 100, nil, nil, time.Minute))()
	if err != nil {
		t.Fatal(err)
	}
	putter := db.ReservePutter()
	for i := range 10 {
		ch := chunk.GenerateTestRandomChunk()
		if i%2 == 1 {
			ch = chunk.GenerateTestRandomSoChunk(t, ch)
		}
		if err := putter.Put(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}
	if got := db.Reserve().ChunkTypeShadowMismatches(); got != 0 {
		t.Fatalf("ReservePutter reached the verified-type check: %d mismatches", got)
	}
	if got := db.Reserve().Size(); got != 10 {
		t.Fatalf("reserve size %d, want 10", got)
	}
}

// TestReservePutVerifiedStoresChunk checks that ReservePutVerified stores a
// chunk like ReservePutter and that the reserve's shadow check agrees with
// the passed types.
func TestReservePutVerifiedStoresChunk(t *testing.T) {
	t.Parallel()

	baseAddr := swarm.RandAddress(t)
	db, err := memStorer(t, dbTestOps(baseAddr, 100, nil, nil, time.Minute))()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		ch := chunk.GenerateTestRandomChunk()
		typ := swarm.ChunkTypeContentAddressed
		if i%2 == 1 {
			ch = chunk.GenerateTestRandomSoChunk(t, ch)
			typ = swarm.ChunkTypeSingleOwner
		}
		if err := db.ReservePutVerified(context.Background(), ch, typ); err != nil {
			t.Fatal(err)
		}
		stampHash, err := ch.Stamp().Hash()
		if err != nil {
			t.Fatal(err)
		}
		has, err := db.ReserveHas(ch.Address(), ch.Stamp().BatchID(), stampHash)
		if err != nil {
			t.Fatal(err)
		}
		if !has {
			t.Fatalf("chunk %d not in the reserve", i)
		}
	}
	if got := db.Reserve().ChunkTypeShadowMismatches(); got != 0 {
		t.Fatalf("shadow check mismatches %d, want 0", got)
	}
}
