package capability

import (
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	corecap "github.com/YuDong999/opscore/internal/core/capability"
)

func TestListHandler_Plan(t *testing.T) {
	h := NewListHandler()
	plan, err := h.Plan(core.NewContext().Build(), nil)
	if err != nil {
		t.Fatalf("plan error: %v", err)
	}
	if plan.OperationName != "system.host.capability.list" {
		t.Fatalf("operation name = %q", plan.OperationName)
	}
	if plan.Permission != (core.Permission{ResourceType: "system.host", Action: "read"}) {
		t.Fatalf("permission = %+v", plan.Permission)
	}
	if plan.Risk != core.RiskLow {
		t.Fatalf("risk = %v, want low", plan.Risk)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
	// Critical: capability discovery must NOT be a shell CommandStep.
	if _, ok := plan.Steps[0].(*corecap.CollectStep); !ok {
		t.Fatalf("expected *corecap.CollectStep, got %T (capability must not be a shell command)", plan.Steps[0])
	}
}
