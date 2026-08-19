package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/controlplane/hostregistry"
	"github.com/YuDong999/opscore/internal/controlplane/sync"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/execution"
	"github.com/YuDong999/opscore/internal/storage"
	"log/slog"
)

// okStep / okHandler drive a trivial successful operation so a run with a
// resolved (named) target can be exercised end-to-end without real SSH.
type okStep struct{}

func (okStep) Describe() string { return "ok" }
func (okStep) Execute(ctx core.Context) core.StepResult {
	return core.StepResult{StepName: "ok", Success: true}
}

type okHandler struct{}

func (okHandler) Plan(_ core.Context, _ map[string]any) (*core.ExecutionPlan, error) {
	return &core.ExecutionPlan{OperationName: "demo.ok", Steps: []core.ExecutionStep{okStep{}}}, nil
}

// containsField reports whether s contains the given substring (used to assert
// that secret fields like "password"/"topsecret" never appear in responses).
func containsField(s, field string) bool { return strings.Contains(s, field) }

// newTestServerWithHosts wires a full Core + Runtime + HostStore (seeded with
// "web-01") so the Host Registry CRUD and named-target resolution can be
// exercised over HTTP (Phase 2.3).
func newTestServerWithHosts(t *testing.T) (*Server, *hostregistry.MemoryHostStore) {
	t.Helper()
	stor := storage.NewMemoryStorage()
	registry := core.NewRegistry()
	registry.Register(core.Operation{Name: "demo.block", Handler: blockHandler{}})
	registry.Register(core.Operation{Name: "demo.ok", Handler: okHandler{}})
	executor := core.NewExecutor(core.NewLogSink(slog.Default()))
	dispatcher := core.NewDispatcher(registry, executor)
	store := execution.NewMemoryStore()
	runtime := core.NewRuntime(executor, store)
	hostStore := hostregistry.NewMemoryHostStore()
	_, _ = hostStore.Save(hostregistry.Host{
		Name:   "web-01",
		Target: core.TargetHost{Address: "10.0.0.1", Port: 22, User: "root", Password: "secret", InsecureIgnoreHostKey: true},
	})
	if err := sync.New(registry, stor).Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	srv, err := New(Config{
		Storage:        stor,
		Dispatcher:     dispatcher,
		Runtime:        runtime,
		HostStore:      hostStore,
		AccessSecret:   "access-secret",
		RefreshSecret:  "refresh-secret",
		BootstrapAdmin: &BootstrapAdmin{Username: "admin", Password: "adminpw"},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv, hostStore
}

func TestParseTargetBody_Resolution(t *testing.T) {
	store := hostregistry.NewMemoryHostStore()
	_, _ = store.Save(hostregistry.Host{Name: "h1", Target: core.TargetHost{Address: "1.2.3.4", Port: 2222, User: "u"}})

	// string -> resolved from registry
	th, err := parseTargetBody(map[string]any{"target": "h1"}, store)
	if err != nil {
		t.Fatal(err)
	}
	if th.Address != "1.2.3.4" || th.Port != 2222 || th.User != "u" {
		t.Fatalf("resolve failed: %+v", th)
	}

	// object -> inline (existing behaviour preserved)
	th, err = parseTargetBody(map[string]any{"target": map[string]any{"address": "9.9.9.9", "port": 22.0}}, store)
	if err != nil {
		t.Fatal(err)
	}
	if th.Address != "9.9.9.9" {
		t.Fatalf("inline parse failed: %+v", th)
	}

	// unknown name -> error
	if _, err := parseTargetBody(map[string]any{"target": "nope"}, store); err == nil {
		t.Fatal("expected error for unknown host name")
	}

	// nil store + string -> error
	if _, err := parseTargetBody(map[string]any{"target": "h1"}, nil); err == nil {
		t.Fatal("expected error when registry not configured")
	}

	// absent -> zero target
	th, err = parseTargetBody(map[string]any{}, store)
	if err != nil || !th.IsZero() {
		t.Fatalf("absent target should be zero, got %+v err=%v", th, err)
	}
}

func TestHosts_CRUD_AdminAndRedaction(t *testing.T) {
	srv, _ := newTestServerWithHosts(t)
	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	// Create a host with a secret password.
	rec := doJSON(t, h, "POST", "/api/hosts", token, map[string]any{
		"name":     "db-1",
		"address":  "10.0.0.2",
		"user":     "u",
		"password": "topsecret",
		"insecure": true,
		"groups":   []string{"db"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	// The secret must NOT be echoed back.
	if contains := rec.Body.String(); containsField(contains, "password") || containsField(contains, "topsecret") {
		t.Fatalf("password leaked in response: %s", rec.Body.String())
	}

	// List must include both seeded + created hosts, neither leaking password.
	rec = doJSON(t, h, "GET", "/api/hosts", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !containsField(body, "web-01") || !containsField(body, "db-1") {
		t.Fatalf("list missing hosts: %s", body)
	}
	if containsField(body, "topsecret") || containsField(body, "\"secret\"") {
		t.Fatalf("password leaked in list: %s", body)
	}

	// Get single.
	rec = doJSON(t, h, "GET", "/api/hosts/db-1", token, nil)
	if rec.Code != http.StatusOK || containsField(rec.Body.String(), "topsecret") {
		t.Fatalf("get failed or leaked secret: %s", rec.Body.String())
	}

	// Delete then 404.
	rec = doJSON(t, h, "DELETE", "/api/hosts/db-1", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	rec = doJSON(t, h, "GET", "/api/hosts/db-1", token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d", rec.Code)
	}

	// Unknown host 404.
	rec = doJSON(t, h, "GET", "/api/hosts/ghost", token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get unknown: want 404, got %d", rec.Code)
	}
}

func TestHosts_RBAC(t *testing.T) {
	srv, _ := newTestServerWithHosts(t)
	h := srv.Handler()
	adminToken := login(t, h, "admin", "adminpw")

	if _, err := srv.auth.Register("bob", "bobpw"); err != nil {
		t.Fatalf("register bob: %v", err)
	}
	bobToken := login(t, h, "bob", "bobpw")

	// Bob cannot create.
	rec := doJSON(t, h, "POST", "/api/hosts", bobToken, map[string]any{"name": "x", "address": "1.1.1.1"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bob create: want 403, got %d", rec.Code)
	}
	// Bob cannot list.
	rec = doJSON(t, h, "GET", "/api/hosts", bobToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bob list: want 403, got %d", rec.Code)
	}
	// Admin can list.
	rec = doJSON(t, h, "GET", "/api/hosts", adminToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list: want 200, got %d", rec.Code)
	}
}

func TestHosts_RegistryDisabled(t *testing.T) {
	// newTestServerWithRuntime has no HostStore configured.
	srv, _ := newTestServerWithRuntime(t)
	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	rec := doJSON(t, h, "GET", "/api/hosts", token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("registry disabled: want 404, got %d", rec.Code)
	}
}

func TestHosts_RunWithNamedTarget(t *testing.T) {
	srv, _ := newTestServerWithHosts(t)
	h := srv.Handler()
	token := login(t, h, "admin", "adminpw")

	// Run demo.ok referencing the named host "web-01" — resolution must reach
	// the executor (the no-op step succeeds regardless of the remote address).
	rec := doJSON(t, h, "POST", "/api/operations/demo.ok/run", token, map[string]any{"target": "web-01"})
	if rec.Code != http.StatusOK {
		t.Fatalf("run with named target: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Success {
		t.Fatalf("run not successful: %s", rec.Body.String())
	}

	// Unknown host name must be rejected at the target-parse stage (400).
	rec = doJSON(t, h, "POST", "/api/operations/demo.ok/run", token, map[string]any{"target": "ghost"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("run with unknown target: want 400, got %d", rec.Code)
	}
}
