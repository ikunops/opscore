package sqlite

import (
	"testing"

	"github.com/YuDong999/opscore/internal/storage"
)

// TestSQLite_AuditCapabilityHash verifies the Phase 2.8 weak-reference closure
// persists and reads back through the real SQLite schema (capability_hash
// column added to audit_events).
func TestSQLite_AuditCapabilityHash(t *testing.T) {
	s := newTestDB(t)
	if _, err := s.Audit().Append(storage.AuditEvent{
		Actor: "admin", Operation: "system.service.restart",
		Action: "execute", Result: "success", CapabilityHash: "deadbeef",
		ExecutionID: "exec-abc",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	events, err := s.Audit().List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].CapabilityHash != "deadbeef" {
		t.Fatalf("CapabilityHash = %q, want deadbeef", events[0].CapabilityHash)
	}
	if events[0].ExecutionID != "exec-abc" {
		t.Fatalf("ExecutionID = %q, want exec-abc", events[0].ExecutionID)
	}
}

// TestSQLite_AuditSnapshotSchemaVersion verifies the v0.1 SHOULD polish round
// trips through the real SQLite schema (snapshot_schema_version column).
func TestSQLite_AuditSnapshotSchemaVersion(t *testing.T) {
	s := newTestDB(t)
	if _, err := s.Audit().Append(storage.AuditEvent{
		Actor: "admin", Operation: "system.service.restart",
		Action: "execute", Result: "success",
		CapabilityHash: "deadbeef", SnapshotSchemaVersion: 3,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	events, err := s.Audit().List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if events[0].SnapshotSchemaVersion != 3 {
		t.Fatalf("SnapshotSchemaVersion = %d, want 3", events[0].SnapshotSchemaVersion)
	}
}
