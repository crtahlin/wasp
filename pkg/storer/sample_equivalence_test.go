// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/bmt"
	"github.com/ethersphere/bee/v2/pkg/cac"
	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/postage"
	batchstore "github.com/ethersphere/bee/v2/pkg/postage/batchstore/mock"
	postagetesting "github.com/ethersphere/bee/v2/pkg/postage/testing"
	"github.com/ethersphere/bee/v2/pkg/soc"
	"github.com/ethersphere/bee/v2/pkg/storage"
	chunk "github.com/ethersphere/bee/v2/pkg/storage/testing"
	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

// valueBatchStore answers Iterate with a fixed set of batches, so the
// sampler's minimum batch value has batches to exclude.
type valueBatchStore struct {
	*batchstore.BatchStore
	batches []*postage.Batch
	err     error // returned by Iterate after the batches, when set
}

func (b *valueBatchStore) Iterate(cb func(*postage.Batch) (bool, error)) error {
	for _, batch := range b.batches {
		if stop, err := cb(batch); stop || err != nil {
			return err
		}
	}
	return b.err
}

type equivalenceReserve struct {
	st         *storer.DB
	bs         *valueBatchStore
	baseAddr   swarm.Address
	consensus  uint64
	lowBatch   []byte
	minBalance *big.Int
	nextBinID  uint64
}

// newEquivalenceReserve builds a reserve with content-addressed chunks and
// single owner chunks of two batches above the minimum value and one below
// it, a single owner chunk stored again with new content (as gsoc does),
// chunks stamped after the consensus time, and legacy entries of
// unspecified type.
func newEquivalenceReserve(t *testing.T) *equivalenceReserve {
	t.Helper()

	r := &equivalenceReserve{
		baseAddr:   swarm.RandAddress(t),
		consensus:  uint64(time.Now().UnixNano()),
		minBalance: big.NewInt(150),
		nextBinID:  1_000_000,
	}
	high1, high2, low := postagetesting.MustNewID(), postagetesting.MustNewID(), postagetesting.MustNewID()
	r.lowBatch = low
	r.bs = &valueBatchStore{
		BatchStore: batchstore.New(batchstore.WithAcceptAllExistsFunc()),
		batches: []*postage.Batch{
			{ID: high1, Value: big.NewInt(200)},
			{ID: high2, Value: big.NewInt(300)},
			{ID: low, Value: big.NewInt(100)},
		},
	}

	opts := dbTestOps(r.baseAddr, 10_000, r.bs, nil, time.Second)
	opts.ValidStamp = func(ch swarm.Chunk) (swarm.Chunk, error) { return ch, nil }
	st, err := diskStorer(t, opts)()
	if err != nil {
		t.Fatal(err)
	}
	r.st = st

	stamp := func(batch []byte, ts uint64) *postage.Stamp {
		tsBuf := make([]byte, 8)
		binary.BigEndian.PutUint64(tsBuf, ts)
		return postage.NewStamp(batch, postagetesting.MustNewID()[:8], tsBuf, postagetesting.MustNewSignature())
	}
	putter := st.ReservePutter()
	put := func(ch swarm.Chunk) {
		t.Helper()
		if err := putter.Put(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}
	batches := [][]byte{high1, high2, low}
	for i := range 600 {
		ch := chunk.GenerateTestRandomChunk()
		if i%5 == 0 {
			ch = chunk.GenerateTestRandomSoChunk(t, ch)
		}
		ts := r.consensus - 1
		if i%37 == 0 {
			ts = r.consensus + 1 // stamped after the consensus time
		}
		put(ch.WithStamp(stamp(batches[i%3], ts)))
	}

	// A single owner chunk stored twice with different content under the
	// same address, as a gsoc update is.
	key, err := crypto.GenerateSecp256k1Key()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.NewDefaultSigner(key)
	id := testutil.RandBytes(t, swarm.HashSize)
	for range 2 {
		sch, err := soc.New(id, chunk.GenerateTestRandomChunk()).Sign(signer)
		if err != nil {
			t.Fatal(err)
		}
		put(sch.WithStamp(stamp(high1, r.consensus-1)))
	}

	// Legacy entries of unspecified type, in every bin, which the sampler
	// must drop before reading them.
	for bin := range uint8(8) {
		for range 4 {
			r.nextBinID++
			addr := swarm.RandAddressAt(t, r.baseAddr, int(bin))
			if err := st.PutLegacyRogueBinItem(context.Background(), bin, r.nextBinID, addr, high1); err != nil {
				t.Fatal(err)
			}
		}
	}
	return r
}

// sampleHash is the hash a node commits to: the content-addressed chunk of
// every item's chunk address and transformed address, as the storage
// incentives agent builds it.
func sampleHash(t *testing.T, items []storer.SampleItem) swarm.Address {
	t.Helper()
	content := make([]byte, 0, len(items)*2*swarm.HashSize)
	for _, it := range items {
		content = append(content, it.ChunkAddress.Bytes()...)
		content = append(content, it.TransformedAddress.Bytes()...)
	}
	ch, err := cac.New(content)
	if err != nil {
		t.Fatal(err)
	}
	return ch.Address()
}

func requireSameSample(t *testing.T, name string, want, got storer.Sample) {
	t.Helper()
	if len(got.Items) != len(want.Items) {
		t.Fatalf("%s: %d items, want %d", name, len(got.Items), len(want.Items))
	}
	for i := range want.Items {
		w, g := want.Items[i], got.Items[i]
		switch {
		case !g.TransformedAddress.Equal(w.TransformedAddress):
			t.Fatalf("%s: item %d transformed address %s, want %s", name, i, g.TransformedAddress, w.TransformedAddress)
		case !g.ChunkAddress.Equal(w.ChunkAddress):
			t.Fatalf("%s: item %d chunk address %s, want %s", name, i, g.ChunkAddress, w.ChunkAddress)
		case !bytes.Equal(g.ChunkData, w.ChunkData):
			t.Fatalf("%s: item %d chunk data differs", name, i)
		case !bytes.Equal(g.Stamp.BatchID(), w.Stamp.BatchID()) ||
			!bytes.Equal(g.Stamp.Index(), w.Stamp.Index()) ||
			!bytes.Equal(g.Stamp.Timestamp(), w.Stamp.Timestamp()) ||
			!bytes.Equal(g.Stamp.Sig(), w.Stamp.Sig()):
			t.Fatalf("%s: item %d stamp differs", name, i)
		}
	}
	if h, wh := sampleHash(t, got.Items), sampleHash(t, want.Items); !h.Equal(wh) {
		t.Fatalf("%s: sample hash %s, want %s", name, h, wh)
	}
}

// requireItemsRehash checks that each item's data is the chunk's own: it
// hashes to the chunk address, and to the item's transformed address under
// the anchor.
func requireItemsRehash(t *testing.T, name string, anchor []byte, items []storer.SampleItem) {
	t.Helper()
	// No two items may share a backing array: the readers' buffers are
	// reused, and an item must own a copy of its data.
	seen := make(map[*byte]int, len(items))
	for i, it := range items {
		if len(it.ChunkData) == 0 {
			continue
		}
		if j, ok := seen[&it.ChunkData[0]]; ok {
			t.Fatalf("%s: items %d and %d share one backing array", name, j, i)
		}
		seen[&it.ChunkData[0]] = i
	}
	for i, it := range items {
		ch := swarm.NewChunk(it.ChunkAddress, it.ChunkData)
		typ := storage.ChunkType(ch)
		if typ == swarm.ChunkTypeUnspecified {
			t.Fatalf("%s: item %d data does not hash to its chunk address %s", name, i, it.ChunkAddress)
		}
		taddr, err := storer.TransformedAddress(bmt.NewPrefixHasher(anchor), ch, typ)
		if err != nil {
			t.Fatal(err)
		}
		if !taddr.Equal(it.TransformedAddress) {
			t.Fatalf("%s: item %d data hashes to transformed address %s, want %s", name, i, taddr, it.TransformedAddress)
		}
	}
}

// TestReserveSampleEquivalentToPreMerge runs the sampler as it was before
// the port of Bee v2.8.3's sampler changes and the sampler as it is now on
// the same reserve, for several anchors and depths, and requires identical
// samples: the same items (address, transformed address, data, stamp) in
// the same order, and so the same sample hash.
func TestReserveSampleEquivalentToPreMerge(t *testing.T) {
	t.Parallel()

	r := newEquivalenceReserve(t)
	var notCandidate, belowBalance, rogue, newIgnored int64

	for _, depth := range []uint8{0, 1, 2, 3} {
		for range 3 {
			anchor := swarm.RandAddress(t).Bytes()
			for _, minBalance := range []*big.Int{nil, r.minBalance} {
				want, err := r.st.ReserveSamplePreMerge(context.Background(), anchor, depth, r.consensus, minBalance)
				if err != nil {
					t.Fatal(err)
				}
				got, err := r.st.ReserveSample(context.Background(), anchor, depth, r.consensus, minBalance)
				if err != nil {
					t.Fatal(err)
				}
				name := "depth " + string(rune('0'+depth))
				requireSameSample(t, name, want, got)
				requireItemsRehash(t, name, anchor, got.Items)
				if len(got.Items) != storer.SampleSize {
					t.Fatalf("%s: %d items, want a full sample of %d", name, len(got.Items), storer.SampleSize)
				}
				for _, it := range got.Items {
					if bytes.Equal(it.Stamp.BatchID(), r.lowBatch) && minBalance != nil {
						t.Fatalf("%s: item from a batch below the minimum value", name)
					}
				}
				if got.Stats.TotalIterated != want.Stats.TotalIterated || got.Stats.RogueChunk != want.Stats.RogueChunk ||
					got.Stats.BelowBalanceIgnored != want.Stats.BelowBalanceIgnored {
					t.Fatalf("%s: stats differ: got %+v, want %+v", name, got.Stats, want.Stats)
				}
				notCandidate += got.Stats.NotCandidate
				belowBalance += got.Stats.BelowBalanceIgnored
				rogue += got.Stats.RogueChunk
				newIgnored += got.Stats.NewIgnored
			}
		}
	}

	// Guard the test itself: each kind of chunk must have been met.
	if notCandidate == 0 {
		t.Fatal("test setup: no hashed chunk was held back as a non-candidate")
	}
	if belowBalance == 0 || rogue == 0 || newIgnored == 0 {
		t.Fatalf("test setup: below balance %d, rogue %d, stamped late %d; want each above 0", belowBalance, rogue, newIgnored)
	}
}

// TestReserveSampleSkipsMalformedChunk checks a behaviour this fork keeps
// against Bee v2.8.3: a chunk whose data cannot be hashed for its type is
// skipped and counted, and the sample is the one the reserve gives without
// it. Bee v2.8.3 fails the whole sample, and so the round, on such a chunk;
// the sampler before the port failed it too.
func TestReserveSampleSkipsMalformedChunk(t *testing.T) {
	t.Parallel()

	r := newEquivalenceReserve(t)
	const depth = 1
	anchor := swarm.RandAddress(t).Bytes()

	want, err := r.st.ReserveSample(context.Background(), anchor, depth, r.consensus, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Malformed entries in the anchor's neighbourhood. Data longer than a
	// single owner chunk cannot be one here: the read into the sampler's
	// buffer refuses it first, and it is counted as a failed load.
	malformed := []struct {
		typ  swarm.ChunkType
		data []byte
	}{
		{swarm.ChunkTypeContentAddressed, []byte{1, 2, 3}},                         // shorter than a span
		{swarm.ChunkTypeContentAddressed, make([]byte, swarm.ChunkWithSpanSize+1)}, // longer than a chunk
		{swarm.ChunkTypeSingleOwner, make([]byte, swarm.SocMinChunkSize-1)},        // shorter than a SOC
	}
	for i, m := range malformed {
		r.nextBinID++
		addr := swarm.RandAddressAt(t, swarm.NewAddress(anchor), depth)
		bin := swarm.Proximity(r.baseAddr.Bytes(), addr.Bytes())
		if err := r.st.PutLegacyBinItemWithData(context.Background(), bin, r.nextBinID, addr, r.bs.batches[0].ID, m.typ, m.data); err != nil {
			t.Fatalf("malformed entry %d: %v", i, err)
		}
	}

	got, err := r.st.ReserveSample(context.Background(), anchor, depth, r.consensus, nil)
	if err != nil {
		t.Fatalf("sample with malformed chunks: %v, want them skipped", err)
	}
	if got.Stats.MalformedChunk != int64(len(malformed)) {
		t.Fatalf("malformed chunks counted %d, want %d", got.Stats.MalformedChunk, len(malformed))
	}
	requireSameSample(t, "with malformed chunks", want, got)

	if _, err := r.st.ReserveSamplePreMerge(context.Background(), anchor, depth, r.consensus, nil); err == nil {
		t.Fatal("the sampler before the port accepted the malformed chunks; this test no longer shows a change")
	}
}

// TestReserveSampleContinuesOnBatchStoreError checks the other behaviour
// this fork keeps against Bee v2.8.3: when the batch store fails while the
// sampler lists the batches below the minimum value, the batches read before
// the failure are still excluded and the sample is made, as before the port.
// Bee v2.8.3 ends the sample with the error.
func TestReserveSampleContinuesOnBatchStoreError(t *testing.T) {
	t.Parallel()

	r := newEquivalenceReserve(t)
	r.bs.err = errors.New("batch store failure")
	const depth = 1
	anchor := swarm.RandAddress(t).Bytes()

	want, err := r.st.ReserveSamplePreMerge(context.Background(), anchor, depth, r.consensus, r.minBalance)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.st.ReserveSample(context.Background(), anchor, depth, r.consensus, r.minBalance)
	if err != nil {
		t.Fatalf("sample after a batch store error: %v, want it made", err)
	}
	requireSameSample(t, "batch store error", want, got)
	if got.Stats.BelowBalanceIgnored == 0 {
		t.Fatal("no chunk excluded: the batches read before the failure must still be excluded")
	}
}
