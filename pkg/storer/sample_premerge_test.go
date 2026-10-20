// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer

// This file keeps the reserve sampler as it was before the port of Bee
// v2.8.3's sampler changes, copied unchanged from origin/main at the
// sync's fork point (wasp a07745812) except for the renamed identifiers.
// The sampler equivalence tests run it next to the current sampler on the
// same reserve and require identical samples. It exists only in tests.

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"time"

	"github.com/ethersphere/bee/v2/pkg/bmt"
	"github.com/ethersphere/bee/v2/pkg/postage"
	"github.com/ethersphere/bee/v2/pkg/safe"
	"github.com/ethersphere/bee/v2/pkg/soc"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/chunkstamp"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/reserve"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"golang.org/x/sync/errgroup"
)

// ReserveSamplePreMerge runs the sampler as it was before the port.
func (db *DB) ReserveSamplePreMerge(
	ctx context.Context,
	anchor []byte,
	committedDepth uint8,
	consensusTime uint64,
	minBatchBalance *big.Int,
) (Sample, error) {
	return db.reserveSamplePreMerge(ctx, anchor, committedDepth, consensusTime, minBatchBalance, nil)
}

type preMergeLoadedChunk struct {
	item  *reserve.ChunkBinItem
	chunk swarm.Chunk
}

func (db *DB) reserveSamplePreMerge(
	ctx context.Context,
	anchor []byte,
	committedDepth uint8,
	consensusTime uint64,
	minBatchBalance *big.Int,
	inWindow func([]byte) bool,
) (Sample, error) {
	// Signal that a sample is running so the puller pauses pulling and
	// eviction pauses, leaving the store quiet. Cleared on every return,
	// including error and cancellation, so a failed sample never leaves
	// pulling or eviction paused. See issues #23 and #649. sampleStarted
	// also waits for an eviction round or radius step in progress, so the
	// radius read below is the one the whole sample sees.
	lottery := isLotterySample(ctx)
	db.sampleStarted(lottery)
	defer db.sampleDone(lottery)

	g, ctx := errgroup.WithContext(ctx)

	allStats := &SampleStats{}
	statsLock := sync.Mutex{}
	addStats := func(stats SampleStats) {
		statsLock.Lock()
		allStats.add(stats)
		statsLock.Unlock()
	}

	workers := max(4, runtime.NumCPU())
	t := time.Now()

	defer func() {
		duration := time.Since(t)
		err := g.Wait()
		db.recordReserveSampleMetrics(duration, allStats, workers, err)
	}()

	excludedBatchIDs, err := db.preMergeBatchesBelowValue(minBatchBalance)
	if err != nil {
		db.logger.Error(err, "get batches below value")
	}

	allStats.BatchesBelowValueDuration = time.Since(t)

	chunkC := make(chan *reserve.ChunkBinItem, 3*workers)

	// Phase 1: Iterate chunk addresses
	g.Go(safe.RunFunc(db.logger, "storer-sample-iterate-chunks", func() error {
		start := time.Now()
		stats := SampleStats{}
		defer func() {
			stats.IterationDuration = time.Since(start)
			close(chunkC)
			addStats(stats)
		}()

		// Walk only the bins that can hold the anchor's neighbourhood. On a
		// node with reserve-capacity-doubling d the reserve holds 2^d
		// neighbourhoods, and walking all of them to sample one cost 8x the
		// index reads at d=3. When the anchor is far from the overlay the
		// range is still one bin holding several neighbourhoods, up to 2^(d-1)
		// of them in bin d below the committed depth. See issue #568.
		lo, hi, ok := sampleBins(db.baseAddr.Bytes(), anchor, committedDepth, db.StorageRadius())
		if !ok {
			return nil
		}

		err := db.reserve.IterateChunksItems(lo, func(ch *reserve.ChunkBinItem) (bool, error) {
			// Keys are ordered by bin first, so nothing after this is in range.
			if ch.Bin > hi {
				return true, nil
			}
			stats.ChunkBinScanned++

			// Required when p < committedDepth: the range is then bin p, which
			// also holds the other neighbourhoods that share p bits with the
			// overlay, and this drops them. When p >= committedDepth every
			// entry in range passes. It cannot recover chunks a range that is
			// too narrow would miss; the tests guard that side.
			if swarm.Proximity(ch.Address.Bytes(), anchor) < committedDepth {
				return false, nil
			}
			// Windowed proof (#273): when a window predicate is set, keep only
			// the chunks inside the anchor-derived sub-window. Placed before the
			// count below so TotalIterated stays the honest cost measure, the
			// number of chunks actually read. A nil predicate is classic mode,
			// the whole neighbourhood.
			if inWindow != nil && !inWindow(ch.Address.Bytes()) {
				return false, nil
			}
			// Counted before the filters below, so that TotalIterated keeps
			// meaning "chunks in the neighbourhood" rather than silently
			// becoming "chunks in the neighbourhood that were also eligible"
			// now that the filters run here instead of in the reader pool.
			stats.TotalIterated++

			// Decided here rather than in the reader pool, because neither
			// decision depends on anything a later stage learns. Deciding late
			// meant every stage after this one paid for chunks that were always
			// going to be discarded — since #11 that includes an index lookup
			// per chunk and a slot in the sort window.
			if _, found := excludedBatchIDs[string(ch.BatchID)]; found {
				stats.BelowBalanceIgnored++
				return false, nil
			}
			if ch.ChunkType != swarm.ChunkTypeSingleOwner &&
				ch.ChunkType != swarm.ChunkTypeContentAddressed {
				stats.RogueChunk++
				return false, nil
			}

			select {
			case chunkC <- ch:
				return false, nil
			case <-ctx.Done():
				return false, ctx.Err()
			}
		})
		return err
	}))

	readers := db.reserveOptions.samplerReadConcurrency
	if readers <= 0 {
		readers = workers
	}

	// Phase 1b: order the reads by physical position.
	//
	// Bin order comes from the chunk address, which is a hash, so it says
	// nothing about where the data sits in the sharky shard files. Every read
	// is therefore a seek to an unrelated position, and raising the reader
	// count does not help: more concurrent random reads is still random reads.
	//
	// This stage buffers a window of items, reads each one's location from
	// retrievalIdx, sorts the window by (shard, slot), and emits it in that
	// order. The location is used only as a sort key and then discarded — the
	// readers still go through ChunkStore().Get, which repeats the lookup.
	//
	// The repeat is deliberate. Get holds a per-address lock across the lookup
	// and the read; splitting them would open a window as wide as this one, in
	// which the chunk can be evicted and its slot reused by an unrelated Put.
	// The read would then return another chunk's bytes labelled with the
	// address that was asked for, and the sampler would hash those. The second
	// lookup is for a key read moments earlier, so it is served from cache
	// rather than the device. See docs/experiments/sampler-read-ordering/spec.md.
	readC := (<-chan *reserve.ChunkBinItem)(chunkC)
	sortWindow := db.reserveOptions.samplerSortWindow
	if sortWindow > maxSamplerSortWindow {
		db.logger.Warning("reserve sampler sort window capped",
			"requested", sortWindow, "used", maxSamplerSortWindow)
		sortWindow = maxSamplerSortWindow
	}
	if sortWindow > 0 {
		readC = db.orderSampleReads(ctx, g, chunkC, sortWindow, readers, addStats)
	}

	// Phase 2: load chunk data, then hash it.
	//
	// These are separated because they are limited by different things. Loading
	// is disk-bound and spends most of its time blocked; hashing is CPU-bound.
	// Running both in one pool means every worker holds a core while waiting on
	// the disk, so the concurrency offered to the storage layer is pinned to the
	// core count whether or not that is the right number. See issue #9 and
	// docs/experiments/sampler-io-split/spec.md.
	//
	// Sample selection takes the smallest transformed addresses and does not
	// depend on the order items arrive in, so decoupling the stages is safe.
	sampleItemChan := make(chan SampleItem, 3*workers)
	// Bound the hand-off buffer. Unlike chunkC, which carries bin items, this
	// carries loaded chunk data, so its depth is memory an operator can set by
	// raising the reader count: at 3 per reader and ~4 KiB a chunk, a careless
	// value costs hundreds of megabytes. The cap is generous enough that it
	// never binds at sane settings.
	loadedBuf := min(3*readers, maxLoadedChunkBuffer)
	loadedC := make(chan preMergeLoadedChunk, loadedBuf)

	db.logger.Debug("reserve sampler workers",
		"readers", readers, "hashers", workers, "sort_window", sortWindow)
	statsLock.Lock()
	allStats.ReadConcurrency = readers
	statsLock.Unlock()

	var readersWg sync.WaitGroup
	for range readers {
		readersWg.Add(1)
		g.Go(safe.RunFunc(db.logger, "storer-sample-reader", func() error {
			defer readersWg.Done()
			wstat := SampleStats{}
			defer func() { addStats(wstat) }()

			for chItem := range readC {
				chunkLoadStart := time.Now()
				chunk, err := db.ChunkStore().Get(ctx, chItem.Address)
				chunkLoadDuration := time.Since(chunkLoadStart)
				if err != nil {
					wstat.ChunkLoadFailed++
					db.logger.Debug("failed loading chunk", "chunk_address", chItem.Address, "error", err)
					continue
				}
				wstat.ChunkLoadDuration += chunkLoadDuration

				select {
				case loadedC <- preMergeLoadedChunk{item: chItem, chunk: chunk}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}))
	}

	go func() {
		readersWg.Wait()
		close(loadedC)
	}()

	for range workers {
		g.Go(safe.RunFunc(db.logger, "storer-sample-hasher", func() error {
			wstat := SampleStats{}
			// One hasher per goroutine: bmt hashers carry state and are not
			// safe to share.
			hasher := bmt.NewPrefixHasher(anchor)
			defer func() { addStats(wstat) }()

			for lc := range loadedC {
				taddrStart := time.Now()
				taddr, err := preMergeTransformedAddress(hasher, lc.chunk, lc.item.ChunkType)
				if err != nil {
					return err
				}
				wstat.TaddrDuration += time.Since(taddrStart)

				select {
				case sampleItemChan <- SampleItem{
					TransformedAddress: taddr,
					ChunkAddress:       lc.chunk.Address(),
					ChunkData:          lc.chunk.Data(),
					Stamp:              postage.NewStamp(lc.item.BatchID, nil, nil, nil),
				}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}))
	}

	go func() {
		_ = g.Wait()
		close(sampleItemChan)
	}()

	sampleItems := make([]SampleItem, 0, SampleSize)

	// insert function will insert the new item in its correct place. If the sample
	// size goes beyond what we need we omit the last item.
	insert := func(item SampleItem) {
		added := false
		for i, sItem := range sampleItems {
			if le(item.TransformedAddress, sItem.TransformedAddress) {
				sampleItems = append(sampleItems[:i+1], sampleItems[i:]...)
				sampleItems[i] = item
				added = true
				break
			} else if item.TransformedAddress.Compare(sItem.TransformedAddress) == 0 { // ensuring to pass the check order function of redistribution contract
				// replace the chunk at index if the chunk is CAC
				ch := swarm.NewChunk(item.ChunkAddress, item.ChunkData)
				_, err := soc.FromChunk(ch)
				if err != nil {
					sampleItems[i] = item
				}
				return
			}
		}
		if len(sampleItems) > SampleSize {
			sampleItems = sampleItems[:SampleSize]
		}
		if len(sampleItems) < SampleSize && !added {
			sampleItems = append(sampleItems, item)
		}
	}

	// Phase 3: Assemble the sample. Here we need to assemble only the first SampleSize
	// no of items from the results of the 2nd phase.
	// In this step stamps are loaded and validated only if chunk will be added to sample.
	stats := SampleStats{}
	for item := range sampleItemChan {
		currentMaxAddr := swarm.EmptyAddress
		if len(sampleItems) > 0 {
			currentMaxAddr = sampleItems[len(sampleItems)-1].TransformedAddress
		}

		if le(item.TransformedAddress, currentMaxAddr) || len(sampleItems) < SampleSize {
			stamp, err := chunkstamp.LoadWithBatchID(db.storage.IndexStore(), "reserve", item.ChunkAddress, item.Stamp.BatchID())
			if err != nil {
				stats.StampLoadFailed++
				db.logger.Debug("failed loading stamp", "chunk_address", item.ChunkAddress, "error", err)
				continue
			}

			ch := swarm.NewChunk(item.ChunkAddress, item.ChunkData).WithStamp(stamp)

			// check if the timestamp on the postage stamp is not later than the consensus time.
			if binary.BigEndian.Uint64(ch.Stamp().Timestamp()) > consensusTime {
				stats.NewIgnored++
				continue
			}

			stampValidStart := time.Now()
			if _, err := db.validStamp(ch); err != nil {
				stats.InvalidStamp++
				db.logger.Debug("invalid stamp for chunk", "chunk_address", ch.Address(), "error", err)
				continue
			}

			stampValidDuration := time.Since(stampValidStart)
			stats.ValidStampDuration += stampValidDuration

			item.Stamp = postage.NewStamp(stamp.BatchID(), stamp.Index(), stamp.Timestamp(), stamp.Sig())

			insert(item)
			stats.SampleInserts++
		}
	}
	addStats(stats)

	allStats.TotalDuration = time.Since(t)

	if err := g.Wait(); err != nil {
		db.logger.Info("reserve sampler finished with error", "err", err, "duration", time.Since(t), "storage_radius", committedDepth, "consensus_time_ns", consensusTime, "stats", fmt.Sprintf("%+v", allStats))

		return Sample{}, fmt.Errorf("sampler: failed creating sample: %w", err)
	}

	db.logger.Info("reserve sampler finished", "duration", time.Since(t), "storage_radius", committedDepth, "consensus_time_ns", consensusTime, "stats", fmt.Sprintf("%+v", allStats))

	return Sample{Stats: *allStats, Items: sampleItems}, nil
}

func (db *DB) preMergeBatchesBelowValue(until *big.Int) (map[string]struct{}, error) {
	res := make(map[string]struct{})

	if until == nil {
		return res, nil
	}

	err := db.batchstore.Iterate(func(b *postage.Batch) (bool, error) {
		if b.Value.Cmp(until) < 0 {
			res[string(b.ID)] = struct{}{}
		}
		return false, nil
	})

	return res, err
}

func preMergeTransformedAddress(hasher bmt.Hasher, chunk swarm.Chunk, chType swarm.ChunkType) (swarm.Address, error) {
	switch chType {
	case swarm.ChunkTypeContentAddressed:
		return preMergeTransformedAddressCAC(hasher, chunk)
	case swarm.ChunkTypeSingleOwner:
		return preMergeTransformedAddressSOC(hasher, chunk)
	default:
		return swarm.ZeroAddress, fmt.Errorf("chunk type [%v] is not valid", chType)
	}
}

func preMergeTransformedAddressCAC(hasher bmt.Hasher, chunk swarm.Chunk) (swarm.Address, error) {
	hasher.Reset()
	hasher.SetHeader(chunk.Data()[:bmt.SpanSize])

	_, err := hasher.Write(chunk.Data()[bmt.SpanSize:])
	if err != nil {
		return swarm.ZeroAddress, err
	}

	return swarm.NewAddress(hasher.Sum(nil)), nil
}

func preMergeTransformedAddressSOC(hasher bmt.Hasher, socChunk swarm.Chunk) (swarm.Address, error) {
	// Calculate transformed address from wrapped chunk
	cacChunk, err := soc.UnwrapCAC(socChunk)
	if err != nil {
		return swarm.ZeroAddress, err
	}
	taddrCac, err := preMergeTransformedAddressCAC(hasher, cacChunk)
	if err != nil {
		return swarm.ZeroAddress, err
	}

	// Hash address and transformed address to make transformed address for this SOC
	sHasher := swarm.NewHasher()
	if _, err := sHasher.Write(socChunk.Address().Bytes()); err != nil {
		return swarm.ZeroAddress, err
	}
	if _, err := sHasher.Write(taddrCac.Bytes()); err != nil {
		return swarm.ZeroAddress, err
	}

	return swarm.NewAddress(sHasher.Sum(nil)), nil
}
