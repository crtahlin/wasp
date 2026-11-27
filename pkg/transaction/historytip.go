// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"time"

	"github.com/ethereum/go-ethereum"
)

// The history tip prices a lottery transaction from what recent blocks
// included rather than from the endpoint's suggestion, which on Gnosis
// answers about 1 wei while included transactions pay millions (#796,
// #846). See docs/experiments/lottery-fee-pricing/spec.md.
const (
	// historyTipBlocks is how many of the latest blocks eth_feeHistory is
	// asked for.
	historyTipBlocks = 20
	// historyTipPercentile is the percentile of each block's included tips
	// that is read.
	historyTipPercentile = 10
	// recentTipBlocks is how many of the latest blocks the recent tip of
	// a replacement is the median of: a replacement follows a sudden rise
	// in what blocks include, which the median of historyTipBlocks lags.
	recentTipBlocks = 3
	// historyTipMarginPercent is added to the median of the per-block
	// values.
	historyTipMarginPercent = 10
	// historyTipTimeout bounds the history read, which is not persistent
	// even for a lottery call: an endpoint that does not answer
	// eth_feeHistory then leaves the suggested tip, and time to sign and
	// send, instead of using up the lottery's step.
	historyTipTimeout = 5 * time.Second
)

// maxHistoryTip caps what the history can add to a tip: 100 gwei. It never
// lowers a tip below the suggested one or a replacement's bumped tip.
var maxHistoryTip = big.NewInt(100_000_000_000)

var errNoFeeHistory = errors.New("fee history has no rewards")

// FeeHistoryBackend is the part of a chain backend the history tip reads.
type FeeHistoryBackend interface {
	BlockNumber(ctx context.Context) (uint64, error)
	FeeHistory(ctx context.Context, blockCount uint64, lastBlock *big.Int, rewardPercentiles []float64) (*ethereum.FeeHistory, error)
}

// feeHistoryCache keeps the last eth_feeHistory answer, or its error, with
// the block number it was read at, and serves it until a new block is
// seen: one read per block at most, shared by the sends, the replacements
// and the balance check of GET /redistributionstate, which a dashboard may
// poll.
type feeHistoryCache struct {
	// sem holds one reader at a time, so that callers in the same block
	// wait for one read instead of each making their own; a caller whose
	// context ends stops waiting.
	sem chan struct{}

	// guarded by sem
	read    bool
	block   uint64
	history *ethereum.FeeHistory
	err     error
}

func newFeeHistoryCache() *feeHistoryCache {
	return &feeHistoryCache{sem: make(chan struct{}, 1)}
}

// get returns the fee history of the latest historyTipBlocks blocks at the
// historyTipPercentile, read at most once per block number. The block
// number and the read share historyTipTimeout. When the block number
// cannot be read, the history is read without the cache.
func (c *feeHistoryCache) get(ctx context.Context, backend FeeHistoryBackend) (*ethereum.FeeHistory, error) {
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, persistentCallsKey{}, time.Duration(0)), historyTipTimeout)
	defer cancel()

	block, err := backend.BlockNumber(ctx)
	if err != nil {
		return readFeeHistory(ctx, backend)
	}

	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()

	if c.read && c.block == block {
		return c.history, c.err
	}
	history, err := readFeeHistory(ctx, backend)
	if ctx.Err() == nil {
		// an answer cut short by the caller's context is not the
		// endpoint's, and is not kept for the other callers
		c.read, c.block, c.history, c.err = true, block, history, err
	}
	return history, err
}

func readFeeHistory(ctx context.Context, backend FeeHistoryBackend) (*ethereum.FeeHistory, error) {
	history, err := backend.FeeHistory(ctx, historyTipBlocks, nil, []float64{historyTipPercentile})
	if err != nil {
		return nil, err
	}
	if history == nil {
		return nil, errNoFeeHistory
	}
	return history, nil
}

// raiseTipFromHistory returns the higher of floor and the history tip,
// and for a replacement also the recent tip, capped at maxHistoryTip; floor
// when the history is unavailable.
func (t *transactionService) raiseTipFromHistory(ctx context.Context, floor *big.Int, replacement bool) *big.Int {
	history, err := t.history.get(ctx, t.backend)
	var tip, recent *big.Int
	if err == nil {
		tip, recent, err = historyTips(history)
	}
	if err != nil {
		t.logger.Debug("tip from fee history unavailable; using the suggested tip", "error", err)
		return floor
	}
	if replacement {
		tip = bigMax(tip, recent)
	}
	if tip.Cmp(maxHistoryTip) > 0 {
		tip = new(big.Int).Set(maxHistoryTip)
	}
	return bigMax(floor, tip)
}

// RaiseTipFromHistory returns the higher of tip and the history tip,
// capped at maxHistoryTip, or tip when the history is unavailable. It
// shares the service's per-block fee history read.
func (t *transactionService) RaiseTipFromHistory(ctx context.Context, tip *big.Int) *big.Int {
	return t.raiseTipFromHistory(ctx, tip, false)
}

// suggestedFeeAndHistoryTip is SuggestedFeeAndTip without a fixed gas
// price, with the tip raised to the history tip and the fee cap raised by
// the same amount.
func (t *transactionService) suggestedFeeAndHistoryTip(ctx context.Context, boostPercent int) (gasFeeCap, gasTipCap *big.Int, err error) {
	gasFeeCap, gasTipCap, err = t.backend.SuggestedFeeAndTip(ctx, nil, boostPercent)
	if err != nil {
		return nil, nil, err
	}
	tip := t.raiseTipFromHistory(ctx, gasTipCap, false)
	return addToFeeCap(gasFeeCap, gasTipCap, tip), tip, nil
}

// historyTip reads the fee history, without the cache, and returns its
// history tip.
func historyTip(ctx context.Context, backend FeeHistoryBackend) (*big.Int, error) {
	history, err := readFeeHistory(ctx, backend)
	if err != nil {
		return nil, err
	}
	tip, _, err := historyTips(history)
	return tip, err
}

// historyTips returns the history tip, the median over all the blocks of
// the answer of each block's historyTipPercentile tip, and the recent tip,
// the same median over its last recentTipBlocks blocks; each plus
// historyTipMarginPercent, rounded up. Every block counts, full or not;
// a block without a reward is skipped.
func historyTips(history *ethereum.FeeHistory) (tip, recent *big.Int, err error) {
	values := make([]*big.Int, 0, len(history.Reward))
	for _, reward := range history.Reward {
		if len(reward) > 0 && reward[0] != nil {
			values = append(values, reward[0])
		}
	}
	if len(values) == 0 {
		return nil, nil, errNoFeeHistory
	}
	recent = medianWithMargin(values[len(values)-min(recentTipBlocks, len(values)):])
	return medianWithMargin(values), recent, nil
}

// medianWithMargin is the median of values, the mean of the two middle
// ones for an even count, plus historyTipMarginPercent, rounded up. It
// leaves values as they are.
func medianWithMargin(values []*big.Int) *big.Int {
	sorted := slices.Clone(values)
	slices.SortFunc(sorted, func(a, b *big.Int) int { return a.Cmp(b) })
	n := len(sorted)
	median := new(big.Int).Set(sorted[n/2])
	if n%2 == 0 {
		median.Add(median, sorted[n/2-1])
		median.Rsh(median, 1)
	}
	median.Mul(median, big.NewInt(100+historyTipMarginPercent))
	median.Add(median, big.NewInt(99))
	return median.Div(median, big.NewInt(100))
}

// addToFeeCap returns gasFeeCap raised by the difference between tip and
// the suggested tip it was computed with, so it stays the multiple of the
// base fee plus the tip.
func addToFeeCap(gasFeeCap, suggestedTip, tip *big.Int) *big.Int {
	v := new(big.Int).Sub(tip, suggestedTip)
	if v.Sign() <= 0 {
		return gasFeeCap
	}
	return v.Add(v, gasFeeCap)
}
