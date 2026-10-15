// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package redistribution_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/storageincentives/redistribution"
)

// revertError is what go-ethereum returns for a reverted eth_call: an
// rpc.DataError carrying the revert data as a hex string.
type revertError struct{ data any }

func (revertError) Error() string    { return "execution reverted" }
func (revertError) ErrorCode() int   { return 3 }
func (e revertError) ErrorData() any { return e.data }

func TestPlayCheckRetryable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"WrongPhase", revertError{"0xe2586bcc"}, true},
		{"WrongPhase wrapped", fmt.Errorf("IsPlaying: owner x depth 9: %w", revertError{"0xe2586bcc"}), true},
		// NotStaked(): keccak256("NotStaked()")[:4].
		{"NotStaked", revertError{"0x039f2e18"}, false},
		{"other selector with arguments", revertError{"0x039f2e18" + "00000000000000000000000000000000000000000000000000000000000000ff"}, false},
		{"revert without data", revertError{"0x"}, true},
		{"revert with non-string data", revertError{42}, true},
		{"plain error", errors.New("connection reset"), true},
		{"canceled", fmt.Errorf("IsPlaying: %w", context.Canceled), false},
		{"deadline", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := redistribution.PlayCheckRetryable(tc.err); got != tc.want {
				t.Fatalf("PlayCheckRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
