package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// --- T-J -------------------------------------------------------------------
// P31-I11 follow-up: the durable response refreshes file_dropped from THIS
// read, so the DERIVED truncated flag must be recomputed from the same fresh
// values. Truncated is defined as
//
//	runtime_dropped > 0 || file_dropped > 0 || retention_meta_inconsistent
//
// If it keeps the value computed at startup from stale counters, a durable
// response can report file_dropped > 0 and truncated = false at the same time —
// a self-contradictory, false-clean statement.
func TestProtectionObs_History_DurableTruncatedTracksFreshFileDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTrackerWithStore(store)
	srv.transitionStore = store
	driveEdges(t, srv.alertTracker, 3, time.Now())

	// Startup view: no durable evictions, so truncated is false.
	if srv.alertTracker.HistoryStats().Truncated {
		t.Fatal("precondition: startup should be untruncated")
	}

	// Simulate evictions happening AFTER startup: rewrite the metadata record
	// with file_dropped > 0, without restarting the tracker.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	lines[0] = "{\"_meta\":true,\"file_dropped\":7}"
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite file: %v", err)
	}

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?source=durable&limit=1000", token)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%q)", w.Code, w.Body.String())
	}
	var body struct {
		HistoryStats map[string]any `json:"history_stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	fd, _ := body.HistoryStats["file_dropped"].(float64)
	if fd != 7 {
		t.Fatalf("durable read must report the fresh file_dropped=7, got %v", fd)
	}
	// The derived flag must agree with the fresh file_dropped.
	if body.HistoryStats["truncated"] != true {
		t.Fatalf("P31-I11: truncated must be recomputed from THIS read's file_dropped "+
			"(file_dropped=%v but truncated=%v — self-contradictory)",
			fd, body.HistoryStats["truncated"])
	}
}
