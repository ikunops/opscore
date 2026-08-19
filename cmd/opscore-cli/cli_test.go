package main

import (
	"context"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestCLIASTGuard (R56 mechanical guard): the CLI package must never import the frozen internals
// (runtime / plugin runtime / isolation / host lifecycle / any executor surface) — it may only reach
// data through internal/external and the sanctioned read facades platformview / correlation.
func TestCLIASTGuardNoFrozenImports(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse dir: %v", err)
	}
	forbidden := []string{
		"internal/plugin/runtime",
		"internal/plugin/isolation",
		"internal/controlplane/hostregistry",
		"internal/core/execution", // Runtime execution path
		"executor",                // any executor surface package
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, forb := range forbidden {
					if strings.Contains(path, forb) {
						t.Errorf("CLI imports forbidden frozen system / executor surface: %s", path)
					}
				}
			}
		}
	}
}

// TestCLIOnlyBindsExternalContract confirms the CLI's data surface is reachable only through the
// sanctioned read source (internal/external, plus the platformview / correlation facades) — never
// directly through any other frozen internal package.
func TestCLIOnlyBindsExternalContract(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse dir: %v", err)
	}
	allowed := []string{
		"internal/external",
		"internal/platformview",
		"internal/correlation",
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if strings.Contains(path, "opscore/internal/") {
					ok := false
					for _, a := range allowed {
						if strings.Contains(path, a) {
							ok = true
							break
						}
					}
					if !ok {
						t.Errorf("CLI imports a non-sanctioned internal read source: %s", path)
					}
				}
			}
		}
	}
}

// TestCLIUsage ensures the CLI reports a usage error (not a panic) on no args.
func TestCLIUsage(t *testing.T) {
	if err := run(context.Background(), nil); err == nil {
		t.Errorf("expected usage error on no args")
	}
}
