// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/accounting"
	"github.com/ethersphere/bee/v2/pkg/log"
)

type recordingAccrualSetter struct {
	calls int
	mode  accounting.AccrualMode
}

func (r *recordingAccrualSetter) SetAccrualMode(m accounting.AccrualMode) {
	r.calls++
	r.mode = m
}

// The setting has to reach accounting. Parsing and validating it and then not
// applying it is the failure ParseAccrualMode's own doc comment exists to
// prevent, and without this test deleting the call leaves every other test
// green.
func TestApplyAccrualMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		setting string
		want    accounting.AccrualMode
		wantErr bool
	}{
		{"", accounting.AccrualStep, false},
		{"step", accounting.AccrualStep, false},
		{"continuous", accounting.AccrualContinuous, false},
		{"Continuous", accounting.AccrualStep, true},
		{"smooth", accounting.AccrualStep, true},
	} {
		t.Run(tc.setting, func(t *testing.T) {
			t.Parallel()

			acc := &recordingAccrualSetter{}
			err := applyAccrualMode(acc, tc.setting, log.Noop)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("%q: want an error, got none", tc.setting)
				}
				if acc.calls != 0 {
					t.Error("a refused setting must not be applied")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if acc.calls != 1 {
				t.Fatalf("want the mode applied once, applied %d times", acc.calls)
			}
			if acc.mode != tc.want {
				t.Errorf("%q: want mode %v, got %v", tc.setting, tc.want, acc.mode)
			}
		})
	}
}
