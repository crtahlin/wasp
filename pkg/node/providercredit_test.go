// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node

import (
	"math/big"
	"testing"
)

// Every refusal here is a configuration an operator would otherwise have to
// discover from the feature quietly doing nothing.
func TestProviderCreditSettings(t *testing.T) {
	t.Parallel()

	paymentThreshold := big.NewInt(13_500_000)

	for _, tc := range []struct {
		name      string
		threshold string
		budget    string
		wantOn    bool
		wantErr   bool
	}{
		{
			name:      "off by default",
			threshold: "0",
			budget:    "0",
		},
		{
			name:      "valid",
			threshold: "54000000",
			budget:    "40500000",
			wantOn:    true,
		},
		{
			name:      "budget set without a threshold grants nothing",
			threshold: "0",
			budget:    "40500000",
			wantErr:   true,
		},
		{
			name:      "below the node-wide threshold would lower, not raise",
			threshold: "10000000",
			budget:    "40500000",
			wantErr:   true,
		},
		{
			name:      "above the maximum generally accepted value",
			threshold: "108000001",
			budget:    "999000000",
			wantErr:   true,
		},
		{
			name:      "at the maximum is allowed",
			threshold: "108000000",
			budget:    "94500000",
			wantOn:    true,
		},
		{
			name:      "a budget below one grant admits nobody",
			threshold: "54000000",
			budget:    "40499999",
			wantErr:   true,
		},
		{
			// Equal means every delta is zero, so the feature reads as
			// configured and grants nothing.
			name:      "equal to the node-wide threshold grants nothing",
			threshold: "13500000",
			budget:    "13500000",
			wantErr:   true,
		},
		{
			name:      "not a number",
			threshold: "fifty four million",
			budget:    "40500000",
			wantErr:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			o := &Options{
				ProvidersPaymentThreshold: tc.threshold,
				ProvidersCreditBudget:     tc.budget,
			}
			threshold, budget, err := providerCreditSettings(o, paymentThreshold)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("threshold %q budget %q was accepted, want a refusal", tc.threshold, tc.budget)
				}
				return
			}
			if err != nil {
				t.Fatalf("threshold %q budget %q was refused: %v", tc.threshold, tc.budget, err)
			}
			if on := threshold.Sign() > 0; on != tc.wantOn {
				t.Fatalf("feature on = %v, want %v", on, tc.wantOn)
			}
			if !tc.wantOn {
				return
			}
			if budget.Sign() <= 0 {
				t.Fatal("the feature is on with a zero budget, so no peer could be admitted")
			}
		})
	}
}
