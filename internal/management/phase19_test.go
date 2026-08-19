package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/governancepolicy"
	"github.com/YuDong999/opscore/internal/observability"
	"github.com/YuDong999/opscore/internal/plugin/sandbox"
	"github.com/YuDong999/opscore/internal/storage"
)

// ---------------------------------------------------------------------------
// Phase 19 — Evidence Consumers (ADR-042 §3)
//
// A consumer may summarize evidence, but it may never weaken the meaning of
// absence. The laws under test:
//   R19-1  every Phase 19 surface is GET, :8082-only, token-gated
//   R19-2  no new dependency; Prometheus hand-rolled; OTel out
//   R19-6  projection is read-time, derived, no new persistence
//   R19-7  metrics expose EXACT counters, never the bounded window
//   R19-8  audit is append-only; scan-history is in-memory and non-authoritative
//   (S-2)  cursor pagination is additive + backward compatible
// ---------------------------------------------------------------------------

// newKitWithCollectorCap builds the harness with an observability collector of
// a SPECIFIC capacity, so the exact-aggregate vs bounded-window divergence can
// be exercised (S-1, R19-7). New fails closed on a nil collector, so a real one
// is always wired.
func newKitWithCollectorCap(t *testing.T, cap int) *kit {
	t.Helper()
	repo, err := governancepolicy.NewFileRepository(t.TempDir())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	audit := &fakeAudit{}
	a, err := NewTokenAuthenticator(testToken, "op-1")
	if err != nil {
		t.Fatalf("authn: %v", err)
	}
	collector := observability.NewCollectorWithCapacity(cap)
	var seq int
	srv, err := New(Config{
		Repo:             repo,
		Audit:            audit,
		Authenticator:    a,
		Collector:        collector,
		NewCorrelationID: func() string { seq++; return fmt.Sprintf("corr-%d", seq) },
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &kit{t: t, repo: repo, audit: audit, ts: ts, srv: srv, collector: collector}
}

// TestRoutePatternsAreManagementScoped10: Phase 19 grows the set from 7 to 10
// by adding three READ-ONLY GET routes (metrics, projections/policy-activity,
// reconciliation/history); Phase 20 (ADR-045 §5) grows it from 10 to 11 by
// adding the traces read route. All remain inside /management/v1 — R19-1.
func TestRoutePatternsAreManagementScoped10(t *testing.T) {
	pats := RoutePatterns()
	if len(pats) != 11 {
		t.Fatalf("exported %d route patterns, want 11 (5 mutations + 2 Phase 18 reads + 3 Phase 19 reads + 1 Phase 20 traces, ADR-045 §5)", len(pats))
	}
	want := map[string]bool{
		"GET " + RoutePrefix + "metrics":                     true,
		"GET " + RoutePrefix + "projections/policy-activity": true,
		"GET " + RoutePrefix + "reconciliation/history":      true,
		"GET " + RoutePrefix + "traces":                      true,
	}
	seen := map[string]bool{}
	for _, p := range pats {
		if !strings.Contains(p, RoutePrefix) {
			t.Errorf("route %q is outside the %s namespace the harness isolates", p, RoutePrefix)
		}
		if want[p] {
			seen[p] = true
		}
	}
	for w := range want {
		if !seen[w] {
			t.Errorf("Phase 19 route %q missing from RoutePatterns()", w)
		}
	}
}

// ---------------------------------------------------------------------------
// S-1 — Metrics endpoint: exact counters, never a window
// ---------------------------------------------------------------------------

// TestMetricsExposesExactCountersNotWindow is the heart of S-1 (R19-7). The
// collector holds EXACT lifetime aggregates; the bounded window (Query/Count) is
// deliberately NOT exported as metrics. A consumer that scrapes the windowed
// sample would conclude "it never happened" from "no longer retained" — the
// false-clean this phase exists to forbid.
func TestMetricsExposesExactCountersNotWindow(t *testing.T) {
	k := newKitWithCollectorCap(t, 4) // small window so window != aggregate
	for i := 0; i < 20; i++ {
		k.collector.ObserveSandbox(sandbox.Decision{
			Allowed:   i%2 == 0,
			Operation: "op",
			Code:      "sandbox",
			Reason:    "test",
			Source:    "plugin:sandbox",
		})
	}
	// Exact allow counter = 10; the retained window still holds only 4 samples.
	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "metrics"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("content-type = %q, want text/plain (Prometheus exposition format)", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, "# HELP") || !strings.Contains(body, "# TYPE") {
		t.Errorf("metrics body missing HELP/TYPE exposition lines:\n%s", body)
	}
	// The EXACT aggregate is rendered (10), never the retained window (4).
	if !strings.Contains(body, `verdict_total{source="sandbox",verdict="allow"} 10`) {
		t.Errorf("exact verdict_total(allow) not rendered as 10 (R19-7):\n%s", body)
	}
	// DroppedCount is surfaced so eviction is counted, never hidden.
	if !strings.Contains(body, "opscore_observations_dropped 16") {
		t.Errorf("observations_dropped not rendered as 16 (20 ingested - 4 retained):\n%s", body)
	}
}

// TestMetricsEndpointFailureIs503: a nil collector is NOT a "0 metrics" answer.
// It must be 503 metrics_unavailable so a Prometheus scraper retries rather than
// recording an all-zero sample that would mask a real outage (Phase 18 false-
// clean, migrated to the consumer).
func TestMetricsEndpointFailureIs503(t *testing.T) {
	k := newKitWithCollectorCap(t, 100)
	k.srv.collector = nil // simulate the collector being unavailable
	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "metrics"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /metrics with nil collector = %d, want 503; body=%s", resp.StatusCode, body)
	}
	if got := decodeErr(t, body).Code; got != codeMetricsUnavailable {
		t.Errorf("error code = %q, want %q", got, codeMetricsUnavailable)
	}
}

// TestMetricsGenuineZeroStillRenders: a real zero is rendered as 0, not omitted.
// Omitting a zero would make "never happened" indistinguishable from "not
// scraped" in the client.
func TestMetricsGenuineZeroStillRenders(t *testing.T) {
	k := newKitWithCollectorCap(t, 100)
	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "metrics"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "opscore_observations_total 0") {
		t.Errorf("genuine zero not rendered as 0:\n%s", body)
	}
}

// ---------------------------------------------------------------------------
// S-2 — Cursor pagination (Additive AuditQuery.After)
// ---------------------------------------------------------------------------

// TestAuditCursorPagesBackward: ?after=N returns only rows with id < N, newest
// first, so a client can page through the trail deterministically (ADR-042 §3.2).
func TestAuditCursorPagesBackward(t *testing.T) {
	k := newKit(t)
	for i := 0; i < 5; i++ {
		if resp, _ := k.create("cur-"+string(rune('a'+i)), ""); resp.StatusCode != http.StatusCreated {
			t.Fatalf("seed %d = %d", i, resp.StatusCode)
		}
	}
	rows := k.audit.snapshot()
	mid := rows[len(rows)/2].ID

	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "audit?after=" + strconv.FormatInt(mid, 10)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /audit?after = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var page storage.AuditPage
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Events) == 0 {
		t.Fatal("cursor returned no rows — backward paging produced nothing")
	}
	for _, e := range page.Events {
		if e.ID >= mid {
			t.Errorf("cursor leaked row id=%d (after=%d) — must be id < after", e.ID, mid)
		}
	}
}

// TestAuditCursorBackwardCompatible: omitting ?after behaves exactly as today
// (After=0 is a wildcard, not a filter). The existing contract is unchanged.
func TestAuditCursorBackwardCompatible(t *testing.T) {
	k := newKit(t)
	for i := 0; i < 3; i++ {
		k.create("bc-"+string(rune('a'+i)), "")
	}
	withAfter, ab := k.do(call{method: http.MethodGet, path: RoutePrefix + "audit?after=0"})
	without, wb := k.do(call{method: http.MethodGet, path: RoutePrefix + "audit"})
	if withAfter.StatusCode != http.StatusOK || without.StatusCode != http.StatusOK {
		t.Fatalf("audit = %d / %d, want 200/200", withAfter.StatusCode, without.StatusCode)
	}
	var pa, pb storage.AuditPage
	json.Unmarshal([]byte(ab), &pa)
	json.Unmarshal([]byte(wb), &pb)
	if len(pa.Events) != len(pb.Events) {
		t.Errorf("after=0 events=%d, no-after events=%d — must be identical (R19-5)", len(pa.Events), len(pb.Events))
	}
}

// TestAuditCursorTruncationMeansOlder: when the cursor window is truncated, the
// envelope says so. "No rows past the cursor" and "there are no older rows" are
// DIFFERENT claims.
func TestAuditCursorTruncationMeansOlder(t *testing.T) {
	k := newKit(t)
	for i := 0; i < 10; i++ {
		k.create("tr-"+string(rune('a'+i)), "")
	}
	rows := k.audit.snapshot()
	first := rows[0].ID // oldest
	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "audit?after=" + strconv.FormatInt(first, 10)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /audit?after = %d, want 200", resp.StatusCode)
	}
	var page storage.AuditPage
	json.Unmarshal([]byte(body), &page)
	if page.Truncated {
		t.Error("page past the oldest row is marked truncated — there is nothing older, so it must be complete")
	}
	if len(page.Events) != 0 {
		t.Errorf("rows = %d, want 0 (nothing is older than the oldest)", len(page.Events))
	}
}

// ---------------------------------------------------------------------------
// S-3 — Policy activity projection (read-time, carries truncation)
// ---------------------------------------------------------------------------

// TestPolicyActivityProjectionCarriesTruncation is the S-3 guard (R19-6). The
// projection is derived from a single bounded AuditQuery; the window's
// Truncated flag MUST reach the response. A projection without search scope
// would recreate the Phase 18 false-clean defect.
func TestPolicyActivityProjectionCarriesTruncation(t *testing.T) {
	k := newKitWithCollectorCap(t, 100)
	for i := 0; i < storage.MaxAuditQueryLimit+5; i++ {
		k.create("pa-"+string(rune('a'+i%3)), "")
	}
	// Capture the row count BEFORE the projection, so the assertion below
	// proves the projection read the EXISTING store only and created nothing.
	// (Each k.create issues a full mutation → intent + outcome = two audit rows,
	// so we compare against the pre-GET snapshot rather than an assumed count.)
	before := len(k.audit.snapshot())
	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "projections/policy-activity"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /projections/policy-activity = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var proj PolicyActivityProjection
	if err := json.Unmarshal([]byte(body), &proj); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !proj.Truncated {
		t.Error("projection.Truncated = false, want true — the window over MaxAuditQueryLimit rows is truncated")
	}
	if !proj.Window.Truncated {
		t.Error("projection.window.Truncated = false, want true (mandatory redundancy)")
	}
	if len(proj.Policies) == 0 {
		t.Error("projection returned no per-policy activity despite traffic")
	}
	// The projection read the EXISTING store only — it created nothing.
	if n := len(k.audit.snapshot()); n != before {
		t.Errorf("store rows = %d, want unchanged (%d) — projection must not persist anything", n, before)
	}
}

// TestPolicyActivityReadsExistingStoreOnly: the projection issues one audit
// Query; it does NOT invent a synchronizer, cache, or background projector
// (R19-6). Failure to read is 503 evidence_unavailable, not 200 with empty.
func TestPolicyActivityReadsExistingStoreOnly(t *testing.T) {
	k := newKitWithCollectorCap(t, 100)
	k.audit.failRead = errAuditDown
	resp, body := k.do(call{method: http.MethodGet, path: RoutePrefix + "projections/policy-activity"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /projections/policy-activity with dead store = %d, want 503; body=%s", resp.StatusCode, body)
	}
	if got := decodeErr(t, body).Code; got != codeEvidenceUnavailable {
		t.Errorf("error code = %q, want %q", got, codeEvidenceUnavailable)
	}
}

// ---------------------------------------------------------------------------
// S-4 — Scan history ring (bounded, non-authoritative)
// ---------------------------------------------------------------------------

// TestScanHistoryEvictsAndFlagsTruncated: every Scan pushes to a bounded ring;
// once it laps, the oldest reports are evicted and Truncated becomes true.
// Absence from the ring is NOT absence from history.
func TestScanHistoryEvictsAndFlagsTruncated(t *testing.T) {
	r, _ := newScanKit(t, nil)
	const ringCap = 3
	r.SetScanHistoryCap(ringCap)
	for i := 0; i < ringCap+4; i++ {
		if _, err := r.Scan(context.Background()); err != nil {
			t.Fatalf("scan %d: %v", i, err)
		}
	}
	hist := r.ScanHistory()
	if len(hist.Reports) != ringCap {
		t.Errorf("ring holds %d reports, want capacity %d (FIFO eviction)", len(hist.Reports), ringCap)
	}
	if !hist.Truncated {
		t.Error("history.Truncated = false, want true — the ring lapped, older scans are gone")
	}
	if hist.Capacity != ringCap {
		t.Errorf("capacity = %d, want %d", hist.Capacity, ringCap)
	}
}

// TestScanHistoryAbsenceNotCompleteness: a fresh reconciler with no scans run
// yields an empty, NON-truncated history. Empty != "all history lost"; it means
// "no scan has run yet". The flag distinguishes the two.
func TestScanHistoryAbsenceNotCompleteness(t *testing.T) {
	r, _ := newScanKit(t, nil)
	hist := r.ScanHistory()
	if len(hist.Reports) != 0 {
		t.Errorf("fresh history has %d reports, want 0", len(hist.Reports))
	}
	if hist.Truncated {
		t.Error("fresh history marked truncated — an empty ring means 'no scan yet', not 'history evicted'")
	}
}

// TestScanHistoryPushedByStartupScan: ScanAtStartup triggers Scan, which pushes
// to the ring, so the history surface reflects the startup pass without any
// request (ADR-042 §3.4 / R19-8).
func TestScanHistoryPushedByStartupScan(t *testing.T) {
	k := newKitWithCollectorCap(t, 100)
	k.srv.ScanAtStartup(context.Background())
	hist := k.srv.reconciler.ScanHistory()
	if len(hist.Reports) != 1 {
		t.Errorf("startup scan pushed %d reports, want 1", len(hist.Reports))
	}
}
