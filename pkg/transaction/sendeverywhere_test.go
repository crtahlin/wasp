// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/backendsimulation"
	"github.com/ethersphere/bee/v2/pkg/transaction/failover"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// The service-level tests of docs/experiments/send-everywhere/spec.md
// (#819): a simulated chain mines what its endpoints receive, a black hole
// keeps what it receives to itself, and the watchdog runs one check at a
// time on a test clock.

const everywhereBlockTime = 5 * time.Second

var everywhereChainID = big.NewInt(100)

// testClock is the service's clock in these tests.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Unix(1_800_000_000, 0)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advanceBlocks(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Duration(n) * everywhereBlockTime)
}

// logBuffer collects log lines written from several goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), s)
}

// forgetfulBackend accepts every send and keeps none: the endpoint of a
// pool that dropped the transaction. It records what it was sent.
type forgetfulBackend struct {
	transaction.Backend
	mu   sync.Mutex
	sent []*types.Transaction
}

func (f *forgetfulBackend) SendTransaction(_ context.Context, tx *types.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, tx)
	return nil
}

func (f *forgetfulBackend) sends() []*types.Transaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*types.Transaction(nil), f.sent...)
}

// blockTimeBackend reports a block time the test changes.
type blockTimeBackend struct {
	transaction.Backend
	d atomic.Int64
}

func (b *blockTimeBackend) AverageBlockTime() time.Duration { return time.Duration(b.d.Load()) }

type harness struct {
	t       *testing.T
	chain   *backendsimulation.Chain
	hole    *backendsimulation.BlackholeEndpoint
	backend transaction.Backend
	store   storage.StateStorer
	key     *ecdsa.PrivateKey
	sender  common.Address
	clock   *testClock
	logs    *logBuffer
	logger  log.Logger
	monitor transaction.Monitor
	svc     transaction.Service
}

type harnessConfig struct {
	blackhole bool // the active endpoint is a black hole
	healthy   int  // healthy endpoints that share the chain
	// backend, when set, replaces the failover backend; it gets the chain.
	backend func(*backendsimulation.Chain) transaction.Backend
}

func newHarness(t *testing.T, cfg harnessConfig) *harness {
	t.Helper()
	key, err := crypto.GenerateSecp256k1Key()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.NewDefaultSigner(key)
	sender, err := signer.EthereumAddress()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t:      t,
		chain:  backendsimulation.NewChain(everywhereChainID, big.NewInt(1_000_000_000)),
		store:  mock.NewStateStore(),
		key:    key,
		sender: sender,
		clock:  newTestClock(),
		logs:   &logBuffer{},
	}
	h.logger = log.NewLogger(t.Name(), log.WithSink(h.logs), log.WithVerbosity(log.VerbosityAll)).Build()

	if cfg.backend != nil {
		h.backend = cfg.backend(h.chain)
	} else {
		var endpoints []failover.Endpoint
		if cfg.blackhole {
			h.hole = h.chain.Blackhole("first")
			endpoints = append(endpoints, failover.Endpoint{Name: "first", Backend: h.hole})
		}
		for _, name := range []string{"second", "third", "fourth"}[:cfg.healthy] {
			endpoints = append(endpoints, failover.Endpoint{Name: name, Backend: h.chain.Endpoint(name)})
		}
		fb, err := failover.New(h.logger, 0, endpoints...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(fb.Close)
		h.backend = fb
	}
	h.start()
	return h
}

// start builds the monitor and the service on the harness's store, as a
// node start does.
func (h *harness) start() {
	h.t.Helper()
	h.monitor = transaction.NewMonitor(h.logger, h.backend, h.sender, 5*time.Millisecond, 1)
	svc, err := transaction.NewService(h.logger, h.sender, h.backend, crypto.NewDefaultSigner(h.key), h.store, everywhereChainID, h.monitor, 0,
		transaction.WithBlockTime(everywhereBlockTime), transaction.WithClock(h.clock.Now), transaction.WithoutWatchdog())
	if err != nil {
		h.t.Fatal(err)
	}
	h.svc = svc
	svc, monitor := h.svc, h.monitor
	h.t.Cleanup(func() {
		_ = svc.Close()
		_ = monitor.Close()
	})
}

// restart closes the service and starts a new one on the same store.
func (h *harness) restart() {
	h.t.Helper()
	_ = h.svc.Close()
	_ = h.monitor.Close()
	h.start()
}

func (h *harness) check() {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transaction.CheckPending(ctx, h.svc)
}

func (h *harness) send(ctx context.Context, description string, value int64) (common.Hash, error) {
	recipient := common.HexToAddress("0x1234")
	return h.svc.Send(ctx, &transaction.TxRequest{To: &recipient, Value: big.NewInt(value), GasLimit: 21_000, Description: description}, 0)
}

func (h *harness) stored(txHash common.Hash) *transaction.StoredTransaction {
	h.t.Helper()
	st, err := h.svc.StoredTransaction(txHash)
	if err != nil {
		h.t.Fatalf("stored transaction %s: %v", txHash, err)
	}
	return st
}

func (h *harness) isPending(txHash common.Hash) bool {
	h.t.Helper()
	hashes, err := h.svc.PendingTransactions()
	if err != nil {
		h.t.Fatal(err)
	}
	for _, p := range hashes {
		if p == txHash {
			return true
		}
	}
	return false
}

// waitReceipt mines until txHash's receipt wait ends, within a deadline.
func (h *harness) waitReceipt(txHash common.Hash) (*types.Receipt, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				h.chain.Mine()
			}
		}
	}()
	defer func() { close(stop); <-done }()
	r, err := h.svc.WaitForReceipt(ctx, txHash)
	if errors.Is(err, context.DeadlineExceeded) {
		h.t.Fatalf("receipt of %s not delivered in time (%s)", txHash, h.chain)
	}
	return r, err
}

// sendsOf counts the sends of txHash the chain's endpoints received, with
// the given answer error (nil: accepted).
func (h *harness) sendsOf(txHash common.Hash, endpoint string) int {
	n := 0
	for _, s := range h.chain.Sends() {
		if s.Tx.Hash() == txHash && (endpoint == "" || s.Endpoint == endpoint) {
			n++
		}
	}
	return n
}

// metricValue reads a gauge or counter; it fails the test on error, so a
// silent zero cannot pass an assertion.
func metricValue(t *testing.T, c prometheus.Metric) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("reading a metric: %v", err)
	}
	if m.Gauge != nil {
		return m.GetGauge().GetValue()
	}
	return m.GetCounter().GetValue()
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Test 1: a withdrawal sent while the active endpoint is a black hole is
// mined through a copy that reached another endpoint.
func TestWithdrawalIsMinedThroughAnotherEndpoint(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessConfig{blackhole: true, healthy: 2})
	txHash, err := h.send(context.Background(), "withdraw", 1)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !h.hole.Held(txHash) {
		t.Fatal("the active black hole did not get the withdrawal")
	}
	waitUntil(t, 5*time.Second, func() bool { return h.sendsOf(txHash, "second") == 1 && h.sendsOf(txHash, "third") == 1 })

	r, err := h.waitReceipt(txHash)
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if r.TxHash != txHash || r.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("receipt %s status %d, want %s mined", r.TxHash, r.Status, txHash)
	}
}

// Test 2: after the withdrawal, a persistent send at the next nonce is not
// held behind it; both are mined, in nonce order.
func TestLaterCommitIsNotBlocked(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessConfig{blackhole: true, healthy: 2})
	first, err := h.send(context.Background(), "withdraw", 1)
	if err != nil {
		t.Fatalf("withdrawal: %v", err)
	}
	ctx := transaction.WithPersistentCalls(context.Background(), time.Second)
	second, err := h.send(ctx, "commit transaction", 0)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n1, n2 := h.stored(first).Nonce, h.stored(second).Nonce; n2 != n1+1 {
		t.Fatalf("nonces %d and %d, want consecutive", n1, n2)
	}
	r2, err := h.waitReceipt(second)
	if err != nil {
		t.Fatalf("commit receipt: %v", err)
	}
	r1, err := h.waitReceipt(first)
	if err != nil {
		t.Fatalf("withdrawal receipt: %v", err)
	}
	if r1.BlockNumber.Cmp(r2.BlockNumber) > 0 {
		t.Fatalf("withdrawal mined in block %v after the commit in %v", r1.BlockNumber, r2.BlockNumber)
	}
}

// Test 7: an entry the endpoint does not know, with its nonce unused, is
// kept, also past AmbiguousPendingTimeout, and its nonce counts. With the
// nonce used on chain, it is removed.
func TestEntriesAreRemovedOnlyByTheChainNonce(t *testing.T) {
	t.Parallel()

	var forgetful *forgetfulBackend
	h := newHarness(t, harnessConfig{backend: func(c *backendsimulation.Chain) transaction.Backend {
		forgetful = &forgetfulBackend{Backend: c.Endpoint("only")}
		return forgetful
	}})

	lost, err := h.send(context.Background(), "withdraw", 1)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	h.clock.advanceBlocks(int(2 * transaction.AmbiguousPendingTimeout / everywhereBlockTime))
	time.Sleep(50 * time.Millisecond) // the monitor polls; nothing may remove the entry

	next, err := h.send(context.Background(), "withdraw", 2)
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if !h.isPending(lost) {
		t.Fatal("an entry whose nonce is unused was removed")
	}
	if got, want := h.stored(next).Nonce, h.stored(lost).Nonce+1; got != want {
		t.Fatalf("next nonce %d, want %d: the unknown entry's nonce must still count", got, want)
	}

	// another transaction uses the lost one's nonce
	other := signedTransfer(t, h.key, h.stored(lost).Nonce, big.NewInt(9_000_000_000))
	if err := h.chain.Endpoint("direct").SendTransaction(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	h.chain.Mine()

	if _, err := h.send(context.Background(), "withdraw", 3); err != nil {
		t.Fatalf("third send: %v", err)
	}
	if h.isPending(lost) {
		t.Fatal("an entry below the chain nonce was kept")
	}
	if !h.isPending(next) {
		t.Fatal("an entry at the chain nonce was removed")
	}
}

// signedTransfer signs a transfer the node's service did not store.
func signedTransfer(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, feeCap *big.Int) *types.Transaction {
	t.Helper()
	to := common.HexToAddress("0x9999")
	tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID: everywhereChainID, Nonce: nonce, To: &to, Value: big.NewInt(5), Gas: 21_000,
		GasTipCap: big.NewInt(1_000_000_000), GasFeeCap: feeCap,
	}), types.LatestSignerForChainID(everywhereChainID), key)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// Test 8: a black-holed transaction whose copies were lost is sent again
// to every endpoint after 12 blocks and mined; the age gauge rises and
// returns to 0, and the overdue line is logged once.
func TestWatchdogResendsAnOverdueEntry(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessConfig{blackhole: true, healthy: 1})
	var copiesLost atomic.Bool
	copiesLost.Store(true)
	h.chain.SetSendFunc(func(endpoint string, _ *types.Transaction) error {
		if copiesLost.Load() {
			return errors.New("connection refused")
		}
		return nil
	})
	m := transaction.WatchdogMetrics(h.svc)
	oldest, resent := m.OldestPendingAge, m.PendingResent

	txHash, err := h.send(context.Background(), "withdraw", 1)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	waitUntil(t, 5*time.Second, func() bool { return h.sendsOf(txHash, "second") == 1 })

	h.clock.advanceBlocks(transaction.OverdueBlocks - 1)
	h.check()
	if n := h.sendsOf(txHash, "second"); n != 1 {
		t.Fatalf("sent again %d times before %d blocks", n-1, transaction.OverdueBlocks)
	}
	if metricValue(t, oldest) <= 0 {
		t.Fatal("oldest pending age not set")
	}

	copiesLost.Store(false)
	h.clock.advanceBlocks(2)
	h.check()
	if n := h.sendsOf(txHash, "second"); n != 2 {
		t.Fatalf("the overdue entry reached the healthy endpoint %d times, want 2", n)
	}
	if got := metricValue(t, resent.WithLabelValues("accepted")); got != 2 {
		t.Fatalf("accepted answers %v, want 2 (already known from the black hole, accepted from the chain)", got)
	}
	if _, err := h.waitReceipt(txHash); err != nil {
		t.Fatalf("receipt: %v", err)
	}

	// settled: removed, never sent again, the gauge back to 0
	h.clock.advanceBlocks(transaction.CheckBlocks)
	h.check()
	if h.isPending(txHash) {
		t.Fatal("a settled entry is still pending")
	}
	if n := h.sendsOf(txHash, ""); n != 4 {
		t.Fatalf("%d sends in all, want 4: a settled nonce was sent again", n)
	}
	if got := metricValue(t, oldest); got != 0 {
		t.Fatalf("oldest pending age %v, want 0", got)
	}
	if n := h.logs.count("pending transaction not mined; sending it to every endpoint again"); n != 1 {
		t.Fatalf("overdue line logged %d times, want 1", n)
	}
}

// Test 9: the watchdog's limits.
func TestWatchdogLimits(t *testing.T) {
	t.Parallel()

	newForgetful := func(t *testing.T) (*harness, *forgetfulBackend) {
		t.Helper()
		var f *forgetfulBackend
		h := newHarness(t, harnessConfig{backend: func(c *backendsimulation.Chain) transaction.Backend {
			f = &forgetfulBackend{Backend: c.Endpoint("only")}
			return f
		}})
		return h, f
	}

	t.Run("newest per nonce", func(t *testing.T) {
		t.Parallel()
		h, f := newForgetful(t)
		original, err := h.send(context.Background(), "withdraw", 1)
		if err != nil {
			t.Fatal(err)
		}
		replacement, err := h.svc.ReplaceTransaction(context.Background(), original, nil)
		if err != nil {
			t.Fatal(err)
		}
		before := len(f.sends())
		h.clock.advanceBlocks(transaction.OverdueBlocks + 1)
		h.check()
		sent := f.sends()[before:]
		if len(sent) != 1 || sent[0].Hash() != replacement {
			t.Fatalf("watchdog sent %v, want only the replacement %s", hashes(sent), replacement)
		}
	})

	t.Run("ascending nonce order", func(t *testing.T) {
		t.Parallel()
		h, f := newForgetful(t)
		want := make([]common.Hash, 0, 3)
		for i := range 3 {
			txHash, err := h.send(context.Background(), "withdraw", int64(i+1))
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, txHash)
		}
		before := len(f.sends())
		h.clock.advanceBlocks(transaction.OverdueBlocks + 1)
		h.check()
		got := hashes(f.sends()[before:])
		if len(got) != len(want) {
			t.Fatalf("sent %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("sent %v, want ascending nonces %v", got, want)
			}
		}
	})

	t.Run("check interval follows the block time", func(t *testing.T) {
		t.Parallel()
		var b *blockTimeBackend
		h := newHarness(t, harnessConfig{backend: func(c *backendsimulation.Chain) transaction.Backend {
			b = &blockTimeBackend{Backend: c.Endpoint("only")}
			return b
		}})
		if got := transaction.CheckInterval(h.svc); got != transaction.CheckBlocks*everywhereBlockTime {
			t.Fatalf("interval %v before a measurement, want the configured block time", got)
		}
		b.d.Store(int64(12 * time.Second))
		if got := transaction.CheckInterval(h.svc); got != transaction.CheckBlocks*12*time.Second {
			t.Fatalf("interval %v, want %v", got, transaction.CheckBlocks*12*time.Second)
		}
		if got := transaction.OverdueAge(h.svc); got != transaction.OverdueBlocks*12*time.Second {
			t.Fatalf("overdue age %v, want %v", got, transaction.OverdueBlocks*12*time.Second)
		}
	})

	t.Run("the goroutine ends when the service closes", func(t *testing.T) {
		t.Parallel()
		chain := backendsimulation.NewChain(everywhereChainID, big.NewInt(1))
		key, _ := crypto.GenerateSecp256k1Key()
		signer := crypto.NewDefaultSigner(key)
		sender, _ := signer.EthereumAddress()
		backend := chain.Endpoint("only")
		monitor := transaction.NewMonitor(log.Noop, backend, sender, time.Millisecond, 1)
		svc, err := transaction.NewService(log.Noop, sender, backend, signer, mock.NewStateStore(), everywhereChainID, monitor, 0)
		if err != nil {
			t.Fatal(err)
		}
		// goleak in TestMain finds a watchdog left running
		if err := svc.Close(); err != nil {
			t.Fatal(err)
		}
		_ = monitor.Close()
	})
}

func hashes(txs []*types.Transaction) []common.Hash {
	out := make([]common.Hash, 0, len(txs))
	for _, tx := range txs {
		out = append(out, tx.Hash())
	}
	return out
}

// replacementOf returns the stored pending transaction that replaces
// txHash, or the zero hash.
func (h *harness) replacementOf(txHash common.Hash) common.Hash {
	h.t.Helper()
	pending, err := h.svc.PendingTransactions()
	if err != nil {
		h.t.Fatal(err)
	}
	for _, p := range pending {
		if st, err := h.svc.StoredTransaction(p); err == nil && st.Replaces == txHash {
			return p
		}
	}
	return common.Hash{}
}

// atLeastTenPercentAbove reports whether v is at least 10 % above old.
func atLeastTenPercentAbove(v, old *big.Int) bool {
	return new(big.Int).Mul(v, big.NewInt(100)).Cmp(new(big.Int).Mul(old, big.NewInt(110))) >= 0
}

// Test 10: every endpoint refuses a postage approve as underpriced. The
// watchdog signs one replacement with raised fees, stored before it is
// sent; it is mined, and the caller waiting on the original hash gets its
// receipt and succeeds.
func TestReplacementAfterAnUnderpricedRefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessConfig{healthy: 2})
	var (
		mu           sync.Mutex
		refused      common.Hash
		storedBefore = true
	)
	h.chain.SetSendFunc(func(_ string, tx *types.Transaction) error {
		mu.Lock()
		defer mu.Unlock()
		if refused == (common.Hash{}) {
			refused = tx.Hash()
		}
		if tx.Hash() == refused {
			return backendsimulation.RPCError{Msg: "transaction underpriced"}
		}
		if _, err := h.svc.StoredTransaction(tx.Hash()); err != nil {
			storedBefore = false
		}
		return nil
	})
	replacements := transaction.WatchdogMetrics(h.svc).Replacements

	approve, err := h.send(context.Background(), "approve", 0)
	if err == nil {
		t.Fatal("the refused send returned no error")
	}
	if approve == (common.Hash{}) || !h.isPending(approve) {
		t.Fatal("the refused send is not pending")
	}

	h.clock.advanceBlocks(transaction.OverdueBlocks + 1)
	h.check()
	h.check() // at most one replacement per nonce: the new one is not overdue yet

	replacement := h.replacementOf(approve)
	if replacement == (common.Hash{}) {
		t.Fatal("no replacement signed")
	}
	if got := metricValue(t, replacements.WithLabelValues("fee_too_low")); got != 1 {
		t.Fatalf("replacements{fee_too_low} = %v, want 1", got)
	}
	orig, repl := h.stored(approve), h.stored(replacement)
	if !atLeastTenPercentAbove(repl.GasFeeCap, orig.GasFeeCap) || !atLeastTenPercentAbove(repl.GasTipCap, orig.GasTipCap) {
		t.Fatalf("fee cap %v tip %v not 10%% above %v and %v", repl.GasFeeCap, repl.GasTipCap, orig.GasFeeCap, orig.GasTipCap)
	}
	if repl.Nonce != orig.Nonce || !bytes.Equal(repl.Data, orig.Data) || *repl.To != *orig.To {
		t.Fatal("the replacement does not do the same thing at the same nonce")
	}
	mu.Lock()
	if !storedBefore {
		mu.Unlock()
		t.Fatal("the replacement was sent before it was stored")
	}
	mu.Unlock()

	r, err := h.waitReceipt(approve)
	if err != nil {
		t.Fatalf("the approve's caller got %v, want success", err)
	}
	if r.TxHash != replacement {
		t.Fatalf("receipt of %s, want the replacement %s", r.TxHash, replacement)
	}
}

// refusingBackend answers every send with err.
type refusingBackend struct {
	transaction.Backend
	mu  sync.Mutex
	err error
}

func (r *refusingBackend) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	r.mu.Lock()
	err := r.err
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return r.Backend.SendTransaction(ctx, tx)
}

func newRefusing(t *testing.T, err error) (*harness, *refusingBackend) {
	t.Helper()
	var r *refusingBackend
	h := newHarness(t, harnessConfig{backend: func(c *backendsimulation.Chain) transaction.Backend {
		r = &refusingBackend{Backend: c.Endpoint("only"), err: err}
		return r
	}})
	return h, r
}

// Test 11: a fee cap below the base fee is replaced, and the fee raise
// stops at maxFeeRaise times the original's fee cap: the entry is blocked.
func TestReplacementBelowTheBaseFeeAndTheRaiseLimit(t *testing.T) {
	t.Parallel()

	h, _ := newRefusing(t, errors.New("connection refused"))
	m := transaction.WatchdogMetrics(h.svc)
	blocked, replacements := m.BlockedNonces, m.Replacements

	original, _ := h.send(context.Background(), "withdraw", 1)
	orig := h.stored(original)

	h.chain.SetBaseFee(new(big.Int).Add(orig.GasFeeCap, big.NewInt(1)))
	h.clock.advanceBlocks(transaction.OverdueBlocks + 1)
	h.check()
	first := h.replacementOf(original)
	if first == (common.Hash{}) {
		t.Fatal("a fee cap below the base fee was not replaced")
	}
	if got := metricValue(t, replacements.WithLabelValues("below_base_fee")); got != 1 {
		t.Fatalf("replacements{below_base_fee} = %v, want 1", got)
	}

	h.chain.SetBaseFee(new(big.Int).Add(h.stored(first).GasFeeCap, big.NewInt(1)))
	h.clock.advanceBlocks(transaction.OverdueBlocks + 1)
	h.check()
	if h.replacementOf(first) != (common.Hash{}) {
		t.Fatalf("a replacement above %d times the original fee cap was signed", transaction.MaxFeeRaise)
	}
	state, err := h.svc.PendingState(first)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != transaction.StateBlocked || state.BlockedReason != transaction.BlockedReasonFeeRaiseLimit {
		t.Fatalf("state %+v, want blocked with %q", state, transaction.BlockedReasonFeeRaiseLimit)
	}

	h.clock.advanceBlocks(10 * transaction.OverdueBlocks)
	h.check()
	if got := metricValue(t, replacements.WithLabelValues("below_base_fee")); got != 1 {
		t.Fatalf("replacements %v after the limit, want 1", got)
	}
	if got := metricValue(t, blocked); got != 1 {
		t.Fatalf("blocked nonces %v, want 1", got)
	}
}

// Test 12: no fee raise without a fee reason, and never for a reveal.
func TestNoFeeRaiseWithoutAFeeReason(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		answer        error
		selfReplacing bool
	}{
		{"unreachable", errors.New("connection refused"), false},
		{"replacement underpriced", backendsimulation.RPCError{Msg: "replacement transaction underpriced"}, false},
		{"reveal refused as underpriced", backendsimulation.RPCError{Msg: "transaction underpriced"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, _ := newRefusing(t, tc.answer)
			replacements := transaction.WatchdogMetrics(h.svc).Replacements
			recipient := common.HexToAddress("0x1234")
			txHash, _ := h.svc.Send(context.Background(), &transaction.TxRequest{
				To: &recipient, Value: big.NewInt(0), GasLimit: 21_000, Description: "reveal transaction", SelfReplacing: tc.selfReplacing,
			}, 0)
			if txHash == (common.Hash{}) {
				t.Fatal("not stored")
			}
			for range 3 {
				h.clock.advanceBlocks(transaction.OverdueBlocks + 1)
				h.check()
			}
			if r := h.replacementOf(txHash); r != (common.Hash{}) {
				t.Fatalf("replacement %s signed", r)
			}
			for _, reason := range []string{"fee_too_low", "below_base_fee"} {
				if got := metricValue(t, replacements.WithLabelValues(reason)); got != 0 {
					t.Fatalf("replacements{%s} = %v", reason, got)
				}
			}
		})
	}
}

// Test 13: a cancellation mined at the nonce is reported to the original's
// caller as a cancellation, never as success.
func TestCancellationIsReportedAsCancellation(t *testing.T) {
	t.Parallel()

	t.Run("cancelled by the node", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessConfig{healthy: 1})
		original, err := h.send(context.Background(), "withdraw", 1)
		if err != nil {
			t.Fatal(err)
		}
		cancellation, err := h.svc.CancelTransaction(context.Background(), original)
		if err != nil {
			t.Fatal(err)
		}
		if h.stored(cancellation).Replaces != original {
			t.Fatal("the cancellation does not record what it replaces")
		}
		if _, err := h.waitReceipt(original); !errors.Is(err, transaction.ErrTransactionCancelled) {
			t.Fatalf("the original's caller got %v, want ErrTransactionCancelled", err)
		}
	})

	t.Run("nonce used by a transaction the node did not store", func(t *testing.T) {
		t.Parallel()
		var forgetful *forgetfulBackend
		h := newHarness(t, harnessConfig{backend: func(c *backendsimulation.Chain) transaction.Backend {
			forgetful = &forgetfulBackend{Backend: c.Endpoint("only")}
			return forgetful
		}})
		original, err := h.send(context.Background(), "withdraw", 1)
		if err != nil {
			t.Fatal(err)
		}
		other := signedTransfer(t, h.key, h.stored(original).Nonce, big.NewInt(9_000_000_000))
		if err := h.chain.Endpoint("direct").SendTransaction(context.Background(), other); err != nil {
			t.Fatal(err)
		}
		if _, err := h.waitReceipt(original); !errors.Is(err, transaction.ErrTransactionCancelled) {
			t.Fatalf("the original's caller got %v, want ErrTransactionCancelled", err)
		}
	})
}

// Test 14: every endpoint answers "insufficient funds": the entry is
// blocked, logged and counted, and its nonce still counts; nothing frees
// it while time passes. After funding, a cancellation frees it.
func TestBlockedNonceOnInsufficientFunds(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessConfig{healthy: 2})
	h.chain.SetFunds(h.sender, big.NewInt(0))
	blocked := transaction.WatchdogMetrics(h.svc).BlockedNonces

	withdrawal, _ := h.send(context.Background(), "withdraw", 1)
	if !h.isPending(withdrawal) {
		t.Fatal("not pending")
	}
	h.clock.advanceBlocks(transaction.OverdueBlocks + 1)
	h.check()
	h.check()

	state, err := h.svc.PendingState(withdrawal)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != transaction.StateBlocked || !strings.Contains(state.BlockedReason, "insufficient funds") {
		t.Fatalf("state %+v, want blocked on insufficient funds", state)
	}
	if got := metricValue(t, blocked); got != 1 {
		t.Fatalf("blocked nonces %v, want 1", got)
	}
	if nonce, reason, ok := h.svc.BlockedNonce(); !ok || nonce != h.stored(withdrawal).Nonce || reason != state.BlockedReason {
		t.Fatalf("BlockedNonce() = %d, %q, %v", nonce, reason, ok)
	}
	if n := h.logs.count("pending transaction cannot be mined; free its nonce with DELETE /transactions/{hash}"); n != 1 {
		t.Fatalf("blocked line logged %d times, want 1", n)
	}

	later, _ := h.send(context.Background(), "withdraw", 2)
	if got, want := h.stored(later).Nonce, h.stored(withdrawal).Nonce+1; got != want {
		t.Fatalf("next nonce %d, want %d: a blocked nonce still counts", got, want)
	}

	sendsBefore := h.sendsOf(withdrawal, "")
	for range 5 {
		h.clock.advanceBlocks(transaction.OverdueBlocks + transaction.CheckBlocks)
		h.check()
	}
	if !h.isPending(withdrawal) {
		t.Fatal("time freed a blocked nonce")
	}
	if n := h.sendsOf(withdrawal, ""); n != sendsBefore {
		t.Fatalf("a blocked entry was sent again %d times", n-sendsBefore)
	}

	// the operator funds the node and cancels the blocked transaction
	h.chain.SetFunds(h.sender, new(big.Int).Lsh(big.NewInt(1), 100))
	cancellation, err := h.svc.CancelTransaction(context.Background(), withdrawal)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if h.stored(cancellation).Nonce != h.stored(withdrawal).Nonce {
		t.Fatal("the cancellation is not at the blocked nonce")
	}
	if _, err := h.waitReceipt(cancellation); err != nil {
		t.Fatalf("cancellation receipt: %v", err)
	}
	// the later transaction is resent by the operator, which clears its mark
	if _, err := h.svc.ResendTransaction(context.Background(), later); err != nil {
		t.Fatalf("resend: %v", err)
	}
	h.check()
	if h.isPending(withdrawal) {
		t.Fatal("the cancelled entry is still pending")
	}
	if got := metricValue(t, blocked); got != 0 {
		t.Fatalf("blocked nonces %v after the cancellation, want 0", got)
	}
}

// Test 15 (the service side): ResendTransaction sends the stored bytes
// exactly, and never asks for a fee.
func TestResendSendsTheStoredBytes(t *testing.T) {
	t.Parallel()

	var f *forgetfulBackend
	var feeAsked atomic.Int32
	h := newHarness(t, harnessConfig{backend: func(c *backendsimulation.Chain) transaction.Backend {
		f = &forgetfulBackend{Backend: &feeCounter{Backend: c.Endpoint("only"), n: &feeAsked}}
		return f
	}})
	txHash, err := h.send(context.Background(), "withdraw", 1)
	if err != nil {
		t.Fatal(err)
	}
	raw := h.stored(txHash).Raw
	feeAsked.Store(0)
	res, err := h.svc.ResendTransaction(context.Background(), txHash)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != transaction.StatePending || len(res.Answers) != 1 {
		t.Fatalf("result %+v", res)
	}
	sent := f.sends()
	got, err := sent[len(sent)-1].MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("the resent bytes differ from the stored ones")
	}
	if n := feeAsked.Load(); n != 0 {
		t.Fatalf("SuggestedFeeAndTip called %d times", n)
	}
}

type feeCounter struct {
	transaction.Backend
	n *atomic.Int32
}

func (f *feeCounter) SuggestedFeeAndTip(ctx context.Context, gasPrice *big.Int, boost int) (*big.Int, *big.Int, error) {
	f.n.Add(1)
	return f.Backend.SuggestedFeeAndTip(ctx, gasPrice, boost)
}
