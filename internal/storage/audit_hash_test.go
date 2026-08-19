package storage

import "testing"

// TestMemoryStorage_AuditCapabilityHash verifies the Phase 2.8 weak-reference
// closure at the storage layer: the CapabilityHash carried by an audit event
// survives a round-trip through the MemoryAuditStore, so an audit viewer can
// resolve it back to a CapabilitySnapshot payload.
func TestMemoryStorage_AuditCapabilityHash(t *testing.T) {
	s := NewMemoryStorage()
	if _, err := s.Audit().Append(AuditEvent{
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

// TestMemoryStorage_AuditSnapshotSchemaVersion verifies the v0.1 SHOULD polish:
// the SnapshotSchemaVersion that an AuditEvent carries (paired with its
// CapabilityHash) survives a round-trip so an audit viewer can decide migration
// without resolving the snapshot.
func TestMemoryStorage_AuditSnapshotSchemaVersion(t *testing.T) {
	s := NewMemoryStorage()
	if _, err := s.Audit().Append(AuditEvent{
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
