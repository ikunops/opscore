package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/YuDong999/opscore/internal/core/snapshot"
	"github.com/YuDong999/opscore/internal/storage"
)

func newTestDB(t *testing.T) *SQLiteStorage {
	t.Helper()
	s, err := NewSQLiteStorage(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLite_Operations(t *testing.T) {
	s := newTestDB(t)
	op, err := s.Operations().Save(storage.Operation{
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

func TestSQLite_RolesAndGrants(t *testing.T) {
	s := newTestDB(t)
	op, _ := s.Operations().Save(storage.Operation{Name: "firewall.rule.add", ResourceType: "firewall", ActionType: "add", Risk: "high", Source: "builtin", Enabled: true})
	role, err := s.Roles().Save(storage.Role{Name: "admin", Description: "superuser"})
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

func TestSQLite_AuditAndTasks(t *testing.T) {
	s := newTestDB(t)
	s.Audit().Append(storage.AuditEvent{Actor: "admin", Operation: "system.service.restart", Action: "execute", Result: "success"})
	s.Audit().Append(storage.AuditEvent{Actor: "admin", Operation: "system.service.restart", Action: "execute", Result: "failure"})
	list, _ := s.Audit().List(10)
	if len(list) != 2 || list[0].Result != "failure" {
		t.Fatalf("audit: %+v", list)
	}

	task, _ := s.Tasks().Save(storage.Task{Operation: "system.service.restart", Status: "running"})
	step, _ := s.Tasks().AppendStep(storage.TaskStep{TaskID: task.ID, StepName: "check", Command: "systemctl is-active nginx", Status: "running"})
	if err := s.Tasks().UpdateStep(step.ID, "success", "active", 12); err != nil {
		t.Fatalf("update step: %v", err)
	}
	steps, _ := s.Tasks().Steps(task.ID)
	if len(steps) != 1 || steps[0].Status != "success" || steps[0].DurationMs != 12 {
		t.Fatalf("steps: %+v", steps)
	}
}

func TestSQLite_CapabilitySnapshots(t *testing.T) {
	s := newTestDB(t)
	snap := &snapshot.CapabilitySnapshot{
		HostID: "h1",
		Items:  map[string]snapshot.CapabilityInfo{"systemd": {Name: "systemd", Available: true}},
	}
	h := snap.Hash()

	id, err := storage.PersistCapabilitySnapshot(snap, s.Snapshots())
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected non-zero snapshot id")
	}
	// Put is idempotent on hash.
	if id2, _ := s.Snapshots().Put(h, []byte("ignored"), storage.CapabilitySnapshotSchemaVersion); id2 != id {
		t.Fatalf("Put not idempotent: %d vs %d", id2, id)
	}
	p, err := s.Snapshots().GetByHash(h)
	if err != nil || len(p) == 0 {
		t.Fatalf("GetByHash: %v", err)
	}
	if _, err := s.Snapshots().GetByHash("nope"); err != storage.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestEnsure_AppliesBaselineAndIdempotent verifies the S5 migration
// framework: the baseline (v1 = full Schema) is applied once, recorded
// in schema_migrations, and a second Ensure is a no-op. A newly
// appended migration applies exactly once on the next Ensure.
func TestEnsure_AppliesBaselineAndIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "opscore.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := Ensure(db, Migrations); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	// baseline tables exist
	if _, err := db.Exec("SELECT 1 FROM operations LIMIT 1"); err != nil {
		t.Fatalf("operations table missing: %v", err)
	}
	if _, err := db.Exec("SELECT 1 FROM execution_records LIMIT 1"); err != nil {
		t.Fatalf("execution_records missing: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version=1").Scan(&n); err != nil {
		t.Fatalf("count v1: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row for v1, got %d", n)
	}

	// idempotent re-run: no error, still exactly the baselined rows
	// (v1 baseline + v2 plugin_registry_and_exec_origin + v3 plugin_identity
	// + v4 audit_revision_correlation). Counted against len(Migrations) so
	// appending the NEXT migration does not require editing this assertion.
	if err := Ensure(db, Migrations); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatalf("count all: %v", err)
	}
	if n != len(Migrations) {
		t.Fatalf("idempotent Ensure added rows: got %d, want %d", n, len(Migrations))
	}
	// the v2 plugin registry table + exec origin columns now exist
	if _, err := db.Exec("SELECT 1 FROM plugin_registry LIMIT 1"); err != nil {
		t.Fatalf("plugin_registry missing: %v", err)
	}
	if _, err := db.Exec("SELECT source, origin FROM execution_records LIMIT 1"); err != nil {
		t.Fatalf("exec origin columns missing: %v", err)
	}

	// the v4 audit columns now exist
	if _, err := db.Exec("SELECT revision, correlation_id FROM audit_events LIMIT 1"); err != nil {
		t.Fatalf("v4 audit columns missing: %v", err)
	}

	// a NEW migration appended applies exactly once. Its version is derived
	// from the tail rather than hardcoded, so this test keeps working as real
	// migrations land.
	probeVersion := Migrations[len(Migrations)-1].Version + 1
	extra := append([]Migration(nil), Migrations...)
	extra = append(extra, Migration{
		Version: probeVersion, Name: "probe",
		Up: `CREATE TABLE IF NOT EXISTS migrate_probe (id INTEGER PRIMARY KEY);`,
	})
	if err := Ensure(db, extra); err != nil {
		t.Fatalf("Ensure+probe: %v", err)
	}
	if _, err := db.Exec("SELECT 1 FROM migrate_probe LIMIT 1"); err != nil {
		t.Fatalf("probe table missing: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version=?", probeVersion).Scan(&n); err != nil {
		t.Fatalf("count v%d: %v", probeVersion, err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row for v%d, got %d", probeVersion, n)
	}
	// re-run with the probe already applied is a no-op
	if err := Ensure(db, extra); err != nil {
		t.Fatalf("Ensure+probe again: %v", err)
	}
}
