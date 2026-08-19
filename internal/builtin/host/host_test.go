package host

import (
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"log/slog"
)

// mustPlan builds a context with a remote target (skips the local capability
// gate) and asserts Plan succeeds.
func mustPlan(t *testing.T, h core.Handler, name string) *core.ExecutionPlan {
	t.Helper()
	ctx := core.NewContext().
		WithTarget(core.TargetHost{Address: "127.0.0.1", Port: 22, User: "root", InsecureIgnoreHostKey: true}).
		WithLogger(slog.Default()).
		Build()
	plan, err := h.Plan(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("%s.Plan err = %v", name, err)
	}
	return plan
}

func TestInfoHandler_Plan(t *testing.T) {
	plan := mustPlan(t, NewInfoHandler(), "info")
	if plan.OperationName != "system.host.info" {
		t.Fatalf("op name = %q", plan.OperationName)
	}
	if plan.Permission != (core.Permission{ResourceType: "system.host", Action: "info"}) {
		t.Fatalf("permission = %+v", plan.Permission)
	}
	if plan.Risk != core.RiskLow {
		t.Fatalf("risk = %v", plan.Risk)
	}
	if len(plan.Steps) != 4 {
		t.Fatalf("expected 4 telemetry steps, got %d", len(plan.Steps))
	}
}

func TestRebootHandler_Plan(t *testing.T) {
	plan := mustPlan(t, NewRebootHandler(), "reboot")
	if plan.OperationName != "system.host.reboot" {
		t.Fatalf("op name = %q", plan.OperationName)
	}
	if plan.Permission != (core.Permission{ResourceType: "system.host", Action: "reboot"}) {
		t.Fatalf("permission = %+v", plan.Permission)
	}
	if plan.Risk != core.RiskCritical {
		t.Fatalf("risk = %v, want critical", plan.Risk)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	if cs.Executable != "reboot" {
		t.Fatalf("reboot step = %+v", cs)
	}
}
