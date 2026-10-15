// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storageincentives

import (
	"context"
	"math/big"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

func (c *fakeChain) setBaseFee(fee *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseFee = fee
}

// revealLogSink is a log sink safe for concurrent writes.
type revealLogSink struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *revealLogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *revealLogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// round 1: reveal phase blocks 16-19, claim phase from block 20
const (
	feeTestRevealStart = testBlocksPerRound + testBlocksPerPhase
	feeTestClaimStart  = testBlocksPerRound + 2*testBlocksPerPhase
)

// pendingUntilCancelled makes the fake contract's reveal pending on chain and
// its receipt wait last until the step's context ends, as for a reveal no
// block includes.
func pendingUntilCancelled(f *revealFixture) {
	f.contract.revealResult = func(ctx context.Context, h common.Hash) error {
		f.txs.set(h, transaction.TxPending)
		<-ctx.Done()
		return ctx.Err()
	}
}

// The base fee rises above the first reveal's fee cap one block into the
// reveal phase. The reveal is replaced with raised fees inside the reveal
// phase, and nothing is sent at or after the first claim-phase block (#793
// tests 1 and 4). Before the change the first step waited for the receipt
// until the claim phase, so no replacement was made in time.
func TestRevealReplacedWhenBaseFeeOvertakesCap(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f := revealFixtureAt(t, feeTestRevealStart)
		pendingUntilCancelled(f)

		var mu sync.Mutex
		var replacedAt []uint64
		f.txs.onReplace = func(common.Hash) {
			mu.Lock()
			defer mu.Unlock()
			replacedAt = append(replacedAt, f.chain.block())
		}

		go func() {
			time.Sleep(testBlockTime)
			f.chain.setBaseFee(big.NewInt(1000)) // the stored fee cap is 100
		}()

		f.revealer.revealUntil(context.Background(), 1)

		mu.Lock()
		defer mu.Unlock()
		if len(replacedAt) == 0 {
			t.Fatal("no replacement in the reveal phase")
		}
		for _, b := range replacedAt {
			if b < feeTestRevealStart || b >= feeTestClaimStart {
				t.Fatalf("replaced at block %d, want inside the reveal phase [%d, %d)", b, feeTestRevealStart, feeTestClaimStart)
			}
		}
		f.contract.mu.Lock()
		attempts := append([]uint64(nil), f.contract.attemptAt...)
		f.contract.mu.Unlock()
		for _, b := range attempts {
			if b >= feeTestClaimStart {
				t.Fatalf("reveal sent at block %d, in the claim phase", b)
			}
		}
		f.txs.mu.Lock()
		rebroadcast := len(f.txs.rebroadcast)
		f.txs.mu.Unlock()
		if rebroadcast != 0 {
			t.Fatalf("%d rebroadcasts of a pending reveal", rebroadcast)
		}
		txs, _ := f.state().RevealTxs(1)
		if len(txs) != 1+len(replacedAt) {
			t.Fatalf("%d reveals listed, want the first and %d replacements", len(txs), len(replacedAt))
		}
	})
}

// A pending reveal whose fee cap the base fee can reach within
// revealFeeHeadroomBlocks full blocks is replaced; one with more room is
// not (#793 test 2). With a base fee of 100 the threshold is
// ceil(100*729/512) = 143.
func TestRevealReplacedBeforeBaseFeeReachesCap(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		feeCap  int64
		replace bool
	}{
		{142, true},
		{143, false},
	} {
		f := revealFixtureInRevealPhase(t)
		f.chain.setBaseFee(big.NewInt(100))
		h := f.txs.newHash()
		f.txs.set(h, transaction.TxPending)
		f.txs.feeCaps[h] = big.NewInt(tc.feeCap)
		if err := f.state().AddRevealTx(1, h, f.chain.block()); err != nil {
			t.Fatal(err)
		}

		if done, err := f.revealer.Reveal(context.Background(), 1); done || err != nil {
			t.Fatalf("fee cap %d: done %v err %v", tc.feeCap, done, err)
		}
		if got := len(f.txs.replaced) == 1; got != tc.replace {
			t.Fatalf("fee cap %d at base fee 100: replaced %v, want %v", tc.feeCap, got, tc.replace)
		}
	}
}

// A reveal mined within its first step ends the round with one send and no
// replacement (#793 test 3).
func TestRevealMinedNotReplaced(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f := revealFixtureAt(t, feeTestRevealStart)

		if !f.revealer.revealUntil(context.Background(), 1) {
			t.Fatal("reveal not marked as mined")
		}
		if _, reveals := f.contract.counts(); reveals != 1 {
			t.Fatalf("%d reveals, want 1", reveals)
		}
		if len(f.txs.replaced)+len(f.txs.rebroadcast) != 0 {
			t.Fatal("a mined reveal was replaced or rebroadcast")
		}
	})
}

// A reveal still pending within revealLateWarnBlocks of the claim phase is
// logged once at warning level (#793 test 6).
func TestRevealLateWarning(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f := revealFixtureAt(t, feeTestRevealStart)
		pendingUntilCancelled(f)
		sink := &revealLogSink{}
		f.revealer.logger = log.NewLogger(t.Name(), log.WithSink(sink), log.WithVerbosity(log.VerbosityWarning))

		f.revealer.revealUntil(context.Background(), 1)

		if n := strings.Count(sink.String(), "reveal still pending close to the claim phase"); n != 1 {
			t.Fatalf("%d late-reveal warnings, want 1; log:\n%s", n, sink.String())
		}
	})
}
