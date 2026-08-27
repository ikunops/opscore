package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// TestFileBackedTransitionStore_CapEviction (T2) proves the durable layer is
// bounded: writing >FileTransitionCapacity evicts the oldest records, increments
// file_dropped, and keeps exactly cap retained. Reopening the SAME file yields
// a consistent file_dropped (P30-I10 crash-consistency): content and metadata
// can never disagree.
func TestFileBackedTransitionStore_CapEviction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	s, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	n := protection.FileTransitionCapacity + 3
	for i := 0; i < n; i++ {
		if err := s.Append(context.Background(), protection.AlertTransition{
			At: time.Now(), From: i%2 == 0, To: i%2 == 1, UnknownRate: int64(i), Threshold: 50,
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	res := s.Load(context.Background())
	if res.LoadErr != nil {
		t.Fatalf("load: %v", res.LoadErr)
	}
	if res.FileDropped <= 0 {
		t.Fatalf("want file_dropped>0 after over-cap writes, got %d", res.FileDropped)
	}
	if len(res.Transitions) != protection.FileTransitionCapacity {
		t.Fatalf("want %d retained, got %d", protection.FileTransitionCapacity, len(res.Transitions))
	}
	if res.RetentionMetaInconsistent {
		t.Fatal("meta should be consistent after normal writes")
	}

	// Crash-consistency: reopen and verify the metadata count matches.
	s2, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	res2 := s2.Load(context.Background())
	if res2.FileDropped != res.FileDropped {
		t.Fatalf("P30-I10: file_dropped inconsistent across reopen: %d vs %d", res2.FileDropped, res.FileDropped)
	}
	if len(res2.Transitions) != protection.FileTransitionCapacity {
		t.Fatalf("P30-I10: reopen retained mismatch: %d", len(res2.Transitions))
	}
}

// TestFileBackedTransitionStore_Ordered (T3) proves P30-I9: the on-disk order
// matches the observation order. A sequence written is reloaded in identical
// order (no goroutine reordering).
func TestFileBackedTransitionStore_Ordered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	s, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	tos := []bool{false, true, false, true, true, false}
	prev := false
	for i, to := range tos {
		if err := s.Append(context.Background(), protection.AlertTransition{
			At: time.Now(), From: i > 0 && prev, To: to, UnknownRate: int64(i), Threshold: 50,
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		prev = to
	}
	res := s.Load(context.Background())
	if len(res.Transitions) != len(tos) {
		t.Fatalf("want %d transitions, got %d", len(tos), len(res.Transitions))
	}
	for i, wantTo := range tos {
		if res.Transitions[i].To != wantTo {
			t.Fatalf("P30-I9: order mismatch at %d: got To=%v want %v", i, res.Transitions[i].To, wantTo)
		}
	}
}

// TestFileBackedTransitionStore_MetaInconsistent (T5) proves P30-I10: a garbage
// (non-meta) first line is surfaced as RetentionMetaInconsistent=true honestly —
// never coerced to 0 / false-clean — while the valid transition line is still
// recovered best-effort.
func TestFileBackedTransitionStore_MetaInconsistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	bad := "{\"garbage\":true}\n" +
		"{\"at\":\"2020-01-01T00:00:00Z\",\"from\":false,\"to\":true,\"unknown_rate\":1,\"threshold\":50}\n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	s, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	res := s.Load(context.Background())
	if !res.RetentionMetaInconsistent {
		t.Fatal("expected retention_meta_inconsistent=true on garbage meta line")
	}
	if len(res.Transitions) != 1 {
		t.Fatalf("should recover the 1 valid transition, got %d", len(res.Transitions))
	}
}

// TestProtectionObs_AlertsHistory_FileBackedPersistence (T6, Phase 30) proves
// the wired file-backed tracker (a) exposes the extended history_stats fields
// over the read API, and (b) survives a simulated restart: a new tracker built
// from the SAME durable file recovers the transitions and reconstructs firing.
func TestProtectionObs_AlertsHistory_FileBackedPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alert-tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTrackerWithStore(store)

	// Drive two genuine edges into the wired tracker.
	srv.alertTracker.Observe(protection.AlertCondition{Firing: true, UnknownRate: 70, Threshold: 50, Window: time.Minute}, time.Now())
	srv.alertTracker.Observe(protection.AlertCondition{Firing: false, UnknownRate: 0, Threshold: 50, Window: time.Minute}, time.Now().Add(time.Second))

	h := srv.ProtectionReadMux()
	w := doReq(h, http.MethodGet, "/management/v1/protection/alerts/history", token)
	if w.Code != http.StatusOK {
		t.Fatalf("alerts/history admin: want 200 got %d (body=%q)", w.Code, w.Body.String())
	}
	var body struct {
		Transitions  []map[string]any `json:"transitions"`
		HistoryStats map[string]any   `json:"history_stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%q)", err, w.Body.String())
	}
	if len(body.Transitions) != 2 {
		t.Fatalf("want 2 transitions, got %d", len(body.Transitions))
	}
	hs := body.HistoryStats
	for _, k := range []string{"file_dropped", "retention_meta_inconsistent", "available", "load_error"} {
		if _, ok := hs[k]; !ok {
			t.Fatalf("history_stats must expose %q (Phase 30), got %+v", k, hs)
		}
	}
	if hs["retention_meta_inconsistent"] != false {
		t.Fatalf("fresh file should be meta-consistent, got %v", hs["retention_meta_inconsistent"])
	}
	if hs["available"] != true {
		t.Fatalf("available must be true, got %v", hs["available"])
	}
	if hs["load_error"] != false {
		t.Fatalf("load_error must be false, got %v", hs["load_error"])
	}

	// Simulate restart: brand-new tracker from the SAME durable file.
	store2, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	restarted := protection.NewAlertTrackerWithStore(store2)
	// P30-I7: firing reconstructs from last transition's To (false).
	if restarted.State().Firing {
		t.Fatal("restart: firing must reconstruct from last To=false")
	}
	if len(restarted.Transitions()) != 2 {
		t.Fatalf("restart: want 2 persisted transitions, got %d", len(restarted.Transitions()))
	}
	if !restarted.HistoryStats().Available {
		t.Fatal("restart: available must be true after clean reload")
	}
}
