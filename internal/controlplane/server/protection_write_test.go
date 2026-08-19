package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/controlplane/auth"
	"github.com/YuDong999/opscore/internal/protection"
	"github.com/YuDong999/opscore/internal/storage"
)

// ---------------------------------------------------------------------------
// Phase 22.2 write-surface regression tests (ADR-050 P22-2 / P22-9 / P22-10).
//
// Locks the invariant that the operator kill/release routes are the SINGLE
// mutation seam, live ONLY on :8082 (never :8080), require admin + a same-origin
// Origin (CSRF fail-closed), and always derive the operator/state server-side.
// ---------------------------------------------------------------------------

// recordingAudit captures protection events so tests can assert the operator is
// server-derived (P22-10), never taken from the request.
type recordingAudit struct {
	mu     sync.Mutex
	events []protection.ProtectionEvent
}

func (a *recordingAudit) WriteEvent(_ context.Context, ev protection.ProtectionEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
	return nil
}

func (a *recordingAudit) last() protection.ProtectionEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.events) == 0 {
		return protection.ProtectionEvent{}
	}
	return a.events[len(a.events)-1]
}

// flakyAudit fails on its Nth write — used to exercise the degraded /
// intent-without-outcome path (P22-8): intent succeeds, outcome fails.
type flakyAudit struct {
	mu         sync.Mutex
	n          int
	failOnCall int
}

func (a *flakyAudit) WriteEvent(_ context.Context, _ protection.ProtectionEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
	if a.failOnCall > 0 && a.n == a.failOnCall {
		return errors.New("boom")
	}
	return nil
}

// newProtectionTestServerWithAudit builds a *Server (optionally with a real gate)
// whose audit writer is the supplied recordingAudit, so tests can inspect the
// server-derived protection.kill / protection.release observations.
func newProtectionTestServerWithAudit(t *testing.T, withGate bool, audit protection.AuditWriter) (*Server, string) {
	t.Helper()
	mem := storage.NewMemoryStorage()
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
		srv.gate = protection.New(protection.Config{KillStore: ks, Audit: audit})
	}
	return srv, access
}

// doReqFull issues a request with optional Bearer token, Origin header, and body.
func doReqFull(h http.Handler, method, path, token, origin string, body []byte) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestProtectionWriteMux_Kill_Unauthenticated_401: no token → 401.
func TestProtectionWriteMux_Kill_Unauthenticated_401(t *testing.T) {
	srv, _ := newProtectionTestServerWithAudit(t, true, &recordingAudit{})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodPost, "/management/v1/protection/kills", "", "http://example.com", []byte(`{"capability":"x"}`))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated kill: want 401, got %d (body=%q)", w.Code, w.Body.String())
	}
}

// TestProtectionWriteMux_Kill_MissingOrigin_403: valid admin token but no
// Origin/Referer → CSRF fail-closed 403 (P22-9).
func TestProtectionWriteMux_Kill_MissingOrigin_403(t *testing.T) {
	srv, token := newProtectionTestServerWithAudit(t, true, &recordingAudit{})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodPost, "/management/v1/protection/kills", token, "", []byte(`{"capability":"x"}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing Origin: want 403, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("csrf")) {
		t.Fatalf("missing-Origin body should explain csrf, got %q", w.Body.String())
	}
}

// TestProtectionWriteMux_Kill_CrossSiteOrigin_403: a cross-site Origin is blocked.
func TestProtectionWriteMux_Kill_CrossSiteOrigin_403(t *testing.T) {
	srv, token := newProtectionTestServerWithAudit(t, true, &recordingAudit{})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodPost, "/management/v1/protection/kills", token, "http://evil.example", []byte(`{"capability":"x"}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site Origin: want 403, got %d (body=%q)", w.Code, w.Body.String())
	}
}

// TestProtectionWriteMux_Kill_NonAdmin_403: an authenticated non-admin → 403.
func TestProtectionWriteMux_Kill_NonAdmin_403(t *testing.T) {
	mem := storage.NewMemoryStorage()
	authSvc := auth.NewAuthService(mem, "test-access-secret", "test-refresh-secret")
	if _, err := authSvc.Register("ops", "pw"); err != nil {
		t.Fatalf("register ops: %v", err)
	}
	opsToken, _, _, err := authSvc.Login("ops", "pw")
	if err != nil {
		t.Fatalf("login ops: %v", err)
	}
	srv := &Server{stor: mem, auth: authSvc, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ks := protection.NewKillStore(fakeKillPersistence{}, time.Now)
	if err := ks.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	srv.gate = protection.New(protection.Config{KillStore: ks, Audit: &recordingAudit{}})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodPost, "/management/v1/protection/kills", opsToken, "http://example.com", []byte(`{"capability":"x"}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin kill: want 403, got %d (body=%q)", w.Code, w.Body.String())
	}
}

// TestProtectionWriteMux_Kill_AdminWithOrigin_200: admin + same-origin → 200;
// the kill flag is set on the Gate (P22-4) and the operator is server-derived
// (the authenticated subject, P22-10) — never from the request.
func TestProtectionWriteMux_Kill_AdminWithOrigin_200(t *testing.T) {
	rec := &recordingAudit{}
	srv, token := newProtectionTestServerWithAudit(t, true, rec)
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodPost, "/management/v1/protection/kills", token, "http://example.com",
		[]byte(`{"capability":"execute.shell","reason":"incident-42"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("admin kill: want 200, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !srv.gate.KillStore().IsKilled("execute.shell") {
		t.Fatalf("kill flag not set on gate after operator kill")
	}
	// P22-10: operator must be the authenticated subject, server-derived.
	last := rec.last()
	if last.Action != protection.ActionOperatorKill {
		t.Fatalf("expected protection.kill audit action, got %q", last.Action)
	}
	if last.Principal != "admin" {
		t.Fatalf("operator must be server-derived subject 'admin', got %q", last.Principal)
	}
	if last.CapID != "execute.shell" {
		t.Fatalf("audit CapID should be server truth, got %q", last.CapID)
	}
}

// TestProtectionWriteMux_Release_AdminWithOrigin_200: kill then release; the flag
// clears (P22-4: release does NOT restore execution, the Gate re-evaluates).
func TestProtectionWriteMux_Release_AdminWithOrigin_200(t *testing.T) {
	rec := &recordingAudit{}
	srv, token := newProtectionTestServerWithAudit(t, true, rec)
	h := srv.ProtectionReadMux()

	wk := doReqFull(h, http.MethodPost, "/management/v1/protection/kills", token, "http://example.com",
		[]byte(`{"capability":"execute.shell"}`))
	if wk.Code != http.StatusOK {
		t.Fatalf("kill: want 200, got %d", wk.Code)
	}
	if !srv.gate.KillStore().IsKilled("execute.shell") {
		t.Fatalf("kill flag not set")
	}

	wr := doReqFull(h, http.MethodPost, "/management/v1/protection/kills/execute.shell/release", token, "http://example.com", nil)
	if wr.Code != http.StatusOK {
		t.Fatalf("release: want 200, got %d (body=%q)", wr.Code, wr.Body.String())
	}
	if srv.gate.KillStore().IsKilled("execute.shell") {
		t.Fatalf("kill flag should clear after release")
	}
	if rec.last().Action != protection.ActionOperatorRelease {
		t.Fatalf("expected protection.release audit action, got %q", rec.last().Action)
	}
}

// TestProtectionWriteMux_NotOnExecutionMux: the new write route and the
// protection dashboard must NOT be served by the :8080 execution handler
// (R21-1 regression for the Phase 22.2 additions). The :8080 mux has a
// `GET /` console catch-all, so an unmatched POST yields 405 (method not
// allowed) and an unmatched GET falls through to the console SPA — either way
// the handler must NOT be the protection handler (no 200 kill response, no
// protection dashboard).
func TestProtectionWriteMux_NotOnExecutionMux(t *testing.T) {
	srv, token := newProtectionTestServerWithAudit(t, true, &recordingAudit{})
	exec := srv.Handler() // :8080 execution mux

	// POST kill must not be served on :8080 (handler must not run → never 200).
	wk := doReqFull(exec, http.MethodPost, "/management/v1/protection/kills", token, "http://example.com", []byte(`{"capability":"x"}`))
	if wk.Code == http.StatusOK {
		t.Fatalf(":8080 must not serve POST kills; got 200 (body=%q)", wk.Body.String())
	}
	if bytes.Contains(wk.Body.Bytes(), []byte("degraded")) {
		t.Fatalf(":8080 leaked the protection kill handler (returned kill JSON)")
	}
	// GET /dashboard on :8080 must not be the protection dashboard.
	wd := doReqFull(exec, http.MethodGet, "/dashboard", token, "http://example.com", nil)
	if wd.Code == http.StatusOK && bytes.Contains(wd.Body.Bytes(), []byte("Operational Protection Console")) {
		t.Fatalf(":8080 must not serve the protection dashboard")
	}
}

// TestProtectionWriteMux_Dashboard_Unauthenticated_401: per ADR-050 / R106=B the
// console shell is admin-only — an unauthenticated GET /dashboard must be 401.
func TestProtectionWriteMux_Dashboard_Unauthenticated_401(t *testing.T) {
	srv, _ := newProtectionTestServerWithAudit(t, true, &recordingAudit{})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodGet, "/dashboard", "", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /dashboard unauthenticated: want 401, got %d (body=%q)", w.Code, w.Body.String())
	}
}

// TestProtectionWriteMux_Dashboard_AuthenticatedAdmin_200: an authenticated admin
// receives the embedded console SPA.
func TestProtectionWriteMux_Dashboard_AuthenticatedAdmin_200(t *testing.T) {
	srv, token := newProtectionTestServerWithAudit(t, true, &recordingAudit{})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodGet, "/dashboard", token, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /dashboard admin: want 200, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("Operational Protection Console")) {
		t.Fatalf("GET /dashboard admin should return the protection console SPA")
	}
}

// TestProtectionWriteMux_LoginShell_Public_200: the public login shell is the
// unauthenticated entry point and must NOT be admin-gated.
func TestProtectionWriteMux_LoginShell_Public_200(t *testing.T) {
	srv, _ := newProtectionTestServerWithAudit(t, true, &recordingAudit{})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodGet, "/login", "", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /login (public): want 200, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("Operational Protection Console")) {
		t.Fatalf("GET /login should return the console SPA (login form)")
	}
}

// TestProtectionWriteMux_OutcomeDegraded: if the audit OUTCOME write fails but the
// intent + kill-store mutation succeeded, the response is degraded:true and the
// kill still stands (P22-8: no rollback, intent-without-outcome queryable).
func TestProtectionWriteMux_OutcomeDegraded(t *testing.T) {
	flaky := &flakyAudit{failOnCall: 2} // intent(1) ok, outcome(2) fails
	srv, token := newProtectionTestServerWithAudit(t, true, flaky)
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodPost, "/management/v1/protection/kills", token, "http://example.com",
		[]byte(`{"capability":"execute.shell"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("degraded kill: want 200, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"degraded":true`)) {
		t.Fatalf("degraded kill response must flag degraded:true, got %q", w.Body.String())
	}
	if !srv.gate.KillStore().IsKilled("execute.shell") {
		t.Fatalf("kill must stand even when audit outcome fails (no rollback)")
	}
}
