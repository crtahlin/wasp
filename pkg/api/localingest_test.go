// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/api"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp/jsonhttptest"
	"github.com/ethersphere/bee/v2/pkg/log"
	mockpost "github.com/ethersphere/bee/v2/pkg/postage/mock"
	mockstorer "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"gitlab.com/nolash/go-mockbytes"
)

const localIngestResource = "/wasp/ingest"

func localIngestContent(t *testing.T, size int) []byte {
	t.Helper()

	g := mockbytes.New(0, mockbytes.MockTypeStandard).WithModulus(255)
	content, err := g.SequentialBytes(size)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// TestLocalIngestDisabled. With no authentication layer to put this endpoint
// behind, the flag is the only control there is, so a node that has not
// enabled it must refuse every request.
func TestLocalIngestDisabled(t *testing.T) {
	t.Parallel()

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer: mockstorer.New(),
		Logger: log.Noop,
	})

	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusForbidden,
		jsonhttptest.WithRequestBody(bytes.NewReader(localIngestContent(t, swarm.ChunkSize))),
		jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
			Message: "local ingest is off; start the node with local-ingest-enable",
			Code:    http.StatusForbidden,
		}),
	)
}

// TestLocalIngestAddressEquivalence is the acceptance test of issue #326: the
// same bytes at the same redundancy level must give the same reference whether
// they were paid for or not. If they did not, content ingested this way would
// not be the content anyone else asks for.
func TestLocalIngestAddressEquivalence(t *testing.T) {
	t.Parallel()

	for _, level := range []string{"0", "1", "4"} {
		t.Run("level_"+level, func(t *testing.T) {
			t.Parallel()

			client, _, _, _ := newTestServer(t, testServerOptions{
				Storer:             mockstorer.New(),
				Logger:             log.Noop,
				Post:               mockpost.New(mockpost.WithAcceptAll()),
				LocalIngestEnabled: true,
			})

			content := localIngestContent(t, swarm.ChunkSize*3)

			var ingested api.LocalIngestResponse
			jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
				jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, level),
				jsonhttptest.WithRequestBody(bytes.NewReader(content)),
				jsonhttptest.WithUnmarshalJSONResponse(&ingested),
			)

			var uploaded api.BytesPostResponse
			jsonhttptest.Request(t, client, http.MethodPost, "/bytes", http.StatusCreated,
				jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
				jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
				jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, level),
				jsonhttptest.WithRequestBody(bytes.NewReader(content)),
				jsonhttptest.WithUnmarshalJSONResponse(&uploaded),
			)

			if ingested.Reference.IsZero() {
				t.Fatal("the ingest returned no reference")
			}
			if !ingested.Reference.Equal(uploaded.Reference) {
				t.Fatalf("ingest gave %s, a stamped upload of the same bytes gave %s; content ingested this way would not be the content anyone asks for",
					ingested.Reference, uploaded.Reference)
			}
			if !ingested.SoleSource {
				t.Fatal("a fresh ingest did not report itself as sole source")
			}
			if ingested.Chunks == 0 {
				t.Fatal("the ingest reported storing no chunks")
			}
		})
	}
}

// TestLocalIngestUsesUploadRedundancyDefault. Pinning defaults to
// DefaultDownloadLevel, and this handler is otherwise a copy of the pinning
// path, so taking that default by accident is the easy mistake. It would break
// address equivalence for a reason that has nothing to do with the feature,
// and a reviewer would read that as a design failure.
func TestLocalIngestUsesUploadRedundancyDefault(t *testing.T) {
	t.Parallel()

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             mockstorer.New(),
		Logger:             log.Noop,
		Post:               mockpost.New(mockpost.WithAcceptAll()),
		LocalIngestEnabled: true,
	})

	content := localIngestContent(t, swarm.ChunkSize*3)

	var noHeader api.LocalIngestResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
		jsonhttptest.WithRequestBody(bytes.NewReader(content)),
		jsonhttptest.WithUnmarshalJSONResponse(&noHeader),
	)

	// The same bytes through POST /bytes with no level header either. That
	// route documents DefaultUploadLevel, so the two references agree only
	// if this handler took the same default.
	var upload api.BytesPostResponse
	jsonhttptest.Request(t, client, http.MethodPost, "/bytes", http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
		jsonhttptest.WithRequestBody(bytes.NewReader(content)),
		jsonhttptest.WithUnmarshalJSONResponse(&upload),
	)

	if !noHeader.Reference.Equal(upload.Reference) {
		t.Fatalf("with no redundancy header the ingest gave %s and POST /bytes gave %s, so the two routes do not share a default",
			noHeader.Reference, upload.Reference)
	}
}

// TestLocalIngestDuplicate. The reference is not known until the body has been
// split, so the duplicate cannot be pre-checked the way pinning does it. It
// surfaces from Done, and the handler must answer 200 rather than 500.
func TestLocalIngestDuplicate(t *testing.T) {
	t.Parallel()

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             mockstorer.New(),
		Logger:             log.Noop,
		LocalIngestEnabled: true,
	})

	content := localIngestContent(t, swarm.ChunkSize*2)

	var first api.LocalIngestResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
		jsonhttptest.WithRequestBody(bytes.NewReader(content)),
		jsonhttptest.WithUnmarshalJSONResponse(&first),
	)

	var second api.LocalIngestResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusOK,
		jsonhttptest.WithRequestBody(bytes.NewReader(content)),
		jsonhttptest.WithUnmarshalJSONResponse(&second),
	)

	if !second.Reference.Equal(first.Reference) {
		t.Fatalf("re-ingesting the same bytes gave %s, want %s", second.Reference, first.Reference)
	}
	if second.SoleSource {
		t.Fatal("a re-ingest reported itself sole source, which says nothing true about content the node already held")
	}
}

// TestLocalIngestLimitPreflight refuses on a declared length, before the body
// is read. It is a convenience rather than the enforcement.
func TestLocalIngestLimitPreflight(t *testing.T) {
	t.Parallel()

	storerMock := mockstorer.New()
	storerMock.SetLocalIngestLimit(1)

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             storerMock,
		Logger:             log.Noop,
		LocalIngestEnabled: true,
	})

	var resp api.LocalIngestFullResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusInsufficientStorage,
		jsonhttptest.WithRequestBody(bytes.NewReader(localIngestContent(t, swarm.ChunkSize*8))),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)

	if resp.Limit != 1 {
		t.Fatalf("the refusal reported a limit of %d, want 1; an operator cannot act on a 507 that does not say what the limit is", resp.Limit)
	}

	committed, reserved, _ := storerMock.LocalIngestUsage()
	if committed != 0 || reserved != 0 {
		t.Fatalf("a refused ingest left committed=%d reserved=%d, want 0 and 0", committed, reserved)
	}

	// No session at all is what makes this the pre-flight path rather than
	// the mid-stream one, which the test below covers.
	if n := storerMock.LocalIngestSessionCount(); n != 0 {
		t.Fatalf("%d sessions were created for an ingest whose declared length already exceeded the limit, so the body was read before it was refused", n)
	}
}

// TestLocalIngestLimitMidStream is the enforcement proper. Content-Length is
// absent under chunked transfer encoding and client-supplied in any case, so
// the limit has to bind while the body is being read. The limit here is set to
// exactly what the declared length predicts, which the parity chunks that
// redundancy adds then exceed.
func TestLocalIngestLimitMidStream(t *testing.T) {
	t.Parallel()

	storerMock := mockstorer.New()
	storerMock.SetLocalIngestLimit(uint64(api.CalculateNumberOfChunks(swarm.ChunkSize*8, false)))

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             storerMock,
		Logger:             log.Noop,
		LocalIngestEnabled: true,
	})

	var resp api.LocalIngestFullResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusInsufficientStorage,
		jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "4"),
		jsonhttptest.WithRequestBody(bytes.NewReader(localIngestContent(t, swarm.ChunkSize*8))),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)

	// A session was created, so the declared length did not refuse this and
	// the limit bound while the body was being read.
	if n := storerMock.LocalIngestSessionCount(); n != 1 {
		t.Fatalf("%d sessions were created, want 1; this test is meant to get past the declared-length check and be refused mid-stream", n)
	}

	// The partial collection must be gone, and the claim with it. This is
	// the assertion that catches a leak no limit would ever count.
	committed, reserved, _ := storerMock.LocalIngestUsage()
	if committed != 0 || reserved != 0 {
		t.Fatalf("a refused ingest left committed=%d reserved=%d, want 0 and 0; a stranded claim disables the feature in silence", committed, reserved)
	}
}

// TestLocalIngestLimitCrossedInsideSum is the case the out-of-band report
// exists for, and the only one that distinguishes it from errors.Is.
//
// The dispersed root replicas are stored inside hashtrie.Sum, after the whole
// body has been read, and Sum formats that failure with %s against err.Error()
// rather than %w (pkg/file/pipeline/hashtrie/hashtrie.go:267). The chain is
// discarded, so errors.Is returns false and a handler relying on it would
// answer 500. The limit is calibrated here so that it is crossed by exactly
// that put. See issue #337.
func TestLocalIngestLimitCrossedInsideSum(t *testing.T) {
	t.Parallel()

	content := localIngestContent(t, swarm.ChunkSize*16)

	// First find how many distinct chunks this content needs in full,
	// replicas included, rather than hard-coding a number that redundancy
	// changes could silently invalidate.
	unlimited := mockstorer.New()
	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             unlimited,
		Logger:             log.Noop,
		LocalIngestEnabled: true,
	})

	var full api.LocalIngestResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "4"),
		jsonhttptest.WithRequestBody(bytes.NewReader(content)),
		jsonhttptest.WithUnmarshalJSONResponse(&full),
	)
	if full.Chunks < 2 {
		t.Fatalf("calibration ingest stored %d chunks, too few to leave room for this test", full.Chunks)
	}

	// One short, so everything fits until the last replica put inside Sum.
	limited := mockstorer.New()
	limited.SetLocalIngestLimit(full.Chunks - 1)

	client2, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             limited,
		Logger:             log.Noop,
		LocalIngestEnabled: true,
	})

	var resp api.LocalIngestFullResponse
	jsonhttptest.Request(t, client2, http.MethodPost, localIngestResource, http.StatusInsufficientStorage,
		jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "4"),
		jsonhttptest.WithRequestBody(bytes.NewReader(content)),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)

	if n := limited.LocalIngestSessionCount(); n != 1 {
		t.Fatalf("%d sessions created, want 1; the declared-length check refused this before the body was read", n)
	}
	if resp.Limit != full.Chunks-1 {
		t.Fatalf("the refusal reported a limit of %d, want %d", resp.Limit, full.Chunks-1)
	}

	committed, reserved, _ := limited.LocalIngestUsage()
	if committed != 0 || reserved != 0 {
		t.Fatalf("a refused ingest left committed=%d reserved=%d, want 0 and 0", committed, reserved)
	}
}

// TestLocalIngestConcurrentPuts. The replicas putter calls Put from several
// goroutines when the dispersed root replicas are stored, so the counting
// wrapper must be safe for concurrent use. Run with -race, this is the test
// that says so.
func TestLocalIngestConcurrentPuts(t *testing.T) {
	t.Parallel()

	storerMock := mockstorer.New()

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:             storerMock,
		Logger:             log.Noop,
		LocalIngestEnabled: true,
	})

	var resp api.LocalIngestResponse
	jsonhttptest.Request(t, client, http.MethodPost, localIngestResource, http.StatusCreated,
		jsonhttptest.WithRequestHeader(api.SwarmRedundancyLevelHeader, "4"),
		jsonhttptest.WithRequestBody(bytes.NewReader(localIngestContent(t, swarm.ChunkSize*16))),
		jsonhttptest.WithUnmarshalJSONResponse(&resp),
	)

	committed, reserved, _ := storerMock.LocalIngestUsage()
	if committed != resp.Chunks {
		t.Fatalf("the response reported %d chunks and the node recorded %d; the two must be one quantity", resp.Chunks, committed)
	}
	if reserved != 0 {
		t.Fatalf("%d chunks are still claimed after the ingest committed", reserved)
	}
}
