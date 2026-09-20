// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package accounting_test

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/accounting"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// The shipped defaults, deliberately rather than this package's other test
// values, so every number below is one an operator would actually see.
var (
	accrualThreshold   = big.NewInt(13_500_000)
	accrualRefreshRate = big.NewInt(4_500_000)
)

// The shipped payment-tolerance-percent.
const accrualTolerance = int64(25)

// newAccrualAccounting builds an Accounting with the shipped defaults and the
// given accrual mode and payment tolerance.
func newAccrualAccounting(t *testing.T, mode accounting.AccrualMode, tolerance int64) *accounting.Accounting {
	t.Helper()

	acc, err := accounting.NewAccounting(
		accrualThreshold,
		tolerance,
		10, // early payment, unused here
		log.Noop,
		mock.NewStateStore(),
		nil,
		accrualRefreshRate,
		10,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	acc.SetAccrualMode(mode)
	return acc
}

func wantDue(t *testing.T, got *big.Int, want int64, at int64) {
	t.Helper()
	if got.Cmp(big.NewInt(want)) != 0 {
		t.Errorf("at %d ms: want refresh due %d, got %s", at, want, got)
	}
}

// The step mode must reproduce today's two values exactly: nothing for the
// whole first second, then a full refreshRate. This is the control, and it is
// what a node that does not set the option gets.
func TestRefreshDueStepUnchanged(t *testing.T) {
	t.Parallel()

	acc := newAccrualAccounting(t, accounting.AccrualStep, accrualTolerance)

	for _, tc := range []struct {
		elapsed int64
		want    int64
	}{
		{0, 0},
		{1, 0},
		{500, 0},
		{999, 0},
		{1000, 4_500_000},
		{60_000, 4_500_000},
	} {
		wantDue(t, acc.RefreshDueForTest(accrualThreshold, 0, tc.elapsed), tc.want, tc.elapsed)
	}
}

// The continuous mode accrues inside the first second in proportion to the
// milliseconds elapsed, until the cap binds.
func TestRefreshDueContinuousAccrual(t *testing.T) {
	t.Parallel()

	acc := newAccrualAccounting(t, accounting.AccrualContinuous, accrualTolerance)

	// 4,500,000 per second, so 4,500 per millisecond. The cap at the shipped
	// defaults is 25% of 13,500,000 less one, 3,374,999, which binds from
	// 750 ms where the uncapped value would be 3,375,000.
	for _, tc := range []struct {
		elapsed int64
		want    int64
	}{
		{0, 0},
		{1, 4_500},
		{100, 450_000},
		{500, 2_250_000},
		{749, 3_370_500},
		{750, 3_374_999}, // capped: uncapped would be 3,375,000
		{999, 3_374_999}, // still capped
	} {
		wantDue(t, acc.RefreshDueForTest(accrualThreshold, 0, tc.elapsed), tc.want, tc.elapsed)
	}
}

// The cap binds strictly below the limit at which a stock peer disconnects,
// and it moves with the threshold the peer announced rather than being a
// constant.
func TestRefreshDueCapBindsBelowTolerance(t *testing.T) {
	t.Parallel()

	acc := newAccrualAccounting(t, accounting.AccrualContinuous, accrualTolerance)

	// The crossover moves with the threshold: the cap is 25 per cent of it,
	// and the allowance accrues at 4,500 per millisecond regardless, so a
	// smaller threshold reaches its cap sooner. The spec asks for both.
	for _, tc := range []struct {
		threshold int64
		// the first millisecond at which the cap binds
		bindsAt int64
	}{
		{13_500_000, 750}, // cap 3,374,999; uncapped at 750 ms is 3,375,000
		{9_000_000, 500},  // cap 2,249,999; uncapped at 500 ms is 2,250,000
	} {
		th := big.NewInt(tc.threshold)

		below := acc.RefreshDueForTest(th, 0, tc.bindsAt-1)
		if want := big.NewInt((tc.bindsAt - 1) * 4_500); below.Cmp(want) != 0 {
			t.Errorf("threshold %d at %d ms: want the uncapped %s, got %s",
				tc.threshold, tc.bindsAt-1, want, below)
		}
		at := acc.RefreshDueForTest(th, 0, tc.bindsAt)
		if want := acc.SafeAccrualCapForTest(th); at.Cmp(want) != 0 {
			t.Errorf("threshold %d at %d ms: want the cap %s, got %s",
				tc.threshold, tc.bindsAt, want, at)
		}
	}

	for _, threshold := range []int64{13_500_000, 9_000_000} {
		th := big.NewInt(threshold)

		// A stock peer disconnects at nextBalance >= threshold * (100+tol)%,
		// so the limit this node works to must be strictly below it.
		disconnectAt := new(big.Int).Div(
			new(big.Int).Mul(th, big.NewInt(100+accrualTolerance)),
			big.NewInt(100),
		)

		// Late in the window the cap is certainly binding.
		due := acc.RefreshDueForTest(th, 0, 999)
		limit := new(big.Int).Add(th, due)

		if limit.Cmp(disconnectAt) >= 0 {
			t.Errorf("threshold %d: limit %s reaches the peer's disconnect limit %s",
				threshold, limit, disconnectAt)
		}
		if want := new(big.Int).Sub(disconnectAt, big.NewInt(1)); limit.Cmp(want) != 0 {
			t.Errorf("threshold %d: want limit %s, got %s", threshold, want, limit)
		}
	}
}

// payment-tolerance-percent: 0 is legal, and pkg/node rejects only negative
// values. With it there is no headroom, and the naive form would put the limit
// one unit BELOW the announced threshold for the whole sub-second window.
// That is the only input on which this change could be worse than doing
// nothing.
func TestRefreshDueZeroToleranceNeverLowersTheLimit(t *testing.T) {
	t.Parallel()

	acc := newAccrualAccounting(t, accounting.AccrualContinuous, 0)

	for _, elapsed := range []int64{0, 1, 500, 999} {
		due := acc.RefreshDueForTest(accrualThreshold, 0, elapsed)
		if due.Sign() < 0 {
			t.Errorf("at %d ms: negative allowance %s", elapsed, due)
		}
		if due.Sign() != 0 {
			t.Errorf("at %d ms: want no allowance with zero tolerance, got %s", elapsed, due)
		}
	}

	// And at a full second the ordinary allowance still arrives.
	wantDue(t, acc.RefreshDueForTest(accrualThreshold, 0, 1000), 4_500_000, 1000)
}

// The cap must not apply at or after one second. It would otherwise hold the
// limit below what the peer already tolerates forever, which is strictly worse
// than today. A failed refreshment leaves refreshTimestampMilliseconds at
// zero, and gate-terms-measured.md records real peers in that state.
func TestRefreshDueCapDoesNotBindAtOrAfterOneSecond(t *testing.T) {
	t.Parallel()

	acc := newAccrualAccounting(t, accounting.AccrualContinuous, accrualTolerance)

	wantDue(t, acc.RefreshDueForTest(accrualThreshold, 0, 1000), 4_500_000, 1000)

	// A zero refresh timestamp with a real clock: an enormous elapsed time,
	// which must take the uncapped branch and reach the full allowance.
	const someRealTimeMillis = 1_700_000_000_000
	wantDue(t, acc.RefreshDueForTest(accrualThreshold, 0, someRealTimeMillis), 4_500_000, someRealTimeMillis)
}

// At a threshold grown past 18,000,000 the cap is larger than a full
// refreshRate, so it can never bind and the continuous mode is a pure
// smoothing of the same total.
func TestRefreshDueCapAbsentAboveEighteenMillion(t *testing.T) {
	t.Parallel()

	acc := newAccrualAccounting(t, accounting.AccrualContinuous, accrualTolerance)

	// 25% of 20,000,000 is 5,000,000, above a full refreshRate of 4,500,000.
	grown := big.NewInt(20_000_000)
	if maxAccrual := acc.SafeAccrualCapForTest(grown); maxAccrual.Cmp(accrualRefreshRate) <= 0 {
		t.Fatalf("expected the cap %s to exceed a full refresh rate %s", maxAccrual, accrualRefreshRate)
	}

	// So at 999 ms the value is the uncapped proportional one.
	wantDue(t, acc.RefreshDueForTest(grown, 0, 999), 4_495_500, 999)
}

// A clock stepping backwards must never produce a negative allowance, which
// would put the overdraft limit BELOW the threshold the peer announced.
//
// How far back it takes to matter differs by mode, and an earlier version of
// this test and its comment got that wrong in both directions.
//
//   - step: only a full second or more. Integer division truncates toward
//     zero, so a smaller step already yields zero. A test at 1 ms would pass
//     against the unclamped code and pin nothing.
//   - continuous: one millisecond is enough, because that path multiplies by
//     the elapsed milliseconds before dividing, so any negative elapsed value
//     produces a negative allowance directly. Measured: 1 ms back gives
//     -4,500 without the clamp.
//
// Small backwards corrections are ordinary and full-second steps are not, so
// the continuous case is the one that is likely to be reached.
func TestRefreshDueBackwardsClockClamped(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		mode accounting.AccrualMode
		// how far the clock stepped back, in milliseconds
		back int64
	}{
		{"step needs a full second", accounting.AccrualStep, 1_500},
		{"continuous needs only a millisecond", accounting.AccrualContinuous, 1},
		{"continuous under a second", accounting.AccrualContinuous, 999},
		{"continuous over a second", accounting.AccrualContinuous, 1_500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			acc := newAccrualAccounting(t, tc.mode, accrualTolerance)

			const refreshedAt = 10_000
			due := acc.RefreshDueForTest(accrualThreshold, refreshedAt, refreshedAt-tc.back)
			if due.Sign() < 0 {
				t.Errorf("%d ms back: negative allowance %s would put the limit below the threshold",
					tc.back, due)
			}
			if due.Sign() != 0 {
				t.Errorf("%d ms back: want no allowance for a backwards clock, got %s", tc.back, due)
			}
		})
	}
}

// The gate admits a credit inside the first second that the step mode refuses.
//
// This is the test the change exists for, and every other test in this file
// reaches refreshDue through a helper or through the reporting path. Without
// it, reverting the one line in PrepareCredit to upstream's expression makes
// `continuous` a no-op at the gate and the whole package still passes. A
// review found exactly that, after a commit message claiming every mutation
// was caught.
func TestPrepareCreditAdmitsInsideTheFirstSecond(t *testing.T) {
	t.Parallel()

	const nowMillis = 1_700_000_000_000

	// A debt above the announced threshold of 13,500,000 and below the
	// sub-second continuous limit. At 500 ms the allowance is 2,250,000, so
	// the limit is 15,750,000. Step grants nothing before one second, so its
	// limit is still 13,500,000.
	price := uint64(14_000_000)

	for _, tc := range []struct {
		name       string
		mode       accounting.AccrualMode
		wantRefuse bool
	}{
		{"step refuses", accounting.AccrualStep, true},
		{"continuous admits", accounting.AccrualContinuous, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			peer := swarm.MustParseHexAddress("00112233")
			acc := newAccrualAccounting(t, tc.mode, accrualTolerance)
			acc.SetTimeNow(func() time.Time { return time.UnixMilli(nowMillis) })
			acc.Connect(peer, true)
			if err := acc.NotifyPaymentThreshold(peer, accrualThreshold); err != nil {
				t.Fatal(err)
			}
			acc.SetRefreshTimestampForTest(peer, nowMillis-500)

			_, err := acc.PrepareCredit(t.Context(), peer, price, true)
			if tc.wantRefuse {
				if !errors.Is(err, accounting.ErrOverdraft) {
					t.Fatalf("want the step mode to refuse %d, got %v", price, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("want the continuous mode to admit %d, got %v", price, err)
			}
		})
	}
}

// The /accounting endpoint must report the limit the gate actually enforces.
//
// CurrentThresholdReceived is computed in PeerAccounting from the same
// quantity the overdraft gate uses, and before this change it was a second
// copy of the expression. A node that accrues continuously while reporting the
// step model publishes a limit that disagrees with the one it works to, by up
// to the whole capped allowance, for the whole window this change is about.
// The two computations are rule 11's own example of a defect, which is why
// #316 already exists for the third copy in settle.
func TestPeerAccountingReportsTheEnforcedLimit(t *testing.T) {
	t.Parallel()

	peer := swarm.MustParseHexAddress("00112233")

	for _, mode := range []struct {
		name string
		mode accounting.AccrualMode
	}{
		{"step", accounting.AccrualStep},
		{"continuous", accounting.AccrualContinuous},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()

			acc := newAccrualAccounting(t, mode.mode, accrualTolerance)
			acc.Connect(peer, true)
			if err := acc.NotifyPaymentThreshold(peer, accrualThreshold); err != nil {
				t.Fatal(err)
			}

			// A peer refreshed 500 ms ago: inside the window, where the two
			// models disagree.
			const nowMillis = 1_700_000_000_000
			acc.SetTimeNow(func() time.Time { return time.UnixMilli(nowMillis) })
			acc.SetRefreshTimestampForTest(peer, nowMillis-500)

			infos, err := acc.PeerAccounting()
			if err != nil {
				t.Fatal(err)
			}
			info, ok := infos[peer.String()]
			if !ok {
				t.Fatalf("peer missing from %v", infos)
			}

			want := new(big.Int).Add(
				accrualThreshold,
				acc.RefreshDueForTest(accrualThreshold, nowMillis-500, nowMillis),
			)
			if info.CurrentThresholdReceived.Cmp(want) != 0 {
				t.Errorf("reported limit %s, gate enforces %s",
					info.CurrentThresholdReceived, want)
			}
		})
	}
}

// An unknown setting is refused rather than silently behaving as the default,
// because a misspelled option that quietly does nothing is the failure this
// refuses.
func TestParseAccrualMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in      string
		want    accounting.AccrualMode
		wantErr bool
	}{
		{"", accounting.AccrualStep, false},
		{"step", accounting.AccrualStep, false},
		{"continuous", accounting.AccrualContinuous, false},
		{"Continuous", accounting.AccrualStep, true},
		{"smooth", accounting.AccrualStep, true},
	} {
		got, err := accounting.ParseAccrualMode(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("%q: want an error, got none", tc.in)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("%q: want mode %v, got %v", tc.in, tc.want, got)
		}
	}
}
