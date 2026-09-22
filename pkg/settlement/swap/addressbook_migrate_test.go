// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package swap_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/settlement/swap"
	mockstore "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

var errInjected = errors.New("injected store failure")

// failingStore fails the first Delete whose key contains match, and passes
// everything else through. pkg/statestore/mock has no way to inject an error,
// and this test is entirely about what a failed write leaves behind, so the
// wrapper lives here rather than changing the shared mock for one caller.
type failingStore struct {
	storage.StateStorer
	match  string
	failed bool
}

func (f *failingStore) Delete(key string) error {
	if !f.failed && strings.Contains(key, f.match) {
		f.failed = true
		return errInjected
	}
	return f.StateStorer.Delete(key)
}

// TestMigratePeerFailureLeavesNoDoubleBeneficiary is wasp #430.
//
// MigratePeer has no transaction available: storage.StateStorer offers only
// Get, Put, Delete and Iterate, so a failure part way leaves the addressbook
// inconsistent whichever order the writes run in. What the order decides is
// which inconsistency survives.
//
// Put then delete, which is what this did, leaves BOTH peers mapped to the
// beneficiary: two payment paths onto one chequebook. Delete then put leaves
// NEITHER mapped, which the next announcement repairs.
//
// The test asserts the dangerous state is absent, not that the migration
// succeeded. It did not succeed; that is the point.
func TestMigratePeerFailureLeavesNoDoubleBeneficiary(t *testing.T) {
	t.Parallel()

	var (
		oldPeer     = swarm.MustParseHexAddress("aabb")
		newPeer     = swarm.MustParseHexAddress("ccdd")
		beneficiary = common.HexToAddress("0xfeed")
	)

	store := &failingStore{StateStorer: mockstore.NewStateStore(), match: "beneficiary"}
	book := swap.NewAddressbook(store)

	if err := book.PutBeneficiary(oldPeer, beneficiary); err != nil {
		t.Fatal(err)
	}

	if err := book.MigratePeer(oldPeer, newPeer); !errors.Is(err, errInjected) {
		t.Fatalf("migrate: got %v, want the injected failure", err)
	}

	_, oldKnown, err := book.Beneficiary(oldPeer)
	if err != nil {
		t.Fatal(err)
	}
	_, newKnown, err := book.Beneficiary(newPeer)
	if err != nil {
		t.Fatal(err)
	}

	if oldKnown && newKnown {
		t.Fatal("both peers map to the beneficiary after a failed migration, which is two " +
			"payment paths onto one chequebook; settle's per-peer gate is keyed by overlay " +
			"and does not serialize them")
	}
}

// TestMigratePeerFailureLeavesNoDoubleChequebook is the same property for the
// chequebook pair, which moves on the same argument and in the same order.
func TestMigratePeerFailureLeavesNoDoubleChequebook(t *testing.T) {
	t.Parallel()

	var (
		oldPeer     = swarm.MustParseHexAddress("aabb")
		newPeer     = swarm.MustParseHexAddress("ccdd")
		beneficiary = common.HexToAddress("0xfeed")
		chequebook  = common.HexToAddress("0xbeef")
	)

	// "swap_chequebook_peer_" is the peer to chequebook key, so matching on
	// "chequebook_peer" fails that delete and not the beneficiary one.
	store := &failingStore{StateStorer: mockstore.NewStateStore(), match: "chequebook_peer"}
	book := swap.NewAddressbook(store)

	if err := book.PutBeneficiary(oldPeer, beneficiary); err != nil {
		t.Fatal(err)
	}
	if err := book.PutChequebook(oldPeer, chequebook); err != nil {
		t.Fatal(err)
	}

	if err := book.MigratePeer(oldPeer, newPeer); !errors.Is(err, errInjected) {
		t.Fatalf("migrate: got %v, want the injected failure", err)
	}

	_, oldKnown, err := book.Chequebook(oldPeer)
	if err != nil {
		t.Fatal(err)
	}
	_, newKnown, err := book.Chequebook(newPeer)
	if err != nil {
		t.Fatal(err)
	}

	if oldKnown && newKnown {
		t.Fatal("both peers map to the chequebook after a failed migration")
	}
}

// TestMigratePeerMovesBothMappings guards the other direction: the reordering
// must not have dropped a write. A fix that simply deleted less would pass the
// two tests above.
func TestMigratePeerMovesBothMappings(t *testing.T) {
	t.Parallel()

	var (
		oldPeer     = swarm.MustParseHexAddress("aabb")
		newPeer     = swarm.MustParseHexAddress("ccdd")
		beneficiary = common.HexToAddress("0xfeed")
		chequebook  = common.HexToAddress("0xbeef")
	)

	book := swap.NewAddressbook(mockstore.NewStateStore())

	if err := book.PutBeneficiary(oldPeer, beneficiary); err != nil {
		t.Fatal(err)
	}
	if err := book.PutChequebook(oldPeer, chequebook); err != nil {
		t.Fatal(err)
	}
	if err := book.MigratePeer(oldPeer, newPeer); err != nil {
		t.Fatal(err)
	}

	gotBen, known, err := book.Beneficiary(newPeer)
	if err != nil || !known {
		t.Fatalf("the new peer has no beneficiary: known=%v err=%v", known, err)
	}
	if gotBen != beneficiary {
		t.Fatalf("beneficiary %v, want %v", gotBen, beneficiary)
	}
	gotCb, known, err := book.Chequebook(newPeer)
	if err != nil || !known {
		t.Fatalf("the new peer has no chequebook: known=%v err=%v", known, err)
	}
	if gotCb != chequebook {
		t.Fatalf("chequebook %v, want %v", gotCb, chequebook)
	}

	if _, known, _ := book.Beneficiary(oldPeer); known {
		t.Fatal("the old peer still maps to the beneficiary")
	}
	if _, known, _ := book.Chequebook(oldPeer); known {
		t.Fatal("the old peer still maps to the chequebook")
	}
}

// TestMigratePeerRetryCompletes is what makes the chosen failure mode
// recoverable rather than merely different: re-running an interrupted
// migration must finish it, which needs Delete on a missing key to be a no-op.
func TestMigratePeerRetryCompletes(t *testing.T) {
	t.Parallel()

	var (
		oldPeer     = swarm.MustParseHexAddress("aabb")
		newPeer     = swarm.MustParseHexAddress("ccdd")
		beneficiary = common.HexToAddress("0xfeed")
	)

	store := &failingStore{StateStorer: mockstore.NewStateStore(), match: "beneficiary"}
	book := swap.NewAddressbook(store)

	if err := book.PutBeneficiary(oldPeer, beneficiary); err != nil {
		t.Fatal(err)
	}
	if err := book.MigratePeer(oldPeer, newPeer); !errors.Is(err, errInjected) {
		t.Fatalf("first migrate: got %v, want the injected failure", err)
	}

	// The old mapping is gone, so the retry cannot read it back. That is the
	// availability gap the spec accepts, and the repair is the announcement
	// that calls PutBeneficiary again.
	if err := book.PutBeneficiary(oldPeer, beneficiary); err != nil {
		t.Fatal(err)
	}
	if err := book.MigratePeer(oldPeer, newPeer); err != nil {
		t.Fatalf("the retry should complete once the store stops failing: %v", err)
	}

	if _, known, _ := book.Beneficiary(newPeer); !known {
		t.Fatal("the retry did not move the beneficiary")
	}
	if _, known, _ := book.Beneficiary(oldPeer); known {
		t.Fatal("the retry left the old mapping in place")
	}
}
