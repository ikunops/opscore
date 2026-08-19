package sqlite

import (
	"database/sql"
	"testing"

	"github.com/YuDong999/opscore/internal/core/execution"
)

func openExecutionStore(t *testing.T) *ExecutionStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Use Ensure (not the raw Schema constant) so the v2 migration that
	// adds source/origin columns to execution_records is applied — exactly
	// what NewSQLiteStorage does in production.
	if err := Ensure(db, Migrations); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewExecutionStore(db)
}

func TestExecutionStore_CreateGetUpdateStatus(t *testing.T) {
	s := openExecutionStore(t)
	id := "exe-test-1"
	rec := execution.ExecutionRecord{
		ID:         id,
		Operation:  "svc.restart",
		Permission: "service.restart",
		Risk:       "medium",
		Status:     execution.StatusRunning,
		UserName:   "alice",
		Target:     "10.0.0.1",
	}
	if err := s.Create(rec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != execution.StatusRunning {
		t.Fatalf("status = %s, want RUNNING", got.Status)
	}
	if got.StartedAt == nil {
		t.Fatalf("StartedAt should be set on Create(RUNNING)")
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt should be defaulted")
	}
	if err := s.UpdateStatus(id, execution.StatusSuccess); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _ = s.Get(id)
	if got.Status != execution.StatusSuccess {
		t.Fatalf("status = %s, want SUCCESS", got.Status)
	}
	if got.FinishedAt == nil {
		t.Fatalf("FinishedAt should be set on terminal status")
	}
	// Unknown id -> ErrNotFound
	if _, err := s.Get("nope"); err != execution.ErrNotFound {
		t.Fatalf("Get unknown: got %v, want ErrNotFound", err)
	}
	if err := s.UpdateStatus("nope", execution.StatusFailed); err != execution.ErrNotFound {
		t.Fatalf("UpdateStatus unknown: got %v, want ErrNotFound", err)
	}
}

func TestExecutionStore_UpdateStepUpsert(t *testing.T) {
	s := openExecutionStore(t)
	id := "exe-steps-1"
	if err := s.Create(execution.ExecutionRecord{ID: id, Operation: "batch", Status: execution.StatusRunning}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.UpdateStep(id, execution.ExecutionStepRecord{ID: "s1", Name: "a", Index: 0, Status: execution.StepSuccess})
	s.UpdateStep(id, execution.ExecutionStepRecord{ID: "s2", Name: "b", Index: 1, Status: execution.StepFailed})
	s.UpdateStep(id, execution.ExecutionStepRecord{ID: "s1", Name: "a-updated", Index: 0, Status: execution.StepSuccess})

	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("steps len = %d, want 2 (upsert by ID)", len(got.Steps))
	}
	var s1 *execution.ExecutionStepRecord
	for i := range got.Steps {
		if got.Steps[i].ID == "s1" {
			s1 = &got.Steps[i]
		}
	}
	if s1 == nil || s1.Name != "a-updated" {
		t.Fatalf("upsert did not replace s1 (got %+v)", s1)
	}
	// UpdateStep on unknown execution -> ErrNotFound
	if err := s.UpdateStep("ghost", execution.ExecutionStepRecord{ID: "x"}); err != execution.ErrNotFound {
		t.Fatalf("UpdateStep unknown: got %v, want ErrNotFound", err)
	}
}

func TestExecutionStore_ListFilter(t *testing.T) {
	s := openExecutionStore(t)
	mk := func(id, op string, st execution.Status) {
		if err := s.Create(execution.ExecutionRecord{ID: id, Operation: op, Status: st}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	mk("e1", "a.restart", execution.StatusSuccess)
	mk("e2", "a.restart", execution.StatusFailed)
	mk("e3", "b.deploy", execution.StatusRunning)

	got, _ := s.List(execution.Query{Operation: "a.restart"})
	if len(got) != 2 {
		t.Fatalf("Operation filter: got %d, want 2", len(got))
	}
	got, _ = s.List(execution.Query{Status: execution.StatusFailed})
	if len(got) != 1 || got[0].ID != "e2" {
		t.Fatalf("Status filter: got %+v, want [e2]", got)
	}
	got, _ = s.List(execution.Query{Limit: 1})
	if len(got) != 1 {
		t.Fatalf("Limit: got %d, want 1", len(got))
	}
	// Round-trip of a timestamp survives a read (RFC3339).
	got, _ = s.List(execution.Query{})
	for _, r := range got {
		if r.CreatedAt.IsZero() {
			t.Fatalf("CreatedAt round-trip zero for %s", r.ID)
		}
	}
}

// TestExecutionStore_Transition verifies the durable CAS: a transition
// only applies when the row is currently in 'from' (AND its version is
// unchanged). A concurrent move yields a 0-rows UPDATE -> ErrConflict.
func TestExecutionStore_Transition(t *testing.T) {
	s := openExecutionStore(t)
	id := "exe-cas-1"
	if err := s.Create(execution.ExecutionRecord{ID: id, Operation: "x", Status: execution.StatusRunning}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// RUNNING -> SUCCESS (version 1 -> 2).
	if err := s.Transition(id, execution.StatusRunning, execution.StatusSuccess); err != nil {
		t.Fatalf("RUNNING->SUCCESS: %v", err)
	}
	rec, _ := s.Get(id)
	if rec.Status != execution.StatusSuccess || rec.Version != 2 {
		t.Fatalf("after transition: status=%s version=%d, want SUCCESS/2", rec.Status, rec.Version)
	}
	if rec.FinishedAt == nil {
		t.Fatal("FinishedAt should be set on terminal transition")
	}

	// A late Cancel tries RUNNING->CANCEL_REQUESTED: the row is now
	// SUCCESS, so the WHERE clauses match 0 rows -> ErrConflict, and
	// the terminal state is preserved.
	if err := s.Transition(id, execution.StatusRunning, execution.StatusCancelRequested); err != execution.ErrConflict {
		t.Fatalf("late cancel: got %v, want ErrConflict", err)
	}
	rec, _ = s.Get(id)
	if rec.Status != execution.StatusSuccess {
		t.Fatalf("late cancel clobbered status to %s", rec.Status)
	}

	// Unknown id -> ErrNotFound.
	if err := s.Transition("ghost", execution.StatusRunning, execution.StatusSuccess); err != execution.ErrNotFound {
		t.Fatalf("unknown: got %v, want ErrNotFound", err)
	}
}

// TestExecutionStore_SourceOriginRoundTrip verifies Phase 3.0 MUST-1 +
// SHOULD: the operation Source ("builtin"/"plugin:mysql") and the
// execution Origin ("API"/"CLI") are persisted on the record and read
// back unchanged.
func TestExecutionStore_SourceOriginRoundTrip(t *testing.T) {
	s := openExecutionStore(t)
	id := "exe-src-1"
	rec := execution.ExecutionRecord{
		ID:         id,
		Operation:  "plugin.mysql.backup.execute",
		Permission: "backup.execute",
		Risk:       "low",
		Status:     execution.StatusRunning,
		UserName:   "alice",
		Source:     "plugin:mysql",
		Origin:     "API",
	}
	if err := s.Create(rec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Source != "plugin:mysql" {
		t.Fatalf("Source = %q, want plugin:mysql", got.Source)
	}
	if got.Origin != "API" {
		t.Fatalf("Origin = %q, want API", got.Origin)
	}
}
