// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"context"
	"errors"
	"math/big"
	"slices"

	"github.com/ethereum/go-ethereum"
	"github.com/ethersphere/bee/v2/pkg/log"
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
	// historyTipMarginPercent is added to the median of the per-block
	// values.
	historyTipMarginPercent = 10
)

// maxHistoryTip caps what the history can add to a tip: 100 gwei. It never
// lowers a tip below the suggested one or a replacement's bumped tip.
var maxHistoryTip = big.NewInt(100_000_000_000)

var errNoFeeHistory = errors.New("fee history has no rewards")

// FeeHistoryBackend is the part of a chain backend the history tip reads.
type FeeHistoryBackend interface {
	SuggestedFeeAndTip(ctx context.Context, gasPrice *big.Int, boostPercent int) (*big.Int, *big.Int, error)
	FeeHistory(ctx context.Context, blockCount uint64, lastBlock *big.Int, rewardPercentiles []float64) (*ethereum.FeeHistory, error)
}

// SuggestedFeeAndHistoryTip is SuggestedFeeAndTip without a fixed gas price,
// with the tip raised to the history tip, capped at maxHistoryTip, and the
// fee cap raised by the same amount. When the history is unavailable it
// returns the suggestion unchanged and logs the reason at debug level.
func SuggestedFeeAndHistoryTip(ctx context.Context, backend FeeHistoryBackend, logger log.Logger, boostPercent int) (gasFeeCap, gasTipCap *big.Int, err error) {
	gasFeeCap, gasTipCap, err = backend.SuggestedFeeAndTip(ctx, nil, boostPercent)
	if err != nil {
		return nil, nil, err
	}
	tip := raiseTipFromHistory(ctx, backend, logger, gasTipCap)
	return addToFeeCap(gasFeeCap, gasTipCap, tip), tip, nil
}

// raiseTipFromHistory returns the higher of floor and the history tip
// capped at maxHistoryTip; floor when the history is unavailable.
func raiseTipFromHistory(ctx context.Context, backend FeeHistoryBackend, logger log.Logger, floor *big.Int) *big.Int {
	history, err := historyTip(ctx, backend)
	if err != nil {
		logger.Debug("tip from fee history unavailable; using the suggested tip", "error", err)
		return floor
	}
	if history.Cmp(maxHistoryTip) > 0 {
		history = new(big.Int).Set(maxHistoryTip)
	}
	return bigMax(floor, history)
}

// historyTip is the median, over the last historyTipBlocks blocks, of each
// block's historyTipPercentile tip, plus historyTipMarginPercent, rounded
// up. Every block counts, full or not.
func historyTip(ctx context.Context, backend FeeHistoryBackend) (*big.Int, error) {
	history, err := backend.FeeHistory(ctx, historyTipBlocks, nil, []float64{historyTipPercentile})
	if err != nil {
		return nil, err
	}
	if history == nil {
		return nil, errNoFeeHistory
	}
	values := make([]*big.Int, 0, len(history.Reward))
	for _, reward := range history.Reward {
		if len(reward) > 0 && reward[0] != nil {
			values = append(values, reward[0])
		}
	}
	if len(values) == 0 {
		return nil, errNoFeeHistory
	}
	slices.SortFunc(values, func(a, b *big.Int) int { return a.Cmp(b) })
	n := len(values)
	median := new(big.Int).Set(values[n/2])
	if n%2 == 0 {
		median.Add(median, values[n/2-1])
		median.Rsh(median, 1)
	}
	median.Mul(median, big.NewInt(100+historyTipMarginPercent))
	median.Add(median, big.NewInt(99))
	return median.Div(median, big.NewInt(100)), nil
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
