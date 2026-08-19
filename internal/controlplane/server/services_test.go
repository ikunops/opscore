package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/builtin/service"
	"github.com/YuDong999/opscore/internal/controlplane/audit"
	"github.com/YuDong999/opscore/internal/controlplane/sync"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/demo"
	"github.com/YuDong999/opscore/internal/storage"
	"log/slog"
)

// TestServer_ServicesFlow exercises the whole stack against the in-process fake
// host: login -> RBAC -> Context(target) -> SSH -> systemctl -> audit, for the
// four service operations. It proves the system-services UI has a working
// backend without needing a real systemd host.
func TestServer_ServicesFlow(t *testing.T) {
	fakeTarget, stop, err := demo.StartFakeHost("demo")
	if err != nil {
		t.Fatalf("start fake host: %v", err)
	}
	defer stop()

	stor := storage.NewMemoryStorage()
	registry := core.NewRegistry()
	for _, op := range []core.Operation{
		{Name: "system.service.restart", Permission: core.Permission{ResourceType: "system.service", Action: "restart"}, Risk: core.RiskMedium, Handler: service.NewRestartHandler()},
		{Name: "system.service.list", Permission: core.Permission{ResourceType: "system.service", Action: "list"}, Risk: core.RiskLow, Handler: service.NewListHandler()},
		{Name: "system.service.start", Permission: core.Permission{ResourceType: "system.service", Action: "start"}, Risk: core.RiskMedium, Handler: service.NewStartHandler()},
		{Name: "system.service.stop", Permission: core.Permission{ResourceType: "system.service", Action: "stop"}, Risk: core.RiskMedium, Handler: service.NewStopHandler()},
	} {
		registry.Register(op)
	}
	dispatcher := core.NewDispatcher(registry, core.NewExecutor(audit.NewStorageAuditSink(stor, slog.Default())))
	if err := sync.New(registry, stor).Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	srv, err := New(Config{
		Storage:        stor,
		Dispatcher:     dispatcher,
		AccessSecret:   "a",
		RefreshSecret:  "r",
		DefaultTarget:  fakeTarget,
		DemoMode:       true,
		BootstrapAdmin: &BootstrapAdmin{Username: "admin", Password: "adminpw"},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	h := srv.Handler()

	// login
	rec := doJSON(t, h, "POST", "/api/auth/login", "", credentials{"admin", "adminpw"})
	var tp tokenPair
	if err := json.Unmarshal(rec.Body.Bytes(), &tp); err != nil || tp.AccessToken == "" {
		t.Fatalf("login failed: %v body=%s", err, rec.Body.String())
	}

	// list -> the fake host's canned services
	rec = doJSON(t, h, "POST", "/api/operations/system.service.list/run", tp.AccessToken, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var runRes struct {
		Success bool `json:"success"`
		Steps   []struct {
			Output string `json:"output"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &runRes); err != nil {
		t.Fatalf("decode list result: %v", err)
	}
	if !runRes.Success || len(runRes.Steps) == 0 {
		t.Fatalf("list not successful: %+v", runRes)
	}
	if runRes.Steps[0].Output == "" || !strings.Contains(runRes.Steps[0].Output, "nginx.service") {
		t.Fatalf("list output missing expected service: %q", runRes.Steps[0].Output)
	}

	// stop an inactive service (mysql) -> success
	rec = doJSON(t, h, "POST", "/api/operations/system.service.stop/run", tp.AccessToken, map[string]any{"name": "mysql.service"})
	if rec.Code != http.StatusOK {
		t.Fatalf("stop status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !jsonSuccessful(t, rec) {
		t.Fatalf("stop failed: %s", rec.Body.String())
	}

	// start it -> success
	rec = doJSON(t, h, "POST", "/api/operations/system.service.start/run", tp.AccessToken, map[string]any{"name": "mysql.service"})
	if rec.Code != http.StatusOK || !jsonSuccessful(t, rec) {
		t.Fatalf("start failed: %s", rec.Body.String())
	}

	// restart nginx -> success, and is-active check inside must pass
	rec = doJSON(t, h, "POST", "/api/operations/system.service.restart/run", tp.AccessToken, map[string]any{"name": "nginx.service"})
	if rec.Code != http.StatusOK || !jsonSuccessful(t, rec) {
		t.Fatalf("restart failed: %s", rec.Body.String())
	}

	// audit reflects the executions (>=3 events: stop/start/restart)
	rec = doJSON(t, h, "GET", "/api/audit?limit=50", tp.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit status = %d", rec.Code)
	}
	var audit struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.Events) < 3 {
		t.Fatalf("expected >=3 audit events, got %d: %+v", len(audit.Events), audit.Events)
	}
}

func jsonSuccessful(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	var r struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		return false
	}
	return r.Success
}
