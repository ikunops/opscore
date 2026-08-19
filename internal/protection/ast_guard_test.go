package protection

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the module root from the package directory (internal/protection).
func repoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(abs, "..", "..")
}

// scanned holds the parsed view of every non-test .go file under a root.
type scanned struct {
	imports map[string][]string            // file -> import paths
	methods map[string]map[string]struct{} // typeName -> method names
	raw     map[string]string              // file -> source text
}

var (
	scanCache *scanned
	scanRoot  string
)

func scanRepo(t *testing.T) *scanned {
	root := repoRoot(t)
	if scanCache != nil && scanRoot == root {
		return scanCache
	}
	s := &scanned{
		imports: map[string][]string{},
		methods: map[string]map[string]struct{}{},
		raw:     map[string]string{},
	}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		s.raw[path] = string(src)
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, src, 0)
		if perr != nil {
			return nil
		}
		for _, imp := range f.Imports {
			s.imports[path] = append(s.imports[path], strings.Trim(imp.Path.Value, "\""))
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			tn := recvTypeName(fn.Recv.List[0].Type)
			if tn == "" {
				continue
			}
			// Key by package directory + type name so identically-named types
			// in different packages (e.g. storage/sqlite/protectionStore and
			// storage/memory/protectionStore) are counted as distinct impls.
			key := filepath.Dir(path) + "|" + tn
			if s.methods[key] == nil {
				s.methods[key] = map[string]struct{}{}
			}
			s.methods[key][fn.Name.Name] = struct{}{}
		}
		return nil
	})
	scanCache = s
	scanRoot = root
	return s
}

func recvTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.IndexExpr:
		return recvTypeName(t.X)
	case *ast.IndexListExpr:
		return recvTypeName(t.X)
	}
	return ""
}

func countKillPersistenceImpls(s *scanned) int {
	const (
		loadKills          = "LoadKills"
		loadPrincipalKills = "LoadPrincipalKills"
		setKilled          = "SetKilled"
		setPrincipalKilled = "SetPrincipalKilled"
		listKills          = "ListKills"
	)
	needed := []string{loadKills, loadPrincipalKills, setKilled, setPrincipalKilled, listKills}
	count := 0
	for _, meths := range s.methods {
		ok := true
		for _, n := range needed {
			if _, has := meths[n]; !has {
				ok = false
				break
			}
		}
		if ok {
			count++
		}
	}
	return count
}

// P-5 / M-4: no goroutine/process kill in the protection package.
func TestTimeoutDoesNotKillGoroutine(t *testing.T) {
	s := scanRepo(t)
	forbidden := []string{
		"runtime.Goexit", "os.Exit", "os.Process.Kill",
		"signal.Kill", "syscall.Kill", "runtime.GoSched",
	}
	for path, src := range s.raw {
		if !strings.Contains(filepath.ToSlash(path), "internal/protection/") {
			continue
		}
		for _, f := range forbidden {
			if strings.Contains(src, f) {
				t.Fatalf("%s: forbidden termination call %q present (R93-①)", path, f)
			}
		}
	}
}

// P-12 / M-8: exactly two KillPersistence implementations in the repo.
func TestKillStoreSingleOwner(t *testing.T) {
	s := scanRepo(t)
	got := countKillPersistenceImpls(s)
	if got != 2 {
		t.Fatalf("expected exactly 2 KillPersistence implementations (sqlite + memory), found %d", got)
	}
}

// P-17 / M-14: protection rejects are observations, not Policy mutation.
func TestProtectionRejectNotPolicyMutation(t *testing.T) {
	s := scanRepo(t)
	forbidden := []string{
		"governancepolicy",
		"management.Intent",
		"management.CAS",
		"management.Outcome",
	}
	for path, src := range s.raw {
		if !strings.Contains(filepath.ToSlash(path), "internal/protection/") {
			continue
		}
		for _, f := range forbidden {
			if strings.Contains(src, f) {
				t.Fatalf("%s: forbidden policy-mutation reference %q (R93-④)", path, f)
			}
		}
	}
}

// P-23 / M-14: RecordOutcome references only breaker/protection types.
func TestRecordOutcomeIsProtectionOnly(t *testing.T) {
	s := scanRepo(t)
	for path, src := range s.raw {
		if !strings.Contains(filepath.ToSlash(path), "internal/protection/") {
			continue
		}
		// M-14: a governance/policy mutation call must never appear in the file.
		for _, f := range []string{"governancepolicy.", "management.Intent", "management.CAS", "management.Outcome"} {
			if strings.Contains(src, f) {
				t.Fatalf("%s: RecordOutcome body must not reference %q (R21-10)", path, f)
			}
		}
	}
}

// P-25 / M-16: breaker depends on FailureEvidenceReader, never on storage/audit.
func TestBreakerDependsOnEvidenceReader(t *testing.T) {
	s := scanRepo(t)
	for path, imps := range s.imports {
		if !strings.Contains(filepath.ToSlash(path), "internal/protection/") {
			continue
		}
		for _, imp := range imps {
			if strings.Contains(imp, "storage/audit") {
				t.Fatalf("%s: protection must not import %q (R21-12)", path, imp)
			}
		}
	}
	for path, src := range s.raw {
		if !strings.Contains(filepath.ToSlash(path), "internal/protection/") {
			continue
		}
		if strings.Contains(src, "AuditStore") {
			t.Fatalf("%s: protection must not reference AuditStore directly (R21-12)", path)
		}
	}
}

// M-17: kill store uses a tri-state, never a bare `loaded bool`.
func TestKillStoreTriStateNotLoadedBool(t *testing.T) {
	s := scanRepo(t)
	for path, src := range s.raw {
		if !strings.Contains(filepath.ToSlash(path), "internal/protection/killstore.go") {
			continue
		}
		if strings.Contains(src, "loaded bool") {
			t.Fatalf("%s: kill store must use tri-state, not `loaded bool` (R21-13)", path)
		}
	}
}

// P-18: frozen packages have zero diff (must be committed-clean to pass).
func TestFrozenPackagesZeroDiff(t *testing.T) {
	root := repoRoot(t)
	frozen := []string{
		"internal/plugin/runtime",
		"internal/plugin/isolation",
		"internal/controlplane/hostregistry",
		"internal/platform",
		"internal/governance",
	}
	for _, d := range frozen {
		cmd := exec.Command("git", "diff", "--quiet", "--", d)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("frozen package %s has diff:\n%s", d, string(out))
		}
	}
}

// P-19: no new dependencies.
func TestNoNewDependencies(t *testing.T) {
	root := repoRoot(t)
	for _, f := range []string{"go.mod", "go.sum"} {
		cmd := exec.Command("git", "diff", "--quiet", "--", f)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s changed (new dependency?):\n%s", f, string(out))
		}
	}
}

// P-20: external/v1 unchanged.
func TestExternalV1Unchanged(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("git", "diff", "--quiet", "--", "external")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("external/ has diff (R21-0):\n%s", string(out))
	}
}
