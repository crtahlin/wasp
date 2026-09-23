// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package libp2p_test

import (
	"strings"
	"testing"

	"github.com/ethersphere/bee/v2"
	"github.com/ethersphere/bee/v2/pkg/p2p/libp2p"
)

// TestUserAgentLeadsWithWasp is wasp #474.
//
// The agent is the only thing a crawler reads about a node without asking it
// anything, and swarmscan keys its client distribution on the COMPLETE string.
// So a wasp node already had its own row; what it did not do is read as a
// distinct client, because the row began "bee/" like every other row but one.
//
// The assertions are on the tokens and not on the whole string, because the Go
// version and the architecture are in it and would make an exact-match test
// fail on every toolchain bump for no reason.
func TestUserAgentLeadsWithWasp(t *testing.T) {
	t.Parallel()

	ua := libp2p.UserAgent()
	if ua == "" {
		t.Fatal("the user agent is empty")
	}

	t.Run("leads with the fork's own name", func(t *testing.T) {
		t.Parallel()
		if !strings.HasPrefix(ua, "wasp/") {
			t.Fatalf("user agent %q does not start with wasp/: a reader of a list of agents "+
				"sees the first token, and while it was bee/ a wasp node read as a Bee node", ua)
		}
	})

	t.Run("still carries the upstream base", func(t *testing.T) {
		t.Parallel()
		// Kept deliberately: this is the only place on the wire that says
		// which Bee the build derives from. Moving the wasp token must not
		// become dropping the bee one.
		want := "bee/" + strings.TrimPrefix(bee.UpstreamBase, "v")
		if !strings.Contains(ua, want) {
			t.Fatalf("user agent %q does not contain %q", ua, want)
		}
	})

	t.Run("the two versions are not swapped", func(t *testing.T) {
		t.Parallel()
		// The mutation most likely to survive a prefix-only test is putting
		// the upstream base in the wasp token and the fork version in the bee
		// one, which still starts with wasp/ and still contains bee/.
		fields := strings.Fields(ua)
		if len(fields) < 2 {
			t.Fatalf("user agent %q has fewer than two tokens", ua)
		}
		if got, want := fields[0], "wasp/"+bee.Version; got != want {
			t.Fatalf("first token is %q, want %q", got, want)
		}
		if got, want := fields[1], "bee/"+strings.TrimPrefix(bee.UpstreamBase, "v"); got != want {
			t.Fatalf("second token is %q, want %q", got, want)
		}
	})
}
