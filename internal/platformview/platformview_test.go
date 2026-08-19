package platformview

import (
	"context"
	"errors"
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

type fakeObs struct {
	obs []ObservationView
}

func (f *fakeObs) QueryObservations(_ context.Context, _ string) ([]ObservationView, error) {
	return f.obs, nil
}

type fakeCluster struct {
	groups    []string
	labels    []string
	placement *PlacementView
}

func (f *fakeCluster) QueryPlacement(_ context.Context, _ string) (*PlacementView, error) {
	return f.placement, nil
}
func (f *fakeCluster) QueryMemberGroups(_ context.Context, _ string) ([]string, error) {
	return f.groups, nil
}
func (f *fakeCluster) QueryMemberLabels(_ context.Context, _ string) ([]string, error) {
	return f.labels, nil
}

type fakeEnt struct {
	atts []AttachmentView
}

func (f *fakeEnt) QueryAttachments(_ context.Context, _ string) ([]AttachmentView, error) {
	return f.atts, nil
}

type fakeGov struct {
	verdict *VerdictView
	rules   []RuleView
}

func (f *fakeGov) QueryVerdict(_ context.Context, _ string) (*VerdictView, error) {
	return f.verdict, nil
}
func (f *fakeGov) QueryRules(_ context.Context, _ string) ([]RuleView, error) {
	return f.rules, nil
}

// fakeGovNotFound mimics governancepolicy.Reader for an unknown policy id: QueryRules reports the
// policy does not exist (R79-A).
type fakeGovNotFound struct{}

func (fakeGovNotFound) QueryVerdict(_ context.Context, _ string) (*VerdictView, error) {
	return nil, nil
}
func (fakeGovNotFound) QueryRules(_ context.Context, _ string) ([]RuleView, error) {
	return nil, ErrPolicyNotFound
}

// ---------------------------------------------------------------------------
// AST guard (MUST-0 / MUST-1 / MUST-3) — forbid importing frozen systems.
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
// TestNoExecMethod (MUST-3) — the Facade type must expose no execution method.
// ---------------------------------------------------------------------------

func TestNoExecMethod(t *testing.T) {
	forbidden := []string{
		"Run", "Exec", "Execute", "Invoke", "Dispatch", "Apply", "Rollback",
		"Kill", "Schedule", "Command", "Emit", "Mutate", "Write", "Create", "Delete",
	}
	typ := reflect.TypeOf(Facade{})
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		for _, f := range forbidden {
			if strings.EqualFold(name, f) || name == f {
				t.Errorf("Facade exposes forbidden exec method: %s", name)
			}
		}
	}
	scanForExecMethods(t)
}

func scanForExecMethods(t *testing.T) {
	forbidden := []string{
		"Run", "Exec", "Execute", "Invoke", "Dispatch", "Apply", "Rollback",
		"Kill", "Schedule", "Command", "Emit",
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
// Behavior + determinism (SHOULD-3 lazy, MUST-5 projection).
// ---------------------------------------------------------------------------

func newFacadeWithFakes() (*Facade, *fakeObs, *fakeCluster, *fakeEnt, *fakeGov) {
	obs := &fakeObs{obs: []ObservationView{{ObsID: "o1", Kind: "exec", ExecutionID: "e1"}}}
	cl := &fakeCluster{
		groups:    []string{"g1"},
		labels:    []string{"env:prod"},
		placement: &PlacementView{Version: "cluster/placement/v1", Targets: []string{"h1"}, Reason: "fit"},
	}
	ent := &fakeEnt{atts: []AttachmentView{{AttachID: "a1", TargetKind: "execution", TargetRef: "e1", Kind: "approval"}}}
	gov := &fakeGov{
		verdict: &VerdictView{Code: "Allow", PolicyID: "p1", Reason: "ok"},
		rules:   []RuleView{{RuleID: "r1", Kind: "quota", Priority: 1}},
	}
	f := New(Readers{Obs: obs, Cluster: cl, Enterprise: ent, Governance: gov})
	return f, obs, cl, ent, gov
}

func TestGetExecutionOverview(t *testing.T) {
	f, _, _, _, _ := newFacadeWithFakes()
	ov, err := f.GetExecutionOverview(context.Background(), "e1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ov.ExecutionID != "e1" {
		t.Errorf("ExecutionID = %q, want e1", ov.ExecutionID)
	}
	if ov.Observation == nil || ov.Observation.ObsID != "o1" {
		t.Errorf("observation not composed: %+v", ov.Observation)
	}
	if ov.Placement == nil || len(ov.Placement.Targets) != 1 {
		t.Errorf("placement not composed: %+v", ov.Placement)
	}
	if len(ov.Attachments) != 1 || ov.Attachments[0].AttachID != "a1" {
		t.Errorf("attachments not composed: %+v", ov.Attachments)
	}
	if ov.Verdict == nil || ov.Verdict.Code != "Allow" {
		t.Errorf("verdict not composed: %+v", ov.Verdict)
	}
	if ov.Meta.SourceCapability != "platform" || ov.Meta.SourceID != "e1" {
		t.Errorf("explainability meta missing: %+v", ov.Meta)
	}
}

func TestGetHostPolicyStatus(t *testing.T) {
	f, _, _, _, _ := newFacadeWithFakes()
	st, err := f.GetHostPolicyStatus(context.Background(), "h1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.HostID != "h1" {
		t.Errorf("HostID = %q, want h1", st.HostID)
	}
	if len(st.Attachments) != 1 {
		t.Errorf("attachments not composed: %+v", st.Attachments)
	}
	if len(st.Verdicts) != 1 || st.Verdicts[0].Code != "Allow" {
		t.Errorf("verdicts not composed: %+v", st.Verdicts)
	}
}

func TestGetGovernanceSummary(t *testing.T) {
	f, _, _, _, _ := newFacadeWithFakes()
	sum, err := f.GetGovernanceSummary(context.Background(), "p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.PolicyID != "p1" {
		t.Errorf("PolicyID = %q, want p1", sum.PolicyID)
	}
	if len(sum.MatchedRules) != 1 || sum.MatchedRules[0].RuleID != "r1" {
		t.Errorf("rules not composed: %+v", sum.MatchedRules)
	}
	if len(sum.RecentVerdicts) != 1 {
		t.Errorf("verdicts not composed: %+v", sum.RecentVerdicts)
	}
}

// TestGetGovernanceSummaryNotFound verifies the facade propagates the reader's not-found error
// instead of swallowing it into a 200-empty summary (R79-A root cause).
func TestGetGovernanceSummaryNotFound(t *testing.T) {
	f := New(Readers{Governance: fakeGovNotFound{}})
	if _, err := f.GetGovernanceSummary(context.Background(), "ghost"); !errors.Is(err, ErrPolicyNotFound) {
		t.Fatalf("ghost: err = %v, want ErrPolicyNotFound", err)
	}
	// empty id is a distinct contract error (ErrMissingID), not a not-found.
	if _, err := f.GetGovernanceSummary(context.Background(), ""); err != ErrMissingID {
		t.Fatalf("empty: err = %v, want ErrMissingID", err)
	}
}

func TestGetClusterPlacementView(t *testing.T) {
	f, _, _, _, _ := newFacadeWithFakes()
	pv, err := f.GetClusterPlacementView(context.Background(), "h1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pv.HostID != "h1" {
		t.Errorf("HostID = %q, want h1", pv.HostID)
	}
	if len(pv.Groups) != 1 || pv.Groups[0] != "g1" {
		t.Errorf("groups not composed: %+v", pv.Groups)
	}
	if len(pv.Labels) != 1 || pv.Labels[0] != "env:prod" {
		t.Errorf("labels not composed: %+v", pv.Labels)
	}
	if pv.Placement == nil {
		t.Errorf("placement not composed")
	}
}

func TestMissingID(t *testing.T) {
	f, _, _, _, _ := newFacadeWithFakes()
	if _, err := f.GetExecutionOverview(context.Background(), ""); err != ErrMissingID {
		t.Errorf("empty ID: err = %v, want ErrMissingID", err)
	}
	if _, err := f.GetHostPolicyStatus(context.Background(), ""); err != ErrMissingID {
		t.Errorf("empty host: err = %v, want ErrMissingID", err)
	}
	if _, err := f.GetGovernanceSummary(context.Background(), ""); err != ErrMissingID {
		t.Errorf("empty policy: err = %v, want ErrMissingID", err)
	}
	if _, err := f.GetClusterPlacementView(context.Background(), ""); err != ErrMissingID {
		t.Errorf("empty host placement: err = %v, want ErrMissingID", err)
	}
}

// TestDeterministicAndLazy: with a fixed clock, repeated calls produce identical views
// (deterministic, no cached mutation) and each carries a CollectedAt (lazy, on-demand).
func TestDeterministicAndLazy(t *testing.T) {
	fixed := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	f, _, _, _, _ := newFacadeWithFakes()
	f.now = func() time.Time { return fixed }

	first, err := f.GetExecutionOverview(context.Background(), "e1")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	for i := 0; i < 30; i++ {
		got, err := f.GetExecutionOverview(context.Background(), "e1")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !viewsEqual(first, got) {
			t.Fatalf("non-deterministic view: run0=%+v run%d=%+v", first, i, got)
		}
		if !got.Meta.CollectedAt.Equal(fixed) {
			t.Errorf("lazy CollectedAt not set: %v", got.Meta.CollectedAt)
		}
	}
}

func viewsEqual(a, b ExecutionOverview) bool {
	if a.ExecutionID != b.ExecutionID {
		return false
	}
	if !reflect.DeepEqual(a.Observation, b.Observation) {
		return false
	}
	if !reflect.DeepEqual(a.Placement, b.Placement) {
		return false
	}
	if !reflect.DeepEqual(a.Attachments, b.Attachments) {
		return false
	}
	if !reflect.DeepEqual(a.Verdict, b.Verdict) {
		return false
	}
	return a.Meta.SourceCapability == b.Meta.SourceCapability &&
		a.Meta.SourceID == b.Meta.SourceID &&
		a.Meta.CollectedAt.Equal(b.Meta.CollectedAt) &&
		reflect.DeepEqual(a.Meta.RelatedIDs, b.Meta.RelatedIDs)
}

// TestViewVersionStable (SHOULD-2): the read contract is frozen under platform/view/v1.
func TestViewVersionStable(t *testing.T) {
	if ViewVersion != "platform/view/v1" {
		t.Errorf("ViewVersion = %q, want platform/view/v1", ViewVersion)
	}
}
