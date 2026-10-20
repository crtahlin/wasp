// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package listener_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/postage"
	"github.com/ethersphere/bee/v2/pkg/postage/listener"
)

var errRangeTooLarge = errors.New("query returned more than 10000 results")

// rangeLimitFilterer is far ahead of the listener and refuses log queries
// over more than limit blocks, like a provider that limits eth_getLogs. It
// records the size of every query and whether it was refused.
type rangeLimitFilterer struct {
	limit     uint64      // guarded by mu
	blockDown atomic.Bool // BlockNumber fails, so FilterLogs is never asked

	mu      sync.Mutex
	sizes   []uint64
	refused []bool
}

func (f *rangeLimitFilterer) BlockNumber(context.Context) (uint64, error) {
	if f.blockDown.Load() {
		return 0, errEndpointDown
	}
	return 1_000_000_000, nil
}

func (f *rangeLimitFilterer) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := q.ToBlock.Uint64() - q.FromBlock.Uint64() + 1
	f.mu.Lock()
	refuse := size > f.limit
	f.sizes = append(f.sizes, size)
	f.refused = append(f.refused, refuse)
	f.mu.Unlock()
	if refuse {
		return nil, errRangeTooLarge
	}
	return nil, nil
}

func (f *rangeLimitFilterer) log() ([]uint64, []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint64(nil), f.sizes...), append([]bool(nil), f.refused...)
}

func startPaging(t *testing.T, f listener.BlockHeightContractFilterer) {
	t.Helper()
	startStale(t, f, log.Noop)
}

// TestPageHalvesAndGrowsBack checks that a refused log query halves the next
// page, and that pages applied in a row at the smaller size grow it back, so
// against a provider that limits the range the page settles in a cycle
// around the limit (#583).
func TestPageHalvesAndGrowsBack(t *testing.T) {
	defer listener.SetStaleMaxBackoff(20 * time.Millisecond)()

	f := &rangeLimitFilterer{limit: 1000}
	startPaging(t, f)

	waitFor(t, "enough queries", func() bool { s, _ := f.log(); return len(s) >= 30 })
	sizes, refused := f.log()

	if sizes[0] != listener.BlockPage {
		t.Fatalf("first query covered %d blocks, want the standard page %d", sizes[0], listener.BlockPage)
	}
	// 5000, 2500, 1250 refused, then 625 applies.
	for i, want := range []uint64{5000, 2500, 1250, 625} {
		if sizes[i] != want {
			t.Fatalf("query %d covered %d blocks, want %d; sizes %v", i, sizes[i], want, sizes[:8])
		}
	}
	grew := false
	for i := 4; i < len(sizes); i++ {
		if sizes[i] < 625 {
			t.Fatalf("query %d covered %d blocks, below the cycle around the limit; sizes %v", i, sizes[i], sizes)
		}
		if sizes[i] == 1250 {
			grew = true
			// It grows only after three applied pages at 625.
			for j := i - 3; j < i; j++ {
				if sizes[j] != 625 || refused[j] {
					t.Fatalf("page grew at query %d without three applied pages before it; sizes %v", i, sizes)
				}
			}
		}
	}
	if !grew {
		t.Fatalf("page never grew back after applied pages; sizes %v", sizes)
	}
}

// TestPageNotBelowMinimum checks that the page does not shrink below
// MinBlockPage, even when every query is refused.
func TestPageNotBelowMinimum(t *testing.T) {
	defer listener.SetStaleMaxBackoff(20 * time.Millisecond)()

	f := &rangeLimitFilterer{limit: 10}
	startPaging(t, f)

	waitFor(t, "the page to reach the minimum", func() bool {
		s, _ := f.log()
		return len(s) >= 12
	})
	sizes, _ := f.log()
	for i, s := range sizes {
		if s < listener.MinBlockPage {
			t.Fatalf("query %d covered %d blocks, below the minimum %d", i, s, listener.MinBlockPage)
		}
	}
	if last := sizes[len(sizes)-1]; last != listener.MinBlockPage {
		t.Fatalf("page settled at %d, want the minimum %d", last, listener.MinBlockPage)
	}
}

// TestBlockNumberFailureKeepsPage checks that a failed block number query
// does not change the page: only a refused log query does.
func TestBlockNumberFailureKeepsPage(t *testing.T) {
	defer listener.SetStaleMaxBackoff(20 * time.Millisecond)()

	f := &rangeLimitFilterer{limit: 1_000_000}
	f.blockDown.Store(true)
	startPaging(t, f)

	time.Sleep(100 * time.Millisecond)
	f.blockDown.Store(false)
	waitFor(t, "log queries", func() bool { s, _ := f.log(); return len(s) >= 3 })
	sizes, _ := f.log()
	for i, s := range sizes[:3] {
		if s != listener.BlockPage {
			t.Fatalf("query %d covered %d blocks after block number failures, want the standard page %d", i, s, listener.BlockPage)
		}
	}
}

// timedFilterer fails every block number query and records when each was
// made.
type timedFilterer struct {
	health atomic.Pointer[postage.SyncHealth]

	mu    sync.Mutex
	calls []timedCall
}

// timedCall is one block number call: when it came, and whether the batch
// store was stale then.
type timedCall struct {
	at    time.Time
	stale bool
}

func (f *timedFilterer) BlockNumber(context.Context) (uint64, error) {
	stale := false
	if h := f.health.Load(); h != nil {
		stale = (*h).Stale()
	}
	f.mu.Lock()
	f.calls = append(f.calls, timedCall{at: time.Now(), stale: stale})
	f.mu.Unlock()
	return 0, errEndpointDown
}

func (*timedFilterer) FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error) {
	return nil, nil
}

// gaps returns the waits between consecutive calls made in the same state,
// stale or not.
func (f *timedFilterer) gaps(stale bool) []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	var g []time.Duration
	for i := 1; i < len(f.calls); i++ {
		if f.calls[i-1].stale == stale && f.calls[i].stale == stale {
			g = append(g, f.calls[i].at.Sub(f.calls[i-1].at))
		}
	}
	return g
}

// TestWaitGrowsOnlyWhileStale checks that the wait after a failed call stays
// at backoffTime before the batch store is stale, then doubles up to its cap
// while stale (#583). The checks are bounds a loaded machine cannot break:
// a slow scheduler only lengthens waits, so the growth check waits for a
// long gap, and the check before stale uses the median.
func TestWaitGrowsOnlyWhileStale(t *testing.T) {
	defer listener.SetStaleTimings(5*time.Millisecond, time.Hour)()
	defer listener.SetStaleMaxBackoff(640 * time.Millisecond)()

	f := &timedFilterer{}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	l := listener.New(
		nil,
		log.Noop,
		f,
		postageStampContractAddress,
		postageStampContractABI,
		func() time.Duration { return time.Millisecond },
		listener.DefaultConfirmationDepth,
		time.Second,
		10*time.Millisecond,
	)
	h := l.(postage.SyncHealth)
	f.health.Store(&h)
	t.Cleanup(func() { _ = l.Close() })
	synced := l.Listen(context.Background(), 0, quietUpdater{})
	go func() {
		select {
		case <-synced:
		case <-done:
		}
	}()

	// While stale the wait doubles from 10 ms towards the 640 ms cap, so a
	// gap of 300 ms or more appears after about 600 ms of stale time.
	deadline := time.Now().Add(10 * time.Second)
	for {
		longest := time.Duration(0)
		for _, g := range f.gaps(true) {
			longest = max(longest, g)
		}
		if longest >= 300*time.Millisecond {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("longest wait while stale %v, want it to grow to 300 ms or more", longest)
		}
		time.Sleep(20 * time.Millisecond)
	}

	before := f.gaps(false)
	if len(before) < 3 {
		t.Skipf("only %d waits before the store was stale, too few to judge", len(before))
	}
	slices.Sort(before)
	if median := before[len(before)/2]; median > 50*time.Millisecond {
		t.Fatalf("median wait before stale %v, want about the 10 ms backoffTime; waits %v", median, before)
	}
}

// TestPageWithinConfiguredRange checks postage-sync-block-range, which Bee
// v2.8.3 added as a fixed page: in wasp it is the largest page of the
// adaptive pager. The default is 5000, not Bee's 1000; a set range is never
// exceeded, also when the page grows back after failures; and a refused
// query still halves the page below it.
func TestPageWithinConfiguredRange(t *testing.T) {
	defer listener.SetStaleMaxBackoff(20 * time.Millisecond)()

	if listener.DefaultBlockPage != 5000 {
		t.Fatalf("default page %d, want 5000", listener.DefaultBlockPage)
	}

	// The provider first refuses every query, so the page falls to
	// MinBlockPage; growing back from there by doubling passes the
	// configured range unless the pager stops at it.
	const configured = 1500
	f := &rangeLimitFilterer{limit: listener.MinBlockPage / 2}
	startStale(t, f, log.Noop, listener.WithBlockPage(configured))

	waitFor(t, "the page at its minimum", func() bool {
		s, _ := f.log()
		return len(s) > 0 && s[len(s)-1] == listener.MinBlockPage
	})
	// The provider now accepts any range, so the page grows back.
	f.mu.Lock()
	f.limit = 1 << 40
	f.mu.Unlock()
	waitFor(t, "the page grown back", func() bool {
		s, _ := f.log()
		return len(s) > 0 && s[len(s)-1] >= configured
	})
	waitFor(t, "more queries", func() bool { s, _ := f.log(); return len(s) >= 40 })
	sizes, _ := f.log()

	// 1500 and 750 refused first.
	for i, want := range []uint64{configured, configured / 2} {
		if sizes[i] != want {
			t.Fatalf("query %d covered %d blocks, want %d; sizes %v", i, sizes[i], want, sizes[:6])
		}
	}
	if m := slices.Max(sizes); m != configured {
		t.Fatalf("the largest query covered %d blocks, want the configured %d; first sizes %v", m, configured, sizes[:30])
	}
	if last := sizes[len(sizes)-1]; last != configured {
		t.Fatalf("the page settled at %d blocks, want the configured %d; first sizes %v", last, configured, sizes[:30])
	}
}

// TestPageZeroRangeKeepsDefault checks that a range of 0, the value an
// unset option could pass, keeps the default page.
func TestPageZeroRangeKeepsDefault(t *testing.T) {
	defer listener.SetStaleMaxBackoff(20 * time.Millisecond)()

	f := &rangeLimitFilterer{limit: 1 << 40}
	startStale(t, f, log.Noop, listener.WithBlockPage(0))

	waitFor(t, "a query", func() bool { s, _ := f.log(); return len(s) >= 1 })
	sizes, _ := f.log()
	if sizes[0] != listener.DefaultBlockPage {
		t.Fatalf("first query covered %d blocks with the range set to 0, want the default %d", sizes[0], listener.DefaultBlockPage)
	}
}
