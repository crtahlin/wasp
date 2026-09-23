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

// TestUserAgentStringTrimsTheUpstreamV pins the formatting against literals,
// with an upstream base that actually carries a leading "v".
//
// The test above cannot do this. bee.UpstreamBase is "unknown" in a test
// binary, which has no "v" to trim, and the Makefile's test targets do not
// pass LDFLAGS, so no test that reads the globals exercises the trim. Review
// found exactly that: deleting strings.TrimPrefix survived the whole suite
// while shipping "bee/v2.8.2" to every peer, where a crawler expects
// "bee/2.8.2".
//
// Asserting the whole string against a literal also pins the order, the
// separators and the token count in one place, which the global-reading test
// deliberately does not do so a toolchain bump cannot break it.
func TestUserAgentStringTrimsTheUpstreamV(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		waspVersion  string
		upstreamBase string
		want         string
	}{
		{
			name:         "the upstream base carries a v, as a real build does",
			waspVersion:  "0.1.4-6484a665",
			upstreamBase: "v2.8.2",
			want:         "wasp/0.1.4-6484a665 bee/2.8.2 go1.26.4 linux/amd64",
		},
		{
			name:         "the upstream base carries no v, as a test binary has",
			waspVersion:  "-dev",
			upstreamBase: "unknown",
			want:         "wasp/-dev bee/unknown go1.26.4 linux/amd64",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := libp2p.UserAgentString(tc.waspVersion, tc.upstreamBase, "go1.26.4", "linux", "amd64")
			if got != tc.want {
				t.Fatalf("user agent %q, want %q", got, tc.want)
			}
		})
	}
}
