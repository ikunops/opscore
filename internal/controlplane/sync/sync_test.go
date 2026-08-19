package sync

import (
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/storage"
)

type stubHandler struct{}

func (stubHandler) Plan(core.Context, map[string]any) (*core.ExecutionPlan, error) {
	return &core.ExecutionPlan{}, nil
}

func TestSynchronizer_Sync(t *testing.T) {
	reg := core.NewRegistry()
	reg.Register(core.Operation{
		Name:       "system.service.restart",
		Permission: core.Permission{ResourceType: "service", Action: "restart"},
		Risk:       core.RiskHigh,
		Handler:    stubHandler{},
	})
	reg.Register(core.Operation{
		Name:       "firewall.rule.add",
		Permission: core.Permission{ResourceType: "firewall", Action: "add"},
		Risk:       core.RiskCritical,
		Handler:    stubHandler{},
	})

	stor := storage.NewMemoryStorage()
	s := New(reg, stor)
	if err := s.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	op, err := stor.Operations().GetByName("system.service.restart")
	if err != nil || op.ResourceType != "service" || op.ActionType != "restart" ||
		op.Risk != "high" || op.Source != "builtin" || !op.Enabled {
		t.Fatalf("op projection: %v %+v", err, op)
	}

	// The control-plane execution.* system ops must also be projected.
	sysOp, err := stor.Operations().GetByName("execution.create")
	if err != nil || sysOp.ResourceType != "execution" || sysOp.ActionType != "execution.create" ||
		sysOp.Risk != "low" || sysOp.Source != "system" || !sysOp.Enabled {
		t.Fatalf("system op projection: %v %+v", err, sysOp)
	}

	roles, _ := stor.Roles().List()
	if len(roles) != 1 || roles[0].Name != DefaultAdminRole {
		t.Fatalf("roles: %+v", roles)
	}
	granted, _ := stor.Roles().Operations(roles[0].ID)
	// 2 builtin ops + 4 system ops (execution.create/read/cancel/list).
	if len(granted) != 6 {
		t.Fatalf("expected admin to hold 6 ops (2 builtin + 4 system), got %d", len(granted))
	}
}

func TestSynchronizer_Idempotent(t *testing.T) {
	reg := core.NewRegistry()
	reg.Register(core.Operation{
		Name: "system.service.restart", Permission: core.Permission{ResourceType: "service", Action: "restart"},
		Risk: core.RiskMedium, Handler: stubHandler{},
	})
	stor := storage.NewMemoryStorage()
	s := New(reg, stor)
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(); err != nil { // second run must not duplicate
		t.Fatalf("second sync: %v", err)
	}
	// 1 builtin op + 4 system ops; re-running must not duplicate either set.
	all, _ := stor.Operations().List()
	if len(all) != 5 {
		t.Fatalf("expected 5 operations (1 builtin + 4 system) after double sync, got %d", len(all))
	}
	roles, _ := stor.Roles().List()
	if len(roles) != 1 {
		t.Fatalf("expected 1 role after double sync, got %d", len(roles))
	}
}
