package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/YuDong999/opscore/internal/builtin/capability"
	"github.com/YuDong999/opscore/internal/builtin/service"
	"github.com/YuDong999/opscore/internal/controlplane/sync"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/storage"
	"log/slog"
)

// newCapTestServer wires a full Core that includes the capability Operation,
// so the /api/capabilities RBAC path can be exercised end-to-end.
func newCapTestServer(t *testing.T) (*Server, storage.Storage) {
	t.Helper()
	stor := storage.NewMemoryStorage()

	registry := core.NewRegistry()
	registry.Register(core.Operation{
		Name:       "system.service.restart",
		Permission: core.Permission{ResourceType: "system.service", Action: "restart"},
		Risk:       core.RiskMedium,
		Handler:    service.NewRestartHandler(),
	})
	registry.Register(core.Operation{
		Name:       "system.host.capability.list",
		Permission: core.Permission{ResourceType: "system.host", Action: "read"},
		Risk:       core.RiskLow,
		Handler:    capability.NewListHandler(),
	})
	dispatcher := core.NewDispatcher(registry, core.NewExecutor(core.NewLogSink(slog.Default())))
	if err := sync.New(registry, stor).Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	srv, err := New(Config{
		Storage:        stor,
		Dispatcher:     dispatcher,
		AccessSecret:   "access-secret",
		RefreshSecret:  "refresh-secret",
		AllowRegister:  true,
		BootstrapAdmin: &BootstrapAdmin{Username: "admin", Password: "adminpw"},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv, stor
}

func login(t *testing.T, h http.Handler, user, pass string) string {
	t.Helper()
	rec := doJSON(t, h, "POST", "/api/auth/login", "", credentials{user, pass})
	if rec.Code != http.StatusOK {
		t.Fatalf("%s login status = %d body=%s", user, rec.Code, rec.Body.String())
	}
	var tp tokenPair
	_ = json.Unmarshal(rec.Body.Bytes(), &tp)
	return tp.AccessToken
}

func TestCapabilities_Unauthenticated_401(t *testing.T) {
	srv, _ := newCapTestServer(t)
	h := srv.Handler()
	rec := doJSON(t, h, "GET", "/api/capabilities", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestCapabilities_Admin_OK(t *testing.T) {
	srv, _ := newCapTestServer(t)
	h := srv.Handler()
	tok := login(t, h, "admin", "adminpw")

	rec := doJSON(t, h, "GET", "/api/capabilities", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Hostname     string `json:"hostname"`
		OS           string `json:"os"`
		Arch         string `json:"arch"`
		Capabilities []struct {
			Name      string `json:"name"`
			Available bool   `json:"available"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Hostname == "" || body.OS == "" || body.Arch == "" {
		t.Fatalf("missing host identity: %+v", body)
	}
	if len(body.Capabilities) != 7 {
		t.Fatalf("expected 7 capabilities, got %d: %+v", len(body.Capabilities), body.Capabilities)
	}
	wantNames := map[string]bool{
		"systemd": true, "ufw": true, "firewalld": true, "iptables": true,
		"docker": true, "ssh.client": true, "ssh.server": true,
	}
	for _, c := range body.Capabilities {
		if !wantNames[c.Name] {
			t.Fatalf("unexpected capability %q", c.Name)
		}
		delete(wantNames, c.Name)
	}
	if len(wantNames) != 0 {
		t.Fatalf("missing capabilities: %v", wantNames)
	}
}

func TestCapabilities_Unprivileged_403(t *testing.T) {
	srv, _ := newCapTestServer(t)
	h := srv.Handler()

	rec := doJSON(t, h, "POST", "/api/auth/register", "", credentials{"bob", "bobpw"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status = %d", rec.Code)
	}
	tok := login(t, h, "bob", "bobpw")

	rec = doJSON(t, h, "GET", "/api/capabilities", tok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unprivileged, got %d body=%s", rec.Code, rec.Body.String())
	}
}
