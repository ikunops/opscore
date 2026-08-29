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

// ---------------------------------------------------------------------------
// Phase 31.2 — Durable Alert History Read Projection (T-A .. T-G)
// ---------------------------------------------------------------------------

// driveEdges pushes n genuine edges through the tracker so BOTH the runtime
// ring and the durable file receive them. It alternates the firing condition,
// which produces one edge per Observe (P29-M2).
func driveEdges(t *testing.T, tr *protection.AlertTracker, n int, start time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		tr.Observe(protection.AlertCondition{
			Firing: i%2 == 0, UnknownRate: 70, Threshold: 50, Window: time.Minute,
		}, start.Add(time.Duration(i)*time.Second))
	}
}

// --- T-A -------------------------------------------------------------------
// The durable read must return MORE than the 256-entry runtime ring, taken
// from the durable dataset directly (R141: never rebuild-the-ring-then-take).
func TestProtectionObs_History_DurableBeyondRing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTrackerWithStore(store)
	srv.transitionStore = store

	const n = 300 // > TransitionHistoryCapacity(256)
	driveEdges(t, srv.alertTracker, n, time.Now())

	h := srv.ProtectionReadMux()
	w := doReq(h, http.MethodGet, "/management/v1/protection/alerts/history?source=durable&limit=1000", token)
	if w.Code != http.StatusOK {
		t.Fatalf("durable read: want 200 got %d (body=%q)", w.Code, w.Body.String())
	}
	var body struct {
		Transitions  []map[string]any `json:"transitions"`
		HistoryStats map[string]any   `json:"history_stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%q)", err, w.Body.String())
	}
	if len(body.Transitions) != n {
		t.Fatalf("P31: durable read must return all %d persisted transitions (not capped at the %d ring), got %d",
			n, protection.TransitionHistoryCapacity, len(body.Transitions))
	}
	// NEWEST-FIRST (P31-I5): the final edge was CLEAR (true->false) because
	// n-1 = 299 is odd => firing=false, and the first edge was FIRING.
	if got := body.Transitions[0]["kind"]; got != "CLEAR" {
		t.Fatalf("P31-I5: newest must be CLEAR, got %v", got)
	}
	if got := body.Transitions[n-1]["kind"]; got != "FIRING" {
		t.Fatalf("P31-I5: oldest must be FIRING, got %v", got)
	}
	hs := body.HistoryStats
	if hs["read_source"] != "durable" || hs["read_status"] != "ok" {
		t.Fatalf("P31-I4: read_source/read_status must be durable/ok, got %v/%v", hs["read_source"], hs["read_status"])
	}
	if hs["durable_retained"] != float64(n) {
		t.Fatalf("P31-I4: durable_retained must be %d, got %v", n, hs["durable_retained"])
	}
	// Sanity: the memory source is still capped by the ring.
	wm := doReq(h, http.MethodGet, "/management/v1/protection/alerts/history?source=memory&limit=1000", token)
	var mem struct {
		Transitions []map[string]any `json:"transitions"`
	}
	if err := json.Unmarshal(wm.Body.Bytes(), &mem); err != nil {
		t.Fatalf("decode memory: %v", err)
	}
	if len(mem.Transitions) != protection.TransitionHistoryCapacity {
		t.Fatalf("memory source must stay ring-bounded at %d, got %d", protection.TransitionHistoryCapacity, len(mem.Transitions))
	}
}

// --- T-B -------------------------------------------------------------------
// P31-I9 No Silent Source Substitution: when the durable read fails, the
// handler MUST fail explicitly (503) and must NOT answer with the memory ring.
func TestProtectionObs_History_DurableFailureNoSilentSubstitution(t *testing.T) {
	srv, token, _ := newObsTestServer(t, true)
	// Give the ring real content, so a forbidden fallback would be visible.
	srv.alertTracker = protection.NewAlertTracker()
	driveEdges(t, srv.alertTracker, 4, time.Now())
	srv.transitionStore = NewFailedTransitionStore("boom.jsonl", errors.New("disk on fire"))

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?source=durable&limit=10", token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("P31-I9: durable failure must be 503, got %d (body=%q)", w.Code, w.Body.String())
	}
	var body struct {
		Reason       string         `json:"reason"`
		ReadSource   string         `json:"read_source"`
		ReadStatus   string         `json:"read_status"`
		Transitions  []map[string]any
		HistoryStats map[string]any `json:"history_stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ReadSource != "durable" || body.ReadStatus != "unavailable" {
		t.Fatalf("P31-I4: read_source/read_status must be durable/unavailable, got %v/%v", body.ReadSource, body.ReadStatus)
	}
	if len(body.Transitions) != 0 {
		t.Fatalf("P31-I9: must NOT return ring data as a durable result, got %d transitions", len(body.Transitions))
	}
	if body.HistoryStats["durable_available"] != false || body.HistoryStats["durable_error"] != true {
		t.Fatalf("P31-I4: durable_available=false / durable_error=true required, got %v/%v",
			body.HistoryStats["durable_available"], body.HistoryStats["durable_error"])
	}
}

// A durable read with no store configured (memory mode) is also explicit.
func TestProtectionObs_History_DurableNotConfigured(t *testing.T) {
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	driveEdges(t, srv.alertTracker, 3, time.Now())
	srv.transitionStore = nil

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?source=durable", token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when no durable store configured, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not_configured") {
		t.Fatalf("reason must be not_configured, body=%q", w.Body.String())
	}
}

// --- T-C -------------------------------------------------------------------
// P31-I3 clamp honesty: the effective limit is echoed next to the requested one.
func TestProtectionObs_History_LimitClampHonesty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTrackerWithStore(store)
	srv.transitionStore = store
	driveEdges(t, srv.alertTracker, 5, time.Now())

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?source=durable&limit=99999", token)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	var body struct {
		HistoryStats map[string]any `json:"history_stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.HistoryStats["limit_requested"] != float64(99999) {
		t.Fatalf("limit_requested must echo 99999, got %v", body.HistoryStats["limit_requested"])
	}
	if body.HistoryStats["limit_effective"] != float64(protection.DurableReadMaxLimit) {
		t.Fatalf("P31-I3: limit_effective must be clamped to %d and exposed, got %v",
			protection.DurableReadMaxLimit, body.HistoryStats["limit_effective"])
	}
}

// --- T-D -------------------------------------------------------------------
// Default (no source) keeps the Phase 29/30 ring behavior and is labelled memory.
func TestProtectionObs_History_DefaultSourceUnchanged(t *testing.T) {
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	driveEdges(t, srv.alertTracker, 3, time.Now())

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history", token)
	if w.Code != http.StatusOK {
		t.Fatalf("default source: want 200 got %d", w.Code)
	}
	var body struct {
		Transitions  []map[string]any `json:"transitions"`
		HistoryStats map[string]any   `json:"history_stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Default limit is 10; 3 edges exist -> all 3 returned, newest-first.
	if len(body.Transitions) != 3 {
		t.Fatalf("default should return the 3 ring entries, got %d", len(body.Transitions))
	}
	if body.HistoryStats["read_source"] != "memory" {
		t.Fatalf("default read_source must be memory, got %v", body.HistoryStats["read_source"])
	}
	// Existing Phase 30 fields must still be present (no regression).
	for _, k := range []string{"file_dropped", "retention_meta_inconsistent", "available", "load_error", "history_corrupt"} {
		if _, ok := body.HistoryStats[k]; !ok {
			t.Fatalf("regression: history_stats must still expose %q, got %+v", k, body.HistoryStats)
		}
	}
	// An unknown source is rejected explicitly, never coerced to memory.
	wb := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?source=wat", token)
	if wb.Code != http.StatusBadRequest {
		t.Fatalf("unknown source must be 400, got %d", wb.Code)
	}
}

// --- T-E -------------------------------------------------------------------
// P31-I3 byte budget: an oversized durable file is rejected BEFORE reading.
func TestProtectionObs_History_DurableBudgetExceeded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.jsonl")
	// meta line + one line larger than the 8 MiB budget.
	var sb strings.Builder
	sb.WriteString("{\"_meta\":true,\"file_dropped\":0}\n")
	sb.WriteString(strings.Repeat("x", 9<<20))
	sb.WriteString("\n")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTrackerWithStore(store)
	srv.transitionStore = store

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?source=durable&limit=10", token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("P31-I3: over-budget file must be 503, got %d", w.Code)
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Reason != "budget_exceeded" {
		t.Fatalf("reason must be budget_exceeded, got %q", body.Reason)
	}
}

// --- T-F -------------------------------------------------------------------
// P31-I2 projection-only: a durable read has no side effects on the tracker.
func TestProtectionObs_History_DurableReadNoSideEffects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTrackerWithStore(store)
	srv.transitionStore = store
	driveEdges(t, srv.alertTracker, 5, time.Now())

	before := srv.alertTracker.State()
	beforeTx := len(srv.alertTracker.Transitions())
	beforeStats := srv.alertTracker.HistoryStats()

	for i := 0; i < 3; i++ {
		w := doReq(srv.ProtectionReadMux(), http.MethodGet,
			"/management/v1/protection/alerts/history?source=durable&limit=1000", token)
		if w.Code != http.StatusOK {
			t.Fatalf("durable read %d: want 200 got %d", i, w.Code)
		}
	}
	after := srv.alertTracker.State()
	if after.Firing != before.Firing || !after.Since.Equal(before.Since) {
		t.Fatalf("P31-I2: durable read mutated alert state: %+v -> %+v", before, after)
	}
	if len(srv.alertTracker.Transitions()) != beforeTx {
		t.Fatalf("P31-I2: durable read must not add ring entries: %d -> %d",
			beforeTx, len(srv.alertTracker.Transitions()))
	}
	if srv.alertTracker.HistoryStats() != beforeStats {
		t.Fatalf("P31-I2: durable read must not change history stats")
	}
}

// --- T-G -------------------------------------------------------------------
// P31-I5: startup Load and durable ReadRecent share ONE corruption definition.
func TestDurableRead_SharesCorruptionSemanticsWithLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	firing := "{\"at\":\"2020-01-01T00:00:00Z\",\"from\":false,\"to\":true,\"unknown_rate\":70,\"threshold\":50}"
	clear := "{\"at\":\"2020-01-02T00:00:00Z\",\"from\":true,\"to\":false,\"unknown_rate\":0,\"threshold\":50}"
	seeded := "{\"_meta\":true,\"file_dropped\":0}\n" + firing + "\n" + "<<<CORRUPT>>>\n" + clear + "\n"
	if err := os.WriteFile(path, []byte(seeded), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// Path 1: startup Load through the tracker.
	tr := protection.NewAlertTrackerWithStore(store)
	if !tr.HistoryStats().HistoryCorrupt {
		t.Fatal("startup Load must mark history corrupt")
	}
	// Path 2: durable ReadRecent directly.
	res := store.ReadRecent(context.Background(), 100)
	if !res.Corrupt {
		t.Fatal("P31-I5: ReadRecent must reach the SAME corrupt verdict as startup Load")
	}
	if len(res.Transitions) != 0 {
		t.Fatalf("P31-I5: corrupt durable history must not be served, got %d", len(res.Transitions))
	}
	// And the API turns that into an explicit 503 (never a silent memory fallback).
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = tr
	srv.transitionStore = store
	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?source=durable", token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("corrupt durable read must be 503, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "corrupt") {
		t.Fatalf("reason must be corrupt, body=%q", w.Body.String())
	}
}
