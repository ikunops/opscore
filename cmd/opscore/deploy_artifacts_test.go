package main

// T386~T388 (ADR-083/084, Phase 52): mechanical guards for the reference
// control-plane deployment artifacts.
//
// These tests exist because the deployment artifacts are the ONLY place where
// the evidence plane's home is written down outside code, and prose cannot
// keep three promises that operators depend on:
//
//  1. the unit wraps the real binary with the real subcommand and a REAL flag
//     set — a unit that names a flag the binary does not define fails at boot
//     on the operator's machine, not in our CI;
//  2. the shipped secrets example carries NO usable value;
//  3. the unit binds addresses that do not collide with the read edge (:8080/
//     :8081) and keeps the documented :8082 evidence surface.
//
// The flag set is extracted from cmdServe's own source via go/parser (the same
// discipline as the harness import guard), so the guard cannot drift from the
// binary it guards.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	deployDir     = "../../deploy"
	unitFile      = "opscore-controlplane.service"
	secretsSample = "controlplane.env.example"
)

// artifactTexts loads the two Phase 52 artifacts; a missing artifact is the
// initial RED state of this guard (ADR-084 §6), so the failure message names
// the file plainly.
func artifactTexts(t *testing.T) (unit, sample string) {
	t.Helper()
	ub, err := os.ReadFile(filepath.Join(deployDir, unitFile))
	if err != nil {
		t.Fatalf("T386: reference unit missing: %v", err)
	}
	sb, err := os.ReadFile(filepath.Join(deployDir, secretsSample))
	if err != nil {
		t.Fatalf("T386: secrets example missing: %v", err)
	}
	return string(ub), string(sb)
}

// joinContinuations folds systemd backslash line continuations into logical
// lines so ExecStart can be examined as one command.
func joinContinuations(s string) string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.HasSuffix(trimmed, "\\") {
			cur.WriteString(strings.TrimSuffix(trimmed, "\\"))
			cur.WriteString(" ")
			continue
		}
		cur.WriteString(trimmed)
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return strings.Join(out, "\n")
}

// execStartOf returns the single ExecStart= logical line, joined.
func execStartOf(t *testing.T, unit string) string {
	t.Helper()
	for _, line := range strings.Split(joinContinuations(unit), "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			return line
		}
	}
	t.Fatal("T386: unit has no ExecStart line")
	return ""
}

// T386: unit structure contract — real binary + subcommand; mandatory secrets
// file; no literal secret on argv; no demo / open-registration switches.
func TestT386ControlplaneUnitContract(t *testing.T) {
	unit, sample := artifactTexts(t)

	es := execStartOf(t, unit)
	fields := strings.Fields(es)
	if len(fields) < 2 || fields[0] != "ExecStart=/usr/local/bin/opscore" || fields[1] != "serve" {
		t.Fatalf("T386: ExecStart must wrap `/usr/local/bin/opscore serve`, got %q", es)
	}

	// I4: the secrets file is MANDATORY — a `-` prefix would let the unit boot
	// with the binary's change-me defaults, which is the failure mode the
	// reference exists to prevent.
	if !strings.Contains(unit, "EnvironmentFile=/etc/opscore/controlplane.env\n") {
		t.Fatalf("T386: unit must require EnvironmentFile=/etc/opscore/controlplane.env (mandatory, no '-' prefix)")
	}
	if strings.Contains(unit, "\nEnvironmentFile=-") {
		t.Fatal("T386: EnvironmentFile must not be optional ('-' prefix) — missing secrets must refuse to start")
	}

	// I3: secrets never travel on argv (visible to every local user via ps).
	for _, bad := range []string{"--jwt-secret", "--admin-pass"} {
		if strings.Contains(unit, bad) {
			t.Fatalf("T386: unit must not pass %s on the command line — secrets arrive via the env file only", bad)
		}
	}

	// The reference deployment is not a demo and never opens self-registration.
	for _, bad := range []string{"--demo", "--allow-register"} {
		if strings.Contains(unit, bad) {
			t.Fatalf("T386: reference unit must not reference %s", bad)
		}
	}

	// The example mirrors the unit's env file: same basename, minus ".example".
	if !strings.Contains(unit, "/etc/opscore/"+secretsSample[:len(secretsSample)-len(".example")]) {
		t.Fatalf("T386: unit must reference the shipped secrets example by its installed name")
	}

	// I3 (other half): the example defines EXACTLY the two env names and no
	// usable value. A non-empty, non-placeholder value would ship a secret.
	defined := map[string]string{}
	for _, line := range strings.Split(sample, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			t.Fatalf("T386: secrets example has a non KEY=VALUE line: %q", l)
		}
		defined[k] = v
	}
	if len(defined) != 2 {
		t.Fatalf("T386: secrets example must define exactly 2 keys, got %v", defined)
	}
	for _, key := range []string{"OPESCORE_JWT_SECRET", "OPESCORE_ADMIN_PASS"} {
		v, ok := defined[key]
		if !ok {
			t.Fatalf("T386: secrets example must define %s", key)
		}
		if v != "" && !(strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">")) {
			t.Fatalf("T386: %s must be empty or an angle-bracket placeholder, got a concrete value", key)
		}
	}
}

// T387: every --flag named anywhere in the unit (comments included — they are
// part of the artifact's promise) must exist in cmdServe's flag set.
func TestT387ArtifactFlagsExistInServeFlagSet(t *testing.T) {
	unit, _ := artifactTexts(t)

	serveFlags := serveFlagNames(t, "main.go")

	re := regexp.MustCompile(`--([a-z0-9][a-z0-9-]*)`)
	found := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(unit, -1) {
		found[m[1]] = true
	}
	// A core floor: if extraction breaks, this list keeps the guard honest
	// instead of vacuously passing on an empty set.
	for _, req := range []string{"addr", "mgmt-addr", "storage", "db", "export-interval", "export-dir", "export-ledger-capacity"} {
		if !found[req] {
			t.Fatalf("T387: unit must explicitly set --%s (core control-plane surface)", req)
		}
	}
	for flag := range found {
		if !serveFlags[flag] {
			t.Errorf("T387: unit references --%s which cmdServe does not define (deployment artifact would fail at boot)", flag)
		}
	}
}

// T388: address discipline — the unit must pass explicit --addr on a port the
// read edge does not own (never :8080/:8081) and keep the documented :8082
// evidence surface. Changing either without updating ADR-084 §4 must fail here.
func TestT388ControlplaneAddressDiscipline(t *testing.T) {
	unit, _ := artifactTexts(t)
	fields := strings.Fields(execStartOf(t, unit))

	valueOf := func(flag string) string {
		t.Helper()
		for i, f := range fields {
			if f == flag {
				if i+1 >= len(fields) {
					t.Fatalf("T388: %s has no value", flag)
				}
				return fields[i+1]
			}
		}
		t.Fatalf("T388: unit must pass %s explicitly", flag)
		return ""
	}

	portOf := func(addr string) int {
		t.Helper()
		i := strings.LastIndexByte(addr, ':')
		if i < 0 {
			t.Fatalf("T388: address %q has no port", addr)
		}
		p, err := strconv.Atoi(addr[i+1:])
		if err != nil {
			t.Fatalf("T388: address %q has a non-numeric port", addr)
		}
		return p
	}

	apiPort := portOf(valueOf("--addr"))
	if apiPort == 8080 || apiPort == 8081 {
		t.Fatalf("T388: control-plane --addr :%d collides with the read edge's contract/probe binds", apiPort)
	}
	if got := portOf(valueOf("--mgmt-addr")); got != 8082 {
		t.Fatalf("T388: --mgmt-addr :%d diverges from the documented :8082 evidence surface (ADR-048/084)", got)
	}
}

// serveFlagNames extracts the flag names registered in cmdServe from main.go
// via go/parser. Any `X.String("name", ...)`-style call inside cmdServe counts,
// which covers both the stage-1 probe set and the stage-2 serve set.
func serveFlagNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("T387: cannot parse %s: %v", path, err)
	}
	names := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "cmdServe" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "String", "Int", "Bool", "Duration":
			default:
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil {
				names[s] = true
			}
			return true
		})
	}
	if len(names) < 20 {
		t.Fatalf("T387: extracted only %d flags from cmdServe — extraction broke, guard would be vacuous", len(names))
	}
	return names
}
