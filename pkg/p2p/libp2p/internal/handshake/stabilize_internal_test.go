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

func addrSetEqual(a, b []ma.Multiaddr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}

// TestStabilizeBootstrapAdoptsFirstPublic: the first set carrying a public address
// is adopted and returned.
func TestStabilizeBootstrapAdoptsFirstPublic(t *testing.T) {
	pub := mustAddr(t, "/ip4/1.2.3.4/tcp/1634")
	lan := mustAddr(t, "/ip4/192.168.1.5/tcp/1634")
	s := &Service{}

	got := s.stabilizeUnderlays([]ma.Multiaddr{pub, lan})
	if !addrSetEqual(got, []ma.Multiaddr{pub, lan}) {
		t.Fatalf("bootstrap did not adopt the public set: %v", got)
	}
}

// TestStabilizeRepeatedObservationStable: the anti-churn case. The same public
// observation returns the same pinned set every time.
func TestStabilizeRepeatedObservationStable(t *testing.T) {
	set := []ma.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/1634")}
	s := &Service{}

	first := s.stabilizeUnderlays(set)
	for i := 0; i < 20; i++ {
		got := s.stabilizeUnderlays([]ma.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/1634")})
		if !addrSetEqual(got, first) {
			t.Fatalf("set changed on repeat %d: %v vs %v", i, got, first)
		}
	}
}

// TestStabilizePortRemappingStable: the real churn driver. The same public IP
// observed with a different port each handshake must not re-pin, because NAT remaps
// the source port per connection.
func TestStabilizePortRemappingStable(t *testing.T) {
	s := &Service{}
	first := s.stabilizeUnderlays([]ma.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/40001")})
	for port := 40002; port < 40040; port++ {
		got := s.stabilizeUnderlays([]ma.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/"+itoa(port))})
		if !addrSetEqual(got, first) {
			t.Fatalf("port %d re-pinned: %v vs %v", port, got, first)
		}
	}
}

// TestStabilizeTransientObservationIgnored: a single differing public IP does not
// re-pin; the pinned set is kept.
func TestStabilizeTransientObservationIgnored(t *testing.T) {
	s := &Service{}
	pinned := s.stabilizeUnderlays([]ma.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/1634")})

	// one differing observation
	got := s.stabilizeUnderlays([]ma.Multiaddr{mustAddr(t, "/ip4/9.9.9.9/tcp/1634")})
	if !addrSetEqual(got, pinned) {
		t.Fatalf("transient observation re-pinned: %v", got)
	}
	// the real address returns; counter resets
	got = s.stabilizeUnderlays([]ma.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/1634")})
	if !addrSetEqual(got, pinned) {
		t.Fatalf("pin lost after real address returned: %v", got)
	}
}

// TestStabilizeSustainedChangeRepins: a public IP change observed for the threshold
// of consecutive handshakes is adopted.
func TestStabilizeSustainedChangeRepins(t *testing.T) {
	s := &Service{}
	old := s.stabilizeUnderlays([]ma.Multiaddr{mustAddr(t, "/ip4/1.2.3.4/tcp/1634")})

	newSet := []ma.Multiaddr{mustAddr(t, "/ip4/5.6.7.8/tcp/1634")}
	var got []ma.Multiaddr
	for i := 0; i < advertisedUnderlayRepinThreshold; i++ {
		got = s.stabilizeUnderlays([]ma.Multiaddr{mustAddr(t, "/ip4/5.6.7.8/tcp/1634")})
	}
	if addrSetEqual(got, old) {
		t.Fatal("sustained change did not re-pin")
	}
	if !addrSetEqual(got, newSet) {
		t.Fatalf("re-pin adopted the wrong set: %v", got)
	}
}

// TestStabilizeMultiHomedStaysValid: with two public addresses pinned, losing one
// while the other is still observed does not re-pin.
func TestStabilizeMultiHomedStaysValid(t *testing.T) {
	v4 := mustAddr(t, "/ip4/1.2.3.4/tcp/1634")
	v6 := mustAddr(t, "/ip6/2001:db8::1/tcp/1634")
	s := &Service{}
	pinned := s.stabilizeUnderlays([]ma.Multiaddr{v4, v6})

	// only the v6 address is observed now; the pin is still valid
	got := s.stabilizeUnderlays([]ma.Multiaddr{mustAddr(t, "/ip6/2001:db8::1/tcp/55555")})
	if !addrSetEqual(got, pinned) {
		t.Fatalf("multi-homed pin dropped when one address still observed: %v", got)
	}
}

// TestStabilizeNoPublicPassthrough: until a public address is seen the computed set
// passes through, so a node can still adopt its public address once reported.
func TestStabilizeNoPublicPassthrough(t *testing.T) {
	lan := mustAddr(t, "/ip4/192.168.1.5/tcp/1634")
	s := &Service{}

	got := s.stabilizeUnderlays([]ma.Multiaddr{lan})
	if !addrSetEqual(got, []ma.Multiaddr{lan}) {
		t.Fatalf("expected passthrough before a public address: %v", got)
	}
	pub := mustAddr(t, "/ip4/1.2.3.4/tcp/1634")
	got = s.stabilizeUnderlays([]ma.Multiaddr{pub})
	if !addrSetEqual(got, []ma.Multiaddr{pub}) {
		t.Fatalf("public address not adopted once seen: %v", got)
	}
}

func TestPublicIPsAndSharesKey(t *testing.T) {
	ips := publicIPs([]ma.Multiaddr{
		mustAddr(t, "/ip4/8.8.8.8/tcp/1634"),
		mustAddr(t, "/ip4/192.168.1.5/tcp/1634"),
		mustAddr(t, "/ip4/127.0.0.1/tcp/1634"),
	})
	if len(ips) != 1 {
		t.Fatalf("expected exactly one public IP, got %d", len(ips))
	}
	if _, ok := ips["8.8.8.8"]; !ok {
		t.Fatal("public IP not extracted")
	}
	if !sharesKey(ips, map[string]struct{}{"8.8.8.8": {}}) {
		t.Fatal("sharesKey missed a common key")
	}
	if sharesKey(ips, map[string]struct{}{"5.5.5.5": {}}) {
		t.Fatal("sharesKey reported a false overlap")
	}
}

// itoa avoids importing strconv for one call in a test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
