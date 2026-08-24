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

// newOptionTestCommand mirrors initConfig's viper wiring closely enough that
// the environment prefix and key replacer behave as they do in a real run.
func newOptionTestCommand(t *testing.T, cfgBody string, setFlag bool) *command {
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
	cc.Flags().Bool(optionUseSIMD, false, "")
	if err := v.BindPFlags(cc.Flags()); err != nil {
		t.Fatal(err)
	}
	if setFlag {
		// Deliberately the SAME value as the flag default. An explicit choice
		// must still register as one.
		if err := cc.Flags().Set(optionUseSIMD, "false"); err != nil {
			t.Fatal(err)
		}
	}
	return &command{config: v}
}

// TestSimdOptionDetection pins the distinction the SIMD default rests on:
// whether an operator mentioned use-simd-hashing at all.
//
// Unmentioned means "work it out from the CPU". Mentioned means "do exactly
// what I said, and refuse to start if you cannot" — see buildBeeNode. Getting
// this backwards would either stop every ARM node booting or silently ignore an
// operator who explicitly asked for SIMD, so it is worth a test of its own
// rather than trust in a library's semantics.
//
// viper answers correctly because IsSet looks the key up with flagDefault
// disabled, so a bound flag nobody touched does not count. That is not
// documented prominently and a viper upgrade could change it, which is exactly
// why these four cases are asserted here.
func TestSimdOptionDetection(t *testing.T) {
	t.Run("unmentioned means auto", func(t *testing.T) {
		c := newOptionTestCommand(t, "", false)
		if c.config.IsSet(optionUseSIMD) {
			t.Error("an option nobody set was reported as set; SIMD would follow the " +
				"flag default instead of detecting the CPU, and would never switch on")
		}
	})

	t.Run("command line counts, even at the default value", func(t *testing.T) {
		c := newOptionTestCommand(t, "", true)
		if !c.config.IsSet(optionUseSIMD) {
			t.Error("an explicitly passed flag was not detected; an operator's " +
				"deliberate false would be overridden by CPU detection")
		}
	})

	t.Run("config file counts", func(t *testing.T) {
		c := newOptionTestCommand(t, "use-simd-hashing: true\n", false)
		if !c.config.IsSet(optionUseSIMD) {
			t.Error("a value in the config file was not detected")
		}
	})

	t.Run("environment counts", func(t *testing.T) {
		t.Setenv("BEE_USE_SIMD_HASHING", "true")
		c := newOptionTestCommand(t, "", false)
		if !c.config.IsSet(optionUseSIMD) {
			t.Error("BEE_USE_SIMD_HASHING was not detected; of the three channels " +
				"this is the easiest to break, since the name is derived rather than declared")
		}
	})

	t.Run("an unrelated key does not count", func(t *testing.T) {
		c := newOptionTestCommand(t, "log-verbosity: debug\n", false)
		if c.config.IsSet(optionUseSIMD) {
			t.Error("an unrelated config key made the option look set")
		}
	})
}
