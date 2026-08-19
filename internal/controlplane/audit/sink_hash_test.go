package audit

import (
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/storage"
)

// TestStorageAuditSink_WritesCapabilityHash verifies the sink copies the
// kernel-emitted CapabilityHash into the persisted storage.AuditEvent, closing
// the weak reference from the audit row to the CapabilitySnapshotStore.
func TestStorageAuditSink_WritesCapabilityHash(t *testing.T) {
	stor := storage.NewMemoryStorage()
	sink := NewStorageAuditSink(stor, nil)
	sink.Emit(core.AuditEvent{
		OperationName:  "system.service.restart",
		User:           core.UserContext{Name: "admin"},
		CapabilityHash: "cafe1234",
		Result:         core.ExecutionResult{Success: true},
	})
	events, _ := stor.Audit().List(10)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].CapabilityHash != "cafe1234" {
		t.Fatalf("CapabilityHash = %q, want cafe1234", events[0].CapabilityHash)
	}
}
