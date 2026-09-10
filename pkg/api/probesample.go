// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"net/http"
	"runtime"

	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/gorilla/mux"
)

// ProbeSampleResponse reports the cost of one probe-based reserve-size sample.
// See issue #241 and docs/experiments/probe-sample-cost. This endpoint is a
// measurement-only benchmark, like /rchash; it is not part of the
// redistribution game and changes no protocol surface.
type ProbeSampleResponse struct {
	K                       int     `json:"k"`
	Hits                    int     `json:"hits"`
	Misses                  int     `json:"misses"`
	ChunkLoadFailed         int     `json:"chunkLoadFailed"`
	DurationSeconds         float64 `json:"durationSeconds"`
	LocateSeconds           float64 `json:"locateSeconds"`
	ChunkLoadSeconds        float64 `json:"chunkLoadSeconds"`
	TaddrSeconds            float64 `json:"taddrSeconds"`
	Mallocs                 uint64  `json:"mallocs"`
	AllocBytes              uint64  `json:"allocBytes"`
	NumGC                   uint32  `json:"numGC"`
	ReserveSizeWithinRadius uint64  `json:"reserveSizeWithinRadius"`
	StorageRadius           uint8   `json:"storageRadius"`
	CommittedDepth          uint8   `json:"committedDepth"`
}

// probeSample runs the probe-based sublinear reserve-size sample and reports
// its cost and resource use. It mirrors /rchash: kept for measuring the
// sampler, no documentation or handler tests are added. The anchor must be the
// node's own overlay so probes land in its neighbourhood.
func (s *Service) probeSample(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("get_probesample").Build()

	paths := struct {
		Depth  uint8  `map:"depth"`
		Anchor string `map:"anchor,decHex" validate:"required"`
		K      int    `map:"k"`
	}{}
	if response := s.mapStructure(mux.Vars(r), &paths); response != nil {
		response("invalid path params", logger, w)
		return
	}

	anchor := []byte(paths.Anchor)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	stats, err := s.storer.ProbeSample(r.Context(), anchor, paths.Depth, paths.K)
	runtime.ReadMemStats(&after)
	if err != nil {
		logger.Error(err, "probe sample failed")
		jsonhttp.InternalServerError(w, "probe sample failed")
		return
	}

	resp := ProbeSampleResponse{
		K:                       stats.K,
		Hits:                    stats.Hits,
		Misses:                  stats.Misses,
		ChunkLoadFailed:         stats.ChunkLoadFailed,
		DurationSeconds:         stats.TotalDuration.Seconds(),
		LocateSeconds:           stats.LocateDuration.Seconds(),
		ChunkLoadSeconds:        stats.ChunkLoadDuration.Seconds(),
		TaddrSeconds:            stats.TaddrDuration.Seconds(),
		Mallocs:                 after.Mallocs - before.Mallocs,
		AllocBytes:              after.TotalAlloc - before.TotalAlloc,
		NumGC:                   after.NumGC - before.NumGC,
		ReserveSizeWithinRadius: s.storer.ReserveSizeWithinRadius(),
		StorageRadius:           s.storer.StorageRadius(),
		CommittedDepth:          s.storer.CommittedDepth(),
	}

	// Logged like "reserve sampler finished" so a bench run reads the result
	// from the log rather than the HTTP body.
	logger.Info("probe sample finished",
		"k", resp.K, "hits", resp.Hits, "misses", resp.Misses,
		"chunk_load_failed", resp.ChunkLoadFailed,
		"duration", stats.TotalDuration,
		"locate", stats.LocateDuration,
		"chunk_load", stats.ChunkLoadDuration,
		"taddr", stats.TaddrDuration,
		"mallocs", resp.Mallocs, "alloc_bytes", resp.AllocBytes, "num_gc", resp.NumGC,
		"reserve_within_radius", resp.ReserveSizeWithinRadius,
		"storage_radius", resp.StorageRadius, "committed_depth", resp.CommittedDepth,
	)

	jsonhttp.OK(w, resp)
}
