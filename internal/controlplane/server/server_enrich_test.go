package server

import (
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/snapshot"
	"github.com/YuDong999/opscore/internal/storage"
)

// TestPersistCapabilitySnapshot_StoresPayload verifies the control-plane
// integration point content-addresses an observed capability snapshot into the
// CapabilitySnapshotStore keyed by its hash, so the frozen CapabilityHash in
// ExecutionRecord/AuditEvent resolves to a real payload (Phase 2.8 / ADR-009).
func TestPersistCapabilitySnapshot_StoresPayload(t *testing.T) {
	stor := storage.NewMemoryStorage()
	snap := &snapshot.CapabilitySnapshot{
		HostID: "test-host",
		Items: map[string]snapshot.CapabilityInfo{
			"systemd": {Name: "systemd", Available: true, Version: "249"},
		},
	}
	ctx := core.NewContext().WithCapabilitySnapshot(snap).Build()

	persistCapabilitySnapshot(ctx, stor, nil)

	id, err := stor.Snapshots().IDForHash(snap.Hash())
	if err != nil {
		t.Fatalf("snapshot not persisted: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero snapshot id")
	}
}

// TestPersistCapabilitySnapshot_NilSnapshotIsNoop verifies a context without an
// observed snapshot is a safe no-op (no panic, no stray row) — local runs and
// probe failures simply skip persistence.
func TestPersistCapabilitySnapshot_NilSnapshotIsNoop(t *testing.T) {
	stor := storage.NewMemoryStorage()
	ctx := core.NewContext().Build()
	persistCapabilitySnapshot(ctx, stor, nil) // must not panic
	if _, err := stor.Snapshots().IDForHash("absent"); err == nil {
		t.Fatal("expected ErrNotFound for absent hash")
	}
}

// TestPersistCapabilitySnapshot_Idempotent verifies re-persisting the same
// capability set reuses the existing row (content-addressed Put).
func TestPersistCapabilitySnapshot_Idempotent(t *testing.T) {
	stor := storage.NewMemoryStorage()
	snap := &snapshot.CapabilitySnapshot{
		HostID: "test-host",
		Items:  map[string]snapshot.CapabilityInfo{"systemd": {Name: "systemd", Available: true}},
	}
	ctx := core.NewContext().WithCapabilitySnapshot(snap).Build()
	persistCapabilitySnapshot(ctx, stor, nil)
	persistCapabilitySnapshot(ctx, stor, nil)

	id, err := stor.Snapshots().IDForHash(snap.Hash())
	if err != nil {
		t.Fatalf("snapshot not persisted: %v", err)
	}
	// A second Put must be a no-op: the id is stable, the payload retrievable.
	payload, perr := stor.Snapshots().GetByID(id)
	if perr != nil {
		t.Fatalf("GetByID: %v", perr)
	}
	if len(payload) == 0 {
		t.Fatal("expected non-empty payload retrievable by id")
	}
}
