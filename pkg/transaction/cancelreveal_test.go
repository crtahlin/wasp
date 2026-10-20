// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/backendsimulation"
)

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

// Test 19, condition (a): a transaction to the redistribution contract is a
// lottery transaction whatever its marks say, as one stored by an older
// build is; so is one marked or described as a commit, reveal or claim, and
// an unknown one.
func TestIsLotteryTransaction(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessConfig{healthy: 1})
	contract := common.HexToAddress("0xc0ffee")
	other := common.HexToAddress("0x1234")
	h.svc.SetLotteryContract(contract)

	send := func(to common.Address, description string, selfReplacing bool) common.Hash {
		t.Helper()
		txHash, err := h.svc.Send(context.Background(), &transaction.TxRequest{
			To: &to, Value: big.NewInt(0), GasLimit: 21_000, Description: description, SelfReplacing: selfReplacing,
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return txHash
	}
	for _, tc := range []struct {
		name    string
		txHash  common.Hash
		lottery bool
	}{
		{"to the contract, unmarked, as an older build stores it", send(contract, "withdraw", false), true},
		{"marked self-replacing", send(other, "something", true), true},
		{"described as a commit", send(other, "commit transaction", false), true},
		{"described as a claim", send(other, "claim win transaction", false), true},
		{"a withdrawal", send(other, "withdraw", false), false},
		{"unknown", common.HexToHash("0x77"), true},
	} {
		if got := h.svc.IsLotteryTransaction(tc.txHash); got != tc.lottery {
			t.Errorf("%s: lottery %v, want %v", tc.name, got, tc.lottery)
		}
	}
}

// Test 19, the transaction side: the cancellation is stored, with Replaces
// and its reason, before it is sent; an endpoint that holds the blocker
// takes it as a replacement; it reaches every endpoint; the reveal's
// stored bytes follow unchanged; the cancellation is mined first, then the
// reveal; the withdrawal's caller gets ErrCancelledForReveal; the metric
// and the blocker's state say so.
func TestCancelForReveal(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessConfig{healthy: 2})
	cancelled := transaction.WatchdogMetrics(h.svc).CancelledForReveal

	withdrawal, err := h.send(context.Background(), "withdraw", 1)
	if err != nil {
		t.Fatal(err)
	}
	// the blocker cannot be mined: the base fee rose above its fee cap
	h.chain.SetBaseFee(new(big.Int).Add(h.stored(withdrawal).GasFeeCap, big.NewInt(1)))
	reveal, err := h.send(context.Background(), "reveal transaction", 0)
	if err != nil {
		t.Fatal(err)
	}
	revealRaw := h.stored(reveal).Raw
	h.chain.Mine()
	if h.chain.Receipt(withdrawal) != nil || h.chain.Receipt(reveal) != nil {
		t.Fatal("the test needs the blocker unmined")
	}

	var (
		mu           sync.Mutex
		storedBefore = true
		cancelHash   atomic.Value
	)
	h.chain.SetSendFunc(func(_ string, tx *types.Transaction) error {
		if tx.Nonce() != h.stored(withdrawal).Nonce || tx.Hash() == withdrawal {
			return nil
		}
		st, err := h.svc.StoredTransaction(tx.Hash())
		mu.Lock()
		defer mu.Unlock()
		if err != nil || st.Replaces != withdrawal || st.CancelReason != transaction.CancelReasonReveal {
			storedBefore = false
		}
		return nil
	})

	cancellation, err := h.svc.CancelForReveal(context.Background(), withdrawal, 7)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cancelHash.Store(cancellation)
	waitUntil(t, 5*time.Second, func() bool { return h.sendsOf(cancellation, "second") == 1 && h.sendsOf(cancellation, "third") == 1 })
	for _, s := range h.chain.Sends() {
		// the endpoints share one pool, so the copy is "already known"
		if s.Tx.Hash() == cancellation && transaction.ClassifySendAnswer(s.Err) != transaction.AnswerAccepted {
			t.Fatalf("%s refused the cancellation: %v", s.Endpoint, s.Err)
		}
	}
	mu.Lock()
	if !storedBefore {
		mu.Unlock()
		t.Fatal("the cancellation was sent before it was stored with Replaces and its reason")
	}
	mu.Unlock()
	c := h.stored(cancellation)
	if c.Value.Sign() != 0 || *c.To != h.sender || c.Nonce != h.stored(withdrawal).Nonce {
		t.Fatalf("not a zero-value transfer to self at the blocker's nonce: %+v", c)
	}
	if !atLeastTenPercentAbove(c.GasFeeCap, h.stored(withdrawal).GasFeeCap) || !atLeastTenPercentAbove(c.GasTipCap, h.stored(withdrawal).GasTipCap) {
		t.Fatal("the cancellation's fees are not 10 % above the blocker's")
	}
	if st, err := h.svc.PendingState(withdrawal); err != nil || st.State != transaction.StateCancelledForReveal {
		t.Fatalf("blocker state %+v, %v; want %s", st, err, transaction.StateCancelledForReveal)
	}

	before := h.sendsOf(reveal, "")
	if _, err := h.svc.ResendTransaction(context.Background(), reveal); err != nil {
		t.Fatalf("resending the reveal: %v", err)
	}
	if h.sendsOf(reveal, "") <= before || !bytesEqual(h.stored(reveal).Raw, revealRaw) {
		t.Fatal("the reveal's stored bytes were not sent again unchanged")
	}

	if _, err := h.waitReceipt(withdrawal); !errors.Is(err, transaction.ErrCancelledForReveal) {
		t.Fatalf("the withdrawal's caller got %v, want ErrCancelledForReveal", err)
	}
	r, err := h.waitReceipt(reveal)
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if r.TxHash != reveal {
		t.Fatal("the reveal was signed again")
	}
	cr := h.chain.Receipt(cancellation)
	if cr == nil || cr.BlockNumber.Cmp(r.BlockNumber) > 0 {
		t.Fatal("the cancellation was not mined before the reveal")
	}
	if got := metricValue(t, cancelled); got != 1 {
		t.Fatalf("cancelled_for_reveal_total %v, want 1", got)
	}
	h.check()
	if st, err := h.svc.PendingState(withdrawal); err != nil || st.State != transaction.StateCancelledForReveal {
		t.Fatalf("blocker state after settling %+v, %v; want %s", st, err, transaction.StateCancelledForReveal)
	}
	if n := h.logs.count(fmt.Sprintf("cancelled transaction %s at nonce %d to protect the reveal of round 7", withdrawal, c.Nonce)); n != 1 {
		t.Fatalf("cancellation line logged %d times", n)
	}
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

// Test 20: a randomized run of sends, black holes, refusals, replacements,
// cancellations, last-resort cancellations, restarts and long waits against
// the simulated chain. After each step: no two transactions the node signed
// share a nonce unless one's Replaces chain leads to the other, and no
// entry was removed while the chain nonce was at or below its nonce.
func TestNonceInvariant(t *testing.T) {
	t.Parallel()

	for seed := uint64(1); seed <= 8; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			runInvariant(t, seed, 80)
		})
	}
}

func runInvariant(t *testing.T, seed uint64, steps int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 819))
	h := newHarness(t, harnessConfig{blackhole: true, healthy: 2})

	var mode atomic.Int32 // 0 accept, 1 underpriced, 2 unreachable
	h.chain.SetSendFunc(func(string, *types.Transaction) error {
		switch mode.Load() {
		case 1:
			return backendsimulation.RPCError{Msg: "transaction underpriced"}
		case 2:
			return errors.New("connection refused")
		}
		return nil
	})

	tracked := make(map[common.Hash]uint64) // pending at the last check
	var log []string
	ctx := context.Background()
	for step := range steps {
		op := rng.IntN(11)
		switch op {
		case 0, 1:
			_, err := h.send(ctx, "withdraw", int64(rng.IntN(5)))
			log = append(log, fmt.Sprintf("send: %v", err))
		case 2:
			_, err := h.send(transaction.WithPersistentCalls(ctx, time.Second), "commit transaction", 0)
			log = append(log, fmt.Sprintf("persistent send: %v", err))
		case 3:
			mode.Store(int32(rng.IntN(3)))
			log = append(log, fmt.Sprintf("mode %d", mode.Load()))
		case 4:
			if target, ok := newestPending(t, h, rng); ok {
				_, err := h.svc.ReplaceTransaction(ctx, target, nil)
				log = append(log, fmt.Sprintf("replace %s: %v", target, err))
			}
		case 5:
			if target, ok := newestPending(t, h, rng); ok {
				_, err := h.svc.CancelTransaction(ctx, target)
				log = append(log, fmt.Sprintf("cancel %s: %v", target, err))
			}
		case 6:
			if target, ok := newestPending(t, h, rng); ok {
				_, err := h.svc.CancelForReveal(ctx, target, uint64(step))
				log = append(log, fmt.Sprintf("cancel for reveal %s: %v", target, err))
			}
		case 7, 8:
			n := 1 + rng.IntN(3)
			for range n {
				h.chain.Mine()
			}
			time.Sleep(20 * time.Millisecond) // the monitor delivers receipts
			log = append(log, fmt.Sprintf("mine %d", n))
		case 9:
			h.clock.advanceBlocks(rng.IntN(3 * transaction.OverdueBlocks))
			h.check()
			log = append(log, "wait and check")
		case 10:
			h.restart()
			log = append(log, "restart")
		}
		checkInvariant(t, h, tracked, log)
	}
}

// newestPending picks a pending nonce at random and returns its newest
// stored transaction: the one no other stored transaction replaces.
func newestPending(t *testing.T, h *harness, rng *rand.Rand) (common.Hash, bool) {
	t.Helper()
	all := storedAll(t, h)
	pending, err := h.svc.PendingTransactions()
	if err != nil || len(pending) == 0 {
		return common.Hash{}, false
	}
	nonce := all[pending[rng.IntN(len(pending))]].Nonce
	replaced := make(map[common.Hash]bool)
	for _, st := range all {
		if st.Replaces != (common.Hash{}) {
			replaced[st.Replaces] = true
		}
	}
	for _, p := range pending {
		if st := all[p]; st != nil && st.Nonce == nonce && !replaced[p] {
			return p, true
		}
	}
	return common.Hash{}, false
}

func storedAll(t *testing.T, h *harness) map[common.Hash]*transaction.StoredTransaction {
	t.Helper()
	out := make(map[common.Hash]*transaction.StoredTransaction)
	err := h.store.Iterate(transaction.StoredTransactionPrefix, func(key, value []byte) (bool, error) {
		var st transaction.StoredTransaction
		if err := json.Unmarshal(value, &st); err != nil {
			return true, err
		}
		out[common.HexToHash(string(key[len(transaction.StoredTransactionPrefix):]))] = &st
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func checkInvariant(t *testing.T, h *harness, tracked map[common.Hash]uint64, log []string) {
	t.Helper()
	all := storedAll(t, h)
	leadsTo := func(from, to common.Hash) bool {
		seen := map[common.Hash]bool{}
		for cur := from; cur != (common.Hash{}) && !seen[cur]; {
			if cur == to {
				return true
			}
			seen[cur] = true
			st := all[cur]
			if st == nil {
				return false
			}
			cur = st.Replaces
		}
		return false
	}
	byNonce := make(map[uint64][]common.Hash)
	for txHash, st := range all {
		byNonce[st.Nonce] = append(byNonce[st.Nonce], txHash)
	}
	for nonce, txs := range byNonce {
		for i := range txs {
			for j := i + 1; j < len(txs); j++ {
				if !leadsTo(txs[i], txs[j]) && !leadsTo(txs[j], txs[i]) {
					t.Fatalf("nonce %d used by %s and %s, neither replacing the other\nsteps: %v", nonce, txs[i], txs[j], log)
				}
			}
		}
	}

	chainNonce := h.chain.Nonce(h.sender)
	pending, err := h.svc.PendingTransactions()
	if err != nil {
		t.Fatal(err)
	}
	now := make(map[common.Hash]bool, len(pending))
	for _, p := range pending {
		now[p] = true
	}
	for txHash, nonce := range tracked {
		if !now[txHash] {
			if nonce >= chainNonce {
				t.Fatalf("entry %s at nonce %d removed while the chain nonce is %d\nsteps: %v", txHash, nonce, chainNonce, log)
			}
			delete(tracked, txHash)
		}
	}
	for _, p := range pending {
		if st := all[p]; st != nil {
			tracked[p] = st.Nonce
		}
	}
}
