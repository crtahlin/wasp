// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// newRPCTestConfig mirrors initConfig's viper wiring, so the environment prefix
// and key replacer behave as they do in a real run.
func newRPCTestConfig(t *testing.T, cfgBody string, flagValues ...string) *viper.Viper {
	t.Helper()

	v := viper.New()
	v.SetEnvPrefix("bee")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))

	if cfgBody != "" {
		p := filepath.Join(t.TempDir(), "bee.yaml")
		if err := os.WriteFile(p, []byte(cfgBody), 0o600); err != nil {
			t.Fatal(err)
		}
		v.SetConfigFile(p)
		if err := v.ReadInConfig(); err != nil {
			t.Fatal(err)
		}
	}

	cc := &cobra.Command{Use: "test"}
	cc.Flags().StringSlice(optionNameBlockchainRpcEndpoint, nil, "")
	for _, fv := range flagValues {
		if err := cc.Flags().Set(optionNameBlockchainRpcEndpoint, fv); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.BindPFlag(configKeyBlockchainRpcEndpoint,
		cc.Flags().Lookup(optionNameBlockchainRpcEndpoint)); err != nil {
		t.Fatal(err)
	}
	v.RegisterAlias(optionNameBlockchainRpcEndpoint, configKeyBlockchainRpcEndpoint)
	return v
}

// TestRPCEndpoints covers the shapes an operator can write. The single-value
// forms matter most: this option was a plain string before #109, and every
// existing configuration uses one. If any of those stopped resolving, nodes
// would fail to reach the chain on upgrade.
func TestRPCEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   string
		flags []string
		want  []string
		why   string
	}{
		{
			name: "unset",
			want: []string{},
			why:  "no endpoints means the chain is disabled, not an endpoint named empty",
		},
		{
			name: "single scalar, the pre-existing form",
			cfg:  "blockchain-rpc:\n  endpoint: https://xdai.example/rpc\n",
			want: []string{"https://xdai.example/rpc"},
			why: "viper splits a scalar on whitespace; a URL contains none, so it must " +
				"survive as one element. Every existing config depends on this",
		},
		{
			name: "yaml list",
			cfg: "blockchain-rpc:\n  endpoint:\n    - https://primary.example\n" +
				"    - https://secondary.example\n",
			want: []string{"https://primary.example", "https://secondary.example"},
		},
		{
			name:  "repeated flag",
			flags: []string{"https://a.example", "https://b.example"},
			want:  []string{"https://a.example", "https://b.example"},
		},
		{
			name:  "comma separated flag",
			flags: []string{"https://a.example,https://b.example"},
			want:  []string{"https://a.example", "https://b.example"},
		},
		{
			name: "blanks are dropped",
			cfg: "blockchain-rpc:\n  endpoint:\n    - https://a.example\n    - \"\"\n" +
				"    - https://b.example\n",
			want: []string{"https://a.example", "https://b.example"},
			why:  "a stray comma or empty list entry must not become an endpoint named empty",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := rpcEndpoints(newRPCTestConfig(t, tc.cfg, tc.flags...))
			if len(got) != len(tc.want) {
				t.Fatalf("got %d endpoints %v, want %d %v. %s", len(got), got, len(tc.want), tc.want, tc.why)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("endpoint %d: got %q, want %q. %s", i, got[i], tc.want[i], tc.why)
				}
			}
		})
	}
}

// TestRPCEndpointsFromEnvironment covers the third channel viper reads.
func TestRPCEndpointsFromEnvironment(t *testing.T) {
	t.Setenv("BEE_BLOCKCHAIN_RPC_ENDPOINT", "https://from-env.example")
	got := rpcEndpoints(newRPCTestConfig(t, ""))
	if len(got) != 1 || got[0] != "https://from-env.example" {
		t.Errorf("got %v, want the endpoint from BEE_BLOCKCHAIN_RPC_ENDPOINT", got)
	}
}
