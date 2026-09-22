// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp/jsonhttptest"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/inmemchunkstore"
	mockstorer "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/topology"
)

// failingChunkStore answers every Get with one chosen error, so a test can say
// what the chunk endpoint does with each way retrieval gives up.
type failingChunkStore struct {
	storage.ChunkStore
	err error
}

func (f failingChunkStore) Get(context.Context, swarm.Address) (swarm.Chunk, error) {
	return nil, f.err
}

func chunkServerFailingWith(t *testing.T, err error) *http.Client {
	t.Helper()

	st := mockstorer.NewWithChunkStore(failingChunkStore{
		ChunkStore: inmemchunkstore.New(),
		err:        err,
	})
	client, _, _, _ := newTestServer(t, testServerOptions{Storer: st})
	return client
}

// TestChunkNotFoundStatus is wasp #440.
//
// Retrieval has two ways of giving up and they return different errors.
// Exhausting the peer walk returns topology.ErrNotFound, "no peer found";
// spending the origin error budget returns storage.ErrNotFound. netstore passes
// either through unwrapped.
//
// A depleted peer walk is a statement about the network, not about this node
// malfunctioning, so it belongs with the other 404s. /bzz has always mapped
// both; this endpoint mapped only storage.ErrNotFound and answered 500 for the
// other, so a caller talking to both had to special-case it.
//
// The third case is the guard: a fix that simply answers 404 for everything
// would pass the first two and hide real faults.
func TestChunkNotFoundStatus(t *testing.T) {
	t.Parallel()

	addr := swarm.MustParseHexAddress("aabbcc")

	t.Run("peer walk depleted gives 404", func(t *testing.T) {
		t.Parallel()

		jsonhttptest.Request(t, chunkServerFailingWith(t, topology.ErrNotFound),
			http.MethodGet, "/chunks/"+addr.String(), http.StatusNotFound,
			jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
				Message: "chunk not found",
				Code:    http.StatusNotFound,
			}),
		)
	})

	t.Run("chunk not found gives 404", func(t *testing.T) {
		t.Parallel()

		jsonhttptest.Request(t, chunkServerFailingWith(t, storage.ErrNotFound),
			http.MethodGet, "/chunks/"+addr.String(), http.StatusNotFound,
			jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
				Message: "chunk not found",
				Code:    http.StatusNotFound,
			}),
		)
	})

	t.Run("an unrelated failure still gives 500", func(t *testing.T) {
		t.Parallel()

		jsonhttptest.Request(t, chunkServerFailingWith(t, errors.New("disk on fire")),
			http.MethodGet, "/chunks/"+addr.String(), http.StatusInternalServerError,
			jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
				Message: "read chunk failed",
				Code:    http.StatusInternalServerError,
			}),
		)
	})
}
