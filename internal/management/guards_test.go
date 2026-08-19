package management

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sourceFiles returns the package's non-test .go files. Guards inspect SOURCE
// only: a test file is allowed to import go/parser, call a bypassed verb inside
// a fixture, or otherwise do things the shipped surface must not do.
func sourceFiles(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	out := make([]string, 0, len(all))
	for _, f := range all {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		// A guard that silently scans nothing is worse than no guard: it reports
		// green forever. Fail loudly if the glob ever stops matching.
		t.Fatal("no source files found — guard would be vacuous")
	}
	return out
}

func parseSource(t *testing.T, file string, mode parser.Mode) *ast.File {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), file, src, mode)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return f
}

// TestManagementDoesNotImportForbidden enforces ADR-036 §3.6.
//
// The write surface is a CLIENT of the policy repository and nothing else. The
// forbidden set is not arbitrary: each entry names a subsystem that, if reachable
// from here, would let an operator-facing HTTP request reach execution.
//
//   - internal/governance — the evaluation Engine. Management stores rules; it
//     must never be able to evaluate them, or the write surface silently becomes
//     a decision surface (ADR-036 §4.5.2).
//   - plugin/runtime, plugin/isolation — frozen execution machinery.
//   - controlplane/hostregistry — the host fan-out seam.
//
// Note the enforcement mechanism, not just the list: the rule types this package
// consumes are re-exported by governancepolicy (rules.go), so the ban is not a
// convention that reviewers must remember — importing governance is simply
// unnecessary, and therefore its absence is verifiable here.
func TestManagementDoesNotImportForbidden(t *testing.T) {
	forbidden := map[string]string{
		"github.com/YuDong999/opscore/internal/governance":                "policy EVALUATION engine — management may store rules, never evaluate them (§4.5.2)",
		"github.com/YuDong999/opscore/internal/plugin/runtime":            "frozen execution machinery",
		"github.com/YuDong999/opscore/internal/plugin/isolation":          "frozen execution machinery",
		"github.com/YuDong999/opscore/internal/controlplane/hostregistry": "host fan-out seam — would make a write request reach hosts",
	}
	for _, f := range sourceFiles(t) {
		for _, imp := range parseSource(t, f, parser.ImportsOnly).Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if why, bad := forbidden[path]; bad {
				t.Errorf("%s imports forbidden package %s (%s)", f, path, why)
			}
		}
	}
}

// bypassVerbs are the mutation entry points that write policy state WITHOUT a
// revision check. Every one of them is legitimate inside governancepolicy — they
// are that package's original surface — and every one of them is a lost-update
// bug when called from here, because this package's callers are concurrent HTTP
// clients holding an If-Match they believe was honored.
//
// The guard matches on the CALLED name alone, deliberately. Resolving receiver
// types would need full type-checking, and a name-only match fails CLOSED: an
// unrelated future `x.Archive()` trips the guard and forces an explicit decision
// rather than sliding through. That is the correct bias for a guard whose whole
// job is to make one specific mistake impossible to make quietly.
var bypassVerbs = map[string]string{
	"Save":       "unconditional write — use CompareAndSave(rec, expected)",
	"Activate":   "unconditional transition — use CompareAndTransition",
	"Deactivate": "unconditional transition — use CompareAndTransition",
	"Archive":    "unconditional transition — use CompareAndTransition",
	"Create":     "package-level helper that wraps Save — use CompareAndSave(rec, 0)",
	"NextRevision": "read-then-write revision allocation; NextRevision→Save is exactly " +
		"the lost-update shape CAS exists to prevent",
}

// TestNoCASBypass enforces ADR-036 §3.6's no-CAS-bypass rule.
//
// Two escape routes exist and both are covered: the Repository METHODS
// (repo.Save / repo.Activate / …) and the governancepolicy PACKAGE-LEVEL helpers
// (governancepolicy.Create / Activate / …), which are thin wrappers over the same
// unconditional writes. Covering only the former would leave the ban trivially
// sidesteppable by switching call style — which is why §3.6 names both.
func TestNoCASBypass(t *testing.T) {
	for _, f := range sourceFiles(t) {
		file := parseSource(t, f, 0)
		fset := token.NewFileSet()
		_ = fset
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name string
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr: // repo.Save(…) or governancepolicy.Create(…)
				name = fun.Sel.Name
			case *ast.Ident: // a bare Create(…) — only possible after a dot-import
				name = fun.Name
			default:
				return true
			}
			if why, bad := bypassVerbs[name]; bad {
				t.Errorf("%s: forbidden non-CAS mutation call %q — %s", f, name, why)
			}
			return true
		})
	}
}

// casPrimitives are the ONLY sanctioned ways to change stored policy state.
var casPrimitives = map[string]bool{
	"CompareAndSave":       true,
	"CompareAndTransition": true,
}

// TestEveryCASCallIsAuditWrapped is a STRUCTURAL proof of MUST-P17-13, and it is
// the guard I consider most load-bearing in this file.
//
// The behavioural tests prove that the five handlers that exist today emit
// intent → mutate → outcome. They cannot prove anything about a sixth handler
// added next quarter by someone who copies a CAS call but not the audit
// scaffolding — and that handler would compile, pass every existing test, and
// mutate state with no durable record. Reviewer attention is not a control.
//
// So the guard asserts the shape instead of the behaviour: a CAS primitive may
// only be invoked from inside the `commit` field of a `mutation` literal. Since
// `mutation` values are executed exclusively by serveMutation — which brackets
// m.commit() with the intent and outcome writes — being inside a commit closure
// IS the audit guarantee. A new handler either adopts the wrapper or fails here.
//
// This is stricter than the letter of §3.6, which only requires the no-bypass
// guard. I am adding it because §3.6's ban stops the WRONG primitive from being
// used, while nothing in the frozen design stops the RIGHT primitive from being
// used in the wrong place — and that residual gap has the same consequence
// (an unaudited mutation) that P17-13 exists to eliminate.
func TestEveryCASCallIsAuditWrapped(t *testing.T) {
	found := 0
	for _, f := range sourceFiles(t) {
		file := parseSource(t, f, 0)

		// Path stack so each CAS call can be asked about its ancestry.
		var stack []ast.Node
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)

			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !casPrimitives[sel.Sel.Name] {
				return true
			}
			found++
			if !insideCommitField(stack) {
				t.Errorf("%s: %s is called outside a mutation{commit: …} closure; "+
					"it would mutate state without the intent/outcome audit pair (MUST-P17-13)",
					f, sel.Sel.Name)
			}
			return true
		})
	}
	// Vacuity check: if a refactor renames the primitives, the loop above finds
	// nothing and reports green while proving nothing. Pin the expected count's
	// lower bound to the mutations the frozen design requires: create + update
	// use CompareAndSave, and the three lifecycle verbs share one
	// CompareAndTransition call site.
	if found < 3 {
		t.Fatalf("found %d CAS call sites, want >= 3 — guard has gone vacuous", found)
	}
}

// insideCommitField reports whether the node path passes through the value of a
// `commit:` field in a composite literal.
func insideCommitField(stack []ast.Node) bool {
	for _, n := range stack {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "commit" {
			return true
		}
	}
	return false
}

// TestNoExecMethod carries the Phase 8 discipline forward: this package is a
// write surface for policy DOCUMENTS, not a way to make anything run. Note that
// "Start"/"Stop" are in the set on purpose — management exposes Handler() and
// lets the composition root own the listener, so a Start method here would mean
// the package had grown its own lifecycle and, with it, its own bind decision
// (which §3.6 assigns to the harness).
func TestNoExecMethod(t *testing.T) {
	forbidden := []string{
		"Run", "Exec", "Execute", "Command", "Apply", "Schedule", "Dispatch",
		"Invoke", "Emit", "Rollback", "Kill", "Remediate", "Start", "Stop",
		"Connect", "Evaluate",
	}
	for _, f := range sourceFiles(t) {
		for _, decl := range parseSource(t, f, 0).Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			for _, verb := range forbidden {
				if strings.HasPrefix(fn.Name.Name, verb) {
					t.Errorf("%s: forbidden execution verb method %q", f, fn.Name.Name)
				}
			}
		}
	}
}

// TestRoutePatternsAreManagementScoped supports the §3.6 separation mechanically.
//
// The harness binds these patterns on the management listener only; a build-time
// assertion there proves they are absent from the external mux. That assertion is
// only meaningful if the pattern list is actually distinctive, so this test pins
// the namespace here, at the source of the list, rather than trusting the consumer.
//
// The patterns are Go 1.22 ServeMux form ("METHOD /path"), so the check looks for
// the namespace anywhere in the pattern rather than at position zero — asserting a
// leading "/" would be testing the mux's syntax, not our isolation.
//
// Phase 17.3 (ADR-038 §3.3) grows the set from 5 mutations to 7 by adding the two
// read-only GET routes (audit / reconciliation); Phase 19 (ADR-042 §3.5) grows it
// from 7 to 10 by adding three read-only GET routes (metrics, projections/
// policy-activity, reconciliation/history); Phase 20 (ADR-045 §5) grows it from
// 10 to 11 by adding the traces read route. All remain inside the management
// namespace, so the isolation property holds for all of them.
func TestRoutePatternsAreManagementScoped(t *testing.T) {
	pats := RoutePatterns()
	if len(pats) != 11 {
		t.Fatalf("exported %d route patterns, want 11 (5 mutations + 2 Phase 18 reads + 3 Phase 19 reads + 1 Phase 20 traces, ADR-045 §5)", len(pats))
	}
	for _, p := range pats {
		if !strings.Contains(p, RoutePrefix) {
			t.Errorf("route %q is outside the %s namespace the harness isolates", p, RoutePrefix)
		}
		if !strings.HasPrefix(p, "GET ") && !strings.HasPrefix(p, "POST ") && !strings.HasPrefix(p, "PUT ") {
			t.Errorf("route %q uses an unexpected verb", p)
		}
	}
}

// TestReconciliationDoesNotMutate enforces ADR-038 MUST-17.3-B mechanically:
// the scanner/reconciliation source must NEVER mutate audit or policy state.
// Scan is pure read; ReconcileForward (the only defined write seam) is NOT
// invoked in Phase 17.3 and appends nothing. If a regression makes
// reconciliation call audit.Append or any repository write, this build-level
// guard fails — the same discipline as TestNoCASBypass, scoped to reconcile.go.
func TestReconciliationDoesNotMutate(t *testing.T) {
	file := parseSource(t, "reconcile.go", 0)
	forbidden := map[string]string{
		"Append":               "audit.Append would mutate the audit trail — Scan must be read-only (MUST-17.3-B)",
		"Save":                 "unconditional policy write",
		"Activate":             "unconditional policy transition",
		"Deactivate":           "unconditional policy transition",
		"Archive":              "unconditional policy transition",
		"CompareAndSave":       "policy CAS write",
		"CompareAndTransition": "policy CAS write",
		"NextRevision":         "read-then-write revision allocation",
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		case *ast.Ident:
			name = fun.Name
		default:
			return true
		}
		if why, bad := forbidden[name]; bad {
			t.Errorf("reconcile.go calls forbidden mutation %q — %s", name, why)
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// Phase 18 — Evidence Integrity guard (ADR-040 §3.4)
// ---------------------------------------------------------------------------

// isErrIdent reports whether an identifier is conventionally an error value.
// The codebase uses err / terr / gerr / perr, so the rule is "lowercase name
// ending in err" rather than an exact-name allowlist that a rename defeats.
func isErrIdent(name string) bool {
	return name != "" && strings.HasSuffix(strings.ToLower(name), "err")
}

// errNilCheck reports whether an if-statement's condition is `<someErr> != nil`.
func errNilCheck(cond ast.Expr) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	lhs, ok := bin.X.(*ast.Ident)
	if !ok || !isErrIdent(lhs.Name) {
		return false
	}
	rhs, ok := bin.Y.(*ast.Ident)
	return ok && rhs.Name == "nil"
}

// returnsOnlyNil reports whether a return statement returns one or more results
// that are ALL the literal `nil`. A bare `return` (void handler after
// writeError) and `return x, err` are both fine; `return nil` is the defect.
func returnsOnlyNil(ret *ast.ReturnStmt) bool {
	if len(ret.Results) == 0 {
		return false
	}
	for _, r := range ret.Results {
		id, ok := r.(*ast.Ident)
		if !ok || id.Name != "nil" {
			return false
		}
	}
	return true
}

// TestNoErrorSwallowInEvidencePath is the mechanical form of R18-1 (ADR-040
// §3.4). It encodes the ADR-039 §2 F-1 defect class as SYNTAX so it cannot
// quietly reappear:
//
//	if err != nil { return nil }   // a failed read shaped like a clean result
//	if err != nil { continue }     // a row that could not be read, silently gone
//
// Both were present in Phase 17.3 reconcile.go and are exactly how a scanner
// fabricates an all-clear. A failed read must produce `scanUnknown` + an error;
// an unreadable row must be REPORTED as `unexaminable`, never skipped.
//
// Scope: all of reconcile.go, plus the two evidence handlers in server.go. The
// handlers are named explicitly rather than scanning the whole file, because
// the mutation surface legitimately does other things with errors.
func TestNoErrorSwallowInEvidencePath(t *testing.T) {
	scopes := []struct {
		file  string
		funcs map[string]bool // nil = the whole file
	}{
		{file: "reconcile.go"},
		{file: "server.go", funcs: map[string]bool{
			"handleListAudit": true,
			"handleReconcile": true,
		}},
	}

	var inspected int
	for _, sc := range scopes {
		f := parseSource(t, sc.file, 0)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if sc.funcs != nil && !sc.funcs[fn.Name.Name] {
				continue
			}
			inspected++
			ast.Inspect(fn, func(n ast.Node) bool {
				ifs, ok := n.(*ast.IfStmt)
				if !ok || !errNilCheck(ifs.Cond) {
					return true
				}
				for _, stmt := range ifs.Body.List {
					switch s := stmt.(type) {
					case *ast.ReturnStmt:
						if returnsOnlyNil(s) {
							t.Errorf("%s: %s: `if %s { return nil }` — a failed read must never be "+
								"shaped like a successful empty result (R18-1)",
								sc.file, fn.Name.Name, exprText(ifs.Cond))
						}
					case *ast.BranchStmt:
						if s.Tok == token.CONTINUE {
							t.Errorf("%s: %s: `if %s { continue }` — an unreadable row must be REPORTED "+
								"as unexaminable, never silently skipped (R18-1)",
								sc.file, fn.Name.Name, exprText(ifs.Cond))
						}
					}
				}
				return true
			})
		}
	}
	// A guard that inspects nothing reports green forever.
	if inspected < 3 {
		t.Fatalf("guard inspected only %d functions — reconcile.go + 2 handlers expected; scope is stale", inspected)
	}
}

// exprText renders `x != nil` for error messages without dragging in go/printer.
func exprText(e ast.Expr) string {
	bin, ok := e.(*ast.BinaryExpr)
	if !ok {
		return "err != nil"
	}
	lhs, _ := bin.X.(*ast.Ident)
	if lhs == nil {
		return "err != nil"
	}
	return lhs.Name + " != nil"
}
