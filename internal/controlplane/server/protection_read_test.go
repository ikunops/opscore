package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/controlplane/auth"
	"github.com/YuDong999/opscore/internal/protection"
	"github.com/YuDong999/opscore/internal/storage"
)

// ---------------------------------------------------------------------------
// Architecture-preserving regression test for the Phase 21 Operational
// Protection read surface (R21-1 / ADR-048 B1 fix).
//
// The read surface MUST live on :8082, colocated with the gate that owns the
// kill store + metric counters — NOT on the :8080 execution mux and NOT on the
// harness :8082 read-surface (which has no gate → always-zero "fake green").
//
// This test locks three states and, critically, asserts the protection routes
// are ABSENT from the :8080 execution handler — closing both R21-1 regression
// paths (re-add to :8080, or bind :8082 in a process without the gate).
// ---------------------------------------------------------------------------

// fakeKillPersistence is a dependency-free KillPersistence for the read path.
type fakeKillPersistence struct{}

func (fakeKillPersistence) LoadKills() (map[string]bool, error)         { return map[string]bool{}, nil }
func (fakeKillPersistence) LoadPrincipalKills() (map[string]bool, error) { return map[string]bool{}, nil }
func (fakeKillPersistence) SetKilled(string, bool) error               { return nil }
func (fakeKillPersistence) SetPrincipalKilled(string, bool) error      { return nil }
func (fakeKillPersistence) ListKills() ([]protection.KillEntry, error) { return nil, nil }

// fakeAuditWriter is a no-op AuditWriter (read path never writes audit).
type fakeAuditWriter struct{}

func (fakeAuditWriter) WriteEvent(context.Context, protection.ProtectionEvent) error { return nil }

// newProtectionTestServer builds a *Server wired with an in-memory store, a
// real AuthService, and (optionally) a real gate. withGate=false exercises the
// gate==nil safe default (R21-1: do not expose protection status when disabled).
func newProtectionTestServer(t *testing.T, withGate bool) (*Server, string) {
	t.Helper()

	mem := storage.NewMemoryStorage()
	// The admin role must exist for isAdmin() to resolve (mirrors the
	// synchronizer + bootstrapAdmin path in production).
	role, err := mem.Roles().Save(storage.Role{Name: "admin"})
	if err != nil {
		t.Fatalf("create admin role: %v", err)
	}
	authSvc := auth.NewAuthService(mem, "test-access-secret", "test-refresh-secret")
	admin, err := authSvc.Register("admin", "test-password")
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	if err := mem.Users().AddRole(admin.ID, role.ID); err != nil {
		t.Fatalf("grant admin role: %v", err)
	}
	access, _, _, err := authSvc.Login("admin", "test-password")
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}

	srv := &Server{
		stor:   mem,
		auth:   authSvc,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if withGate {
		ks := protection.NewKillStore(fakeKillPersistence{}, time.Now)
		if err := ks.Bootstrap(); err != nil {
			t.Fatalf("kill store bootstrap: %v", err)
		}
		srv.gate = protection.New(protection.Config{
			KillStore: ks,
			Audit:     fakeAuditWriter{},
		})
	}
	return srv, access
}

// doReq performs an HTTP request against h, optionally with a Bearer token.
func doReq(h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestProtectionReadMux_Unauthenticated_401: no token → 401 (sensitive control).
func TestProtectionReadMux_Unauthenticated_401(t *testing.T) {
	srv, _ := newProtectionTestServer(t, true)
	h := srv.ProtectionReadMux()

	w := doReq(h, http.MethodGet, "/management/v1/protection/metrics", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: want 401, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unauthenticated") {
		t.Fatalf("unauthenticated body should explain 401, got %q", w.Body.String())
	}
}

// TestProtectionReadMux_AuthenticatedAdmin_200: valid admin token + gate → 200
// with real (colocated) counters. Locks the "usable" state.
func TestProtectionReadMux_AuthenticatedAdmin_200(t *testing.T) {
	srv, token := newProtectionTestServer(t, true)
	h := srv.ProtectionReadMux()

	w := doReq(h, http.MethodGet, "/management/v1/protection/metrics", token)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated+gate: want 200, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "admitted") {
		t.Fatalf("metrics body should contain counters, got %q", w.Body.String())
	}

	// The kills endpoint must also be reachable (same gate, same process).
	wk := doReq(h, http.MethodGet, "/management/v1/protection/kills", token)
	if wk.Code != http.StatusOK {
		t.Fatalf("authenticated+gate kills: want 200, got %d (body=%q)", wk.Code, wk.Body.String())
	}
	if !strings.Contains(wk.Body.String(), "state") {
		t.Fatalf("kills body should contain state, got %q", wk.Body.String())
	}
}

// TestProtectionReadMux_GateNil_404: protection disabled (gate==nil) must NOT
// leak kill state / counters — returns 404.
func TestProtectionReadMux_GateNil_404(t *testing.T) {
	srv, token := newProtectionTestServer(t, false) // gate == nil
	h := srv.ProtectionReadMux()

	w := doReq(h, http.MethodGet, "/management/v1/protection/metrics", token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("gate==nil: want 404, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "protection not enabled") {
		t.Fatalf("gate==nil body should explain 404, got %q", w.Body.String())
	}
}

// TestProtectionReadMux_NotOnExecutionMux: the protection routes must NOT be
// registered on the :8080 execution handler. The :8080 mux has a `GET /`
// catch-all (root subtree match) that serves the console SPA, so an unmatched
// path returns the HTML SPA rather than 404. We therefore detect a leak by
// sending an AUTHENTICATED request: if the route were registered on :8080 the
// handler would run and return JSON metrics ("admitted"); if it is correctly
// absent, the request falls through to the console SPA (HTML). This closes the
// R21-1 regression path "protection route re-added to the execution mux".
func TestProtectionReadMux_NotOnExecutionMux(t *testing.T) {
	srv, token := newProtectionTestServer(t, true)
	exec := srv.Handler() // the :8080 execution mux

	w := doReq(exec, http.MethodGet, "/management/v1/protection/metrics", token)
	if strings.Contains(w.Body.String(), "admitted") {
		t.Fatalf(":8080 leaked protection handler (returned JSON metrics); route must stay on :8082 only")
	}
	if !strings.Contains(w.Body.String(), "<!doctype") {
		t.Fatalf(":8080 expected the console SPA fallback; body=%q", w.Body.String())
	}
}
