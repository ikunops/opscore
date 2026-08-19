package core

import "testing"

// TestContext_ExecutionID_Verifies the id stamped by WithExecutionID
// propagates through WithCancel / WithTarget children, so an async
// ExecutionService.Submit can correlate every downstream AuditEvent with
// the run's ExecutionRecord (Phase 2.1 / Round 3 decision).
func TestContext_ExecutionID_Propagates(t *testing.T) {
	base := NewContext().WithExecutionID("exec-xyz").Build()
	if base.ExecutionID() != "exec-xyz" {
		t.Fatalf("base ExecutionID = %q, want exec-xyz", base.ExecutionID())
	}

	cancelled, cancel := WithCancel(base)
	if cancelled.ExecutionID() != "exec-xyz" {
		t.Fatalf("WithCancel dropped ExecutionID: %q", cancelled.ExecutionID())
	}
	cancel()

	targeted := WithTarget(base, TargetHost{Address: "192.168.0.1"})
	if targeted.ExecutionID() != "exec-xyz" {
		t.Fatalf("WithTarget dropped ExecutionID: %q", targeted.ExecutionID())
	}

	// A child can re-stamp without affecting the parent.
	child := base.WithExecutionID("exec-child")
	if child.ExecutionID() != "exec-child" {
		t.Fatalf("child ExecutionID = %q, want exec-child", child.ExecutionID())
	}
	if base.ExecutionID() != "exec-xyz" {
		t.Fatalf("parent mutated by child: %q", base.ExecutionID())
	}
}
