package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Phase 33 — Durable Alert-Transition History Export (T-A .. T-E)
// ---------------------------------------------------------------------------

// T-A: the export must return the FULL retained durable history — more than the
// 256-entry runtime ring AND more than the 1000-per-page cap (the exact gap
// Phase 33 closes). It must be NEWEST-FIRST and carry honest history_stats.
func TestProtectionObs_HistoryExport_FullRetainedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTrackerWithStore(store)
	srv.transitionStore = store

	const n = 1024 // > ring(256) AND > page cap(1000)
	driveEdges(t, srv.alertTracker, n, time.Now())

	h := srv.ProtectionReadMux()
	w := doReq(h, http.MethodGet, "/management/v1/protection/alerts/history/export?format=json", token)
	if w.Code != http.StatusOK {
		t.Fatalf("export: want 200 got %d (body=%q)", w.Code, w.Body.String())
	}
	var body struct {
		Schema              string            `json:"schema"`
		ExportCompleteness  string            `json:"export_completeness"`
		Transitions         []map[string]any  `json:"transitions"`
		HistoryStats        map[string]any    `json:"history_stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%q)", err, w.Body.String())
	}
	if len(body.Transitions) != n {
		t.Fatalf("P33: export must return all %d retained transitions (not clamped to the 1000 page cap), got %d",
			n, len(body.Transitions))
	}
	if body.Schema != "alert-transition/export/v1" {
		t.Fatalf("P33-I3: schema must be alert-transition/export/v1, got %q", body.Schema)
	}
	if body.ExportCompleteness != "retained-snapshot" {
		t.Fatalf("P33-I2: completeness must be retained-snapshot (never a false zero-loss claim), got %q", body.ExportCompleteness)
	}
	if body.HistoryStats["retained"] != float64(n) {
		t.Fatalf("P33-I2: retained must be %d, got %v", n, body.HistoryStats["retained"])
	}
	// Durable layer is clean (no eviction yet): file_dropped must be 0. The
	// runtime ring (cap 256) DID drop 1024-256=768 records, so truncated=true is
	// the HONEST union-history signal — the export itself is complete
	// (retained=1024), but the live ring lost older entries. Asserting both
	// proves the export surfaces durable honesty without masking the runtime
	// drop (P33-I2 / P31-I11).
	if body.HistoryStats["file_dropped"] != float64(0) {
		t.Fatalf("P33-I2: no durable eviction yet, file_dropped must be 0, got %v", body.HistoryStats["file_dropped"])
	}
	if body.HistoryStats["truncated"] != true {
		t.Fatalf("P33-I2: runtime ring dropped records, truncated must be true, got %v", body.HistoryStats["truncated"])
	}
	// NEWEST-FIRST: last edge (i=n-1, odd => Firing=false) is CLEAR; first edge FIRING.
	if body.Transitions[0]["kind"] != "CLEAR" {
		t.Fatalf("P33: newest must be CLEAR, got %v", body.Transitions[0]["kind"])
	}
	if body.Transitions[n-1]["kind"] != "FIRING" {
		t.Fatalf("P33: oldest must be FIRING, got %v", body.Transitions[n-1]["kind"])
	}
}

// T-B: CSV export is parseable — header + N data rows + '#' metadata lines that
// a consumer can ignore (P33-I5, mirrors R127-fix-3).
func TestProtectionObs_HistoryExport_CSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTrackerWithStore(store)
	srv.transitionStore = store

	const n = 50
	driveEdges(t, srv.alertTracker, n, time.Now())

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history/export?format=csv", token)
	if w.Code != http.StatusOK {
		t.Fatalf("csv export: want 200 got %d (body=%q)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Fatalf("csv export must set text/csv content-type, got %q", ct)
	}
	raw := w.Body.String()
	if !strings.Contains(raw, "# schema=alert-transition/export/v1") {
		t.Fatalf("csv must carry '# schema=alert-transition/export/v1' metadata line")
	}
	if !strings.Contains(raw, "# export_completeness=retained-snapshot") {
		t.Fatalf("csv must carry '# export_completeness=retained-snapshot'")
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if lines[0] != "at,from,to,kind,unknown_rate,threshold" {
		t.Fatalf("csv header mismatch: %q", lines[0])
	}
	data := 0
	for _, ln := range lines[1:] {
		if strings.HasPrefix(ln, "#") {
			continue // metadata line, ignorable
		}
		data++
	}
	if data != n {
		t.Fatalf("csv must carry %d data rows, got %d", n, data)
	}
	// n=50 < ring cap(256): no runtime drop, no durable eviction -> the durable
	// layer is honestly clean (P33-I2).
	if !strings.Contains(raw, "# file_dropped=0") {
		t.Fatalf("csv must report file_dropped=0 for a clean store")
	}
	if !strings.Contains(raw, "# truncated=false") {
		t.Fatalf("csv must report truncated=false when nothing was dropped")
	}
}

// T-C: durable store unconfigured (memory mode) -> 503 explicit, NEVER a
// 200-with-memory fallback (P33-I1 / P31-I9).
func TestProtectionObs_HistoryExport_Unconfigured503(t *testing.T) {
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	driveEdges(t, srv.alertTracker, 5, time.Now())
	srv.transitionStore = nil // memory mode

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history/export?format=json", token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("P33-I1: unconfigured durable must be 503, got %d (body=%q)", w.Code, w.Body.String())
	}
	var body struct {
		Reason     string `json:"reason"`
		ReadSource string `json:"read_source"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Reason != "not_configured" || body.ReadSource != "durable" {
		t.Fatalf("P33-I1: reason/read_source must be not_configured/durable, got %q/%q", body.Reason, body.ReadSource)
	}
}

// T-D: degraded (FailedTransitionStore) -> 503 explicit, same discipline as T-C.
func TestProtectionObs_HistoryExport_Degraded503(t *testing.T) {
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	driveEdges(t, srv.alertTracker, 4, time.Now())
	srv.transitionStore = NewFailedTransitionStore("boom.jsonl", errors.New("disk on fire"))

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history/export?format=json", token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("P33-I1: degraded durable must be 503, got %d (body=%q)", w.Code, w.Body.String())
	}
}

// T-E: store-level ReadAll returns EVERY retained record (no 1000 clamp) and is
// NEWEST-FIRST. This is the core gap fix at the storage boundary.
func TestFileBackedTransitionStore_ReadAll_NoClamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	const n = 1024
	base := time.Now()
	for i := 0; i < n; i++ {
		tr := protection.AlertTransition{
			At:          base.Add(time.Duration(i) * time.Second),
			From:        i%2 == 1,
			To:          i%2 == 0,
			UnknownRate: 70,
			Threshold:   50,
		}
		if err := store.Append(context.Background(), tr); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	res := store.ReadAll(context.Background())
	if res.LoadErr != nil {
		t.Fatalf("ReadAll LoadErr: %v", res.LoadErr)
	}
	if res.Corrupt {
		t.Fatalf("ReadAll must not report corrupt on a clean file")
	}
	if len(res.Transitions) != n {
		t.Fatalf("ReadAll must return all %d retained (no 1000 clamp), got %d", n, len(res.Transitions))
	}
	// NEWEST-FIRST.
	for i := 1; i < len(res.Transitions); i++ {
		if !res.Transitions[i-1].At.After(res.Transitions[i].At) {
			t.Fatalf("ReadAll must be newest-first; idx %d not strictly newer than %d", i-1, i)
		}
	}
}
