package core

import (
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/core/execution"
)

// blockingStep blocks until its context is cancelled, then fails. It lets us
// exercise mid-run cancellation deterministically.
type blockingStep struct{}

func (blockingStep) Describe() string { return "block" }
func (blockingStep) Execute(ctx Context) StepResult {
	<-ctx.Done()
	return StepResult{StepName: "block", Success: false, Error: ctx.Err()}
}

// waitForRunning polls the store until a RUNNING record appears, returning its
// id. Used to obtain the execution id of an in-flight run for cancellation.
func waitForRunning(t *testing.T, store *execution.MemoryStore) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		recs, _ := store.List(execution.Query{Status: execution.StatusRunning})
		if len(recs) > 0 {
			return recs[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	return ""
}

func TestExecutor_CancelMidRun(t *testing.T) {
	store := execution.NewMemoryStore()
	e := NewExecutor(nil)
	rt := NewRuntime(e, store)

	ctx := NewContext().WithLogger(execTestLogger()).Build()
	plan := &ExecutionPlan{
		OperationName: "demo.block",
		Steps:         []ExecutionStep{blockingStep{}},
	}

	done := make(chan RunResult, 1)
	go func() { done <- rt.Run(ctx, plan) }()

	id := waitForRunning(t, store)
	if id == "" {
		t.Fatal("run never registered a RUNNING record")
	}
	if !rt.Cancel(id) {
		t.Fatal("Cancel returned false for a known id")
	}

	rr := <-done
	if !rr.Result.Cancelled {
		t.Fatalf("expected Cancelled=true, got Success=%v Cancelled=%v err=%v",
			rr.Result.Success, rr.Result.Cancelled, rr.Result.Error)
	}
	if rr.ID != id {
		t.Fatalf("run id = %q, want %q", rr.ID, id)
	}

	rec, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != execution.StatusCancelled {
		t.Fatalf("status = %s, want CANCELLED", rec.Status)
	}
	if rec.FinishedAt == nil {
		t.Fatal("expected FinishedAt to be set on CANCELLED")
	}
}

func TestExecutor_CancelUnknownID(t *testing.T) {
	store := execution.NewMemoryStore()
	e := NewExecutor(nil)
	rt := NewRuntime(e, store)
	if rt.Cancel("does-not-exist") {
		t.Fatal("Cancel should return false for an unknown id")
	}
}

// TestExecutor_NoCancelRegistryIsUncancellable confirms that without a
// CancelRegistry the Executor still runs to completion (Phase 0 behaviour is
// preserved) and Cancel is a no-op.
func TestExecutor_NoCancelRegistryIsUncancellable(t *testing.T) {
	store := execution.NewMemoryStore()
	e := NewExecutor(nil)
	e.SetRecorder(store)

	ctx := NewContext().WithLogger(execTestLogger()).Build()
	plan := &ExecutionPlan{
		OperationName: "demo.run",
		Steps:         []ExecutionStep{recFakeStep{name: "a", ok: true}},
	}
	res := e.Execute(ctx, plan)
	if !res.Success {
		t.Fatalf("unexpected failure: %v", res.Error)
	}
}
