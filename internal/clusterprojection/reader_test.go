package clusterprojection

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/cluster"
	"github.com/YuDong999/opscore/internal/correlation"
)

// newJoined builds a manager with two clusters and known members for testing.
func newJoined() *cluster.Manager {
	m := cluster.NewManager()
	_, _ = m.Join("c1", "host-a", []string{"web", "edge"}, map[string]string{"zone": "z1", "tier": "front"})
	_, _ = m.Join("c1", "host-b", []string{"web"}, map[string]string{"zone": "z1"})
	_, _ = m.Join("c2", "host-c", []string{"db"}, map[string]string{"zone": "z2"})
	return m
}

func TestProjection(t *testing.T) {
	r := NewReader(newJoined())
	ctx := context.Background()

	// Groups sorted + stable.
	g, err := r.QueryMemberGroups(ctx, "host-a")
	if err != nil {
		t.Fatalf("groups err: %v", err)
	}
	if len(g) != 2 || g[0] != "edge" || g[1] != "web" {
		t.Fatalf("groups = %v, want [edge web]", g)
	}

	// Labels as k=v, sorted.
	l, err := r.QueryMemberLabels(ctx, "host-a")
	if err != nil {
		t.Fatalf("labels err: %v", err)
	}
	if len(l) != 2 || l[0] != "tier=front" || l[1] != "zone=z1" {
		t.Fatalf("labels = %v, want [tier=front zone=z1]", l)
	}

	// Placement: host-a is in c1 whose active members are host-a, host-b.
	p, err := r.QueryPlacement(ctx, "host-a")
	if err != nil {
		t.Fatalf("placement err: %v", err)
	}
	if p == nil {
		t.Fatal("placement nil for known host")
	}
	if p.Version == "" {
		t.Fatal("placement version empty")
	}
	if len(p.Targets) != 2 || p.Targets[0] != "host-a" || p.Targets[1] != "host-b" {
		t.Fatalf("placement targets = %v, want [host-a host-b]", p.Targets)
	}

	// Determinism: a second call yields identical targets.
	p2, _ := r.QueryPlacement(ctx, "host-a")
	if strings.Join(p2.Targets, ",") != strings.Join(p.Targets, ",") {
		t.Fatalf("non-deterministic targets: %v vs %v", p.Targets, p2.Targets)
	}

	// Unknown host → honest empty (nil).
	if g2, _ := r.QueryMemberGroups(ctx, "nope"); g2 != nil {
		t.Fatalf("expected nil groups for unknown host, got %v", g2)
	}
	if l2, _ := r.QueryMemberLabels(ctx, "nope"); l2 != nil {
		t.Fatalf("expected nil labels for unknown host, got %v", l2)
	}
	if p3, _ := r.QueryPlacement(ctx, "nope"); p3 != nil {
		t.Fatalf("expected nil placement for unknown host, got %v", p3)
	}

	// Correlation refs for host-a scope = active members of c1.
	refs, err := r.QueryPlacementRefs(ctx, correlation.Scope{Kind: correlation.ScopeHost, Ref: "host-a"})
	if err != nil {
		t.Fatalf("refs err: %v", err)
	}
	if len(refs) != 2 || refs[0] != "host-a" || refs[1] != "host-b" {
		t.Fatalf("refs = %v, want [host-a host-b]", refs)
	}
	// Non-host scope → honest empty.
	if refs2, _ := r.QueryPlacementRefs(ctx, correlation.Scope{Kind: correlation.ScopePolicy, Ref: "p1"}); refs2 != nil {
		t.Fatalf("expected nil refs for policy scope, got %v", refs2)
	}
}

// TestNoExecMethod enforces MUST-A4 / MUST-13.4: the projection exposes only
// query methods — no Run/Exec/Apply/Schedule/Dispatch or other execution verbs.
// It parses every non-test .go file in the package and fails on any receiver
// method whose name begins with a forbidden verb.
func TestNoExecMethod(t *testing.T) {
	forbidden := []string{
		"Run", "Exec", "Execute", "Command", "Apply", "Schedule", "Dispatch",
		"Invoke", "Emit", "Rollback", "Kill", "Mutate", "Resolve", "Replay",
		"Remediate", "Delete", "Create", "Update", "Write", "Store", "Persist",
		"Start", "Stop", "Open", "Connect",
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, decl := range src.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue // only methods with a receiver are in scope
			}
			name := fn.Name.Name
			for _, verb := range forbidden {
				if strings.HasPrefix(name, verb) {
					t.Errorf("%s: forbidden execution verb method %q", filepath.Base(f), name)
				}
			}
		}
	}
}
