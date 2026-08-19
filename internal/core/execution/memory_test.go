package execution

import "testing"

func TestMemoryStore_CreateGetUpdateStatus(t *testing.T) {
	s := NewMemoryStore()
	id := NewExecutionID()
	if err := s.Create(ExecutionRecord{ID: id, Operation: "x.restart", Status: StatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateStatus(id, StatusSuccess); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusSuccess {
		t.Fatalf("status = %s, want SUCCESS", rec.Status)
	}
	if rec.StartedAt == nil {
		t.Fatal("expected StartedAt to be set on RUNNING")
	}
	if rec.FinishedAt == nil {
		t.Fatal("expected FinishedAt to be set on SUCCESS")
	}

	if _, err := s.Get("does-not-exist"); err != ErrNotFound {
		t.Fatalf("Get unknown: got %v, want ErrNotFound", err)
	}
	if err := s.UpdateStatus("does-not-exist", StatusFailed); err != ErrNotFound {
		t.Fatalf("UpdateStatus unknown: got %v, want ErrNotFound", err)
	}
}

func TestMemoryStore_UpdateStepUpsert(t *testing.T) {
	s := NewMemoryStore()
	id := NewExecutionID()
	s.Create(ExecutionRecord{ID: id, Operation: "x", Status: StatusRunning})

	s.UpdateStep(id, ExecutionStepRecord{ID: "s1", Name: "a", Index: 0, Status: StepSuccess})
	s.UpdateStep(id, ExecutionStepRecord{ID: "s2", Name: "b", Index: 1, Status: StepFailed})
	// upsert same id must replace, not append
	s.UpdateStep(id, ExecutionStepRecord{ID: "s1", Name: "a-updated", Index: 0, Status: StepSuccess})

	rec, _ := s.Get(id)
	if len(rec.Steps) != 2 {
		t.Fatalf("len(steps) = %d, want 2", len(rec.Steps))
	}
	if rec.Steps[0].Name != "a-updated" {
		t.Fatalf("upsert failed: step[0].Name = %q", rec.Steps[0].Name)
	}
}

func TestMemoryStore_ListFilter(t *testing.T) {
	s := NewMemoryStore()
	s.Create(ExecutionRecord{ID: NewExecutionID(), Operation: "a.restart", Status: StatusSuccess})
	s.Create(ExecutionRecord{ID: NewExecutionID(), Operation: "b.info", Status: StatusFailed})
	s.Create(ExecutionRecord{ID: NewExecutionID(), Operation: "a.restart", Status: StatusSuccess})

	got, _ := s.List(Query{Operation: "a.restart"})
	if len(got) != 2 {
		t.Fatalf("filter by operation: got %d, want 2", len(got))
	}
	got, _ = s.List(Query{Status: StatusFailed})
	if len(got) != 1 {
		t.Fatalf("filter by status: got %d, want 1", len(got))
	}
	got, _ = s.List(Query{Limit: 1})
	if len(got) != 1 {
		t.Fatalf("limit: got %d, want 1", len(got))
	}
}

// TestMemoryStore_Transition verifies the S3 CAS semantics: a transition
// only applies when the record is currently in 'from', and a mismatch
// (e.g. a concurrent Cancel that already moved it) returns ErrConflict
// instead of clobbering the terminal state.
func TestMemoryStore_Transition(t *testing.T) {
	s := NewMemoryStore()
	id := NewExecutionID()
	// Mirrors ExecutionService.Submit, which stamps Version:1 on create.
	s.Create(ExecutionRecord{ID: id, Operation: "x", Status: StatusRunning, Version: 1})

	if err := s.Transition(id, StatusRunning, StatusSuccess); err != nil {
		t.Fatalf("RUNNING->SUCCESS: %v", err)
	}
	rec, _ := s.Get(id)
	if rec.Status != StatusSuccess || rec.Version != 2 {
		t.Fatalf("after transition: status=%s version=%d, want SUCCESS/2", rec.Status, rec.Version)
	}
	if rec.FinishedAt == nil {
		t.Fatal("FinishedAt should be set on terminal transition")
	}

	// Now a late Cancel tries RUNNING->CANCEL_REQUESTED: status is
	// already SUCCESS, so this MUST conflict (never overwrite terminal).
	if err := s.Transition(id, StatusRunning, StatusCancelRequested); err != ErrConflict {
		t.Fatalf("late cancel: got %v, want ErrConflict", err)
	}
	rec, _ = s.Get(id)
	if rec.Status != StatusSuccess {
		t.Fatalf("late cancel clobbered status to %s", rec.Status)
	}

	// Unknown id -> ErrNotFound (not ErrConflict).
	if err := s.Transition("nope", StatusRunning, StatusSuccess); err != ErrNotFound {
		t.Fatalf("unknown: got %v, want ErrNotFound", err)
	}
}
