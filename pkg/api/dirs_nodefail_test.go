// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/api"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp/jsonhttptest"
	mockpost "github.com/ethersphere/bee/v2/pkg/postage/mock"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/inmemchunkstore"
	mockstorer "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// failingPutChunkStore refuses every write, so a test can drive a failure that
// is the node's and not the caller's. The sibling in chunk_notfound_test.go
// fails reads instead, which is why this one exists rather than growing a
// second mode onto it.
type failingPutChunkStore struct {
	storage.ChunkStore
	err error
}

func (f failingPutChunkStore) Put(context.Context, swarm.Chunk) error {
	return f.err
}

// TestDirsNodeSideFailureAnswers500 is the standing guard for wasp #455.
//
// That change gives multipartReader.Next a sentinel and answers 400 for
// anything carrying it. The sentinel is sound only because Next calls nothing
// but NextPart, so every error it can return is the caller's. If it were ever
// attached higher up, at storeDir's own "read dir stream" or "store dir file"
// wrap, it would start claiming the node's failures as the caller's too, and
// this node would answer 400 for its own broken disk.
//
// None of the mutations in the spec's list moves the sentinel, so nothing
// there makes this test fail, and the spec says so rather than pretending it
// is mutation checked. Attaching the sentinel at either of those wraps is the
// mutation it does catch.
func TestDirsNodeSideFailureAnswers500(t *testing.T) {
	t.Parallel()

	store := mockstorer.NewWithChunkStore(failingPutChunkStore{
		ChunkStore: inmemchunkstore.New(),
		err:        errors.New("the node cannot write"),
	})

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:          store,
		PreventRedirect: true,
		Post:            mockpost.New(mockpost.WithAcceptAll()),
	})

	// A well-formed body, so nothing about the request is the caller's fault
	// and only the node's failure can decide the answer.
	complete, boundary := multipartFiles(t, []f{{
		data: []byte("<h1>Swarm"),
		name: "index.html",
	}})

	jsonhttptest.Request(t, client, http.MethodPost, "/bzz", http.StatusInternalServerError,
		jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
		jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "True"),
		jsonhttptest.WithRequestHeader(api.ContentTypeHeader, "multipart/form-data; boundary="+boundary),
		jsonhttptest.WithRequestBody(bytes.NewReader(complete.Bytes())),
		jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
			Message: api.ErrDirectoryStoreError.Error(),
			Code:    http.StatusInternalServerError,
		}),
	)
}
