package userop

import (
	"errors"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
)

func step(t *testing.T, p *core.ExecutionPlan) *core.CommandStep {
	t.Helper()
	if len(p.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(p.Steps))
	}
	cs, ok := p.Steps[0].(*core.CommandStep)
	if !ok {
		t.Fatalf("step is not a CommandStep: %T", p.Steps[0])
	}
	return cs
}

func TestCreate_Plan(t *testing.T) {
	p, err := NewCreateHandler().Plan(nil, map[string]any{
		"name": "deploy", "group": "app", "shell": "/bin/bash", "system": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cs := step(t, p)
	want := []string{"-m", "-r", "-s", "/bin/bash", "-g", "app", "deploy"}
	if cs.Executable != "useradd" || !equal(cs.Args, want) {
		t.Fatalf("create = %q %v, want useradd %v", cs.Executable, cs.Args, want)
	}
	if p.Risk != core.RiskHigh {
		t.Fatalf("create must be RiskHigh, got %v", p.Risk)
	}
}

func TestCreate_MissingName(t *testing.T) {
	_, err := NewCreateHandler().Plan(nil, map[string]any{})
	if err == nil || !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestDelete_Plan(t *testing.T) {
	p, err := NewDeleteHandler().Plan(nil, map[string]any{"name": "deploy", "recursive": true})
	if err != nil {
		t.Fatal(err)
	}
	cs := step(t, p)
	if cs.Executable != "userdel" || !equal(cs.Args, []string{"-r", "deploy"}) {
		t.Fatalf("delete = %q %v", cs.Executable, cs.Args)
	}
}

func TestList_Plan(t *testing.T) {
	p, err := NewListHandler().Plan(nil, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cs := step(t, p)
	if cs.Executable != "getent" || !equal(cs.Args, []string{"passwd"}) {
		t.Fatalf("list = %q %v", cs.Executable, cs.Args)
	}
	if p.Risk != core.RiskLow {
		t.Fatalf("list must be RiskLow, got %v", p.Risk)
	}
}

func TestModule_RegistersAll(t *testing.T) {
	reg := core.NewRegistry()
	NewModule().Register(reg)
	for _, name := range []string{"system.user.create", "system.user.delete", "system.user.list"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("operation %q not registered", name)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
