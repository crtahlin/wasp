// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	chunk "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// BenchmarkReservePutParallel drives concurrent reserve Puts so a mutex profile
// can show whether the two locks Put takes, one on the batch id and one on the
// bin, actually contend under concurrent sync streams. Issue #29 reasons from
// code reading that they do and asks for a profile before any change, because if
// the real constraint is the engine write path underneath, removing lock
// contention shows nothing.
//
// Measured on an idle bench-class machine at 20,000 concurrent Puts, the
// contention is the leveldb write path, not these locks: leveldb's Batch.Commit
// RWMutex and the runtime lock under it are the large majority, the stamp-index
// Get and Has locks a further slice, and the reserve's own per-key multex about
// 1.4 percent. So the answer to #29 is measured, not asserted: removing the two
// locks would change almost nothing, and the constraint is the engine, which is
// consistent with the write-throughput results in
// docs/experiments/storage-engine-eval/results.md.
//
//	Run: go test ./pkg/storer/ -run x -bench BenchmarkReservePutParallel \
//	       -benchtime 20000x -mutexprofile /tmp/mutex.out
//
// then: go tool pprof -top /tmp/mutex.out
//
// Chunks are pre-generated in the test goroutine, because the generator calls
// testing.TB and must not run from the parallel goroutines. Proximity is varied
// so concurrent Puts overlap on bin locks; each chunk carries its own batch.
func BenchmarkReservePutParallel(b *testing.B) {
	baseAddr := swarm.RandAddress(b)
	opts := dbTestOps(baseAddr, 2_000_000, nil, nil, time.Second)
	opts.ValidStamp = func(ch swarm.Chunk) (swarm.Chunk, error) { return ch, nil }

	st, err := diskStorer(b, opts)()
	if err != nil {
		b.Fatal(err)
	}
	putter := st.ReservePutter()

	chunks := make([]swarm.Chunk, b.N)
	for i := range chunks {
		chunks[i] = chunk.GenerateValidRandomChunkAt(b, baseAddr, i%12).WithBatch(3, 2, false)
	}

	var idx atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := idx.Add(1) - 1
			if i >= int64(len(chunks)) {
				return
			}
			if err := putter.Put(context.Background(), chunks[i]); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
