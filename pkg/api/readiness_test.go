// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"net/http"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/api"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp/jsonhttptest"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	topologymock "github.com/ethersphere/bee/v2/pkg/topology/mock"
)

func TestReadiness(t *testing.T) {
	t.Parallel()

	t.Run("probe not set", func(t *testing.T) {
		t.Parallel()

		testServer, _, _, _ := newTestServer(t, testServerOptions{})

		// When probe is not set readiness endpoint should indicate that API is not ready
		jsonhttptest.Request(t, testServer, http.MethodGet, "/readiness", http.StatusBadRequest)
	})

	t.Run("readiness probe status change", func(t *testing.T) {
		t.Parallel()

		probe := api.NewProbe()
		// Readiness now also requires a connected peer, so give the node one.
		// The zero-peer case is covered separately below.
		testServer, _, _, _ := newTestServer(t, testServerOptions{
			Probe:        probe,
			TopologyOpts: []topologymock.Option{topologymock.WithPeers(swarm.RandAddress(t))},
		})

		// Current readiness probe is pending which should indicate that API is not ready
		jsonhttptest.Request(t, testServer, http.MethodGet, "/readiness", http.StatusBadRequest)

		// When we set readiness probe to OK it should indicate that API is ready
		probe.SetReady(api.ProbeStatusOK)
		jsonhttptest.Request(t, testServer, http.MethodGet, "/readiness", http.StatusOK,
			jsonhttptest.WithExpectedJSONResponse(api.ReadyStatusResponse{
				Status:     "ready",
				Version:    "-dev",
				APIVersion: "0.0.0",
			}))

		// When we set readiness probe to NOK it should indicate that API is not ready
		probe.SetReady(api.ProbeStatusNOK)
		jsonhttptest.Request(t, testServer, http.MethodGet, "/readiness", http.StatusBadRequest,
			jsonhttptest.WithExpectedJSONResponse(api.ReadyStatusResponse{
				Status:     "notReady",
				Version:    "-dev",
				APIVersion: "0.0.0",
			}))
	})

	t.Run("ready probe but no peers", func(t *testing.T) {
		t.Parallel()

		probe := api.NewProbe()
		probe.SetReady(api.ProbeStatusOK)

		// No peers configured on the topology driver.
		testServer, _, _, _ := newTestServer(t, testServerOptions{
			Probe: probe,
		})

		// A node with no connected peers cannot serve or retrieve anything, so
		// it must not advertise itself as ready even though startup completed.
		// Reporting ready here is what let a node sit isolated for 65 minutes
		// with every health signal green — see issue #74.
		jsonhttptest.Request(t, testServer, http.MethodGet, "/readiness", http.StatusBadRequest,
			jsonhttptest.WithExpectedJSONResponse(api.ReadyStatusResponse{
				Status:     "notReady",
				Version:    "-dev",
				APIVersion: "0.0.0",
			}))
	})

	t.Run("recovers when a peer connects", func(t *testing.T) {
		t.Parallel()

		probe := api.NewProbe()
		probe.SetReady(api.ProbeStatusOK)

		testServer, _, _, _ := newTestServer(t, testServerOptions{
			Probe:        probe,
			TopologyOpts: []topologymock.Option{topologymock.WithPeers(swarm.RandAddress(t))},
		})

		jsonhttptest.Request(t, testServer, http.MethodGet, "/readiness", http.StatusOK,
			jsonhttptest.WithExpectedJSONResponse(api.ReadyStatusResponse{
				Status:     "ready",
				Version:    "-dev",
				APIVersion: "0.0.0",
			}))
	})
}
