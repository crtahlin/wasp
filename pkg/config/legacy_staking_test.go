// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/config"
)

func TestLegacyStakingDeployments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		chainID int64
		want    int
	}{
		{"gnosis mainnet", config.Mainnet.ChainID, 2},
		{"sepolia testnet", config.Testnet.ChainID, 2},
		{"unknown chain", 999999, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := config.LegacyStakingDeployments(tc.chainID)
			if len(got) != tc.want {
				t.Fatalf("got %d deployments for chain %d, want %d", len(got), tc.chainID, tc.want)
			}
			seen := map[string]bool{}
			for _, d := range got {
				if d.ChainID != tc.chainID {
					t.Errorf("deployment %q has chain %d, want %d", d.ID, d.ChainID, tc.chainID)
				}
				if d.ID == "" {
					t.Error("deployment has an empty ID")
				}
				if seen[d.ID] {
					t.Errorf("duplicate deployment ID %q", d.ID)
				}
				seen[d.ID] = true
				if d.Address == (common.Address{}) {
					t.Errorf("deployment %q has the zero address", d.ID)
				}
				// Every seeded entry is a paused contract recovered by migrate.
				if d.RecoverMethod != config.RecoverByMigrate {
					t.Errorf("deployment %q has recover method %q, want %q", d.ID, d.RecoverMethod, config.RecoverByMigrate)
				}
			}
		})
	}
}
