package main

// T404 (ADR-092): the --storage kind is a CLOSED domain {memory, sqlite}. An
// unknown value must be refused, not silently served from memory: the typo'd
// deployment looks persistent (the password and jwt-secret gates treat any
// non-memory value as persistent) while its database file is never opened and
// its state vanishes on restart. The empirical probe before the fix:
//
//	opscore serve --storage sqllite ... -> "Control Plane listening ... storage=sqllite"
//	(the wrong value echoed back as accepted, memory-backed, process running).

import (
	"path/filepath"
	"testing"

	"github.com/YuDong999/opscore/internal/storage"
	"github.com/YuDong999/opscore/internal/storage/sqlite"
)

func TestT404OpenStorageRejectsUnknownKind(t *testing.T) {
	// memory: the explicit in-memory backend.
	st, err := openStorage("memory", "")
	if err != nil {
		t.Fatalf("T404: memory is a valid kind, got %v", err)
	}
	if _, ok := st.(*storage.MemoryStorage); !ok {
		t.Fatalf("T404: memory kind must yield the in-memory storage, got %T", st)
	}
	_ = st.Close()

	// sqlite: a real backend on a temp path.
	db := filepath.Join(t.TempDir(), "probe.db")
	st2, err := openStorage("sqlite", db)
	if err != nil {
		t.Fatalf("T404: sqlite is a valid kind, got %v", err)
	}
	if _, ok := st2.(*sqlite.SQLiteStorage); !ok {
		t.Fatalf("T404: sqlite kind must yield the sqlite storage, got %T", st2)
	}
	_ = st2.Close()

	// The regression shape: a typo must be an error, never a working memory store.
	if _, err := openStorage("sqllite", db); err == nil {
		t.Fatal("T404: an unknown storage kind must be refused, got a working store")
	}
	if _, err := openStorage("", db); err == nil {
		t.Fatal("T404: an empty storage kind must be refused")
	}
}
