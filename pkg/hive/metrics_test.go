// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hive

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryMetricIsWritten asserts that no metric is declared without something
// observing it.
//
// A registered metric that nothing writes does not read as missing. It reads as
// zero. `bee_hive_unreachable_peers 0` says "this node has met no unreachable
// peers", which is a confident answer to a question nobody is answering, and
// anyone building an alert on it gets silence and reads it as health.
//
// Five such metrics survived here after upstream removed the reachability ping
// they belonged to. The declarations stayed because nothing connects a metric's
// existence to its use, so this test does. See issue #138.
func TestEveryMetricIsWritten(t *testing.T) {
	t.Parallel()

	declared := metricFields(t, "metrics.go")
	if len(declared) == 0 {
		t.Fatal("no metric fields found; the test is not looking where it thinks it is")
	}

	sources := packageSources(t)

	for _, name := range declared {
		if !strings.Contains(sources, "metrics."+name+".") {
			t.Errorf("metric %s is declared but never written to.\n"+
				"A metric nothing observes reports zero rather than reporting nothing, "+
				"which is worse than having no metric at all. Remove it, or observe it.", name)
		}
	}
}

// metricFields returns the field names of the metrics struct.
func metricFields(t *testing.T, file string) []string {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	var names []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "metrics" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, field := range st.Fields.List {
			for _, id := range field.Names {
				names = append(names, id.Name)
			}
		}
		return false
	})
	return names
}

// packageSources returns every non-test source file in the package, joined.
func packageSources(t *testing.T) string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "metrics.go" {
			// The declarations themselves live here; counting them would make
			// every metric look used.
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
	}
	return b.String()
}
