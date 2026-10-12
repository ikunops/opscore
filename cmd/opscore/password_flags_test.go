package main

// T405 (ADR-093): every password-bearing flag registration must seed its
// default through the env-aware resolvers (envValue / cfgVal) — never a bare
// literal. Command-line flags are visible to every local user via argv, so the
// password flags must at least OFFER an env channel (OPSCORE_TARGET_PASS /
// OPSCORE_PASSWORD, with the legacy OPESCORE_ alias); the AST walk keeps a
// future registration from silently dropping it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func TestT405PasswordFlagsAreEnvAware(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("T405: cannot parse main.go: %v", err)
	}
	passwordFlags := map[string]bool{"target-pass": true, "password": true}
	found := map[string]int{}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 3 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "String" {
			return true
		}
		nameLit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || nameLit.Kind != token.STRING {
			return true
		}
		name, err := strconv.Unquote(nameLit.Value)
		if err != nil || !passwordFlags[name] {
			return true
		}
		found[name]++
		defCall, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			t.Errorf("T405: --%s default is a literal — an argv-only password channel (ADR-093)", name)
			return true
		}
		defFun, ok := defCall.Fun.(*ast.Ident)
		if !ok || (defFun.Name != "envValue" && defFun.Name != "cfgVal") {
			t.Errorf("T405: --%s default must come from envValue/cfgVal, got %v", name, defCall.Fun)
		}
		return true
	})

	// Floor: if the AST shape changes and the walk stops matching, fail loudly
	// instead of passing vacuously. serve's --target-pass uses cfgVal and is
	// counted; plan/run use envValue.
	if found["target-pass"] < 3 || found["password"] < 1 {
		t.Fatalf("T405: expected at least 3 target-pass and 1 password registrations, got %v", found)
	}
}
