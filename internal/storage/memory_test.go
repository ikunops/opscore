package storage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/core/snapshot"
)

func TestMemoryStorage_Operations(t *testing.T) {
	s := NewMemoryStorage()
	op, err := s.Operations().Save(Operation{
		Name: "system.service.restart", ResourceType: "service",
		ActionType: "restart", Risk: "medium", Source: "builtin", Enabled: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if op.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	got, err := s.Operations().GetByName("system.service.restart")
	if err != nil || got.Name != "system.service.restart" {
		t.Fatalf("get: %v %+v", err, got)
	}

	if err := s.Operations().SetEnabled("system.service.restart", false); err != nil {
		t.Fatalf("setenabled: %v", err)
	}
	enabled, _ := s.Operations().ListEnabled()
	if len(enabled) != 0 {
		t.Fatalf("expected 0 enabled, got %d", len(enabled))
	}
}

func TestMemoryStorage_RolesAndGrants(t *testing.T) {
	s := NewMemoryStorage()
	op, _ := s.Operations().Save(Operation{Name: "firewall.rule.add", ResourceType: "firewall", ActionType: "add", Risk: "high", Source: "builtin", Enabled: true})
	role, err := s.Roles().Save(Role{Name: "admin", Description: "superuser"})
	if err != nil {
		t.Fatalf("save role: %v", err)
	}
	if err := s.Roles().AddOperation(role.ID, op.ID); err != nil {
		t.Fatalf("add op: %v", err)
	}
	ops, err := s.Roles().Operations(role.ID)
	if err != nil || len(ops) != 1 || ops[0].Name != "firewall.rule.add" {
		t.Fatalf("role ops: %v %+v", err, ops)
	}
}

func TestMemoryStorage_Audit(t *testing.T) {
	s := NewMemoryStorage()
	s.Audit().Append(AuditEvent{Actor: "admin", Operation: "system.service.restart", Action: "execute", Result: "success"})
	s.Audit().Append(AuditEvent{Actor: "admin", Operation: "system.service.restart", Action: "execute", Result: "failure"})
	list, _ := s.Audit().List(10)
	if len(list) != 2 {
		t.Fatalf("expected 2 audit events, got %d", len(list))
	}
	// newest first
	if list[0].Result != "failure" {
		t.Fatalf("expected newest first, got %+v", list[0])
	}
}

func TestMemoryStorage_Tasks(t *testing.T) {
	s := NewMemoryStorage()
	task, _ := s.Tasks().Save(Task{Operation: "system.service.restart", Status: "running", CreatedAt: time.Now()})
	step, _ := s.Tasks().AppendStep(TaskStep{TaskID: task.ID, StepName: "check", Command: "systemctl is-active nginx", Status: "running"})
	if err := s.Tasks().UpdateStep(step.ID, "success", "active", 12); err != nil {
		t.Fatalf("update step: %v", err)
	}
	steps, _ := s.Tasks().Steps(task.ID)
	if len(steps) != 1 || steps[0].Status != "success" || steps[0].DurationMs != 12 {
		t.Fatalf("steps: %+v", steps)
	}
}

func TestMemoryStorage_CapabilitySnapshots(t *testing.T) {
	s := NewMemoryStorage()
	snap := &snapshot.CapabilitySnapshot{
		HostID: "h1",
		Items:  map[string]snapshot.CapabilityInfo{"systemd": {Name: "systemd", Available: true}},
	}
	h := snap.Hash()

	id, err := PersistCapabilitySnapshot(snap, s.Snapshots())
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected non-zero snapshot id")
	}
	// Put is idempotent on hash.
	if id2, _ := s.Snapshots().Put(h, []byte("ignored"), CapabilitySnapshotSchemaVersion); id2 != id {
		t.Fatalf("Put not idempotent: %d vs %d", id2, id)
	}
	p, err := s.Snapshots().GetByHash(h)
	if err != nil || len(p) == 0 {
		t.Fatalf("GetByHash: %v", err)
	}
	if got, _ := s.Snapshots().GetByID(id); len(got) == 0 {
		t.Fatal("GetByID empty")
	}
	if hid, err := s.Snapshots().IDForHash(h); err != nil || hid != id {
		t.Fatalf("IDForHash = %d err %v", hid, err)
	}
	if _, err := s.Snapshots().GetByHash("nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestPersistCapabilitySnapshot_Envelope locks the M4 contract: the store holds
// a versioned JSON envelope, never a bare opaque BLOB. An audit viewer reads
// SchemaVersion first and can migrate older payloads.
func TestPersistCapabilitySnapshot_Envelope(t *testing.T) {
	s := NewMemoryStorage()
	snap := &snapshot.CapabilitySnapshot{
		HostID: "h1",
		Items:  map[string]snapshot.CapabilityInfo{"systemd": {Name: "systemd", Available: true}},
	}
	if _, err := PersistCapabilitySnapshot(snap, s.Snapshots()); err != nil {
		t.Fatal(err)
	}
	raw, err := s.Snapshots().GetByHash(snap.Hash())
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		SchemaVersion int             `json:"schema_version"`
		Payload       json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("payload is not a JSON envelope: %v", err)
	}
	if env.SchemaVersion != CapabilitySnapshotSchemaVersion {
		t.Fatalf("envelope schema_version = %d, want %d", env.SchemaVersion, CapabilitySnapshotSchemaVersion)
	}
	if len(env.Payload) == 0 {
		t.Fatal("envelope payload empty")
	}
}
