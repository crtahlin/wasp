// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storageincentives

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/statestore/mock"
)

// Tests 1 to 6 of docs/experiments/commit-round-wait/spec.md (#838).
//
// They call handleCommit directly, so the phase events that start the
// commit phase and give it its context are not exercised here.

// commitRoundTestRound is the round the node commits in. The chain starts at
// the first block of that round, with the mainnet phase length, so a wait of
// commitRoundAttempts block times stays inside the commit phase.
const commitRoundTestRound = 10

// scriptedRoundContract answers CurrentRound from answer, called with the
// 1-based number of the call, and records how many commits had been sent
// when each call was made.
type scriptedRoundContract struct {
	*fakeContract
	answer func(call int) (uint64, error)

	mu            sync.Mutex
	calls         int
	commitsAtCall []int
}

func (c *scriptedRoundContract) CurrentRound(context.Context) (uint64, error) {
	commits, _ := c.counts()
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.commitsAtCall = append(c.commitsAtCall, commits)
	c.mu.Unlock()
	return c.answer(n)
}

func (c *scriptedRoundContract) reads() (calls int, commitsAtCall []int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, append([]int(nil), c.commitsAtCall...)
}

// commitRoundRun is what runCommitRound observed after one handleCommit.
type commitRoundRun struct {
	err           error
	elapsed       time.Duration
	reads         int
	commitsAtRead []int
	commits       int
	lastPlayed    uint64
	waits         float64
	caughtUp      float64
	mismatch      float64
	warnings      int // "commit round wait ran out" lines at warning level
}

// commitRoundWarningLines counts the captured log lines at warning level
// whose message is msg.
func commitRoundWarningLines(logs, msg string) int {
	n := 0
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, `"level"="warning"`) && strings.Contains(line, `"msg"="`+msg+`"`) {
			n++
		}
	}
	return n
}

// runCommitRound runs one commit phase of a node that has a sample, with the
// given CurrentRound answers, under ctx.
func runCommitRound(t *testing.T, ctx context.Context, answer func(call int) (uint64, error)) commitRoundRun {
	t.Helper()
	const round = commitRoundTestRound
	f := newRevealFixture(t, mock.NewStateStore(), newFakeChain(round*DefaultBlocksPerRound), newFakeTxs())
	f.blocksPerRound, f.blocksPerPhase = DefaultBlocksPerRound, DefaultBlocksPerPhase
	f.restart(t)
	logs := &lockedBuffer{}
	f.agent.logger = log.NewLogger(t.Name(), log.WithSink(logs), log.WithVerbosity(log.VerbosityAll)).Build()
	f.agent.blockTime = func() time.Duration { return testBlockTime }
	contract := &scriptedRoundContract{fakeContract: f.contract, answer: answer}
	f.agent.contract = contract
	f.setSample(round)

	start := time.Now()
	err := f.agent.handleCommit(ctx, round)
	elapsed := time.Since(start)

	reads, commitsAtRead := contract.reads()
	commits, _ := f.contract.counts()
	status, serr := f.state().Status()
	if serr != nil {
		t.Fatal(serr)
	}
	return commitRoundRun{
		err:           err,
		elapsed:       elapsed,
		reads:         reads,
		commitsAtRead: commitsAtRead,
		commits:       commits,
		lastPlayed:    status.LastPlayedRound,
		waits:         gaugeValue(t, f.agent.metrics.CommitRoundWaits),
		caughtUp:      gaugeValue(t, f.agent.metrics.CommitRoundCaughtUp),
		mismatch:      gaugeValue(t, f.agent.metrics.RoundMismatch),
		warnings:      commitRoundWarningLines(logs.String(), "commit round wait ran out, committing"),
	}
}

// Test 1: the contract reports the previous round, then the node's. The
// commit is sent once, after one block time, and not before the contract
// reports the node's round.
func TestCommitRoundWaitsOneBlock(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := runCommitRound(t, context.Background(), func(call int) (uint64, error) {
			if call == 1 {
				return commitRoundTestRound - 1, nil
			}
			return commitRoundTestRound, nil
		})
		if r.err != nil {
			t.Fatalf("handleCommit: %v", r.err)
		}
		if r.reads != 2 {
			t.Fatalf("%d CurrentRound reads, want 2", r.reads)
		}
		if r.commits != 1 {
			t.Fatalf("%d commits, want 1", r.commits)
		}
		if r.commitsAtRead[1] != 0 {
			t.Fatalf("%d commits sent before the contract reported the node's round, want 0", r.commitsAtRead[1])
		}
		if r.elapsed != testBlockTime {
			t.Fatalf("commit sent after %v, want one block time (%v)", r.elapsed, testBlockTime)
		}
		if r.waits != 1 || r.caughtUp != 1 {
			t.Fatalf("commit_round_waits = %v, commit_round_caught_up = %v, want 1 and 1", r.waits, r.caughtUp)
		}
		if r.lastPlayed != commitRoundTestRound {
			t.Fatalf("last played round %d, want %d", r.lastPlayed, commitRoundTestRound)
		}
	})
}

// Test 2: the contract reports the node's round at once, or the round after
// it. The commit is sent without a wait and nothing is counted.
func TestCommitRoundNoWaitWhenAgreed(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		contractRound uint64
	}{
		{"same round", commitRoundTestRound},
		{"contract one round ahead", commitRoundTestRound + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				r := runCommitRound(t, context.Background(), func(int) (uint64, error) {
					return tc.contractRound, nil
				})
				if r.err != nil {
					t.Fatalf("handleCommit: %v", r.err)
				}
				if r.reads != 1 || r.commits != 1 {
					t.Fatalf("%d CurrentRound reads and %d commits, want 1 and 1", r.reads, r.commits)
				}
				if r.elapsed != 0 {
					t.Fatalf("commit sent after %v, want no wait", r.elapsed)
				}
				if r.waits != 0 || r.caughtUp != 0 || r.mismatch != 0 {
					t.Fatalf("commit_round_waits = %v, commit_round_caught_up = %v, round_mismatch = %v, want all 0",
						r.waits, r.caughtUp, r.mismatch)
				}
			})
		})
	}
}

// Test 3: the contract reports the previous round on every read. The node
// reads commitRoundAttempts times, then commits with one warning.
func TestCommitRoundGivesUp(t *testing.T) {
	t.Parallel()

	// The spec fixes the bound at 6 reads.
	if commitRoundAttempts != 6 {
		t.Fatalf("commitRoundAttempts = %d, want 6 as the spec sets it", commitRoundAttempts)
	}

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := runCommitRound(t, ctx, func(call int) (uint64, error) {
			// Timers in the bubble fire at once, so a wait without its
			// attempt bound would loop for ever. Report it and end the
			// commit phase instead.
			if call > commitRoundAttempts {
				t.Errorf("CurrentRound read %d, want at most %d", call, commitRoundAttempts)
				cancel()
			}
			return commitRoundTestRound - 1, nil
		})
		if r.err != nil {
			t.Fatalf("handleCommit: %v", r.err)
		}
		if r.reads != commitRoundAttempts {
			t.Fatalf("%d CurrentRound reads, want %d", r.reads, commitRoundAttempts)
		}
		if r.commits != 1 {
			t.Fatalf("%d commits, want 1", r.commits)
		}
		for i, c := range r.commitsAtRead {
			if c != 0 {
				t.Fatalf("%d commits sent before read %d, want 0", c, i+1)
			}
		}
		if want := time.Duration(commitRoundAttempts-1) * testBlockTime; r.elapsed != want {
			t.Fatalf("commit sent after %v, want %v", r.elapsed, want)
		}
		if r.warnings != 1 {
			t.Fatalf("%d warnings, want 1", r.warnings)
		}
		if r.waits != commitRoundAttempts-1 || r.caughtUp != 0 {
			t.Fatalf("commit_round_waits = %v, commit_round_caught_up = %v, want %d and 0", r.waits, r.caughtUp, commitRoundAttempts-1)
		}
	})
}

// Test 4: the commit phase ends during the wait. No commit is sent, the
// contract is not read again and the phase ends without an error. Two
// cases: the context has already ended when the first read is answered,
// and the context ends while the timer before the second read is pending.
func TestCommitRoundStopsAtPhaseEnd(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// setup returns the commit phase's context, its cancel and the
		// CurrentRound answers.
		setup func() (context.Context, context.CancelFunc, func(int) (uint64, error))
	}{
		{"already ended", func() (context.Context, context.CancelFunc, func(int) (uint64, error)) {
			// A deadline already in the past cancels synchronously.
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			return ctx, cancel, func(int) (uint64, error) { return commitRoundTestRound - 1, nil }
		}},
		{"ends during the wait", func() (context.Context, context.CancelFunc, func(int) (uint64, error)) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, cancel, func(int) (uint64, error) {
				cancel()
				return commitRoundTestRound - 1, nil
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				ctx, cancel, answer := tc.setup()
				defer cancel()
				r := runCommitRound(t, ctx, answer)
				if r.err != nil {
					t.Fatalf("handleCommit = %v, want nil when the phase ends", r.err)
				}
				if r.reads != 1 {
					t.Fatalf("%d CurrentRound reads, want 1", r.reads)
				}
				if r.commits != 0 {
					t.Fatalf("%d commits, want 0", r.commits)
				}
				if r.lastPlayed != 0 {
					t.Fatalf("last played round %d, want 0", r.lastPlayed)
				}
				if r.elapsed != 0 {
					t.Fatalf("handleCommit returned after %v, want at once", r.elapsed)
				}
			})
		})
	}
}

// Test 5: the contract's round cannot be read. The commit is sent at once
// on the node's own round, as before.
func TestCommitRoundReadError(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := runCommitRound(t, context.Background(), func(int) (uint64, error) {
			return 0, errors.New("connection reset")
		})
		if r.err != nil {
			t.Fatalf("handleCommit: %v", r.err)
		}
		if r.reads != 1 || r.commits != 1 {
			t.Fatalf("%d CurrentRound reads and %d commits, want 1 and 1", r.reads, r.commits)
		}
		if r.elapsed != 0 {
			t.Fatalf("commit sent after %v, want no wait", r.elapsed)
		}
		if r.waits != 0 || r.caughtUp != 0 || r.mismatch != 0 {
			t.Fatalf("commit_round_waits = %v, commit_round_caught_up = %v, round_mismatch = %v, want all 0",
				r.waits, r.caughtUp, r.mismatch)
		}
	})
}

// Test 6: the contract is more than one round away from the node. Nothing
// is committed and the mismatch is counted, as before; the contract is not
// read again.
func TestCommitRoundFarApart(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		contractRound uint64
	}{
		{"contract behind", commitRoundTestRound - 5},
		{"contract ahead", commitRoundTestRound + 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				r := runCommitRound(t, context.Background(), func(int) (uint64, error) {
					return tc.contractRound, nil
				})
				if r.err != nil {
					t.Fatalf("handleCommit: %v", r.err)
				}
				if r.reads != 1 {
					t.Fatalf("%d CurrentRound reads, want 1", r.reads)
				}
				if r.commits != 0 {
					t.Fatalf("%d commits, want 0", r.commits)
				}
				if r.mismatch != 1 {
					t.Fatalf("round_mismatch = %v, want 1", r.mismatch)
				}
				if r.waits != 0 || r.caughtUp != 0 {
					t.Fatalf("commit_round_waits = %v, commit_round_caught_up = %v, want 0 and 0", r.waits, r.caughtUp)
				}
			})
		})
	}
}
