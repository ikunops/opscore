package journal

import (
	"errors"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"log/slog"
)

func remoteCtx(t *testing.T) core.Context {
	t.Helper()
	return core.NewContext().
		WithTarget(core.TargetHost{Address: "127.0.0.1", Port: 22, User: "root", InsecureIgnoreHostKey: true}).
		WithLogger(slog.Default()).
		Build()
}

func TestLogHandler_Defaults(t *testing.T) {
	h := NewLogHandler()
	plan, err := h.Plan(remoteCtx(t), map[string]any{})
	if err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	if plan.OperationName != "system.journal.log" || plan.Risk != core.RiskLow {
		t.Fatalf("op/risk = %q / %v", plan.OperationName, plan.Risk)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	if cs.Executable != "journalctl" {
		t.Fatalf("tool = %q", cs.Executable)
	}
	want := []string{"-n", "100", "--no-pager"}
	if !equalArgs(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
}

func TestLogHandler_UnitAndSince(t *testing.T) {
	h := NewLogHandler()
	plan, err := h.Plan(remoteCtx(t), map[string]any{"unit": "nginx.service", "lines": 50, "since": "1h"})
	if err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	cs := plan.Steps[0].(*core.CommandStep)
	want := []string{"-u", "nginx.service", "--since", "1h", "-n", "50", "--no-pager"}
	if !equalArgs(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
}

func TestLogHandler_NegativeLines(t *testing.T) {
	h := NewLogHandler()
	if _, err := h.Plan(remoteCtx(t), map[string]any{"lines": -5}); err == nil || !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("negative lines expected ErrInvalidInput, got %v", err)
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
