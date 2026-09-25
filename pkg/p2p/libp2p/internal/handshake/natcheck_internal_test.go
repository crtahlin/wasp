// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handshake

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/log"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

func TestNATDisagreement(t *testing.T) {
	t.Parallel()

	a := func(s ...string) []ma.Multiaddr {
		out := make([]ma.Multiaddr, 0, len(s))
		for _, x := range s {
			out = append(out, mustAddr(t, x))
		}
		return out
	}

	for _, tc := range []struct {
		name       string
		observed   []ma.Multiaddr
		advertised []ma.Multiaddr
		want       bool
	}{
		{
			name:       "configured IP is observed",
			observed:   a("/ip4/1.2.3.4/tcp/40001"),
			advertised: a("/ip4/1.2.3.4/tcp/1634", "/ip4/192.168.1.5/tcp/1634"),
			want:       false,
		},
		{
			name:       "configured IP is not observed",
			observed:   a("/ip4/5.6.7.8/tcp/40001"),
			advertised: a("/ip4/1.2.3.4/tcp/1634", "/ip4/192.168.1.5/tcp/1634"),
			want:       true,
		},
		{
			name:       "IPv6 observation says nothing about an IPv4 nat-addr",
			observed:   a("/ip6/2a00:1450:1::5/tcp/40001"),
			advertised: a("/ip4/1.2.3.4/tcp/1634"),
			want:       false,
		},
		{
			name:       "private observation only",
			observed:   a("/ip4/192.168.1.9/tcp/40001"),
			advertised: a("/ip4/1.2.3.4/tcp/1634"),
			want:       false,
		},
		{
			name:       "IPv6 configured address is not observed",
			observed:   a("/ip6/2a00:1450:1::5/tcp/40001"),
			advertised: a("/ip6/2a00:1450:2::9/tcp/1634"),
			want:       true,
		},
		{
			name:       "one family disagrees",
			observed:   a("/ip4/5.6.7.8/tcp/40001", "/ip6/2a00:1450:1::5/tcp/40001"),
			advertised: a("/ip4/1.2.3.4/tcp/1634", "/ip6/2a00:1450:1::5/tcp/1634"),
			want:       true,
		},
		{
			name:       "DNS nat-addr carries no IP to compare",
			observed:   a("/ip4/5.6.7.8/tcp/40001"),
			advertised: a("/dns4/node.example.com/tcp/1634"),
			want:       false,
		},
		{
			name:       "nothing public advertised",
			observed:   a("/ip4/5.6.7.8/tcp/40001"),
			advertised: a("/ip4/192.168.1.5/tcp/1634"),
			want:       false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _, _ := natDisagreement(tc.observed, tc.advertised)
			if got != tc.want {
				t.Fatalf("natDisagreement = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCheckNATAddrRun: a stale nat-addr is reported once per sustained run of
// disagreeing handshakes, not on a single one, and an agreeing handshake resets
// the run.
func TestCheckNATAddrRun(t *testing.T) {
	t.Parallel()

	stale := []ma.Multiaddr{mustAddr(t, "/ip4/5.6.7.8/tcp/40001")}
	fresh := []ma.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/40001")}
	advertised := []ma.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/1634")}

	s := &Service{metrics: newMetrics(), logger: log.Noop}
	count := func() float64 { return counterValue(t, s.metrics.NATAddrMismatch) }

	for i := 1; i < advertisedUnderlayRepinThreshold; i++ {
		s.checkNATAddr(stale, advertised)
	}
	if got := count(); got != 0 {
		t.Fatalf("mismatch counted before a sustained run: %v", got)
	}

	s.checkNATAddr(fresh, advertised)
	s.checkNATAddr(stale, advertised)
	if got := count(); got != 0 {
		t.Fatalf("an agreeing handshake did not reset the run: %v", got)
	}

	for i := 1; i < advertisedUnderlayRepinThreshold; i++ {
		s.checkNATAddr(stale, advertised)
	}
	if got := count(); got != 1 {
		t.Fatalf("sustained run counted %v times, want 1", got)
	}

	for i := 0; i < 10; i++ {
		s.checkNATAddr(stale, advertised)
	}
	if got := count(); got != 1 {
		t.Fatalf("one run counted %v times, want 1", got)
	}
}

// TestCheckNATAddrPortOnlyNeverCounts: with a port-only nat-addr the advertised
// IP is the observed one, so a changing public IP is followed, not reported.
func TestCheckNATAddrPortOnlyNeverCounts(t *testing.T) {
	t.Parallel()

	s := &Service{metrics: newMetrics(), logger: log.Noop}
	for _, ip := range []string{"1.2.3.4", "5.6.7.8", "5.6.7.8", "5.6.7.8", "5.6.7.8"} {
		observed := []ma.Multiaddr{mustAddr(t, "/ip4/"+ip+"/tcp/40001")}
		advertised := []ma.Multiaddr{mustAddr(t, "/ip4/"+ip+"/tcp/1634")}
		s.checkNATAddr(observed, advertised)
	}
	if got := counterValue(t, s.metrics.NATAddrMismatch); got != 0 {
		t.Fatalf("port-only mode counted a mismatch: %v", got)
	}
}
