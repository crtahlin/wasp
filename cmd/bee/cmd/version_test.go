// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd_test

import (
	"bytes"
	"testing"

	"github.com/ethersphere/bee/v2"
	"github.com/ethersphere/bee/v2/cmd/bee/cmd"
)

func TestVersionCmd(t *testing.T) {
	t.Parallel()

	var outputBuf bytes.Buffer
	if err := newCommand(t,
		cmd.WithArgs("version"),
		cmd.WithOutput(&outputBuf),
	).Execute(); err != nil {
		t.Fatal(err)
	}

	// This fork reports its own version line alongside the upstream release it
	// derives from, so that an operator can always tell which upstream code base
	// is actually running. See docs/agent-playbooks/release-process.md.
	want := bee.Version + " (upstream bee " + bee.UpstreamBase + ")\n"
	got := outputBuf.String()
	if got != want {
		t.Errorf("got output %q, want %q", got, want)
	}
}

func TestVersionCmdReportsUpstreamBase(t *testing.T) {
	t.Parallel()

	// UpstreamBase is injected by the Makefile from .upstream-base. In an
	// uninstrumented `go test` run it is empty, and must degrade to "unknown"
	// rather than rendering an empty parenthesis that reads like a real answer.
	if bee.UpstreamBase == "" {
		t.Fatal("UpstreamBase must never be empty; it should fall back to \"unknown\"")
	}
}
