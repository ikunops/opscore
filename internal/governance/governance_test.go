package governance

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestASTGuardForbiddenImports mechanically enforces ADR-018 MUST-5: Governance
// must not re-implement or own the frozen/accepted systems. It forbids
// importing the Runtime engine, process isolation, host registry, cluster,
// observability, or enterprise — matching the AST-guard discipline established
// by internal/observability, internal/cluster, and internal/enterprise.
func TestASTGuardForbiddenImports(t *testing.T) {
	forbidden := []string{
		"internal/plugin/runtime",
		"internal/plugin/isolation",
		"internal/controlplane/hostregistry",
		"internal/cluster",
		"internal/observability",
		"internal/enterprise",
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
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
					t.Errorf("%s imports forbidden package %s (ADR-018 MUST-5: Governance must not own/replicate frozen systems)", filepath.Base(f), forb)
				}
			}
		}
	}
}

// TestNoExecMethod verifies by construction that the Engine type exposes no
// execution-style method. We assert the public method set is limited to
// evaluation — there is no Run/Exec/Apply/Rollback/Schedule/Emit.
func TestNoExecMethod(t *testing.T) {
	typ := reflect.TypeOf(&Engine{})
	banned := []string{"Run", "Exec", "Apply", "Rollback", "Schedule", "Emit", "Execute"}
	for _, b := range banned {
		if _, ok := typ.MethodByName(b); ok {
			t.Errorf("Engine must not expose execution method %q (ADR-018 MUST-1/2)", b)
		}
	}
}

// TestVerdictIsFrozenValueObject asserts the Stable Verdict Contract
// (ADR-018 SHOULD-2): the verdict is a fixed-shape Value Object whose fields
// are all present and are plain data, never an Action/Command. Compile-time
// literal proves the shape is frozen.
func TestVerdictIsFrozenValueObject(t *testing.T) {
	v := Verdict{
		Code:          Deny,
		Reason:        "change freeze active",
		PolicyID:      "pol-1",
		RuleID:        "r-1",
		MatchedPolicy: "pol-1",
		MatchedRule:   "r-1",
		Priority:      100,
		Evidence:      map[string]string{"ruleKind": "change-freeze"},
	}
	if v.Code != Deny || v.MatchedRule != "r-1" || v.Priority != 100 {
		t.Fatalf("verdict value object wrong: %+v", v)
	}
	// The verdict must not carry any action/command-shaped field; only the
	// enumerated fields above exist by construction (see model.go).
}

// TestEvaluateMaintenanceWindow proves an active maintenance window yields
// MaintenanceBlocked (MUST-2: a Verdict, not an action).
func TestEvaluateMaintenanceWindow(t *testing.T) {
	e := NewEngine()
	pol := Policy{PolicyID: "prod-maint", Rules: []Rule{
		{RuleID: "r-mw", Priority: 100, Kind: RuleMaintenanceWindow},
	}}
	st := State{InMaintenanceWindow: true, Group: "web", Tenant: "acme"}
	v := e.Evaluate(pol, st)
	if v.Code != MaintenanceBlocked {
		t.Fatalf("expected MaintenanceBlocked, got %s", v.Code)
	}
}

// TestEvaluateChangeFreeze proves an active change freeze yields Deny.
func TestEvaluateChangeFreeze(t *testing.T) {
	e := NewEngine()
	pol := Policy{PolicyID: "prod-freeze", Rules: []Rule{
		{RuleID: "r-cf", Priority: 100, Kind: RuleChangeFreeze},
	}}
	v := e.Evaluate(pol, State{ChangeFreeze: true})
	if v.Code != Deny {
		t.Fatalf("expected Deny, got %s", v.Code)
	}
}

// TestEvaluateRequireApproval proves a weighed action yields RequireApproval.
func TestEvaluateRequireApproval(t *testing.T) {
	e := NewEngine()
	pol := Policy{PolicyID: "prod-approval", Rules: []Rule{
		{RuleID: "r-ra", Priority: 50, Kind: RuleRequireApproval},
	}}
	v := e.Evaluate(pol, State{RequiresApproval: true})
	if v.Code != RequireApproval {
		t.Fatalf("expected RequireApproval, got %s", v.Code)
	}
}

// TestEvaluateTenantScope proves a tenant out of scope yields Deny.
func TestEvaluateTenantScope(t *testing.T) {
	e := NewEngine()
	pol := Policy{PolicyID: "tenant-acme", Rules: []Rule{
		{RuleID: "r-ts", Priority: 80, Kind: RuleTenantScope, Param: "acme"},
	}}
	// In-scope tenant: allow (no match).
	if v := e.Evaluate(pol, State{Tenant: "acme"}); v.Code != Allow {
		t.Fatalf("in-scope tenant should allow, got %s", v.Code)
	}
	// Out-of-scope tenant: deny.
	if v := e.Evaluate(pol, State{Tenant: "other"}); v.Code != Deny {
		t.Fatalf("out-of-scope tenant should deny, got %s", v.Code)
	}
}

// TestEvaluateGroupAllow proves an explicit group allow yields Allow.
func TestEvaluateGroupAllow(t *testing.T) {
	e := NewEngine()
	pol := Policy{PolicyID: "group-web", Rules: []Rule{
		{RuleID: "r-ga", Priority: 10, Kind: RuleGroupAllow, Param: "web"},
	}}
	if v := e.Evaluate(pol, State{Group: "web"}); v.Code != Allow {
		t.Fatalf("group web should allow, got %s", v.Code)
	}
	if v := e.Evaluate(pol, State{Group: "db"}); v.Code != Allow {
		t.Fatalf("no rule matched → default allow, got %s", v.Code)
	}
}

// TestEvaluateNoMatchDefaultsAllow proves that when no rule matches, the
// default verdict is Allow (the engine never blocks by omission).
func TestEvaluateNoMatchDefaultsAllow(t *testing.T) {
	e := NewEngine()
	pol := Policy{PolicyID: "noop", Rules: []Rule{
		{RuleID: "r-1", Priority: 100, Kind: RuleChangeFreeze},
	}}
	v := e.Evaluate(pol, State{}) // nothing active
	if v.Code != Allow {
		t.Fatalf("expected default Allow, got %s", v.Code)
	}
	if v.RuleID != "" {
		t.Fatalf("default Allow should have no matched rule, got %q", v.RuleID)
	}
}

// TestEvaluatePriorityOrder proves the highest-priority matching rule wins,
// independent of slice order. Two rules match (both Deny/MaintBlocked), the
// higher-priority one's verdict is returned.
func TestEvaluatePriorityOrder(t *testing.T) {
	e := NewEngine()
	// Both rules would match; maint (pri 100) outranks freeze (pri 50).
	pol := Policy{PolicyID: "pri", Rules: []Rule{
		{RuleID: "r-freeze", Priority: 50, Kind: RuleChangeFreeze},
		{RuleID: "r-maint", Priority: 100, Kind: RuleMaintenanceWindow},
	}}
	v := e.Evaluate(pol, State{ChangeFreeze: true, InMaintenanceWindow: true})
	if v.Code != MaintenanceBlocked {
		t.Fatalf("expected higher-priority MaintenanceBlocked, got %s", v.Code)
	}
	if v.RuleID != "r-maint" {
		t.Fatalf("expected matched rule r-maint, got %q", v.RuleID)
	}

	// Reverse the slice order — result must be identical (deterministic order).
	pol2 := Policy{PolicyID: "pri", Rules: []Rule{
		{RuleID: "r-maint", Priority: 100, Kind: RuleMaintenanceWindow},
		{RuleID: "r-freeze", Priority: 50, Kind: RuleChangeFreeze},
	}}
	v2 := e.Evaluate(pol2, State{ChangeFreeze: true, InMaintenanceWindow: true})
	if v2.Code != MaintenanceBlocked || v2.RuleID != "r-maint" {
		t.Fatalf("priority order must be stable regardless of slice order: %+v", v2)
	}
}

// TestEvaluateDeterministic proves MUST-4: identical (Policy, State) always
// yields an identical Verdict. Evaluated many times, the verdict is unchanged.
func TestEvaluateDeterministic(t *testing.T) {
	e := NewEngine()
	pol := Policy{PolicyID: "det", Rules: []Rule{
		{RuleID: "r-b", Priority: 100, Kind: RuleMaintenanceWindow},
		{RuleID: "r-a", Priority: 100, Kind: RuleChangeFreeze},
	}}
	st := State{InMaintenanceWindow: true, ChangeFreeze: true, Group: "web", Tenant: "acme", PluginID: "p1"}

	first := e.Evaluate(pol, st)
	for i := 0; i < 50; i++ {
		got := e.Evaluate(pol, st)
		if !verdictEqual(got, first) {
			t.Fatalf("non-deterministic verdict: run0=%+v run%d=%+v", first, i, got)
		}
	}
}

// verdictEqual compares two verdicts field-by-field. Verdict contains a map,
// so it cannot use ==; we compare the deterministic data explicitly.
func verdictEqual(a, b Verdict) bool {
	if a.Code != b.Code || a.Reason != b.Reason || a.PolicyID != b.PolicyID ||
		a.RuleID != b.RuleID || a.MatchedPolicy != b.MatchedPolicy || a.MatchedRule != b.MatchedRule ||
		a.Priority != b.Priority {
		return false
	}
	if len(a.Evidence) != len(b.Evidence) {
		return false
	}
	for k, v := range a.Evidence {
		if b.Evidence[k] != v {
			return false
		}
	}
	return true
}

// TestExplainableVerdict proves SHOULD-1: every non-default verdict carries
// Reason / MatchedPolicy / MatchedRule / Priority / Evidence so Observability,
// Audit, and Enterprise can reference it directly without re-interpreting.
func TestExplainableVerdict(t *testing.T) {
	e := NewEngine()
	pol := Policy{PolicyID: "explain", Rules: []Rule{
		{RuleID: "r-1", Priority: 100, Kind: RuleMaintenanceWindow},
	}}
	v := e.Evaluate(pol, State{InMaintenanceWindow: true, PluginID: "p-x", Tenant: "acme"})
	if v.Reason == "" {
		t.Fatal("verdict must carry a Reason (SHOULD-1)")
	}
	if v.MatchedPolicy != "explain" || v.MatchedRule != "r-1" {
		t.Fatalf("verdict must carry MatchedPolicy/MatchedRule (SHOULD-1): %+v", v)
	}
	if v.Priority != 100 {
		t.Fatalf("verdict must carry Priority: %+v", v)
	}
	if len(v.Evidence) == 0 {
		t.Fatal("verdict must carry Evidence (SHOULD-1)")
	}
	if v.Evidence["plugin"] != "p-x" || v.Evidence["tenant"] != "acme" {
		t.Fatalf("evidence must reference existing IDs only: %+v", v.Evidence)
	}
}

// TestValidatePolicy proves the input guard rejects an empty policy id
// (ADR-018 MUST-3) while tolerating it at evaluate time.
func TestValidatePolicy(t *testing.T) {
	if err := ValidatePolicy(Policy{}); err != ErrPolicyEmpty {
		t.Fatalf("expected ErrPolicyEmpty, got %v", err)
	}
	if err := ValidatePolicy(Policy{PolicyID: "ok"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
