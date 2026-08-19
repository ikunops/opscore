package enterprise

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestASTGuardForbiddenImports mechanically enforces ADR-017 MUST-5: Enterprise
// must not re-implement or own the four frozen/accepted systems. It forbids
// importing the Runtime engine, process isolation, host registry, cluster,
// observability, or governance — matching the AST-guard discipline established
// by internal/observability and internal/cluster.
func TestASTGuardForbiddenImports(t *testing.T) {
	forbidden := []string{
		"internal/plugin/runtime",
		"internal/plugin/isolation",
		"internal/controlplane/hostregistry",
		"internal/cluster",
		"internal/observability",
		"internal/governance",
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range node.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, forb := range forbidden {
				if strings.Contains(path, forb) {
					t.Errorf("%s imports forbidden package %s (ADR-017 MUST-5: Enterprise must not own/replicate frozen systems)", filepath.Base(f), forb)
				}
			}
		}
	}
}

// TestAttachAndQuery exercises the core policy-attachment behavior: attaching
// to existing IDs, querying by target, and detaching. It asserts Enterprise
// holds ONLY metadata and references — never host/cluster/runtime internals.
func TestAttachAndQuery(t *testing.T) {
	s := NewService()

	// Attach a maintenance window to a host ref (Enterprise owns the policy,
	// not the host — it only holds the opaque HostRef).
	a1, err := s.Attach(TargetHost, "host-a", PolicyMaintenanceWindow, map[string]string{
		"window": "22:00-23:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	if a1.AttachID == "" {
		t.Fatal("expected non-empty AttachID")
	}
	if a1.TargetKind != TargetHost || a1.TargetRef != "host-a" {
		t.Fatalf("attachment target wrong: %+v", a1)
	}
	if a1.Kind != PolicyMaintenanceWindow {
		t.Fatalf("attachment kind wrong: %s", a1.Kind)
	}

	// Stack a second policy on the same target (approvals + windows coexist).
	a2, err := s.Attach(TargetHost, "host-a", PolicyApproval, map[string]string{"approver": "ops"})
	if err != nil {
		t.Fatal(err)
	}

	// Attach a tenant scope to a plugin id (reuses Ecosystem's PluginID).
	if _, err := s.Attach(TargetPlugin, "plugin-x", PolicyTenantScope, map[string]string{"tenant": "acme"}); err != nil {
		t.Fatal(err)
	}

	// Query by target: host-a should have exactly 2 attachments.
	got := s.AttachmentsFor(TargetHost, "host-a")
	if len(got) != 2 {
		t.Fatalf("expected 2 attachments for host-a, got %d", len(got))
	}

	// All() returns 3 total.
	if all := s.All(); len(all) != 3 {
		t.Fatalf("expected 3 total attachments, got %d", len(all))
	}

	// Detach one; Enterprise metadata shrinks, host identity untouched.
	if err := s.Detach(a1.AttachID); err != nil {
		t.Fatal(err)
	}
	if got := s.AttachmentsFor(TargetHost, "host-a"); len(got) != 1 || got[0].AttachID != a2.AttachID {
		t.Fatalf("after detach expected 1 attachment (a2), got %+v", got)
	}

	// Detaching an unknown id is a clean error, never a panic.
	if err := s.Detach("ent-999"); err != ErrAttachmentNotFound {
		t.Fatalf("expected ErrAttachmentNotFound, got %v", err)
	}
}

// TestAttachValidation asserts Enterprise cannot attach to an empty target or
// empty policy kind — it must always bind to an existing ID (ADR-017 §3).
func TestAttachValidation(t *testing.T) {
	s := NewService()
	if _, err := s.Attach(TargetHost, "", PolicyApproval, nil); err != ErrInvalidTarget {
		t.Fatalf("expected ErrInvalidTarget, got %v", err)
	}
	if _, err := s.Attach(TargetHost, "host-a", "", nil); err != ErrInvalidPolicy {
		t.Fatalf("expected ErrInvalidPolicy, got %v", err)
	}
}

// TestNoExecMethod verifies by construction that the Service type exposes no
// execution-style method. We assert the public method set is limited to the
// policy-metadata API (attach/detach/query) — there is no Run/Exec/Invoke.
func TestNoExecMethod(t *testing.T) {
	typ := reflect.TypeOf(&Service{})
	banned := []string{"Run", "Exec", "Invoke", "Execute", "Command", "Evaluate"}
	for _, b := range banned {
		if _, ok := typ.MethodByName(b); ok {
			t.Errorf("Service must not expose execution method %q (ADR-017 MUST-1/4)", b)
		}
	}
}
