// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction_test

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
	signermock "github.com/ethersphere/bee/v2/pkg/crypto/mock"
	"github.com/ethersphere/bee/v2/pkg/log"
	storemock "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/backendmock"
	"github.com/ethersphere/bee/v2/pkg/transaction/monitormock"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

const historyTestBaseFee = 100

// historyChain is a backend whose suggested tip and fee history the test
// sets, and which records every signed transaction. A new fee history
// comes with a new block, since the service reads it once per block.
type historyChain struct {
	mu         sync.Mutex
	block      uint64
	tip        *big.Int   // the suggested tip
	rewards    []*big.Int // the 10th-percentile tip of each block, oldest first
	historyErr error
	hang       bool // FeeHistory waits until its context ends
	historyCtx context.Context
	calls      int // FeeHistory calls
	signed     []*types.Transaction
}

func (c *historyChain) setTip(v *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tip = v
}

// setRewards gives each of 20 blocks the same 10th-percentile tip; nil
// gives blocks without rewards.
func (c *historyChain) setRewards(v *big.Int) {
	rewards := make([]*big.Int, 20)
	for i := range rewards {
		rewards[i] = v
	}
	c.setRewardList(rewards)
}

// setRewardList gives the blocks of the history these 10th-percentile
// tips, oldest first, in a new block.
func (c *historyChain) setRewardList(rewards []*big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.historyErr = nil
	c.rewards = rewards
	c.block++
}

func (c *historyChain) setHistoryErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.historyErr = err
	c.block++
}

func (c *historyChain) nextBlock() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.block++
}

func (c *historyChain) historyCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *historyChain) last(t *testing.T) *types.Transaction {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.signed) == 0 {
		t.Fatal("nothing signed")
	}
	return c.signed[len(c.signed)-1]
}

func newHistoryService(t *testing.T, c *historyChain) transaction.Service {
	t.Helper()
	svc, err := transaction.NewService(log.Noop, common.HexToAddress("0xddff"),
		backendmock.New(
			backendmock.WithBlockNumberFunc(func(context.Context) (uint64, error) {
				c.mu.Lock()
				defer c.mu.Unlock()
				return c.block, nil
			}),
			backendmock.WithSendTransactionFunc(func(context.Context, *types.Transaction) error { return nil }),
			backendmock.WithPendingNonceAtFunc(func(context.Context, common.Address) (uint64, error) { return 1, nil }),
			backendmock.WithSuggestedFeeAndTipFunc(func(_ context.Context, _ *big.Int, _ int) (*big.Int, *big.Int, error) {
				c.mu.Lock()
				defer c.mu.Unlock()
				fee := new(big.Int).Add(big.NewInt(2*historyTestBaseFee), c.tip)
				return fee, new(big.Int).Set(c.tip), nil
			}),
			backendmock.WithFeeHistoryFunc(func(ctx context.Context, blocks uint64, _ *big.Int, percentiles []float64) (*ethereum.FeeHistory, error) {
				c.mu.Lock()
				c.historyCtx = ctx
				hang := c.hang
				if hang {
					c.calls++
				}
				c.mu.Unlock()
				if hang {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				c.mu.Lock()
				defer c.mu.Unlock()
				c.calls++
				if c.historyErr != nil {
					return nil, c.historyErr
				}
				if blocks != 20 || len(percentiles) != 1 || percentiles[0] != 10 {
					return nil, errors.New("unexpected fee history query")
				}
				h := &ethereum.FeeHistory{OldestBlock: big.NewInt(1)}
				for _, r := range c.rewards {
					if r == nil {
						h.Reward = append(h.Reward, nil)
						continue
					}
					h.Reward = append(h.Reward, []*big.Int{new(big.Int).Set(r)})
				}
				return h, nil
			}),
			backendmock.WithHeaderbyNumberFunc(func(context.Context, *big.Int) (*types.Header, error) {
				return &types.Header{Number: big.NewInt(10), BaseFee: big.NewInt(historyTestBaseFee)}, nil
			}),
		),
		signermock.New(signermock.WithSignTxFunc(func(tx *types.Transaction, _ *big.Int) (*types.Transaction, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.signed = append(c.signed, tx)
			return tx, nil
		})),
		storemock.NewStateStore(),
		big.NewInt(5),
		monitormock.New(monitormock.WithWatchTransactionFunc(func(common.Hash, uint64) (<-chan types.Receipt, <-chan error, error) {
			return nil, nil, nil
		})),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	testutil.CleanupCloser(t, svc)
	return svc
}

func historyRequest(fromHistory bool) *transaction.TxRequest {
	to := common.HexToAddress("0xabcd")
	return &transaction.TxRequest{
		To:             &to,
		GasLimit:       100_000,
		Value:          big.NewInt(0),
		TipFromHistory: fromHistory,
	}
}

// atLeastPercent fails unless got >= base * (100+percent) / 100.
func atLeastPercent(t *testing.T, what string, got, base *big.Int, percent int64) {
	t.Helper()
	want := new(big.Int).Mul(base, big.NewInt(100+percent))
	if new(big.Int).Mul(got, big.NewInt(100)).Cmp(want) < 0 {
		t.Fatalf("%s %s, want at least %d %% above %s", what, got, percent, base)
	}
}

// A lottery request is sent with the history tip, the median 10th-percentile
// tip plus 10 %, when the endpoint suggests 1 wei; a request without
// TipFromHistory keeps the suggestion (#796 and #846, test 1).
func TestTipFromHistoryAtFirstSend(t *testing.T) {
	t.Parallel()

	c := &historyChain{tip: big.NewInt(1)}
	c.setRewards(big.NewInt(2_000_000))
	svc := newHistoryService(t, c)

	if _, err := svc.Send(context.Background(), historyRequest(true), 0); err != nil {
		t.Fatal(err)
	}
	tx := c.last(t)
	if tx.GasTipCap().Cmp(big.NewInt(2_200_000)) < 0 {
		t.Fatalf("lottery tip %s, want at least 2200000", tx.GasTipCap())
	}
	if want := big.NewInt(2*historyTestBaseFee + 2_200_000); tx.GasFeeCap().Cmp(want) < 0 {
		t.Fatalf("lottery fee cap %s, want at least twice the base fee plus the tip, %s", tx.GasFeeCap(), want)
	}

	if _, err := svc.Send(context.Background(), historyRequest(false), 0); err != nil {
		t.Fatal(err)
	}
	if tip := c.last(t).GasTipCap(); tip.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("tip of a request without TipFromHistory %s, want the suggested 1 wei", tip)
	}
}

// A replacement of a stored lottery transaction with a 1 wei tip is priced
// from the history too, and both its fees are more than 10 % above the
// stored ones (test 2).
func TestTipFromHistoryAtReplacement(t *testing.T) {
	t.Parallel()

	c := &historyChain{tip: big.NewInt(1)}
	c.setHistoryErr(errors.New("method not found"))
	svc := newHistoryService(t, c)

	original, err := svc.Send(context.Background(), historyRequest(true), 0)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := svc.StoredTransaction(original)
	if err != nil {
		t.Fatal(err)
	}
	if stored.GasTipCap.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("original tip %s, want 1 wei", stored.GasTipCap)
	}
	if !stored.TipFromHistory {
		t.Fatal("TipFromHistory not stored")
	}

	c.setRewards(big.NewInt(2_000_000))
	replacement, err := svc.ReplaceTransaction(context.Background(), original, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx := c.last(t)
	if tx.GasTipCap().Cmp(big.NewInt(2_200_000)) < 0 {
		t.Fatalf("replacement tip %s, want at least 2200000", tx.GasTipCap())
	}
	atLeastPercent(t, "replacement tip", tx.GasTipCap(), stored.GasTipCap, 10)
	atLeastPercent(t, "replacement fee cap", tx.GasFeeCap(), stored.GasFeeCap, 10)
	if want := transaction.Bumped(stored.GasFeeCap); tx.GasFeeCap().Cmp(want) < 0 {
		t.Fatalf("replacement fee cap %s, want at least the stored cap plus 12 %%, %s", tx.GasFeeCap(), want)
	}
	if want := new(big.Int).Add(big.NewInt(2*historyTestBaseFee), tx.GasTipCap()); tx.GasFeeCap().Cmp(want) < 0 {
		t.Fatalf("replacement fee cap %s, want at least twice the base fee plus the tip, %s", tx.GasFeeCap(), want)
	}
	if r, err := svc.StoredTransaction(replacement); err != nil || !r.TipFromHistory {
		t.Fatalf("replacement does not keep TipFromHistory: %v", err)
	}

	// the history falls: the next replacement still raises both fees by
	// ReplacementBumpPercent over the stored ones
	stored, err = svc.StoredTransaction(replacement)
	if err != nil {
		t.Fatal(err)
	}
	c.setRewards(big.NewInt(1))
	if _, err := svc.ReplaceTransaction(context.Background(), replacement, nil); err != nil {
		t.Fatal(err)
	}
	tx = c.last(t)
	if want := transaction.Bumped(stored.GasTipCap); tx.GasTipCap().Cmp(want) < 0 {
		t.Fatalf("second replacement tip %s, want at least the stored tip plus 12 %%, %s", tx.GasTipCap(), want)
	}
	if want := transaction.Bumped(stored.GasFeeCap); tx.GasFeeCap().Cmp(want) < 0 {
		t.Fatalf("second replacement fee cap %s, want at least the stored cap plus 12 %%, %s", tx.GasFeeCap(), want)
	}
}

// Without a usable history, an error or no rewards, the tip is the
// suggested one and the send goes ahead (test 3).
func TestTipFromHistoryUnavailable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		set  func(*historyChain)
	}{
		{"error", func(c *historyChain) { c.setHistoryErr(errors.New("method not found")) }},
		{"no rewards", func(c *historyChain) { c.setRewards(nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &historyChain{tip: big.NewInt(7)}
			tc.set(c)
			svc := newHistoryService(t, c)

			original, err := svc.Send(context.Background(), historyRequest(true), 0)
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if tip := c.last(t).GasTipCap(); tip.Cmp(big.NewInt(7)) != 0 {
				t.Fatalf("tip %s, want the suggested 7 wei", tip)
			}
			// the replacement comes in a later block, so it reads the
			// history again
			c.nextBlock()
			if _, err := svc.ReplaceTransaction(context.Background(), original, nil); err != nil {
				t.Fatalf("replace: %v", err)
			}
			if tip := c.last(t).GasTipCap(); tip.Cmp(big.NewInt(8)) != 0 {
				t.Fatalf("replacement tip %s, want the bumped stored 8 wei", tip)
			}
			if calls := c.historyCalls(); calls != 2 {
				t.Fatalf("%d fee history reads, want 2", calls)
			}
		})
	}
}

// A history of 10^15 wei gives a tip of maxHistoryTip, 100 gwei; a stored
// tip already above it is still raised by ReplacementBumpPercent (test 4).
func TestTipFromHistoryCeiling(t *testing.T) {
	t.Parallel()

	gwei := big.NewInt(1_000_000_000)
	c := &historyChain{tip: big.NewInt(1)}
	c.setRewards(new(big.Int).Exp(big.NewInt(10), big.NewInt(15), nil))
	svc := newHistoryService(t, c)

	if _, err := svc.Send(context.Background(), historyRequest(true), 0); err != nil {
		t.Fatal(err)
	}
	if tip, want := c.last(t).GasTipCap(), new(big.Int).Mul(big.NewInt(100), gwei); tip.Cmp(want) != 0 {
		t.Fatalf("tip %s, want the ceiling %s", tip, want)
	}

	c.setTip(new(big.Int).Mul(big.NewInt(200), gwei))
	original, err := svc.Send(context.Background(), historyRequest(true), 0)
	if err != nil {
		t.Fatal(err)
	}
	if tip, want := c.last(t).GasTipCap(), new(big.Int).Mul(big.NewInt(200), gwei); tip.Cmp(want) != 0 {
		t.Fatalf("tip %s, want the suggested %s above the ceiling", tip, want)
	}
	if _, err := svc.ReplaceTransaction(context.Background(), original, nil); err != nil {
		t.Fatal(err)
	}
	if tip, want := c.last(t).GasTipCap(), new(big.Int).Mul(big.NewInt(224), gwei); tip.Cmp(want) != 0 {
		t.Fatalf("replacement tip %s, want the stored tip plus 12 %%, %s", tip, want)
	}
}

// The history tip is the median of the per-block values, the mean of the
// two middle ones for an even count, plus 10 % rounded up; blocks without a
// reward are skipped, and no rewards at all is an error.
func TestHistoryTipMedian(t *testing.T) {
	t.Parallel()

	b := func(v int64) []*big.Int { return []*big.Int{big.NewInt(v)} }
	for _, tc := range []struct {
		name    string
		rewards [][]*big.Int
		want    int64 // 0: an error
	}{
		{"odd count with an outlier", [][]*big.Int{b(1), b(5), b(1_000_000), b(2), b(3)}, 4},    // 3 + 10 % = 3.3
		{"even count", [][]*big.Int{b(10), b(1_000), b(20), b(30)}, 28},                         // 25 + 10 % = 27.5
		{"blocks without a reward", [][]*big.Int{nil, {}, {nil}, b(7), b(9), b(1_000_000)}, 10}, // 9 + 10 % = 9.9
		{"exact margin", [][]*big.Int{b(100)}, 110},
		{"no rewards", [][]*big.Int{nil, {}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backend := backendmock.New(backendmock.WithFeeHistoryFunc(func(context.Context, uint64, *big.Int, []float64) (*ethereum.FeeHistory, error) {
				return &ethereum.FeeHistory{OldestBlock: big.NewInt(1), Reward: tc.rewards}, nil
			}))
			got, err := transaction.HistoryTip(context.Background(), backend)
			if tc.want == 0 {
				if err == nil {
					t.Fatalf("got %s, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Cmp(big.NewInt(tc.want)) != 0 {
				t.Fatalf("history tip %s, want %d", got, tc.want)
			}
		})
	}
}

// An eth_feeHistory that never answers does not hold a lottery send up: the
// history read is not persistent and ends after a few seconds, and the send
// goes ahead with the suggested tip. The timed-out read counts for its
// block: a second read in the same block does not wait again.
func TestTipFromHistoryHangs(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		c := &historyChain{tip: big.NewInt(7), hang: true}
		svc := newHistoryService(t, c)

		ctx, cancel := context.WithTimeout(transaction.WithPersistentCalls(context.Background(), 10*time.Second), 15*time.Second)
		defer cancel()
		start := time.Now()
		if _, err := svc.Send(ctx, historyRequest(true), 0); err != nil {
			t.Fatalf("send: %v", err)
		}
		if took := time.Since(start); took > 10*time.Second {
			t.Fatalf("send took %s waiting for the fee history", took)
		}
		if tip := c.last(t).GasTipCap(); tip.Cmp(big.NewInt(7)) != 0 {
			t.Fatalf("tip %s, want the suggested 7 wei", tip)
		}
		c.mu.Lock()
		hctx := c.historyCtx
		c.mu.Unlock()
		if _, persistent := transaction.PersistentCalls(hctx); persistent {
			t.Fatal("the fee history read is persistent")
		}

		// the balance check in the same block does not wait for the
		// endpoint again: the timed-out read counts for the block
		start = time.Now()
		if tip := svc.RaiseTipFromHistory(ctx, big.NewInt(7)); tip.Cmp(big.NewInt(7)) != 0 {
			t.Fatalf("raised tip %s, want the given 7 wei", tip)
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("a second read in the same block took %s waiting for the fee history again", took)
		}
		if calls := c.historyCalls(); calls != 1 {
			t.Fatalf("%d fee history reads in one block with an endpoint that does not answer, want 1", calls)
		}
	})
}

// A replacement's tip is also at least the recent tip, the median of the
// last recentTipBlocks blocks' 10th-percentile tips plus 10 %, so it
// follows a sudden rise that the median of 20 blocks lags; the first send
// keeps the 20-block median (#796 test 11).
func TestRecentTipAtReplacement(t *testing.T) {
	t.Parallel()

	gwei := big.NewInt(1_000_000_000)
	rewards := make([]*big.Int, 20)
	for i := range rewards {
		rewards[i] = big.NewInt(2)
	}
	for i := 17; i < 20; i++ {
		rewards[i] = gwei
	}
	c := &historyChain{tip: big.NewInt(1)}
	c.setRewardList(rewards)
	svc := newHistoryService(t, c)

	original, err := svc.Send(context.Background(), historyRequest(true), 0)
	if err != nil {
		t.Fatal(err)
	}
	// the median of 17 blocks at 2 wei and 3 at 1 gwei is 2 wei; plus
	// 10 %, rounded up, 3 wei
	if tip := c.last(t).GasTipCap(); tip.Cmp(big.NewInt(3)) != 0 {
		t.Fatalf("first tip %s, want the 20-block median plus 10 %%, 3 wei", tip)
	}

	if _, err := svc.ReplaceTransaction(context.Background(), original, nil); err != nil {
		t.Fatal(err)
	}
	if tip, want := c.last(t).GasTipCap(), big.NewInt(1_100_000_000); tip.Cmp(want) < 0 {
		t.Fatalf("replacement tip %s, want the median of the last 3 blocks plus 10 %%, at least %s", tip, want)
	}
	if calls := c.historyCalls(); calls != 1 {
		t.Fatalf("%d fee history reads for a send and a replacement in one block, want 1", calls)
	}
}
