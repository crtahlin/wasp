// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction_test

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

// These tests shorten a package-wide interval, so they do not run in
// parallel; parallel tests resume only after them.
const testInterval = 10 * time.Millisecond

// sendsOf counts how often the fixture's backend got txHash.
func (f *broadcastFixture) sendsOf(txHash common.Hash) int {
	n := 0
	for _, tx := range f.sentTxs() {
		if tx.Hash() == txHash {
			n++
		}
	}
	return n
}

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(testInterval)
	}
}

// ambiguousSend makes a send that fails on the transport, as every endpoint
// failing would, and lets later sends succeed.
func (f *broadcastFixture) ambiguousSend(t *testing.T, s transaction.Service) common.Hash {
	t.Helper()
	f.mu.Lock()
	f.sendErr = errors.New("connection reset")
	f.mu.Unlock()
	txHash, err := s.Send(context.Background(), broadcastRequest(), 0)
	if err == nil || txHash == (common.Hash{}) {
		t.Fatalf("send: hash %s, error %v; want an ambiguous send", txHash, err)
	}
	f.mu.Lock()
	f.sendErr = nil
	f.mu.Unlock()
	return txHash
}

// An ambiguous transaction is sent again, unchanged, until the backend knows
// it, and then no more.
func TestAmbiguousTransactionIsRebroadcastUntilKnown(t *testing.T) {
	defer transaction.SetAmbiguousRebroadcastInterval(testInterval)()

	f := newBroadcastFixture(t)
	txHash := f.ambiguousSend(t, f.service)

	waitFor(t, "a re-broadcast", func() bool { return f.sendsOf(txHash) >= 2 })
	for _, tx := range f.sentTxs() {
		if tx.Hash() != txHash {
			t.Fatalf("sent %s, want only the original %s: a re-broadcast must not re-sign", tx.Hash(), txHash)
		}
	}

	f.mu.Lock()
	f.knownTxs[txHash] = true
	f.mu.Unlock()
	time.Sleep(5 * testInterval)
	n := f.sendsOf(txHash)
	time.Sleep(10 * testInterval)
	if m := f.sendsOf(txHash); m != n {
		t.Fatalf("still re-broadcast after the backend knew it: %d sends, then %d", n, m)
	}
}

// After a same-nonce replacement is stored, re-sending the original could let
// an endpoint without the replacement mine the original instead.
func TestAmbiguousTransactionIsNotRebroadcastAfterAReplacement(t *testing.T) {
	defer transaction.SetAmbiguousRebroadcastInterval(testInterval)()

	f := newBroadcastFixture(t)
	txHash := f.ambiguousSend(t, f.service)
	waitFor(t, "a re-broadcast", func() bool { return f.sendsOf(txHash) >= 2 })

	if _, err := f.service.ReplaceTransaction(context.Background(), txHash, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * testInterval)
	n := f.sendsOf(txHash)
	time.Sleep(10 * testInterval)
	if m := f.sendsOf(txHash); m != n {
		t.Fatalf("original re-broadcast after its replacement: %d sends, then %d", n, m)
	}
}

// Past AmbiguousPendingTimeout the transaction no longer holds its nonce, and
// is not sent again.
func TestAmbiguousTransactionIsNotRebroadcastAfterTheTimeout(t *testing.T) {
	defer transaction.SetAmbiguousRebroadcastInterval(testInterval)()

	f := newBroadcastFixture(t)
	txHash := f.ambiguousSend(t, f.service)
	waitFor(t, "a re-broadcast", func() bool { return f.sendsOf(txHash) >= 2 })

	var stored transaction.StoredTransaction
	if err := f.store.Get(transaction.StoredTransactionKey(txHash), &stored); err != nil {
		t.Fatal(err)
	}
	stored.Created = time.Now().Add(-transaction.AmbiguousPendingTimeout - time.Minute).Unix()
	if err := f.store.Put(transaction.StoredTransactionKey(txHash), stored); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * testInterval)
	n := f.sendsOf(txHash)
	time.Sleep(10 * testInterval)
	if m := f.sendsOf(txHash); m != n {
		t.Fatalf("re-broadcast after the ambiguity timeout: %d sends, then %d", n, m)
	}
}

// A node restarted while a transaction is ambiguous picks the re-broadcast up
// again from the store.
func TestAmbiguousTransactionIsRebroadcastAfterARestart(t *testing.T) {
	defer transaction.SetAmbiguousRebroadcastInterval(testInterval)()

	f := newBroadcastFixture(t)
	txHash := f.ambiguousSend(t, f.service)
	if err := f.service.Close(); err != nil {
		t.Fatal(err)
	}
	before := f.sendsOf(txHash)

	f.newService(t)
	waitFor(t, "a re-broadcast after the restart", func() bool { return f.sendsOf(txHash) > before })
}

// No copy goes out in the last two intervals before the ambiguity times out:
// after that the nonce may be given to another transaction (review of #775).
func TestRebroadcastWindowClosesBeforeTheTimeout(t *testing.T) {
	t.Parallel()

	now := time.Now()
	interval := 30 * time.Second
	for _, tc := range []struct {
		age  time.Duration
		open bool
	}{
		{0, true},
		{transaction.AmbiguousPendingTimeout - 2*interval - time.Second, true},
		{transaction.AmbiguousPendingTimeout - 2*interval, false},
		{transaction.AmbiguousPendingTimeout - interval, false},
		{transaction.AmbiguousPendingTimeout, false},
	} {
		if got := transaction.RebroadcastWindowOpen(now.Add(-tc.age), interval, now); got != tc.open {
			t.Errorf("age %s: window open %v, want %v", tc.age, got, tc.open)
		}
	}
}

func TestNewerThan(t *testing.T) {
	t.Parallel()

	old := &transaction.StoredTransaction{Created: 100, GasFeeCap: big.NewInt(10)}
	for _, tc := range []struct {
		name  string
		other *transaction.StoredTransaction
		newer bool
	}{
		{"created later", &transaction.StoredTransaction{Created: 101, GasFeeCap: big.NewInt(10)}, true},
		{"same second, higher fee cap", &transaction.StoredTransaction{Created: 100, GasFeeCap: big.NewInt(11)}, true},
		{"same second, same fee cap", &transaction.StoredTransaction{Created: 100, GasFeeCap: big.NewInt(10)}, false},
		{"created earlier", &transaction.StoredTransaction{Created: 99, GasFeeCap: big.NewInt(20)}, false},
	} {
		if got := transaction.NewerThan(tc.other, old); got != tc.newer {
			t.Errorf("%s: newer %v, want %v", tc.name, got, tc.newer)
		}
	}
}

// A replacement whose own send was ambiguous is re-broadcast; the older
// original does not stop it.
func TestAmbiguousReplacementIsRebroadcast(t *testing.T) {
	defer transaction.SetAmbiguousRebroadcastInterval(testInterval)()

	f := newBroadcastFixture(t)
	original := f.ambiguousSend(t, f.service)

	f.mu.Lock()
	f.sendErr = errors.New("connection reset")
	f.mu.Unlock()
	replacement, err := f.service.ReplaceTransaction(context.Background(), original, nil)
	if err == nil || replacement == (common.Hash{}) {
		t.Fatalf("replace: hash %s, error %v; want an ambiguous replacement", replacement, err)
	}
	f.mu.Lock()
	f.sendErr = nil
	f.mu.Unlock()

	waitFor(t, "a re-broadcast of the replacement", func() bool { return f.sendsOf(replacement) >= 2 })
}

// Once the nonce is used on chain, by this transaction or another, the
// transaction is not sent again.
func TestAmbiguousTransactionIsNotRebroadcastOnceItsNonceIsUsed(t *testing.T) {
	defer transaction.SetAmbiguousRebroadcastInterval(testInterval)()

	f := newBroadcastFixture(t)
	txHash := f.ambiguousSend(t, f.service)
	waitFor(t, "a re-broadcast", func() bool { return f.sendsOf(txHash) >= 2 })

	stored, err := f.service.StoredTransaction(txHash)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.onchain = stored.Nonce + 1
	f.mu.Unlock()
	time.Sleep(5 * testInterval)
	n := f.sendsOf(txHash)
	time.Sleep(10 * testInterval)
	if m := f.sendsOf(txHash); m != n {
		t.Fatalf("re-broadcast after its nonce was used: %d sends, then %d", n, m)
	}
}
