// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package salud_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/salud"
	"github.com/ethersphere/bee/v2/pkg/spinlock"
	stabilmock "github.com/ethersphere/bee/v2/pkg/stabilization/mock"
	"github.com/ethersphere/bee/v2/pkg/status"
	"github.com/ethersphere/bee/v2/pkg/storer"
	mockstorer "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	topMock "github.com/ethersphere/bee/v2/pkg/topology/mock"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func depthPeer(t *testing.T, mode string, depth uint32) peer {
	t.Helper()
	return peer{swarm.RandAddress(t), &status.Snapshot{ConnectedPeers: 100, StorageRadius: depth, BeeMode: mode, BatchCommitment: 50, ReserveSize: 100, CommittedDepth: depth}, 0, true}
}

func gauge(t *testing.T, cs []prometheus.Collector, name string) float64 {
	t.Helper()
	for _, c := range cs {
		g, ok := c.(prometheus.Gauge)
		if ok && strings.Contains(g.Desc().String(), `"bee_salud_`+name+`"`) {
			var m dto.Metric
			if err := g.Write(&m); err != nil {
				t.Fatal(err)
			}
			return m.GetGauge().GetValue()
		}
	}
	t.Fatalf("gauge %s not found", name)
	return 0
}

// TestCommittedDepths checks the reading the reserve worker takes its floor
// from (wasp #822): the most common depths, the peers behind them, and the
// time of the pass.
func TestCommittedDepths(t *testing.T) {
	t.Parallel()

	// The mock reserve counts every peer as a neighbour. The light node
	// does not answer in full mode and is not counted.
	peers := []peer{
		depthPeer(t, "full", 10), depthPeer(t, "full", 10), depthPeer(t, "full", 10), depthPeer(t, "full", 10),
		depthPeer(t, "full", 9),
		depthPeer(t, "light", 9),
	}
	statusM := &statusMock{make(map[string]peer)}
	addrs := make([]swarm.Address, 0, len(peers))
	for _, p := range peers {
		addrs = append(addrs, p.addr)
		statusM.peers[p.addr.ByteString()] = p
	}
	topM := topMock.NewTopologyDriver(topMock.WithPeers(addrs...))
	reserve := mockstorer.NewReserve(mockstorer.WithRadius(9), mockstorer.WithCapacityDoubling(1))

	before := time.Now()
	service := salud.New(statusM, topM, reserve, log.Noop, stabilmock.NewSubscriber(true), "full", 0.8, 0.8)
	testutil.CleanupCloser(t, service)

	var (
		r  storer.DepthReading
		ok bool
	)
	if err := spinlock.Wait(time.Minute, func() bool { r, ok = service.CommittedDepths(); return ok }); err != nil {
		t.Fatal("no reading after the first pass")
	}
	if r.Neighbourhood != 10 || r.Network != 10 {
		t.Fatalf("depths %d and %d, want 10 and 10", r.Neighbourhood, r.Network)
	}
	if r.Neighbours != 5 || r.NeighboursAgreeing != 4 || r.Peers != 5 || r.PeersAgreeing != 4 {
		t.Fatalf("counts %+v, want 5 neighbours with 4 agreeing, 5 peers with 4 agreeing", r)
	}
	if r.At.Before(before) || r.At.After(time.Now()) {
		t.Fatalf("reading time %v outside the pass", r.At)
	}
	if g := gauge(t, service.Metrics(), "neighborhood_depth_peers"); g != 5 {
		t.Fatalf("neighborhood_depth_peers %v, want 5", g)
	}
	if g := gauge(t, service.Metrics(), "neighborhood_depth_agreeing"); g != 4 {
		t.Fatalf("neighborhood_depth_agreeing %v, want 4", g)
	}
}

func TestCommittedDepthsBeforeFirstPass(t *testing.T) {
	t.Parallel()
	topM := topMock.NewTopologyDriver(topMock.WithPeers(swarm.RandAddress(t)))
	service := salud.New(&statusMock{make(map[string]peer)}, topM, mockstorer.NewReserve(), log.Noop, stabilmock.NewSubscriber(false), "full", 0.8, 0.8)
	testutil.CleanupCloser(t, service)
	if _, ok := service.CommittedDepths(); ok {
		t.Fatal("a reading before the first pass")
	}
}
