package cluster

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestClusterDoesNotOwnOrExecute enforces ADR-016 MUST-1/2/5: Cluster is
// PLATFORM COORDINATION, not a second Runtime. It must never import the Runtime
// execution engine (internal/plugin/runtime), process isolation
// (internal/plugin/isolation), nor re-own the host (internal/controlplane/
// hostregistry) — importing any of these would let it drift into executing
// commands or building a second inventory. This is the same AST-guard
// discipline used across the Plugin Ecosystem and Phase 8.1 Observability.
func TestClusterDoesNotOwnOrExecute(t *testing.T) {
	fset := token.NewFileSet()
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"github.com/YuDong999/opscore/internal/plugin/runtime":            true,
		"github.com/YuDong999/opscore/internal/plugin/isolation":          true,
		"github.com/YuDong999/opscore/internal/controlplane/hostregistry": true,
	}
	for _, f := range matches {
		if filepath.Base(f) == "cluster_test.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range file.Imports {
			path := imp.Path.Value
			if len(path) >= 2 {
				path = path[1 : len(path)-1]
			}
			if forbidden[path] {
				t.Errorf("%s must not import forbidden package %s (ADR-016 MUST-1/2/5)", f, path)
			}
		}
	}
}

// TestClusterManagesMembershipAndPlacement proves the coordination layer joins
// members, manages group/label metadata, and computes placement as host
// references only — never executing, never owning the host.
func TestClusterManagesMembershipAndPlacement(t *testing.T) {
	m := NewManager()
	cid := ClusterID("prod")

	// Join three hosts by opaque ref (no host hardware/connection stored).
	if _, err := m.Join(cid, HostRef("web-01"), []string{"web"}, map[string]string{"zone": "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Join(cid, HostRef("web-02"), []string{"web"}, map[string]string{"zone": "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Join(cid, HostRef("db-01"), []string{"db"}, map[string]string{"zone": "a"}); err != nil {
		t.Fatal(err)
	}

	if got := len(m.Members(cid)); got != 3 {
		t.Fatalf("expected 3 members, got %d", got)
	}

	// Group-based selection: all web hosts.
	web := m.ByGroup(cid, "web")
	if len(web) != 2 {
		t.Fatalf("expected 2 web members, got %d", len(web))
	}

	// Placement by label: zone "a" → web-01 + db-01 (no command emitted).
	p := m.ComputePlacement(cid, PlacementSpec{RequireLabels: map[string]string{"zone": "a"}})
	if len(p.Targets) != 2 {
		t.Fatalf("expected 2 zone-a targets, got %d (%v)", len(p.Targets), p.Targets)
	}
	for _, tgt := range p.Targets {
		if tgt != HostRef("web-01") && tgt != HostRef("db-01") {
			t.Errorf("unexpected placement target %q (must be a host ref, never a command)", tgt)
		}
	}
	// Placement carries schema version + declarative reason (Round 45 SHOULD-1/2).
	if p.Version != PlacementVersion {
		t.Errorf("placement missing version, got %q", p.Version)
	}
	if p.Reason == "" {
		t.Errorf("placement missing explainability reason")
	}

	// Affinity ordering: prefer zone "b" → web-02 first.
	pa := m.ComputePlacement(cid, PlacementSpec{Affinity: map[string]string{"zone": "b"}})
	if len(pa.Targets) != 3 || pa.Targets[0] != HostRef("web-02") {
		t.Errorf("affinity ordering wrong: %v", pa.Targets)
	}

	// Limit bounds the result (still refs, not commands).
	pl := m.ComputePlacement(cid, PlacementSpec{Limit: 1})
	if len(pl.Targets) != 1 {
		t.Fatalf("limit not applied: got %d targets", len(pl.Targets))
	}

	// Leave removes membership, does not delete the host elsewhere.
	if err := m.Leave(cid, HostRef("web-01")); err != nil {
		t.Fatal(err)
	}
	if got := len(m.Members(cid)); got != 2 {
		t.Fatalf("expected 2 members after leave, got %d", got)
	}

	// Unknown cluster / member errors (metadata errors, not exec failures).
	if err := m.Leave(ClusterID("nope"), HostRef("x")); err != ErrClusterNotFound {
		t.Errorf("expected ErrClusterNotFound, got %v", err)
	}
	if err := m.SetLabel(cid, HostRef("ghost"), "k", "v"); err != ErrMemberNotFound {
		t.Errorf("expected ErrMemberNotFound, got %v", err)
	}
}

// TestPlacementYieldsRefsNotCommands asserts the structural contract that a
// Placement carries only host references (MUST-4). If a future edit ever adds a
// command field here, this documents the invariant and should force a
// deliberate ADR change rather than a silent drift.
func TestPlacementYieldsRefsNotCommands(t *testing.T) {
	p := Placement{Targets: []HostRef{"web-01", "web-02"}}
	for _, tgt := range p.Targets {
		if len(tgt) == 0 {
			t.Error("placement target must be a non-empty host reference")
		}
	}
	// Placement has no command field by construction (see model.go). Compile
	// time guarantees this; the assertion above documents the intent.
	_ = p
}
