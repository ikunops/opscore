package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/YuDong999/opscore/internal/governancepolicy"
	"github.com/YuDong999/opscore/internal/storage"
)

// ---------------------------------------------------------------------------
// Phase 18 — Evidence Integrity (ADR-040 §3.2)
//
// The laws under test:
//   R18-1  no success-shaped empty result on a failed read
//   R18-2  every capped read reports its window
//   R18-3  no new mutation
// ---------------------------------------------------------------------------

var errAuditDown = errors.New("audit store unavailable")

// TestScanVerifiedOnCompleteWindow: a clean trail that fits under the cap is
// "verified" — and only then does an empty Entries list mean "nothing wrong".
func TestScanVerifiedOnCompleteWindow(t *testing.T) {
	r, fa := newScanKit(t, nil)
	for i := 0; i < 5; i++ {
		mustAppend(t, fa, storage.AuditEvent{Result: resultSuccess, Target: "p", CorrelationID: "c"})
	}
	report, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if report.Status != scanVerified {
		t.Errorf("Status = %q, want %q", report.Status, scanVerified)
	}
	if report.Window.Truncated {
		t.Error("Window.Truncated = true, want false: 5 rows fit under the cap")
	}
	if report.Window.Scanned != 5 {
		t.Errorf("Window.Scanned = %d, want 5", report.Window.Scanned)
	}
	if report.Window.Cap != scanCap {
		t.Errorf("Window.Cap = %d, want %d — the ceiling must be stated", report.Window.Cap, scanCap)
	}
	if len(report.Entries) != 0 {
		t.Errorf("Entries = %d, want 0 for a clean trail", len(report.Entries))
	}
	if report.Entries == nil {
		t.Error("Entries must be a non-nil empty slice so it marshals as [] not null")
	}
}

// TestScanStoreFailureIsUnknown is the heart of the phase. A failed audit read
// must NOT look like a healthy trail. Phase 17.3 returned nil here, which the
// handler turned into `200 []` — a fabricated all-clear (ADR-039 §2 F-1).
func TestScanStoreFailureIsUnknown(t *testing.T) {
	r, fa := newScanKit(t, nil)
	mustAppend(t, fa, storage.AuditEvent{Result: resultIntent, Target: "p", CorrelationID: "c"})
	fa.failRead = errAuditDown

	report, err := r.Scan(context.Background())
	if err == nil {
		t.Fatal("Scan returned nil error on a failed store read — the caller cannot distinguish 'clean' from 'unreadable'")
	}
	if report.Status != scanUnknown {
		t.Errorf("Status = %q, want %q — a failed read is NEVER verified", report.Status, scanUnknown)
	}
	if len(report.Entries) != 0 {
		t.Errorf("Entries = %d, want 0: an unknown scan carries no information", len(report.Entries))
	}
	if report.Window.Reason == "" {
		t.Error("Window.Reason must explain why the scan is unknown")
	}
}

// TestScanTruncatedIsFlagged: when the window hits the cap, an empty Entries
// list means "nothing found IN THE NEWEST N ROWS" — a different claim, and the
// report must say so (R18-2).
func TestScanTruncatedIsFlagged(t *testing.T) {
	r, fa := newScanKit(t, nil)
	for i := 0; i < scanCap+1; i++ {
		mustAppend(t, fa, storage.AuditEvent{Result: resultSuccess, Target: "p", CorrelationID: "c"})
	}
	report, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if report.Status != scanTruncated {
		t.Errorf("Status = %q, want %q — %d rows exceed cap %d", report.Status, scanTruncated, scanCap+1, scanCap)
	}
	if !report.Window.Truncated {
		t.Error("Window.Truncated = false, want true")
	}
	if report.Window.Scanned != scanCap {
		t.Errorf("Window.Scanned = %d, want %d", report.Window.Scanned, scanCap)
	}
}

// TestScanUnexaminableRowIsReportedNotDropped: a per-row evidence read failure
// must surface as an entry, not as silence. Phase 17.3 `continue`d, so the
// intent simply vanished from the report (ADR-039 §2 F-1, second site).
func TestScanUnexaminableRowIsReportedNotDropped(t *testing.T) {
	r, fa := newScanKit(t, nil)
	mustAppend(t, fa, storage.AuditEvent{Result: resultIntent, Target: "p-bad", CorrelationID: "c-bad"})
	fa.failCorr = map[string]error{"c-bad": errAuditDown}

	report, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v (a per-row failure is not a scan failure)", err)
	}
	if report.Status != scanVerified {
		t.Errorf("Status = %q, want %q — the scan itself completed; per-item truth belongs on the item",
			report.Status, scanVerified)
	}
	if len(report.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1 — the unreadable intent must be REPORTED, never dropped", len(report.Entries))
	}
	if report.Entries[0].Status != reportUnexaminable {
		t.Errorf("entry status = %q, want %q", report.Entries[0].Status, reportUnexaminable)
	}
	if report.Entries[0].CorrelationID != "c-bad" {
		t.Errorf("entry correlation = %q, want %q", report.Entries[0].CorrelationID, "c-bad")
	}
	if report.Window.Unexaminable != 1 {
		t.Errorf("Window.Unexaminable = %d, want 1", report.Window.Unexaminable)
	}
}

// TestScanStillDoesNotMutateUnderFailure: R18-3 holds on every path, including
// the new failure paths.
func TestScanStillDoesNotMutateUnderFailure(t *testing.T) {
	r, fa := newScanKit(t, map[string]governancepolicy.PolicyRecord{"p": {PolicyID: "p", Revision: 3}})
	mustAppend(t, fa, storage.AuditEvent{Result: resultIntent, Target: "p", CorrelationID: "c1"})
	mustAppend(t, fa, storage.AuditEvent{Result: resultIntent, Target: "gone", CorrelationID: "c2"})
	fa.failCorr = map[string]error{"c2": errAuditDown}
	before := len(fa.snapshot())

	if _, err := r.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if after := len(fa.snapshot()); after != before {
		t.Errorf("Scan mutated the audit store: %d -> %d rows", before, after)
	}
}

// ---------------------------------------------------------------------------
// HTTP surface — one failure rule for every evidence call site (ADR-040 §3.2)
// ---------------------------------------------------------------------------

// TestReconciliationEndpointFailureIs503: an unreadable audit store must yield
// 503 evidence_unavailable, never `200 []`. 503 tells an operator "retry, do
// not conclude"; 200-with-empty-body tells them "all clear", which is a lie.
func TestReconciliationEndpointFailureIs503(t *testing.T) {
	k := newKit(t)
	k.audit.failRead = errAuditDown

	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "reconciliation"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /reconciliation with a dead store = %d, want 503; body=%s", resp.StatusCode, body)
	}
	if got := decodeErr(t, body).Code; got != codeEvidenceUnavailable {
		t.Errorf("error code = %q, want %q", got, codeEvidenceUnavailable)
	}
}

// TestAuditEndpointFailureIs503: the same rule on the audit surface. Phase 17.3
// answered 500 here; a well-formed request against a healthy server whose
// evidence is momentarily unreadable is not an internal error.
func TestAuditEndpointFailureIs503(t *testing.T) {
	k := newKit(t)
	k.audit.failRead = errAuditDown

	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "audit"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /audit with a dead store = %d, want 503; body=%s", resp.StatusCode, body)
	}
	if got := decodeErr(t, body).Code; got != codeEvidenceUnavailable {
		t.Errorf("error code = %q, want %q", got, codeEvidenceUnavailable)
	}
}

// TestAuditEndpointReturnsWindowEnvelope: the response carries the window
// metadata, which a bare array is structurally incapable of expressing
// (ADR-040 §5).
func TestAuditEndpointReturnsWindowEnvelope(t *testing.T) {
	k := newKit(t)
	if resp, _ := k.create("pol-env", ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed create = %d, want 201", resp.StatusCode)
	}

	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "audit?policy=pol-env&limit=1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /audit = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var page storage.AuditPage
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, body)
	}
	if page.Limit != 1 {
		t.Errorf("limit = %d, want 1 (the effective limit must be echoed)", page.Limit)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(page.Events))
	}
	if !page.Truncated {
		t.Error("truncated = false, want true: the create wrote intent+outcome, so more matches exist beyond limit=1")
	}
	if page.Events[0].Target != "pol-env" {
		t.Errorf("target = %q, want pol-env", page.Events[0].Target)
	}
}

// TestAuditEndpointFiltersBeforeLimit: the endpoint-level expression of the
// F-3 defect. "pol-old" is written first and buried under later traffic; a
// small limit must still find it.
func TestAuditEndpointFiltersBeforeLimit(t *testing.T) {
	k := newKit(t)
	if resp, _ := k.create("pol-old", ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed pol-old = %d, want 201", resp.StatusCode)
	}
	for i := 0; i < 10; i++ {
		if resp, _ := k.create("pol-noise-"+string(rune('a'+i)), ""); resp.StatusCode != http.StatusCreated {
			t.Fatalf("seed noise %d = %d, want 201", i, resp.StatusCode)
		}
	}

	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "audit?policy=pol-old&limit=5"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /audit = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var page storage.AuditPage
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Events) == 0 {
		t.Fatal("GET /audit?policy=pol-old&limit=5 returned no events — the oldest policy became invisible " +
			"because the limit was applied before the predicate (ADR-039 §2 F-3)")
	}
	for _, e := range page.Events {
		if e.Target != "pol-old" {
			t.Errorf("predicate leaked row with target %q", e.Target)
		}
	}
}

// TestReconciliationEndpointReturnsStatusEnvelope: the healthy path carries the
// three-state status and window so `entries: []` is interpretable.
func TestReconciliationEndpointReturnsStatusEnvelope(t *testing.T) {
	k := newKit(t)
	if resp, _ := k.create("pol-rec", ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed create = %d, want 201", resp.StatusCode)
	}

	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "reconciliation"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /reconciliation = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var report ScanReport
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, body)
	}
	if report.Status != scanVerified {
		t.Errorf("status = %q, want %q", report.Status, scanVerified)
	}
	if report.Window.Cap != scanCap {
		t.Errorf("window.cap = %d, want %d", report.Window.Cap, scanCap)
	}
	if report.Entries == nil {
		t.Error("entries must marshal as [] not null")
	}
}

func mustAppend(t *testing.T, fa *fakeAudit, e storage.AuditEvent) {
	t.Helper()
	if _, err := fa.Append(e); err != nil {
		t.Fatalf("append: %v", err)
	}
}
