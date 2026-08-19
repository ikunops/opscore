package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

// writeManifest writes a manifest.json under root/key/ and returns the key.
func writeManifest(t *testing.T, root, key string, m manifest.Manifest) string {
	t.Helper()
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return key
}

// validManifest builds a plugin manifest that passes manifest.Validate():
// every operation name carries the "plugin.<Name>." namespace prefix.
func validManifest(name, version string) manifest.Manifest {
	return manifest.Manifest{
		Name:    name,
		Version: version,
		Operations: []manifest.OperationDecl{
			{Name: "plugin." + name + ".ping", Resource: "ping", Action: "ping", Risk: "low"},
		},
	}
}

// TestFileLoader_DiscoverIsolatesBadPlugin verifies MUST-1: a malformed
// manifest in one subdirectory does not abort discovery of the others.
func TestFileLoader_DiscoverIsolatesBadPlugin(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "mysql", validManifest("mysql", "1.0.0"))
	// redis: invalid — operation name lacks the required "plugin.redis."
	// namespace prefix, so manifest.Validate fails at the Provider boundary.
	writeManifest(t, root, "redis", manifest.Manifest{
		Name:    "redis",
		Version: "1.0.0",
		Operations: []manifest.OperationDecl{
			{Name: "redis.ping", Resource: "ping", Action: "ping", Risk: "low"},
		},
	})

	loader := NewFileLoader(manifest.NewFileProvider(root))
	descs := loader.Discover(context.Background())

	if len(descs) != 1 {
		t.Fatalf("want 1 discovered, got %d: %+v", len(descs), descs)
	}
	if descs[0].ID != "mysql@1.0.0" {
		t.Fatalf("want mysql@1.0.0, got %q", descs[0].ID)
	}
	if len(loader.LoadErrors()) != 1 {
		t.Fatalf("want 1 isolated error, got %d: %+v", len(loader.LoadErrors()), loader.LoadErrors())
	}
}

// TestFileLoader_DuplicateIDRejected verifies MUST-2: two subdirectories that
// resolve to the same plugin ID (name@version) are BOTH rejected.
func TestFileLoader_DuplicateIDRejected(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "mysql", validManifest("mysql", "1.0.0"))
	writeManifest(t, root, "mysql2", validManifest("mysql", "1.0.0")) // same ID

	loader := NewFileLoader(manifest.NewFileProvider(root))
	descs := loader.Discover(context.Background())
	if len(descs) != 0 {
		t.Fatalf("want 0 discovered (duplicate ID rejected), got %d: %+v", len(descs), descs)
	}
	if len(loader.LoadErrors()) != 1 {
		t.Fatalf("want 1 conflict error, got %d: %+v", len(loader.LoadErrors()), loader.LoadErrors())
	}
}

// TestFileLoader_DifferentVersionsCoexist verifies two DIFFERENT versions of
// the same name do NOT collide on ID (mysql@1.0.0 vs mysql@2.0.0).
//
// NOTE: the Manager currently keys its runtime map by Name, not ID, so
// actually loading both would still overwrite in m.descs/modules. True
// end-to-end coexistence requires the Manager to key by ID — a follow-up
// tracked for Phase 3.4.x. This test only asserts the Loader-level ID
// uniqueness passes (no false conflict).
func TestFileLoader_DifferentVersionsCoexist(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "mysql-1", validManifest("mysql", "1.0.0"))
	writeManifest(t, root, "mysql-2", validManifest("mysql", "2.0.0"))

	loader := NewFileLoader(manifest.NewFileProvider(root))
	descs := loader.Discover(context.Background())
	if len(descs) != 2 {
		t.Fatalf("want 2 discovered, got %d: %+v", len(descs), descs)
	}
	if len(loader.LoadErrors()) != 0 {
		t.Fatalf("want 0 errors, got %v", loader.LoadErrors())
	}
}

// TestFileLoader_LoadReturnsModule verifies Load turns a discovered
// descriptor into a Loaded+Frozen module (loop closes without .so).
func TestFileLoader_LoadReturnsModule(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "mysql", validManifest("mysql", "1.0.0"))
	loader := NewFileLoader(manifest.NewFileProvider(root))
	descs := loader.Discover(context.Background())
	if len(descs) != 1 {
		t.Fatalf("precondition: want 1 discovered, got %d", len(descs))
	}
	mod, err := loader.Load(descs[0])
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if mod.Descriptor().ID != "mysql@1.0.0" {
		t.Fatalf("loaded module id = %q", mod.Descriptor().ID)
	}
	d := mod.Descriptor()
	if !d.IsFrozen() {
		t.Fatal("loaded module descriptor must be Frozen")
	}
}
