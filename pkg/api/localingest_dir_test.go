// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
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
//
// Swarm-Error-Document is set on both sides deliberately. It reaches the root
// manifest entry just as the index document does, so sending it here is what
// makes a handler that silently drops it fail: without it the header was
// honoured in the code and asserted by nothing.
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
				jsonhttptest.WithRequestHeader(api.SwarmErrorDocumentHeader, "index.html"),
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
				jsonhttptest.WithRequestHeader(api.SwarmErrorDocumentHeader, "index.html"),
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

	// The fixture's files are 1 + 1 + 4 chunks, so a putter that counted file
	// chunks and skipped every manifest node would report 6. Comparing against
	// the file count of 3 would let that through, which is how a first version
	// of this assertion could not detect the defect it names.
	const fileChunksAlone = 6
	if ingested.Chunks <= fileChunksAlone {
		t.Fatalf("reported %d chunks, but the files alone are %d: the manifest nodes are not being counted",
			ingested.Chunks, fileChunksAlone)
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

		body, boundary := multipartFiles(t, dirIngestFiles(t))
		raw := body.Bytes()

		// multipartFiles returns the BOUNDARY, not a content type. Sending it
		// raw made this subtest send a header mime.ParseMediaType does not
		// resolve to multipart/form-data, so it passed without ever exercising
		// the multipart branch it exists to guard. Found by mutation.
		contentType := fmt.Sprintf("multipart/form-data; boundary=%q", boundary)

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

	// Every assertion above also holds for an encrypted BLOB, so without this
	// the test would pass with the collection feature removed entirely. A
	// manifest is what makes an inner path resolvable.
	jsonhttptest.Request(t, client, http.MethodGet,
		"/bzz/"+first.Reference.String()+"/css/style.css", http.StatusOK,
		jsonhttptest.WithExpectedResponse([]byte("body{}")),
	)
}

// TestLocalIngestDirLimitMidStreamAnswers507. The declared-length
// pre-flight is skipped for a collection, so the mid-stream claim is the only
// enforcement there is and it has to both refuse and clean up. A refused
// collection that leaves its claim behind would disable the feature in silence.
//
// Named for the status and the claim, not for residue: mockstorer's Cleanup
// releases the claim and removes no chunks, so chunk residue cannot be asserted
// here at all. That belongs to package storer_test, against a real database.
func TestLocalIngestDirLimitMidStreamAnswers507(t *testing.T) {
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

// TestLocalIngestDirServesIndexDocument is the test that asks whether the
// feature does what the issue wants: hosting a site. Every other test here
// compares a hash or a count, and a manifest that is byte-identical to a stamped
// one but does not resolve would satisfy all of them.
//
// The spec named this test and a first version of this change shipped without
// it, which is the gap worth remembering: the assertions that are easy to write
// are the ones that do not need the feature to work.
func TestLocalIngestDirServesIndexDocument(t *testing.T) {
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
		jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "0"),
		jsonhttptest.WithRequestBody(tarFiles(t, dirIngestFiles(t))),
		jsonhttptest.WithUnmarshalJSONResponse(&ingested),
	)

	root := ingested.Reference.String()

	// The bare root resolves through the index document.
	jsonhttptest.Request(t, client, http.MethodGet, "/bzz/"+root+"/", http.StatusOK,
		jsonhttptest.WithExpectedResponse([]byte("<h1>index</h1>")),
	)

	// And a path inside the collection resolves on its own.
	jsonhttptest.Request(t, client, http.MethodGet, "/bzz/"+root+"/css/style.css", http.StatusOK,
		jsonhttptest.WithExpectedResponse([]byte("body{}")),
	)
}

// TestLocalIngestDirWithoutIndexDocument. Without a root index document the bare
// root answers 404 even with every chunk held locally, which is why the handler
// warns. The inner paths still resolve, so the site is reachable by full path and
// the warning is the right response rather than a refusal.
func TestLocalIngestDirWithoutIndexDocument(t *testing.T) {
	t.Parallel()

	// The warning is asserted, not just the 404. It is the only thing that tells
	// an operator their site will not answer at its own root, so a handler that
	// stopped emitting it would leave them to find out from a browser.
	sink := &syncBuffer{}
	logger := log.NewLogger("localingest_dir_noindex", log.WithSink(sink), log.WithVerbosity(log.VerbosityDebug)).Build()

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             mockstorer.New(),
		Logger:             logger,
		Post:               mockpost.New(mockpost.WithAcceptAll()),
		LocalIngestEnabled: true,
	})

	var ingested api.LocalIngestResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "true"),
		jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
		jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "0"),
		jsonhttptest.WithRequestBody(tarFiles(t, dirIngestFiles(t))),
		jsonhttptest.WithUnmarshalJSONResponse(&ingested),
	)

	root := ingested.Reference.String()

	jsonhttptest.Request(t, client, http.MethodGet, "/bzz/"+root+"/", http.StatusNotFound)

	jsonhttptest.Request(t, client, http.MethodGet, "/bzz/"+root+"/css/style.css", http.StatusOK,
		jsonhttptest.WithExpectedResponse([]byte("body{}")),
	)

	if !strings.Contains(sink.String(), "no index document") {
		t.Fatalf("a collection ingested without an index document logged no warning about it; the log was:\n%s", sink.String())
	}
}

// tarThenGarbage is a tar whose first entry is valid and which then stops being
// a tar, without the two zero blocks that would end it properly.
//
// The shape matters. A failure before any chunk is written reserves nothing, so
// there is no claim to leak and no assertion can tell whether the collection was
// released. This archive stores one file first, so a claim exists by the time the
// archive goes bad, which is what makes the release observable.
func tarThenGarbage(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	data := localIngestContent(t, swarm.ChunkSize*2)
	if err := tw.WriteHeader(&tar.Header{
		Name: "first.bin",
		Mode: 0o600,
		Size: int64(len(data)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	// Flush, not Close: Close would append the end-of-archive blocks and the
	// reader would stop cleanly before reaching the garbage.
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	buf.Write(bytes.Repeat([]byte("x"), 2048))
	return &buf
}

// tarTruncated is a body shorter than one 512-byte tar header block, which
// archive/tar reports as an unexpected EOF rather than an invalid header.
func tarTruncated(t *testing.T) *bytes.Buffer {
	t.Helper()
	return bytes.NewBuffer([]byte("this is not a tar archive"))
}

// tarInvalidHeader is a full-size block of nonsense, which archive/tar reports
// as an invalid header. The distinction from tarTruncated is what makes these
// two separate cases rather than one.
func tarInvalidHeader(t *testing.T) *bytes.Buffer {
	t.Helper()
	return bytes.NewBuffer(bytes.Repeat([]byte("x"), 2048))
}

// tarEmpty wraps the shared helper so every case in the table is a named
// function and none is a closure taking *testing.T.
func tarEmpty(t *testing.T) *bytes.Buffer {
	t.Helper()
	return tarEmptyDir(t)
}

// TestLocalIngestDirRejectsMalformedArchive. Both of these are the caller's
// fault, so both are 400 rather than 500, and both have to release the
// collection on the way out.
//
// The release is the point. The spec names a leaked collection as one of the two
// main risks of this change, and a first version of these branches had no test
// at all: answering through the plain writer instead of the cleanup one left
// every test green.
func TestLocalIngestDirRejectsMalformedArchive(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body func(t *testing.T) *bytes.Buffer
	}{
		{"empty archive", tarEmpty},
		// The two garbage cases are not the same case. archive/tar reports a
		// body shorter than one 512-byte header block as an unexpected EOF, and
		// a full-size block of nonsense as an invalid header, so each reaches a
		// different branch of the handler's switch. A first version of this test
		// used only the short one and left the other branch untested.
		{"truncated", tarTruncated},
		{"invalid header", tarInvalidHeader},
		// The only case that can detect a leaked collection, because it is the
		// only one where chunks are stored before the failure.
		{"corrupt after one file", tarThenGarbage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := mockstorer.New()
			client, _, _, _ := newTestServer(t, testServerOptions{
				Storer:             store,
				Logger:             log.Noop,
				Post:               mockpost.New(mockpost.WithAcceptAll()),
				LocalIngestEnabled: true,
			})

			jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusBadRequest,
				jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "true"),
				jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
				jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
				jsonhttptest.WithRequestBody(tc.body(t)),
			)

			// The collection must be released. Answering a 400 through the
			// plain writer rather than the cleanup one leaves the claim behind
			// and nothing ever counts it again.
			//
			// Only the last case can fail here: the others stop before a chunk
			// is written, so they reserve nothing and would report zero whether
			// the collection was released or not.
			committed, reserved, _ := store.LocalIngestUsage()
			if committed != 0 || reserved != 0 {
				t.Fatalf("a refused collection left committed=%d reserved=%d, want 0 and 0", committed, reserved)
			}
		})
	}
}
