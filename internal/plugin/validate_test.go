package plugin

import (
	"testing"

	"github.com/YuDong999/opscore/internal/core"
)

func TestValidateOperation_AcceptsPluginNamespace(t *testing.T) {
	op := core.Operation{
		Name:       "plugin.mysql.backup.execute",
		Permission: core.Permission{ResourceType: "backup", Action: "execute"},
		Risk:       core.RiskLow,
	}
	if err := ValidateOperation(op); err != nil {
		t.Fatalf("valid plugin op rejected: %v", err)
	}
}

func TestValidateOperation_RejectsNonPluginPrefix(t *testing.T) {
	op := core.Operation{
		Name:       "system.service.restart",
		Permission: core.Permission{ResourceType: "service", Action: "restart"},
		Risk:       core.RiskHigh,
	}
	if err := ValidateOperation(op); err == nil {
		t.Fatal("expected rejection for non-plugin namespace, got nil")
	}
}

func TestValidateOperation_RejectsEmptyResourceAction(t *testing.T) {
	cases := []core.Operation{
		{Name: "plugin.x.a", Permission: core.Permission{ResourceType: "", Action: "a"}},
		{Name: "plugin.x.a", Permission: core.Permission{ResourceType: "r", Action: ""}},
	}
	for i, op := range cases {
		if err := ValidateOperation(op); err == nil {
			t.Fatalf("case %d: expected rejection for empty resource/action", i)
		}
	}
}

func TestValidateOperation_RejectsReservedResource(t *testing.T) {
	op := core.Operation{
		Name:       "plugin.mysql.execution.create",
		Permission: core.Permission{ResourceType: "execution", Action: "create"},
		Risk:       core.RiskLow,
	}
	if err := ValidateOperation(op); err == nil {
		t.Fatal("expected rejection for reserved system resource 'execution'")
	}
}

func TestValidateOperation_RejectsReservedPrefix(t *testing.T) {
	cases := []core.Operation{
		{Name: "plugin.builtin.x.y", Permission: core.Permission{ResourceType: "x", Action: "y"}},
		{Name: "plugin.system.x.y", Permission: core.Permission{ResourceType: "x", Action: "y"}},
		{Name: "plugin.core.x.y", Permission: core.Permission{ResourceType: "x", Action: "y"}},
		{Name: "plugin.internal.x.y", Permission: core.Permission{ResourceType: "x", Action: "y"}},
	}
	for i, op := range cases {
		if err := ValidateOperation(op); err == nil {
			t.Fatalf("case %d: expected rejection for reserved system prefix in %q", i, op.Name)
		}
	}
}
