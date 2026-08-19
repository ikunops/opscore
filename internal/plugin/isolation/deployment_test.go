package isolation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/YuDong999/opscore/ecosystem/packaging"
)

// TestDeploymentMapRoutesToHelper proves ruling (b): an operation listed in
// the host-side deployment map resolves to a process-isolated core.Handler
// that actually works, while an unmapped operation returns (nil, false) so the
// caller can fall back to the in-process handler. Manager never sees any of
// this (it only ever receives the returned core.Handler).
func TestDeploymentMapRoutesToHelper(t *testing.T) {
	m := DeploymentMap{
		"plugin.demo.echo": Deployment{
			Operation:          "plugin.demo.echo",
			Path:               os.Args[0],
			Args:               []string{"-test.run=TestHelperProcess"},
			Env:                append(os.Environ(), helperEnv+"=ok"),
			ExecTimeoutSeconds: 10,
		},
	}

	// Mapped op -> a working helper handler.
	h, ok := m.Handler("plugin.demo.echo")
	if !ok {
		t.Fatal("mapped operation must resolve to a handler")
	}
	plan, err := h.Plan(hostCtx(), nil)
	if err != nil {
		t.Fatalf("routed helper failed: %v", err)
	}
	if plan.OperationName != "plugin.demo.echo" {
		t.Errorf("routed handler lost its operation: %q", plan.OperationName)
	}

	// Unmapped op -> no handler (caller falls back to in-process).
	if _, ok := m.Handler("plugin.demo.unmapped"); ok {
		t.Error("unmapped operation must NOT resolve to a helper handler")
	}
}

// TestDeploymentMapLoadFromJSON proves the host can source deployments from a
// JSON file (stdlib only), keeping the package offline-friendly.
func TestDeploymentMapLoadFromJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy.json")
	doc := `{
  "plugin.demo.echo": {
    "operation": "plugin.demo.echo",
    "path": "/usr/lib/opscore/helpers/echo",
    "args": ["--verbose"],
    "execTimeoutSeconds": 15,
    "maxResponseMB": 4
  }
}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	m, err := LoadDeploymentMap(path)
	if err != nil {
		t.Fatalf("LoadDeploymentMap: %v", err)
	}
	d, ok := m["plugin.demo.echo"]
	if !ok {
		t.Fatal("deployment not parsed")
	}
	if d.Path != "/usr/lib/opscore/helpers/echo" {
		t.Errorf("path lost: %q", d.Path)
	}
	if len(d.Args) != 1 || d.Args[0] != "--verbose" {
		t.Errorf("args lost: %+v", d.Args)
	}
	if d.ExecTimeoutSeconds != 15 || d.MaxResponseMB != 4 {
		t.Errorf("tuning lost: %+v", d)
	}

	// And it still produces a usable handler with the tuning applied.
	h, ok := m.Handler("plugin.demo.echo")
	if !ok {
		t.Fatal("loaded deployment must resolve to a handler")
	}
	if h == nil {
		t.Error("handler must be non-nil")
	}
}

// TestAddFromPackageBridgesToDeploymentWithoutRuntime proves Phase 7.2 MUST-3:
// Unpack -> DeploymentMap -> Run does NOT modify the Runtime. The bridge turns
// a Package into a Deployment (pure host-side deployment policy); its
// Handler() yields a core.Handler, exactly what an in-process plugin emits, so
// Manager / Registry / Reload / Watcher stay unaware that a package exists.
func TestAddFromPackageBridgesToDeploymentWithoutRuntime(t *testing.T) {
	pkgDir := t.TempDir()

	// A dummy executable is enough to satisfy Validate (existence + checksum).
	exeRel := filepath.Join("bin", "demo-helper")
	exe := filepath.Join(pkgDir, exeRel)
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := mustSHA256(t, exe)

	doc := fmt.Sprintf(`{
  "name": "opscore-plugin-demo",
  "version": "1.0.0",
  "sdkVersion": "opscore.isolation/v1",
  "operations": {
    "plugin.demo.echo": {
      "executable": "bin/demo-helper",
      "args": ["--trace"],
      "env": ["OPSCORE_LOG=info"],
      "execTimeoutSeconds": 30,
      "maxResponseMB": 8
    }
  },
  "checksums": {
    "bin/demo-helper": %q
  }
}`, sum)
	if err := os.WriteFile(filepath.Join(pkgDir, "plugin.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	pkg, err := packaging.Load(pkgDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := pkg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	m := DeploymentMap{}
	if err := m.AddFromPackage("plugin.demo.echo", pkg); err != nil {
		t.Fatalf("AddFromPackage: %v", err)
	}
	d, ok := m["plugin.demo.echo"]
	if !ok {
		t.Fatal("operation not added to the map")
	}
	if d.Path != exe {
		t.Errorf("executable path not resolved against package dir: %q != %q", d.Path, exe)
	}
	if len(d.Args) != 1 || d.Args[0] != "--trace" {
		t.Errorf("args lost across the bridge: %+v", d.Args)
	}
	if d.ExecTimeoutSeconds != 30 || d.MaxResponseMB != 8 {
		t.Errorf("per-op tuning lost across the bridge: %+v", d)
	}

	// The bridge yields a core.Handler WITHOUT touching the Runtime — the host
	// would hand this straight to Manager, which cannot tell it from an
	// in-process handler (MUST-2 restated for the ecosystem layer).
	h, ok := m.Handler("plugin.demo.echo")
	if !ok || h == nil {
		t.Fatal("bridge must produce a usable core.Handler")
	}

	// An undeclared operation is an error, never a silent override.
	if err := m.AddFromPackage("plugin.demo.nope", pkg); err == nil {
		t.Error("AddFromPackage must fail for an undeclared operation")
	}
}

func mustSHA256(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
