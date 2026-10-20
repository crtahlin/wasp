// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package migration_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/log"
	postagetesting "github.com/ethersphere/bee/v2/pkg/postage/testing"
	"github.com/ethersphere/bee/v2/pkg/storage"
	chunktest "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/storer/internal"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/chunkstamp"
	pinstore "github.com/ethersphere/bee/v2/pkg/storer/internal/pinning"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/reserve"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/stampindex"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/transaction"
	localmigration "github.com/ethersphere/bee/v2/pkg/storer/migration"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// rogueCount is more than two of the migration's batches of 1000, so that a
// stop between batches leaves some removed and some not.
const rogueCount = 2500

// rogueFixture is a reserve as wasp holds it: valid reserve chunks, a
// provider record (a single owner chunk), content kept outside the reserve
// in a pin collection (pinned content and stampless local-ingest content are
// both kept this way), and legacy reserve entries the migration exists for.
type rogueFixture struct {
	reserveCAC, record, mistyped, pinned, pinnedInReserve swarm.Chunk
	pinnedRogue                                           swarm.Chunk // invalid, in the reserve and pinned
	rogue                                                 []swarm.Chunk
	pinRoot                                               swarm.Address
}

func putReserveEntry(t *testing.T, st transaction.Storage, binID uint64, ch swarm.Chunk, chType swarm.ChunkType, data bool) {
	t.Helper()
	stampHash, err := ch.Stamp().Hash()
	if err != nil {
		t.Fatal(err)
	}
	item := &reserve.ChunkBinItem{
		Bin:       0,
		BinID:     binID,
		Address:   ch.Address(),
		BatchID:   ch.Stamp().BatchID(),
		ChunkType: chType,
		StampHash: stampHash,
	}
	err = st.Run(context.Background(), func(s transaction.Store) error {
		err := errors.Join(
			s.IndexStore().Put(item),
			s.IndexStore().Put(batchRadiusItem(item)),
			chunkstamp.Store(s.IndexStore(), "reserve", ch),
			stampindex.Store(s.IndexStore(), "reserve", ch),
		)
		if err != nil || !data {
			return err
		}
		return s.ChunkStore().Put(context.Background(), ch)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newRogueFixture(t *testing.T, st transaction.Storage) *rogueFixture {
	t.Helper()
	stamped := func(ch swarm.Chunk) swarm.Chunk { return ch.WithStamp(postagetesting.MustNewStamp()) }
	invalid := func() swarm.Chunk { return stamped(chunktest.GenerateTestRandomInvalidChunk()) }

	f := &rogueFixture{
		reserveCAC:      stamped(chunktest.GenerateTestRandomChunk()),
		record:          stamped(chunktest.GenerateTestRandomSoChunk(t, chunktest.GenerateTestRandomChunk())),
		mistyped:        stamped(chunktest.GenerateTestRandomChunk()),
		pinned:          stamped(chunktest.GenerateTestRandomChunk()),
		pinnedInReserve: stamped(chunktest.GenerateTestRandomChunk()),
		pinnedRogue:     invalid(),
		pinRoot:         swarm.RandAddress(t),
	}
	for range rogueCount {
		f.rogue = append(f.rogue, invalid())
	}

	// the pin collection, outside the reserve
	err := st.Run(context.Background(), func(s transaction.Store) error {
		putter, err := pinstore.NewCollection(s.IndexStore())
		if err != nil {
			return err
		}
		for _, ch := range []swarm.Chunk{f.pinned, f.pinnedInReserve, f.pinnedRogue} {
			if err := putter.Put(context.Background(), s, ch); err != nil {
				return err
			}
		}
		return putter.Close(s.IndexStore(), f.pinRoot)
	})
	if err != nil {
		t.Fatal(err)
	}

	binID := uint64(1)
	next := func() uint64 { binID++; return binID }
	putReserveEntry(t, st, next(), f.reserveCAC, swarm.ChunkTypeContentAddressed, true)
	putReserveEntry(t, st, next(), f.record, swarm.ChunkTypeSingleOwner, true)
	putReserveEntry(t, st, next(), f.mistyped, swarm.ChunkTypeUnspecified, true)
	putReserveEntry(t, st, next(), f.pinnedInReserve, swarm.ChunkTypeUnspecified, true)
	putReserveEntry(t, st, next(), f.pinnedRogue, swarm.ChunkTypeUnspecified, true)
	for _, ch := range f.rogue {
		putReserveEntry(t, st, next(), ch, swarm.ChunkTypeUnspecified, true)
	}
	return f
}

// check asserts the state after a completed migration.
func (f *rogueFixture) check(t *testing.T, st transaction.Storage) {
	t.Helper()
	ctx := context.Background()

	bins := map[string]swarm.ChunkType{}
	err := st.IndexStore().Iterate(storage.Query{Factory: func() storage.Item { return &reserve.ChunkBinItem{} }},
		func(r storage.Result) (bool, error) {
			item := r.Entry.(*reserve.ChunkBinItem)
			bins[item.Address.ByteString()] = item.ChunkType
			return false, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	radius, err := st.IndexStore().Count(&reserve.BatchRadiusItem{})
	if err != nil {
		t.Fatal(err)
	}

	// Exactly the entries that fail validation are gone, and the reserve's
	// two indexes still count the same entries.
	want := map[string]swarm.ChunkType{
		f.reserveCAC.Address().ByteString():      swarm.ChunkTypeContentAddressed,
		f.record.Address().ByteString():          swarm.ChunkTypeSingleOwner,
		f.mistyped.Address().ByteString():        swarm.ChunkTypeContentAddressed,
		f.pinnedInReserve.Address().ByteString(): swarm.ChunkTypeContentAddressed,
	}
	if len(bins) != len(want) || radius != len(want) {
		t.Fatalf("reserve holds %d bin entries and %d radius entries, want %d of each", len(bins), radius, len(want))
	}
	for addr, typ := range want {
		if got, ok := bins[addr]; !ok || got != typ {
			t.Errorf("reserve entry %x: type %v present %t, want type %v", addr, got, ok, typ)
		}
	}
	for _, ch := range append([]swarm.Chunk{f.pinnedRogue}, f.rogue...) {
		if _, err := chunkstamp.LoadWithBatchID(st.IndexStore(), "reserve", ch.Address(), ch.Stamp().BatchID()); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("rogue entry %s: reserve stamp still present (%v)", ch.Address(), err)
		}
	}
	for _, ch := range f.rogue {
		if has, err := st.ChunkStore().Has(ctx, ch.Address()); err != nil || has {
			t.Fatalf("rogue chunk %s still in the chunk store (%v)", ch.Address(), err)
		}
	}

	// Content outside the reserve is kept, also where the reserve held the
	// same address: the migration only drops the reserve's reference.
	for _, ch := range []swarm.Chunk{f.pinned, f.pinnedInReserve, f.pinnedRogue, f.reserveCAC, f.record, f.mistyped} {
		got, err := st.ChunkStore().Get(ctx, ch.Address())
		if err != nil {
			t.Fatalf("chunk %s: %v", ch.Address(), err)
		}
		if string(got.Data()) != string(ch.Data()) {
			t.Fatalf("chunk %s: data changed", ch.Address())
		}
	}
	var pinnedCount int
	err = pinstore.IterateCollection(st.IndexStore(), f.pinRoot, func(swarm.Address) (bool, error) {
		pinnedCount++
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pinnedCount != 3 {
		t.Fatalf("pin collection lists %d chunks, want 3", pinnedCount)
	}

	// A second run finds nothing left to change.
	if err := localmigration.RemoveRogueChunks(st, log.Noop)(); err != nil {
		t.Fatal(err)
	}
	again, err := st.IndexStore().Count(&reserve.BatchRadiusItem{})
	if err != nil {
		t.Fatal(err)
	}
	if again != len(want) {
		t.Fatalf("a second run left %d radius entries, want %d", again, len(want))
	}
}

// TestRemoveRogueChunksKeepsWaspContent runs the rogue-chunk migration of Bee
// v2.8.3 over a reserve with wasp's content: it removes exactly the entries
// whose chunk fails validation, and keeps provider records and content held
// outside the reserve.
func TestRemoveRogueChunksKeepsWaspContent(t *testing.T) {
	t.Parallel()

	st := internal.NewInmemStorage()
	f := newRogueFixture(t, st)
	if err := localmigration.RemoveRogueChunks(st, log.Noop)(); err != nil {
		t.Fatal(err)
	}
	f.check(t, st)
}

// stoppingStorage is a store whose transactions start failing after a number
// of them, as a node killed during the migration stops committing. The
// transactions committed before stay committed.
type stoppingStorage struct {
	transaction.Storage
	left atomic.Int64
}

var errKilled = errors.New("killed")

func (s *stoppingStorage) Run(ctx context.Context, f func(transaction.Store) error) error {
	if s.left.Add(-1) < 0 {
		return errKilled
	}
	return s.Storage.Run(ctx, f)
}

// TestRemoveRogueChunksResumesAfterKill stops the migration between two of
// its batches and runs it again, as a restart after a kill does: the second
// run finishes, and the result is the same as one uninterrupted run.
func TestRemoveRogueChunksResumesAfterKill(t *testing.T) {
	t.Parallel()

	st := internal.NewInmemStorage()
	f := newRogueFixture(t, st)

	stopping := &stoppingStorage{Storage: st}
	stopping.left.Store(1) // the first batch commits, the second does not
	if err := localmigration.RemoveRogueChunks(stopping, log.Noop)(); !errors.Is(err, errKilled) {
		t.Fatalf("first run: got %v, want it stopped", err)
	}

	// The stop happened between batches: some entries are removed already
	// and some are not, and the two reserve indexes still agree.
	bins, err := st.IndexStore().Count(&reserve.ChunkBinItem{})
	if err != nil {
		t.Fatal(err)
	}
	radius, err := st.IndexStore().Count(&reserve.BatchRadiusItem{})
	if err != nil {
		t.Fatal(err)
	}
	if total := 5 + rogueCount; bins >= total || bins <= 4 {
		t.Fatalf("after the stop the reserve holds %d entries, want some but not all of the %d rogue ones removed", bins, total-4)
	}
	if bins != radius {
		t.Fatalf("after the stop %d bin entries and %d radius entries, want them equal", bins, radius)
	}

	if err := localmigration.RemoveRogueChunks(st, log.Noop)(); err != nil {
		t.Fatal(err)
	}
	f.check(t, st)
}
