// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/api"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp/jsonhttptest"
	mockpost "github.com/ethersphere/bee/v2/pkg/postage/mock"
	"github.com/ethersphere/bee/v2/pkg/storage/inmemchunkstore"
	mockstorer "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// soleSource stands for content that only a provider holds: it removes every
// chunk from the requester's store and returns a function that puts them back,
// which is what a connected provider makes available.
func soleSource(t *testing.T, cs *inmemchunkstore.ChunkStore) func() bool {
	t.Helper()
	var chunks []swarm.Chunk
	if err := cs.Iterate(context.Background(), func(ch swarm.Chunk) (bool, error) {
		chunks = append(chunks, ch)
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, ch := range chunks {
		if err := cs.Delete(context.Background(), ch.Address()); err != nil {
			t.Fatal(err)
		}
	}
	return func() bool {
		for _, ch := range chunks {
			if err := cs.Put(context.Background(), ch); err != nil {
				t.Error(err)
			}
		}
		return true
	}
}

func newMissServer(t *testing.T, fake *fakeProviders) (*http.Client, *inmemchunkstore.ChunkStore) {
	t.Helper()
	cs := inmemchunkstore.New()
	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:    mockstorer.NewWithChunkStore(cs),
		Post:      mockpost.New(mockpost.WithAcceptAll()),
		Providers: fake,
	})
	return client, cs
}

func discoveries(f *fakeProviders) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.discovered...)
}

var missPayload = bytes.Repeat([]byte("never stamped "), 1000)

// TestProvidersLookupOnMissBytes: a /bytes download that cannot fetch its
// root chunk looks up providers once and, when one connects, succeeds. Before
// #498 it answered 404, because discovery waits for a 64th chunk that content
// only a provider holds can never deliver.
func TestProvidersLookupOnMissBytes(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client, cs := newMissServer(t, fake)
	var resp api.BytesPostResponse
	jsonhttptest.Request(t, client, http.MethodPost, "/bytes", http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
		jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "0"),
		jsonhttptest.WithRequestBody(bytes.NewReader(missPayload)),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)
	restore := soleSource(t, cs)
	fake.mu.Lock()
	fake.onDiscover = restore
	// the provider becomes reachable only after a while, as with a real
	// lookup and dial, so a retry that did not wait would miss again. The
	// download asks for redundancy level 0, so neither fetch spends time on
	// replicas and the timing separates a waiting retry from one that is not.
	fake.discoverDelay = time.Second
	fake.mu.Unlock()

	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+resp.Reference.String(), http.StatusOK,
		jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "0"),
		jsonhttptest.WithExpectedResponse(missPayload),
	)
	if got := discoveries(fake); len(got) != 1 || !bytes.Equal(got[0], resp.Reference.Bytes()) {
		t.Fatalf("discoveries %x, want one for the reference", got)
	}
}

// TestProvidersLookupOnMissBzz: the same for /bzz, where a missing root
// surfaces in the manifest read rather than in the joiner.
func TestProvidersLookupOnMissBzz(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client, cs := newMissServer(t, fake)
	var resp api.BzzUploadResponse
	jsonhttptest.Request(t, client, http.MethodPost, "/bzz?name=page.html", http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
		jsonhttptest.WithRequestHeader(api.ContentTypeHeader, "text/html; charset=utf-8"),
		jsonhttptest.WithRequestBody(bytes.NewReader(missPayload)),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)
	restore := soleSource(t, cs)
	fake.mu.Lock()
	fake.onDiscover = restore
	fake.mu.Unlock()

	jsonhttptest.Request(t, client, http.MethodGet, "/bzz/"+resp.Reference.String()+"/", http.StatusOK,
		jsonhttptest.WithExpectedResponse(missPayload),
	)
	if got := discoveries(fake); len(got) != 1 {
		t.Fatalf("discoveries %x, want one", got)
	}
}

// TestProvidersNoLookupWhenRootPresent: content the network holds fetches its
// root chunk and never looks up on a miss; the 64-chunk threshold is the only
// trigger for it, unchanged.
func TestProvidersNoLookupWhenRootPresent(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client, _ := newMissServer(t, fake)
	var resp api.BytesPostResponse
	jsonhttptest.Request(t, client, http.MethodPost, "/bytes", http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
		jsonhttptest.WithRequestBody(bytes.NewReader(missPayload)),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)
	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+resp.Reference.String(), http.StatusOK)
	jsonhttptest.Request(t, client, http.MethodGet, "/bzz/"+resp.Reference.String()+"/", http.StatusNotFound)
	if got := discoveries(fake); len(got) != 0 {
		t.Fatalf("looked up providers for content whose root was present: %x", got)
	}
}

// TestProvidersLookupOnMissOnlyWhenUnhinted: a reference nobody holds costs
// one lookup and then answers 404; a hinted download, which already connected
// to its named providers, and an encrypted reference, which has no content
// key, do not look up at all.
func TestProvidersLookupOnMissOnlyWhenUnhinted(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client, _ := newMissServer(t, fake)
	missing := swarm.RandAddress(t)

	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+missing.String(), http.StatusNotFound)
	if got := discoveries(fake); len(got) != 1 {
		t.Fatalf("discoveries %x, want one for a missing root", got)
	}

	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+missing.String(), http.StatusNotFound,
		jsonhttptest.WithRequestHeader(api.WaspProvidersHeader, swarm.RandAddress(t).String()),
	)
	encrypted := strings.Repeat("ab", 64)
	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+encrypted, http.StatusNotFound)
	if got := discoveries(fake); len(got) != 1 {
		t.Fatalf("discoveries %x, want none for a hinted or encrypted download", got)
	}
}

// TestProvidersLookupOnMissOncePerRequest: a provider that connects but does
// not hold the content leaves the root missing after the retry. The request
// must then answer 404. On /bzz the retry re-reads the manifest, so looking
// up and retrying again on every pass would never end.
//
// The existing 64-chunk trigger may also run a lookup during the re-read,
// because the redundancy reader asks for the root several times; that one
// neither waits nor retries, so the test asserts that the request ends rather
// than counting lookups.
func TestProvidersLookupOnMissOncePerRequest(t *testing.T) {
	t.Parallel()

	var stop atomic.Bool
	fake := &fakeProviders{onDiscover: func() bool {
		// always connected, never holding the content, until the test gives up
		return !stop.Load()
	}}
	client, _ := newMissServer(t, fake)
	missing := swarm.RandAddress(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		jsonhttptest.Request(t, client, http.MethodGet, "/bzz/"+missing.String()+"/", http.StatusNotFound)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		stop.Store(true) // let the request end, so the server can shut down
		<-done
		t.Fatal("a /bzz request looked up and retried without end")
	}
}
