package core

import (
	"testing"

	"github.com/YuDong999/opscore/internal/core/execution"
)

// fakeStep is a no-op ExecutionStep that always succeeds — used to exercise
// the Executor without touching the OS or network.
type fakeStep struct{ name string }

func (s *fakeStep) Describe() string { return s.name }
func (s *fakeStep) Execute(ctx Context) StepResult {
	return StepResult{StepName: s.name, StepID: "step-0", Index: 0, Success: true}
}

// captureSink records every emitted AuditEvent for assertions.
type captureSink struct{ events []AuditEvent }

func (s *captureSink) Emit(e AuditEvent) { s.events = append(s.events, e) }

// TestExecutor_RecordsCapabilityHash verifies the Phase 2.8 weak reference:
// the CapabilitySnapshot hash in effect for the run is frozen into both the
// ExecutionRecord and the AuditEvent, so each execution is traceable to the
// exact capabilities that drove it (ADR-009) without bloating the hot tables.
func TestExecutor_RecordsCapabilityHash(t *testing.T) {
	ctx := NewContext().Build() // auto-detects local capability -> non-nil snapshot
	var wantHash string
	if snap := ctx.CapabilitySnapshot(); snap != nil {
		wantHash = snap.Hash()
	}
	if wantHash == "" {
		t.Fatal("test context must have a non-empty local capability snapshot")
	}

	rec := execution.NewMemoryStore()
	sink := &captureSink{}
	ex := NewExecutor(sink)
	ex.SetRecorder(rec)

	plan := &ExecutionPlan{
		OperationName: "test.capability.hash",
		Permission:    Permission{ResourceType: "test", Action: "hash"},
		Risk:          RiskLow,
		Steps:         []ExecutionStep{&fakeStep{name: "ok"}},
	}

	ex.Execute(ctx, plan)

	// ExecutionRecord carries the hash.
	recs, err := rec.List(execution.Query{Limit: 10})
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 execution record, got %d", len(recs))
	}
	if recs[0].CapabilityHash != wantHash {
		t.Fatalf("ExecutionRecord.CapabilityHash = %q, want %q", recs[0].CapabilityHash, wantHash)
	}

	// AuditEvent carries the hash.
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(sink.events))
	}
	if sink.events[0].CapabilityHash != wantHash {
		t.Fatalf("AuditEvent.CapabilityHash = %q, want %q", sink.events[0].CapabilityHash, wantHash)
	}
}

// TestCapabilityHashOf_EmptyContext verifies the helper is nil-safe and returns
// "" when no snapshot has been observed (no capability discovery performed).
func TestCapabilityHashOf_EmptyContext(t *testing.T) {
	ctx := NewContext().WithCapability(CapabilityContext{}).Build() // autoDetect disabled, no snapshot
	if got := capabilityHashOf(ctx); got != "" {
		t.Fatalf("capabilityHashOf on a snapshot-less context = %q, want empty", got)
	}
}
