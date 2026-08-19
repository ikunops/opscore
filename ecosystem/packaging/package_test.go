package packaging

import (
	"crypto/sha256"
	"encoding/hex"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPackagingImportsOnlyStdlibAndSDK mechanically pins the Phase 7.2
// layering rule: packaging must stay OUT of internal/ and may only lean on the
// standard library plus the ecosystem SDK. If someone adds a
// "github.com/.../internal/..." import, this test goes red instead of the
// design silently eroding (mirrors the Phase 7.1 AST guard on the SDK).
func TestPackagingImportsOnlyStdlibAndSDK(t *testing.T) {
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
				t.Errorf("%s imports %q — forbidden (packaging must stay out of internal/)", e.Name(), p)
			}
		}
	}
}

// writePkg is a test fixture helper: it writes a plugin.json plus a dummy
// executable (satisfying Validate's existence + checksum checks) and returns
// the Package loaded from the temp dir.
func writePkg(t *testing.T, withChecksum bool) *Package {
	t.Helper()
	dir := t.TempDir()

	exeRel := filepath.Join("bin", "demo-helper")
	exe := filepath.Join(dir, exeRel)
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("#!/bin/sh\n")
	if err := os.WriteFile(exe, body, 0o755); err != nil {
		t.Fatal(err)
	}

	sum := sha256OfFileOrFail(t, exe)
	var sb strings.Builder
	sb.WriteString(`{
  "name": "opscore-plugin-demo",
  "version": "1.0.0",
  "sdkVersion": "opscore.isolation/v1",
  "description": "demo executable plugin",
  "operations": {
    "plugin.demo.echo": {
      "executable": "bin/demo-helper",
      "args": ["--trace"],
      "env": ["OPSCORE_LOG=info"],
      "execTimeoutSeconds": 30,
      "maxResponseMB": 8
    }
  }`)
	if withChecksum {
		sb.WriteString(`,
  "checksums": {
    "bin/demo-helper": "` + sum + `"
  }`)
	}
	sb.WriteString("}\n")

	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return pkg
}

func sha256OfFileOrFail(t *testing.T, path string) string {
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

// TestLoadParsesMetadata proves Load reads the package identity and operations.
func TestLoadParsesMetadata(t *testing.T) {
	pkg := writePkg(t, false)
	if pkg.Name != "opscore-plugin-demo" {
		t.Errorf("name lost: %q", pkg.Name)
	}
	if pkg.Version != "1.0.0" {
		t.Errorf("version lost: %q", pkg.Version)
	}
	if pkg.SDKVersion != "opscore.isolation/v1" {
		t.Errorf("sdkVersion lost: %q", pkg.SDKVersion)
	}
	rs, ok := pkg.RunSpec("plugin.demo.echo")
	if !ok {
		t.Fatal("operation not parsed")
	}
	if rs.Executable != "bin/demo-helper" || len(rs.Args) != 1 || rs.Args[0] != "--trace" {
		t.Errorf("run spec lost: %+v", rs)
	}
}

// TestValidateRejectsWrongSDKVersion proves a package speaking the wrong
// protocol is rejected before any executable is launched (MUST-2).
func TestValidateRejectsWrongSDKVersion(t *testing.T) {
	pkg := writePkg(t, false)
	pkg.SDKVersion = "opscore.isolation/v2"
	if err := pkg.Validate(); err == nil {
		t.Fatal("Validate must reject an unsupported sdkVersion")
	}
}

// TestValidateMissingExecutable proves Validate fails when an operation's
// executable did not survive the unpack.
func TestValidateMissingExecutable(t *testing.T) {
	pkg := writePkg(t, false)
	// Point the operation at a file that does not exist.
	pkg.Operations["plugin.demo.echo"] = RunSpec{Executable: "bin/ghost"}
	if err := pkg.Validate(); err == nil {
		t.Fatal("Validate must fail when the executable is missing")
	}
}

// TestValidateChecksumOK proves the optional checksum path accepts a matching
// digest.
func TestValidateChecksumOK(t *testing.T) {
	pkg := writePkg(t, true)
	if err := pkg.Validate(); err != nil {
		t.Fatalf("Validate with matching checksum failed: %v", err)
	}
}

// TestValidateChecksumMismatch proves a tampered executable is rejected.
func TestValidateChecksumMismatch(t *testing.T) {
	pkg := writePkg(t, true)
	// Corrupt the on-disk executable after the checksum was computed.
	exe := filepath.Join(pkg.Dir(), "bin", "demo-helper")
	if err := os.WriteFile(exe, []byte("TAMPERED"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := pkg.Validate(); err == nil {
		t.Fatal("Validate must reject a checksum mismatch")
	}
}

// TestRunSpecUnknownOperation proves an undeclared operation is reported, not
// silently defaulted — so the host never deploys a phantom handler.
func TestRunSpecUnknownOperation(t *testing.T) {
	pkg := writePkg(t, false)
	if _, ok := pkg.RunSpec("plugin.demo.ghost"); ok {
		t.Error("undeclared operation must not resolve")
	}
}
