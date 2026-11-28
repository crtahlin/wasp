// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storageincentives

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/log"
	contractMock "github.com/ethersphere/bee/v2/pkg/postage/postagecontract/mock"
	"github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/redistribution"
	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

// Tests 1 to 5 of docs/experiments/claim-iswinner-retry/spec.md (#855).
//
// They call handleClaim directly, so the phase events that start the claim
// phase and give it its context are not exercised here.

// winnerRevertError is what go-ethereum returns for a reverted eth_call: an
// rpc.DataError carrying the revert data as a hex string.
type winnerRevertError struct{ data string }

func (winnerRevertError) Error() string    { return "execution reverted" }
func (winnerRevertError) ErrorCode() int   { return 3 }
func (e winnerRevertError) ErrorData() any { return e.data }

var (
	errNotClaimPhase  = winnerRevertError{"0x6f118d82"} // NotClaimPhase()
	errAlreadyClaimed = winnerRevertError{"0x646cf558"} // AlreadyClaimed()
)

// scriptedWinnerContract answers IsWinner from answer, called with the
// 1-based number of the call, and records claims.
type scriptedWinnerContract struct {
	*fakeContract
	answer func(call int) (bool, error)
	salt   []byte

	mu     sync.Mutex
	calls  int
	claims int
}

func (c *scriptedWinnerContract) IsWinner(context.Context) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.answer(c.calls)
}

func (c *scriptedWinnerContract) ReserveSalt(context.Context) ([]byte, error) {
	return c.salt, nil
}

// Claim sends the claim as fakeContract.Commit does: it calls the hook with
// a fresh hash, and the transaction is mined at once.
func (c *scriptedWinnerContract) Claim(_ context.Context, _ redistribution.ChunkInclusionProofs, hook transaction.BeforeBroadcastFunc) (common.Hash, error) {
	h := c.txs.newHash()
	if err := hook(h); err != nil {
		return common.Hash{}, fmt.Errorf("claim: %w: %w", transaction.ErrNotBroadcast, err)
	}
	c.mu.Lock()
	c.claims++
	c.mu.Unlock()
	c.txs.set(h, transaction.TxMined)
	return h, nil
}

func (c *scriptedWinnerContract) counts() (calls, claims int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.claims
}

// winnerCheckRun is what runWinnerCheck observed after one handleClaim.
type winnerCheckRun struct {
	err       error
	calls     int
	claims    int
	errWinner float64
	retries   float64
	recovered float64
	warnings  int // "winner check failed after retries" lines
	notWinner int // "not a winner" lines
}

// runWinnerCheck runs one claim phase of a node that revealed, with the
// given IsWinner answers, under ctx.
func runWinnerCheck(t *testing.T, ctx context.Context, answer func(call int) (bool, error)) winnerCheckRun {
	t.Helper()
	const round = 1
	f := newRevealFixture(t, mock.NewStateStore(), newFakeChain(13), newFakeTxs())
	logs := &lockedBuffer{}
	f.agent.logger = log.NewLogger(t.Name(), log.WithSink(logs), log.WithVerbosity(log.VerbosityAll)).Build()
	f.agent.blockTime = func() time.Duration { return testBlockTime }
	f.agent.batchExpirer = contractMock.New(contractMock.WithExpiresBatchesFunc(func(context.Context) error { return nil }))
	contract := &scriptedWinnerContract{fakeContract: f.contract, answer: answer, salt: testutil.RandBytes(t, 32)}
	f.agent.contract = contract

	anchor1 := testutil.RandBytes(t, 32)
	sample := storer.RandSample(t, anchor1)
	f.state().SetSampleData(round-1, SampleData{Anchor1: anchor1, ReserveSampleItems: sample.Items}, 0)
	f.state().SetHasRevealed(round)

	err := f.agent.handleClaim(ctx, round)
	calls, claims := contract.counts()
	return winnerCheckRun{
		err:       err,
		calls:     calls,
		claims:    claims,
		errWinner: gaugeValue(t, f.agent.metrics.ErrWinner),
		retries:   gaugeValue(t, f.agent.metrics.WinnerCheckRetries),
		recovered: gaugeValue(t, f.agent.metrics.WinnerCheckRecovered),
		warnings:  logs.count("winner check failed after retries"),
		notWinner: logs.count("not a winner"),
	}
}

// Test 1: a NotClaimPhase() revert, then true. The node claims once in the
// same claim phase, the error is not counted and the check is counted as
// recovered.
func TestWinnerCheckRetriesNotClaimPhase(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := runWinnerCheck(t, context.Background(), func(call int) (bool, error) {
			if call == 1 {
				return false, errNotClaimPhase
			}
			return true, nil
		})
		if r.err != nil {
			t.Fatalf("handleClaim: %v", r.err)
		}
		if r.calls != 2 {
			t.Fatalf("%d IsWinner calls, want 2", r.calls)
		}
		if r.claims != 1 {
			t.Fatalf("%d claims, want 1", r.claims)
		}
		if r.errWinner != 0 {
			t.Fatalf("win_phase_errors = %v, want 0", r.errWinner)
		}
		if r.retries != 1 || r.recovered != 1 {
			t.Fatalf("winner_check_retries = %v, winner_check_recovered = %v, want 1 and 1", r.retries, r.recovered)
		}
	})
}

// Test 2: a revert with another selector holds for the round. It is not
// asked again, and the error is returned and counted once, as before.
func TestWinnerCheckOtherRevertNotRetried(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := runWinnerCheck(t, context.Background(), func(int) (bool, error) {
			return false, errAlreadyClaimed
		})
		if !errors.Is(r.err, errAlreadyClaimed) {
			t.Fatalf("handleClaim = %v, want the AlreadyClaimed() revert", r.err)
		}
		if r.calls != 1 {
			t.Fatalf("%d IsWinner calls, want 1", r.calls)
		}
		if r.claims != 0 {
			t.Fatalf("%d claims, want 0", r.claims)
		}
		if r.errWinner != 1 {
			t.Fatalf("win_phase_errors = %v, want 1", r.errWinner)
		}
		if r.retries != 0 || r.recovered != 0 {
			t.Fatalf("winner_check_retries = %v, winner_check_recovered = %v, want 0 and 0", r.retries, r.recovered)
		}
	})
}

// Test 3: an error without revert data, then false. The check is asked
// once more and ends with "not a winner"; nothing is claimed.
//
// This covers the agent's rule only. In a running node the claim phase's
// context is marked persistent, so the failover backend keeps retrying a
// transport error itself and rarely returns one to the agent before the
// phase ends.
func TestWinnerCheckTransportErrorThenFalse(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := runWinnerCheck(t, context.Background(), func(call int) (bool, error) {
			if call == 1 {
				return false, errors.New("connection reset")
			}
			return false, nil
		})
		if r.err != nil {
			t.Fatalf("handleClaim: %v", r.err)
		}
		if r.calls != 2 {
			t.Fatalf("%d IsWinner calls, want 2", r.calls)
		}
		if r.claims != 0 {
			t.Fatalf("%d claims, want 0", r.claims)
		}
		if r.notWinner != 1 {
			t.Fatalf("%d \"not a winner\" lines, want 1", r.notWinner)
		}
		if r.errWinner != 0 || r.retries != 1 || r.recovered != 1 {
			t.Fatalf("win_phase_errors = %v, retries = %v, recovered = %v, want 0, 1, 1", r.errWinner, r.retries, r.recovered)
		}
	})
}

// Test 4: every attempt fails. The check is asked winnerCheckAttempts times,
// then the last error is returned, counted once and warned about once.
func TestWinnerCheckGivesUp(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := runWinnerCheck(t, ctx, func(call int) (bool, error) {
			// Timers in the bubble fire at once, so a check without its
			// attempt bound would loop for ever. Report it and end the
			// claim phase instead.
			if call > winnerCheckAttempts {
				t.Errorf("IsWinner call %d, want at most %d", call, winnerCheckAttempts)
				cancel()
			}
			return false, errNotClaimPhase
		})
		if !errors.Is(r.err, errNotClaimPhase) {
			t.Fatalf("handleClaim = %v, want the NotClaimPhase() revert", r.err)
		}
		if r.calls != winnerCheckAttempts {
			t.Fatalf("%d IsWinner calls, want %d", r.calls, winnerCheckAttempts)
		}
		if r.claims != 0 {
			t.Fatalf("%d claims, want 0", r.claims)
		}
		if r.errWinner != 1 {
			t.Fatalf("win_phase_errors = %v, want 1 per claim phase", r.errWinner)
		}
		if r.warnings != 1 {
			t.Fatalf("%d warnings, want 1", r.warnings)
		}
		if r.retries != winnerCheckAttempts-1 || r.recovered != 0 {
			t.Fatalf("winner_check_retries = %v, winner_check_recovered = %v, want %d and 0", r.retries, r.recovered, winnerCheckAttempts-1)
		}
	})
}

// Test 5: the claim phase ends during the wait. The check returns the error
// without asking again, instead of using up its attempts. Two cases: the
// context has already ended when the first call fails, and the context ends
// while the timer before the second call is pending.
func TestWinnerCheckStopsAtPhaseEnd(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// setup returns the claim phase's context, its cancel and the IsWinner
		// answers.
		setup func() (context.Context, context.CancelFunc, func(int) (bool, error))
	}{
		{"already ended", func() (context.Context, context.CancelFunc, func(int) (bool, error)) {
			// A deadline already in the past cancels synchronously.
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			return ctx, cancel, func(int) (bool, error) { return false, errNotClaimPhase }
		}},
		{"ends during the wait", func() (context.Context, context.CancelFunc, func(int) (bool, error)) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, cancel, func(call int) (bool, error) {
				if call == 1 {
					// The bubble's clock runs only when every goroutine
					// waits, so this fires halfway through the one-block
					// wait that follows this answer, before the retry.
					time.AfterFunc(testBlockTime/2, cancel)
				}
				return false, errNotClaimPhase
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				ctx, cancel, answer := tc.setup()
				defer cancel()
				start := time.Now()
				r := runWinnerCheck(t, ctx, answer)
				if !errors.Is(r.err, errNotClaimPhase) {
					t.Fatalf("handleClaim = %v, want the NotClaimPhase() revert", r.err)
				}
				if r.calls != 1 {
					t.Fatalf("%d IsWinner calls after the context ended, want 1", r.calls)
				}
				if r.errWinner != 1 || r.warnings != 1 {
					t.Fatalf("win_phase_errors = %v, warnings = %d, want 1 and 1", r.errWinner, r.warnings)
				}
				if waited := time.Since(start); waited >= testBlockTime {
					t.Fatalf("returned after %v, want before the %v retry timer", waited, testBlockTime)
				}
			})
		})
	}
}
