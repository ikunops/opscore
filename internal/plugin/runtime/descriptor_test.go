package runtime

import (
	"testing"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

func TestNewDescriptor_IDAndState(t *testing.T) {
	m := &manifest.Manifest{Name: "mysql", Version: "1.4.2"}
	d := NewDescriptor(m)
	if d.ID != "mysql@1.4.2" {
		t.Fatalf("ID = %q, want mysql@1.4.2", d.ID)
	}
	if d.Source != "plugin:mysql@1.4.2" {
		t.Fatalf("Source = %q, want plugin:mysql@1.4.2", d.Source)
	}
	if d.State != StateDiscovered {
		t.Fatalf("State = %q, want discovered", d.State)
	}
	if d.IsFrozen() {
		t.Fatal("new descriptor must not be frozen yet")
	}
}

func TestDescriptor_Freeze(t *testing.T) {
	m := &manifest.Manifest{Name: "redis", Version: "0.9.0"}
	d := NewDescriptor(m)
	d.Freeze()
	if !d.IsFrozen() {
		t.Fatal("Freeze() should lock the definition")
	}
	// Freezing is idempotent and the ID/Source stay pinned.
	d.Freeze()
	if d.ID != "redis@0.9.0" || d.Source != "plugin:redis@0.9.0" {
		t.Fatalf("frozen definition mutated: ID=%q Source=%q", d.ID, d.Source)
	}
}
