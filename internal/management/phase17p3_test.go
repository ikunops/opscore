package management

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/governancepolicy"
	"github.com/YuDong999/opscore/internal/storage"
)

// ---------------------------------------------------------------------------
// Reconciler three-state scan (ADR-038 §3.2 / MUST-17.3-B)
// ---------------------------------------------------------------------------

// stubRepo is a minimal governancepolicy.Repository backed by an in-memory map.
// Only Get is exercised by Scan; the embedded interface satisfies the rest so
// the type compiles without a filesystem.
type stubRepo struct {
	governancepolicy.Repository
	present map[string]governancepolicy.PolicyRecord
}

func (s *stubRepo) Get(id string) (governancepolicy.PolicyRecord, bool, error) {
	rec, ok := s.present[id]
	return rec, ok, nil
}

func newScanKit(t *testing.T, present map[string]governancepolicy.PolicyRecord) (*Reconciler, *fakeAudit) {
	t.Helper()
	fa := &fakeAudit{}
	r := &Reconciler{audit: fa, repo: &stubRepo{present: present}}
	return r, fa
}

// TestScanClosedWhenOutcomeExists: an intent followed by a terminal outcome on
// the same correlation id is reported "closed" and is NOT treated as orphaned.
func TestScanClosedWhenOutcomeExists(t *testing.T) {
	r, fa := newScanKit(t, nil)
	const corr = "c-closed"
	if _, err := fa.Append(storage.AuditEvent{Result: resultIntent, Target: "pol-1", Revision: 1, CorrelationID: corr}); err != nil {
		t.Fatal(err)
	}
	if _, err := fa.Append(storage.AuditEvent{Result: resultSuccess, Target: "pol-1", Revision: 1, CorrelationID: corr}); err != nil {
		t.Fatal(err)
	}
	report, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(report.Entries) != 1 {
		t.Fatalf("report len = %d, want 1", len(report.Entries))
	}
	if report.Entries[0].Status != reportClosed {
		t.Errorf("status = %q, want %q", report.Entries[0].Status, reportClosed)
	}
}

// TestScanUnresolvedWhenPolicyPresent: an orphaned intent whose target policy
// STILL EXISTS must be "unresolved" — never synthesized into success. The
// schema records no field tying a committed mutation to this correlation id,
// so revision movement is not taken as proof of commit (forbidden inference).
func TestScanUnresolvedWhenPolicyPresent(t *testing.T) {
	r, fa := newScanKit(t, map[string]governancepolicy.PolicyRecord{
		"pol-present": {PolicyID: "pol-present", Revision: 5},
	})
	const corr = "c-unresolved"
	if _, err := fa.Append(storage.AuditEvent{Result: resultIntent, Target: "pol-present", Revision: 1, CorrelationID: corr}); err != nil {
		t.Fatal(err)
	}
	report, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(report.Entries) != 1 {
		t.Fatalf("report len = %d, want 1", len(report.Entries))
	}
	if report.Entries[0].Status != reportUnresolved {
		t.Errorf("status = %q, want %q (orphaned intent with a present policy must NOT be synthesized)", report.Entries[0].Status, reportUnresolved)
	}
	if report.Entries[0].ObservedRevision != 5 {
		t.Errorf("observed_revision = %d, want 5 (the current policy revision)", report.Entries[0].ObservedRevision)
	}
}

// TestScanNoMatchWhenPolicyGone: an orphaned intent whose target policy no
// longer exists is reported "no_match" — it cannot be attributed.
func TestScanNoMatchWhenPolicyGone(t *testing.T) {
	r, fa := newScanKit(t, nil) // "pol-gone" absent from the map
	const corr = "c-no-match"
	if _, err := fa.Append(storage.AuditEvent{Result: resultIntent, Target: "pol-gone", Revision: 1, CorrelationID: corr}); err != nil {
		t.Fatal(err)
	}
	report, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(report.Entries) != 1 {
		t.Fatalf("report len = %d, want 1", len(report.Entries))
	}
	if report.Entries[0].Status != reportNoMatch {
		t.Errorf("status = %q, want %q (target policy absent)", report.Entries[0].Status, reportNoMatch)
	}
}

// TestScanNeverAppends: Scan is pure read — it must not change the audit store
// by even a single row (MUST-17.3-B / ADR-038 §3.6).
func TestScanNeverAppends(t *testing.T) {
	r, fa := newScanKit(t, nil)
	if _, err := fa.Append(storage.AuditEvent{Result: resultIntent, Target: "x", Revision: 1, CorrelationID: "c"}); err != nil {
		t.Fatal(err)
	}
	before := len(fa.snapshot())
	if _, err := r.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	after := len(fa.snapshot())
	if before != after {
		t.Errorf("Scan mutated the audit store: %d -> %d rows", before, after)
	}
}

// TestReconcileForwardIsNoOp: the write seam is defined but not invoked in
// Phase 17.3; it appends nothing and reports unresolved (no attribution schema).
func TestReconcileForwardIsNoOp(t *testing.T) {
	r, fa := newScanKit(t, nil)
	before := len(fa.snapshot())
	entry := r.ReconcileForward(context.Background(), "c-x")
	after := len(fa.snapshot())
	if before != after {
		t.Errorf("ReconcileForward appended %d rows; it must be a no-op in Phase 17.3", after-before)
	}
	if entry.Status != reportUnresolved {
		t.Errorf("status = %q, want %q", entry.Status, reportUnresolved)
	}
}

// ---------------------------------------------------------------------------
// Replay guard (MUST-17.3-A, ADR-038 §3.4)
// ---------------------------------------------------------------------------

// TestReplayGuardStrictReject409: a client-supplied Idempotency-Key whose
// terminal outcome already exists is strictly rejected with 409 — no allow mode.
func TestReplayGuardStrictReject409(t *testing.T) {
	k := newKit(t)
	const key = "dup-key"

	// First mutation under the key succeeds (201) and records the key as the
	// correlation id of its intent+success rows.
	resp1, _ := k.do(call{
		method:  http.MethodPost,
		path:    RoutePrefix + "policies",
		body:    `{"policy_id":"pol-a","rules":[]}`,
		headers: map[string]string{HeaderIdempotencyKey: key},
	})
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first create = %d, want 201", resp1.StatusCode)
	}

	// Second mutation reusing the same key is rejected (409), regardless of
	// the (different) policy id.
	resp2, body2 := k.do(call{
		method:  http.MethodPost,
		path:    RoutePrefix + "policies",
		body:    `{"policy_id":"pol-b","rules":[]}`,
		headers: map[string]string{HeaderIdempotencyKey: key},
	})
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("replay = %d, want 409; body=%s", resp2.StatusCode, body2)
	}
	if !strings.Contains(body2, codeReplayConflict) {
		t.Errorf("replay response missing %q code: %s", codeReplayConflict, body2)
	}
}

// TestReplayGuardNoKeyAllowsDistinctRequests: without an Idempotency-Key the
// guard is a no-op and normal mutations proceed (the server generates a fresh
// correlation id per request).
func TestReplayGuardNoKeyAllowsDistinctRequests(t *testing.T) {
	k := newKit(t)
	resp1, _ := k.do(call{method: http.MethodPost, path: RoutePrefix + "policies", body: `{"policy_id":"pol-c","rules":[]}`})
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("create pol-c = %d, want 201", resp1.StatusCode)
	}
	resp2, _ := k.do(call{method: http.MethodPost, path: RoutePrefix + "policies", body: `{"policy_id":"pol-d","rules":[]}`})
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("create pol-d = %d, want 201", resp2.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Read-only audit surface (ADR-038 §3.3, R17-6 / R17-7)
// ---------------------------------------------------------------------------

// TestAuditReadSurfaceIsReadOnlyAndGated: both GET routes inherit the AuthN+
// AuthZ gate (unauthenticated → 401) and return 200 with a JSON array; the
// surface performs no write.
func TestAuditReadSurfaceIsReadOnlyAndGated(t *testing.T) {
	k := newKit(t)

	// Unauthenticated GET must be rejected fail-closed (R17-7).
	respUnauth, _ := k.do(call{method: http.MethodGet, path: RoutePrefix + "audit", noToken: true})
	if respUnauth.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauth GET /audit = %d, want 401", respUnauth.StatusCode)
	}

	// A mutation feeds the audit store; the read surface then reflects it.
	respX, _ := k.create("pol-x", "")
	if respX.StatusCode != http.StatusCreated {
		t.Fatalf("create pol-x = %d, want 201", respX.StatusCode)
	}

	// GET /audit returns the management trail.
	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "audit"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /audit = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var page storage.AuditPage
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("decode /audit: %v", err)
	}
	var forX int
	for _, e := range page.Events {
		if e.Target == "pol-x" {
			forX++
		}
	}
	if forX < 2 {
		t.Errorf("GET /audit returned %d rows for pol-x, want >= 2 (intent+outcome)", forX)
	}

	// GET /audit?policy=pol-x filters to that target only.
	respF, bodyF := k.do(call{method: http.MethodGet, path: RoutePrefix + "audit?policy=pol-x"})
	if respF.StatusCode != http.StatusOK {
		t.Fatalf("GET /audit?policy = %d, want 200; body=%s", respF.StatusCode, bodyF)
	}
	var filtered storage.AuditPage
	if err := json.Unmarshal([]byte(bodyF), &filtered); err != nil {
		t.Fatalf("decode filtered /audit: %v", err)
	}
	for _, e := range filtered.Events {
		if e.Target != "pol-x" {
			t.Errorf("filter leaked row with target %q", e.Target)
		}
	}

	// GET /reconciliation returns the Phase 18 status envelope, read-only.
	rresp, rbody := k.do(call{method: http.MethodGet, path: RoutePrefix + "reconciliation"})
	if rresp.StatusCode != http.StatusOK {
		t.Fatalf("GET /reconciliation = %d, want 200; body=%s", rresp.StatusCode, rbody)
	}
	var report ScanReport
	if err := json.Unmarshal([]byte(rbody), &report); err != nil {
		t.Fatalf("decode /reconciliation: %v", err)
	}
	if report.Status != scanVerified {
		t.Errorf("reconciliation status = %q, want %q", report.Status, scanVerified)
	}
}
