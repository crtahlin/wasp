// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handshake

import (
	"testing"

	ma "github.com/multiformats/go-multiaddr"
)

func mustAddr(t *testing.T, s string) ma.Multiaddr {
	t.Helper()
	a, err := ma.NewMultiaddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestStabilizeUnderlaysPinsFirstPublic verifies the issue #221 fix: once a set
// containing a public address is seen, it is pinned and reused, so the advertised
// underlay set stays stable across handshakes even when a later peer observes a
// different address for this node.
func TestStabilizeUnderlaysPinsFirstPublic(t *testing.T) {
	pub := mustAddr(t, "/ip4/1.2.3.4/tcp/1634")
	lan := mustAddr(t, "/ip4/192.168.1.5/tcp/1634")
	loop := mustAddr(t, "/ip4/127.0.0.1/tcp/1634")

	s := &Service{}

	first := s.stabilizeUnderlays([]ma.Multiaddr{pub, lan})
	if len(first) != 2 {
		t.Fatalf("first set len = %d, want 2", len(first))
	}

	// A later handshake observes a different address; the pinned set must be reused.
	second := s.stabilizeUnderlays([]ma.Multiaddr{mustAddr(t, "/ip4/5.6.7.8/tcp/1634"), loop})
	if len(second) != len(first) || !second[0].Equal(first[0]) || !second[1].Equal(first[1]) {
		t.Fatalf("pinned set not reused: got %v, want %v", second, first)
	}
}

// TestStabilizeUnderlaysNoPublicNotPinned verifies that until a public address is
// seen the freshly computed set is used, so a node can still adopt its public
// address once a peer reports it.
func TestStabilizeUnderlaysNoPublicNotPinned(t *testing.T) {
	lan := mustAddr(t, "/ip4/192.168.1.5/tcp/1634")
	s := &Service{}

	got := s.stabilizeUnderlays([]ma.Multiaddr{lan})
	if len(got) != 1 || !got[0].Equal(lan) {
		t.Fatalf("passthrough expected before any public address, got %v", got)
	}

	pub := mustAddr(t, "/ip4/1.2.3.4/tcp/1634")
	adopted := s.stabilizeUnderlays([]ma.Multiaddr{pub})
	if len(adopted) != 1 || !adopted[0].Equal(pub) {
		t.Fatalf("public address should be adopted once seen, got %v", adopted)
	}
}

func TestContainsPublicAddr(t *testing.T) {
	if !containsPublicAddr([]ma.Multiaddr{mustAddr(t, "/ip4/8.8.8.8/tcp/1634")}) {
		t.Fatal("public address not detected")
	}
	if containsPublicAddr([]ma.Multiaddr{
		mustAddr(t, "/ip4/192.168.1.5/tcp/1634"),
		mustAddr(t, "/ip4/127.0.0.1/tcp/1634"),
	}) {
		t.Fatal("private/loopback wrongly reported public")
	}
}
