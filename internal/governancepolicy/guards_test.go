package governancepolicy

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/correlation"
	"github.com/YuDong999/opscore/internal/governance"
	"github.com/YuDong999/opscore/internal/platformview"
)

// scanForbiddenMethods parses every non-test .go file in the package and fails
// on any RECEIVER method whose name begins with a forbidden (execution) verb.
// Persistence / lifecycle verbs (Save/Get/List/Archive/Activate/Deactivate/
// NextRevision/Create) are intentionally NOT in the forbidden set — they are the
// legal repository surface (B6/B7), distinct from an execution bridge (B9).
func scanForbiddenMethods(t *testing.T, forbidden []string) {
	t.Helper()
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
				continue // only receiver methods are in scope
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

// TestNoExecMethod enforces B9 (no execution bridge): this package may persist
// and read policies, but must never expose methods that execute/apply/schedule/
// dispatch/etc. Persistence/lifecycle verbs are excluded as LEGAL.
func TestNoExecMethod(t *testing.T) {
	forbidden := []string{
		"Run", "Exec", "Execute", "Command", "Apply", "Schedule", "Dispatch",
		"Invoke", "Emit", "Rollback", "Kill", "Mutate", "Resolve", "Replay",
		"Remediate", "Start", "Stop", "Connect", "Open",
	}
	scanForbiddenMethods(t, forbidden)
}

// TestNoEngineEval enforces B7 (Lifecycle separated from Evaluation): this
// package must never construct or call the governance Engine. We scan the
// non-test source for the engine construction/call signatures.
func TestNoEngineEval(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		s := string(data)
		// Only real CALLS trip the guard — a description like "Engine.Evaluate" in a
		// comment (without the invocation parenthesis) is harmless.
		if strings.Contains(s, "NewEngine(") {
			t.Errorf("%s: must not construct governance.Engine (B7)", filepath.Base(f))
		}
		if strings.Contains(s, ".Evaluate(") {
			t.Errorf("%s: must not call Engine.Evaluate (B7)", filepath.Base(f))
		}
	}
}

func newTestRepo(t *testing.T) Repository {
	t.Helper()
	repo, err := NewFileRepository(t.TempDir())
	if err != nil {
		t.Fatalf("new repo: %v", err)
	}
	return repo
}

// TestRepositoryContract exercises the full persistence/lifecycle surface
// (Save/Get/List/Activate/Archive, revision bump, stable order, not-found).
func TestRepositoryContract(t *testing.T) {
	repo := newTestRepo(t)

	rules := []governance.Rule{
		{RuleID: "r1", Priority: 10, Kind: governance.RuleGroupAllow, Param: "g1"},
		{RuleID: "r2", Priority: 5, Kind: governance.RuleChangeFreeze},
	}
	rec, err := Create(repo, "pol-1", rules)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.Revision != 1 || rec.Status != StatusDraft {
		t.Fatalf("create rec = %+v", rec)
	}

	rec, err = Activate(repo, "pol-1")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if rec.Status != StatusActive {
		t.Fatalf("status = %s", rec.Status)
	}

	got, ok, err := repo.Get("pol-1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if len(got.Rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(got.Rules))
	}

	if _, err := Create(repo, "pol-2", nil); err != nil {
		t.Fatalf("create pol-2: %v", err)
	}
	all, err := repo.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 || all[0].PolicyID != "pol-1" || all[1].PolicyID != "pol-2" {
		t.Fatalf("list order = [%s %s]", all[0].PolicyID, all[1].PolicyID)
	}

	if _, err := Archive(repo, "pol-1"); err != nil {
		t.Fatalf("archive: %v", err)
	}
	got, _, _ = repo.Get("pol-1")
	if got.Status != StatusArchived {
		t.Fatalf("archived status = %s", got.Status)
	}

	if _, ok, _ := repo.Get("nope"); ok {
		t.Fatal("expected not-found for unknown policy")
	}
}

// TestReaderDeterministic verifies the read projection: stable rule ordering,
// honest-empty for unknown policy, always-nil verdict, and scope-bounded refs.
func TestReaderDeterministic(t *testing.T) {
	repo := newTestRepo(t)
	if _, err := Create(repo, "pol-1", []governance.Rule{
		{RuleID: "b", Priority: 5, Kind: governance.RuleGroupAllow, Param: "g"},
		{RuleID: "a", Priority: 10, Kind: governance.RuleChangeFreeze},
		{RuleID: "c", Priority: 10, Kind: governance.RuleRequireApproval},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	r := NewReader(repo)
	ctx := context.Background()

	views, err := r.QueryRules(ctx, "pol-1")
	if err != nil {
		t.Fatalf("rules: %v", err)
	}
	// priority desc, then RuleID asc: a(10) c(10) b(5).
	want := []string{"a", "c", "b"}
	if len(views) != len(want) {
		t.Fatalf("views = %d, want %d", len(views), len(want))
	}
	for i, v := range views {
		if v.RuleID != want[i] {
			t.Fatalf("views[%d].RuleID = %s, want %s", i, v.RuleID, want[i])
		}
		if v.Meta.SourceCapability != "governance" {
			t.Fatalf("view %s source = %s", v.RuleID, v.Meta.SourceCapability)
		}
	}

	if v2, err2 := r.QueryRules(ctx, "nope"); v2 != nil || !errors.Is(err2, platformview.ErrPolicyNotFound) {
		t.Fatalf("unknown policy: rules=%v err=%v, want nil rules + platformview.ErrPolicyNotFound", v2, err2)
	}
	if v, _ := r.QueryVerdict(ctx, "pol-1"); v != nil {
		t.Fatalf("expected nil verdict (no state), got %v", v)
	}

	refs, err := r.QueryVerdictRefs(ctx, correlation.Scope{Kind: correlation.ScopePolicy, Ref: "pol-1"})
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	if !sort.StringsAreSorted(refs) || len(refs) != 1 || refs[0] != "pol-1" {
		t.Fatalf("policy-scoped refs = %v", refs)
	}

	if _, err := Create(repo, "pol-2", nil); err != nil {
		t.Fatalf("create pol-2: %v", err)
	}
	refs2, _ := r.QueryVerdictRefs(ctx, correlation.Scope{Kind: correlation.ScopePolicy, Ref: "pol-2"})
	if len(refs2) != 1 || refs2[0] != "pol-2" {
		t.Fatalf("policy-scoped filter refs2 = %v", refs2)
	}
	refs3, _ := r.QueryVerdictRefs(ctx, correlation.Scope{Kind: "all", Ref: ""})
	if len(refs3) != 2 {
		t.Fatalf("all-scope refs3 = %v", refs3)
	}
}
