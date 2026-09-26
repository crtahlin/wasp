// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"bytes"
	"errors"
	"net/http"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/api"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp/jsonhttptest"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/postage"
	mockpost "github.com/ethersphere/bee/v2/pkg/postage/mock"
	mockstorer "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// The ingest endpoint announces in the same call when given a batch (#503).

func ingestAnnounceServer(t *testing.T, fake *fakeProviders, mode api.BeeNodeMode) *http.Client {
	t.Helper()

	opts := testServerOptions{
		Storer:             mockstorer.New(),
		Logger:             log.Noop,
		LocalIngestEnabled: true,
		BeeMode:            mode,
	}
	if fake != nil {
		opts.Providers = fake
	}
	client, _, _, _ := newTestServer(t, opts)
	return client
}

func announcedKeys(f *fakeProviders) []swarm.Address {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]swarm.Address, 0, len(f.announced))
	for _, a := range f.announced {
		out = append(out, swarm.NewAddress(a.Key))
	}
	return out
}

func TestLocalIngestAnnounces(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client := ingestAnnounceServer(t, fake, api.FullMode)

	var resp api.LocalIngestResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
		jsonhttptest.WithRequestBody(bytes.NewReader(localIngestContent(t, swarm.ChunkSize*2))),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)

	if resp.Announced == nil || !*resp.Announced || resp.AnnounceError != "" {
		t.Fatalf("got announced %v, error %q, want true and no error", resp.Announced, resp.AnnounceError)
	}
	got := announcedKeys(fake)
	if len(got) != 1 || !got[0].Equal(resp.Reference) {
		t.Fatalf("announced %v, want exactly the ingested reference %s", got, resp.Reference)
	}
	fake.mu.Lock()
	batch := fake.announced[0].BatchID
	fake.mu.Unlock()
	if !bytes.Equal(batch, batchOk) {
		t.Fatalf("announced with batch %x, want the request's %x", batch, batchOk)
	}

	// and the node lists it, as an operator would check
	var list struct {
		Announcements []struct {
			Reference string `json:"reference"`
		} `json:"announcements"`
	}
	jsonhttptest.Request(t, client, http.MethodGet, "/wasp/providers", http.StatusOK,
		jsonhttptest.WithUnmarshalJSONResponse(&list),
	)
	if len(list.Announcements) != 1 || list.Announcements[0].Reference != resp.Reference.String() {
		t.Fatalf("GET /wasp/providers lists %+v, want the ingested reference %s", list.Announcements, resp.Reference)
	}
}

// TestLocalIngestAnnouncesCollectionRoot: a collection is announced by its
// manifest root, the reference the response returns and a download names.
func TestLocalIngestAnnouncesCollectionRoot(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             mockstorer.New(),
		Logger:             log.Noop,
		Post:               mockpost.New(mockpost.WithAcceptAll()),
		LocalIngestEnabled: true,
		Providers:          fake,
	})

	var resp api.LocalIngestResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
		jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "true"),
		jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
		jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
		jsonhttptest.WithRequestBody(tarFiles(t, dirIngestFiles(t))),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)

	got := announcedKeys(fake)
	if len(got) != 1 || !got[0].Equal(resp.Reference) {
		t.Fatalf("announced %v, want exactly the manifest root %s", got, resp.Reference)
	}
}

func TestLocalIngestAnnouncesDuplicate(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client := ingestAnnounceServer(t, fake, api.FullMode)
	content := localIngestContent(t, swarm.ChunkSize*2)

	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
		jsonhttptest.WithRequestBody(bytes.NewReader(content)),
	)

	var resp api.LocalIngestResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusOK,
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
		jsonhttptest.WithRequestBody(bytes.NewReader(content)),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)

	if resp.Announced == nil || !*resp.Announced {
		t.Fatalf("got announced %v on a duplicate, want true", resp.Announced)
	}
	if got := announcedKeys(fake); len(got) != 1 || !got[0].Equal(resp.Reference) {
		t.Fatalf("announced %v, want exactly %s", got, resp.Reference)
	}
}

// TestLocalIngestAnnounceFailureKeepsIngest: the content is stored either
// way, so a failed announcement keeps the ingest's status and says why in the
// body.
func TestLocalIngestAnnounceFailureKeepsIngest(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		msg  string
	}{
		{"unusable batch", postage.ErrNotUsable, "batch not usable yet or does not exist"},
		{"unknown batch", postage.ErrNotFound, "batch with id not found"},
		{"any other failure", errors.New("stamper: store closed"), "announce failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := ingestAnnounceServer(t, &fakeProviders{err: tc.err}, api.FullMode)
			content := localIngestContent(t, swarm.ChunkSize*2)

			var resp api.LocalIngestResponse
			jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
				jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
				jsonhttptest.WithRequestBody(bytes.NewReader(content)),
				jsonhttptest.WithUnmarshalJSONResponse(&resp),
			)
			if resp.Announced == nil || *resp.Announced {
				t.Fatalf("got announced %v, want false", resp.Announced)
			}
			if resp.AnnounceError != tc.msg {
				t.Fatalf("got announce error %q, want %q", resp.AnnounceError, tc.msg)
			}

			// stored: the same bytes again are a duplicate
			jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusOK,
				jsonhttptest.WithRequestBody(bytes.NewReader(content)),
			)
		})
	}
}

// TestLocalIngestAnnounceRefusedBeforeBody: an announcement that can never
// succeed refuses the request before anything is stored.
func TestLocalIngestAnnounceRefusedBeforeBody(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		fake    *fakeProviders
		mode    api.BeeNodeMode
		headers []jsonhttptest.Option
		status  int
		msg     string
	}{
		{
			name:   "providers off",
			mode:   api.FullMode,
			status: http.StatusForbidden,
			msg:    "content providers are off; start the node with providers-enable",
		},
		{
			name:   "light node",
			fake:   &fakeProviders{},
			mode:   api.LightMode,
			status: http.StatusBadRequest,
			msg:    "only a full node can announce content",
		},
		{
			name:    "encrypted",
			fake:    &fakeProviders{},
			mode:    api.FullMode,
			headers: []jsonhttptest.Option{jsonhttptest.WithRequestHeader(api.SwarmEncryptHeader, "true")},
			status:  http.StatusBadRequest,
			msg:     "encrypted references cannot be announced: a record would publish their key",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := ingestAnnounceServer(t, tc.fake, tc.mode)
			content := localIngestContent(t, swarm.ChunkSize*2)

			opts := append([]jsonhttptest.Option{
				jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
				jsonhttptest.WithRequestBody(bytes.NewReader(content)),
				jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{Message: tc.msg, Code: tc.status}),
			}, tc.headers...)
			jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, tc.status, opts...)

			if tc.fake != nil && len(announcedKeys(tc.fake)) != 0 {
				t.Fatal("announced after a refusal")
			}
			// nothing stored: the same bytes are new content, not a duplicate
			headers := []jsonhttptest.Option{jsonhttptest.WithRequestBody(bytes.NewReader(content))}
			headers = append(headers, tc.headers...)
			jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated, headers...)
		})
	}
}

// TestLocalIngestAnnounceMalformedBatch: a batch ID that is not hex fails
// header validation before anything is stored.
func TestLocalIngestAnnounceMalformedBatch(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client := ingestAnnounceServer(t, fake, api.FullMode)
	content := localIngestContent(t, swarm.ChunkSize*2)

	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusBadRequest,
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, "not-hex"),
		jsonhttptest.WithRequestBody(bytes.NewReader(content)),
	)
	if len(announcedKeys(fake)) != 0 {
		t.Fatal("announced after a malformed batch ID")
	}
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
		jsonhttptest.WithRequestBody(bytes.NewReader(content)),
	)
}

// TestLocalIngestWithoutBatch: no batch, no announcement, and the body is
// the one clients saw before #503.
func TestLocalIngestWithoutBatch(t *testing.T) {
	t.Parallel()

	fake := &fakeProviders{}
	client := ingestAnnounceServer(t, fake, api.FullMode)

	var body map[string]any
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
		jsonhttptest.WithRequestBody(bytes.NewReader(localIngestContent(t, swarm.ChunkSize*2))),
		jsonhttptest.WithUnmarshalJSONResponse(&body),
	)
	if len(announcedKeys(fake)) != 0 {
		t.Fatal("announced without a batch")
	}
	for _, k := range []string{"announced", "announceError"} {
		if _, ok := body[k]; ok {
			t.Fatalf("response carries %s without a batch", k)
		}
	}
}
