// Copyright 2021 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"net/http"

	"github.com/ethersphere/bee/v2"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/topology"
)

type ReadyStatusResponse healthStatusResponse

// hasConnectedPeer reports whether the node has at least one connected peer.
//
// Iterates and stops at the first hit rather than calling Snapshot, which walks
// every bin and allocates. Readiness is polled frequently, so this must stay
// cheap.
func (s *Service) hasConnectedPeer() bool {
	if s.topologyDriver == nil {
		// Not wired in every configuration (some tests, and the dev node).
		// Fall back to the probe alone rather than reporting a node unready
		// because we cannot see its peers.
		return true
	}

	found := false
	_ = s.topologyDriver.EachConnectedPeer(
		func(swarm.Address, uint8) (bool, bool, error) {
			found = true
			return true, false, nil // stop on the first peer
		},
		topology.Select{IncludeBootnodes: true},
	)
	return found
}

func (s *Service) readinessHandler(w http.ResponseWriter, _ *http.Request) {
	// A node with no connected peers cannot serve or retrieve anything. Before
	// this check it still reported "ready", which meant an operator or an
	// orchestrator polling readiness saw a healthy node while it was in fact
	// completely isolated — see issue #74, where a node sat at zero peers for
	// 65 minutes reporting ready the whole time.
	//
	// The probe alone is not sufficient: it only tracks startup and warmup, and
	// is never lowered when connectivity is lost afterwards.
	if s.probe.Ready() == ProbeStatusOK && s.hasConnectedPeer() {
		jsonhttp.OK(w, ReadyStatusResponse{
			Status:     "ready",
			Version:    bee.Version,
			APIVersion: Version,
		})
	} else {
		jsonhttp.BadRequest(w, ReadyStatusResponse{
			Status:     "notReady",
			Version:    bee.Version,
			APIVersion: Version,
		})
	}
}
