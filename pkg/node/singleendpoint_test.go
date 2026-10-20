// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/node"
)

// TestSingleEndpointWarning (spec test 6, #819): one configured blockchain
// endpoint gets no copies, so the node warns at startup; two do not.
func TestSingleEndpointWarning(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		endpoints int
		warn      bool
	}{{1, true}, {2, false}, {3, false}} {
		var buf bytes.Buffer
		logger := log.NewLogger("test", log.WithSink(&buf), log.WithVerbosity(log.VerbosityWarning)).Build()
		node.WarnSingleEndpoint(logger, tc.endpoints)
		if got := strings.Contains(buf.String(), "only one blockchain rpc endpoint is configured"); got != tc.warn {
			t.Fatalf("%d endpoints: warned %v, want %v (log %q)", tc.endpoints, got, tc.warn, buf.String())
		}
	}
}
