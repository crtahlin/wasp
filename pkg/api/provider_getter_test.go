// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"context"
	"sync"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/api"
	"github.com/ethersphere/bee/v2/pkg/retrieval"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// These cover issue #299: on erasure-coded content most chunk fetches come from
// the decoder's prefetch, whose context is built from context.Background() and
// therefore carries nothing the request put there, including the preferred set.
// The set is re-attached in this fork's own getter wrapper, which is the last
// point before retrieval, so that pkg/file/ stays byte-identical to upstream.

// capturingGetter records the context each fetch arrives with.
//
// The mutex is not needed by the tests here, which drive one sequential fetch
// each. It is there because this stands in for the getter the real prefetch
// calls from one goroutine per outstanding shard, so the first person to extend
// these tests towards that would otherwise get a data race in the helper rather
// than a result.
type capturingGetter struct {
	mu  sync.Mutex
	got []context.Context
}

func (c *capturingGetter) Get(ctx context.Context, _ swarm.Address) (swarm.Chunk, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, ctx)
	return nil, storage.ErrNotFound
}

func (c *capturingGetter) contexts() []context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]context.Context(nil), c.got...)
}

func newProviderGetterUnderTest(t *testing.T, set *retrieval.PreferredSet) (storage.Getter, *capturingGetter) {
	t.Helper()
	c := &capturingGetter{}
	g := api.NewProviderGetterForTest(&fakeProviders{}, swarm.RandAddress(t).Bytes(), set, c)
	return g, c
}

// TestProviderGetterRestoresSetOnBackgroundContext is the defect itself: a
// prefetch fetch arrives with a context that never saw the request, and must
// leave the wrapper carrying the set.
func TestProviderGetterRestoresSetOnBackgroundContext(t *testing.T) {
	t.Parallel()

	set := retrieval.NewPreferredSet()
	set.Add(swarm.RandAddress(t))
	g, rec := newProviderGetterUnderTest(t, set)

	// the prefetch calls with a context of its own, not the one the wrapper was
	// built with
	_, _ = g.Get(context.Background(), swarm.RandAddress(t))

	if len(rec.contexts()) != 1 {
		t.Fatalf("the underlying getter saw %d fetches, want 1", len(rec.contexts()))
	}
	if got := retrieval.PreferredPeers(rec.contexts()[0]); got != set {
		t.Fatal("a fetch on a background context did not arrive with the preferred set")
	}
}

// TestProviderGetterLeavesExistingSetAlone: a context that already carries a
// set is passed through unchanged, so a reader fetch is not rewritten.
func TestProviderGetterLeavesExistingSetAlone(t *testing.T) {
	t.Parallel()

	hintSet := retrieval.NewPreferredSet()
	hintSet.Add(swarm.RandAddress(t))
	otherSet := retrieval.NewPreferredSet()
	otherSet.Add(swarm.RandAddress(t))
	g, rec := newProviderGetterUnderTest(t, hintSet)

	_, _ = g.Get(retrieval.WithPreferredPeers(context.Background(), otherSet), swarm.RandAddress(t))

	if len(rec.contexts()) != 1 {
		t.Fatalf("the underlying getter saw %d fetches, want 1", len(rec.contexts()))
	}
	if got := retrieval.PreferredPeers(rec.contexts()[0]); got != otherSet {
		t.Fatal("a fetch that already carried a set had it replaced")
	}
}

// TestProviderGetterKeepsDeliberateNil: a context carrying a deliberately nil
// set keeps it, rather than having the hint's set put back.
//
// This is what HasPreferredPeers buys over the naive nil check, and the spec
// for #299 is explicit that no reachable path today tells the two apart: the
// suppressed context built at the Discover call is handed straight to Discover
// and never re-enters this wrapper. The test pins the intent rather than a
// defect it prevents today.
func TestProviderGetterKeepsDeliberateNil(t *testing.T) {
	t.Parallel()

	hintSet := retrieval.NewPreferredSet()
	hintSet.Add(swarm.RandAddress(t))
	g, rec := newProviderGetterUnderTest(t, hintSet)

	_, _ = g.Get(retrieval.WithPreferredPeers(context.Background(), nil), swarm.RandAddress(t))

	if len(rec.contexts()) != 1 {
		t.Fatalf("the underlying getter saw %d fetches, want 1", len(rec.contexts()))
	}
	if got := retrieval.PreferredPeers(rec.contexts()[0]); got != nil {
		t.Fatal("a deliberately suppressed set was replaced by the hint's set")
	}
}
