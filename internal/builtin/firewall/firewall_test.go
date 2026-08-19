package firewall

import (
	"errors"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"log/slog"
)

// remoteCtx builds a context with a remote target so the platform resolver
// defaults to iptables — deterministic regardless of the test host tooling.
func remoteCtx(t *testing.T) core.Context {
	t.Helper()
	return core.NewContext().
		WithTarget(core.TargetHost{Address: "127.0.0.1", Port: 22, User: "root", InsecureIgnoreHostKey: true}).
		WithLogger(slog.Default()).
		Build()
}

func TestRuleHandler_Add_Allow(t *testing.T) {
	h := NewAddHandler()
	plan, err := h.Plan(remoteCtx(t), map[string]any{"port": 80, "protocol": "tcp", "action": "allow"})
	if err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	if plan.OperationName != "firewall.rule.add" {
		t.Fatalf("op = %q", plan.OperationName)
	}
	if plan.Risk != core.RiskHigh {
		t.Fatalf("risk = %v", plan.Risk)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	if cs.Executable != "iptables" {
		t.Fatalf("tool = %q", cs.Executable)
	}
	want := []string{"-A", "INPUT", "-p", "tcp", "--dport", "80", "-j", "ACCEPT"}
	if !equalArgs(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
}

func TestRuleHandler_Add_DenyWithSource(t *testing.T) {
	h := NewAddHandler()
	plan, err := h.Plan(remoteCtx(t), map[string]any{"port": 443, "protocol": "tcp", "action": "deny", "source": "10.0.0.0/8"})
	if err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	want := []string{"-A", "INPUT", "-p", "tcp", "--dport", "443", "-s", "10.0.0.0/8", "-j", "DROP"}
	if !equalArgs(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
}

func TestRuleHandler_Remove(t *testing.T) {
	h := NewRemoveHandler()
	plan, err := h.Plan(remoteCtx(t), map[string]any{"port": 80, "protocol": "tcp"})
	if err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	if plan.OperationName != "firewall.rule.remove" {
		t.Fatalf("op = %q", plan.OperationName)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	want := []string{"-D", "INPUT", "-p", "tcp", "--dport", "80", "-j", "ACCEPT"}
	if !equalArgs(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
}

func TestRuleHandler_Validation(t *testing.T) {
	h := NewAddHandler()
	if _, err := h.Plan(remoteCtx(t), map[string]any{}); err == nil || !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("missing port expected ErrInvalidInput, got %v", err)
	}
	if _, err := h.Plan(remoteCtx(t), map[string]any{"port": 80, "protocol": "icmp"}); err == nil || !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("bad protocol expected ErrInvalidInput, got %v", err)
	}
	if _, err := h.Plan(remoteCtx(t), map[string]any{"port": 99999}); err == nil || !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("out-of-range port expected ErrInvalidInput, got %v", err)
	}
}

func TestListHandler(t *testing.T) {
	h := NewListHandler()
	plan, err := h.Plan(remoteCtx(t), map[string]any{})
	if err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	if plan.OperationName != "firewall.rule.list" || plan.Risk != core.RiskLow {
		t.Fatalf("op/risk = %q / %v", plan.OperationName, plan.Risk)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	if cs.Executable != "iptables" || !equalArgs(cs.Args, []string{"-L", "-n"}) {
		t.Fatalf("list step = %q %v", cs.Executable, cs.Args)
	}
}

func equalArgs(a, b []string) bool {
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
