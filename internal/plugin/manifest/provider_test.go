package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// writeManifest creates <dir>/<key>/manifest.json with body.
func writeManifest(t *testing.T, dir, key, body string) {
	t.Helper()
	target := filepath.Join(dir, key)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", target, err)
	}
	if err := os.WriteFile(filepath.Join(target, "manifest.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest %q: %v", key, err)
	}
}

const mysqlManifest = `{
  "name": "mysql",
  "version": "1.4.2",
  "capabilities": ["linux"],
  "operations": [
    {"name": "plugin.mysql.backup.run", "resource": "backup", "action": "run", "risk": "high"}
  ]
}`

const redisManifest = `{
  "name": "redis",
  "version": "0.9.0",
  "operations": [
    {"name": "plugin.redis.cache.flush", "resource": "cache", "action": "flush", "risk": "medium"}
  ]
}`

func TestFileProvider_List(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "mysql", mysqlManifest)
	writeManifest(t, dir, "redis", redisManifest)
	// a sibling dir WITHOUT a manifest.json must be ignored
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := NewFileProvider(dir)
	keys, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("List = %v, want 2 plugin dirs", keys)
	}
	got := map[string]bool{}
	for _, k := range keys {
		got[k] = true
	}
	if !got["mysql"] || !got["redis"] {
		t.Fatalf("List missing expected keys: %v", keys)
	}
}

func TestFileProvider_Read(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "mysql", mysqlManifest)

	p := NewFileProvider(dir)
	m, err := p.Read("mysql")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.Name != "mysql" || m.Version != "1.4.2" {
		t.Fatalf("Read wrong manifest: name=%q version=%q", m.Name, m.Version)
	}
	if len(m.Operations) != 1 || m.Operations[0].Name != "plugin.mysql.backup.run" {
		t.Fatalf("Read wrong operations: %+v", m.Operations)
	}
}

func TestFileProvider_ReadUnknown(t *testing.T) {
	dir := t.TempDir()
	p := NewFileProvider(dir)
	if _, err := p.Read("ghost"); err == nil {
		t.Fatal("Read of unknown plugin should fail")
	}
}

func TestFileProvider_ReadTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	p := NewFileProvider(dir)
	if _, err := p.Read("../escape"); err == nil {
		t.Fatal("Read must reject path-traversal keys")
	}
	if _, err := p.Read("a/b"); err == nil {
		t.Fatal("Read must reject nested keys")
	}
}

func TestFileProvider_ReadInvalidManifest(t *testing.T) {
	dir := t.TempDir()
	// empty version + op name without the plugin prefix -> Validate fails
	bad := `{"name":"bad","version":"","operations":[{"name":"x","resource":"r","action":"a"}]}`
	writeManifest(t, dir, "bad", bad)
	p := NewFileProvider(dir)
	if _, err := p.Read("bad"); err == nil {
		t.Fatal("Read must reject an invalid manifest (Validate)")
	}
}
