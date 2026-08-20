package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/controlplane/auth"
	"github.com/YuDong999/opscore/internal/protection"
	"github.com/YuDong999/opscore/internal/storage"
	"github.com/YuDong999/opscore/internal/storage/memory"
)

// ---------------------------------------------------------------------------
// Phase 23.2 quota management-surface regression tests (R23-3 / P22-9 / P22-10
// analogs). Locks: admin-only + CSRF fail-closed for set/clear, the single
// mutation seam writes through Gate.QuotaStore() (no second owner), the read
// surface projects DEFINITIONS ONLY (no consumption — R23-3), and the routes
// live ONLY on :8082 (never :8080, R21-1).
// ---------------------------------------------------------------------------

func newQuotaTestServer(t *testing.T, audit protection.AuditWriter) (*Server, string) {
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
		t.Fatalf("grant admin: %v", err)
	}
	access, _, _, err := authSvc.Login("admin", "test-password")
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}
	qs := protection.NewQuotaStore(memory.NewQuotaStore(), time.Now)
	srv := &Server{
		stor:   mem,
		auth:   authSvc,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		gate: protection.New(protection.Config{
			Quotas:  qs,
			Evidence: memory.NewQuotaEvidenceReader(),
			Audit:   audit,
		}),
	}
	return srv, access
}

func TestProtectionQuota_Read_Unauthenticated_401(t *testing.T) {
	srv, _ := newQuotaTestServer(t, &recordingAudit{})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodGet, "/management/v1/protection/quotas", "", "http://example.com", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated quotas read: want 401, got %d", w.Code)
	}
}

func TestProtectionQuota_Set_Unauthenticated_401(t *testing.T) {
	srv, _ := newQuotaTestServer(t, &recordingAudit{})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodPost, "/management/v1/protection/quotas", "", "http://example.com",
		[]byte(`{"capability":"exec","rss_bytes":1000}`))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated quota set: want 401, got %d", w.Code)
	}
}

func TestProtectionQuota_Set_MissingOrigin_403(t *testing.T) {
	srv, token := newQuotaTestServer(t, &recordingAudit{})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodPost, "/management/v1/protection/quotas", token, "",
		[]byte(`{"capability":"exec","rss_bytes":1000}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing Origin: want 403, got %d (body=%q)", w.Code, w.Body.String())
	}
}

func TestProtectionQuota_Set_CrossSiteOrigin_403(t *testing.T) {
	srv, token := newQuotaTestServer(t, &recordingAudit{})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodPost, "/management/v1/protection/quotas", token, "http://evil.example",
		[]byte(`{"capability":"exec","rss_bytes":1000}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site Origin: want 403, got %d", w.Code)
	}
}

func TestProtectionQuota_Set_AdminWithOrigin_200(t *testing.T) {
	rec := &recordingAudit{}
	srv, token := newQuotaTestServer(t, rec)
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodPost, "/management/v1/protection/quotas", token, "http://example.com",
		[]byte(`{"capability":"exec","principal":"alice","rss_bytes":500,"cpu_secs":3}`))
	if w.Code != http.StatusOK {
		t.Fatalf("admin quota set: want 200, got %d (body=%q)", w.Code, w.Body.String())
	}
	// R23-3: single owner — the definition lands on the Gate's QuotaStore.
	d, ok := srv.gate.QuotaStore().GetDefinition("exec", "alice")
	if !ok {
		t.Fatalf("definition not persisted to gate QuotaStore")
	}
	if d.RSSBytes != 500 || d.CPUSecs != 3 {
		t.Fatalf("definition fields wrong: %+v", d)
	}
	// P22-10 analog: operator is the server-derived subject, not the body.
	last := rec.last()
	if last.Action != protection.ActionQuotaSet {
		t.Fatalf("want %s audit, got %q", protection.ActionQuotaSet, last.Action)
	}
	if last.Principal != "admin" {
		t.Fatalf("operator must be server-derived 'admin', got %q", last.Principal)
	}
}

func TestProtectionQuota_Read_ProjectionsDefinitionsOnly(t *testing.T) {
	rec := &recordingAudit{}
	srv, token := newQuotaTestServer(t, rec)
	_ = srv.gate.QuotaStore().SetDefinition(protection.QuotaDefinition{
		Capability: "exec", Principal: "", RSSBytes: 1000, CPUSecs: 5,
	})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodGet, "/management/v1/protection/quotas", token, "http://example.com", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("quotas read: want 200, got %d (body=%q)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !bytes.Contains(w.Body.Bytes(), []byte(`"capability":"exec"`)) {
		t.Fatalf("response should project capability: %s", body)
	}
	// R23-3: the read surface MUST NOT leak consumption — no usage/consumption
	// keys are ever present.
	for _, leak := range []string{"\"usage\"", "\"consumption\"", "\"complete\"", "\"observed\""} {
		if bytes.Contains(w.Body.Bytes(), []byte(leak)) {
			t.Fatalf("read surface leaked consumption field %s: %s", leak, body)
		}
	}
}

func TestProtectionQuota_Clear_AdminWithOrigin_200(t *testing.T) {
	rec := &recordingAudit{}
	srv, token := newQuotaTestServer(t, rec)
	_ = srv.gate.QuotaStore().SetDefinition(protection.QuotaDefinition{Capability: "exec", Principal: "", RSSBytes: 1000})
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodDelete, "/management/v1/protection/quotas", token, "http://example.com",
		[]byte(`{"capability":"exec","principal":""}`))
	if w.Code != http.StatusOK {
		t.Fatalf("admin quota clear: want 200, got %d (body=%q)", w.Code, w.Body.String())
	}
	if _, ok := srv.gate.QuotaStore().GetDefinition("exec", ""); ok {
		t.Fatalf("definition should be cleared")
	}
	if rec.last().Action != protection.ActionQuotaClear {
		t.Fatalf("want %s audit, got %q", protection.ActionQuotaClear, rec.last().Action)
	}
}

func TestProtectionQuota_Set_OutcomeDegraded(t *testing.T) {
	flaky := &flakyAudit{failOnCall: 2} // intent(1) ok, outcome(2) fails
	srv, token := newQuotaTestServer(t, flaky)
	h := srv.ProtectionReadMux()
	w := doReqFull(h, http.MethodPost, "/management/v1/protection/quotas", token, "http://example.com",
		[]byte(`{"capability":"exec","rss_bytes":1000}`))
	if w.Code != http.StatusOK {
		t.Fatalf("degraded set: want 200, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"degraded":true`)) {
		t.Fatalf("degraded set must flag degraded:true: %s", w.Body.String())
	}
	// R22-8 analog: the definition still stands even when the audit outcome fails.
	if _, ok := srv.gate.QuotaStore().GetDefinition("exec", ""); !ok {
		t.Fatalf("definition must stand despite degraded audit outcome (no rollback)")
	}
}

func TestProtectionQuota_NotOnExecutionMux(t *testing.T) {
	srv, token := newQuotaTestServer(t, &recordingAudit{})
	exec := srv.Handler() // :8080 execution mux
	w := doReqFull(exec, http.MethodPost, "/management/v1/protection/quotas", token, "http://example.com",
		[]byte(`{"capability":"exec","rss_bytes":1000}`))
	if w.Code == http.StatusOK {
		t.Fatalf(":8080 must not serve POST quotas; got 200 (body=%q)", w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("degraded")) {
		t.Fatalf(":8080 leaked the protection quota handler")
	}
}
