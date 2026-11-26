// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storageincentives

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	chaincfg "github.com/ethersphere/bee/v2/pkg/config"
	signermock "github.com/ethersphere/bee/v2/pkg/crypto/mock"
	"github.com/ethersphere/bee/v2/pkg/log"
	erc20mock "github.com/ethersphere/bee/v2/pkg/settlement/swap/erc20/mock"
	"github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/redistribution"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/backendmock"
	transactionmock "github.com/ethersphere/bee/v2/pkg/transaction/mock"
	"github.com/ethersphere/bee/v2/pkg/transaction/monitormock"
	"github.com/ethersphere/bee/v2/pkg/util/abiutil"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

// Phases long enough for a replacement after revealReplaceAfterBlocks:
// round 1 commits in blocks 36-47, reveals in 48-59 and claims in 60-71.
const (
	ptBlocksPerRound = 36
	ptBlocksPerPhase = 12
	ptCommitStart    = ptBlocksPerRound
	ptRevealStart    = ptBlocksPerRound + ptBlocksPerPhase
	ptClaimStart     = ptBlocksPerRound + 2*ptBlocksPerPhase
	ptRoundEnd       = 2 * ptBlocksPerRound
)

var lotteryABI = abiutil.MustParseABI(chaincfg.Testnet.RedistributionABI)

// roundCtx bounds a test's commit, reveal or claim to the rest of the
// round, so a send that never returns fails the test with a message
// instead of a hang.
func roundCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), ptRoundEnd*testBlockTime)
	t.Cleanup(cancel)
	return ctx
}

type signedAt struct {
	tx    *types.Transaction
	block uint64
}

// lotteryChain is the chain behind a real transaction service: the
// endpoint suggests a 1 wei tip, eth_feeHistory reports historyTip for
// every block, and a transaction is mined only when mineOnSign says so.
type lotteryChain struct {
	chain *fakeChain

	mu         sync.Mutex
	historyTip *big.Int
	signed     []signedAt
	mined      map[common.Hash]bool
	// mineOnSign, if set, is asked for the n-th signed transaction
	// (from 1) and mines it at once when it answers true.
	mineOnSign func(n int) bool
	// afterSign, if set, is called after the n-th signing.
	afterSign func(n int)
}

func (c *lotteryChain) setHistoryTip(v *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.historyTip = v
}

func (c *lotteryChain) signedTxs() []signedAt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]signedAt(nil), c.signed...)
}

func (c *lotteryChain) sign(tx *types.Transaction, _ *big.Int) (*types.Transaction, error) {
	c.mu.Lock()
	c.signed = append(c.signed, signedAt{tx: tx, block: c.chain.block()})
	n := len(c.signed)
	if c.mineOnSign != nil && c.mineOnSign(n) {
		c.mined[tx.Hash()] = true
	}
	after := c.afterSign
	c.mu.Unlock()
	if after != nil {
		after(n)
	}
	return tx, nil
}

func (c *lotteryChain) find(h common.Hash) (*types.Transaction, bool) {
	for _, s := range c.signed {
		if s.tx.Hash() == h {
			return s.tx, c.mined[h]
		}
	}
	return nil, false
}

func (c *lotteryChain) backend() transaction.Backend {
	return backendmock.New(
		backendmock.WithBlockNumberFunc(c.chain.BlockNumber),
		backendmock.WithHeaderbyNumberFunc(c.chain.HeaderByNumber),
		backendmock.WithPendingNonceAtFunc(func(context.Context, common.Address) (uint64, error) { return 0, nil }),
		backendmock.WithNonceAtFunc(func(context.Context, common.Address, *big.Int) (uint64, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			if len(c.mined) > 0 {
				return 1, nil
			}
			return 0, nil
		}),
		backendmock.WithSuggestedFeeAndTipFunc(func(ctx context.Context, _ *big.Int, _ int) (*big.Int, *big.Int, error) {
			header, _ := c.chain.HeaderByNumber(ctx, nil)
			fee := new(big.Int).Mul(header.BaseFee, big.NewInt(2))
			return fee.Add(fee, big.NewInt(1)), big.NewInt(1), nil
		}),
		backendmock.WithFeeHistoryFunc(func(context.Context, uint64, *big.Int, []float64) (*ethereum.FeeHistory, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			h := &ethereum.FeeHistory{OldestBlock: big.NewInt(1)}
			for range 20 {
				h.Reward = append(h.Reward, []*big.Int{new(big.Int).Set(c.historyTip)})
			}
			return h, nil
		}),
		backendmock.WithSendTransactionFunc(func(context.Context, *types.Transaction) error { return nil }),
		backendmock.WithTransactionReceiptFunc(func(_ context.Context, h common.Hash) (*types.Receipt, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.mined[h] {
				return &types.Receipt{Status: types.ReceiptStatusSuccessful, TxHash: h, BlockNumber: big.NewInt(1)}, nil
			}
			return nil, ethereum.NotFound
		}),
		backendmock.WithTransactionByHashFunc(func(_ context.Context, h common.Hash) (*types.Transaction, bool, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			if tx, mined := c.find(h); tx != nil {
				return tx, !mined, nil
			}
			return nil, false, ethereum.NotFound
		}),
		backendmock.WithCallContractFunc(func(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
			return nil, errors.New("no contract")
		}),
	)
}

// lotteryHarness is an agent on the real redistribution contract and the
// real transaction service, over lotteryChain.
type lotteryHarness struct {
	chain *lotteryChain
	agent *Agent
	state *RedistributionState
}

func newLotteryHarness(t *testing.T, block uint64) *lotteryHarness {
	t.Helper()
	c := &lotteryChain{chain: newFakeChain(block), historyTip: big.NewInt(2_000_000), mined: make(map[common.Hash]bool)}
	svc, err := transaction.NewService(log.Noop, common.Address{}, c.backend(), signermock.New(signermock.WithSignTxFunc(c.sign)),
		mock.NewStateStore(), big.NewInt(5),
		monitormock.New(monitormock.WithWatchTransactionFunc(func(common.Hash, uint64) (<-chan types.Receipt, <-chan error, error) {
			return nil, nil, nil
		})), 0)
	if err != nil {
		t.Fatal(err)
	}
	testutil.CleanupCloser(t, svc)
	contract := redistribution.New(swarm.RandAddress(t), common.Address{}, log.Noop, svc, common.HexToAddress("0xffff"), lotteryABI, 1_000_000)
	state, err := NewRedistributionState(log.Noop, common.Address{}, mock.NewStateStore(), erc20mock.New(), svc)
	if err != nil {
		t.Fatal(err)
	}
	revealer := NewRevealer(state, contract, svc, c.chain, func() time.Duration { return testBlockTime }, ptBlocksPerRound, ptBlocksPerPhase, log.Noop)
	return &lotteryHarness{
		chain: c,
		state: state,
		agent: &Agent{logger: log.Noop, metrics: newMetrics(), contract: contract, revealer: revealer, state: state, overlay: swarm.RandAddress(t)},
	}
}

// checkReplacedOnce checks that the transaction was signed twice, the
// second time revealReplaceAfterBlocks or more after the first and before
// end, with a tip priced from the fee history as it was then.
func checkReplacedOnce(t *testing.T, signed []signedAt, end uint64, wantTip *big.Int) {
	t.Helper()
	if len(signed) != 2 {
		t.Fatalf("%d transactions signed, want the first and one replacement", len(signed))
	}
	first, second := signed[0], signed[1]
	if second.tx.Nonce() != first.tx.Nonce() {
		t.Fatalf("replacement nonce %d, want %d", second.tx.Nonce(), first.tx.Nonce())
	}
	if second.block < first.block+revealReplaceAfterBlocks {
		t.Fatalf("replaced at block %d, %d blocks after the send at %d, want at least %d", second.block, second.block-first.block, first.block, revealReplaceAfterBlocks)
	}
	if second.block >= end {
		t.Fatalf("replaced at block %d, after the phase ended at %d", second.block, end)
	}
	if first.tx.GasTipCap().Cmp(big.NewInt(2_200_000)) < 0 {
		t.Fatalf("first tip %s, want the history tip of at least 2200000", first.tx.GasTipCap())
	}
	if second.tx.GasTipCap().Cmp(wantTip) < 0 {
		t.Fatalf("replacement tip %s, want the history tip at the replacement, at least %s", second.tx.GasTipCap(), wantTip)
	}
}

// A commit no block includes is replaced after revealReplaceAfterBlocks
// with a tip priced from the history at that time; the stored commit hash
// is the replacement's, and once the replacement is mined the commit
// returns without an error inside the commit phase (#796 test 5).
func TestCommitReplacedWithinPhase(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		h := newLotteryHarness(t, ptCommitStart)
		h.chain.mineOnSign = func(n int) bool { return n == 2 }
		h.chain.afterSign = func(n int) {
			if n == 1 {
				h.chain.setHistoryTip(big.NewInt(3_000_000)) // blocks now include more
			}
		}
		const round = 1
		h.state.SetSampleData(round-1, SampleData{ReserveSampleHash: swarm.NewAddress(make([]byte, swarm.HashSize)), StorageRadius: 1}, 0)
		sample, _ := h.state.SampleData(round - 1)

		err := h.agent.commit(roundCtx(t), sample, round)
		returnedAt := h.chain.chain.block()
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		if returnedAt >= ptRevealStart {
			t.Fatalf("commit returned at block %d, after the commit phase", returnedAt)
		}
		signed := h.chain.signedTxs()
		checkReplacedOnce(t, signed, ptRevealStart, big.NewInt(3_300_000))
		if got := h.state.CommitTx(round); got != signed[1].tx.Hash() {
			t.Fatalf("stored commit %s, want the replacement %s", got, signed[1].tx.Hash())
		}
	})
}

// A claim no block includes is replaced within the claim phase as the
// commit is (#796 test 6).
func TestClaimReplacedWithinPhase(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		h := newLotteryHarness(t, ptClaimStart)
		h.chain.mineOnSign = func(n int) bool { return n == 2 }
		h.chain.afterSign = func(n int) {
			if n == 1 {
				h.chain.setHistoryTip(big.NewInt(3_000_000))
			}
		}

		txHash, err := h.agent.claim(roundCtx(t), 1, redistribution.ChunkInclusionProofs{})
		returnedAt := h.chain.chain.block()
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if returnedAt >= ptRoundEnd {
			t.Fatalf("claim returned at block %d, after the claim phase", returnedAt)
		}
		signed := h.chain.signedTxs()
		checkReplacedOnce(t, signed, ptRoundEnd, big.NewInt(3_300_000))
		if txHash != signed[1].tx.Hash() {
			t.Fatalf("claim returned %s, want the mined replacement %s", txHash, signed[1].tx.Hash())
		}
	})
}

// ptFixture is a revealFixture with the longer phases above, at block.
func ptFixture(t *testing.T, block uint64) *revealFixture {
	t.Helper()
	chain := newFakeChain(block)
	txs := newFakeTxs()
	f := &revealFixture{
		store: mock.NewStateStore(), chain: chain, txs: txs,
		contract:       &fakeContract{txs: txs, chain: chain},
		blocksPerRound: ptBlocksPerRound, blocksPerPhase: ptBlocksPerPhase,
	}
	f.restart(t)
	f.setSample(1)
	return f
}

// A base fee that rises by 12.5 % per block, the most a full block allows:
// the pending commit is replaced before the base fee passes its fee cap,
// which is before revealReplaceAfterBlocks (#796 test 7). The fee cap is
// 100 and the base fee 64 at the send, so the base fee passes it 4 blocks
// later.
func TestCommitReplacedBeforeFeeCapPassed(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f := ptFixture(t, ptCommitStart)
		f.chain.baseFeeAt = func(block uint64) *big.Int {
			n := int64(block - ptCommitStart)
			v := new(big.Int).Mul(big.NewInt(64), new(big.Int).Exp(big.NewInt(9), big.NewInt(n), nil))
			return v.Div(v, new(big.Int).Exp(big.NewInt(8), big.NewInt(n), nil))
		}
		passedAt := uint64(ptCommitStart)
		for f.chain.baseFeeAt(passedAt).Cmp(big.NewInt(100)) <= 0 {
			passedAt++
		}

		f.contract.commitResult = func(ctx context.Context, h common.Hash) error {
			f.txs.set(h, transaction.TxPending)
			<-ctx.Done()
			return ctx.Err()
		}
		var mu sync.Mutex
		var replacedAt []uint64
		f.txs.onReplace = func(n common.Hash) {
			mu.Lock()
			replacedAt = append(replacedAt, f.chain.block())
			mu.Unlock()
			go func() {
				time.Sleep(testBlockTime)
				f.txs.set(n, transaction.TxMined)
			}()
		}
		sample, _ := f.state().SampleData(0)

		if err := f.agent.commit(roundCtx(t), sample, 1); err != nil {
			t.Fatalf("commit: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(replacedAt) == 0 {
			t.Fatal("the commit was not replaced")
		}
		if replacedAt[0] >= passedAt {
			t.Fatalf("first replacement at block %d, the base fee passed the fee cap at block %d", replacedAt[0], passedAt)
		}
	})
}

// The first commit is mined after its replacement was sent: the commit
// returns done, the stored commit is the replacement, which then reads as
// cancelled with its nonce used, and the revealer reveals (#796 test 8).
func TestOlderCommitMined(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f := ptFixture(t, ptCommitStart)
		var mu sync.Mutex
		var first, replacement common.Hash
		f.contract.commitResult = func(ctx context.Context, h common.Hash) error {
			mu.Lock()
			first = h
			mu.Unlock()
			f.txs.set(h, transaction.TxPending)
			<-ctx.Done()
			return ctx.Err()
		}
		f.txs.onReplace = func(n common.Hash) {
			mu.Lock()
			replacement = n
			h := first
			mu.Unlock()
			go func() {
				time.Sleep(testBlockTime)
				f.txs.set(h, transaction.TxMined)
			}()
		}
		sample, _ := f.state().SampleData(0)

		if err := f.agent.commit(roundCtx(t), sample, 1); err != nil {
			t.Fatalf("commit: %v", err)
		}
		mu.Lock()
		repl := replacement
		mu.Unlock()
		if repl == (common.Hash{}) {
			t.Fatal("no replacement sent")
		}
		if got := f.state().CommitTx(1); got != repl {
			t.Fatalf("stored commit %s, want the replacement %s", got, repl)
		}

		f.txs.set(repl, transaction.TxCancelled)
		f.chain.setNonceAt(func(uint64) (uint64, error) { return 1, nil })
		if !f.revealer.revealUntil(roundCtx(t), 1) {
			t.Fatal("not revealed after the older commit was mined")
		}
		if _, reveals := f.contract.counts(); reveals != 1 {
			t.Fatalf("%d reveals, want 1", reveals)
		}
	})
}

// fundsBackend has a balance that covers minTxCountToCover transactions at
// the endpoint's 1 wei tip but not at the history tip.
type fundsBackend struct {
	ChainBackend
	history error // FeeHistory fails with it when set
}

func (b *fundsBackend) BalanceAt(context.Context, common.Address, *big.Int) (*big.Int, error) {
	return big.NewInt(1_000_000_000), nil
}

func (b *fundsBackend) SuggestedFeeAndTip(context.Context, *big.Int, int) (*big.Int, *big.Int, error) {
	return big.NewInt(2*100 + 1), big.NewInt(1), nil
}

func (b *fundsBackend) FeeHistory(context.Context, uint64, *big.Int, []float64) (*ethereum.FeeHistory, error) {
	if b.history != nil {
		return nil, b.history
	}
	h := &ethereum.FeeHistory{OldestBlock: big.NewInt(1)}
	for range 20 {
		h.Reward = append(h.Reward, []*big.Int{big.NewInt(2_000_000)})
	}
	return h, nil
}

// The balance check prices with the history tip: a balance that covers 15
// transactions at a 1 wei tip but not at the history tip does not play
// (#796 test 9). Without a history it plays, as before.
func TestFundsCheckUsesHistoryTip(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		history error
		play    bool
	}{
		{"history tip", nil, false},
		{"no history", errors.New("method not found"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state, err := NewRedistributionState(log.Noop, common.Address{}, mock.NewStateStore(), erc20mock.New(), transactionmock.New())
			if err != nil {
				t.Fatal(err)
			}
			a := &Agent{logger: log.Noop, backend: &fundsBackend{history: tc.history}, state: state}

			minBalance, play, err := a.HasEnoughFundsToPlay(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if play != tc.play {
				t.Fatalf("plays %v with a balance of 1000000000 against a minimum of %s, want %v", play, minBalance, tc.play)
			}
		})
	}
}
