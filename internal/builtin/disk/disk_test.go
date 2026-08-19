package diskop

import (
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

func TestUsage_Plan(t *testing.T) {
	p, err := NewUsageHandler().Plan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs := step(t, p)
	if cs.Executable != "df" || !equal(cs.Args, []string{"-h"}) {
		t.Fatalf("usage = %q %v", cs.Executable, cs.Args)
	}
	if p.Risk != core.RiskLow {
		t.Fatalf("usage must be RiskLow, got %v", p.Risk)
	}
}

func TestList_Plan(t *testing.T) {
	p, err := NewListHandler().Plan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs := step(t, p)
	want := []string{"-o", "NAME,SIZE,TYPE,MOUNTPOINT,FSTYPE"}
	if cs.Executable != "lsblk" || !equal(cs.Args, want) {
		t.Fatalf("list = %q %v, want lsblk %v", cs.Executable, cs.Args, want)
	}
}

func TestMounts_Plan(t *testing.T) {
	p, err := NewMountsHandler().Plan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs := step(t, p)
	want := []string{"-o", "TARGET,SOURCE,FSTYPE,OPTIONS"}
	if cs.Executable != "findmnt" || !equal(cs.Args, want) {
		t.Fatalf("mounts = %q %v, want findmnt %v", cs.Executable, cs.Args, want)
	}
}

func TestModule_RegistersAll(t *testing.T) {
	reg := core.NewRegistry()
	NewModule().Register(reg)
	for _, name := range []string{"system.disk.usage", "system.disk.list", "system.disk.mounts"} {
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
