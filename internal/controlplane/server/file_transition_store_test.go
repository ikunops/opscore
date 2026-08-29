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

// TestFileBackedTransitionStore_TrailingPartialLine (P30-I12 counter-case)
// proves ADR-053 recovery is preserved: a partial line left by a crash at the
// very END of the file (without a trailing newline) is silently recovered — the
// valid records before it are still returned and the store stays CLEAN.
func TestFileBackedTransitionStore_TrailingPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	good := "{\"at\":\"2020-01-01T00:00:00Z\",\"from\":false,\"to\":true,\"unknown_rate\":1,\"threshold\":50}"
	// No trailing newline: the last line is a truncated (partial) write.
	seeded := "{\"_meta\":true,\"file_dropped\":0}\n" + good + "\n" + "{\"at\":\"2020-01-02T00:00:0"
	if err := os.WriteFile(path, []byte(seeded), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	s, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	res := s.Load(context.Background())
	if res.LoadErr != nil {
		t.Fatalf("trailing partial line must not be a load error: %v", res.LoadErr)
	}
	if res.Corrupt {
		t.Fatal("P30-I12: trailing partial line must NOT be flagged corrupt")
	}
	if len(res.Transitions) != 1 {
		t.Fatalf("want 1 recovered transition, got %d", len(res.Transitions))
	}
}

// TestFileBackedTransitionStore_TrailingCorruptLineWithTerminator (P30-I12,
// R138 blocker) proves the trailing-partial exemption is NOT over-broad: a
// corrupt final line that was FULLY WRITTEN (it carries a line terminator) is
// not a crash partial write, so it must be reported as corruption rather than
// silently recovered.
func TestFileBackedTransitionStore_TrailingCorruptLineWithTerminator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	firing := "{\"at\":\"2020-01-01T00:00:00Z\",\"from\":false,\"to\":true,\"unknown_rate\":70,\"threshold\":50}"
	// NOTE the trailing "\n": the corrupt line is complete, i.e. fully written.
	seeded := "{\"_meta\":true,\"file_dropped\":0}\n" + firing + "\n" + "<<<CORRUPT>>>\n"
	if err := os.WriteFile(path, []byte(seeded), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	s, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	res := s.Load(context.Background())
	if !res.Corrupt {
		t.Fatal("P30-I12: a fully-written corrupt final line must set Corrupt=true (only a NON-terminated tail is a partial write)")
	}
	tr := protection.NewAlertTrackerWithStore(s)
	st := tr.HistoryStats()
	if !st.HistoryCorrupt {
		t.Fatal("P30-I12: tracker must expose HistoryCorrupt=true")
	}
	if st.Available {
		t.Fatal("P30-I12: Available must be false — corrupted history is never clean")
	}
}

// TestFileBackedTransitionStore_MiddleCorruption (P30-I12) proves a corrupt
// line in the MIDDLE of durable history is never silently skipped: the store
// must raise the corruption signal rather than `continue`-ing and presenting the
// surviving remainder as normal history.
func TestFileBackedTransitionStore_MiddleCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	firing := "{\"at\":\"2020-01-01T00:00:00Z\",\"from\":false,\"to\":true,\"unknown_rate\":70,\"threshold\":50}"
	clear := "{\"at\":\"2020-01-02T00:00:00Z\",\"from\":true,\"to\":false,\"unknown_rate\":0,\"threshold\":50}"
	seeded := "{\"_meta\":true,\"file_dropped\":0}\n" + firing + "\n" + "<<<CORRUPT>>>\n" + clear + "\n"
	if err := os.WriteFile(path, []byte(seeded), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	s, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	res := s.Load(context.Background())
	if !res.Corrupt {
		t.Fatal("P30-I12: middle-line corruption must set Corrupt=true (never silent)")
	}

	// The tracker wired on top must degrade, not present a false-clean history.
	tr := protection.NewAlertTrackerWithStore(s)
	st := tr.HistoryStats()
	if !st.HistoryCorrupt {
		t.Fatal("P30-I12: tracker must expose HistoryCorrupt=true")
	}
	if st.Available {
		t.Fatal("P30-I12: Available must be false on corruption (never clean)")
	}
	if len(tr.Transitions()) != 0 {
		t.Fatalf("P30-I12: no history should be served as authoritative when corrupt, got %d", len(tr.Transitions()))
	}
	// Degraded baseline: the first Observe must NOT emit a synthetic edge.
	tr.Observe(protection.AlertCondition{Firing: true, UnknownRate: 70, Threshold: 50, Window: time.Minute}, time.Now())
	if n := len(tr.Transitions()); n != 0 {
		t.Fatalf("P30-I12: first Observe after corruption must establish baseline without a synthetic edge, got %d transitions", n)
	}
}

// TestFileBackedTransitionStore_LoadHardError (P30-I11-impl) proves a durable
// file that cannot be READ at all surfaces as a load failure reaching the
// tracker/API — it must never be downgraded to a clean in-memory tracker, which
// would make an unreadable persisted history look like "no history".
func TestFileBackedTransitionStore_LoadHardError(t *testing.T) {
	// A directory is not readable as a file, so os.ReadFile fails hard.
	dir := filepath.Join(t.TempDir(), "not-a-file.jsonl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	s, err := NewFileBackedTransitionStore(dir)
	if err == nil {
		// Construction may succeed (open/mkdir worked) or fail; either way the
		// resulting tracker must be degraded, never clean.
		_ = s
	}
	var store protection.AlertTransitionStore = s
	if err != nil {
		store = NewFailedTransitionStore(dir, err) // what main.go now wires
	}
	res := store.Load(context.Background())
	if res.LoadErr == nil {
		t.Fatal("P30-I11-impl: an unreadable durable path must surface LoadErr")
	}
	tr := protection.NewAlertTrackerWithStore(store)
	st := tr.HistoryStats()
	if st.Available {
		t.Fatal("P30-I11-impl: Available must be false when the durable load failed")
	}
	if !st.LoadError {
		t.Fatal("P30-I11-impl: LoadError must be true and reach the read API")
	}
	// Degraded baseline, not a synthetic FIRING edge.
	tr.Observe(protection.AlertCondition{Firing: true, UnknownRate: 70, Threshold: 50, Window: time.Minute}, time.Now())
	if n := len(tr.Transitions()); n != 0 {
		t.Fatalf("P30-I11-impl: first Observe after load failure must not emit a synthetic edge, got %d", n)
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
