package hostregistry

import (
	"testing"

	"github.com/YuDong999/opscore/internal/core"
)

func sampleStore(t *testing.T) *MemoryHostStore {
	t.Helper()
	s := NewMemoryHostStore()
	if _, err := s.Save(Host{
		Name:   "web-01",
		Target: core.TargetHost{Address: "10.0.0.1", Port: 22, User: "deploy"},
		Groups: []string{"web", "prod"},
	}); err != nil {
		t.Fatalf("save web-01: %v", err)
	}
	if _, err := s.Save(Host{
		Name:   "web-02",
		Target: core.TargetHost{Address: "10.0.0.2", Port: 22, User: "deploy"},
		Groups: []string{"web", "prod"},
	}); err != nil {
		t.Fatalf("save web-02: %v", err)
	}
	if _, err := s.Save(Host{
		Name:   "db-01",
		Target: core.TargetHost{Address: "10.0.0.9", Port: 22, User: "dba"},
		Groups: []string{"db"},
	}); err != nil {
		t.Fatalf("save db-01: %v", err)
	}
	return s
}

func TestMemoryHostStore_CRUD(t *testing.T) {
	s := NewMemoryHostStore()
	if _, err := s.GetByName("nope"); err != ErrHostNotFound {
		t.Fatalf("expected ErrHostNotFound, got %v", err)
	}
	h, err := s.Save(Host{Name: "a", Target: core.TargetHost{Address: "1.2.3.4"}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if h.Name != "a" {
		t.Fatalf("round-trip name mismatch: %q", h.Name)
	}
	// overwrite is allowed
	if _, err := s.Save(Host{Name: "a", Target: core.TargetHost{Address: "5.6.7.8"}}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, err := s.GetByName("a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Target.Address != "5.6.7.8" {
		t.Fatalf("overwrite did not apply: %q", got.Target.Address)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 host after overwrite, got %d", len(list))
	}
	if err := s.Delete("a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Delete("a"); err != ErrHostNotFound {
		t.Fatalf("expected ErrHostNotFound on double delete, got %v", err)
	}
}

func TestMemoryHostStore_ListSorted(t *testing.T) {
	s := NewMemoryHostStore()
	for _, n := range []string{"c", "a", "b"} {
		if _, err := s.Save(Host{Name: n}); err != nil {
			t.Fatalf("save %s: %v", n, err)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"a", "b", "c"}
	for i, h := range list {
		if h.Name != want[i] {
			t.Fatalf("list not sorted: got %s at %d, want %s", h.Name, i, want[i])
		}
	}
}

func TestResolveTarget(t *testing.T) {
	s := sampleStore(t)
	tg, err := ResolveTarget(s, "web-01")
	if err != nil {
		t.Fatalf("resolve web-01: %v", err)
	}
	if tg.Address != "10.0.0.1" {
		t.Fatalf("resolved address mismatch: %q", tg.Address)
	}
	if _, err := ResolveTarget(s, "missing"); err != ErrHostNotFound {
		t.Fatalf("expected ErrHostNotFound, got %v", err)
	}
	if _, err := ResolveTarget(nil, "web-01"); err == nil {
		t.Fatalf("expected error for nil store")
	}
}

func TestResolveGroup(t *testing.T) {
	s := sampleStore(t)
	web, err := ResolveGroup(s, "web")
	if err != nil {
		t.Fatalf("resolve group web: %v", err)
	}
	if len(web) != 2 {
		t.Fatalf("expected 2 hosts in web group, got %d", len(web))
	}
	db, err := ResolveGroup(s, "db")
	if err != nil {
		t.Fatalf("resolve group db: %v", err)
	}
	if len(db) != 1 || db[0].Address != "10.0.0.9" {
		t.Fatalf("db group wrong: %+v", db)
	}
	empty, err := ResolveGroup(s, "nope")
	if err != nil {
		t.Fatalf("resolve empty group: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected 0 hosts for unknown group, got %d", len(empty))
	}
}

func TestHost_ToTarget(t *testing.T) {
	h := Host{Name: "x", Target: core.TargetHost{Address: "9.9.9.9", Port: 2022}}
	if h.ToTarget().Address != "9.9.9.9" || h.ToTarget().Port != 2022 {
		t.Fatalf("ToTarget round-trip failed: %+v", h.ToTarget())
	}
}
