// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package accounting_test

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/accounting"
	"github.com/ethersphere/bee/v2/pkg/log"
	p2pmock "github.com/ethersphere/bee/v2/pkg/p2p/mock"
	"github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// announceRecorder records every threshold announced, and can be told to call
// back while it is announcing, which is how the "not under the peer lock" test
// gets inside the announcement.
type announceRecorder struct {
	mu      sync.Mutex
	got     []*big.Int
	peers   []swarm.Address
	during  func()
	entered atomic.Int32
	err     error // when set, every announcement fails
}

func (p *announceRecorder) AnnouncePaymentThreshold(_ context.Context, peer swarm.Address, t *big.Int) error {
	p.entered.Add(1)
	if p.during != nil {
		p.during()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, new(big.Int).Set(t))
	p.peers = append(p.peers, peer)
	return p.err
}

func (p *announceRecorder) values() []*big.Int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*big.Int, len(p.got))
	copy(out, p.got)
	return out
}

// The harness uses a payment threshold of 10,000, so these are expressed
// relative to it rather than in the production units: the ratio is what the
// behaviour depends on, and hard-coding 54,000,000 against a 10,000 default
// makes every grant exceed any sane budget.
const (
	provThreshold = 40_000                 // four times the harness default
	provDelta     = provThreshold - 10_000 // what one grant costs the budget
	provBudget    = provDelta              // room for exactly one grant
)

func newProviderAccounting(t *testing.T, budget int64) (*accounting.Accounting, *announceRecorder) {
	t.Helper()

	rec := &announceRecorder{}
	acc, err := accounting.NewAccounting(
		testPaymentThreshold, testPaymentTolerance, testPaymentEarly,
		log.Noop, mock.NewStateStore(), rec,
		big.NewInt(testRefreshRate), testLightFactor, p2pmock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}
	acc.SetProviderCredit(big.NewInt(provThreshold), big.NewInt(budget))
	return acc, rec
}

// currentThresholdGiven reads the disconnect limit derived from the granted
// threshold, which is what PeerInfo exposes of it.
func currentThresholdGiven(t *testing.T, acc *accounting.Accounting, peer swarm.Address) *big.Int {
	t.Helper()
	info, err := acc.PeerAccounting()
	if err != nil {
		t.Fatal(err)
	}
	pi, ok := info[peer.String()]
	if !ok {
		t.Fatalf("no accounting record for %s", peer)
	}
	return pi.CurrentThresholdGiven
}

func thresholdGiven(t *testing.T, acc *accounting.Accounting, peer swarm.Address) *big.Int {
	t.Helper()
	info, err := acc.PeerAccounting()
	if err != nil {
		t.Fatal(err)
	}
	pi, ok := info[peer.String()]
	if !ok {
		t.Fatalf("no accounting record for %s", peer)
	}
	return pi.ThresholdGiven
}

// TestProviderGrantRaisesAndAnnounces is the behaviour the feature exists for.
func TestProviderGrantRaisesAndAnnounces(t *testing.T) {
	t.Parallel()

	acc, rec := newProviderAccounting(t, provBudget)
	peer := swarm.MustParseHexAddress("00112233")
	acc.Connect(peer, true)

	before := thresholdGiven(t, acc, peer)
	acc.GrantProviderCredit(peer, true)
	after := thresholdGiven(t, acc, peer)

	if after.Int64() != provThreshold {
		t.Fatalf("threshold given is %s, want %d", after, provThreshold)
	}
	if before.Cmp(after) >= 0 {
		t.Fatalf("threshold did not rise: %s then %s", before, after)
	}

	sent := rec.values()
	if len(sent) != 1 {
		t.Fatalf("%d announcements, want exactly 1", len(sent))
	}
	if sent[0].Int64() != provThreshold {
		t.Fatalf("announced %s, want %d", sent[0], provThreshold)
	}
}

// TestProviderGrantOncePerConnection: a second local-only hit changes nothing,
// which is what makes one slot per peer follow from the rule rather than
// needing separate enforcement.
func TestProviderGrantOncePerConnection(t *testing.T) {
	t.Parallel()

	acc, rec := newProviderAccounting(t, provBudget)
	peer := swarm.MustParseHexAddress("00112233")
	acc.Connect(peer, true)

	for range 5 {
		acc.GrantProviderCredit(peer, true)
	}

	if n := len(rec.values()); n != 1 {
		t.Fatalf("%d announcements for 5 hits on one connection, want 1", n)
	}
	if got := thresholdGiven(t, acc, peer); got.Int64() != provThreshold {
		t.Fatalf("threshold given is %s, want %d", got, provThreshold)
	}
}

// TestProviderGrantSkipsLightPeers. Upstream gives a light peer a tenth of the
// threshold and it clears debt a tenth as fast, so the same grant would be far
// more exposure than intended and would hold its slot far longer.
func TestProviderGrantSkipsLightPeers(t *testing.T) {
	t.Parallel()

	acc, rec := newProviderAccounting(t, provBudget)
	peer := swarm.MustParseHexAddress("00112233")
	acc.Connect(peer, false)

	acc.GrantProviderCredit(peer, false)

	if n := len(rec.values()); n != 0 {
		t.Fatalf("a light peer was granted credit and announced to %d times", n)
	}
}

// TestProviderGrantSkipsUnconnected. Connect runs in a goroutine, so a request
// can arrive before the record is ready, and granting then would raise a
// threshold that Connect is about to reset.
func TestProviderGrantSkipsUnconnected(t *testing.T) {
	t.Parallel()

	acc, rec := newProviderAccounting(t, provBudget)
	peer := swarm.MustParseHexAddress("00112233")

	acc.GrantProviderCredit(peer, true) // never connected

	if n := len(rec.values()); n != 0 {
		t.Fatalf("a peer with no connected record was granted credit %d times", n)
	}
}

// TestProviderGrantOffByDefault.
func TestProviderGrantOffByDefault(t *testing.T) {
	t.Parallel()

	rec := &announceRecorder{}
	acc, err := accounting.NewAccounting(
		testPaymentThreshold, testPaymentTolerance, testPaymentEarly,
		log.Noop, mock.NewStateStore(), rec,
		big.NewInt(testRefreshRate), testLightFactor, p2pmock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}
	peer := swarm.MustParseHexAddress("00112233")
	acc.Connect(peer, true)
	acc.GrantProviderCredit(peer, true)

	if n := len(rec.values()); n != 0 {
		t.Fatalf("credit was granted with the feature unset, %d announcements", n)
	}
}

// TestProviderGrantRefusedWhenBudgetExhausted, and the peer is still served at
// the ordinary threshold rather than refused service.
func TestProviderGrantRefusedWhenBudgetExhausted(t *testing.T) {
	t.Parallel()

	// Room for one grant only.
	acc, rec := newProviderAccounting(t, provBudget)

	first := swarm.MustParseHexAddress("00112233")
	second := swarm.MustParseHexAddress("44556677")
	acc.Connect(first, true)
	acc.Connect(second, true)

	acc.GrantProviderCredit(first, true)
	acc.GrantProviderCredit(second, true)

	if n := len(rec.values()); n != 1 {
		t.Fatalf("%d grants against a budget of one, want 1", n)
	}
	if got := thresholdGiven(t, acc, second); got.Int64() != testPaymentThreshold.Int64() {
		t.Fatalf("the refused peer's threshold is %s, want the ordinary %s", got, testPaymentThreshold)
	}
}

// TestProviderGrantBudgetReleasedOnDisconnect, so that a slot does not leak
// until restart and a few peers cannot switch the feature off for everyone.
func TestProviderGrantBudgetReleasedOnDisconnect(t *testing.T) {
	t.Parallel()

	acc, rec := newProviderAccounting(t, provBudget)
	first := swarm.MustParseHexAddress("00112233")
	second := swarm.MustParseHexAddress("44556677")

	acc.Connect(first, true)
	acc.GrantProviderCredit(first, true)
	acc.Disconnect(first)

	acc.Connect(second, true)
	acc.GrantProviderCredit(second, true)

	if n := len(rec.values()); n != 2 {
		t.Fatalf("%d grants, want 2: the first peer's slot was not released on disconnect", n)
	}
}

// TestProviderGrantDisconnectTwiceReleasesOnce. terminate is registered as both
// DisconnectIn and DisconnectOut, so an initiated disconnect runs it twice. A
// release outside the connected check would free the slot twice and let the
// budget admit peers it never had room for.
func TestProviderGrantDisconnectTwiceReleasesOnce(t *testing.T) {
	t.Parallel()

	acc, rec := newProviderAccounting(t, provBudget)
	first := swarm.MustParseHexAddress("00112233")
	second := swarm.MustParseHexAddress("44556677")
	third := swarm.MustParseHexAddress("8899aabb")

	acc.Connect(first, true)
	acc.GrantProviderCredit(first, true)
	acc.Disconnect(first)
	acc.Disconnect(first) // the second, duplicate teardown

	acc.Connect(second, true)
	acc.GrantProviderCredit(second, true)
	acc.Connect(third, true)
	acc.GrantProviderCredit(third, true)

	if n := len(rec.values()); n != 2 {
		t.Fatalf("%d grants against a budget of one at a time; a double disconnect released the slot twice", n)
	}
}

// TestProviderGrantClearedOnReconnect. A grant belongs to one connection, and
// Connect knows nothing about it otherwise, so a reconnecting peer would carry
// a stale delta and could be granted again while still holding a slot.
func TestProviderGrantClearedOnReconnect(t *testing.T) {
	t.Parallel()

	acc, rec := newProviderAccounting(t, provBudget*4)
	peer := swarm.MustParseHexAddress("00112233")

	acc.Connect(peer, true)
	acc.GrantProviderCredit(peer, true)
	acc.Disconnect(peer)
	acc.Connect(peer, true)

	if got := thresholdGiven(t, acc, peer); got.Int64() != testPaymentThreshold.Int64() {
		t.Fatalf("after reconnect the threshold given is %s, want the ordinary %s", got, testPaymentThreshold)
	}

	acc.GrantProviderCredit(peer, true)
	if n := len(rec.values()); n != 2 {
		t.Fatalf("%d grants across two connections, want 2", n)
	}
	if got := thresholdGiven(t, acc, peer); got.Int64() != provThreshold {
		t.Fatalf("after regranting the threshold given is %s, want %d", got, provThreshold)
	}
}

// TestProviderGrantConcurrentLastSlot: two peers racing for the last slot must
// not both take it.
func TestProviderGrantConcurrentLastSlot(t *testing.T) {
	t.Parallel()

	acc, rec := newProviderAccounting(t, provBudget)
	peers := []swarm.Address{
		swarm.MustParseHexAddress("00112233"),
		swarm.MustParseHexAddress("44556677"),
		swarm.MustParseHexAddress("8899aabb"),
		swarm.MustParseHexAddress("ccddeeff"),
	}
	for _, p := range peers {
		acc.Connect(p, true)
	}

	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Add(1)
		go func(p swarm.Address) {
			defer wg.Done()
			acc.GrantProviderCredit(p, true)
		}(p)
	}
	wg.Wait()

	if n := len(rec.values()); n != 1 {
		t.Fatalf("%d peers were granted against a budget of one; the budget admission is not exclusive", n)
	}
}

// TestProviderGrantAnnouncesOutsideThePeerLock is the property that keeps this
// change from causing the failure it exists to remove. Announcing under the
// per-peer lock stalls every concurrent PrepareDebit for that peer for as long
// as the stream takes, and under a lookahead prefetch that is many requests at
// once.
func TestProviderGrantAnnouncesOutsideThePeerLock(t *testing.T) {
	t.Parallel()

	acc, rec := newProviderAccounting(t, provBudget)
	peer := swarm.MustParseHexAddress("00112233")
	acc.Connect(peer, true)

	// While the announcement is in flight, a debit for the same peer must still
	// be able to take the per-peer lock. If the announcement were made under it,
	// this would block until the announcement returned, which is the stall.
	debited := make(chan error, 1)
	rec.during = func() {
		go func() {
			_, err := acc.PrepareDebit(context.Background(), peer, 1000)
			debited <- err
		}()
		select {
		case err := <-debited:
			if err != nil {
				t.Errorf("a debit during the announcement failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			// A real deadline. An earlier version waited on
			// context.Background().Done(), which is a nil channel and never
			// fires, so announcing under the peer lock hung the whole package
			// until the ten-minute panic instead of failing here.
			t.Error("a debit could not proceed while an announcement was in flight, so the announcement is being made under the per-peer lock; every concurrent delivery to this peer would stall behind it")
		}
	}

	acc.GrantProviderCredit(peer, true)

	if rec.entered.Load() != 1 {
		t.Fatalf("the announcement ran %d times, want 1", rec.entered.Load())
	}
}

// TestProviderGrantRaisesDisconnectLimit. The spec requires the two to move
// together, and an earlier version of these tests asserted only the threshold.
// A raise that forgot the limit would blocklist exactly the peers it meant to
// help, because Apply blocklists on a balance crossing it.
func TestProviderGrantRaisesDisconnectLimit(t *testing.T) {
	t.Parallel()

	acc, _ := newProviderAccounting(t, provBudget)
	peer := swarm.MustParseHexAddress("00112233")
	acc.Connect(peer, true)

	before := currentThresholdGiven(t, acc, peer)
	acc.GrantProviderCredit(peer, true)
	after := currentThresholdGiven(t, acc, peer)

	if after.Cmp(before) <= 0 {
		t.Fatalf("the disconnect limit did not move with the threshold: %s then %s", before, after)
	}
}

// TestProviderGrantFailedAnnounceChangesNothing. A failed announcement cannot
// tell "the peer did not get it" from "the peer got it and the transport failed
// afterwards", so the spec resolves that ambiguity by changing nothing at all.
func TestProviderGrantFailedAnnounceChangesNothing(t *testing.T) {
	t.Parallel()

	rec := &announceRecorder{err: errors.New("stream reset")}
	acc, err := accounting.NewAccounting(
		testPaymentThreshold, testPaymentTolerance, testPaymentEarly,
		log.Noop, mock.NewStateStore(), rec,
		big.NewInt(testRefreshRate), testLightFactor, p2pmock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}
	acc.SetProviderCredit(big.NewInt(provThreshold), big.NewInt(provBudget))

	peer := swarm.MustParseHexAddress("00112233")
	acc.Connect(peer, true)
	acc.GrantProviderCredit(peer, true)

	// The grant stands: the peer may well have received it.
	if got := thresholdGiven(t, acc, peer); got.Int64() != provThreshold {
		t.Fatalf("after a failed announcement the threshold given is %s, want it left at %d", got, provThreshold)
	}

	// And the slot stays taken, so the budget still reflects what was granted.
	second := swarm.MustParseHexAddress("44556677")
	acc.Connect(second, true)
	acc.GrantProviderCredit(second, true)
	if got := thresholdGiven(t, acc, second); got.Int64() != testPaymentThreshold.Int64() {
		t.Fatal("a second peer was granted after a failed announcement, so the budget was released when it should not have been")
	}
}

// TestProviderGrantBudgetReleasedOnReconnect. Connect and Disconnect are
// dispatched with go and unordered, so a fast reconnect can run Connect first.
// Clearing the grant there without releasing the budget would strand the delta
// for the life of the process, and enough of those disable the feature in
// silence.
func TestProviderGrantBudgetReleasedOnReconnect(t *testing.T) {
	t.Parallel()

	acc, rec := newProviderAccounting(t, provBudget)
	first := swarm.MustParseHexAddress("00112233")
	second := swarm.MustParseHexAddress("44556677")

	acc.Connect(first, true)
	acc.GrantProviderCredit(first, true)

	// Reconnect without a Disconnect in between, which is the racing order.
	acc.Connect(first, true)

	// The slot the first connection held must be back.
	acc.Connect(second, true)
	acc.GrantProviderCredit(second, true)

	if n := len(rec.values()); n != 2 {
		t.Fatalf("%d grants, want 2: Connect cleared the grant without returning its budget, so the slot is stranded", n)
	}
}
