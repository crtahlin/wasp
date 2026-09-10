// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/ethersphere/bee/v2/pkg/bmt"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/chunkstore"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// ProbeStats reports the cost of one probe-based reserve-size sample.
//
// This is a measurement-only path for issue #241, exploring the probe-based
// sublinear alternative sketched in the reserve-size-independent sampling study
// (issue #235). It is a benchmark, like the reserve sampler behind /rchash: it
// is not wired into the redistribution game and changes no protocol surface. It
// exists to measure how the cost of the probe approach scales with k, not to
// change how a node plays the game. See docs/experiments/probe-sample-cost.
type ProbeStats struct {
	K                 int
	Hits              int
	Misses            int
	TotalDuration     time.Duration
	LocateDuration    time.Duration
	ChunkLoadDuration time.Duration
	TaddrDuration     time.Duration
	ChunkLoadFailed   int
}

// ProbeSample runs the probe-based sublinear reserve-size sample and measures
// its cost. It derives k probe addresses inside the node's committed-depth
// neighbourhood from the anchor; for each it seeks the retrieval index to the
// probe, takes the nearest held chunk in address order (its successor), loads
// it, and computes its transformed address with the same anchor-keyed BMT hash
// the real sampler uses, so per-chunk work is comparable. It performs no
// selection or proof, because it is only a cost measurement.
func (db *DB) ProbeSample(ctx context.Context, anchor []byte, committedDepth uint8, k int) (stats ProbeStats, err error) {
	stats.K = k
	start := time.Now()
	// Named return, so the deferred duration lands on the returned value.
	defer func() { stats.TotalDuration = time.Since(start) }()

	if len(anchor) != swarm.HashSize {
		return stats, fmt.Errorf("probe sample: anchor must be %d bytes, got %d", swarm.HashSize, len(anchor))
	}
	if k < 0 {
		return stats, fmt.Errorf("probe sample: k must not be negative")
	}

	hasher := bmt.NewPrefixHasher(anchor)
	idx := db.Storage().IndexStore()

	for i := 0; i < k; i++ {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		probe := deriveProbe(anchor, committedDepth, i)

		// Seek the retrieval index to the probe and take the first entry at or
		// after it, the nearest held chunk in address order. PrefixAtStart
		// seeks within the namespace and iterates forward, so the first result
		// is that successor.
		locateStart := time.Now()
		var winner *chunkstore.RetrievalIndexItem
		err := idx.Iterate(storage.Query{
			Factory:       func() storage.Item { return new(chunkstore.RetrievalIndexItem) },
			Prefix:        string(probe.Bytes()),
			PrefixAtStart: true,
		}, func(res storage.Result) (bool, error) {
			winner = res.Entry.(*chunkstore.RetrievalIndexItem)
			return true, nil
		})
		stats.LocateDuration += time.Since(locateStart)
		if err != nil {
			return stats, fmt.Errorf("probe sample: locate: %w", err)
		}
		if winner == nil {
			// No chunk at or after the probe, only near the very top of the
			// address space; rare, counted rather than wrapped around.
			stats.Misses++
			continue
		}

		loadStart := time.Now()
		ch, err := db.ChunkStore().Get(ctx, winner.Address)
		stats.ChunkLoadDuration += time.Since(loadStart)
		if err != nil {
			stats.ChunkLoadFailed++
			continue
		}

		taddrStart := time.Now()
		_, err = transformedAddress(hasher, ch, getChunkType(ch))
		stats.TaddrDuration += time.Since(taddrStart)
		if err != nil {
			return stats, fmt.Errorf("probe sample: transformed address: %w", err)
		}
		stats.Hits++
	}

	return stats, nil
}

// deriveProbe returns a probe address inside the committed-depth neighbourhood
// of the anchor: the anchor's top committedDepth bits, the rest filled from
// keccak(anchor || i). Probes therefore land among the node's stored chunks, so
// each has a nearby held chunk to find.
func deriveProbe(anchor []byte, committedDepth uint8, i int) swarm.Address {
	h := swarm.NewHasher()
	_, _ = h.Write(anchor)
	var ib [8]byte
	binary.BigEndian.PutUint64(ib[:], uint64(i))
	_, _ = h.Write(ib[:])
	rnd := h.Sum(nil)

	probe := make([]byte, swarm.HashSize)
	full := int(committedDepth) / 8
	if full > swarm.HashSize {
		full = swarm.HashSize
	}
	copy(probe, anchor[:full])
	if bit := int(committedDepth) % 8; bit > 0 && full < swarm.HashSize {
		mask := byte(0xFF << (8 - bit))
		probe[full] = (anchor[full] & mask) | (rnd[full] &^ mask)
		copy(probe[full+1:], rnd[full+1:])
	} else {
		copy(probe[full:], rnd[full:])
	}
	return swarm.NewAddress(probe)
}
