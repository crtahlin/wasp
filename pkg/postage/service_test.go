// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package postage_test

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"errors"
	"io"
	"math/big"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/postage"
	pstoremock "github.com/ethersphere/bee/v2/pkg/postage/batchstore/mock"
	postagetesting "github.com/ethersphere/bee/v2/pkg/postage/testing"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/inmemstore"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

// TestSaveLoad tests the idempotence of saving and loading the postage.Service
// with all the active stamp issuers.
func TestSaveLoad(t *testing.T) {
	t.Parallel()

	store := inmemstore.New()
	defer store.Close()
	pstore := pstoremock.New()
	saved := func(id int64) postage.Service {
		ps, err := postage.NewService(log.Noop, store, pstore, id, false)
		if err != nil {
			t.Fatal(err)
		}
		for range 16 {
			err := ps.Add(newTestStampIssuer(t, 1000))
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := ps.Close(); err != nil {
			t.Fatal(err)
		}
		return ps
	}
	loaded := func(id int64) postage.Service {
		ps, err := postage.NewService(log.Noop, store, pstore, id, false)
		if err != nil {
			t.Fatal(err)
		}
		return ps
	}
	test := func(id int64) {
		psS := saved(id)
		psL := loaded(id)
		defer psL.Close()

		sMap := map[string]struct{}{}
		stampIssuers := psS.StampIssuers()
		for _, s := range stampIssuers {
			sMap[string(s.ID())] = struct{}{}
		}

		stampIssuers = psL.StampIssuers()
		for _, s := range stampIssuers {
			if _, ok := sMap[string(s.ID())]; !ok {
				t.Fatalf("mismatch between saved and loaded")
			}
		}
	}
	test(0)
	test(1)
}

func TestGetStampIssuer(t *testing.T) {
	t.Parallel()

	store := inmemstore.New()
	defer store.Close()
	chainID := int64(0)
	testChainState := postagetesting.NewChainState()
	if testChainState.Block < uint64(postage.BlockThreshold) {
		testChainState.Block += uint64(postage.BlockThreshold + 1)
	}
	validBlockNumber := testChainState.Block - uint64(postage.BlockThreshold+1)
	pstore := pstoremock.New(pstoremock.WithChainState(testChainState))
	ps, err := postage.NewService(log.Noop, store, pstore, chainID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()

	ids := make([][]byte, 8)
	for i := range ids {
		id := make([]byte, 32)
		_, err := io.ReadFull(crand.Reader, id)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
		if i == 0 {
			continue
		}

		var shift uint64 = 0
		if i > 3 {
			shift = uint64(i)
		}
		err = ps.Add(postage.NewStampIssuer(
			string(id),
			"",
			id,
			big.NewInt(3),
			16,
			8,
			validBlockNumber+shift, true),
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Run("found", func(t *testing.T) {
		for _, id := range ids[1:4] {
			st, save, err := ps.GetStampIssuer(id)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			_ = save()
			if st.Label() != string(id) {
				t.Fatalf("wrong issuer returned")
			}
		}

		// check if the save() call persisted the stamp issuers
		for _, id := range ids[1:4] {
			stampIssuerItem := postage.NewStampIssuerItem(id)
			err := store.Get(stampIssuerItem)
			if err != nil {
				t.Fatal(err)
			}
			if string(id) != stampIssuerItem.ID() {
				t.Fatalf("got id %s, want id %s", stampIssuerItem.ID(), string(id))
			}
		}
	})
	t.Run("not found", func(t *testing.T) {
		_, _, err := ps.GetStampIssuer(ids[0])
		if !errors.Is(err, postage.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})
	t.Run("not usable", func(t *testing.T) {
		for _, id := range ids[4:] {
			_, _, err := ps.GetStampIssuer(id)
			if !errors.Is(err, postage.ErrNotUsable) {
				t.Fatalf("expected ErrNotUsable, got %v", err)
			}
		}
	})
	t.Run("recovered", func(t *testing.T) {
		b := postagetesting.MustNewBatch()
		b.Start = validBlockNumber
		testAmount := big.NewInt(1)
		err := ps.HandleCreate(b, testAmount)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		st, sv, err := ps.GetStampIssuer(b.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if st.Label() != "recovered" {
			t.Fatal("wrong issuer returned")
		}
		err = sv()
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("topup", func(t *testing.T) {
		ps.HandleTopUp(ids[1], big.NewInt(10))
		if err != nil {
			t.Fatal(err)
		}
		stampIssuer, save, err := ps.GetStampIssuer(ids[1])
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		_ = save()
		if stampIssuer.Amount().Cmp(big.NewInt(13)) != 0 {
			t.Fatalf("expected amount %d got %d", 13, stampIssuer.Amount().Int64())
		}
	})
	t.Run("dilute", func(t *testing.T) {
		ps.HandleDepthIncrease(ids[2], 17)
		if err != nil {
			t.Fatal(err)
		}
		stampIssuer, save, err := ps.GetStampIssuer(ids[2])
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		_ = save()
		if stampIssuer.Amount().Cmp(big.NewInt(3)) != 0 {
			t.Fatalf("expected amount %d got %d", 3, stampIssuer.Amount().Int64())
		}
		if stampIssuer.Depth() != 17 {
			t.Fatalf("expected depth %d got %d", 17, stampIssuer.Depth())
		}
	})
}

func TestSetExpired(t *testing.T) {
	t.Parallel()

	store := inmemstore.New()
	testutil.CleanupCloser(t, store)

	batch := swarm.RandAddress(t).Bytes()
	notExistsBatch := swarm.RandAddress(t).Bytes()

	pstore := pstoremock.New(pstoremock.WithExistsFunc(func(b []byte) (bool, error) {
		return bytes.Equal(b, batch), nil
	}))

	ps, err := postage.NewService(log.Noop, store, pstore, 1, false)
	if err != nil {
		t.Fatal(err)
	}

	itemExists := postage.NewStampItem().WithChunkAddress(swarm.RandAddress(t)).WithBatchID(batch)
	err = store.Put(itemExists)
	if err != nil {
		t.Fatal(err)
	}

	itemNotExists := postage.NewStampItem().WithChunkAddress(swarm.RandAddress(t)).WithBatchID(notExistsBatch)
	err = store.Put(itemNotExists)
	if err != nil {
		t.Fatal(err)
	}

	err = ps.Add(newTestStampIssuerID(t, 1000, itemExists.BatchID))
	if err != nil {
		t.Fatal(err)
	}
	err = ps.Add(newTestStampIssuerID(t, 1000, itemNotExists.BatchID))
	if err != nil {
		t.Fatal(err)
	}
	err = ps.HandleStampExpiry(context.Background(), itemNotExists.BatchID)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = ps.GetStampIssuer(itemNotExists.BatchID)
	if !errors.Is(err, postage.ErrNotFound) {
		t.Fatalf("expected %v, got %v", postage.ErrNotFound, err)
	}

	err = store.Iterate(
		storage.Query{
			Factory: func() storage.Item {
				return new(postage.StampItem)
			},
		}, func(result storage.Result) (bool, error) {
			item := result.Entry.(*postage.StampItem)
			exists, err := pstore.Exists(item.BatchID)
			if err != nil {
				return false, err
			}

			if bytes.Equal(item.BatchID, notExistsBatch) && exists {
				return false, errors.New("found stamp item belonging to a non-existent batch")
			}

			if bytes.Equal(item.BatchID, batch) && !exists {
				return false, errors.New("found stamp item belonging to a batch that should exist")
			}

			return false, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	err = store.Get(itemExists)
	if err != nil {
		t.Fatal(err)
	}

	err = store.Get(itemNotExists)
	if err == nil {
		t.Fatal("expected error getting expired stamp item, got nil")
	}

	testutil.CleanupCloser(t, ps)
}

// TestCrashRecovery verifies that bucket counts are restored from stamp items
// when the service starts after an unclean shutdown (wasDirty=true).
func TestCrashRecovery(t *testing.T) {
	t.Parallel()

	store := inmemstore.New()
	defer store.Close()
	pstore := pstoremock.New()

	issuer := newTestStampIssuer(t, 1000)
	batchID := issuer.ID()

	// Pick two random chunk addresses in different collision buckets. The
	// assertions below read the two buckets as though they were distinct, so
	// the second address is chosen rather than hoped for: two independent
	// random addresses share a bucket about 1 run in 256 at this bucket depth.
	chunkAddr0, bIdx0, chunkAddr1, bIdx1 := twoAddressesInDifferentBuckets(t, issuer)

	// Write StampItems directly, simulating stamps issued before a crash
	// without the issuer bucket state being saved.
	// bIdx0: issued at collision count 2 → bucket should recover to 3
	// bIdx1: issued at collision count 0 → bucket should recover to 1
	items := []*postage.StampItem{
		postage.NewStampItem().WithBatchID(batchID).WithChunkAddress(chunkAddr0).WithBatchIndex(postage.IndexToBytes(bIdx0, 2)),
		postage.NewStampItem().WithBatchID(batchID).WithChunkAddress(chunkAddr1).WithBatchIndex(postage.IndexToBytes(bIdx1, 0)),
	}
	for _, item := range items {
		if err := store.Put(item); err != nil {
			t.Fatal(err)
		}
	}

	// Save the issuer with zero bucket counts to the store.
	ps, err := postage.NewService(log.Noop, store, pstore, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.Add(issuer); err != nil {
		t.Fatal(err)
	}
	if err := ps.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify that the issuer on disk still has zero bucket counts (i.e., recovery
	// has not happened yet). Open with wasDirty=false to skip recovery.
	psCheck, err := postage.NewService(log.Noop, store, pstore, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	checkIssuers := psCheck.StampIssuers()
	if len(checkIssuers) != 1 {
		t.Fatalf("pre-recovery check: expected 1 issuer, got %d", len(checkIssuers))
	}
	checkBuckets := checkIssuers[0].Buckets()
	if checkBuckets[bIdx0] != 0 {
		t.Errorf("pre-recovery check: bucket %d: want 0, got %d", bIdx0, checkBuckets[bIdx0])
	}
	if checkBuckets[bIdx1] != 0 {
		t.Errorf("pre-recovery check: bucket %d: want 0, got %d", bIdx1, checkBuckets[bIdx1])
	}
	if err := psCheck.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart with wasDirty=true — should trigger bucket recovery.
	ps2, err := postage.NewService(log.Noop, store, pstore, 1, true)
	if err != nil {
		t.Fatal(err)
	}

	issuers := ps2.StampIssuers()
	if len(issuers) != 1 {
		t.Fatalf("expected 1 issuer, got %d", len(issuers))
	}

	buckets := issuers[0].Buckets()
	if buckets[bIdx0] != 3 {
		t.Errorf("bucket %d: want 3, got %d", bIdx0, buckets[bIdx0])
	}
	if buckets[bIdx1] != 1 {
		t.Errorf("bucket %d: want 1, got %d", bIdx1, buckets[bIdx1])
	}

	// Clean shutdown — recovered counts must be flushed to disk.
	if err := ps2.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart with wasDirty=false — recovery is skipped, but counts must still
	// be correct because ps2.Close() persisted the recovered state.
	ps3, err := postage.NewService(log.Noop, store, pstore, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ps3.Close()

	issuers3 := ps3.StampIssuers()
	if len(issuers3) != 1 {
		t.Fatalf("post-recovery clean restart: expected 1 issuer, got %d", len(issuers3))
	}

	buckets3 := issuers3[0].Buckets()
	if buckets3[bIdx0] != 3 {
		t.Errorf("post-recovery clean restart: bucket %d: want 3, got %d", bIdx0, buckets3[bIdx0])
	}
	if buckets3[bIdx1] != 1 {
		t.Errorf("post-recovery clean restart: bucket %d: want 1, got %d", bIdx1, buckets3[bIdx1])
	}
}

func TestUpdateIssuerLabel(t *testing.T) {
	t.Parallel()

	store := inmemstore.New()
	defer store.Close()
	pstore := pstoremock.New()

	ps, err := postage.NewService(log.Noop, store, pstore, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()

	issuer := newTestStampIssuer(t, 1000)
	batchID := issuer.ID()

	if err := ps.Add(issuer); err != nil {
		t.Fatal(err)
	}

	t.Run("not found", func(t *testing.T) {
		unknown := make([]byte, 32)
		if err := ps.UpdateIssuerLabel(unknown, "x"); !errors.Is(err, postage.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("updates in-memory and persists", func(t *testing.T) {
		const newLabel = "updated-label"

		if err := ps.UpdateIssuerLabel(batchID, newLabel); err != nil {
			t.Fatal(err)
		}

		// in-memory label must reflect the update immediately
		issuers := ps.StampIssuers()
		var found bool
		for _, si := range issuers {
			if bytes.Equal(si.ID(), batchID) {
				found = true
				if si.Label() != newLabel {
					t.Fatalf("in-memory label: got %q, want %q", si.Label(), newLabel)
				}
			}
		}
		if !found {
			t.Fatal("issuer not found after update")
		}

		// persisted label must also reflect the update
		item := postage.NewStampIssuerItem(batchID)
		if err := store.Get(item); err != nil {
			t.Fatal(err)
		}
		if item.Issuer.Label() != newLabel {
			t.Fatalf("persisted label: got %q, want %q", item.Issuer.Label(), newLabel)
		}
	})
}

// maxBucketTries bounds twoAddressesInDifferentBuckets. At a bucket depth of 8
// a single draw already avoids a given bucket 255 times in 256, so the bound
// sits far past any plausible run of collisions: all 64 colliding has
// probability 256^-64. Reaching it means address generation is broken, and
// reporting that is more useful than looping until the test times out.
const maxBucketTries = 64

// twoAddressesInDifferentBuckets returns two random chunk addresses whose
// collision buckets differ, each with its bucket index.
//
// It takes the issuer rather than a bucket depth, and returns the indices
// rather than leaving the caller to work them out, so that there is no depth
// for a caller to pass wrongly. StampIssuer.Depth and StampIssuer.BucketDepth
// are adjacent methods returning uint8, and reaching for the first compiles
// and silently restores the 1 in 256 failure this helper exists to remove.
//
// Both addresses must come from one call. Taking one address from each of two
// calls restores that failure just as quietly.
func twoAddressesInDifferentBuckets(t *testing.T, issuer *postage.StampIssuer) (swarm.Address, uint32, swarm.Address, uint32) {
	t.Helper()

	depth := issuer.BucketDepth()
	first := swarm.RandAddress(t)
	firstBucket := postage.ToBucket(depth, first)

	for range maxBucketTries {
		second := swarm.RandAddress(t)
		if secondBucket := postage.ToBucket(depth, second); secondBucket != firstBucket {
			return first, firstBucket, second, secondBucket
		}
	}

	t.Fatalf("no second address outside bucket %d in %d tries", firstBucket, maxBucketTries)

	return swarm.ZeroAddress, 0, swarm.ZeroAddress, 0
}

// TestTwoAddressesInDifferentBuckets pins what the helper promises: the two
// addresses it returns never share a collision bucket.
func TestTwoAddressesInDifferentBuckets(t *testing.T) {
	t.Parallel()

	issuer := newTestStampIssuer(t, 1000)

	// A helper that drew the second address without checking would return a
	// colliding pair at least once over this many draws with probability
	// 1 - (255/256)^4096, leaving about 1 chance in 9,200,000 of passing even
	// so. It also catches a helper that drew the second address once, outside
	// the loop, which runs out of tries instead.
	//
	// It does not catch a helper that returned one cached pair on every call:
	// a pair that does not collide never collides, however many times it is
	// handed back. Nothing here covers that, and saying so is cheaper than
	// implying a guarantee this loop cannot give.
	const draws = 4096

	for i := range draws {
		_, firstBucket, _, secondBucket := twoAddressesInDifferentBuckets(t, issuer)
		if firstBucket == secondBucket {
			t.Fatalf("draw %d: both addresses landed in bucket %d", i, firstBucket)
		}
	}
}
