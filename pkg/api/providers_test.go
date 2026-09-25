// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/api"
	"github.com/ethersphere/bee/v2/pkg/bzz"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp/jsonhttptest"
	"github.com/ethersphere/bee/v2/pkg/postage"
	mockpost "github.com/ethersphere/bee/v2/pkg/postage/mock"
	"github.com/ethersphere/bee/v2/pkg/providers"
	"github.com/ethersphere/bee/v2/pkg/retrieval"
	"github.com/ethersphere/bee/v2/pkg/storer"
	mockstorer "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	ma "github.com/multiformats/go-multiaddr"
)

// fakeProviders records what the API asks of the content-providers service.
type fakeProviders struct {
	mu        sync.Mutex
	err       error
	announced []providers.Announcement
	withdrawn [][]byte
	records   []*providers.Record
	hints     []swarm.Address
	hintKeys  [][]byte
	// hintRun, when set, is returned by ConnectHints; otherwise a finished
	// run is returned, so a download does not wait
	hintRun    *providers.HintRun
	discovered [][]byte
	sets       []providers.Adder
	// lookupHasSet records, per Discover, whether its context carried a
	// preferred set
	lookupHasSet []bool
}

func (f *fakeProviders) Announce(_ context.Context, k, batchID []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.announced = append(f.announced, providers.Announcement{Key: k, BatchID: batchID, Written: []uint64{1000}})
	return nil
}

func (f *fakeProviders) Withdraw(k []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.withdrawn = append(f.withdrawn, k)
	return nil
}

func (f *fakeProviders) Announced() ([]providers.Announcement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.announced, nil
}

func (f *fakeProviders) Lookup(context.Context, []byte) ([]*providers.Record, error) {
	return f.records, nil
}

func (f *fakeProviders) Discover(ctx context.Context, k []byte, set providers.Adder) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discovered = append(f.discovered, k)
	f.sets = append(f.sets, set)
	// the lookup's own reads must not go to the download's preferred peers
	f.lookupHasSet = append(f.lookupHasSet, retrieval.PreferredPeers(ctx) != nil)
}

func (f *fakeProviders) ConnectHints(_ context.Context, overlays []swarm.Address, k []byte) *providers.HintRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hints = append(f.hints, overlays...)
	f.hintKeys = append(f.hintKeys, k)
	if f.hintRun != nil {
		return f.hintRun
	}
	run := providers.NewHintRun()
	run.Finish()
	return run
}

var providersBatch = strings.Repeat("ab", 32)

func withProvidersBatch() jsonhttptest.Option {
	return jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, providersBatch)
}

func TestProvidersOff(t *testing.T) {
	t.Parallel()

	client, _, _, _ := newTestServer(t, testServerOptions{Storer: mockstorer.New()})
	ref := swarm.RandAddress(t).String()

	jsonhttptest.Request(t, client, http.MethodPost, "/wasp/providers/"+ref, http.StatusForbidden, withProvidersBatch())
	jsonhttptest.Request(t, client, http.MethodDelete, "/wasp/providers/"+ref, http.StatusForbidden)
	jsonhttptest.Request(t, client, http.MethodGet, "/wasp/providers", http.StatusForbidden)
	jsonhttptest.Request(t, client, http.MethodGet, "/wasp/providers/"+ref+"/lookup", http.StatusForbidden)

	// with the setting off the header is ignored, even when it is malformed
	jsonhttptest.Request(t, client, http.MethodGet, "/chunks/"+ref, http.StatusNotFound,
		jsonhttptest.WithRequestHeader(api.WaspProvidersHeader, "not-an-overlay"),
	)
}

func TestProvidersAnnounce(t *testing.T) {
	t.Parallel()

	st := mockstorer.New()
	fake := &fakeProviders{}
	client, _, _, _ := newTestServer(t, testServerOptions{Storer: st, Providers: fake})
	ref := swarm.RandAddress(t)
	path := "/wasp/providers/" + ref.String()

	// a batch is required
	jsonhttptest.Request(t, client, http.MethodPost, path, http.StatusBadRequest)
	// the reference must be pinned first
	jsonhttptest.Request(t, client, http.MethodPost, path, http.StatusBadRequest, withProvidersBatch())

	pinProvided(t, st, ref)
	jsonhttptest.Request(t, client, http.MethodPost, path, http.StatusCreated, withProvidersBatch())

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.announced) != 1 || !bytes.Equal(fake.announced[0].Key, ref.Bytes()) {
		t.Fatalf("announced %v, want the reference", fake.announced)
	}
	if hex.EncodeToString(fake.announced[0].BatchID) != providersBatch {
		t.Fatalf("batch %x, want %s", fake.announced[0].BatchID, providersBatch)
	}
}

func TestProvidersAnnounceRefused(t *testing.T) {
	t.Parallel()

	ref := swarm.RandAddress(t)
	path := "/wasp/providers/" + ref.String()

	t.Run("light node", func(t *testing.T) {
		t.Parallel()
		st := mockstorer.New()
		pinProvided(t, st, ref)
		client, _, _, _ := newTestServer(t, testServerOptions{Storer: st, Providers: &fakeProviders{}, BeeMode: api.LightMode})
		jsonhttptest.Request(t, client, http.MethodPost, path, http.StatusBadRequest, withProvidersBatch())
	})

	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"unusable batch", postage.ErrNotUsable, http.StatusUnprocessableEntity},
		{"unknown batch", postage.ErrNotFound, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := mockstorer.New()
			pinProvided(t, st, ref)
			client, _, _, _ := newTestServer(t, testServerOptions{Storer: st, Providers: &fakeProviders{err: tc.err}})
			jsonhttptest.Request(t, client, http.MethodPost, path, tc.code, withProvidersBatch())
		})
	}
}

func TestProvidersListLookupWithdraw(t *testing.T) {
	t.Parallel()

	overlay := swarm.RandAddress(t)
	fake := &fakeProviders{records: []*providers.Record{{
		Owner:      bytes.Repeat([]byte{7}, 20),
		Address:    &bzz.Address{Overlay: overlay, Underlays: []ma.Multiaddr{ma.StringCast("/ip4/192.0.2.1/tcp/1634")}},
		Capability: providers.CapabilityFull,
	}}}
	st := mockstorer.New()
	client, _, _, _ := newTestServer(t, testServerOptions{Storer: st, Providers: fake})

	ref := swarm.RandAddress(t)
	pinProvided(t, st, ref)
	jsonhttptest.Request(t, client, http.MethodPost, "/wasp/providers/"+ref.String(), http.StatusCreated, withProvidersBatch())

	var list struct {
		Announcements []struct {
			Reference string   `json:"reference"`
			BatchID   string   `json:"batchID"`
			Windows   []uint64 `json:"windows"`
		} `json:"announcements"`
	}
	jsonhttptest.Request(t, client, http.MethodGet, "/wasp/providers", http.StatusOK, jsonhttptest.WithUnmarshalJSONResponse(&list))
	if len(list.Announcements) != 1 || list.Announcements[0].Reference != ref.String() || list.Announcements[0].BatchID != providersBatch {
		t.Fatalf("listed %+v, want the announced reference", list)
	}

	var lookup struct {
		Providers []struct {
			Overlay    string   `json:"overlay"`
			Underlays  []string `json:"underlays"`
			Capability string   `json:"capability"`
		} `json:"providers"`
	}
	jsonhttptest.Request(t, client, http.MethodGet, "/wasp/providers/"+ref.String()+"/lookup", http.StatusOK, jsonhttptest.WithUnmarshalJSONResponse(&lookup))
	if len(lookup.Providers) != 1 || lookup.Providers[0].Overlay != overlay.String() || lookup.Providers[0].Capability != "full" || len(lookup.Providers[0].Underlays) != 1 {
		t.Fatalf("lookup returned %+v, want the provider", lookup)
	}

	jsonhttptest.Request(t, client, http.MethodDelete, "/wasp/providers/"+ref.String(), http.StatusOK)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.withdrawn) != 1 || !bytes.Equal(fake.withdrawn[0], ref.Bytes()) {
		t.Fatalf("withdrew %x, want the reference", fake.withdrawn)
	}
}

func TestProvidersHeader(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client, _, _, _ := newTestServer(t, testServerOptions{Storer: mockstorer.New(), Providers: fake})
	chunk := "/chunks/" + swarm.RandAddress(t).String()

	for _, bad := range []string{"zz", "/ip4/192.0.2.1/tcp/1634", "abcd"} {
		jsonhttptest.Request(t, client, http.MethodGet, chunk, http.StatusBadRequest,
			jsonhttptest.WithRequestHeader(api.WaspProvidersHeader, bad),
		)
	}

	overlays := make([]string, 0, 9)
	for range 9 {
		overlays = append(overlays, swarm.RandAddress(t).String())
	}
	jsonhttptest.Request(t, client, http.MethodGet, chunk, http.StatusNotFound,
		jsonhttptest.WithRequestHeader(api.WaspProvidersHeader, strings.Join(overlays, ", ")),
	)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.hints) != 8 {
		t.Fatalf("connected to %d hinted providers, want the first 8", len(fake.hints))
	}
	for i, h := range fake.hints {
		if h.String() != overlays[i] {
			t.Fatalf("hint %d is %s, want %s", i, h, overlays[i])
		}
	}
}

// TestProvidersDiscoverAfterManyChunks tests that a /bytes download looks up
// providers of its reference only once it has fetched enough chunks, and that
// a hint on /bytes reaches the service.
func TestProvidersDiscoverAfterManyChunks(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:    mockstorer.New(),
		Post:      mockpost.New(mockpost.WithAcceptAll()),
		Providers: fake,
	})

	upload := func(chunks int) swarm.Address {
		t.Helper()
		content := make([]byte, chunks*swarm.ChunkSize)
		for i := range content {
			content[i] = byte(i*7 + chunks)
		}
		var resp api.BytesPostResponse
		jsonhttptest.Request(t, client, http.MethodPost, "/bytes", http.StatusCreated,
			jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
			jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
			jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "0"),
			jsonhttptest.WithRequestBody(bytes.NewReader(content)),
			jsonhttptest.WithUnmarshalJSONResponse(&resp),
		)
		return resp.Reference
	}
	small, large := upload(10), upload(100)
	hint := swarm.RandAddress(t)

	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+small.String(), http.StatusOK)
	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+large.String(), http.StatusOK,
		jsonhttptest.WithRequestHeader(api.WaspProvidersHeader, hint.String()),
	)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.discovered) != 1 || !bytes.Equal(fake.discovered[0], large.Bytes()) {
		t.Fatalf("looked up %x, want only the large download's reference", fake.discovered)
	}
	if len(fake.hints) != 1 || !fake.hints[0].Equal(hint) {
		t.Fatalf("hints %v, want the one named on /bytes", fake.hints)
	}
}

// uploadProviderBytes uploads chunks worth of data through /bytes and returns
// its reference.
func uploadProviderBytes(t *testing.T, client *http.Client, chunks int, encrypt bool) swarm.Address {
	t.Helper()
	content := make([]byte, chunks*swarm.ChunkSize)
	for i := range content {
		content[i] = byte(i*13 + chunks)
	}
	var resp api.BytesPostResponse
	jsonhttptest.Request(t, client, http.MethodPost, "/bytes", http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
		jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "0"),
		jsonhttptest.WithRequestHeader(api.SwarmEncryptHeader, strconv.FormatBool(encrypt)),
		jsonhttptest.WithRequestBody(bytes.NewReader(content)),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)
	return resp.Reference
}

// TestProvidersSharedSet tests that downloads of one reference without a hint
// share one preferred set, that a hinted download gets its own, and that no
// lookup runs with a download's preferred peers.
func TestProvidersSharedSet(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:    mockstorer.New(),
		Post:      mockpost.New(mockpost.WithAcceptAll()),
		Providers: fake,
	})
	ref := uploadProviderBytes(t, client, 100, false)

	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+ref.String(), http.StatusOK)
	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+ref.String(), http.StatusOK)
	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+ref.String(), http.StatusOK,
		jsonhttptest.WithRequestHeader(api.WaspProvidersHeader, swarm.RandAddress(t).String()),
	)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sets) != 3 {
		t.Fatalf("%d lookups, want one per download", len(fake.sets))
	}
	if fake.sets[0] != fake.sets[1] {
		t.Fatal("two downloads without a hint did not share a preferred set")
	}
	if fake.sets[2] == fake.sets[0] {
		t.Fatal("a hinted download used the shared set")
	}
	for i, has := range fake.lookupHasSet {
		if has {
			t.Fatalf("lookup %d ran with the download's preferred peers", i)
		}
	}
}

// TestProvidersEncryptedReference tests that an encrypted reference is never
// announced or looked up, and that downloading one starts no lookup.
func TestProvidersEncryptedReference(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	st := mockstorer.New()
	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:    st,
		Post:      mockpost.New(mockpost.WithAcceptAll()),
		Providers: fake,
	})
	ref := uploadProviderBytes(t, client, 100, true)
	if len(ref.Bytes()) != 2*swarm.HashSize {
		t.Fatalf("encrypted reference of %d bytes", len(ref.Bytes()))
	}
	pinProvided(t, st, ref)

	jsonhttptest.Request(t, client, http.MethodPost, "/wasp/providers/"+ref.String(), http.StatusBadRequest, withProvidersBatch())
	jsonhttptest.Request(t, client, http.MethodGet, "/wasp/providers/"+ref.String()+"/lookup", http.StatusBadRequest)
	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+ref.String(), http.StatusOK)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.announced) != 0 || len(fake.discovered) != 0 {
		t.Fatalf("encrypted reference reached the service: announced %d, looked up %d", len(fake.announced), len(fake.discovered))
	}
}

func TestProvidersHeaderAllowedByCORS(t *testing.T) {
	t.Parallel()

	const origin = "example.com"
	client, _, _, _ := newTestServer(t, testServerOptions{Storer: mockstorer.New(), CORSAllowedOrigins: []string{origin}})

	h := jsonhttptest.Request(t, client, http.MethodGet, "/chunks/"+swarm.RandAddress(t).String(), http.StatusNotFound,
		jsonhttptest.WithRequestHeader(api.OriginHeader, origin),
	)
	if !strings.Contains(h.Get("Access-Control-Allow-Headers"), api.WaspProvidersHeader) {
		t.Fatalf("allowed headers %q do not include %s", h.Get("Access-Control-Allow-Headers"), api.WaspProvidersHeader)
	}
}

// pinProvided marks ref as pinned in the mock storer.
func pinProvided(t *testing.T, st interface {
	NewCollection(context.Context) (storer.PutterSession, error)
}, ref swarm.Address,
) {
	t.Helper()
	p, err := st.NewCollection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Done(ref); err != nil {
		t.Fatal(err)
	}
}

// uploadForHintTest stores a small blob and returns its reference.
func uploadForHintTest(t *testing.T, client *http.Client) swarm.Address {
	t.Helper()
	var resp api.BytesPostResponse
	jsonhttptest.Request(t, client, http.MethodPost, "/bytes", http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
		jsonhttptest.WithRequestBody(strings.NewReader("hinted download")),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)
	return resp.Reference
}

// TestProvidersHintWaitsForConnection: a hinted download does not fetch its
// first chunk until a named provider is connected, so a provider reached
// through its record is a candidate for that chunk. See #499.
func TestProvidersHintWaitsForConnection(t *testing.T) {
	t.Parallel()

	run := providers.NewHintRun()
	fake := &fakeProviders{hintRun: run}
	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:    mockstorer.New(),
		Post:      mockpost.New(mockpost.WithAcceptAll()),
		Providers: fake,
	})
	ref := uploadForHintTest(t, client)

	const delay = 300 * time.Millisecond
	go func() {
		time.Sleep(delay)
		run.Add(providers.HintOutcome{Connected: 1})
		run.Finish()
	}()

	start := time.Now()
	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+ref.String(), http.StatusOK,
		jsonhttptest.WithRequestHeader(api.WaspProvidersHeader, swarm.RandAddress(t).String()),
	)
	if elapsed := time.Since(start); elapsed < delay {
		t.Fatalf("download answered after %v, before the provider connected at %v", elapsed, delay)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.hintKeys) != 1 || !bytes.Equal(fake.hintKeys[0], ref.Bytes()) {
		t.Fatalf("hint content keys %x, want the downloaded reference", fake.hintKeys)
	}
}

// TestProvidersHintWaitIsBounded: a named provider that never connects delays
// the download by the limit and no more.
func TestProvidersHintWaitIsBounded(t *testing.T) {
	t.Parallel()

	const limit = 200 * time.Millisecond
	run := providers.NewHintRun() // never finishes on its own
	fake := &fakeProviders{hintRun: run}
	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:          mockstorer.New(),
		Post:            mockpost.New(mockpost.WithAcceptAll()),
		Providers:       fake,
		HintConnectWait: limit,
	})
	ref := uploadForHintTest(t, client)

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+ref.String(), http.StatusOK,
			jsonhttptest.WithRequestHeader(api.WaspProvidersHeader, swarm.RandAddress(t).String()),
		)
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		if elapsed < limit {
			t.Fatalf("download answered after %v, before the limit %v", elapsed, limit)
		}
	case <-time.After(5 * time.Second):
		// release the request, so the server can shut down and the test
		// reports the failure rather than hanging
		run.Finish()
		<-done
		t.Fatal("the wait for a named provider is not bounded")
	}
}

// TestProvidersHintedNotFoundSaysWhy: a hinted download that ends in 404 says
// what happened to the named providers; one without the header keeps the
// default message.
func TestProvidersHintedNotFoundSaysWhy(t *testing.T) {
	t.Parallel()

	run := providers.NewHintRun()
	run.Add(providers.HintOutcome{NoAddress: 1})
	run.Finish()
	fake := &fakeProviders{hintRun: run}
	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:    mockstorer.New(),
		Post:      mockpost.New(mockpost.WithAcceptAll()),
		Providers: fake,
	})
	missing := swarm.RandAddress(t)

	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+missing.String(), http.StatusNotFound,
		jsonhttptest.WithRequestHeader(api.WaspProvidersHeader, swarm.RandAddress(t).String()),
		jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
			Code:    http.StatusNotFound,
			Message: "not found; of the 1 named providers, 0 were connected, 1 had no known address and no provider record, 0 could not be dialled, 0 were still being tried",
		}),
	)
	jsonhttptest.Request(t, client, http.MethodGet, "/bytes/"+missing.String(), http.StatusNotFound,
		jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
			Code:    http.StatusNotFound,
			Message: http.StatusText(http.StatusNotFound),
		}),
	)

	// /bzz with a missing root ends at the manifest path's own 404, which
	// keeps its message and adds the outcome.
	jsonhttptest.Request(t, client, http.MethodGet, "/bzz/"+missing.String()+"/", http.StatusNotFound,
		jsonhttptest.WithRequestHeader(api.WaspProvidersHeader, swarm.RandAddress(t).String()),
		jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
			Code:    http.StatusNotFound,
			Message: "address not found or incorrect; of the 1 named providers, 0 were connected, 1 had no known address and no provider record, 0 could not be dialled, 0 were still being tried",
		}),
	)
	jsonhttptest.Request(t, client, http.MethodGet, "/bzz/"+missing.String()+"/", http.StatusNotFound,
		jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
			Code:    http.StatusNotFound,
			Message: "address not found or incorrect",
		}),
	)
}
