// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storageincentives

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/log"
	erc20mock "github.com/ethersphere/bee/v2/pkg/settlement/swap/erc20/mock"
	"github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/redistribution"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	transactionmock "github.com/ethersphere/bee/v2/pkg/transaction/mock"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// The storage-incentives side of docs/experiments/send-everywhere/spec.md,
// part 6 (#819): tests 17, 18 and 19.

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), s)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func gaugeValue(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var d dto.Metric
	if err := m.Write(&d); err != nil {
		t.Fatal(err)
	}
	if d.Gauge != nil {
		return d.GetGauge().GetValue()
	}
	return d.GetCounter().GetValue()
}

// winnerContract wins every claim and records claims.
type winnerContract struct {
	*fakeContract
	claims atomic.Int32
}

func (c *winnerContract) IsWinner(context.Context) (bool, error) { return true, nil }

func (c *winnerContract) Claim(context.Context, redistribution.ChunkInclusionProofs) (common.Hash, error) {
	c.claims.Add(1)
	return common.Hash{0xc1}, nil
}

// Test 17: while a nonce is blocked the agent signs no commit and no
// claim, logs the skip once per round and phase and counts it; a reveal is
// still signed; once the nonce is free, the agent commits again.
func TestLotterySkipsWhileANonceIsBlocked(t *testing.T) {
	t.Parallel()

	var blocked atomic.Bool
	blocked.Store(true)
	txs := newFakeTxs()
	txs.extra = []transactionmock.Option{transactionmock.WithBlockedNonceFunc(func() (uint64, string, bool) {
		if blocked.Load() {
			return 5, "insufficient funds for gas * price + value", true
		}
		return 0, "", false
	})}
	f := newRevealFixture(t, mock.NewStateStore(), newFakeChain(13), txs)
	logs := &lockedBuffer{}
	f.agent.logger = log.NewLogger(t.Name(), log.WithSink(logs), log.WithVerbosity(log.VerbosityAll)).Build()
	winner := &winnerContract{fakeContract: f.contract}
	f.agent.contract = winner
	ctx := context.Background()

	for round := uint64(1); round <= 3; round++ {
		f.setSample(round)
		for range 2 { // the sample phase can publish the commit again
			if err := f.agent.handleCommit(ctx, round); err != nil {
				t.Fatalf("commit round %d: %v", round, err)
			}
		}
		f.state().SetHasRevealed(round)
		for range 2 {
			if err := f.agent.handleClaim(ctx, round); err != nil {
				t.Fatalf("claim round %d: %v", round, err)
			}
		}
		line := fmt.Sprintf("skipping round %d: nonce 5 blocked (insufficient funds", round)
		if n := logs.count(line); n != 2 {
			t.Fatalf("round %d: skip line logged %d times, want once per phase (2)\n%s", round, n, logs)
		}
	}
	if commits, _ := f.contract.counts(); commits != 0 {
		t.Fatalf("%d commits signed behind a blocked nonce", commits)
	}
	if n := winner.claims.Load(); n != 0 {
		t.Fatalf("%d claims signed behind a blocked nonce", n)
	}
	for _, phase := range []string{"commit", "claim"} {
		if got := gaugeValue(t, f.agent.metrics.SkippedBlockedNonce.WithLabelValues(phase)); got != 3 {
			t.Fatalf("skipped_blocked_nonce_total{phase=%q} = %v, want 3", phase, got)
		}
	}

	// a reveal owed for a commit mined before the block is still signed
	const owed = 4
	f.setSample(owed)
	if err := f.state().SetCommitKey(owed, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	f.chain.mu.Lock()
	f.chain.start = time.Now()
	f.chain.baseBlock = owed*testBlocksPerRound + testBlocksPerPhase + 1 // in the reveal phase
	f.chain.mu.Unlock()
	if err := f.agent.handleReveal(ctx, owed); err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if _, reveals := f.contract.counts(); reveals != 1 {
		t.Fatalf("%d reveals signed, want 1", reveals)
	}

	// freed: the next round commits
	blocked.Store(false)
	f.setSample(5)
	if err := f.agent.handleCommit(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if commits, _ := f.contract.counts(); commits != 1 {
		t.Fatalf("%d commits after the nonce was freed, want 1", commits)
	}
	if got := gaugeValue(t, f.agent.metrics.SkippedBlockedNonce.WithLabelValues("commit")); got != 3 {
		t.Fatalf("a commit without a blocked nonce was counted as skipped: %v", got)
	}
}

// guardChain is the chain the reveal guard reads.
type guardChain struct {
	mu         sync.Mutex
	block      uint64
	chainNonce uint64
	balance    *big.Int
	feeCap     *big.Int
	tip        *big.Int
}

func (c *guardChain) set(f func(c *guardChain)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c)
}

func (c *guardChain) BlockNumber(context.Context) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.block, nil
}

func (c *guardChain) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &types.Header{Number: new(big.Int).SetUint64(c.block), BaseFee: big.NewInt(1), Time: uint64(time.Now().Unix())}, nil
}

func (c *guardChain) NonceAt(context.Context, common.Address, *big.Int) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.chainNonce, nil
}

func (c *guardChain) BalanceAt(context.Context, common.Address, *big.Int) (*big.Int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return new(big.Int).Set(c.balance), nil
}

func (c *guardChain) SuggestedFeeAndTip(context.Context, *big.Int, int) (*big.Int, *big.Int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return new(big.Int).Set(c.feeCap), new(big.Int).Set(c.tip), nil
}

// guardTxs is the transaction service the reveal guard sees.
type guardTxs struct {
	mu        sync.Mutex
	stored    map[common.Hash]*transaction.StoredTransaction
	blocked   []transaction.BlockedTransaction
	lottery   map[common.Hash]bool
	cancelled map[common.Hash]bool
	cancelErr error    // CancelForReveal's error, with the hash: a send error
	calls     []string // "cancel <hash>" and "resend <hash>", in order
}

func (g *guardTxs) service() transaction.Service {
	return transactionmock.New(
		transactionmock.WithStoredTransactionFunc(func(h common.Hash) (*transaction.StoredTransaction, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			if st, ok := g.stored[h]; ok {
				return st, nil
			}
			return nil, errors.New("not stored")
		}),
		transactionmock.WithPendingTransactionsFunc(func() ([]common.Hash, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			out := make([]common.Hash, 0, len(g.stored))
			for h := range g.stored {
				out = append(out, h)
			}
			return out, nil
		}),
		transactionmock.WithBlockedBeforeFunc(func(nonce uint64) []transaction.BlockedTransaction {
			g.mu.Lock()
			defer g.mu.Unlock()
			var out []transaction.BlockedTransaction
			for _, b := range g.blocked {
				if b.Nonce < nonce {
					out = append(out, b)
				}
			}
			return out
		}),
		transactionmock.WithPendingStateFunc(func(h common.Hash) (transaction.PendingState, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.cancelled[h] {
				return transaction.PendingState{State: transaction.StateCancelledForReveal}, nil
			}
			return transaction.PendingState{State: transaction.StateBlocked}, nil
		}),
		transactionmock.WithIsLotteryTransactionFunc(func(h common.Hash) bool {
			g.mu.Lock()
			defer g.mu.Unlock()
			return g.lottery[h]
		}),
		transactionmock.WithCancelForRevealFunc(func(_ context.Context, h common.Hash, _ uint64) (common.Hash, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.cancelled[h] = true
			g.calls = append(g.calls, "cancel "+h.Hex())
			return common.Hash{0xcc, h[0]}, g.cancelErr
		}),
		transactionmock.WithResendTransactionFunc(func(_ context.Context, h common.Hash) (*transaction.ResendResult, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.calls = append(g.calls, "resend "+h.Hex())
			return &transaction.ResendResult{State: transaction.StatePending}, nil
		}),
	)
}

func (g *guardTxs) callList() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

const guardRound = 10

var (
	revealHash   = common.Hash{0x0e}
	blockerHash  = common.Hash{0x0b}
	blocker2Hash = common.Hash{0x0d}
	betweenHash  = common.Hash{0x0a}
)

type guardFixture struct {
	chain    *guardChain
	txs      *guardTxs
	revealer *Revealer
	logs     *lockedBuffer
}

// newGuardFixture: the round's reveal at nonce 3 is listed; the withdrawal
// at nonce 1 is blocked; a transaction at nonce 2 waits between them.
func newGuardFixture(t *testing.T) *guardFixture {
	t.Helper()
	g := &guardTxs{
		stored: map[common.Hash]*transaction.StoredTransaction{
			blockerHash: {Nonce: 1, GasLimit: 21_000, GasFeeCap: big.NewInt(100), GasTipCap: big.NewInt(10), Value: big.NewInt(0)},
			betweenHash: {Nonce: 2, GasLimit: 50_000, GasFeeCap: big.NewInt(200), GasTipCap: big.NewInt(10), Value: big.NewInt(7)},
			revealHash:  {Nonce: 3, GasLimit: 300_000, GasFeeCap: big.NewInt(300), GasTipCap: big.NewInt(10), Value: big.NewInt(0)},
		},
		blocked:   []transaction.BlockedTransaction{{Hash: blockerHash, Nonce: 1, Reason: "insufficient funds", Description: "withdraw"}},
		lottery:   map[common.Hash]bool{},
		cancelled: map[common.Hash]bool{},
	}
	chain := &guardChain{chainNonce: 1, balance: new(big.Int).Lsh(big.NewInt(1), 80), feeCap: big.NewInt(150), tip: big.NewInt(5)}
	logs := &lockedBuffer{}
	logger := log.NewLogger(t.Name(), log.WithSink(logs), log.WithVerbosity(log.VerbosityAll)).Build()
	txService := g.service()
	state, err := NewRedistributionState(log.Noop, common.Address{}, mock.NewStateStore(), erc20mock.New(), txService)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRevealer(state, nil, txService, chain, func() time.Duration { return time.Second }, DefaultBlocksPerRound, DefaultBlocksPerPhase, logger)
	if err := state.SetCommitKey(guardRound, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := state.AddRevealTx(guardRound, revealHash, r.revealStart(guardRound)); err != nil {
		t.Fatal(err)
	}
	return &guardFixture{chain: chain, txs: g, revealer: r, logs: logs}
}

// at runs a guard step with the given number of reveal-phase blocks left.
func (f *guardFixture) at(left uint64) {
	f.chain.set(func(c *guardChain) { c.block = f.revealer.claimStart(guardRound) - left })
	f.revealer.guardOwedReveal(context.Background(), guardRound)
}

// need is condition (b) for the fixture: the cancellation of each blocker,
// the transaction between and the reveal's gas.
func (f *guardFixture) need(blockers ...common.Hash) *big.Int {
	n := new(big.Int)
	for _, b := range blockers {
		feeCap, _ := transaction.CancellationFees(f.chain.feeCap, f.chain.tip, f.txs.stored[b])
		n.Add(n, new(big.Int).Mul(feeCap, big.NewInt(int64(transaction.CancelForRevealGas))))
	}
	if len(blockers) == 1 {
		n.Add(n, big.NewInt(50_000*200+7))
	}
	return n.Add(n, big.NewInt(300_000*300))
}

const owedLine = "reveal for round 10 owed behind blocked nonce 1 (insufficient funds)"

// Test 18: escalation at once, once; the gauge returns to 0 when the
// reveal is mined, and when the claim phase starts without it.
func TestRevealOwedBehindABlockedNonceIsEscalated(t *testing.T) {
	t.Parallel()

	for _, end := range []string{"reveal mined", "claim phase", "blocker mined"} {
		t.Run(end, func(t *testing.T) {
			t.Parallel()
			f := newGuardFixture(t)
			gauge := f.revealer.metrics.RevealOwedBehindBlockedNonce
			f.at(35)
			f.at(32)
			if n := f.logs.count(owedLine); n != 1 {
				t.Fatalf("escalation line logged %d times, want 1\n%s", n, f.logs)
			}
			if got := gaugeValue(t, gauge); got != 1 {
				t.Fatalf("gauge %v, want 1", got)
			}
			if calls := f.txs.callList(); len(calls) != 0 {
				t.Fatalf("acted with more than 20 blocks left: %v", calls)
			}
			switch end {
			case "reveal mined":
				f.revealer.state.SetHasRevealed(guardRound)
				f.at(30)
			case "claim phase":
				f.chain.set(func(c *guardChain) { c.block = f.revealer.claimStart(guardRound) })
				f.revealer.guardOwedReveal(context.Background(), guardRound)
			case "blocker mined":
				f.chain.set(func(c *guardChain) { c.chainNonce = 2 })
				f.at(30)
			}
			if got := gaugeValue(t, gauge); got != 0 {
				t.Fatalf("gauge %v after %s, want 0", got, end)
			}
		})
	}
}

// Test 19: the last-resort cancel and each condition that stops it.
func TestLastResortCancel(t *testing.T) {
	t.Parallel()

	t.Run("fires at 20 blocks, not before, cancellation first", func(t *testing.T) {
		t.Parallel()
		f := newGuardFixture(t)
		f.chain.set(func(c *guardChain) { c.balance = f.need(blockerHash) })
		f.at(cancelAtRemainingBlocks + 1)
		if calls := f.txs.callList(); len(calls) != 0 {
			t.Fatalf("acted with %d blocks left: %v", cancelAtRemainingBlocks+1, calls)
		}
		f.at(cancelAtRemainingBlocks)
		want := []string{"cancel " + blockerHash.Hex(), "resend " + revealHash.Hex()}
		if calls := f.txs.callList(); fmt.Sprint(calls) != fmt.Sprint(want) {
			t.Fatalf("calls %v, want %v", calls, want)
		}
		f.at(cancelAtRemainingBlocks - 3)
		if calls := f.txs.callList(); len(calls) != 2 {
			t.Fatalf("cancelled again: %v", calls)
		}
	})

	t.Run("does not fire", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name  string
			setup func(f *guardFixture)
			left  uint64
			line  string
		}{
			{"the blocker is a lottery transaction", func(f *guardFixture) { f.txs.lottery[blockerHash] = true }, 20, "not cancelling: lottery transaction; the stake will be frozen if the reveal is not mined by block"},
			{"the native balance is one wei short", func(f *guardFixture) {
				f.chain.balance = new(big.Int).Sub(f.need(blockerHash), big.NewInt(1))
			}, 20, "not cancelling: native balance too low; the stake will be frozen if the reveal is not mined by block"},
			{"fewer than 4 blocks left", func(*guardFixture) {}, minCancelMarginBlocks - 1, "not cancelling: fewer than 4 blocks left in the reveal phase; the stake will be frozen"},
			{"the blocker is mined", func(f *guardFixture) { f.chain.chainNonce = 2 }, 20, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				f := newGuardFixture(t)
				tc.setup(f)
				f.at(tc.left)
				if calls := f.txs.callList(); len(calls) != 0 {
					t.Fatalf("cancelled: %v", calls)
				}
				if tc.line != "" && f.logs.count(tc.line) != 1 {
					t.Fatalf("no line %q\n%s", tc.line, f.logs)
				}
			})
		}
	})

	t.Run("a cancellation sent with an error is still followed by the reveal", func(t *testing.T) {
		t.Parallel()
		f := newGuardFixture(t)
		f.txs.cancelErr = errors.New("connection reset")
		f.at(20)
		want := []string{"cancel " + blockerHash.Hex(), "resend " + revealHash.Hex()}
		if calls := f.txs.callList(); fmt.Sprint(calls) != fmt.Sprint(want) {
			t.Fatalf("calls %v, want %v", calls, want)
		}
	})

	t.Run("exactly enough balance fires", func(t *testing.T) {
		t.Parallel()
		f := newGuardFixture(t)
		f.chain.set(func(c *guardChain) { c.balance = f.need(blockerHash) })
		f.at(20)
		if calls := f.txs.callList(); len(calls) != 2 {
			t.Fatalf("calls %v", calls)
		}
	})

	twoBlockers := func(f *guardFixture) {
		delete(f.txs.stored, betweenHash)
		f.txs.stored[blocker2Hash] = &transaction.StoredTransaction{Nonce: 2, GasLimit: 21_000, GasFeeCap: big.NewInt(120), GasTipCap: big.NewInt(12), Value: big.NewInt(0)}
		f.txs.blocked = append(f.txs.blocked, transaction.BlockedTransaction{Hash: blocker2Hash, Nonce: 2, Reason: "insufficient funds"})
	}

	t.Run("two blockers, ascending", func(t *testing.T) {
		t.Parallel()
		f := newGuardFixture(t)
		twoBlockers(f)
		f.at(20)
		want := []string{"cancel " + blockerHash.Hex(), "cancel " + blocker2Hash.Hex(), "resend " + revealHash.Hex()}
		if calls := f.txs.callList(); fmt.Sprint(calls) != fmt.Sprint(want) {
			t.Fatalf("calls %v, want %v", calls, want)
		}
	})

	t.Run("one lottery blocker among two: none cancelled", func(t *testing.T) {
		t.Parallel()
		f := newGuardFixture(t)
		twoBlockers(f)
		f.txs.lottery[blocker2Hash] = true
		f.at(20)
		if calls := f.txs.callList(); len(calls) != 0 {
			t.Fatalf("cancelled: %v", calls)
		}
	})
}
