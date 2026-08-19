package core

import (
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/core/execution"
)

// okStep succeeds immediately so a submitted execution reaches Success
// without needing a timer.
type okStep struct{}

func (okStep) Describe() string { return "ok" }
func (okStep) Execute(ctx Context) StepResult {
	return StepResult{StepName: "ok", Success: true}
}

// blockStep blocks on ctx.Done() so a submitted execution stays
// Running long enough for the test to observe and cancel it. Once the
// run is cancelled it sleeps briefly to simulate a step that takes time
// to honor cancellation (cleanup / drain), keeping the record in the
// CancelRequested window long enough for observers to see the
// Running -> CancelRequested -> Cancelled handshake deterministically.
type blockStep struct{}

func (blockStep) Describe() string { return "block" }
func (blockStep) Execute(ctx Context) StepResult {
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond)
	return StepResult{StepName: "block", Success: false, Error: ctx.Err()}
}

func newTestService(t *testing.T) (*ExecutionService, execution.Store) {
	t.Helper()
	store := execution.NewMemoryStore()
	executor := NewExecutor(NewNoopSink())
	rt := NewRuntime(executor, store)
	return rt.Service(), store
}

// waitStatus polls the store until rec.Status == want or the timeout elapses.
func waitStatus(t *testing.T, store execution.Store, id string, want execution.Status, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		rec, err := store.Get(id)
		if err == nil && rec.Status == want {
			return
		}
		if time.Now().After(deadline) {
			got := "<not-found>"
			if rec, e := store.Get(id); e == nil {
				got = string(rec.Status)
			}
			t.Fatalf("waitStatus: status for %s never became %s (last seen %s)", id, want, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newUserCtx() Context {
	return NewContext().WithUser(UserContext{ID: "u1", Name: "alice"}).Build()
}

// TestExecutionService_SubmitAsync verifies Submit returns a Planning
// record immediately (202 + id semantics) and the background goroutine
// drives it to Running then Success.
func TestExecutionService_SubmitAsync(t *testing.T) {
	svc, store := newTestService(t)
	ctx := newUserCtx()
	plan := &ExecutionPlan{OperationName: "demo.ok", Steps: []ExecutionStep{okStep{}}}

	rec, err := svc.Submit(ctx, plan)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if rec.Status != execution.StatusPlanning {
		t.Fatalf("submit status = %s, want Planning", rec.Status)
	}
	if rec.Version != 1 {
		t.Fatalf("submit version = %d, want 1", rec.Version)
	}
	// The id must be stamped onto the context so audit correlates.
	if rec.ID == "" {
		t.Fatalf("submit returned empty id")
	}

	waitStatus(t, store, rec.ID, execution.StatusSuccess, 2*time.Second)

	got, err := store.Get(rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserName != "alice" {
		t.Fatalf("UserName = %q, want alice", got.UserName)
	}
	if got.TraceID == "" {
		t.Fatalf("TraceID not propagated to record")
	}
	// After a terminal transition the optimistic-lock version bumped.
	if got.Version < 2 {
		t.Fatalf("version after terminal = %d, want >=2", got.Version)
	}
}

// TestExecutionService_CancelHandshake verifies the Running -> CancelRequested
// -> Cancelled handshake driven by Service.Cancel.
func TestExecutionService_CancelHandshake(t *testing.T) {
	svc, store := newTestService(t)
	ctx := newUserCtx()
	plan := &ExecutionPlan{OperationName: "demo.block", Steps: []ExecutionStep{blockStep{}}}

	rec, err := svc.Submit(ctx, plan)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitStatus(t, store, rec.ID, execution.StatusRunning, 2*time.Second)

	if err := svc.Cancel(rec.ID, "user request"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// Cancel must first mark CancelRequested (the in-flight signal).
	waitStatus(t, store, rec.ID, execution.StatusCancelRequested, 2*time.Second)
	// Then the run observes ctx.Done() and settles at Cancelled.
	waitStatus(t, store, rec.ID, execution.StatusCancelled, 2*time.Second)
}

// TestExecutionService_CancelUnknown is a no-op for an unknown id
// (the registry returns false; the store update is best-effort).
func TestExecutionService_CancelUnknown(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Cancel("nope", "x"); err != nil {
		t.Fatalf("cancel unknown: %v", err)
	}
}
