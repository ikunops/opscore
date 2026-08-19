package compat

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/ecosystem/packaging"
	"github.com/YuDong999/opscore/ecosystem/registry"
)

// TestCompatImportsOnlyStdlib pins the Phase 7.5 boundary: this package must
// not import any internal/ package (the frozen Runtime Core). It may import
// sibling ecosystem packages (packaging / registry) — those are not internal/.
func TestCompatImportsOnlyStdlib(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(path, "internal/") || strings.Contains(path, "/internal/") {
					t.Errorf("compat must not import internal/ packages, found %s", path)
				}
			}
		}
	}
}

func TestSDKMismatchIsIncompatible(t *testing.T) {
	p := NewPolicy()
	res := p.Check(
		PackageSpec{SDKVersion: "opscore.isolation/v2", Version: "1.0.0"},
		RuntimeSpec{Version: "1.4.0"},
	)
	if res.Compatible {
		t.Fatalf("expected incompatible, got %+v", res)
	}
	if res.Code != CodeSDKMismatch {
		t.Errorf("expected CodeSDKMismatch, got %q", res.Code)
	}
	if len(res.Reasons) == 0 {
		t.Fatal("expected at least one blocking reason")
	}
}

func TestRuntimeBelowMinimumIsIncompatible(t *testing.T) {
	p := NewPolicy()
	res := p.Check(
		PackageSpec{SDKVersion: DefaultSDK, Version: "1.4.0", MinRuntime: "1.5.0"},
		RuntimeSpec{Version: "1.4.0"},
	)
	if res.Compatible {
		t.Fatalf("expected incompatible, got %+v", res)
	}
	if res.Code != CodeRuntimeTooOld {
		t.Errorf("expected CodeRuntimeTooOld, got %q", res.Code)
	}
}

func TestRuntimeAboveMaximumIsIncompatible(t *testing.T) {
	p := NewPolicy()
	res := p.Check(
		PackageSpec{SDKVersion: DefaultSDK, Version: "1.4.0", MaxRuntime: "1.3.0"},
		RuntimeSpec{Version: "1.4.0"},
	)
	if res.Compatible {
		t.Fatalf("expected incompatible, got %+v", res)
	}
	if res.Code != CodeRuntimeTooNew {
		t.Errorf("expected CodeRuntimeTooNew, got %q", res.Code)
	}
}

func TestCompatiblePackageWithinWindow(t *testing.T) {
	p := NewPolicy()
	res := p.Check(
		PackageSpec{SDKVersion: DefaultSDK, Version: "1.4.0", MinRuntime: "1.0.0", MaxRuntime: "2.0.0"},
		RuntimeSpec{Version: "1.4.0"},
	)
	if !res.Compatible {
		t.Fatalf("expected compatible, got reasons %v", res.Reasons)
	}
	if res.Code != CodeOK {
		t.Errorf("expected CodeOK, got %q", res.Code)
	}
}

func TestForwardCompatOlderPackageOnNewerRuntime(t *testing.T) {
	// Runtime 2.0.0 is newer than the package's declared max 1.9.0; normally
	// incompatible. But a package that declares NO upper bound runs on a newer
	// runtime (forward compatibility is the host's choice via the window).
	p := NewPolicy()
	res := p.Check(
		PackageSpec{SDKVersion: DefaultSDK, Version: "1.0.0", MinRuntime: "1.0.0"},
		RuntimeSpec{Version: "2.3.0"},
	)
	if !res.Compatible {
		t.Fatalf("expected forward-compatible, got %v", res.Reasons)
	}
}

func TestFromPackageAdapter(t *testing.T) {
	pkg := &packaging.Package{SDKVersion: DefaultSDK, Version: "9.9.9"}
	spec := FromPackage(pkg)
	if spec.SDKVersion != DefaultSDK || spec.Version != "9.9.9" {
		t.Fatalf("FromPackage wrong: %+v", spec)
	}
}

func TestFromRefAdapterCarriesWindow(t *testing.T) {
	ref := registry.PackageRef{
		SDKVersion:    DefaultSDK,
		LatestVersion: "1.4.0",
		MinRuntime:    "1.0.0",
		MaxRuntime:    "2.0.0",
	}
	spec := FromRef(ref)
	if spec.MinRuntime != "1.0.0" || spec.MaxRuntime != "2.0.0" {
		t.Fatalf("FromRef lost window: %+v", spec)
	}
	// The window flows through the policy unchanged.
	p := NewPolicy()
	if !p.Check(spec, RuntimeSpec{Version: "1.5.0"}).Compatible {
		t.Fatal("expected compatible through FromRef path")
	}
}
