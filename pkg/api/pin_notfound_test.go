// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
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

func pinServerFailingWith(t *testing.T, err error) *http.Client {
	t.Helper()

	st := mockstorer.NewWithChunkStore(failingChunkStore{
		ChunkStore: inmemchunkstore.New(),
		err:        err,
	})
	client, _, _, _ := newTestServer(t, testServerOptions{Storer: st})
	return client
}

// TestPinNotFoundStatus is wasp #449, which is #440 on a second endpoint.
//
// The pin handler traverses the reference through the network getter, and
// traversal wraps a failed fetch with %w, so both ways retrieval gives up
// survive to the handler. It mapped only storage.ErrNotFound, so a depleted
// peer walk answered 500 while a spent error budget answered 404.
//
// The reference must be a full length address. traversal only fetches when
// addr.IsValidLength(), so a short one never reaches the getter and fails
// earlier with "storage: invalid reference length", which the handler answers
// 500. That would fail the two 404 cases rather than passing them for the
// wrong reason, and would leave only the 500 guard passing vacuously.
func TestPinNotFoundStatus(t *testing.T) {
	t.Parallel()

	addr := swarm.RandAddress(t)

	t.Run("peer walk depleted gives 404", func(t *testing.T) {
		t.Parallel()

		jsonhttptest.Request(t, pinServerFailingWith(t, topology.ErrNotFound),
			http.MethodPost, "/pins/"+addr.String(), http.StatusNotFound,
			jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
				Message: "pin collection failed",
				Code:    http.StatusNotFound,
			}),
		)
	})

	t.Run("chunk not found gives 404", func(t *testing.T) {
		t.Parallel()

		jsonhttptest.Request(t, pinServerFailingWith(t, storage.ErrNotFound),
			http.MethodPost, "/pins/"+addr.String(), http.StatusNotFound,
			jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
				Message: "pin collection failed",
				Code:    http.StatusNotFound,
			}),
		)
	})

	t.Run("an unrelated failure still gives 500", func(t *testing.T) {
		t.Parallel()

		jsonhttptest.Request(t, pinServerFailingWith(t, errors.New("disk on fire")),
			http.MethodPost, "/pins/"+addr.String(), http.StatusInternalServerError,
			jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
				Message: "pin collection failed",
				Code:    http.StatusInternalServerError,
			}),
		)
	})
}
