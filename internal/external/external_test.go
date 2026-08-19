package external

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/YuDong999/opscore/internal/correlation"
	"github.com/YuDong999/opscore/internal/platformview"
)

// ---------------------------------------------------------------------------
// Fakes — satisfy the Reader interfaces without importing any frozen system. They return
// intentionally unsorted slices to exercise the mapping's determinism (sort-on-copy).
// ---------------------------------------------------------------------------

type fakePlatform struct{}

func (fakePlatform) GetExecutionOverview(_ context.Context, id string) (platformview.ExecutionOverview, error) {
	return platformview.ExecutionOverview{
		ExecutionID: id,
		Observation: &platformview.ObservationView{ObsID: "obs-2", Kind: "trace", ExecutionID: id},
		Placement:   &platformview.PlacementView{Version: "v1", Targets: []string{"h-b", "h-a"}},
		Attachments: []platformview.AttachmentView{
			{AttachID: "a-2", TargetKind: "host", TargetRef: "h1", Kind: "firewall"},
			{AttachID: "a-1", TargetKind: "host", TargetRef: "h1", Kind: "disk"},
		},
		Verdict: &platformview.VerdictView{Code: "allow", PolicyID: "p1", Priority: 1, Evidence: []string{"e-2", "e-1"}},
		Meta:    platformview.Meta{SourceCapability: "platform", SourceID: id, RelatedIDs: []string{"r-2", "r-1"}},
	}, nil
}

func (fakePlatform) GetHostPolicyStatus(_ context.Context, hostRef string) (platformview.HostPolicyStatus, error) {
	return platformview.HostPolicyStatus{
		HostID:      hostRef,
		Attachments: []platformview.AttachmentView{{AttachID: "a-2", TargetKind: "host", TargetRef: hostRef, Kind: "firewall"}, {AttachID: "a-1", TargetKind: "host", TargetRef: hostRef, Kind: "disk"}},
		Verdicts:    []platformview.VerdictView{{Code: "allow", PolicyID: "p1", Priority: 1}},
		Meta:        platformview.Meta{SourceCapability: "platform", SourceID: hostRef},
	}, nil
}

func (fakePlatform) GetGovernanceSummary(_ context.Context, policyID string) (platformview.GovernanceSummary, error) {
	return platformview.GovernanceSummary{
		PolicyID:       policyID,
		MatchedRules:   []platformview.RuleView{{RuleID: "r-2", Kind: "firewall", Priority: 2}, {RuleID: "r-1", Kind: "disk", Priority: 1}},
		RecentVerdicts: []platformview.VerdictView{{Code: "allow", PolicyID: policyID, Priority: 1}},
		Meta:           platformview.Meta{SourceCapability: "platform", SourceID: policyID},
	}, nil
}

func (fakePlatform) GetClusterPlacementView(_ context.Context, hostRef string) (platformview.ClusterPlacementView, error) {
	return platformview.ClusterPlacementView{
		HostID:    hostRef,
		Groups:    []string{"g-b", "g-a"},
		Labels:    []string{"l-b", "l-a"},
		Placement: &platformview.PlacementView{Version: "v1", Targets: []string{"h-b", "h-a"}},
		Meta:      platformview.Meta{SourceCapability: "platform", SourceID: hostRef},
	}, nil
}

type fakeCorrelate struct{}

func (fakeCorrelate) Correlate(_ context.Context, scope correlation.Scope) (correlation.CorrelationView, error) {
	execRef := ""
	if scope.Kind == correlation.ScopeExecution {
		execRef = scope.Ref
	}
	return correlation.CorrelationView{
		Scope:           scope,
		ExecutionRef:    execRef,
		ObservationRefs: []string{"o-3", "o-1", "o-2"},
		VerdictRefs:     []string{"p1.r2", "p1.r1"},
		PlacementRefs:   []string{"h-2", "h-1"},
		PolicyRefs:      []string{"a-2", "a-1"},
		Meta: correlation.Meta{
			SourceRefs:   []string{"o-1", "o-2", "o-3", "p1.r1", "p1.r2", "h-1", "h-2", "a-1", "a-2"},
			Reason:       "correlated by " + scope.Kind + " " + scope.Ref + " across observation/verdict/placement/policy references",
			CorrelatedAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC),
		},
	}, nil
}

func newServer() *Server {
	return NewServer(fakePlatform{}, fakeCorrelate{}, nil)
}

// ---------------------------------------------------------------------------
// AST guard (MUST-0 / MUST-1 / SHOULD-3) — forbid importing frozen systems or any executor surface.
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
		"internal/core/execution", // Runtime execution path
		"executor",                // any executor surface package
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, forb := range forbidden {
					if strings.Contains(path, forb) {
						t.Errorf("forbidden import of frozen system / executor surface: %s", path)
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestNoExecMethod (MUST-3) — the Server type must expose no execution method, and the package must
// define no command-method functions. R53/R54 added Resolve/Replay/Remediate; ADR-024 adds Mutate.
// ---------------------------------------------------------------------------

func TestNoExecMethod(t *testing.T) {
	forbidden := []string{
		"Run", "Exec", "Execute", "Invoke", "Dispatch", "Apply", "Rollback",
		"Kill", "Schedule", "Command", "Emit", "Mutate", "Write", "Create", "Delete",
		"Resolve", "Replay", "Remediate",
	}
	typ := reflect.TypeOf(Server{})
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		for _, f := range forbidden {
			if strings.EqualFold(name, f) || name == f {
				t.Errorf("Server exposes forbidden exec method: %s", name)
			}
		}
	}
	scanForExecMethods(t)
}

func scanForExecMethods(t *testing.T) {
	forbidden := []string{
		"Run", "Exec", "Execute", "Invoke", "Dispatch", "Apply", "Rollback",
		"Kill", "Schedule", "Command", "Emit", "Mutate", "Resolve", "Replay", "Remediate",
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
// Behavior / determinism.
// ---------------------------------------------------------------------------

// TestDeterministicDTO (SHOULD-4 analogue): 30 runs over the same query produce byte-identical
// DTOs, proving the mapping is deterministic even though the fakes return unsorted slices.
func TestDeterministicDTO(t *testing.T) {
	srv := newServer()
	first, err := srv.GetExecution(context.Background(), "e1")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b1, _ := json.Marshal(first)
	for i := 0; i < 30; i++ {
		got, err := srv.GetExecution(context.Background(), "e1")
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		b2, _ := json.Marshal(got)
		if !bytes.Equal(b1, b2) {
			t.Fatalf("non-deterministic DTO:\n run0=%s\n run%d=%s", b1, i, b2)
		}
	}
}

// TestMappingSortsDeterministically ensures every slice in the DTO is copied and sorted, so the
// output is stable and traceable (MUST-4/5 + SHOULD-1).
func TestMappingSortsDeterministically(t *testing.T) {
	srv := newServer()
	v, err := srv.GetExecution(context.Background(), "e1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Placement.Targets, []string{"h-a", "h-b"}) {
		t.Errorf("Placement.Targets not sorted: %v", v.Placement.Targets)
	}
	if len(v.Attachments) != 2 || v.Attachments[0].AttachID != "a-1" || v.Attachments[1].AttachID != "a-2" {
		t.Errorf("Attachments not sorted by AttachID: %+v", v.Attachments)
	}
	if !reflect.DeepEqual(v.Verdict.Evidence, []string{"e-1", "e-2"}) {
		t.Errorf("Verdict.Evidence not sorted: %v", v.Verdict.Evidence)
	}
	if !reflect.DeepEqual(v.Meta.SourceRefs, []string{"r-1", "r-2"}) {
		t.Errorf("Meta.SourceRefs not sorted: %v", v.Meta.SourceRefs)
	}
}

// TestContractVersionStable (MUST-6 / SHOULD-2): the read contract is frozen under external/v1 and
// is distinct from platform/view/v1 and correlation/view/v1.
func TestContractVersionStable(t *testing.T) {
	if ContractVersion != "external/v1" {
		t.Errorf("ContractVersion = %q, want external/v1", ContractVersion)
	}
	if ContractVersion == platformview.ViewVersion {
		t.Errorf("ContractVersion collides with platform/view/v1")
	}
	if ContractVersion == correlation.ViewVersion {
		t.Errorf("ContractVersion collides with correlation/view/v1")
	}
}

// TestAuthnSeam (MUST-0 / MUST-5): nil authn defaults to the single-tenant / no-auth stub; the stub
// always succeeds and returns the constant tenant. It never becomes an execution surface.
func TestAuthnSeam(t *testing.T) {
	srv := newServer() // authn nil -> NoAuthAuthenticator
	v, err := srv.GetExecution(context.Background(), "e1")
	if err != nil {
		t.Fatalf("stub authn should not reject: %v", err)
	}
	if v == nil {
		t.Fatal("nil view")
	}
	ten, err := NoAuthAuthenticator{}.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("authn err: %v", err)
	}
	if ten.ID != "single-tenant" {
		t.Errorf("tenant ID = %q, want single-tenant", ten.ID)
	}
}

// TestGetCorrelation exercises the correlation projection over the external/v1 contract.
func TestGetCorrelation(t *testing.T) {
	srv := newServer()
	cv, err := srv.GetCorrelation(context.Background(), ScopeDTO{Kind: correlation.ScopeExecution, Ref: "e1"})
	if err != nil {
		t.Fatalf("correlate: %v", err)
	}
	if cv.Scope.Kind != "execution" || cv.ExecutionRef != "e1" {
		t.Errorf("scope/ExecutionRef wrong: %+v", cv)
	}
	if !reflect.DeepEqual(cv.ObservationRefs, []string{"o-1", "o-2", "o-3"}) {
		t.Errorf("ObservationRefs not sorted: %v", cv.ObservationRefs)
	}
	if cv.Meta.ViewVersion != "external/v1" {
		t.Errorf("meta.ViewVersion = %q", cv.Meta.ViewVersion)
	}
	if _, err := srv.GetCorrelation(context.Background(), ScopeDTO{}); err != ErrInvalidInput {
		t.Errorf("empty scope: err=%v, want ErrInvalidInput", err)
	}
}

// TestGetHostAndPolicy exercises the host/policy projections and their deterministic sorting.
func TestGetHostAndPolicy(t *testing.T) {
	srv := newServer()
	h, err := srv.GetHost(context.Background(), "h1")
	if err != nil {
		t.Fatal(err)
	}
	if h.HostID != "h1" {
		t.Errorf("host id = %q", h.HostID)
	}
	if len(h.Attachments) != 2 || h.Attachments[0].AttachID != "a-1" {
		t.Errorf("host attachments not sorted: %+v", h.Attachments)
	}
	if !reflect.DeepEqual(h.Groups, []string{"g-a", "g-b"}) {
		t.Errorf("groups not sorted: %v", h.Groups)
	}
	if !reflect.DeepEqual(h.Labels, []string{"l-a", "l-b"}) {
		t.Errorf("labels not sorted: %v", h.Labels)
	}

	p, err := srv.GetPolicy(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if p.PolicyID != "p1" {
		t.Errorf("policy id = %q", p.PolicyID)
	}
	if len(p.MatchedRules) != 2 || p.MatchedRules[0].RuleID != "r-1" {
		t.Errorf("rules not sorted: %+v", p.MatchedRules)
	}
}

// TestInvalidInput ensures missing ids are rejected with ErrInvalidInput (contract/query error
// only — no execution error is defined, MUST-0/3).
func TestInvalidInput(t *testing.T) {
	srv := newServer()
	cases := []func() error{
		func() error { _, e := srv.GetExecution(context.Background(), ""); return e },
		func() error { _, e := srv.GetHost(context.Background(), ""); return e },
		func() error { _, e := srv.GetPolicy(context.Background(), ""); return e },
	}
	for i, c := range cases {
		if err := c(); err != ErrInvalidInput {
			t.Errorf("case %d: err = %v, want ErrInvalidInput", i, err)
		}
	}
}

// notFoundPlatform delegates to fakePlatform but reports platformview.ErrPolicyNotFound for the
// sentinel "ghost" id, exercising the external/v1 not-found → 404 translation path (R79-A).
type notFoundPlatform struct{ fakePlatform }

func (notFoundPlatform) GetGovernanceSummary(_ context.Context, policyID string) (platformview.GovernanceSummary, error) {
	if policyID == "ghost" {
		return platformview.GovernanceSummary{}, platformview.ErrPolicyNotFound
	}
	return fakePlatform{}.GetGovernanceSummary(context.Background(), policyID)
}

// TestGetPolicyNotFound verifies an unknown policy surfaces as external.ErrNotFound (and therefore
// HTTP 404 at the harness layer), while an existing policy still resolves normally.
func TestGetPolicyNotFound(t *testing.T) {
	srv := NewServer(notFoundPlatform{}, fakeCorrelate{}, nil)

	if _, err := srv.GetPolicy(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ghost: err = %v, want external.ErrNotFound", err)
	}
	p, err := srv.GetPolicy(context.Background(), "p1")
	if err != nil {
		t.Fatalf("existing p1: err = %v", err)
	}
	if p == nil || p.PolicyID != "p1" {
		t.Fatalf("existing p1: p = %+v", p)
	}
}
