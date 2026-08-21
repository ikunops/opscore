package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/controlplane/auth"
	"github.com/YuDong999/opscore/internal/protection"
	"github.com/YuDong999/opscore/internal/storage"
)

// newObsTestServer builds a *Server with the Phase 24.2 observability read
// surface fully wired (gate + provenance sink + alert tracker + policy). When
// withObs is false the gate is nil, exercising the safe 404 default.
func newObsTestServer(t *testing.T, withObs bool) (*Server, string, *protection.RecordingProvenanceSink) {
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

	if withObs {
		sink := protection.NewRecordingProvenanceSink(4096)
		ks := protection.NewKillStore(fakeKillPersistence{}, time.Now)
		if err := ks.Bootstrap(); err != nil {
			t.Fatalf("kill store bootstrap: %v", err)
		}
		srv.gate = protection.New(protection.Config{
			KillStore:  ks,
			Audit:      fakeAuditWriter{},
			Provenance: sink,
		})
		srv.alertTracker = protection.NewAlertTracker()
		srv.alertPolicy = protection.DefaultAlertPolicy()
		return srv, access, sink
	}
	return srv, access, nil
}

// --- /decisions -------------------------------------------------------------

func TestProtectionObs_Decisions_Admin200(t *testing.T) {
	srv, token, sink := newObsTestServer(t, true)
	// Emit a decision record off the gate path (mirrors Gate.Check emission).
	sink.Emit(context.Background(), protection.DecisionProvenance{
		TraceID:       "trace-1",
		CapabilityID:  "cap-x",
		PrincipalHash: "hash-y",
		Guard:         "breaker",
		Decision:      "reject",
		Action:        protection.ActionBreakerUnknown,
		Threshold:     "3",
		Observed:      "5",
		Detail:        "open",
		At:            time.Now(),
	})
	h := srv.ProtectionReadMux()
	w := doReq(h, http.MethodGet, "/management/v1/protection/decisions", token)
	if w.Code != http.StatusOK {
		t.Fatalf("decisions admin: want 200 got %d (body=%q)", w.Code, w.Body.String())
	}
	var body struct {
		Decisions       []map[string]any `json:"decisions"`
		ProvenanceStats map[string]any   `json:"provenance_stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode decisions: %v (body=%q)", err, w.Body.String())
	}
	if len(body.Decisions) != 1 {
		t.Fatalf("want 1 decision, got %d", len(body.Decisions))
	}
	if body.Decisions[0]["capability_id"] != "cap-x" {
		t.Fatalf("unexpected decision: %+v", body.Decisions[0])
	}
	if body.ProvenanceStats["capacity"] != float64(4096) {
		t.Fatalf("provenance capacity want 4096 got %v", body.ProvenanceStats["capacity"])
	}
}

func TestProtectionObs_Decisions_GateNil_404(t *testing.T) {
	srv, token, _ := newObsTestServer(t, false)
	h := srv.ProtectionReadMux()
	w := doReq(h, http.MethodGet, "/management/v1/protection/decisions", token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("gate==nil decisions: want 404 got %d (body=%q)", w.Code, w.Body.String())
	}
}

func TestProtectionObs_Decisions_Unauthenticated_401(t *testing.T) {
	srv, _, _ := newObsTestServer(t, true)
	h := srv.ProtectionReadMux()
	w := doReq(h, http.MethodGet, "/management/v1/protection/decisions", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("decisions unauth: want 401 got %d (body=%q)", w.Code, w.Body.String())
	}
}

// TestProtectionObs_Decisions_NotOnExecutionMux locks the architecture: the
// decision-log read surface must live ONLY on :8082, never on the :8080
// execution mux (it would otherwise expose protection state from the wrong
// process/context).
func TestProtectionObs_Decisions_NotOnExecutionMux(t *testing.T) {
	srv, token, _ := newObsTestServer(t, true)
	exec := srv.Handler()
	w := doReq(exec, http.MethodGet, "/management/v1/protection/decisions", token)
	if strings.Contains(w.Body.String(), "provenance_stats") {
		t.Fatalf(":8080 leaked decisions handler (returned JSON); route must stay on :8082 only")
	}
	if !strings.Contains(w.Body.String(), "<!doctype") {
		t.Fatalf(":8080 expected console SPA fallback; body=%q", w.Body.String())
	}
}

// --- /alerts ----------------------------------------------------------------

func TestProtectionObs_Alerts_Admin200(t *testing.T) {
	srv, token, _ := newObsTestServer(t, true)
	// Drive a firing condition into the tracker (mirrors the background ticker).
	srv.alertTracker.Observe(protection.AlertCondition{
		Firing:      true,
		UnknownRate: 99,
		Threshold:   50,
		Window:      time.Minute,
	}, time.Now())
	h := srv.ProtectionReadMux()
	w := doReq(h, http.MethodGet, "/management/v1/protection/alerts", token)
	if w.Code != http.StatusOK {
		t.Fatalf("alerts admin: want 200 got %d (body=%q)", w.Code, w.Body.String())
	}
	var body struct {
		Firing        bool   `json:"firing"`
		Since         string `json:"since"`
		UnknownRate   int64  `json:"unknown_rate"`
		Threshold     int64  `json:"threshold"`
		WindowSeconds int64  `json:"window_seconds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode alerts: %v (body=%q)", err, w.Body.String())
	}
	if !body.Firing {
		t.Fatal("expected firing=true")
	}
	if body.Since == "" {
		t.Fatal("expected non-empty since")
	}
	if body.UnknownRate != 99 || body.Threshold != 50 || body.WindowSeconds != 60 {
		t.Fatalf("unexpected alert body: %+v", body)
	}
}

func TestProtectionObs_Alerts_GateNil_404(t *testing.T) {
	srv, token, _ := newObsTestServer(t, false)
	h := srv.ProtectionReadMux()
	w := doReq(h, http.MethodGet, "/management/v1/protection/alerts", token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("gate==nil alerts: want 404 got %d (body=%q)", w.Code, w.Body.String())
	}
}

func TestProtectionObs_Alerts_Unauthenticated_401(t *testing.T) {
	srv, _, _ := newObsTestServer(t, true)
	h := srv.ProtectionReadMux()
	w := doReq(h, http.MethodGet, "/management/v1/protection/alerts", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("alerts unauth: want 401 got %d (body=%q)", w.Code, w.Body.String())
	}
}
