package peripheral

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/tracing"
)

// TestNoFrozenImports enforces R20-3 for the new tracing package: it must not
// import any frozen package. The guard scans the parent package SOURCE files
// only; this test file's own imports (go/parser, tracing) are exempt.
func TestNoFrozenImports(t *testing.T) {
	forbidden := []string{
		"internal/platform",
		"internal/governance",
		"internal/plugin/runtime",
		"internal/plugin/isolation",
		"internal/controlplane/hostregistry",
	}
	matches, err := filepath.Glob("../*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range matches {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range node.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, forb := range forbidden {
				if strings.Contains(path, forb) {
					t.Errorf("%s imports forbidden frozen package %s (R20-3)", filepath.Base(f), forb)
				}
			}
		}
	}
}

// TestNoExecMethod enforces R20-4 / §2.2: the tracing types expose no
// execution-style method. Refs are advisory; tracing is observation, never action.
func TestNoExecMethod(t *testing.T) {
	banned := []string{"Run", "Exec", "Invoke", "Apply", "Execute", "Command", "Emit", "Dispatch", "Rollback", "Kill", "Schedule"}
	for _, typ := range []reflect.Type{reflect.TypeOf(&tracing.TraceRing{}), reflect.TypeOf(&tracing.Span{})} {
		for _, b := range banned {
			if _, ok := typ.MethodByName(b); ok {
				t.Errorf("%s must not expose execution method %q (R20-4)", typ.String(), b)
			}
		}
	}
}
