// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package swap_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/settlement/swap"
	mockstore "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

var errInjected = errors.New("injected store failure")

// failingStore fails the first Put or the first Delete whose key contains the
// chosen substring, and passes everything else through.
//
// pkg/statestore/mock has no way to inject an error and these tests are
// entirely about what a failed write leaves behind, so the wrapper lives here
// rather than changing the shared mock for one caller.
//
// Failing a PUT is the case that matters and the one an earlier version of
// this file did not have. MigratePeer's first store operation decides which
// partial states are reachable, so a wrapper that can only fail Delete cannot
// reach the states a put-first migration leaves behind.
// The wrapper starts disarmed so that building the starting state cannot
// consume the one injected failure. Only arm() makes it live, and it fires
// once.
type failingStore struct {
	storage.StateStorer
	failPut    string
	failDelete string
	armed      bool
	putFailed  bool
	delFailed  bool
}

func (f *failingStore) arm() { f.armed = true }

func (f *failingStore) Put(key string, i interface{}) error {
	if f.armed && f.failPut != "" && !f.putFailed && strings.Contains(key, f.failPut) {
		f.putFailed = true
		return errInjected
	}
	return f.StateStorer.Put(key, i)
}

func (f *failingStore) Delete(key string) error {
	if f.armed && f.failDelete != "" && !f.delFailed && strings.Contains(key, f.failDelete) {
		f.delFailed = true
		return errInjected
	}
	return f.StateStorer.Delete(key)
}

// handshakeService builds a swap.Service around a real addressbook. Handshake
// reads only the addressbook and the logger, so the remaining dependencies are
// not exercised and are left nil deliberately rather than mocked.
func handshakeService(t *testing.T, book swap.Addressbook) *swap.Service {
	t.Helper()
	return swap.New(nil, log.Noop, nil, nil, nil, book, 1, nil, nil, common.Address{})
}

// TestMigratePeerPartialWriteLeavesTheHandshakeAbleToRepair is wasp #430, and
// it is the test that decides the issue.
//
// MigratePeer has no transaction available: storage.StateStorer offers only
// Get, Put, Delete, Iterate and Close, so a failure part way leaves the
// addressbook inconsistent whichever order the writes run in. The question is
// therefore not "is a partial state possible", which it always is, but
// **whether the node can recover from the partial state it leaves**.
//
// It can only recover through the swap handshake, which is the one thing that
// ever calls MigratePeer (swap.go:255-271):
//
//	oldPeer, known := BeneficiaryPeer(beneficiary)
//	if known && !peer.Equal(oldPeer) { return MigratePeer(oldPeer, peer) }
//	if _, known := Beneficiary(peer); !known { return PutBeneficiary(peer, ...) }
//
// The reverse mapping is the gate. If beneficiaryPeer[ba] names a peer whose
// forward mapping has been deleted, that branch is taken on every handshake and
// MigratePeer returns "old beneficiary not known" every time. Handshake is
// swapprotocol's ConnectIn AND ConnectOut, and libp2p disconnects the peer when
// either returns an error, so that state is not a missed payment. It is a peer
// this node can never complete a handshake with again, across restarts, with no
// repair path.
//
// So the property worth pinning is the recovery, not the ordering. Each case
// below fails one write, then asks whether a handshake on a healthy store puts
// the addressbook right.
func TestMigratePeerPartialWriteLeavesTheHandshakeAbleToRepair(t *testing.T) {
	t.Parallel()

	var (
		oldPeer     = swarm.MustParseHexAddress("aabb")
		newPeer     = swarm.MustParseHexAddress("ccdd")
		beneficiary = common.HexToAddress("0xfeed")
		chequebook  = common.HexToAddress("0xbeef")
	)

	// MigratePeer makes SIX store mutations, in this order:
	//
	//   1 PUT    swap_peer_beneficiary_<new>     PutBeneficiary, forward
	//   2 PUT    swap_beneficiary_peer_<ba>      PutBeneficiary, reverse
	//   3 DELETE swap_peer_beneficiary_<old>
	//   4 PUT    swap_chequebook_peer_<new>      PutChequebook, forward
	//   5 PUT    swap_peer_chequebook_<cb>       PutChequebook, reverse
	//   6 DELETE swap_chequebook_peer_<old>
	//
	// The wrapper matches on a substring of the key, so it can fail 1, 3, 4
	// and 6. The two reverse puts, 2 and 5, share no distinguishing substring
	// with anything else this test can target and are not reached here. That
	// is worth naming rather than glossing, because write 2 is the one whose
	// position relative to write 3 is the whole invariant.
	for _, tc := range []struct {
		name string
		// wantNewChequebook is whether the new peer holds the chequebook once
		// the recovery has run.
		//
		// It is not always true, and that is the point of asserting it. The
		// recovery repairs the BENEFICIARY mapping only. Handshake re-runs
		// MigratePeer just when the reverse beneficiary mapping still names
		// somebody else, so a migration that failed AFTER the beneficiary half
		// completed looks settled to the handshake and the chequebook half is
		// never finished. That leaves the new peer with no chequebook, which
		// ReceiveCheque treats as "not known" and repairs from the next cheque
		// it accepts, so it is a gap that closes rather than a wrong value.
		//
		// Asserting the true case is also what catches a reordering of the
		// chequebook pair on its own, which leaves the new peer unmapped where
		// the shipped order has already written it.
		wantNewChequebook bool
		failPut           string
		failDelete        string
	}{
		{
			// The discriminating case. Under the shipped order the failed put
			// is the FIRST store operation, so nothing has changed and the
			// retry is clean and completes the whole migration. Under a
			// delete-first order the old forward mapping is already gone while
			// the reverse mapping still names the old peer, which is the state
			// that wedges the handshake.
			name:              "the new beneficiary write fails",
			failPut:           "peer_beneficiary",
			wantNewChequebook: true,
		},
		{
			name:       "the old beneficiary delete fails",
			failDelete: "peer_beneficiary",
		},
		{
			// "swap_chequebook_peer_" is the peer to chequebook key, so this
			// matches that delete and not the beneficiary one, and not the
			// "swap_peer_chequebook_" reverse key either.
			name:              "the old chequebook delete fails",
			failDelete:        "chequebook_peer",
			wantNewChequebook: true,
		},
		{
			name:    "the new chequebook write fails",
			failPut: "chequebook_peer",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := &failingStore{
				StateStorer: mockstore.NewStateStore(),
				failPut:     tc.failPut,
				failDelete:  tc.failDelete,
			}
			book := swap.NewAddressbook(store)

			if err := book.PutBeneficiary(oldPeer, beneficiary); err != nil {
				t.Fatal(err)
			}
			if err := book.PutChequebook(oldPeer, chequebook); err != nil {
				t.Fatal(err)
			}

			store.arm()
			if err := book.MigratePeer(oldPeer, newPeer); !errors.Is(err, errInjected) {
				t.Fatalf("migrate: got %v, want the injected failure", err)
			}

			// The store is healthy again from here. Everything below is the
			// node's own recovery, with no help from the test.
			svc := handshakeService(t, book)

			for i := 1; i <= 3; i++ {
				if err := svc.Handshake(newPeer, beneficiary); err != nil {
					t.Fatalf("handshake %d after the failed migration: %v\n"+
						"this peer cannot complete a swap handshake, and because "+
						"Handshake is both ConnectIn and ConnectOut the node "+
						"disconnects it on every attempt, across restarts", i, err)
				}
			}

			// Recovery means the new peer is usable, not merely that the
			// handshake stopped returning an error.
			gotBen, known, err := book.Beneficiary(newPeer)
			if err != nil {
				t.Fatal(err)
			}
			if !known {
				t.Fatal("the new peer has no beneficiary after three handshakes, so it cannot be paid")
			}
			if gotBen != beneficiary {
				t.Fatalf("beneficiary %v, want %v", gotBen, beneficiary)
			}

			// The reverse mapping must name the new peer, otherwise the next
			// handshake takes the migrate branch again.
			gotPeer, known, err := book.BeneficiaryPeer(beneficiary)
			if err != nil {
				t.Fatal(err)
			}
			if !known || !gotPeer.Equal(newPeer) {
				t.Fatalf("the reverse mapping names %v known=%v, want the new peer %v",
					gotPeer, known, newPeer)
			}

			// The chequebook half moves on the same argument and in the same
			// order, and without this the chequebook pair could be reordered
			// on its own and nothing would notice.
			gotCb, known, err := book.Chequebook(newPeer)
			if err != nil {
				t.Fatal(err)
			}
			if known != tc.wantNewChequebook {
				t.Fatalf("the new peer's chequebook is known=%v, want %v",
					known, tc.wantNewChequebook)
			}
			if known && gotCb != chequebook {
				t.Fatalf("chequebook %v, want %v", gotCb, chequebook)
			}
		})
	}
}

// TestMigratePeerRefusesAnUnknownOldPeer pins the guard whose error message the
// whole of #430 turns on. MigratePeer returns "old beneficiary not known" when
// the old peer has no forward mapping, Handshake returns that error, and libp2p
// disconnects on it. Without this, the guard could be removed and the migration
// would silently write a zero beneficiary for the new peer instead.
func TestMigratePeerRefusesAnUnknownOldPeer(t *testing.T) {
	t.Parallel()

	var (
		oldPeer = swarm.MustParseHexAddress("aabb")
		newPeer = swarm.MustParseHexAddress("ccdd")
	)

	book := swap.NewAddressbook(mockstore.NewStateStore())

	if err := book.MigratePeer(oldPeer, newPeer); err == nil {
		t.Fatal("migrating a peer with no beneficiary succeeded, want an error")
	}

	if _, known, _ := book.Beneficiary(newPeer); known {
		t.Fatal("the refused migration still mapped the new peer, which would be a zero beneficiary")
	}
	if _, known, _ := book.BeneficiaryPeer(common.Address{}); known {
		t.Fatal("the refused migration wrote a reverse mapping for the zero beneficiary")
	}
}

// TestMigratePeerWithoutAChequebookMovesOnlyTheBeneficiary pins the other
// guard. A peer whose chequebook is not known must not acquire a zero one:
// Chequebook(newPeer) would then report known with a zero address, and
// ReceiveCheque compares against it, so every later cheque from that peer would
// be refused as the wrong chequebook.
func TestMigratePeerWithoutAChequebookMovesOnlyTheBeneficiary(t *testing.T) {
	t.Parallel()

	var (
		oldPeer     = swarm.MustParseHexAddress("aabb")
		newPeer     = swarm.MustParseHexAddress("ccdd")
		beneficiary = common.HexToAddress("0xfeed")
	)

	book := swap.NewAddressbook(mockstore.NewStateStore())

	if err := book.PutBeneficiary(oldPeer, beneficiary); err != nil {
		t.Fatal(err)
	}
	if err := book.MigratePeer(oldPeer, newPeer); err != nil {
		t.Fatal(err)
	}

	if _, known, _ := book.Beneficiary(newPeer); !known {
		t.Fatal("the beneficiary did not move")
	}
	if got, known, _ := book.Chequebook(newPeer); known {
		t.Fatalf("the new peer acquired a chequebook it never had: %v", got)
	}
	if _, known, _ := book.ChequebookPeer(common.Address{}); known {
		t.Fatal("the migration wrote a reverse mapping for the zero chequebook")
	}
}

// TestMigratePeerMovesBothMappings is the success path: nothing about the
// recovery argument above is worth anything if an uninterrupted migration does
// not move the mappings in the first place.
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

// TestMigratePeerMovesTheReverseMappings covers the two reverse keys, which had
// no coverage at all: deleting the reverse write from PutBeneficiary left the
// whole package passing. The reverse mapping is the only reason MigratePeer is
// ever reached, so an untested write there is the one that hurts.
func TestMigratePeerMovesTheReverseMappings(t *testing.T) {
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

	gotPeer, known, err := book.BeneficiaryPeer(beneficiary)
	if err != nil || !known || !gotPeer.Equal(oldPeer) {
		t.Fatalf("before the migration the beneficiary maps to %v known=%v err=%v, want %v",
			gotPeer, known, err, oldPeer)
	}
	gotPeer, known, err = book.ChequebookPeer(chequebook)
	if err != nil || !known || !gotPeer.Equal(oldPeer) {
		t.Fatalf("before the migration the chequebook maps to %v known=%v err=%v, want %v",
			gotPeer, known, err, oldPeer)
	}

	if err := book.MigratePeer(oldPeer, newPeer); err != nil {
		t.Fatal(err)
	}

	gotPeer, known, err = book.BeneficiaryPeer(beneficiary)
	if err != nil || !known || !gotPeer.Equal(newPeer) {
		t.Fatalf("after the migration the beneficiary maps to %v known=%v err=%v, want %v",
			gotPeer, known, err, newPeer)
	}
	gotPeer, known, err = book.ChequebookPeer(chequebook)
	if err != nil || !known || !gotPeer.Equal(newPeer) {
		t.Fatalf("after the migration the chequebook maps to %v known=%v err=%v, want %v",
			gotPeer, known, err, newPeer)
	}
}

// TestHandshakeMigratesWhenABeneficiaryAnnouncesFromANewPeer pins the one
// production route into MigratePeer, so the recovery argument above rests on
// the real call and not on the test calling MigratePeer directly.
func TestHandshakeMigratesWhenABeneficiaryAnnouncesFromANewPeer(t *testing.T) {
	t.Parallel()

	var (
		oldPeer     = swarm.MustParseHexAddress("aabb")
		newPeer     = swarm.MustParseHexAddress("ccdd")
		beneficiary = common.HexToAddress("0xfeed")
	)

	book := swap.NewAddressbook(mockstore.NewStateStore())
	svc := handshakeService(t, book)

	// First contact: no mapping exists, so the handshake establishes one.
	if err := svc.Handshake(oldPeer, beneficiary); err != nil {
		t.Fatal(err)
	}
	if _, known, _ := book.Beneficiary(oldPeer); !known {
		t.Fatal("the first handshake did not record the beneficiary")
	}

	// The same beneficiary announces from a different overlay, which is the
	// only thing that reaches MigratePeer.
	if err := svc.Handshake(newPeer, beneficiary); err != nil {
		t.Fatal(err)
	}
	if _, known, _ := book.Beneficiary(newPeer); !known {
		t.Fatal("the migration did not map the new peer")
	}
	if _, known, _ := book.Beneficiary(oldPeer); known {
		t.Fatal("the migration left the old peer mapped, which is two payment paths onto one chequebook")
	}

	// A repeat handshake from the peer that now holds the mapping is a no-op
	// rather than a second migration.
	if err := svc.Handshake(newPeer, beneficiary); err != nil {
		t.Fatal(err)
	}
}
