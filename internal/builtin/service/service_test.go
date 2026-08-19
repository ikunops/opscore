package service

import (
	"errors"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"log/slog"
)

// mustPlan builds a context with a remote target (so the local capability gate
// is skipped) and asserts Plan succeeds with the expected single step.
func mustPlan(t *testing.T, h core.Handler, name string, input map[string]any) *core.ExecutionPlan {
	t.Helper()
	ctx := core.NewContext().
		WithTarget(core.TargetHost{Address: "127.0.0.1", Port: 22, User: "root", InsecureIgnoreHostKey: true}).
		WithLogger(slog.Default()).
		Build()
	plan, err := h.Plan(ctx, input)
	if err != nil {
		t.Fatalf("%s.Plan err = %v", name, err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("%s: expected 1 step, got %d", name, len(plan.Steps))
	}
	return plan
}

func TestListHandler_Plan(t *testing.T) {
	plan := mustPlan(t, NewListHandler(), "list", map[string]any{})
	if plan.OperationName != "system.service.list" {
		t.Fatalf("op name = %q", plan.OperationName)
	}
	if plan.Permission != (core.Permission{ResourceType: "system.service", Action: "list"}) {
		t.Fatalf("permission = %+v", plan.Permission)
	}
	if plan.Risk != core.RiskLow {
		t.Fatalf("risk = %v", plan.Risk)
	}
	cs, ok := plan.Steps[0].(*core.CommandStep)
	if !ok || cs.Executable != "systemctl" || cs.Args[0] != "list-units" {
		t.Fatalf("unexpected list step: %+v", plan.Steps[0])
	}
}

func TestStartStopHandler_Plan(t *testing.T) {
	for _, tc := range []struct {
		h    core.Handler
		op   string
		verb string
	}{
		{NewStartHandler(), "system.service.start", "start"},
		{NewStopHandler(), "system.service.stop", "stop"},
	} {
		plan := mustPlan(t, tc.h, tc.op, map[string]any{"name": "nginx"})
		if plan.OperationName != tc.op {
			t.Fatalf("op name = %q, want %q", plan.OperationName, tc.op)
		}
		if plan.Risk != core.RiskMedium {
			t.Fatalf("%s risk = %v", tc.op, plan.Risk)
		}
		cs := plan.Steps[0].(*core.CommandStep)
		if cs.Args[0] != tc.verb || cs.Args[1] != "nginx" {
			t.Fatalf("%s step = %v", tc.op, cs.Args)
		}
	}
}

func TestStartStopHandler_RequiresName(t *testing.T) {
	for _, h := range []core.Handler{NewStartHandler(), NewStopHandler()} {
		ctx := core.NewContext().
			WithTarget(core.TargetHost{Address: "127.0.0.1", InsecureIgnoreHostKey: true}).
			WithLogger(slog.Default()).
			Build()
		if _, err := h.Plan(ctx, map[string]any{}); err == nil || !errors.Is(err, core.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput for missing name, got %v", err)
		}
	}
}
