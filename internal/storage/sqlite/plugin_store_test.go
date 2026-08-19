package sqlite

import (
	"database/sql"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/storage"
)

// TestPluginStore_RoundTrip exercises the Phase 3.0 MUST-3 durable
// plugin registry through the :memory: SQLite backend wired by NewSQLiteStorage
// (so the v2 migration that creates plugin_registry is implicit).
func TestPluginStore_RoundTrip(t *testing.T) {
	db, err := openTestDB(t)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s := &SQLiteStorage{db: db, plugin: &pluginStore{db: db}}

	want := storage.Plugin{
		ID:       "mysql@1.2.0",
		Name:     "mysql",
		Version:  "1.2.0",
		Status:   storage.PluginEnabled,
		Enabled:  true,
		LoadedAt: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
	}
	if err := s.Plugins().Upsert(want); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.Plugins().Get("mysql@1.2.0")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != want {
		t.Fatalf("get mismatch:\n got=%+v\nwant=%+v", got, want)
	}

	// flip enabled + status, re-upsert (ON CONFLICT update)
	want.Enabled = false
	want.Status = storage.PluginDisabled
	if err := s.Plugins().Upsert(want); err != nil {
		t.Fatalf("upsert#2: %v", err)
	}
	if err := s.Plugins().SetEnabled("mysql@1.2.0", true); err != nil {
		t.Fatalf("set enabled: %v", err)
	}
	if err := s.Plugins().SetStatus("mysql@1.2.0", storage.PluginUnloaded); err != nil {
		t.Fatalf("set status: %v", err)
	}
	got, _ = s.Plugins().Get("mysql@1.2.0")
	if !got.Enabled || got.Status != storage.PluginUnloaded {
		t.Fatalf("post-update mismatch: %+v", got)
	}

	// a second plugin so List returns 1+ entries
	if err := s.Plugins().Upsert(storage.Plugin{ID: "redis@0.9.0", Name: "redis", Version: "0.9.0", Status: storage.PluginLoaded}); err != nil {
		t.Fatalf("upsert redis: %v", err)
	}
	all, err := s.Plugins().List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("list len = %d, want 2", len(all))
	}

	// unknown id
	if _, err := s.Plugins().Get("nope"); err != storage.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.Plugins().SetEnabled("nope", true); err != storage.ErrNotFound {
		t.Fatalf("expected ErrNotFound on set, got %v", err)
	}
}

// openTestDB opens a fresh :memory: SQLite DB and runs the latest
// migrations, so plugin_registry (v2) exists for the test. :memory:
// avoids a Windows temp-file lock on cleanup.
func openTestDB(t *testing.T) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	if err := Ensure(db, Migrations); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
