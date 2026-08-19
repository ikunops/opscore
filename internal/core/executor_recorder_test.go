package core

import (
	"io"
	"log/slog"
	"testing"

	"github.com/YuDong999/opscore/internal/core/execution"
)

// recFakeStep is a deterministic, side-effect-free ExecutionStep for tests.
type recFakeStep struct {
	name string
	ok   bool
	out  string
}

func (f recFakeStep) Describe() string { return f.name }
func (f recFakeStep) Execute(ctx Context) StepResult {
	// Give the step a stable id so the recorder can distinguish steps
	// (mirrors how CommandStep derives StepID from ID / Index).
	return StepResult{StepName: f.name, StepID: "step-" + f.name, Success: f.ok, Output: f.out}
}

func execTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestExecutor_RecordsSuccessLifecycle(t *testing.T) {
	store := execution.NewMemoryStore()
	e := NewExecutor(nil)
	e.SetRecorder(store)

	ctx := NewContext().WithLogger(execTestLogger()).Build()
	plan := &ExecutionPlan{
		OperationName: "demo.run",
		Permission:    Permission{ResourceType: "demo", Action: "run"},
		Risk:          RiskLow,
		Steps: []ExecutionStep{
			recFakeStep{name: "step1", ok: true, out: "hello"},
			recFakeStep{name: "step2", ok: true, out: "world"},
		},
	}

	res := e.Execute(ctx, plan)
	if !res.Success {
		t.Fatalf("unexpected failure: %v", res.Error)
	}

	all, err := store.List(execution.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("recorded executions = %d, want 1", len(all))
	}
	rec := all[0]
	if rec.Operation != "demo.run" {
		t.Fatalf("operation = %q", rec.Operation)
	}
	if rec.Status != execution.StatusSuccess {
		t.Fatalf("status = %s, want SUCCESS", rec.Status)
	}
	if rec.FinishedAt == nil {
		t.Fatal("expected FinishedAt on success")
	}
	if len(rec.Steps) != 2 {
		t.Fatalf("recorded steps = %d, want 2", len(rec.Steps))
	}
	if rec.Steps[0].Output != "hello" || rec.Steps[1].Output != "world" {
		t.Fatalf("step outputs: %q / %q", rec.Steps[0].Output, rec.Steps[1].Output)
	}
}

func TestExecutor_RecordsFailureLifecycle(t *testing.T) {
	store := execution.NewMemoryStore()
	e := NewExecutor(nil)
	e.SetRecorder(store)

	ctx := NewContext().WithLogger(execTestLogger()).Build()
	plan := &ExecutionPlan{
		OperationName: "demo.fail",
		Steps: []ExecutionStep{
			recFakeStep{name: "ok", ok: true},
			recFakeStep{name: "boom", ok: false},
		},
	}

	res := e.Execute(ctx, plan)
	if res.Success {
		t.Fatal("expected failure")
	}

	all, _ := store.List(execution.Query{})
	if len(all) != 1 {
		t.Fatalf("recorded executions = %d, want 1", len(all))
	}
	rec := all[0]
	if rec.Status != execution.StatusFailed {
		t.Fatalf("status = %s, want FAILED", rec.Status)
	}
}

func TestExecutor_NoRecorderIsNoOp(t *testing.T) {
	// Default Executor (no recorder) must not panic and must preserve
	// the original Phase 0 behaviour.
	e := NewExecutor(nil)
	ctx := NewContext().WithLogger(execTestLogger()).Build()
	plan := &ExecutionPlan{
		OperationName: "demo.noop",
		Steps:         []ExecutionStep{recFakeStep{name: "a", ok: true}},
	}
	res := e.Execute(ctx, plan)
	if !res.Success {
		t.Fatalf("unexpected failure: %v", res.Error)
	}
}
