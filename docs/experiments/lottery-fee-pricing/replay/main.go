// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build replay

// Command replay is the code-level replay of the lottery fee-pricing spec
// (docs/experiments/lottery-fee-pricing/spec.md, Measurement, #796, #846).
// It drives the node's own code, the transaction service with its pricing
// and replacements and the storage incentives' commit, reveal and claim
// with their phase loop, against a simulated chain fed block by block from
// a TSV of recorded Gnosis fee history, and counts the lottery
// transactions not mined in their phase.
//
// The chain includes a pending transaction in block b when its fee cap is
// at least b's base fee, its effective tip, min(tip, fee cap - base fee),
// at least b's lowest included tip (p0), and, when b is full (gas used
// ratio 0.95 or more), at least b's tip at the -full-percentile. It
// answers eth_feeHistory from the same data, as an endpoint would have at
// that block, and eth_maxPriorityFeePerGas with 1 wei.
//
// The "main" build is not main's code: it is this branch's code with what
// this change adds switched off, as main behaves. Every lottery request is
// sent without TipFromHistory, so the tip is the suggestion plus 50 % and
// a replacement reprices from the suggestion, as on main; the commit and
// the claim are sent once with main's fee cap (twice the base fee plus the
// tip), not SelfReplacing, and wait for their receipt until the phase
// ends, as on main; the reveal keeps its fee cap of four times the base
// fee and its replacement loop, as on main.
//
// Time is simulated (testing/synctest), so the program runs under
// testing.Main and prints a PASS line at the end. It is built only with
// the replay build tag:
//
//	go run -tags replay ./docs/experiments/lottery-fee-pricing/replay -data <fee history TSV>
//
// The TSV has a header line and the columns block, base_fee,
// gas_used_ratio, p0, p5, p10, p25, p50, p75, p90, one row per block.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	chaincfg "github.com/ethersphere/bee/v2/pkg/config"
	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storageincentives"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/redistribution"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/wrapped"
	"github.com/ethersphere/bee/v2/pkg/util/abiutil"
)

const (
	kindCommit = "commit"
	kindReveal = "reveal"
	kindClaim  = "claim"
	kindProbe  = "probe"
	buildMain  = "main"
	buildThis  = "branch"
)

const (
	blockTime      = 5 * time.Second
	blocksPerRound = storageincentives.DefaultBlocksPerRound
	blocksPerPhase = storageincentives.DefaultBlocksPerPhase
	fullRatio      = 0.95
	// warmup is how many rows of the data a send must come after, as in
	// the Python replay (a history to read, and the same rounds).
	warmup = 600
)

var (
	dataPath     = flag.String("data", "", "fee history TSV (required)")
	thresholdsF  = flag.String("thresholds", "10,25,50", "full-block percentiles to run, from 5, 10, 25, 50, 75, 90")
	commitAtF    = flag.String("commit-at", "1,3", "phase blocks the commit is priced at")
	buildsF      = flag.String("builds", "branch,main", "builds to run: branch (this change) and main (as main behaves)")
	maxRoundsF   = flag.Int("rounds", 0, "replay only the first this many rounds (0: all)")
	probeBlockF  = flag.Uint64("probe-block", 49488638, "block a probe-like transaction is sent at (0: none)")
	probeBlocksF = flag.Uint64("probe-blocks", 40, "blocks the probe is followed for")
	traceF       = flag.String("trace", "", "print every send of the jobs of one round, as kind:round (kind commit, reveal, claim or probe)")
	historyLagF  = flag.Uint64("history-lag", 0, "blocks eth_feeHistory lags the latest block (the Python replay read the history up to the block before)")
)

// Gas used per transaction, from the redistribution contract's
// transactions over two rounds (the medians), as in the Python replay.
var gasUsed = map[string]uint64{"commit": 160_000, "reveal": 200_000, "claim": 450_000, "probe": 21_000}

var percentileColumn = map[int]int{0: 0, 5: 1, 10: 2, 25: 3, 50: 4, 75: 5, 90: 6}

type row struct {
	baseFee uint64
	ratio   float64
	tips    [7]uint64 // p0, p5, p10, p25, p50, p75, p90
}

type feeData struct {
	first uint64
	rows  []row
}

func (d *feeData) has(block uint64) bool {
	return block >= d.first && block < d.first+uint64(len(d.rows))
}

func (d *feeData) at(block uint64) row { return d.rows[block-d.first] }

func loadData(path string) (*feeData, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	d := &feeData{}
	line := 0
	for sc.Scan() {
		line++
		if line == 1 {
			continue
		}
		cols := strings.Split(sc.Text(), "\t")
		if len(cols) != 10 {
			return nil, fmt.Errorf("line %d: %d columns, want 10", line, len(cols))
		}
		block, err := strconv.ParseUint(cols[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if len(d.rows) == 0 {
			d.first = block
		} else if block != d.first+uint64(len(d.rows)) {
			return nil, fmt.Errorf("line %d: block %d, want %d (the data must have every block)", line, block, d.first+uint64(len(d.rows)))
		}
		var r row
		if r.baseFee, err = strconv.ParseUint(cols[1], 10, 64); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if r.ratio, err = strconv.ParseFloat(cols[2], 64); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		for i := range r.tips {
			if cols[3+i] == "" {
				continue
			}
			if r.tips[i], err = strconv.ParseUint(cols[3+i], 10, 64); err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
		}
		d.rows = append(d.rows, r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(d.rows) == 0 {
		return nil, errors.New("no rows")
	}
	return d, nil
}

// chain is the simulated chain of one run: one account, whose pending
// transactions are included block by block by the inclusion model.
type chain struct {
	data      *feeData
	threshold int    // column of the full-block percentile
	gas       uint64 // gas used by each transaction

	mu      sync.Mutex
	cur     uint64
	curTime time.Time
	reached map[uint64]chan struct{}

	pool    map[uint64]*types.Transaction // nonce: the newest pending
	known   map[common.Hash]*types.Transaction
	mined   map[common.Hash]minedTx
	minedAt []uint64 // block each nonce was mined in
	sent    []sentTx
}

type minedTx struct {
	block uint64
	price *big.Int // effective gas price
}

type sentTx struct {
	hash  common.Hash
	nonce uint64
	block uint64
	tip   *big.Int
	cap   *big.Int
}

func newChain(d *feeData, threshold int, block, gas uint64) *chain {
	return &chain{
		data:      d,
		gas:       gas,
		threshold: percentileColumn[threshold],
		cur:       block,
		// the latest block was made half a block ago; the node's timers
		// then never fire at the same instant as a new block
		curTime: time.Now().Add(-blockTime / 2),
		reached: make(map[uint64]chan struct{}),
		pool:    make(map[uint64]*types.Transaction),
		known:   make(map[common.Hash]*types.Transaction),
		mined:   make(map[common.Hash]minedTx),
	}
}

// run makes a block every blockTime until ctx ends or the data ends.
func (c *chain) run(ctx context.Context) {
	timer := time.NewTimer(blockTime / 2)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if !c.mine() {
			return
		}
		timer.Reset(blockTime)
	}
}

// mine makes the next block, with every pending transaction the model
// includes in it, in nonce order. It reports false at the end of the data.
func (c *chain) mine() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.cur + 1
	if !c.data.has(b) {
		return false
	}
	r := c.data.at(b)
	for {
		nonce := uint64(len(c.minedAt))
		tx, ok := c.pool[nonce]
		if !ok || !c.includable(r, tx) {
			break
		}
		base := new(big.Int).SetUint64(r.baseFee)
		eff := new(big.Int).Sub(tx.GasFeeCap(), base)
		if eff.Cmp(tx.GasTipCap()) > 0 {
			eff.Set(tx.GasTipCap())
		}
		c.mined[tx.Hash()] = minedTx{block: b, price: eff.Add(eff, base)}
		c.minedAt = append(c.minedAt, b)
		delete(c.pool, nonce)
	}
	c.cur = b
	c.curTime = time.Now()
	if ch, ok := c.reached[b]; ok {
		close(ch)
		delete(c.reached, b)
	}
	return true
}

func (c *chain) includable(r row, tx *types.Transaction) bool {
	base := new(big.Int).SetUint64(r.baseFee)
	if tx.GasFeeCap().Cmp(base) < 0 {
		return false
	}
	eff := new(big.Int).Sub(tx.GasFeeCap(), base)
	if eff.Cmp(tx.GasTipCap()) > 0 {
		eff.Set(tx.GasTipCap())
	}
	if eff.Cmp(new(big.Int).SetUint64(r.tips[0])) < 0 {
		return false
	}
	if r.ratio < fullRatio {
		return true
	}
	return eff.Cmp(new(big.Int).SetUint64(r.tips[c.threshold])) >= 0
}

// reachedCh is closed once block b is made.
func (c *chain) reachedCh(b uint64) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan struct{})
	if c.cur >= b {
		close(ch)
		return ch
	}
	if old, ok := c.reached[b]; ok {
		return old
	}
	c.reached[b] = ch
	return ch
}

func (c *chain) header(number uint64) *types.Header {
	return &types.Header{
		Number:  new(big.Int).SetUint64(number),
		BaseFee: new(big.Int).SetUint64(c.data.at(number).baseFee),
		Time:    uint64(c.curTime.Add(-time.Duration(c.cur-number) * blockTime).Unix()),
	}
}

// The go-ethereum client methods the node calls (backend.Geth).

func (c *chain) BalanceAt(context.Context, common.Address, *big.Int) (*big.Int, error) {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil), nil
}

func (c *chain) BlockNumber(context.Context) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur, nil
}

func (c *chain) CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
	return nil, errors.New("replay: no contract calls")
}

func (c *chain) ChainID(context.Context) (*big.Int, error) { return big.NewInt(100), nil }

func (c *chain) Close() {}

func (c *chain) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	return nil, nil
}

func (c *chain) EstimateGas(context.Context, ethereum.CallMsg) (uint64, error) {
	return 0, errors.New("replay: no gas estimate")
}

func (c *chain) FeeHistory(_ context.Context, blockCount uint64, lastBlock *big.Int, percentiles []float64) (*ethereum.FeeHistory, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	newest := c.cur - min(c.cur, *historyLagF)
	if lastBlock != nil {
		newest = min(newest, lastBlock.Uint64())
	}
	cols := make([]int, len(percentiles))
	for i, p := range percentiles {
		col, ok := percentileColumn[int(p)]
		if !ok || float64(int(p)) != p {
			return nil, fmt.Errorf("replay: percentile %v not recorded", p)
		}
		cols[i] = col
	}
	oldest := newest + 1 - min(blockCount, newest+1-c.data.first)
	h := &ethereum.FeeHistory{OldestBlock: new(big.Int).SetUint64(oldest)}
	for b := oldest; b <= newest; b++ {
		r := c.data.at(b)
		reward := make([]*big.Int, len(cols))
		for i, col := range cols {
			reward[i] = new(big.Int).SetUint64(r.tips[col])
		}
		h.Reward = append(h.Reward, reward)
		h.BaseFee = append(h.BaseFee, new(big.Int).SetUint64(r.baseFee))
		h.GasUsedRatio = append(h.GasUsedRatio, r.ratio)
	}
	return h, nil
}

func (c *chain) FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error) {
	return nil, nil
}

func (c *chain) HeaderByNumber(_ context.Context, number *big.Int) (*types.Header, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.cur
	if number != nil {
		n = number.Uint64()
	}
	if n > c.cur || !c.data.has(n) {
		return nil, ethereum.NotFound
	}
	return c.header(n), nil
}

func (c *chain) NonceAt(_ context.Context, _ common.Address, block *big.Int) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	at := c.cur
	if block != nil {
		at = block.Uint64()
	}
	var n uint64
	for _, b := range c.minedAt {
		if b <= at {
			n++
		}
	}
	return n, nil
}

func (c *chain) PendingNonceAt(context.Context, common.Address) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := uint64(len(c.minedAt))
	for {
		if _, ok := c.pool[n]; !ok {
			return n, nil
		}
		n++
	}
}

// SendTransaction answers as a go-ethereum pool does: a known
// transaction is "already known", a used nonce "nonce too low", and a
// replacement must raise both fees by 10 %.
func (c *chain) SendTransaction(_ context.Context, tx *types.Transaction) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.known[tx.Hash()]; ok {
		if _, mined := c.mined[tx.Hash()]; mined {
			return errors.New("nonce too low")
		}
		if p, ok := c.pool[tx.Nonce()]; ok && p.Hash() == tx.Hash() {
			return errors.New("already known")
		}
	}
	if tx.Nonce() < uint64(len(c.minedAt)) {
		return errors.New("nonce too low")
	}
	if old, ok := c.pool[tx.Nonce()]; ok && (!raised10(old.GasTipCap(), tx.GasTipCap()) || !raised10(old.GasFeeCap(), tx.GasFeeCap())) {
		return errors.New("replacement transaction underpriced")
	}
	c.pool[tx.Nonce()] = tx
	c.known[tx.Hash()] = tx
	c.sent = append(c.sent, sentTx{hash: tx.Hash(), nonce: tx.Nonce(), block: c.cur, tip: tx.GasTipCap(), cap: tx.GasFeeCap()})
	return nil
}

// raised10 reports whether v is at least 110 % of old.
func raised10(old, v *big.Int) bool {
	want := new(big.Int).Mul(old, big.NewInt(110))
	return new(big.Int).Mul(v, big.NewInt(100)).Cmp(want) >= 0
}

func (c *chain) SuggestGasTipCap(context.Context) (*big.Int, error) { return big.NewInt(1), nil }

func (c *chain) TransactionByHash(_ context.Context, hash common.Hash) (*types.Transaction, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, ok := c.known[hash]
	if !ok {
		return nil, false, ethereum.NotFound
	}
	if _, mined := c.mined[hash]; mined {
		return tx, false, nil
	}
	if p, ok := c.pool[tx.Nonce()]; ok && p.Hash() == hash {
		return tx, true, nil
	}
	// replaced in the pool, or its nonce used by another
	return nil, false, ethereum.NotFound
}

func (c *chain) TransactionReceipt(_ context.Context, hash common.Hash) (*types.Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.mined[hash]
	if !ok {
		return nil, ethereum.NotFound
	}
	return &types.Receipt{
		Status:            types.ReceiptStatusSuccessful,
		TxHash:            hash,
		BlockNumber:       new(big.Int).SetUint64(m.block),
		GasUsed:           c.gas,
		EffectiveGasPrice: new(big.Int).Set(m.price),
	}, nil
}

// simBackend is the node's chain backend: wasp's wrapped backend over the
// simulated chain, so the suggested fee and tip are priced by wasp's own
// code, with the block number read from the chain itself rather than
// estimated from the wall clock.
type simBackend struct {
	transaction.Backend
	chain *chain
}

func (b simBackend) BlockNumber(ctx context.Context) (uint64, error) {
	return b.chain.BlockNumber(ctx)
}

// mainService sends lottery requests as main does: without the history
// tip, and the commit and the claim with main's fee cap and not
// SelfReplacing.
type mainService struct {
	transaction.Service
}

func asMain(r *transaction.TxRequest) *transaction.TxRequest {
	m := *r
	m.TipFromHistory = false
	if m.Description != "reveal transaction" {
		m.MinBaseFeeMultiple = 0
		m.SelfReplacing = false
	}
	return &m
}

func (m mainService) Send(ctx context.Context, r *transaction.TxRequest, boost int) (common.Hash, error) {
	return m.Service.Send(ctx, asMain(r), boost)
}

func (m mainService) SendWithHook(ctx context.Context, r *transaction.TxRequest, boost int, hook transaction.BeforeBroadcastFunc) (common.Hash, error) {
	return m.Service.SendWithHook(ctx, asMain(r), boost, hook)
}

// job is one lottery transaction to replay.
type job struct {
	build     string
	kind      string // commit, reveal, claim or probe
	at        uint64 // the phase block it is priced at
	threshold int
	round     uint64
	send, end uint64 // the block it is priced at, the first block after its phase
}

type outcome struct {
	minedBlock uint64 // 0: not mined in its phase
	price      *big.Int
	tip        *big.Int
	firstTip   *big.Int
	sent       int // transactions broadcast before the phase ended
	codeErr    error
	trace      []string
}

var (
	signerKey     = mustKey()
	redistABI     = abiutil.MustParseABI(chaincfg.Mainnet.RedistributionABI)
	redistAddress = common.HexToAddress("0xffff")
)

func mustKey() crypto.Signer {
	k, err := crypto.DecodeSecp256k1PrivateKey(common.HexToHash("0x0101010101010101010101010101010101010101010101010101010101010101").Bytes())
	if err != nil {
		panic(err)
	}
	return crypto.NewDefaultSigner(k)
}

// replayOne runs one job in its own simulated chain and node, in fake
// time.
func replayOne(t *testing.T, d *feeData, j job) outcome {
	t.Helper()
	var out outcome
	synctest.Test(t, func(t *testing.T) {
		out = replayInBubble(t, d, j)
	})
	return out
}

func replayInBubble(t *testing.T, d *feeData, j job) outcome {
	t.Helper()
	c := newChain(d, j.threshold, j.send, gasUsed[j.kind])
	chainCtx, stopChain := context.WithCancel(context.Background())
	chainDone := make(chan struct{})
	go func() {
		defer close(chainDone)
		c.run(chainCtx)
	}()
	defer func() {
		stopChain()
		<-chainDone
	}()

	sender, err := signerKey.EthereumAddress()
	if err != nil {
		t.Fatal(err)
	}
	be := simBackend{Backend: wrapped.NewBackend(c, 0, blockTime, 1), chain: c}
	monitor := transaction.NewMonitor(log.Noop, be, sender, blockTime, 6)
	svc, err := transaction.NewService(log.Noop, sender, be, signerKey, mock.NewStateStore(), big.NewInt(100), monitor, 0, transaction.WithBlockTime(blockTime))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = svc.Close()
		_ = monitor.Close()
	}()
	svc.SetLotteryContract(redistAddress)
	txs := svc
	if j.build == buildMain {
		txs = mainService{svc}
	}
	overlay := swarm.MustParseHexAddress("ca1e9f3938cc1425c6061b96ad9eb93e134dfe8734ad490164ef20af9d1cf59c")
	contract := redistribution.New(overlay, sender, log.Noop, txs, redistAddress, redistABI, gasUsed[j.kind])
	state, err := storageincentives.NewRedistributionState(log.Noop, sender, mock.NewStateStore(), nil, txs)
	if err != nil {
		t.Fatal(err)
	}
	revealer := storageincentives.NewRevealer(state, contract, txs, be, func() time.Duration { return blockTime }, blocksPerRound, blocksPerPhase, log.Noop)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runJob(ctx, j, contract, revealer, txs, sender, overlay)
	}()
	var codeErr error
	select {
	case codeErr = <-done:
	case <-c.reachedCh(j.end):
		cancel()
		codeErr = <-done
	}
	_ = revealer.Close()

	c.mu.Lock()
	defer c.mu.Unlock()
	out := outcome{codeErr: codeErr}
	for _, s := range c.sent {
		if s.block < j.end && s.nonce == 0 {
			out.sent++
			if out.firstTip == nil {
				out.firstTip = s.tip
			}
		}
	}
	for _, s := range c.sent {
		if m, ok := c.mined[s.hash]; ok && s.nonce == 0 && m.block < j.end {
			out.minedBlock, out.price, out.tip = m.block, m.price, s.tip
		}
	}
	if *traceF == fmt.Sprintf("%s:%d", j.kind, j.round) {
		for _, s := range c.sent {
			m := c.mined[s.hash]
			out.trace = append(out.trace, fmt.Sprintf("%s p%d %s: sent at block %d (phase block %d), tip %s, fee cap %s, mined at %d",
				j.build, j.threshold, group(j), s.block, s.block%blocksPerRound, commas(s.tip), commas(s.cap), m.block))
		}
	}
	return out
}

func runJob(ctx context.Context, j job, contract redistribution.Contract, revealer *storageincentives.Revealer, txs transaction.Service, sender common.Address, overlay swarm.Address) error {
	switch {
	case j.kind == kindProbe:
		// a zero-value self-transfer priced and replaced as a lottery
		// transaction, like the bench-2 probe
		return sendProbe(ctx, j, revealer, txs, sender)
	case j.build == buildMain && j.kind == kindCommit:
		_, err := contract.Commit(ctx, make([]byte, 32), j.round, nil)
		return err
	case j.build == buildMain && j.kind == kindClaim:
		_, err := contract.Claim(ctx, redistribution.ChunkInclusionProofs{}, nil)
		return err
	case j.kind == kindCommit:
		return storageincentives.NewReplayAgent(contract, revealer, overlay).ReplayCommit(ctx, j.round)
	case j.kind == kindClaim:
		_, err := storageincentives.NewReplayAgent(contract, revealer, overlay).ReplayClaim(ctx, j.round)
		return err
	case j.kind == kindReveal:
		revealed, err := revealer.ReplayReveal(ctx, j.round)
		if err == nil && !revealed {
			err = errors.New("not revealed")
		}
		return err
	}
	return fmt.Errorf("unknown job %q", j.kind)
}

func sendProbe(ctx context.Context, j job, revealer *storageincentives.Revealer, txs transaction.Service, sender common.Address) error {
	_, err := revealer.ReplaySendInPhase(ctx, "probe", 0, j.end, func(ctx context.Context, hook transaction.BeforeBroadcastFunc) (common.Hash, error) {
		req := &transaction.TxRequest{
			To:                 &sender,
			GasLimit:           gasUsed[kindProbe],
			Value:              big.NewInt(0),
			Description:        "probe",
			TipFromHistory:     j.build != buildMain,
			MinBaseFeeMultiple: redistribution.RevealBaseFeeMultiple,
			SelfReplacing:      true,
		}
		txHash, err := txs.SendWithHook(ctx, req, redistribution.BoostTipPercent, hook)
		if err != nil {
			return txHash, err
		}
		if _, err := txs.WaitForSentReceipt(ctx, txHash, nil); err != nil {
			return txHash, err
		}
		return txHash, nil
	})
	return err
}

func parseInts(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// jobs lists every lottery transaction of every round in the data whose
// send comes warmup rows or more after the first block and whose phase
// ends within the data, as the Python replay selects them.
func jobs(d *feeData, builds []string, thresholds, commitAt []int, maxRounds int) []job {
	last := d.first + uint64(len(d.rows)) - 1
	var out []job
	rounds := 0
	for round := d.first / blocksPerRound; round <= last/blocksPerRound; round++ {
		start := round * blocksPerRound
		type kindAt struct {
			kind       string
			at         uint64
			begin, end uint64
		}
		kinds := make([]kindAt, 0, len(commitAt)+2)
		for _, at := range commitAt {
			kinds = append(kinds, kindAt{kindCommit, uint64(at), start, start + blocksPerPhase})
		}
		kinds = append(kinds,
			kindAt{kindReveal, 2, start + blocksPerPhase, start + 2*blocksPerPhase},
			kindAt{kindClaim, 4, start + 2*blocksPerPhase, start + blocksPerRound})
		counted := false
		for _, k := range kinds {
			send := k.begin + k.at
			if send < d.first+warmup || k.end-1 > last {
				continue
			}
			counted = true
			for _, b := range builds {
				for _, th := range thresholds {
					out = append(out, job{build: b, kind: k.kind, at: k.at, threshold: th, round: round, send: send, end: k.end})
				}
			}
		}
		if counted {
			rounds++
			if maxRounds > 0 && rounds >= maxRounds {
				break
			}
		}
	}
	return out
}

func replay(t *testing.T) {
	t.Helper()
	if *dataPath == "" {
		t.Fatal("-data is required")
	}
	thresholds, err := parseInts(*thresholdsF)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range thresholds {
		if _, ok := percentileColumn[th]; !ok || th == 0 {
			t.Fatalf("threshold %d not recorded", th)
		}
	}
	commitAt, err := parseInts(*commitAtF)
	if err != nil {
		t.Fatal(err)
	}
	builds := strings.Split(*buildsF, ",")
	started := time.Now()
	d, err := loadData(*dataPath)
	if err != nil {
		t.Fatal(err)
	}
	all := jobs(d, builds, thresholds, commitAt, *maxRoundsF)
	if p := *probeBlockF; p != 0 && d.has(p+*probeBlocksF) {
		// main has no loop that replaces such a transaction, so the probe
		// runs on this branch only
		if slices.Contains(builds, buildThis) {
			all = append(all, job{build: buildThis, kind: kindProbe, threshold: 10, send: p, end: p + *probeBlocksF})
		}
	}
	results := make([]outcome, len(all))

	workers := runtime.GOMAXPROCS(0)
	for w := range workers {
		t.Run(fmt.Sprintf("worker-%d", w), func(t *testing.T) {
			t.Parallel()
			for i := w; i < len(all); i += workers {
				results[i] = replayOne(t, d, all[i])
			}
		})
	}
	t.Cleanup(func() {
		report(d, all, results, builds, thresholds, commitAt, time.Since(started))
	})
}

type agg struct {
	n, missed, sent, mined int
	fee                    *big.Float
	maxTip                 *big.Int
	maxTipRound            uint64
	codeDisagrees          int
}

func newAgg() *agg { return &agg{fee: new(big.Float), maxTip: new(big.Int)} }

func (a *agg) add(j job, o outcome) {
	a.n++
	a.sent += max(o.sent, 1) - 1
	codeOK := o.codeErr == nil
	if o.minedBlock == 0 {
		a.missed++
	} else {
		a.mined++
		fee := new(big.Int).Mul(o.price, new(big.Int).SetUint64(gasUsed[j.kind]))
		a.fee.Add(a.fee, new(big.Float).SetInt(fee))
		if o.tip.Cmp(a.maxTip) > 0 {
			a.maxTip.Set(o.tip)
			a.maxTipRound = j.round
		}
	}
	if codeOK != (o.minedBlock != 0) {
		a.codeDisagrees++
	}
}

func (a *agg) merge(b *agg) {
	a.n += b.n
	a.missed += b.missed
	a.sent += b.sent
	a.mined += b.mined
	a.fee.Add(a.fee, b.fee)
	if b.maxTip.Cmp(a.maxTip) > 0 {
		a.maxTip.Set(b.maxTip)
		a.maxTipRound = b.maxTipRound
	}
	a.codeDisagrees += b.codeDisagrees
}

func (a *agg) meanFee() string {
	if a.mined == 0 {
		return "-"
	}
	f := new(big.Float).Quo(a.fee, big.NewFloat(float64(a.mined)*1e18))
	v, _ := f.Float64()
	return fmt.Sprintf("%.7f", v)
}

func (a *agg) replacements() string {
	if a.n == 0 {
		return "-"
	}
	return fmt.Sprintf("%.3f", float64(a.sent)/float64(a.n))
}

func group(j job) string {
	if j.kind == kindCommit {
		return fmt.Sprintf("commit@%d", j.at)
	}
	return j.kind
}

func report(d *feeData, all []job, results []outcome, builds []string, thresholds, commitAt []int, took time.Duration) {
	w := os.Stdout
	stats := map[string]*agg{}
	key := func(build string, th int, g string) string { return fmt.Sprintf("%s|%d|%s", build, th, g) }
	rounds := map[uint64]bool{}
	for i, j := range all {
		if j.kind == kindProbe {
			continue
		}
		rounds[j.round] = true
		k := key(j.build, j.threshold, group(j))
		if stats[k] == nil {
			stats[k] = newAgg()
		}
		stats[k].add(j, results[i])
	}
	groups := make([]string, 0, len(commitAt)+2)
	for _, at := range commitAt {
		groups = append(groups, fmt.Sprintf("commit@%d", at))
	}
	groups = append(groups, "reveal", "claim")
	get := func(b string, th int, g string) *agg {
		if a := stats[key(b, th, g)]; a != nil {
			return a
		}
		return newAgg()
	}

	last := d.first + uint64(len(d.rows)) - 1
	fmt.Fprintf(w, "\nCode-level replay of docs/experiments/lottery-fee-pricing/spec.md: blocks %d..%d, %d rounds, %d replayed transactions, %s.\n",
		d.first, last, len(rounds), len(all), took.Round(time.Second))
	fmt.Fprintln(w, `The "main" rows are this branch's code with the change switched off (see the package comment), not main's code.`)

	fmt.Fprintf(w, "\n**Table: lottery transactions not mined in their phase, by build and full-block threshold (code-level replay, Gnosis blocks %d..%d, %d rounds)**\n\n", d.first, last, len(rounds))
	header := "| Build | Full-block threshold |"
	sep := "|---|---|"
	for _, g := range groups {
		header += " " + strings.Replace(g, "@", " at phase block ", 1) + " |"
		sep += "---|"
	}
	for _, at := range commitAt {
		header += fmt.Sprintf(" Total (commit at %d) |", at)
		sep += "---|"
	}
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, sep)
	colTotals := make([]int, len(groups)+len(commitAt))
	for _, b := range builds {
		for _, th := range thresholds {
			line := fmt.Sprintf("| %s | p%d |", b, th)
			for gi, g := range groups {
				m := get(b, th, g).missed
				colTotals[gi] += m
				line += fmt.Sprintf(" %d |", m)
			}
			for ai, at := range commitAt {
				tot := get(b, th, fmt.Sprintf("commit@%d", at)).missed + get(b, th, "reveal").missed + get(b, th, "claim").missed
				colTotals[len(groups)+ai] += tot
				line += fmt.Sprintf(" %d |", tot)
			}
			fmt.Fprintln(w, line)
		}
	}
	line := "| **Total** | |"
	for _, v := range colTotals {
		line += fmt.Sprintf(" **%d** |", v)
	}
	fmt.Fprintln(w, line)

	for _, th := range thresholds {
		fmt.Fprintf(w, "\n**Table: fee, highest tip and replacements per lottery transaction, full-block threshold p%d (code-level replay, %d rounds)**\n\n", th, len(rounds))
		fmt.Fprintln(w, "| Build | Transaction | Transactions | Mined in phase | Mean fee per mined transaction (xDAI) | Highest tip of a mined transaction (wei) | Its round | Replacements per transaction | Code and chain disagree |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|")
		total := newAgg()
		for _, b := range builds {
			for _, g := range groups {
				a := get(b, th, g)
				total.merge(a)
				fmt.Fprintf(w, "| %s | %s | %d | %d | %s | %s | %d | %s | %d |\n", b, strings.Replace(g, "@", " at ", 1), a.n, a.mined, a.meanFee(), commas(a.maxTip), a.maxTipRound, a.replacements(), a.codeDisagrees)
			}
		}
		fmt.Fprintf(w, "| **Total** | | **%d** | **%d** | **%s** | **%s** | %d | **%s** | **%d** |\n", total.n, total.mined, total.meanFee(), commas(total.maxTip), total.maxTipRound, total.replacements(), total.codeDisagrees)
	}

	for i := range all {
		for _, l := range results[i].trace {
			fmt.Fprintln(w, "trace:", l)
		}
	}

	fmt.Fprintf(w, "\n**Table: a probe-like transaction sent at block %d, blocks to inclusion (code-level replay, full-block threshold p10)**\n\n", *probeBlockF)
	fmt.Fprintln(w, "| Build | Blocks to inclusion | First tip (wei) | Tip when mined (wei) | Transactions sent |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	sentTotal := 0
	for i, j := range all {
		if j.kind != kindProbe {
			continue
		}
		o := results[i]
		delay := "not mined"
		tip := "-"
		if o.minedBlock != 0 {
			delay = strconv.FormatUint(o.minedBlock-j.send, 10)
			tip = commas(o.tip)
		}
		sentTotal += o.sent
		fmt.Fprintf(w, "| %s | %s | %s | %s | %d |\n", j.build, delay, commas(o.firstTip), tip, o.sent)
	}
	fmt.Fprintf(w, "| **Total** | | | | **%d** |\n", sentTotal)
}

func commas(v *big.Int) string {
	if v == nil {
		return "-"
	}
	s := v.String()
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func main() {
	testing.Main(func(string, string) (bool, error) { return true, nil },
		[]testing.InternalTest{{Name: "Replay", F: replay}}, nil, nil)
}
