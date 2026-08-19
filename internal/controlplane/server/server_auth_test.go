package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/storage"
)

// newRegisterGateServer builds a Server wired to in-memory storage for the
// registration-gate tests. allowRegister toggles Config.AllowRegister.
func newRegisterGateServer(t *testing.T, allowRegister bool) *Server {
	t.Helper()
	reg := core.NewRegistry()
	exec := core.NewExecutor(core.NewLogSink(slog.Default()))
	disp := core.NewDispatcher(reg, exec)
	stor := storage.NewMemoryStorage()
	srv, err := New(Config{
		Storage:       stor,
		Dispatcher:    disp,
		AccessSecret:  "test:access",
		RefreshSecret: "test:refresh",
		Logger:        slog.Default(),
		AllowRegister: allowRegister,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// TestRegister_DisabledByDefault locks P0-1: open registration must be off
// unless explicitly enabled. Anyone reaching POST /api/auth/register without
// AllowRegister should get 403, not a freshly created account.
func TestRegister_DisabledByDefault(t *testing.T) {
	srv := newRegisterGateServer(t, false)
	body := `{"username":"alice","password":"secret"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleRegister(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when registration disabled, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestRegister_AllowedWhenEnabled ensures the gate is a switch, not a hard
// block: with AllowRegister=true the endpoint still works.
func TestRegister_AllowedWhenEnabled(t *testing.T) {
	srv := newRegisterGateServer(t, true)
	body := `{"username":"alice","password":"secret"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleRegister(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 when registration enabled, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp["username"] != "alice" {
		t.Fatalf("expected username alice, got %v", resp["username"])
	}
}
