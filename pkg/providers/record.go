// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package providers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ethersphere/bee/v2/pkg/bzz"
	"github.com/ethersphere/bee/v2/pkg/cac"
	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/soc"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// Capability says how much of the content a provider holds.
type Capability string

const (
	CapabilityFull    Capability = "full"
	CapabilityPartial Capability = "partial"
)

const formatVersion = 1

var (
	// ErrInvalidRecord is returned for a record or slot that fails a check.
	ErrInvalidRecord = errors.New("providers: invalid record")
	// ErrTooLarge is returned for a payload that does not fit in one chunk.
	ErrTooLarge = errors.New("providers: payload too large")
)

type recordJSON struct {
	V          int          `json:"v"`
	Key        string       `json:"key"`
	Window     uint64       `json:"window"`
	Address    *bzz.Address `json:"address"`
	Capability Capability   `json:"capability"`
}

// Record is a verified provider record.
type Record struct {
	// Owner is the provider's Ethereum address, the owner of the record.
	Owner []byte
	// Address is the provider's verified overlay and underlays.
	Address    *bzz.Address
	Capability Capability
	Window     uint64
}

// NewRecordChunk returns the record, signed by the provider's signer, in which
// the provider says it holds content key k during window w. The address must
// be signed by the same key and carry between 1 and MaxUnderlays underlays.
func NewRecordChunk(signer crypto.Signer, k []byte, w uint64, addr *bzz.Address, capability Capability) (swarm.Chunk, error) {
	if err := checkKey(k); err != nil {
		return nil, err
	}
	if addr == nil || len(addr.Underlays) == 0 || len(addr.Underlays) > MaxUnderlays {
		return nil, fmt.Errorf("%w: the address must carry 1 to %d underlays", ErrInvalidRecord, MaxUnderlays)
	}
	if !validCapability(capability) {
		return nil, fmt.Errorf("%w: capability %q", ErrInvalidRecord, capability)
	}

	payload, err := json.Marshal(recordJSON{
		V:          formatVersion,
		Key:        hex.EncodeToString(k),
		Window:     w,
		Address:    addr,
		Capability: capability,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal record: %w", err)
	}
	return signedChunk(signer, RecordID(k, w), payload)
}

// VerifyRecord checks a record chunk for content key k in window w, following
// the steps in the spec, and returns the record.
func VerifyRecord(ch swarm.Chunk, k []byte, w uint64, networkID uint64) (*Record, error) {
	s, payload, err := unwrap(ch)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(s.ID(), RecordID(k, w)) {
		return nil, fmt.Errorf("%w: id does not match key and window", ErrInvalidRecord)
	}

	var r recordJSON
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	switch {
	case r.V != formatVersion:
		return nil, fmt.Errorf("%w: version %d", ErrInvalidRecord, r.V)
	case r.Key != hex.EncodeToString(k):
		return nil, fmt.Errorf("%w: key does not match", ErrInvalidRecord)
	case r.Window != w:
		return nil, fmt.Errorf("%w: window %d, want %d", ErrInvalidRecord, r.Window, w)
	case !validCapability(r.Capability):
		return nil, fmt.Errorf("%w: capability %q", ErrInvalidRecord, r.Capability)
	case r.Address == nil || len(r.Address.Underlays) == 0 || len(r.Address.Underlays) > MaxUnderlays:
		return nil, fmt.Errorf("%w: the address must carry 1 to %d underlays", ErrInvalidRecord, MaxUnderlays)
	}

	// UnmarshalJSON does not verify the address; rebuild the signed bytes
	// and let ParseAddress check them
	underlays, err := bzz.SerializeUnderlays(r.Address.Underlays)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	addr, err := bzz.ParseAddress(underlays, r.Address.Overlay.Bytes(), r.Address.Signature, r.Address.Nonce, r.Address.Timestamp, networkID, r.Address.ChequebookAddress.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	if !bytes.Equal(addr.EthereumAddress, s.OwnerAddress()) {
		return nil, fmt.Errorf("%w: the address is not signed by the record owner", ErrInvalidRecord)
	}

	return &Record{
		Owner:      s.OwnerAddress(),
		Address:    addr,
		Capability: r.Capability,
		Window:     w,
	}, nil
}

type slotJSON struct {
	V         int      `json:"v"`
	Key       string   `json:"key"`
	Window    uint64   `json:"window"`
	Providers []string `json:"providers"`
}

// NewSlotChunk returns pointer slot i for content key k in window w, listing
// the provider owners and signed with the index key of k. It holds at most
// MaxSlotEntries owners.
func NewSlotChunk(k []byte, w uint64, i int, owners [][]byte) (swarm.Chunk, error) {
	if err := checkKey(k); err != nil {
		return nil, err
	}
	if i < 0 || i >= Slots {
		return nil, fmt.Errorf("%w: slot %d", ErrInvalidRecord, i)
	}
	if len(owners) > MaxSlotEntries {
		return nil, fmt.Errorf("%w: %d entries, at most %d", ErrInvalidRecord, len(owners), MaxSlotEntries)
	}

	entries := make([]string, 0, len(owners))
	for _, o := range owners {
		if len(o) != crypto.AddressSize {
			return nil, fmt.Errorf("%w: owner of %d bytes", ErrInvalidRecord, len(o))
		}
		entries = append(entries, hex.EncodeToString(o))
	}

	payload, err := json.Marshal(slotJSON{
		V:         formatVersion,
		Key:       hex.EncodeToString(k),
		Window:    w,
		Providers: entries,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal slot: %w", err)
	}

	signer, err := IndexSigner(k)
	if err != nil {
		return nil, fmt.Errorf("index signer: %w", err)
	}
	return signedChunk(signer, SlotID(k, w, i), payload)
}

// ParseSlot reads pointer slot i for content key k in window w and returns
// the provider owners it lists. Anyone can write a slot, so its entries are
// only candidates: entries that are not owner addresses are skipped, and no
// more than MaxSlotEntries are returned.
func ParseSlot(ch swarm.Chunk, k []byte, w uint64, i int) ([][]byte, error) {
	s, payload, err := unwrap(ch)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(s.ID(), SlotID(k, w, i)) {
		return nil, fmt.Errorf("%w: id does not match key, window and slot", ErrInvalidRecord)
	}

	var sl slotJSON
	if err := json.Unmarshal(payload, &sl); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	if sl.V != formatVersion || sl.Key != hex.EncodeToString(k) || sl.Window != w {
		return nil, fmt.Errorf("%w: slot for another key, window or version", ErrInvalidRecord)
	}

	owners := make([][]byte, 0, len(sl.Providers))
	for _, e := range sl.Providers {
		o, err := hex.DecodeString(e)
		if err != nil || len(o) != crypto.AddressSize || containsOwner(owners, o) {
			continue
		}
		owners = append(owners, o)
		if len(owners) == MaxSlotEntries {
			break
		}
	}
	return owners, nil
}

// AddToSlot returns the owners with owner added once. When the slot is full,
// the first entry is dropped to make room.
func AddToSlot(owners [][]byte, owner []byte) [][]byte {
	out := make([][]byte, 0, len(owners)+1)
	for _, o := range owners {
		if !bytes.Equal(o, owner) {
			out = append(out, o)
		}
	}
	out = append(out, owner)
	if len(out) > MaxSlotEntries {
		out = out[len(out)-MaxSlotEntries:]
	}
	return out
}

func containsOwner(owners [][]byte, o []byte) bool {
	for _, x := range owners {
		if bytes.Equal(x, o) {
			return true
		}
	}
	return false
}

// checkKey refuses a content key that is not 32 bytes. An encrypted reference
// is 64 bytes and carries its decryption key, which a record would publish.
func checkKey(k []byte) error {
	if len(k) != swarm.HashSize {
		return fmt.Errorf("%w: content key of %d bytes, want %d", ErrInvalidRecord, len(k), swarm.HashSize)
	}
	return nil
}

func validCapability(c Capability) bool {
	return c == CapabilityFull || c == CapabilityPartial
}

// signedChunk wraps payload in a content-addressed chunk and signs it as a
// single owner chunk with the given id.
func signedChunk(signer crypto.Signer, id, payload []byte) (swarm.Chunk, error) {
	if len(payload) > MaxPayload {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(payload))
	}
	ch, err := cac.New(payload)
	if err != nil {
		return nil, fmt.Errorf("content chunk: %w", err)
	}
	return soc.New(id, ch).Sign(signer)
}

// unwrap checks that ch is a valid single owner chunk and returns it with the
// payload of the chunk it wraps.
func unwrap(ch swarm.Chunk) (*soc.SOC, []byte, error) {
	if !soc.Valid(ch) {
		return nil, nil, fmt.Errorf("%w: not a valid single owner chunk", ErrInvalidRecord)
	}
	s, err := soc.FromChunk(ch)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	data := s.WrappedChunk().Data()
	if len(data) < swarm.SpanSize {
		return nil, nil, fmt.Errorf("%w: short chunk", ErrInvalidRecord)
	}
	return s, data[swarm.SpanSize:], nil
}
