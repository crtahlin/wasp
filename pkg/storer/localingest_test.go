// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	chunktesting "github.com/ethersphere/bee/v2/pkg/storage/testing"
	storer "github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// These run against a real database rather than the mock, because the mock has
// no dirty-collection concept at all: its NewCollection never refuses a
// duplicate and its Cleanup returns nil unconditionally, so every path that
// matters here would pass without being exercised.

func localIngestOpts(t *testing.T, limit uint64) *storer.Options {
	t.Helper()

	opts := dbTestOps(swarm.RandAddress(t), 0, nil, nil, time.Second)
	opts.LocalIngestLimit = limit
	return opts
}

// ingest stores chunks through a local ingest session and commits them under
// root, claiming room for each as the counting putter in pkg/api does.
func ingest(t *testing.T, lstore *storer.DB, root swarm.Address, chunks []swarm.Chunk) error {
	t.Helper()

	session, err := lstore.NewLocalIngestCollection(context.Background())
	if err != nil {
		t.Fatalf("NewLocalIngestCollection(...): unexpected error: %v", err)
	}

	for _, ch := range chunks {
		if err := session.Reserve(1); err != nil {
			return errors.Join(err, session.Cleanup())
		}
		if err := session.Put(context.Background(), ch); err != nil {
			return errors.Join(err, session.Cleanup())
		}
	}

	if err := session.Done(root, uint64(len(chunks))); err != nil {
		return errors.Join(err, session.Cleanup())
	}
	return nil
}

func assertUsage(t *testing.T, lstore *storer.DB, wantCommitted, wantReserved uint64) {
	t.Helper()

	committed, reserved, _ := lstore.LocalIngestUsage()
	if committed != wantCommitted {
		t.Fatalf("committed usage %d, want %d", committed, wantCommitted)
	}
	if reserved != wantReserved {
		t.Fatalf("reserved %d, want %d; a claim that is never released disables the feature in silence", reserved, wantReserved)
	}
}

func TestLocalIngestStoresAndUnpins(t *testing.T) {
	t.Parallel()

	lstore, err := newStorer(t, "", localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}

	chunks := chunktesting.GenerateTestRandomChunks(10)
	root := chunks[0].Address()

	if err := ingest(t, lstore, root, chunks); err != nil {
		t.Fatalf("ingest: unexpected error: %v", err)
	}

	assertUsage(t, lstore, 10, 0)

	has, err := lstore.HasPin(root)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("an ingested reference is not reported as pinned, so the existing unpin route could not remove it")
	}

	pins, err := lstore.Pins()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pins {
		if p.Equal(root) {
			found = true
		}
	}
	if !found {
		t.Fatal("an ingested reference is missing from Pins()")
	}

	// Unpinning must take the record away with the collection, or the usage
	// figure drifts upward for good and the limit shrinks every time.
	if err := lstore.DeletePin(context.Background(), root); err != nil {
		t.Fatalf("DeletePin(...): unexpected error: %v", err)
	}

	assertUsage(t, lstore, 0, 0)
}

// TestLocalIngestUnpinOfOrdinaryPin checks the common case: a collection with
// no local ingest record unpins without error and changes no total.
func TestLocalIngestUnpinOfOrdinaryPin(t *testing.T) {
	t.Parallel()

	lstore, err := newStorer(t, "", localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}

	ingested := chunktesting.GenerateTestRandomChunks(4)
	if err := ingest(t, lstore, ingested[0].Address(), ingested); err != nil {
		t.Fatalf("ingest: unexpected error: %v", err)
	}

	// An ordinary pinning collection, the kind POST /pins/{reference} makes.
	plain := chunktesting.GenerateTestRandomChunks(3)
	session, err := lstore.NewCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range plain {
		if err := session.Put(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Done(plain[0].Address()); err != nil {
		t.Fatal(err)
	}

	assertUsage(t, lstore, 4, 0)

	if err := lstore.DeletePin(context.Background(), plain[0].Address()); err != nil {
		t.Fatalf("DeletePin(...) on an ordinary pin: unexpected error: %v", err)
	}

	assertUsage(t, lstore, 4, 0)
}

func TestLocalIngestDuplicate(t *testing.T) {
	t.Parallel()

	lstore, err := newStorer(t, "", localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}

	chunks := chunktesting.GenerateTestRandomChunks(6)
	root := chunks[0].Address()

	if err := ingest(t, lstore, root, chunks); err != nil {
		t.Fatalf("first ingest: unexpected error: %v", err)
	}
	assertUsage(t, lstore, 6, 0)

	// The reference cannot be checked before the body is split, so the
	// duplicate surfaces from Done and the handler answers 200 on it.
	session, err := lstore.NewLocalIngestCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range chunks {
		if err := session.Reserve(1); err != nil {
			t.Fatal(err)
		}
		if err := session.Put(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}

	err = session.Done(root, uint64(len(chunks)))
	if !errors.Is(err, storer.ErrLocalIngestDuplicate) {
		t.Fatalf("Done on an already-held reference returned %v, want ErrLocalIngestDuplicate; the handler would answer 500 where it should answer 200", err)
	}

	if err := session.Cleanup(); err != nil {
		t.Fatalf("Cleanup after duplicate: unexpected error: %v", err)
	}

	// The second attempt must leave the total exactly where the first put it.
	assertUsage(t, lstore, 6, 0)

	has, err := lstore.HasPin(root)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("cleaning up after a duplicate removed the collection the first ingest committed")
	}
}

// TestLocalIngestCleanupReleasesClaim is the abandoned-request case: a client
// that disconnects mid-body. Without it the claim is stranded for the life of
// the process, and enough of those stop the feature admitting anybody.
func TestLocalIngestCleanupReleasesClaim(t *testing.T) {
	t.Parallel()

	lstore, err := newStorer(t, "", localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}

	chunks := chunktesting.GenerateTestRandomChunks(8)

	session, err := lstore.NewLocalIngestCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range chunks {
		if err := session.Reserve(1); err != nil {
			t.Fatal(err)
		}
		if err := session.Put(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}

	committed, reserved, _ := lstore.LocalIngestUsage()
	if committed != 0 || reserved != 8 {
		t.Fatalf("mid-ingest usage is committed=%d reserved=%d, want 0 and 8; a claim that lands in the committed total inflates it permanently on every abandoned request", committed, reserved)
	}

	if err := session.Cleanup(); err != nil {
		t.Fatalf("Cleanup(): unexpected error: %v", err)
	}

	assertUsage(t, lstore, 0, 0)

	has, err := lstore.HasPin(chunks[0].Address())
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("an abandoned ingest left a pinned collection behind")
	}
}

// TestLocalIngestCleanupIsIdempotent covers the ordering the handler actually
// produces: cleanupOnErrWriter fires Cleanup from WriteHeader, and the handler
// may call it again on the duplicate path.
//
// A second session holds a claim throughout, because with only one session a
// double release is invisible: release clamps at the outstanding total, so
// releasing five twice when only five are held subtracts five and then nothing.
func TestLocalIngestCleanupIsIdempotent(t *testing.T) {
	t.Parallel()

	lstore, err := newStorer(t, "", localIngestOpts(t, 20))
	if err != nil {
		t.Fatal(err)
	}

	bystander, err := lstore.NewLocalIngestCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bystander.Cleanup() })
	if err := bystander.Reserve(5); err != nil {
		t.Fatal(err)
	}

	session, err := lstore.NewLocalIngestCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range chunktesting.GenerateTestRandomChunks(5) {
		if err := session.Reserve(1); err != nil {
			t.Fatal(err)
		}
		if err := session.Put(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}

	if err := session.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := session.Cleanup(); err != nil {
		t.Fatalf("second Cleanup(): unexpected error: %v", err)
	}

	assertUsage(t, lstore, 0, 5)
}

// TestLocalIngestSessionRefusesUseAfterClose. Once a session is finished, a
// claim taken on it would be added to the node-wide total with nothing left
// that could ever give it back, and the limit would shrink for the life of the
// process. A second Done would write the record again and take Close down a
// path it has already been through.
func TestLocalIngestSessionRefusesUseAfterClose(t *testing.T) {
	t.Parallel()

	lstore, err := newStorer(t, "", localIngestOpts(t, 20))
	if err != nil {
		t.Fatal(err)
	}

	// A bystander holds a claim so the assertion below is about this
	// session's behaviour and not about an empty total.
	bystander, err := lstore.NewLocalIngestCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bystander.Cleanup() })
	if err := bystander.Reserve(5); err != nil {
		t.Fatal(err)
	}

	session, err := lstore.NewLocalIngestCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Reserve(5); err != nil {
		t.Fatal(err)
	}
	if err := session.Cleanup(); err != nil {
		t.Fatal(err)
	}

	if err := session.Reserve(3); !errors.Is(err, storer.ErrLocalIngestSessionClosed) {
		t.Fatalf("Reserve on a finished session returned %v, want ErrLocalIngestSessionClosed", err)
	}

	// The refusal has to be the whole story: a claim taken and then refused
	// would be just as stranded as one taken and accepted.
	assertUsage(t, lstore, 0, 5)

	if err := session.Done(swarm.RandAddress(t), 5); !errors.Is(err, storer.ErrLocalIngestSessionClosed) {
		t.Fatalf("Done on a finished session returned %v, want ErrLocalIngestSessionClosed", err)
	}
}

// TestLocalIngestFailedDoneWritesNoRecord is the transaction test the merged
// spec asks for: a failure in either half must leave neither.
//
// It is asserted from outside the transaction, because there is no hook to
// fail the record write from a test. The second session commits the SAME root
// with a deliberately different chunk count, so a record written outside the
// rollback shows up as that wrong count after a restart rather than hiding
// behind the first session's identical key.
func TestLocalIngestFailedDoneWritesNoRecord(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	lstore, err := storer.New(context.Background(), dir, localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}

	chunks := chunktesting.GenerateTestRandomChunks(6)
	root := chunks[0].Address()
	if err := ingest(t, lstore, root, chunks); err != nil {
		t.Fatalf("first ingest: unexpected error: %v", err)
	}

	second, err := lstore.NewLocalIngestCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range chunks {
		if err := second.Reserve(1); err != nil {
			t.Fatal(err)
		}
		if err := second.Put(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}
	if err := second.Done(root, 999); !errors.Is(err, storer.ErrLocalIngestDuplicate) {
		t.Fatalf("Done returned %v, want ErrLocalIngestDuplicate", err)
	}
	if err := second.Cleanup(); err != nil {
		t.Fatal(err)
	}

	if err := lstore.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := storer.New(context.Background(), dir, localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("failed closing storer: %v", err)
		}
	})

	// A total of 999 here would mean a record survived a rolled back
	// transaction.
	assertUsage(t, reopened, 6, 0)
}

func TestLocalIngestLimit(t *testing.T) {
	t.Parallel()

	lstore, err := newStorer(t, "", localIngestOpts(t, 5))
	if err != nil {
		t.Fatal(err)
	}

	session, err := lstore.NewLocalIngestCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Cleanup() })

	for i := range 5 {
		if err := session.Reserve(1); err != nil {
			t.Fatalf("claim %d of 5 refused: %v", i+1, err)
		}
	}

	if err := session.Reserve(1); !errors.Is(err, storer.ErrLocalIngestLimit) {
		t.Fatalf("claim past the limit returned %v, want ErrLocalIngestLimit", err)
	}
}

// TestLocalIngestLimitSpansConcurrentSessions is why this is a claim and not a
// check. Two ingests interleave, and a check against the committed total would
// let both pass and together exceed the limit.
func TestLocalIngestLimitSpansConcurrentSessions(t *testing.T) {
	t.Parallel()

	lstore, err := newStorer(t, "", localIngestOpts(t, 10))
	if err != nil {
		t.Fatal(err)
	}

	first, err := lstore.NewLocalIngestCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Cleanup() })

	second, err := lstore.NewLocalIngestCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Cleanup() })

	if err := first.Reserve(6); err != nil {
		t.Fatal(err)
	}
	if err := second.Reserve(5); !errors.Is(err, storer.ErrLocalIngestLimit) {
		t.Fatalf("a second session claimed 5 more against a limit of 10 with 6 already claimed, returning %v; nothing in the committed total would have stopped it", err)
	}

	// Room the first session gives back is available to the second.
	if err := first.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := second.Reserve(5); err != nil {
		t.Fatalf("after the first session released its claim, a claim of 5 against a limit of 10 was refused: %v", err)
	}
}

// TestLocalIngestTotalSurvivesRestart checks the startup rebuild. The total is
// held in memory, so without it every restart would reset usage to zero and
// the limit would never bind.
func TestLocalIngestTotalSurvivesRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := localIngestOpts(t, 0)

	lstore, err := storer.New(context.Background(), dir, opts)
	if err != nil {
		t.Fatal(err)
	}

	chunks := chunktesting.GenerateTestRandomChunks(12)
	root := chunks[0].Address()
	if err := ingest(t, lstore, root, chunks); err != nil {
		t.Fatalf("ingest: unexpected error: %v", err)
	}
	assertUsage(t, lstore, 12, 0)

	if err := lstore.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := storer.New(context.Background(), dir, localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("failed closing storer: %v", err)
		}
	})

	assertUsage(t, reopened, 12, 0)
}

// TestLocalIngestEncryptedReferenceRoundTrips is the 64-byte reference case.
// A 32-byte assumption in the record would truncate every encrypted ingest
// silently, and the count would come back attached to the wrong address.
func TestLocalIngestEncryptedReferenceRoundTrips(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	lstore, err := storer.New(context.Background(), dir, localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}

	chunks := chunktesting.GenerateTestRandomChunks(3)
	// An encrypted reference is the 32-byte address followed by a 32-byte
	// decryption key, which is what Swarm-Encrypt produces.
	root := swarm.NewAddress(append(chunks[0].Address().Bytes(), swarm.RandAddress(t).Bytes()...))
	if len(root.Bytes()) != 64 {
		t.Fatalf("test set up a %d-byte reference, want 64", len(root.Bytes()))
	}

	if err := ingest(t, lstore, root, chunks); err != nil {
		t.Fatalf("ingest: unexpected error: %v", err)
	}
	if err := lstore.Close(); err != nil {
		t.Fatal(err)
	}

	// Reading it back from disk is what proves the full reference survived:
	// a truncated key would leave the record under a different id, the
	// rebuild would not match it to its pin, and the total would come back 0.
	reopened, err := storer.New(context.Background(), dir, localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("failed closing storer: %v", err)
		}
	})

	assertUsage(t, reopened, 3, 0)

	if err := reopened.DeletePin(context.Background(), root); err != nil {
		t.Fatalf("DeletePin on an encrypted reference: unexpected error: %v", err)
	}
	assertUsage(t, reopened, 0, 0)
}

// TestLocalIngestSendsNothingToThePusher. A local ingest must not reach the
// network: the whole point is content that is stored without postage, and the
// pusher would need a stamp it does not have.
func TestLocalIngestSendsNothingToThePusher(t *testing.T) {
	t.Parallel()

	lstore, err := newStorer(t, "", localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}

	chunks := chunktesting.GenerateTestRandomChunks(5)
	if err := ingest(t, lstore, chunks[0].Address(), chunks); err != nil {
		t.Fatalf("ingest: unexpected error: %v", err)
	}

	select {
	case op := <-lstore.PusherFeed():
		t.Fatalf("a locally ingested chunk reached the pusher: %v", op.Chunk.Address())
	case <-time.After(250 * time.Millisecond):
	}
}

// TestLocalIngestGaugeTracksEveryPath. The gauge is what an operator sees and
// what the warning is measured against, so it has to follow the total on every
// path that moves it. Publishing it only where an ingest succeeds leaves a node
// reporting zero after a restart however much it is holding, and reporting a
// figure that only ever rises however much is unpinned.
func TestLocalIngestGaugeTracksEveryPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	lstore, err := storer.New(context.Background(), dir, localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}

	if got := lstore.LocalIngestPublished(); got != 0 {
		t.Fatalf("a fresh node reports %d ingested chunks, want 0", got)
	}

	chunks := chunktesting.GenerateTestRandomChunks(9)
	root := chunks[0].Address()
	if err := ingest(t, lstore, root, chunks); err != nil {
		t.Fatalf("ingest: unexpected error: %v", err)
	}
	if got := lstore.LocalIngestPublished(); got != 9 {
		t.Fatalf("after an ingest the gauge reports %d, want 9", got)
	}

	if err := lstore.Close(); err != nil {
		t.Fatal(err)
	}

	// After a restart the gauge must report what the node actually holds,
	// not zero until somebody happens to ingest again.
	reopened, err := storer.New(context.Background(), dir, localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("failed closing storer: %v", err)
		}
	})

	if got := reopened.LocalIngestPublished(); got != 9 {
		t.Fatalf("after a restart the gauge reports %d, want 9; the node would report holding nothing however much it holds", got)
	}

	if err := reopened.DeletePin(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if got := reopened.LocalIngestPublished(); got != 0 {
		t.Fatalf("after unpinning everything the gauge reports %d, want 0; the figure would only ever rise", got)
	}
}

// TestLocalIngestShadowedByOrdinaryPin is the case an operator hits and neither
// layer covered: a pin that already exists at that root, whatever created it,
// makes an ingest of the same content answer as a duplicate.
//
// The realistic way in is a stamped upload that was pinned. The site is then on
// disk twice over as far as the operator is concerned, but only the ordinary pin
// holds it, and the ingest contributes nothing to the local ingest total, so
// unpinning the ordinary pin removes the content and the ingest figure never
// showed it. Both existing duplicate tests ingest the same content twice, which
// does not reach this path, and the one ordinary-pin test uses a different root.
func TestLocalIngestShadowedByOrdinaryPin(t *testing.T) {
	t.Parallel()

	lstore, err := newStorer(t, "", localIngestOpts(t, 0))
	if err != nil {
		t.Fatal(err)
	}

	chunks := chunktesting.GenerateTestRandomChunks(4)
	root := chunks[0].Address()

	// An ordinary pinning collection at root, the kind POST /pins/{reference}
	// leaves behind after a stamped upload.
	session, err := lstore.NewCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range chunks {
		if err := session.Put(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.Done(root); err != nil {
		t.Fatal(err)
	}

	// Nothing has been ingested, so the local ingest total is zero.
	assertUsage(t, lstore, 0, 0)

	// Ingesting the same content now collides with that pin.
	err = ingest(t, lstore, root, chunks)
	if !errors.Is(err, storer.ErrLocalIngestDuplicate) {
		t.Fatalf("ingest over an ordinary pin: got %v, want ErrLocalIngestDuplicate", err)
	}

	// And it contributed nothing, neither committed nor stranded as a claim.
	// A claim left behind here would be the worst version of this: the operator
	// would hold the content under a pin they can remove, while the ingest total
	// counted capacity against them for content it does not hold.
	assertUsage(t, lstore, 0, 0)
}
