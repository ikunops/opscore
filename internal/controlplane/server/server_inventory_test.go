package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/controlplane/sync"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/execution"
	"github.com/YuDong999/opscore/internal/storage"
)

// TestServer_Inventory_CurrentTarget verifies the /api/inventory endpoint is
// wired and RBAC-gated (reusing the system.host.capability.list graph), and
// degrades gracefully for a local (snapshot-less) target: it returns 200 with
// an empty package manager rather than 500.
func TestServer_Inventory_CurrentTarget(t *testing.T) {
	srv, _ := newCapTestServer(t)
	h := srv.Handler()

	tok := login(t, h, "admin", "adminpw")

	rec := doJSON(t, h, "GET", "/api/inventory", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("inventory status = %d body=%s", rec.Code, rec.Body.String())
	}
	var view struct {
		Host           any    `json:"host"`
		PackageManager string `json:"package_manager"`
		Capabilities   []any  `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	// local target has no observed snapshot -> Host nil, PackageManager empty.
	if view.PackageManager != "" {
		t.Fatalf("expected empty package manager for local target, got %q", view.PackageManager)
	}
}

// TestServer_Inventory_Unauthenticated verifies the endpoint rejects missing
// bearer tokens (same authn as the rest of the API surface).
func TestServer_Inventory_Unauthenticated(t *testing.T) {
	srv, _ := newCapTestServer(t)
	h := srv.Handler()
	rec := doJSON(t, h, "GET", "/api/inventory", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// echoHandler is a fake read-only op whose single CommandStep runs
// `cmd /c echo <out>`. Using cmd (always present on Windows) keeps the
// inventory detail collection deterministic in CI without a real target host.
type echoHandler struct{ out string }

func (e echoHandler) Plan(_ core.Context, _ map[string]any) (*core.ExecutionPlan, error) {
	return &core.ExecutionPlan{
		OperationName: "fake.ro",
		Permission:    core.Permission{ResourceType: "ro", Action: "ro"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "echo",
				Executable: "cmd",
				Args:       []string{"/c", "echo", e.out},
				Timeout:    5 * time.Second,
			},
		},
	}, nil
}

// TestServer_Inventory_Detail verifies the Phase 2.9 closure: GET /api/inventory
// runs the read-only op whitelist through the real Execution stack (no bypass)
// and returns the aggregated real output in view.detail. Admin is granted the
// ops via the normal synchronizer so the per-op RBAC path is exercised too.
func TestServer_Inventory_Detail(t *testing.T) {
	stor := storage.NewMemoryStorage()
	registry := core.NewRegistry()

	canned := map[string]string{
		"system.host.info":    "uptime fake",
		"system.disk.mounts":  "mount fake",
		"system.disk.list":    "disk fake",
		"system.package.list": "pkg fake",
		"system.user.list":    "user fake",
		"system.service.list": "svc fake",
		"system.process.list": "proc fake",
	}
	for name, out := range canned {
		registry.Register(core.Operation{
			Name:       name,
			Permission: core.Permission{ResourceType: "ro", Action: "ro"},
			Risk:       core.RiskLow,
			Handler:    echoHandler{out: out},
		})
	}
	// The inventory endpoint's entry auth gate requires the read-capability op
	// to exist and be granted to admin (mirrors production's capability builtin
	// + sync). Register it so the gate passes; it is not part of the
	// read-only detail whitelist, so it does not affect the assertions below.
	registry.Register(core.Operation{
		Name:       "system.host.capability.list",
		Permission: core.Permission{ResourceType: "system.host.capability", Action: "list"},
		Risk:       core.RiskLow,
		Handler:    echoHandler{out: "cap fake"},
	})

	executor := core.NewExecutor(core.NewLogSink(slog.Default()))
	dispatcher := core.NewDispatcher(registry, executor)
	store := execution.NewMemoryStore()
	runtime := core.NewRuntime(executor, store)
	if err := sync.New(registry, stor).Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	srv, err := New(Config{
		Storage:        stor,
		Dispatcher:     dispatcher,
		Runtime:        runtime,
		AccessSecret:   "access-secret",
		RefreshSecret:  "refresh-secret",
		BootstrapAdmin: &BootstrapAdmin{Username: "admin", Password: "adminpw"},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	h := srv.Handler()
	tok := login(t, h, "admin", "adminpw")

	rec := doJSON(t, h, "GET", "/api/inventory", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("inventory status = %d body=%s", rec.Code, rec.Body.String())
	}
	var view struct {
		Detail *struct {
			Target string `json:"target"`
			Ops    []struct {
				Name  string   `json:"name"`
				Ok    bool     `json:"ok"`
				Steps []string `json:"steps"`
			} `json:"ops"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if view.Detail == nil {
		t.Fatal("expected detail in inventory view, got none")
	}
	byOk := map[string]bool{}
	byOut := map[string]string{}
	for _, o := range view.Detail.Ops {
		byOk[o.Name] = o.Ok
		if len(o.Steps) > 0 {
			byOut[o.Name] = o.Steps[0]
		}
	}
	for name, want := range canned {
		if !byOk[name] {
			t.Fatalf("op %s not ok in detail: %+v", name, view.Detail.Ops)
		}
		// Shell output varies by platform (trailing newline, quoting of
		// space-containing args); assert the expected fragment is present
		// rather than exact-byte equality.
		if !strings.Contains(byOut[name], want) {
			t.Fatalf("op %s output = %q, want it to contain %q", name, byOut[name], want)
		}
	}
}
