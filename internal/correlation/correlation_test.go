package correlation

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fakes — satisfy the Reader interfaces without importing any frozen system.
// ---------------------------------------------------------------------------

type fakeObs struct{ refs []string }

func (f *fakeObs) QueryObservationRefs(_ context.Context, _ Scope) ([]string, error) {
	return f.refs, nil
}

type fakeCluster struct{ refs []string }

func (f *fakeCluster) QueryPlacementRefs(_ context.Context, _ Scope) ([]string, error) {
	return f.refs, nil
}

type fakeEnt struct{ refs []string }

func (f *fakeEnt) QueryPolicyRefs(_ context.Context, _ Scope) ([]string, error) {
	return f.refs, nil
}

type fakeGov struct{ refs []string }

func (f *fakeGov) QueryVerdictRefs(_ context.Context, _ Scope) ([]string, error) {
	return f.refs, nil
}

// ---------------------------------------------------------------------------
// AST guard (MUST-0 / MUST-1) — forbid importing frozen systems.
// ---------------------------------------------------------------------------

func TestASTGuardNoFrozenImports(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse dir: %v", err)
	}
	forbidden := []string{
		"internal/plugin/runtime",
		"internal/plugin/isolation",
		"internal/controlplane/hostregistry",
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, forb := range forbidden {
					if strings.Contains(path, forb) {
						t.Errorf("forbidden import of frozen system: %s", path)
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestNoExecMethod (MUST-3) — the Correlator type must expose no execution method,
// and the package must define no command-method functions. The Phase 10 sign-off
// (R53) additionally forbids Resolve / Replay / Remediate vocabulary.
// ---------------------------------------------------------------------------

func TestNoExecMethod(t *testing.T) {
	forbidden := []string{
		"Run", "Exec", "Execute", "Invoke", "Dispatch", "Apply", "Rollback",
		"Kill", "Schedule", "Command", "Emit", "Mutate", "Write", "Create", "Delete",
		"Resolve", "Replay", "Remediate",
	}
	typ := reflect.TypeOf(Correlator{})
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		for _, f := range forbidden {
			if strings.EqualFold(name, f) || name == f {
				t.Errorf("Correlator exposes forbidden exec method: %s", name)
			}
		}
	}
	scanForExecMethods(t)
}

func scanForExecMethods(t *testing.T) {
	forbidden := []string{
		"Run", "Exec", "Execute", "Invoke", "Dispatch", "Apply", "Rollback",
		"Kill", "Schedule", "Command", "Emit", "Resolve", "Replay", "Remediate",
	}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			for _, f := range forbidden {
				if fn.Name.Name == f {
					t.Errorf("%s: forbidden exec method %s", path, f)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Behavior helpers.
// ---------------------------------------------------------------------------

func newCorrelatorWithFakes() (*Correlator, *fakeObs, *fakeCluster, *fakeEnt, *fakeGov) {
	obs := &fakeObs{refs: []string{"o3", "o1", "o2"}} // intentionally unsorted to test determinism
	cl := &fakeCluster{refs: []string{"h2", "h1"}}
	ent := &fakeEnt{refs: []string{"a2", "a1"}}
	gov := &fakeGov{refs: []string{"p1.r2", "p1.r1"}}
	c := New(Readers{Obs: obs, Cluster: cl, Enterprise: ent, Governance: gov})
	return c, obs, cl, ent, gov
}

// TestCorrelateExecutionScope composes refs for an execution-scoped query (MUST-4/5 projection).
func TestCorrelateExecutionScope(t *testing.T) {
	c, _, _, _, _ := newCorrelatorWithFakes()
	view, err := c.Correlate(context.Background(), Scope{Kind: ScopeExecution, Ref: "e1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.Scope.Kind != ScopeExecution || view.Scope.Ref != "e1" {
		t.Errorf("scope not preserved: %+v", view.Scope)
	}
	if view.ExecutionRef != "e1" {
		t.Errorf("ExecutionRef = %q, want e1", view.ExecutionRef)
	}
	if !reflect.DeepEqual(view.ObservationRefs, []string{"o1", "o2", "o3"}) {
		t.Errorf("ObservationRefs not sorted/deterministic: %+v", view.ObservationRefs)
	}
	if !reflect.DeepEqual(view.PlacementRefs, []string{"h1", "h2"}) {
		t.Errorf("PlacementRefs not sorted: %+v", view.PlacementRefs)
	}
	if !reflect.DeepEqual(view.PolicyRefs, []string{"a1", "a2"}) {
		t.Errorf("PolicyRefs not sorted: %+v", view.PolicyRefs)
	}
	if !reflect.DeepEqual(view.VerdictRefs, []string{"p1.r1", "p1.r2"}) {
		t.Errorf("VerdictRefs not sorted: %+v", view.VerdictRefs)
	}
	if len(view.Meta.SourceRefs) != 9 {
		t.Errorf("SourceRefs len = %d, want 9 (all refs joined)", len(view.Meta.SourceRefs))
	}
	if view.Meta.Reason == "" {
		t.Errorf("explainability Reason empty")
	}
}

// TestCorrelateAllScopeKinds ensures plugin/host/policy scopes are accepted and bounded (SHOULD-5).
func TestCorrelateAllScopeKinds(t *testing.T) {
	c, _, _, _, _ := newCorrelatorWithFakes()
	for _, kind := range []string{ScopePlugin, ScopeHost, ScopePolicy} {
		view, err := c.Correlate(context.Background(), Scope{Kind: kind, Ref: "x1"})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", kind, err)
		}
		if view.Scope.Kind != kind || view.ExecutionRef != "" {
			t.Errorf("%s: scope/ExecutionRef wrong: %+v", kind, view)
		}
		if len(view.Meta.SourceRefs) == 0 {
			t.Errorf("%s: expected joined refs", kind)
		}
	}
}

// TestInvalidScope (SHOULD-5) — missing kind / unknown kind / empty ref are rejected.
func TestInvalidScope(t *testing.T) {
	c, _, _, _, _ := newCorrelatorWithFakes()
	cases := []Scope{
		{Kind: "", Ref: "e1"},
		{Kind: ScopeExecution, Ref: ""},
		{Kind: "everything", Ref: "e1"}, // unbounded "correlate everything" must be rejected
		{Kind: "global", Ref: ""},
	}
	for i, s := range cases {
		if _, err := c.Correlate(context.Background(), s); err != ErrInvalidScope {
			t.Errorf("case %d (%+v): err = %v, want ErrInvalidScope", i, s, err)
		}
	}
}

// TestDeterministicAndLazy (SHOULD-4 determinism + SHOULD-3 lazy): with a fixed clock, 30 runs
// over the same scope produce identical views (inputs sorted, no map drift) and each carries a
// freshly-set CorrelatedAt.
func TestDeterministicAndLazy(t *testing.T) {
	fixed := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	c, _, _, _, _ := newCorrelatorWithFakes()
	c.now = func() time.Time { return fixed }

	first, err := c.Correlate(context.Background(), Scope{Kind: ScopeExecution, Ref: "e1"})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	for i := 0; i < 30; i++ {
		got, err := c.Correlate(context.Background(), Scope{Kind: ScopeExecution, Ref: "e1"})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !viewsEqual(first, got) {
			t.Fatalf("non-deterministic view: run0=%+v run%d=%+v", first, i, got)
		}
		if !got.Meta.CorrelatedAt.Equal(fixed) {
			t.Errorf("lazy CorrelatedAt not set: %v", got.Meta.CorrelatedAt)
		}
	}
}

// TestExplainability (SHOULD-1): every view carries non-empty SourceRefs and a Reason.
func TestExplainability(t *testing.T) {
	c, _, _, _, _ := newCorrelatorWithFakes()
	view, err := c.Correlate(context.Background(), Scope{Kind: ScopePolicy, Ref: "p1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(view.Meta.SourceRefs) == 0 {
		t.Errorf("SourceRefs empty — explainability broken")
	}
	if view.Meta.Reason == "" {
		t.Errorf("Reason empty — explainability broken")
	}
	for _, ref := range view.Meta.SourceRefs {
		if ref == "" {
			t.Errorf("empty ref in SourceRefs")
		}
	}
}

// TestViewVersionStable (SHOULD-2): the read contract is frozen under correlation/view/v1 and is
// distinct from platform/view/v1.
func TestViewVersionStable(t *testing.T) {
	if ViewVersion != "correlation/view/v1" {
		t.Errorf("ViewVersion = %q, want correlation/view/v1", ViewVersion)
	}
	if ViewVersion == "platform/view/v1" {
		t.Errorf("ViewVersion collides with platform/view/v1")
	}
}

// viewsEqual compares two CorrelationViews field-by-field (no map fields, so DeepEqual suffices).
func viewsEqual(a, b CorrelationView) bool {
	return a.Scope == b.Scope &&
		a.ExecutionRef == b.ExecutionRef &&
		reflect.DeepEqual(a.ObservationRefs, b.ObservationRefs) &&
		reflect.DeepEqual(a.VerdictRefs, b.VerdictRefs) &&
		reflect.DeepEqual(a.PlacementRefs, b.PlacementRefs) &&
		reflect.DeepEqual(a.PolicyRefs, b.PolicyRefs) &&
		reflect.DeepEqual(a.Meta.SourceRefs, b.Meta.SourceRefs) &&
		a.Meta.Reason == b.Meta.Reason &&
		a.Meta.CorrelatedAt.Equal(b.Meta.CorrelatedAt)
}
