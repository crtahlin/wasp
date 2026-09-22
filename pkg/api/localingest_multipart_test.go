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
)

// TestLocalIngestMultipartMalformed is wasp #424 on the second caller of
// storeDir.
//
// storeDir has two callers and each builds its own multipart reader and carries
// its own copy of the error switch. #366 was merged having fixed only /bzz, and
// this route was found answering 500 for a request the other route answered
// 400. Testing both is the lesson from that, not a formality.
//
// Both bodies here are the caller's mistake. Neither error can be matched by
// identity: mime/multipart builds the boundary error with fmt.Errorf and never
// exports it, and a malformed part header is a textproto.ProtocolError whose
// text carries the offending line.
func TestLocalIngestMultipartMalformed(t *testing.T) {
	t.Parallel()

	_, boundary := multipartFiles(t, []f{{
		data: []byte("<h1>Swarm"),
		name: "index.html",
	}})
	complete, _ := multipartFiles(t, []f{{
		data: []byte("<h1>Swarm"),
		name: "index.html",
	}})

	// A valid first part, then a malformed header. Only a body that stores
	// something before failing can detect a leaked collection, which is the
	// caveat TestLocalIngestDirRejectsMalformedArchive records next to its own
	// last case. Without this one the usage assertion below cannot fail: both
	// bodies above stop on the FIRST part, so nothing is ever reserved and the
	// usage reads zero whether the collection was released or not.
	afterOneFile := []byte("--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"f\"; filename=\"index.html\"\r\n" +
		"Content-Type: text/html\r\n\r\n" +
		"<h1>Swarm\r\n" +
		"--" + boundary + "\r\nnot a header line\r\n\r\nbody\r\n" +
		"--" + boundary + "--\r\n")

	for _, tc := range []struct {
		name        string
		body        []byte
		contentType string
		message     string
	}{
		{
			name:        "content type declares no boundary",
			body:        complete.Bytes(),
			contentType: "multipart/form-data",
			message:     api.ErrNoBoundary.Error(),
		},
		{
			name:        "part header line without a colon",
			body:        []byte("--" + boundary + "\r\nnot a header line\r\n\r\nbody\r\n--" + boundary + "--\r\n"),
			contentType: "multipart/form-data; boundary=" + boundary,
			message:     "malformed multipart header",
		},
		{
			name:        "malformed header after one stored file",
			body:        afterOneFile,
			contentType: "multipart/form-data; boundary=" + boundary,
			message:     "malformed multipart header",
		},
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
				jsonhttptest.WithRequestHeader(api.ContentTypeHeader, tc.contentType),
				jsonhttptest.WithRequestBody(bytes.NewReader(tc.body)),
				jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
					Message: tc.message,
					Code:    http.StatusBadRequest,
				}),
			)

			// A refused collection must be released, for the reason
			// TestLocalIngestDirRejectsMalformedArchive records: answering
			// through the plain writer rather than the cleanup one leaves the
			// claim behind and nothing counts it again.
			committed, reserved, _ := store.LocalIngestUsage()
			if committed != 0 || reserved != 0 {
				t.Fatalf("a refused collection left committed=%d reserved=%d, want 0 and 0", committed, reserved)
			}
		})
	}
}
