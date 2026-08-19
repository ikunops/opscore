package packageop

import (
	"errors"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// hostCtx builds a Context whose HostSnapshot reports the given distro, so the
// package resolver selects the matching package manager.
func hostCtx(platformID string) core.Context {
	h := &snapshot.HostSnapshot{OS: "linux", Platform: platformID, Source: snapshot.SourceSSH}
	return core.NewContext().WithHostSnapshot(h).Build()
}

// stepArgs returns the args of the single CommandStep in a plan (test helper).
func stepArgs(t *testing.T, p *core.ExecutionPlan) (string, []string) {
	t.Helper()
	if len(p.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(p.Steps))
	}
	cs, ok := p.Steps[0].(*core.CommandStep)
	if !ok {
		t.Fatalf("step is not a CommandStep: %T", p.Steps[0])
	}
	return cs.Executable, cs.Args
}

func TestInstall_Plan(t *testing.T) {
	p, err := NewInstallHandler().Plan(hostCtx("ubuntu"), map[string]any{
		"names": []any{"nginx", "curl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ex, args := stepArgs(t, p)
	want := []string{"install", "-y", "nginx", "curl"}
	if ex != "apt-get" || !equal(args, want) {
		t.Fatalf("install(ubuntu) = %q %v, want apt-get %v", ex, args, want)
	}
	if p.Permission.String() != "system.package.install" {
		t.Fatalf("permission = %s", p.Permission)
	}
	if p.Risk != core.RiskMedium {
		t.Fatalf("risk = %v", p.Risk)
	}
}

func TestInstall_MissingNames(t *testing.T) {
	_, err := NewInstallHandler().Plan(hostCtx("ubuntu"), map[string]any{})
	if err == nil || !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestRemove_Plan(t *testing.T) {
	p, err := NewRemoveHandler().Plan(hostCtx("centos"), map[string]any{
		"names": []any{"httpd"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ex, args := stepArgs(t, p)
	want := []string{"remove", "-y", "httpd"}
	if ex != "dnf" || !equal(args, want) {
		t.Fatalf("remove(centos) = %q %v, want dnf %v", ex, args, want)
	}
	if p.Risk != core.RiskHigh {
		t.Fatalf("remove must be RiskHigh, got %v", p.Risk)
	}
}

func TestUpdate_Plan(t *testing.T) {
	cases := []struct {
		platform, pm, arg string
	}{
		{"ubuntu", "apt-get", "update"},
		{"alpine", "apk", "update"},
		{"arch", "pacman", "-Sy"},
	}
	for _, tc := range cases {
		p, err := NewUpdateHandler().Plan(hostCtx(tc.platform), map[string]any{})
		if err != nil {
			t.Fatalf("%s: %v", tc.platform, err)
		}
		ex, args := stepArgs(t, p)
		if ex != tc.pm || len(args) != 1 || args[0] != tc.arg {
			t.Fatalf("update(%s) = %q %v, want %q [%q]", tc.platform, ex, args, tc.pm, tc.arg)
		}
	}
}

func TestList_Plan(t *testing.T) {
	p, err := NewListHandler().Plan(hostCtx("debian"), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	ex, args := stepArgs(t, p)
	if ex != "apt-get" || !equal(args, []string{"list", "--installed"}) {
		t.Fatalf("list(debian) = %q %v", ex, args)
	}
	if p.Risk != core.RiskLow {
		t.Fatalf("list must be RiskLow, got %v", p.Risk)
	}
}

// TestModule_RegistersAll verifies every package operation is wired into a
// fresh Registry with the expected name/permission/risk.
func TestModule_RegistersAll(t *testing.T) {
	reg := core.NewRegistry()
	NewModule().Register(reg)
	want := map[string]core.RiskLevel{
		"system.package.install": core.RiskMedium,
		"system.package.remove":  core.RiskHigh,
		"system.package.update":  core.RiskMedium,
		"system.package.list":    core.RiskLow,
	}
	for name, risk := range want {
		op, ok := reg.Get(name)
		if !ok {
			t.Fatalf("operation %q not registered", name)
		}
		if op.Risk != risk {
			t.Fatalf("%s risk = %v, want %v", name, op.Risk, risk)
		}
		if op.Handler == nil {
			t.Fatalf("%s has nil handler", name)
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
