package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// --- T-H -------------------------------------------------------------------
// P31-I10: a Stat failure must surface as an explicit durable read failure,
// never as a silently skipped budget check that falls through to reading.
func TestDurableRead_StatErrorIsExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Remove the file after opening: Stat now fails, which must NOT be
	// swallowed into "budget check skipped, proceed to read".
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	res := store.ReadRecent(context.Background(), 10)
	if res.LoadErr == nil {
		t.Fatal("P31-I10: Stat failure must surface as a durable read error")
	}
	if errors.Is(res.LoadErr, ErrDurableBudgetExceeded) {
		t.Fatalf("P31-I10: Stat failure must not be reported as a budget issue: %v", res.LoadErr)
	}
	// And the API turns it into an explicit 503 (read_error), not a 200.
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	driveEdges(t, srv.alertTracker, 2, time.Now())
	srv.transitionStore = store
	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?source=durable", token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("P31-I10: Stat failure must be 503, got %d (body=%q)", w.Code, w.Body.String())
	}
}

// --- T-I -------------------------------------------------------------------
// P31-I11: a durable read must report what IT found, not the tracker's stale
// startup stats. Scenario: startup is clean, then the durable metadata record
// is corrupted afterwards — source=durable MUST surface the inconsistency.
func TestProtectionObs_History_DurableSurfacesFreshMetadataInconsistency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTrackerWithStore(store)
	srv.transitionStore = store
	driveEdges(t, srv.alertTracker, 3, time.Now())

	// Startup view is clean — this is the stale state we must NOT keep using.
	if srv.alertTracker.HistoryStats().RetentionMetaInconsistent {
		t.Fatal("precondition: startup should be meta-consistent")
	}

	// Corrupt the metadata record AFTER startup, without restarting the tracker.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	lines[0] = "{\"garbage\":true}" // valid JSON, but not a meta record
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite file: %v", err)
	}

	// The tracker still believes everything is clean (stale by construction).
	if srv.alertTracker.HistoryStats().RetentionMetaInconsistent {
		t.Fatal("precondition: tracker stats must remain stale-clean for this test")
	}

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?source=durable&limit=1000", token)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (best-effort recovery), got %d (body=%q)", w.Code, w.Body.String())
	}
	var body struct {
		Transitions  []map[string]any `json:"transitions"`
		HistoryStats map[string]any   `json:"history_stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The fresh read's finding must reach the response.
	if body.HistoryStats["retention_meta_inconsistent"] != true {
		t.Fatalf("P31-I11: durable read must surface the inconsistency it detected, got %v",
			body.HistoryStats["retention_meta_inconsistent"])
	}
	// ... and the result must not be labelled clean.
	if body.HistoryStats["read_status"] != "degraded" {
		t.Fatalf("P31-I11: read_status must be degraded (not ok), got %v", body.HistoryStats["read_status"])
	}
	// Records are still recovered best-effort (Phase 30 I10 semantics).
	if len(body.Transitions) != 3 {
		t.Fatalf("best-effort recovery should still return 3 transitions, got %d", len(body.Transitions))
	}
}
