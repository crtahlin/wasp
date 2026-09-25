// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handshake_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/bzz"
	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/p2p/libp2p/internal/handshake"
	"github.com/ethersphere/bee/v2/pkg/p2p/libp2p/internal/handshake/mock"
	"github.com/ethersphere/bee/v2/pkg/p2p/libp2p/internal/handshake/pb"
	"github.com/ethersphere/bee/v2/pkg/p2p/protobuf"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const testPeerID = "16Uiu2HAkx8ULY8cTXhdVAcMmLcH9AsTKz6uBQ7DPLKRjMLgBVYkA"

// portOnlyResolver does what the static nat-addr resolver does for a port-only
// value such as ":1634": keep the observed IP, replace the port. The real
// resolver's port replacement is covered by the "replace port" case in
// pkg/p2p/libp2p/static_resolver_test.go.
type portOnlyResolver struct{ port string }

func (r portOnlyResolver) Resolve(observed ma.Multiaddr) (ma.Multiaddr, error) {
	parts := strings.Split(observed.String(), "/")
	if len(parts) < 5 {
		return observed, nil
	}
	parts[4] = r.port
	return ma.NewMultiaddr(strings.Join(parts, "/"))
}

// newNATTestService builds a handshake service with the given nat-addr
// resolver, and returns it with a function that runs one inbound handshake in
// which the peer observed this node at ip on an ephemeral NAT port, returning
// the underlays the node advertised.
func newNATTestService(t *testing.T, resolver handshake.AdvertisableAddressResolver, host handshake.Addresser) (*handshake.Service, func(ip string) []ma.Multiaddr) {
	t.Helper()

	const networkID = uint64(3)
	now := time.Unix(1700000000, 0)

	pk, err := crypto.GenerateSecp256k1Key()
	if err != nil {
		t.Fatal(err)
	}
	nonce := common.HexToHash("0x1").Bytes()
	overlay, err := crypto.NewOverlayAddress(pk.PublicKey, networkID, nonce)
	if err != nil {
		t.Fatal(err)
	}
	id, err := libp2ppeer.Decode(testPeerID)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := handshake.New(crypto.NewDefaultSigner(pk), resolver, overlay, networkID, true, nonce, host, "", noopAddressbook{}, id, nil, log.Noop)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetTime(func() time.Time { return now })

	handle := func(ip string) []ma.Multiaddr {
		t.Helper()

		family := "/ip4/"
		if strings.Contains(ip, ":") {
			family = "/ip6/"
		}
		observed := []ma.Multiaddr{mustMultiaddr(t, family+ip+"/tcp/40001/p2p/"+testPeerID)}
		observedBinary, err := bzz.SerializeUnderlays(observed)
		if err != nil {
			t.Fatal(err)
		}

		var buffer1, buffer2 bytes.Buffer
		stream1 := mock.NewStream(&buffer1, &buffer2)
		stream2 := mock.NewStream(&buffer2, &buffer1)

		w := protobuf.NewWriter(stream2)
		if err := w.WriteMsg(&pb.Syn{ObservedUnderlay: observedBinary}); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteMsg(signProtoAck(t, networkID, now.Unix())); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Handle(context.Background(), stream1, observed); err != nil {
			t.Fatal(err)
		}

		_, r := protobuf.NewWriterAndReader(stream2)
		var got pb.SynAck
		if err := r.ReadMsg(&got); err != nil {
			t.Fatal(err)
		}
		advertised, err := bzz.DeserializeUnderlays(got.Ack.Address.Underlay)
		if err != nil {
			t.Fatal(err)
		}
		return advertised
	}
	return svc, handle
}

// TestHandle_PortOnlyNATFollowsPublicIP: with a port-only nat-addr the node
// advertises the public IP peers observe on the configured port, and follows a
// change of public IP after a sustained run of handshakes, with no restart.
// This is what lets a node behind a port-forwarding NAT survive a public IP
// change. See #500.
func TestHandle_PortOnlyNATFollowsPublicIP(t *testing.T) {
	t.Parallel()

	svc, handle := newNATTestService(t, portOnlyResolver{port: "1634"}, nil)

	want := func(t *testing.T, got []ma.Multiaddr, ip string) {
		t.Helper()
		expected := "/ip4/" + ip + "/tcp/1634/p2p/" + testPeerID
		if len(got) != 1 || got[0].String() != expected {
			t.Fatalf("advertised %v, want [%s]", got, expected)
		}
	}

	// The observed ephemeral port is replaced by the configured one.
	want(t, handle("1.2.3.4"), "1.2.3.4")

	// One or two handshakes seeing a new IP do not move the address.
	want(t, handle("5.6.7.8"), "1.2.3.4")
	want(t, handle("5.6.7.8"), "1.2.3.4")

	// The third does: the node now advertises the new IP on the configured port.
	want(t, handle("5.6.7.8"), "5.6.7.8")

	if got := natMismatchCount(t, svc); got != 0 {
		t.Fatalf("port-only mode reported a stale nat-addr %v times", got)
	}
}

// TestHandle_StaleNATAddrReported: with a nat-addr that carries a host, the
// advertised IP never follows the observed one, and a sustained run of
// handshakes observing another public IP is reported once.
func TestHandle_StaleNATAddrReported(t *testing.T) {
	t.Parallel()

	fixed := &AdvertisableAddresserMock{advertisableAddress: mustMultiaddr(t, "/ip4/1.2.3.4/tcp/1634/p2p/"+testPeerID)}
	svc, handle := newNATTestService(t, fixed, nil)

	handle("1.2.3.4")
	handle("5.6.7.8")
	handle("5.6.7.8")
	if got := natMismatchCount(t, svc); got != 0 {
		t.Fatalf("reported before a sustained run: %v", got)
	}
	advertised := handle("5.6.7.8")
	if got := natMismatchCount(t, svc); got != 1 {
		t.Fatalf("stale nat-addr reported %v times, want 1", got)
	}
	if len(advertised) != 1 || !strings.HasPrefix(advertised[0].String(), "/ip4/1.2.3.4/") {
		t.Fatalf("a configured IP must not move: advertised %v", advertised)
	}
}

type publicHost struct{ addr ma.Multiaddr }

func (h publicHost) AdvertizableAddrs() ([]ma.Multiaddr, error) {
	return []ma.Multiaddr{h.addr}, nil
}

// TestHandle_HostAddressesNotCompared: only what peers observed, as the
// resolver rewrites it, is compared, never the node's own listen addresses.
// A host with a fixed IPv4 nat-addr and a global IPv6 listen address, reached
// by a peer that sees its IPv6 address rewritten, says nothing about nat-addr
// and must not be reported as a stale one.
func TestHandle_HostAddressesNotCompared(t *testing.T) {
	t.Parallel()

	fixed := &AdvertisableAddresserMock{advertisableAddress: mustMultiaddr(t, "/ip4/1.2.3.4/tcp/1634/p2p/"+testPeerID)}
	host := publicHost{addr: mustMultiaddr(t, "/ip6/2a00:1450:2::9/tcp/1634/p2p/"+testPeerID)}
	svc, handle := newNATTestService(t, fixed, host)
	for i := 0; i < 5; i++ {
		handle("2a00:1450:1::5")
	}
	if got := natMismatchCount(t, svc); got != 0 {
		t.Fatalf("a listen address was reported as a stale nat-addr %v times", got)
	}
}

// natMismatchCount reads handshake_nat_addr_mismatch_total from the service's
// collectors.
func natMismatchCount(t *testing.T, svc *handshake.Service) float64 {
	t.Helper()
	for _, c := range svc.Metrics() {
		counter, ok := c.(prometheus.Counter)
		if !ok || !strings.Contains(counter.Desc().String(), "nat_addr_mismatch_total") {
			continue
		}
		var m dto.Metric
		if err := counter.Write(&m); err != nil {
			t.Fatal(err)
		}
		return m.GetCounter().GetValue()
	}
	t.Fatal("handshake_nat_addr_mismatch_total not registered")
	return 0
}

func mustMultiaddr(t *testing.T, s string) ma.Multiaddr {
	t.Helper()
	a, err := ma.NewMultiaddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
