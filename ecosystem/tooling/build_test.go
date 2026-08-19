package tooling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/ecosystem/packaging"
)

// TestToolingImportsOnlyAllowed mechanically pins the Phase 7.3 layering rule:
// tooling must stay OUT of internal/ and may only lean on the standard library
// plus ecosystem/sdk and ecosystem/packaging. A forbidden import turns this test
// red instead of the design silently eroding.
func TestToolingImportsOnlyAllowed(t *testing.T) {
	const forbidden = "internal/"
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read .: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(p, forbidden) {
				t.Errorf("%s imports %q — forbidden (tooling must stay out of internal/)", e.Name(), p)
			}
		}
	}
}

// stubBuild is a hermetic BuildFunc: it writes a dummy executable instead of
// shelling out to `go build`, keeping the suite offline and fast.
func stubBuild(_ *testing.T) BuildFunc {
	return func(ctx context.Context, op OpSpec, outPath string) error {
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(outPath, []byte("#!/bin/sh\n"), 0o755)
	}
}

func shaOf(t *testing.T, path string) string {
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

// TestBuildProducesVerifiedPackage proves the full chain: build -> checksum ->
// write plugin.json -> write release.json -> verify. The produced directory
// must load + validate as a packaging.Package, and the checksums must be real.
func TestBuildProducesVerifiedPackage(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "dist")

	spec := BuildSpec{
		Name:        "opscore-plugin-demo",
		Version:     "1.0.0",
		Description: "demo",
		OutDir:      out,
		Ops: map[string]OpSpec{
			"plugin.demo.echo": {ExecPath: "bin/helper"},
		},
	}
	if err := Build(context.Background(), spec, stubBuild(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "plugin.json")); err != nil {
		t.Fatalf("plugin.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "release.json")); err != nil {
		t.Fatalf("release.json missing: %v", err)
	}

	// The produced package loads + validates (the same check the host runs).
	pkg, err := packaging.Load(out)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := pkg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if pkg.SDKVersion != "opscore.isolation/v1" {
		t.Errorf("sdkVersion not set from SDK: %q", pkg.SDKVersion)
	}

	// Checksum in plugin.json must match the on-disk executable.
	want := shaOf(t, filepath.Join(out, "bin", "helper"))
	if got := pkg.Checksums["bin/helper"]; !strings.EqualFold(got, want) {
		t.Errorf("checksum mismatch: want %s got %s", want, got)
	}

	// release.json carries the plugin.json checksum (Release Tool concern) —
	// the hash of the whole package manifest, not of any single executable.
	relRaw, err := os.ReadFile(filepath.Join(out, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rel map[string]any
	if err := json.Unmarshal(relRaw, &rel); err != nil {
		t.Fatal(err)
	}
	pjWant := shaOf(t, filepath.Join(out, "plugin.json"))
	if rel["checksum"] != pjWant {
		t.Errorf("release.json checksum should equal plugin.json checksum: got %v want %s", rel["checksum"], pjWant)
	}
	if rel["version"] != "1.0.0" || rel["sdkVersion"] != "opscore.isolation/v1" {
		t.Errorf("release.json missing expected fields: %+v", rel)
	}
}

// TestBuildFailsWhenBuildErrors proves a build failure is propagated, never
// silently swallowed into a broken package.
func TestBuildFailsWhenBuildErrors(t *testing.T) {
	dir := t.TempDir()
	boom := func(ctx context.Context, op OpSpec, outPath string) error {
		return fmt.Errorf("boom")
	}
	spec := BuildSpec{
		Name:    "x",
		Version: "1.0.0",
		OutDir:  filepath.Join(dir, "dist"),
		Ops:     map[string]OpSpec{"a.b": {ExecPath: "bin/h"}},
	}
	if err := Build(context.Background(), spec, boom); err == nil {
		t.Fatal("Build must propagate build errors")
	}
}
