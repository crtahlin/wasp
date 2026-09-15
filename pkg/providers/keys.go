// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package providers implements content providers: a node announces that it
// holds some content, and other nodes find it and ask it first for that
// content. The format constants and derivations in this file are fixed by
// docs/experiments/content-providers/spec.md so that other clients can read
// and write the same records.
package providers

import (
	"encoding/binary"
	"time"

	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/soc"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

const (
	// Window is the length of a record window. Records and pointer slots carry
	// their window number in their id, so they are written once and never
	// overwritten.
	Window = 12 * time.Hour
	// Slots is the number of pointer-index slots per content key and window.
	Slots = 8
	// MaxSlotEntries is the most provider owners one pointer slot holds.
	MaxSlotEntries = 32
	// MaxUnderlays is the most underlays the address in a record carries.
	MaxUnderlays = 4
	// MaxPayload is the largest record or slot payload, in bytes.
	MaxPayload = swarm.ChunkSize

	recordTag = "wasp-providers-v1"
	indexTag  = "wasp-provider-index-v1"
)

// WindowAt returns the number of the window that contains t.
func WindowAt(t time.Time) uint64 {
	return uint64(t.Unix()) / uint64(Window/time.Second)
}

// RecordID returns the SOC id of a provider record for content key k in
// window w: keccak256("wasp-providers-v1" || k || uint64be(w)).
func RecordID(k []byte, w uint64) []byte {
	return keccak([]byte(recordTag), k, be64(w))
}

// RecordAddress returns the address of the record that owner writes for
// content key k in window w.
func RecordAddress(k, owner []byte, w uint64) (swarm.Address, error) {
	return soc.CreateAddress(RecordID(k, w), owner)
}

// SlotID returns the SOC id of pointer slot i for content key k in window w:
// keccak256("wasp-provider-index-v1" || k || uint64be(w) || byte(i)).
func SlotID(k []byte, w uint64, i int) []byte {
	return keccak([]byte(indexTag), k, be64(w), []byte{byte(i)})
}

// SlotFor returns the pointer slot a provider with the given owner address
// writes to: keccak256(owner)[0] mod Slots.
func SlotFor(owner []byte) int {
	return int(keccak(owner)[0]) % Slots
}

// IndexSigner returns the signer of the pointer index of content key k. Its
// private key is keccak256("wasp-provider-index-v1" || k) reduced modulo the
// secp256k1 curve order, so every client derives the same key. Anyone can
// compute it, which is why the index is only a list of candidates.
func IndexSigner(k []byte) (crypto.Signer, error) {
	key, err := crypto.DecodeSecp256k1PrivateKey(keccak([]byte(indexTag), k))
	if err != nil {
		return nil, err
	}
	return crypto.NewDefaultSigner(key), nil
}

func keccak(parts ...[]byte) []byte {
	h := swarm.NewHasher()
	for _, p := range parts {
		_, _ = h.Write(p)
	}
	return h.Sum(nil)
}

func be64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}
