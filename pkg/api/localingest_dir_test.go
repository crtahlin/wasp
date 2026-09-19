// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/api"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp/jsonhttptest"
	"github.com/ethersphere/bee/v2/pkg/log"
	mockpost "github.com/ethersphere/bee/v2/pkg/postage/mock"
	mockstorer "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// dirIngestFiles is one small site at several path depths, with one entry large
// enough to span several chunks.
//
// The multi-chunk entry is load-bearing and was added after a mutation check.
// With every file inside one chunk the manifest root is the same at every
// redundancy level, so a version of this fixture with only small files let a
// deliberate bug that ignored the level pass unnoticed: the level_0 and level_4
// cases were the same test twice. A file that spans chunks makes the level
// change the reference, which is what the level cases are for.
func dirIngestFiles(t *testing.T) []f {
	t.Helper()

	return []f{
		{data: []byte("<h1>index</h1>"), name: "index.html", dir: ""},
		{data: []byte("body{}"), name: "style.css", dir: "css"},
		{data: localIngestContent(t, swarm.ChunkSize*3), name: "big.bin", dir: "a/b"},
	}
}

// TestLocalIngestDirAddressEquivalence is the claim the directory feature rests
// on: a manifest built with no postage is the same manifest as one built with a
// stamp. If the roots differ, content ingested this way is not the content
// anyone else would ask for and the feature is pointless.
//
// Scoped to one process deliberately. A manifest is not a pure function of the
// bytes the way a blob reference is: for a tar the entry content type comes from
// mime.TypeByExtension, whose table Go reads from files on the host, so two
// nodes on different distributions can type the same file differently and
// produce different roots. That is a cross-host question this test does not
// answer and the spec does not claim.
func TestLocalIngestDirAddressEquivalence(t *testing.T) {
	t.Parallel()

	for _, level := range []string{"0", "4"} {
		t.Run("level_"+level, func(t *testing.T) {
			t.Parallel()

			client, _, _, _ := newTestServer(t, testServerOptions{
				Storer:             mockstorer.New(),
				Logger:             log.Noop,
				Post:               mockpost.New(mockpost.WithAcceptAll()),
				LocalIngestEnabled: true,
			})

			var ingested api.LocalIngestResponse
			jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
				jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "true"),
				jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
				jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
				jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, level),
				jsonhttptest.WithRequestBody(tarFiles(t, dirIngestFiles(t))),
				jsonhttptest.WithUnmarshalJSONResponse(&ingested),
			)

			var uploaded api.BzzUploadResponse
			jsonhttptest.Request(t, client, http.MethodPost, "/bzz", http.StatusCreated,
				jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
				jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
				jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "true"),
				jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
				jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
				jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, level),
				jsonhttptest.WithRequestBody(tarFiles(t, dirIngestFiles(t))),
				jsonhttptest.WithUnmarshalJSONResponse(&uploaded),
			)

			if ingested.Reference.IsZero() {
				t.Fatal("the directory ingest returned no reference")
			}
			if !ingested.Reference.Equal(uploaded.Reference) {
				t.Fatalf("ingested manifest root %s, a stamped upload of the same archive gave %s",
					ingested.Reference, uploaded.Reference)
			}
			if !ingested.SoleSource {
				t.Fatal("a fresh directory ingest did not report itself as sole source")
			}
		})
	}
}

// TestLocalIngestDirCountsManifestChunks. Manifest node chunks are chunks on the
// same disk, so they must count against the limit. The defect this guards is a
// putter that stores them without counting them, which would let the limit be
// bypassed by whatever the manifest costs, and for many small files the manifest
// outweighs the files themselves.
//
// StoredChunkCount is the mock's own figure, arrived at independently of
// anything the handler reports, which is why it is the thing to compare against
// rather than a number the handler also produced.
func TestLocalIngestDirCountsManifestChunks(t *testing.T) {
	t.Parallel()

	store := mockstorer.New()
	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             store,
		Logger:             log.Noop,
		Post:               mockpost.New(mockpost.WithAcceptAll()),
		LocalIngestEnabled: true,
	})

	files := dirIngestFiles(t)

	var ingested api.LocalIngestResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "true"),
		jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
		jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
		jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "0"),
		jsonhttptest.WithRequestBody(tarFiles(t, files)),
		jsonhttptest.WithUnmarshalJSONResponse(&ingested),
	)

	if ingested.Chunks <= uint64(len(files)) {
		t.Fatalf("reported %d chunks for %d single-chunk files: the manifest nodes are not being counted",
			ingested.Chunks, len(files))
	}
	stored, err := store.StoredChunkCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored != ingested.Chunks {
		t.Fatalf("reported %d chunks, the store holds %d: the two have to be one quantity or the limit can be bypassed",
			ingested.Chunks, stored)
	}
}

// TestLocalIngestDirRejectsBadContentType. The archive format is chosen by the
// content type, so an absent or unsupported one has nothing to dispatch on. Both
// are the caller's fault and must be 400, not 500.
func TestLocalIngestDirRejectsBadContentType(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, contentType string }{
		{"absent", ""},
		{"unsupported", "application/zip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, _, _, _ := newTestServer(t, testServerOptions{
				Storer:             mockstorer.New(),
				Logger:             log.Noop,
				Post:               mockpost.New(mockpost.WithAcceptAll()),
				LocalIngestEnabled: true,
			})

			opts := []jsonhttptest.Option{
				jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "true"),
				jsonhttptest.WithRequestBody(tarFiles(t, dirIngestFiles(t))),
			}
			if tc.contentType != "" {
				opts = append(opts, jsonhttptest.WithRequestHeader(api.ContentTypeHeader, tc.contentType))
			}
			jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusBadRequest, opts...)
		})
	}
}

// TestLocalIngestBlobUnchangedWithoutCollectionHeader. The directory path is
// selected by the header alone. /bzz also dispatches on a multipart content type
// with no header at all, and copying that here would silently change what an
// existing request means: a tar posted to this endpoint today is stored as a
// blob and would become its contents instead.
//
// Multipart is the sharper of the two cases, because that is where the two
// routes deliberately differ.
func TestLocalIngestBlobUnchangedWithoutCollectionHeader(t *testing.T) {
	t.Parallel()

	t.Run("tar", func(t *testing.T) {
		t.Parallel()

		client, _, _, _ := newTestServer(t, testServerOptions{
			Storer:             mockstorer.New(),
			Logger:             log.Noop,
			Post:               mockpost.New(mockpost.WithAcceptAll()),
			LocalIngestEnabled: true,
		})

		body := tarFiles(t, dirIngestFiles(t))
		raw := body.Bytes()

		var ingested api.LocalIngestResponse
		jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
			jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
			jsonhttptest.WithRequestBody(bytes.NewReader(raw)),
			jsonhttptest.WithUnmarshalJSONResponse(&ingested),
		)

		// The reference of the tar as a blob, which is what this endpoint has
		// always returned for it.
		var blob api.LocalIngestResponse
		client2, _, _, _ := newTestServer(t, testServerOptions{
			Storer:             mockstorer.New(),
			Logger:             log.Noop,
			Post:               mockpost.New(mockpost.WithAcceptAll()),
			LocalIngestEnabled: true,
		})
		jsonhttptest.Request(t, client2, http.MethodPost, localIngestResource, http.StatusCreated,
			jsonhttptest.WithRequestBody(bytes.NewReader(raw)),
			jsonhttptest.WithUnmarshalJSONResponse(&blob),
		)

		if !ingested.Reference.Equal(blob.Reference) {
			t.Fatalf("a tar with a tar content type and no collection header gave %s, the same bytes with no content type gave %s: the content type alone changed what the request means",
				ingested.Reference, blob.Reference)
		}
	})

	t.Run("multipart", func(t *testing.T) {
		t.Parallel()

		client, _, _, _ := newTestServer(t, testServerOptions{
			Storer:             mockstorer.New(),
			Logger:             log.Noop,
			Post:               mockpost.New(mockpost.WithAcceptAll()),
			LocalIngestEnabled: true,
		})

		body, contentType := multipartFiles(t, dirIngestFiles(t))
		raw := body.Bytes()

		var ingested api.LocalIngestResponse
		jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
			jsonhttptest.WithRequestHeader(api.ContentTypeHeader, contentType),
			jsonhttptest.WithRequestBody(bytes.NewReader(raw)),
			jsonhttptest.WithUnmarshalJSONResponse(&ingested),
		)

		// A collection would report more chunks than the body itself splits
		// into, since it adds manifest nodes. Comparing against the blob
		// reference of the same bytes is the direct statement.
		var blob api.LocalIngestResponse
		client2, _, _, _ := newTestServer(t, testServerOptions{
			Storer:             mockstorer.New(),
			Logger:             log.Noop,
			Post:               mockpost.New(mockpost.WithAcceptAll()),
			LocalIngestEnabled: true,
		})
		jsonhttptest.Request(t, client2, http.MethodPost, localIngestResource, http.StatusCreated,
			jsonhttptest.WithRequestBody(bytes.NewReader(raw)),
			jsonhttptest.WithUnmarshalJSONResponse(&blob),
		)

		if !ingested.Reference.Equal(blob.Reference) {
			t.Fatalf("a multipart body with no collection header gave %s, the same bytes with no content type gave %s: this endpoint must not treat multipart as a collection the way /bzz does",
				ingested.Reference, blob.Reference)
		}
	})
}

// TestLocalIngestDirEncryptedIsNotDeterministic records documented behaviour so
// it is not mistaken for a defect later. Every encrypted chunk takes a fresh
// random key, so two encryptions of one archive give different references by
// construction. Address equivalence cannot hold for an encrypted collection and
// the duplicate path can never fire for one.
func TestLocalIngestDirEncryptedIsNotDeterministic(t *testing.T) {
	t.Parallel()

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             mockstorer.New(),
		Logger:             log.Noop,
		Post:               mockpost.New(mockpost.WithAcceptAll()),
		LocalIngestEnabled: true,
	})

	ingest := func() api.LocalIngestResponse {
		var got api.LocalIngestResponse
		jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
			jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "true"),
			jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
			jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
			jsonhttptest.WithRequestHeader(api.SwarmEncryptHeader, "true"),
			jsonhttptest.WithRequestBody(tarFiles(t, dirIngestFiles(t))),
			jsonhttptest.WithUnmarshalJSONResponse(&got),
		)
		return got
	}

	first, second := ingest(), ingest()

	if first.Reference.Equal(second.Reference) {
		t.Fatal("two encrypted ingests of one archive gave the same reference; if this now holds, the encryption key is no longer per chunk and the spec needs revisiting")
	}
	if !second.SoleSource {
		t.Fatal("the second encrypted ingest reported itself as a duplicate, which cannot happen while every chunk takes a fresh key")
	}
	if l := len(first.Reference.Bytes()); l != swarm.HashSize*2 {
		t.Fatalf("encrypted reference is %d bytes, want %d", l, swarm.HashSize*2)
	}
}

// TestLocalIngestDirLimitMidStreamLeavesNoResidue. The declared-length
// pre-flight is skipped for a collection, so the mid-stream claim is the only
// enforcement there is and it has to both refuse and clean up. A refused
// collection that leaves its partial manifest behind would hold chunks the
// limit never counts again.
func TestLocalIngestDirLimitMidStreamLeavesNoResidue(t *testing.T) {
	t.Parallel()

	store := mockstorer.New()
	// Room for a couple of chunks only, so the archive cannot fit and the
	// claim binds while the body is being read.
	store.SetLocalIngestLimit(2)

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             store,
		Logger:             log.Noop,
		Post:               mockpost.New(mockpost.WithAcceptAll()),
		LocalIngestEnabled: true,
	})

	var resp api.LocalIngestFullResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusInsufficientStorage,
		jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "true"),
		jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
		jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
		jsonhttptest.WithRequestBody(tarFiles(t, dirIngestFiles(t))),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)

	// A session was created, so this got past the pre-flight, which a
	// collection skips, and was refused while the archive was being read.
	if n := store.LocalIngestSessionCount(); n != 1 {
		t.Fatalf("%d sessions were created, want 1", n)
	}

	committed, reserved, _ := store.LocalIngestUsage()
	if committed != 0 || reserved != 0 {
		t.Fatalf("a refused collection left committed=%d reserved=%d, want 0 and 0; a stranded claim disables the feature in silence",
			committed, reserved)
	}
}

// TestLocalIngestDirDuplicateAnswers200NotSoleSource. Re-ingesting a site the
// node already holds is not an error, but it is not a fresh sole-source copy
// either, and the second body has to say so. The case that matters to an
// operator is that ANY existing pin collection at that root shadows an ingest of
// it, including one left by a stamped upload.
func TestLocalIngestDirDuplicateAnswers200NotSoleSource(t *testing.T) {
	t.Parallel()

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             mockstorer.New(),
		Logger:             log.Noop,
		Post:               mockpost.New(mockpost.WithAcceptAll()),
		LocalIngestEnabled: true,
	})

	ingest := func(expect int) api.LocalIngestResponse {
		var got api.LocalIngestResponse
		jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, expect,
			jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "true"),
			jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
			jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
			jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "0"),
			jsonhttptest.WithRequestBody(tarFiles(t, dirIngestFiles(t))),
			jsonhttptest.WithUnmarshalJSONResponse(&got),
		)
		return got
	}

	first := ingest(http.StatusCreated)
	if !first.SoleSource {
		t.Fatal("the first collection ingest did not report itself as sole source")
	}

	second := ingest(http.StatusOK)
	if !second.Reference.Equal(first.Reference) {
		t.Fatalf("a repeat ingest of one archive gave %s, first gave %s", second.Reference, first.Reference)
	}
	if second.SoleSource {
		t.Fatal("a repeat ingest reported itself as sole source; the node already held this site")
	}
}
