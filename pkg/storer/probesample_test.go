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
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

func TestProbeSample(t *testing.T) {
	t.Parallel()

	baseAddr := swarm.RandAddress(t)
	st, err := diskStorer(t, dbTestOps(baseAddr, 1000, nil, nil, time.Second))()
	if err != nil {
		t.Fatal(err)
	}

	timeVar := uint64(time.Now().UnixNano())
	putter := st.ReservePutter()
	for po := range 8 {
		for range 20 {
			ch := chunk.GenerateValidRandomChunkAt(t, baseAddr, po).WithBatch(3, 2, false)
			ch = ch.WithStamp(postagetesting.MustNewStampWithTimestamp(timeVar - 1))
			if err := putter.Put(context.Background(), ch); err != nil {
				t.Fatal(err)
			}
		}
	}

	anchor := baseAddr.Bytes()
	const k = 20

	stats, err := st.ProbeSample(context.Background(), anchor, 0, k)
	if err != nil {
		t.Fatal(err)
	}
	if stats.K != k {
		t.Fatalf("k = %d, want %d", stats.K, k)
	}
	if got := stats.Hits + stats.Misses + stats.ChunkLoadFailed; got != k {
		t.Fatalf("probes unaccounted: hits=%d misses=%d failed=%d, sum %d want %d",
			stats.Hits, stats.Misses, stats.ChunkLoadFailed, got, k)
	}
	if stats.Hits == 0 {
		t.Fatal("no probe found a chunk in a populated reserve")
	}
	if stats.TotalDuration == 0 {
		t.Fatal("no duration recorded")
	}

	// The anchor must be a full address; a short anchor is rejected.
	if _, err := st.ProbeSample(context.Background(), []byte("short"), 0, 1); err == nil {
		t.Fatal("expected an error for a short anchor")
	}
}
