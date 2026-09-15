// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package providers_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/bzz"
	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/providers"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	ma "github.com/multiformats/go-multiaddr"
)

const networkID = uint64(1)

type node struct {
	signer crypto.Signer
	owner  []byte
	addr   *bzz.Address
}

func newNode(t *testing.T, underlays int) node {
	t.Helper()

	key, err := crypto.GenerateSecp256k1Key()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.NewDefaultSigner(key)
	nonce := make([]byte, 32)
	overlay, err := crypto.NewOverlayAddress(key.PublicKey, networkID, nonce)
	if err != nil {
		t.Fatal(err)
	}

	mas := make([]ma.Multiaddr, 0, underlays)
	for i := range underlays {
		mas = append(mas, ma.StringCast("/ip4/192.0.2.1/tcp/"+string(rune('1'+i))+"634"))
	}
	addr, err := bzz.NewAddress(signer, mas, overlay, networkID, nonce, time.Now().Unix(), common.Address{})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := signer.EthereumAddress()
	if err != nil {
		t.Fatal(err)
	}
	return node{signer: signer, owner: owner.Bytes(), addr: addr}
}

func TestRecordRoundTrip(t *testing.T) {
	t.Parallel()

	n := newNode(t, 2)
	k := swarm.RandAddress(t).Bytes()
	w := providers.WindowAt(time.Now())

	ch, err := providers.NewRecordChunk(n.signer, k, w, n.addr, providers.CapabilityFull)
	if err != nil {
		t.Fatal(err)
	}

	want, err := providers.RecordAddress(k, n.owner, w)
	if err != nil {
		t.Fatal(err)
	}
	if !ch.Address().Equal(want) {
		t.Fatalf("record at %s, want %s", ch.Address(), want)
	}

	r, err := providers.VerifyRecord(ch, k, w, networkID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.Owner, n.owner) {
		t.Fatal("record owner differs from the provider")
	}
	if !r.Address.Overlay.Equal(n.addr.Overlay) || len(r.Address.Underlays) != 2 {
		t.Fatalf("record address %v, want %v", r.Address, n.addr)
	}
	if r.Capability != providers.CapabilityFull {
		t.Fatalf("capability %q", r.Capability)
	}
}

func TestRecordRejected(t *testing.T) {
	t.Parallel()

	n := newNode(t, 1)
	other := newNode(t, 1)
	k := swarm.RandAddress(t).Bytes()
	w := providers.WindowAt(time.Now())

	good, err := providers.NewRecordChunk(n.signer, k, w, n.addr, providers.CapabilityFull)
	if err != nil {
		t.Fatal(err)
	}
	// signed by one node but carrying another node's address
	foreign, err := providers.NewRecordChunk(other.signer, k, w, n.addr, providers.CapabilityFull)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		ch        swarm.Chunk
		k         []byte
		w         uint64
		networkID uint64
	}{
		{"previous window", good, k, w - 1, networkID},
		{"other key", good, swarm.RandAddress(t).Bytes(), w, networkID},
		{"other network", good, k, w, networkID + 1},
		{"owner is not the address key", foreign, k, w, networkID},
		{"not a single owner chunk", swarm.NewChunk(good.Address(), good.Data()[:40]), k, w, networkID},
	} {
		if _, err := providers.VerifyRecord(tc.ch, tc.k, tc.w, tc.networkID); !errors.Is(err, providers.ErrInvalidRecord) {
			t.Errorf("%s: got %v, want ErrInvalidRecord", tc.name, err)
		}
	}
}

func TestRecordLimits(t *testing.T) {
	t.Parallel()

	k := swarm.RandAddress(t).Bytes()
	w := providers.WindowAt(time.Now())

	n := newNode(t, providers.MaxUnderlays+1)
	if _, err := providers.NewRecordChunk(n.signer, k, w, n.addr, providers.CapabilityFull); !errors.Is(err, providers.ErrInvalidRecord) {
		t.Fatalf("too many underlays: got %v, want ErrInvalidRecord", err)
	}

	n = newNode(t, 1)
	if _, err := providers.NewRecordChunk(n.signer, k, w, n.addr, "withdrawn"); !errors.Is(err, providers.ErrInvalidRecord) {
		t.Fatalf("unknown capability: got %v, want ErrInvalidRecord", err)
	}
}

func TestSlotRoundTrip(t *testing.T) {
	t.Parallel()

	k := swarm.RandAddress(t).Bytes()
	w := providers.WindowAt(time.Now())
	a, b := newNode(t, 1), newNode(t, 1)

	owners := providers.AddToSlot(nil, a.owner)
	owners = providers.AddToSlot(owners, b.owner)
	owners = providers.AddToSlot(owners, a.owner) // moves a to the end, once

	slot := providers.SlotFor(a.owner)
	ch, err := providers.NewSlotChunk(k, w, slot, owners)
	if err != nil {
		t.Fatal(err)
	}

	got, err := providers.ParseSlot(ch, k, w, slot)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !bytes.Equal(got[0], b.owner) || !bytes.Equal(got[1], a.owner) {
		t.Fatalf("slot lists %x, want b then a", got)
	}

	if _, err := providers.ParseSlot(ch, k, w, (slot+1)%providers.Slots); !errors.Is(err, providers.ErrInvalidRecord) {
		t.Fatalf("slot read as another slot: got %v, want ErrInvalidRecord", err)
	}
}

func TestSlotLimit(t *testing.T) {
	t.Parallel()

	var owners [][]byte
	var first []byte
	for i := range providers.MaxSlotEntries + 1 {
		o := swarm.RandAddress(t).Bytes()[:20]
		if i == 0 {
			first = o
		}
		owners = providers.AddToSlot(owners, o)
	}
	if len(owners) != providers.MaxSlotEntries {
		t.Fatalf("slot holds %d entries, want %d", len(owners), providers.MaxSlotEntries)
	}
	for _, o := range owners {
		if bytes.Equal(o, first) {
			t.Fatal("a full slot kept its first entry")
		}
	}
}

func TestIndexSignerDerivation(t *testing.T) {
	t.Parallel()

	k := bytes.Repeat([]byte{0xab}, 32)

	s1, err := providers.IndexSigner(k)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := providers.IndexSigner(k)
	if err != nil {
		t.Fatal(err)
	}
	a1, _ := s1.EthereumAddress()
	a2, _ := s2.EthereumAddress()
	if a1 != a2 {
		t.Fatal("index key is not deterministic")
	}

	// the spec's derivation, done by hand
	h := swarm.NewHasher()
	_, _ = h.Write([]byte("wasp-provider-index-v1"))
	_, _ = h.Write(k)
	key, err := crypto.DecodeSecp256k1PrivateKey(h.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := crypto.NewDefaultSigner(key).EthereumAddress()
	if a1 != want {
		t.Fatalf("index key %s, want %s", a1, want)
	}
}

// TestFormatVectors pins the derivations of the record format, so that a
// change to them is noticed and other clients can check against the same
// values. Content key: 32 bytes of 0xab.
func TestFormatVectors(t *testing.T) {
	t.Parallel()

	k := bytes.Repeat([]byte{0xab}, 32)

	s, err := providers.IndexSigner(k)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.EthereumAddress()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := owner.Hex(), "0x09962A04042FFE0552f180700f012B16293f8046"; got != want {
		t.Errorf("index owner %s, want %s", got, want)
	}
	if got, want := hex.EncodeToString(providers.RecordID(k, 1000)), "f79e98e5c46f0a18f48fe74924392e6f141e3017bc23538104d2b722f6d07366"; got != want {
		t.Errorf("record id %s, want %s", got, want)
	}
	if got, want := hex.EncodeToString(providers.SlotID(k, 1000, 3)), "a3a42098438ea550a492ddf22b7b4381e49978d7241e340178fe0ea75643596a"; got != want {
		t.Errorf("slot id %s, want %s", got, want)
	}
}

func TestWindow(t *testing.T) {
	t.Parallel()

	start := time.Unix(int64(providers.Window/time.Second)*1000, 0)
	if got := providers.WindowAt(start); got != 1000 {
		t.Fatalf("window %d, want 1000", got)
	}
	if got := providers.WindowAt(start.Add(providers.Window - time.Second)); got != 1000 {
		t.Fatalf("window %d, want 1000", got)
	}
	if got := providers.WindowAt(start.Add(providers.Window)); got != 1001 {
		t.Fatalf("window %d, want 1001", got)
	}
}
