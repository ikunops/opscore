package core

import (
	"errors"
	"testing"

	"log/slog"
)

// echoStep records the target address it ran against into its Output, proving
// the Batch fan-out actually ran the operation against each distinct target.
type echoStep struct{}

func (echoStep) Describe() string { return "echo" }
func (echoStep) Execute(ctx Context) StepResult {
	return StepResult{StepName: "echo", Success: true, Output: ctx.Target().Address}
}

// failStep fails when the target address equals "fail.me", exercising per-target
// failure isolation.
type failStep struct{}

func (failStep) Describe() string { return "fail" }
func (failStep) Execute(ctx Context) StepResult {
	if ctx.Target().Address == "fail.me" {
		return StepResult{StepName: "fail", Success: false, Error: errors.New("boom")}
	}
	return StepResult{StepName: "fail", Success: true, Output: ctx.Target().Address}
}

type batchHandler struct{ fail bool }

func (batchHandler) Plan(_ Context, _ map[string]any) (*ExecutionPlan, error) {
	return &ExecutionPlan{OperationName: "demo.batch", Steps: []ExecutionStep{echoStep{}}}, nil
}

type batchFailHandler struct{}

func (batchFailHandler) Plan(_ Context, _ map[string]any) (*ExecutionPlan, error) {
	return &ExecutionPlan{OperationName: "demo.batchfail", Steps: []ExecutionStep{failStep{}}}, nil
}

func newBatchDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	reg := NewRegistry()
	reg.Register(Operation{Name: "demo.batch", Permission: Permission{ResourceType: "demo", Action: "batch"}, Handler: batchHandler{}})
	reg.Register(Operation{Name: "demo.batchfail", Permission: Permission{ResourceType: "demo", Action: "batchfail"}, Handler: batchFailHandler{}})
	return NewDispatcher(reg, NewExecutor(nil))
}

func TestDispatcher_Batch_FanOut(t *testing.T) {
	d := newBatchDispatcher(t)
	ctx := NewContext().WithUser(UserContext{Name: "tester"}).WithLogger(slog.Default()).Build()
	targets := []TargetHost{
		{Address: "10.0.0.1"},
		{Address: "10.0.0.2"},
		{Address: "10.0.0.3"},
	}
	results := d.Batch(ctx, "demo.batch", targets, nil)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, tr := range targets {
		if results[i].Target.Address != tr.Address {
			t.Fatalf("result %d target = %q, want %q", i, results[i].Target.Address, tr.Address)
		}
		if !results[i].Success {
			t.Fatalf("target %s failed: %s", tr.Address, results[i].Error)
		}
		if results[i].Result.Output != tr.Address {
			t.Fatalf("result %d output = %q, want %q", i, results[i].Result.Output, tr.Address)
		}
	}
}

func TestDispatcher_Batch_FailureIsolation(t *testing.T) {
	d := newBatchDispatcher(t)
	ctx := NewContext().WithUser(UserContext{Name: "tester"}).WithLogger(slog.Default()).Build()
	targets := []TargetHost{
		{Address: "10.0.0.1"},
		{Address: "fail.me"},
		{Address: "10.0.0.3"},
	}
	results := d.Batch(ctx, "demo.batchfail", targets, nil)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if !results[0].Success || !results[2].Success {
		t.Fatalf("healthy targets should still succeed: %+v", results)
	}
	if results[1].Success {
		t.Fatalf("fail.me should have failed")
	}
	if results[1].Error == "" {
		t.Fatalf("failed target should carry an error message")
	}
	// The failure of one target must not abort the others.
	if results[0].Result.Output != "10.0.0.1" || results[2].Result.Output != "10.0.0.3" {
		t.Fatalf("healthy targets not executed: %+v", results)
	}
}

func TestDispatcher_Batch_UnknownOp(t *testing.T) {
	d := newBatchDispatcher(t)
	ctx := NewContext().WithLogger(slog.Default()).Build()
	results := d.Batch(ctx, "nope.op", []TargetHost{{Address: "1.2.3.4"}}, nil)
	if len(results) != 1 || results[0].Success {
		t.Fatalf("unknown op should fail: %+v", results)
	}
}
