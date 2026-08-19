package dist

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/YuDong999/opscore/ecosystem/oci"
)

// TestDistImportsOnlyEcosystem pins the layering: dist must stay in the
// ecosystem layer and never reach into the Runtime Core (Round 36 MUST-3).
func TestDistImportsOnlyEcosystem(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return filepath.Ext(fi.Name()) == ".go" && !hasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse dir: %v", err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := unquote(imp.Path.Value)
				if contains(path, "internal/") {
					t.Errorf("dist must not import %q (Round 36 MUST-3)", path)
				}
			}
		}
	}
}

func TestLocalTransportRoundTrip(t *testing.T) {
	_, files := makePackageDir(t)
	store := t.TempDir()
	tr := &LocalTransport{Root: store}
	c := NewClient(tr)
	ref := oci.Tag{Name: "demo", Version: "1.0.0"}

	if err := c.PushPackage(context.Background(), ref, files); err != nil {
		t.Fatalf("push: %v", err)
	}

	outDir := t.TempDir()
	pkg, err := c.PullPackage(context.Background(), ref, outDir)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pkg.Name != "demo" || pkg.Version != "1.0.0" {
		t.Fatalf("unexpected package identity: %+v", pkg)
	}
	if err := pkg.Validate(); err != nil {
		t.Fatalf("pulled package invalid: %v", err)
	}
	hb, err := os.ReadFile(filepath.Join(outDir, "bin", "helper"))
	if err != nil || len(hb) == 0 {
		t.Fatalf("bin/helper missing/empty after pull: %v", err)
	}
	res, err := os.ReadFile(filepath.Join(outDir, "resources", "help.txt"))
	if err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if string(res) != "help content" {
		t.Fatalf("resource mismatch: %q", res)
	}
}

func TestResolveDigestAndFetch(t *testing.T) {
	srcDir, files := makePackageDir(t)
	store := t.TempDir()
	tr := &LocalTransport{Root: store}
	c := NewClient(tr)
	ref := oci.Tag{Name: "demo", Version: "1.0.0"}
	if err := c.PushPackage(context.Background(), ref, files); err != nil {
		t.Fatal(err)
	}

	d, err := tr.ResolveDigest(context.Background(), ref)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if d.IsZero() {
		t.Fatal("resolved digest is zero")
	}
	// A digest-pinned ref must resolve to the same manifest digest.
	dref := oci.Tag{Name: "demo", Digest: d.String()}
	d2, err := tr.ResolveDigest(context.Background(), dref)
	if err != nil {
		t.Fatal(err)
	}
	if d2 != d {
		t.Fatalf("digest ref mismatch: %v vs %v", d2, d)
	}

	m, err := tr.FetchManifest(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("fetched manifest invalid: %v", err)
	}
	cfgDigest, _ := oci.ParseDigest(m.Config.Digest)
	cfgBytes, err := tr.FetchBlob(context.Background(), ref, cfgDigest)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(filepath.Join(srcDir, "plugin.json"))
	if string(cfgBytes) != string(want) {
		t.Fatalf("config blob mismatch:\n got %q\nwant %q", cfgBytes, want)
	}
}

// --- test helpers ---

func makePackageDir(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	pluginJSON := `{
  "name": "demo",
  "version": "1.0.0",
  "sdkVersion": "opscore.isolation/v1",
  "description": "demo plugin",
  "operations": { "run": { "executable": "bin/helper" } }
}`
	writeFile(t, filepath.Join(dir, "plugin.json"), pluginJSON)
	writeFile(t, filepath.Join(dir, "bin", "helper"), "#!/bin/sh\necho hi\n")
	writeFile(t, filepath.Join(dir, "resources", "help.txt"), "help content")
	files := map[string]string{
		"plugin.json":        filepath.Join(dir, "plugin.json"),
		"bin/helper":         filepath.Join(dir, "bin", "helper"),
		"resources/help.txt": filepath.Join(dir, "resources", "help.txt"),
	}
	return dir, files
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
