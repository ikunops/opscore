package process

import (
	"errors"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"log/slog"
)

func mustPlan(t *testing.T, h core.Handler, name string, input map[string]any) *core.ExecutionPlan {
	t.Helper()
	ctx := core.NewContext().
		WithTarget(core.TargetHost{Address: "127.0.0.1", InsecureIgnoreHostKey: true}).
		WithLogger(slog.Default()).
		Build()
	plan, err := h.Plan(ctx, input)
	if err != nil {
		t.Fatalf("%s.Plan err = %v", name, err)
	}
	return plan
}

func TestListHandler_Plan(t *testing.T) {
	plan := mustPlan(t, NewListHandler(), "list", map[string]any{})
	if plan.OperationName != "system.process.list" {
		t.Fatalf("op name = %q", plan.OperationName)
	}
	if plan.Permission != (core.Permission{ResourceType: "system.process", Action: "list"}) {
		t.Fatalf("permission = %+v", plan.Permission)
	}
	if plan.Risk != core.RiskLow {
		t.Fatalf("risk = %v", plan.Risk)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	if cs.Executable != "ps" || cs.Args[0] != "-eo" {
		t.Fatalf("unexpected ps step: %+v", cs)
	}
}

func TestKillHandler_Plan(t *testing.T) {
	plan := mustPlan(t, NewKillHandler(), "kill", map[string]any{"pid": float64(1234)})
	if plan.OperationName != "system.process.kill" {
		t.Fatalf("op name = %q", plan.OperationName)
	}
	if plan.Permission != (core.Permission{ResourceType: "system.process", Action: "kill"}) {
		t.Fatalf("permission = %+v", plan.Permission)
	}
	if plan.Risk != core.RiskHigh {
		t.Fatalf("risk = %v, want high", plan.Risk)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	if cs.Args[0] != "-TERM" || cs.Args[1] != "1234" {
		t.Fatalf("kill step = %v", cs.Args)
	}
}

func TestKillHandler_PlanWithSignal(t *testing.T) {
	plan := mustPlan(t, NewKillHandler(), "kill", map[string]any{"pid": float64(9), "signal": "KILL"})
	cs := plan.Steps[0].(*core.CommandStep)
	if cs.Args[0] != "-9" || cs.Args[1] != "9" {
		t.Fatalf("kill -9 step = %v", cs.Args)
	}
}

// TestKillHandler_PlanStringPid locks the CLI input channel: -arg pid=123 arrives
// as a string, not a float64. The handler must accept both.
func TestKillHandler_PlanStringPid(t *testing.T) {
	plan := mustPlan(t, NewKillHandler(), "kill", map[string]any{"pid": "123"})
	cs := plan.Steps[0].(*core.CommandStep)
	if cs.Args[0] != "-TERM" || cs.Args[1] != "123" {
		t.Fatalf("kill step from string pid = %v", cs.Args)
	}
}

func TestKillHandler_RequiresPid(t *testing.T) {
	ctx := core.NewContext().
		WithTarget(core.TargetHost{Address: "127.0.0.1", InsecureIgnoreHostKey: true}).
		WithLogger(slog.Default()).
		Build()
	if _, err := NewKillHandler().Plan(ctx, map[string]any{}); err == nil || !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for missing pid, got %v", err)
	}
}

func TestKillHandler_InvalidPid(t *testing.T) {
	ctx := core.NewContext().
		WithTarget(core.TargetHost{Address: "127.0.0.1", InsecureIgnoreHostKey: true}).
		WithLogger(slog.Default()).
		Build()
	for _, bad := range []any{"abc", float64(-1), float64(1.5)} {
		if _, err := NewKillHandler().Plan(ctx, map[string]any{"pid": bad}); err == nil || !errors.Is(err, core.ErrInvalidInput) {
			t.Fatalf("pid=%v: expected ErrInvalidInput, got %v", bad, err)
		}
	}
}
