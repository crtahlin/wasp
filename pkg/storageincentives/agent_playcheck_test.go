// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storageincentives_test

import (
	"bytes"
	"context"
	"math/big"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/postage"
	contractMock "github.com/ethersphere/bee/v2/pkg/postage/postagecontract/mock"
	statestore "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storageincentives"
	stakingmock "github.com/ethersphere/bee/v2/pkg/storageincentives/staking/mock"
	"github.com/ethersphere/bee/v2/pkg/storer"
	resMock "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	transactionmock "github.com/ethersphere/bee/v2/pkg/transaction/mock"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

// lockedBuffer is a log sink safe for the agent's goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// revertError is what go-ethereum returns for a reverted eth_call: an
// rpc.DataError carrying the revert data as a hex string.
type revertError struct{ data string }

func (revertError) Error() string    { return "execution reverted" }
func (revertError) ErrorCode() int   { return 3 }
func (e revertError) ErrorData() any { return e.data }

var (
	errWrongPhase = revertError{"0xe2586bcc"} // WrongPhase()
	errNotStaked  = revertError{"0x039f2e18"} // NotStaked()
)

// playCheckContract is a mockContract whose IsPlaying answers come from
// answer, called with the 1-based number of the call within one play check.
// A new play check starts after a successful call or after six calls.
type playCheckContract struct {
	*mockContract
	answer func(call int) (bool, error)

	mu      sync.Mutex
	calls   int // all IsPlaying calls
	inCheck int // calls in the current play check
}

func (c *playCheckContract) IsPlaying(context.Context, uint8) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.inCheck++
	playing, err := c.answer(c.inCheck)
	if err == nil || c.inCheck == 6 {
		c.inCheck = 0
	}
	return playing, err
}

// snapshot returns the number of IsPlaying calls so far.
func (c *playCheckContract) snapshot() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// playCheckRun is what runPlayCheckAgent observed.
type playCheckRun struct {
	calls          []contractCall
	isPlayingCalls int
	retries        float64
	recovered      float64
	errs           float64
	warnings       int // "play check failed after retries" lines
}

// runPlayCheckAgent runs an agent through several rounds with the given
// IsPlaying behaviour.
func runPlayCheckAgent(t *testing.T, answer func(call int) (bool, error)) playCheckRun {
	t.Helper()
	var run playCheckRun
	synctest.Test(t, func(t *testing.T) {
		const (
			blocksPerRound = 9
			blocksPerPhase = 3
		)
		var radius uint8 = 8
		blockTime := func() time.Duration { return 100 * time.Millisecond }
		wait := make(chan struct{}, 1)
		backend := &mockchainBackend{
			limit:         blocksPerRound * 6,
			limitCallback: func() { wait <- struct{}{} },
			incrementBy:   1,
			block:         blocksPerRound,
			balance:       big.NewInt(4_000_000_000),
		}
		mc := &mockContract{t: t, expectedRadius: radius}
		contract := &playCheckContract{mockContract: mc, answer: answer}
		sink := &lockedBuffer{}
		logger := log.NewLogger(t.Name(), log.WithSink(sink), log.WithVerbosity(log.VerbosityWarning))

		reserve := resMock.NewReserve(resMock.WithRadius(radius), resMock.WithSample(storer.RandSample(t, nil)))
		postageContract := contractMock.New(contractMock.WithExpiresBatchesFunc(func(context.Context) error { return nil }))
		stakingContract := stakingmock.New(stakingmock.WithIsFrozen(func(context.Context, uint64) (bool, error) { return false, nil }))
		revealer, err := newRevealer(statestore.NewStateStore(), contract, transactionmock.New(), revealBackend{backend}, blockTime, blocksPerRound, blocksPerPhase)
		if err != nil {
			t.Fatal(err)
		}
		agent, err := storageincentives.New(
			swarm.RandAddress(t),
			backend, contract, postageContract, stakingContract, reserve,
			func() bool { return true },
			blockTime,
			blocksPerRound, blocksPerPhase,
			revealer, &postage.NoOpBatchStore{},
			&mockHealth{}, logger, storer.ReserveProofModeClassic,
		)
		if err != nil {
			t.Fatal(err)
		}
		testutil.CleanupCloser(t, agent)

		<-wait
		synctest.Wait()
		run = playCheckRun{
			calls:          mc.getCalls(),
			isPlayingCalls: contract.snapshot(),
			retries:        counterValue(t, agent, "play_check_retries"),
			recovered:      counterValue(t, agent, "play_check_recovered"),
			errs:           counterValue(t, agent, "is_playing_errors"),
			warnings:       strings.Count(sink.String(), "play check failed after retries"),
		}
	})
	return run
}

// TestPlayCheckRetriesWrongPhase checks #773: a play check that reverts with
// WrongPhase() once and then answers true is asked again a block later, and
// the node plays the round.
func TestPlayCheckRetriesWrongPhase(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		first = true
		at    []time.Time // times of the first two calls
	)
	r := runPlayCheckAgent(t, func(int) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(at) < 2 {
			at = append(at, time.Now())
		}
		if first {
			first = false
			return false, errWrongPhase
		}
		return true, nil
	})
	// The retry waits one block time (100 ms in the test) before asking again.
	if len(at) < 2 || at[1].Sub(at[0]) < 100*time.Millisecond {
		t.Fatalf("second play check %v after the first, want at least one block time", at)
	}
	if !hasCommit(r.calls) {
		t.Fatalf("no commit after a recovered play check (calls %v)", r.calls)
	}
	if r.retries != 1 || r.recovered != 1 {
		t.Fatalf("play_check_retries %v, play_check_recovered %v, want 1 and 1", r.retries, r.recovered)
	}
	if r.errs != 0 || r.warnings != 0 {
		t.Fatalf("is_playing_errors %v, warnings %d, want 0 and 0", r.errs, r.warnings)
	}
}

// TestPlayCheckGivesUp checks that a play check that reverts with
// WrongPhase() on every call is asked playCheckAttempts times per round, the
// node does not play, and each round's failure is counted once.
func TestPlayCheckGivesUp(t *testing.T) {
	t.Parallel()

	r := runPlayCheckAgent(t, func(int) (bool, error) {
		return false, errWrongPhase
	})
	calls, n, retries, recovered, errs := r.calls, r.isPlayingCalls, r.retries, r.recovered, r.errs
	if hasCommit(calls) {
		t.Fatalf("committed although every play check failed (calls %v)", calls)
	}
	if errs < 1 {
		t.Fatalf("is_playing_errors %v, want at least 1", errs)
	}
	// The run can end in the middle of a check, so the calls are six per
	// finished check plus up to five of the check still running.
	const attempts = 6
	if n < attempts*int(errs) || n >= attempts*(int(errs)+1) {
		t.Fatalf("%d IsPlaying calls for %v failed checks, want %d per check", n, errs, attempts)
	}
	// Every failed call is either asked again or ends its check.
	if retries != float64(n)-errs {
		t.Fatalf("play_check_retries %v for %d calls and %v failed checks, want %v", retries, n, errs, float64(n)-errs)
	}
	if recovered != 0 {
		t.Fatalf("play_check_recovered %v, want 0", recovered)
	}
	if float64(r.warnings) != errs {
		t.Fatalf("%d warnings for %v failed checks, want one each", r.warnings, errs)
	}
}

// TestPlayCheckNoRetry checks the answers that are never asked again: false,
// which is the contract's answer, and a revert with another selector
// (NotStaked()), which holds for the whole round.
func TestPlayCheckNoRetry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		answer  func(int) (bool, error)
		wantErr bool
	}{
		{"not chosen", func(int) (bool, error) { return false, nil }, false},
		{"not staked", func(int) (bool, error) { return false, errNotStaked }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := runPlayCheckAgent(t, tc.answer)
			calls, n, retries, recovered, errs := r.calls, r.isPlayingCalls, r.retries, r.recovered, r.errs
			if hasCommit(calls) {
				t.Fatalf("committed (calls %v)", calls)
			}
			if n < 1 {
				t.Fatal("IsPlaying was never called")
			}
			if retries != 0 || recovered != 0 {
				t.Fatalf("play_check_retries %v, play_check_recovered %v, want 0 and 0", retries, recovered)
			}
			if tc.wantErr && errs != float64(n) {
				t.Fatalf("is_playing_errors %v for %d calls, want one per call", errs, n)
			}
			if !tc.wantErr && errs != 0 {
				t.Fatalf("is_playing_errors %v, want 0", errs)
			}
			if r.warnings != 0 {
				t.Fatalf("%d retry warnings, want 0", r.warnings)
			}
		})
	}
}

// directCallKey marks the IsPlaying calls a test makes itself, apart from the
// agent's own calls in the background.
type directCallKey struct{}

// countingContract counts the IsPlaying calls made with directCallKey and
// answers them with WrongPhase().
type countingContract struct {
	*mockContract
	mu     sync.Mutex
	direct int
}

func (c *countingContract) IsPlaying(ctx context.Context, _ uint8) (bool, error) {
	if ctx.Value(directCallKey{}) != nil {
		c.mu.Lock()
		c.direct++
		c.mu.Unlock()
	}
	return false, errWrongPhase
}

// TestPlayCheckStopsAtPhaseEnd checks that the play check stops asking when
// its context ends during the wait, instead of using up its attempts.
func TestPlayCheckStopsAtPhaseEnd(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		mc := &mockContract{t: t, expectedRadius: 8}
		contract := &countingContract{mockContract: mc}
		backend := &mockchainBackend{block: 0, balance: big.NewInt(4_000_000_000)}
		agent, err := createServiceWithBlockTime(t, swarm.RandAddress(t), backend, contract,
			func() time.Duration { return time.Hour }, 9, 3, 8, 0, storer.ReserveProofModeClassic)
		if err != nil {
			t.Fatal(err)
		}
		testutil.CleanupCloser(t, agent)

		// A deadline already in the past cancels synchronously.
		ctx, cancel := context.WithDeadline(context.WithValue(context.Background(), directCallKey{}, true), time.Now().Add(-time.Second))
		defer cancel()
		playing, err := agent.IsPlaying(ctx, 8)
		if playing || err != nil {
			t.Fatalf("IsPlaying = %v, %v, want false, nil", playing, err)
		}
		contract.mu.Lock()
		direct := contract.direct
		contract.mu.Unlock()
		if direct != 1 {
			t.Fatalf("%d IsPlaying calls after the context ended, want 1", direct)
		}
	})
}
