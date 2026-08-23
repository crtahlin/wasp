// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd_test

import (
	"strings"
	"testing"

	"github.com/ethersphere/bee/v2/cmd/bee/cmd"
)

// TestStartRefusesSIMDHashing checks that the node refuses to start when
// use-simd-hashing is set.
//
// The SIMD hashing binding corrupts memory and kills the node within minutes
// under load (issue #92). The damage surfaces far from the hasher — in the
// allocator, in LevelDB's cache, in a channel's type flags — so an operator has
// almost no chance of connecting a crash back to this flag. Refusing to start
// is the only point at which the cost of acting on it is low.
//
// This test exists so the refusal cannot be softened back to a warning without
// someone deciding to.
func TestStartRefusesSIMDHashing(t *testing.T) {
	t.Parallel()

	err := newCommand(t, cmd.WithArgs("start", "--use-simd-hashing")).Execute()
	if err == nil {
		t.Fatal("node started with use-simd-hashing; it must refuse")
	}
	if !strings.Contains(err.Error(), "use-simd-hashing is refused") {
		t.Fatalf("wrong error: %v", err)
	}
	// The message has to say why, and where to read more. An operator who hits
	// this needs to know it is deliberate, not a broken build.
	for _, want := range []string{"corrupts memory", "#92", "deliberately"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message missing %q: %v", want, err)
		}
	}
}

// TestStartWithoutSIMDIsNotRefused guards against the check firing on nodes
// that never asked for it.
func TestStartWithoutSIMDIsNotRefused(t *testing.T) {
	t.Parallel()

	err := newCommand(t, cmd.WithArgs("start")).Execute()
	if err != nil && strings.Contains(err.Error(), "use-simd-hashing is refused") {
		t.Fatalf("refused a node that did not enable SIMD: %v", err)
	}
}
