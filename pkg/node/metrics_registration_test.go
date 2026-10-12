// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// NewBee registers the chain backend's metrics through one type assertion on
// chainBackend (to metrics.Collector). A second assertion that reaches the
// same collectors, as #775 added for *failover.Backend, registers them twice
// and panics at start on a node with several RPC endpoints (#779). A
// source check: a node build needs a chain endpoint and is not run in unit
// tests.
func TestChainBackendMetricsRegisteredOnce(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "node.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name != "NewBee" {
			return false
		}
		ta, ok := n.(*ast.TypeAssertExpr)
		if !ok || ta.Type == nil {
			return true
		}
		if x, ok := ta.X.(*ast.Ident); ok && x.Name == "chainBackend" {
			found = append(found, ta.Pos())
		}
		return true
	})
	if len(found) != 1 {
		for _, p := range found {
			t.Logf("type assertion on chainBackend at %s", fset.Position(p))
		}
		t.Fatalf("%d type assertions on chainBackend in NewBee, want 1 (the metrics.Collector registration)", len(found))
	}
}
