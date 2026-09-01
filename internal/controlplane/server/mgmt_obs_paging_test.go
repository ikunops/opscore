package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Phase 32.2 — Opaque durable cursor paging (T-a .. T-h)
// ---------------------------------------------------------------------------

// appendN writes n transitions straight into the store (bypassing the tracker),
// each with a distinct timestamp so identities are distinct.
func appendN(t *testing.T, s *FileBackedTransitionStore, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := s.Append(context.Background(), protection.AlertTransition{
			At: time.Now().Add(time.Duration(i) * time.Second),
			From: i%2 == 0, To: i%2 == 1, UnknownRate: int64(i), Threshold: 50,
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
}

func getPage(t *testing.T, srv *Server, token, query string) (int, map[string]any) {
	t.Helper()
	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?"+query, token)
	var body struct {
		Transitions  []map[string]any `json:"transitions"`
		HistoryStats map[string]any   `json:"history_stats"`
		Page         map[string]any   `json:"page"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (code=%d body=%q)", err, w.Code, w.Body.String())
	}
	return w.Code, map[string]any{
		"transitions":   body.Transitions,
		"history_stats": body.HistoryStats,
		"page":          body.Page,
	}
}

// --- T-a -------------------------------------------------------------------
// Basic traversal: 2500 records walk as 1000/1000/500 with no duplication and
// no omission, globally newest-first.
func TestDurablePaging_BasicTraversal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	appendN(t, store, 2500)
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	srv.transitionStore = store

	seen := make([]string, 0, 2500)
	cursor := ""
	pages := 0
	for pages < 20 { // hard bound: 2500 records / 1000 per page can never need more
		q := "source=durable&limit=1000"
		if cursor != "" {
			q += "&before=" + cursor
		}
		code, got := getPage(t, srv, token, q)
		if code != http.StatusOK {
			t.Fatalf("page %d: want 200 got %d", pages+1, code)
		}
		txs := got["transitions"].([]map[string]any)
		page := got["page"].(map[string]any)
		for _, m := range txs {
			seen = append(seen, m["at"].(string))
		}
		pages++
		hasMore, _ := page["has_more"].(bool)
		if !hasMore {
			break
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			t.Fatalf("page %d: has_more=true but next_cursor is empty", pages)
		}
		cursor = next
	}
	if pages >= 20 {
		t.Fatal("paging did not terminate")
	}
	if len(seen) != 2500 {
		t.Fatalf("P32: traversal must cover all 2500 records exactly once, got %d", len(seen))
	}
	uniq := map[string]bool{}
	for _, v := range seen {
		if uniq[v] {
			t.Fatalf("P32-I9: duplicate record %q across pages", v)
		}
		uniq[v] = true
	}
}

// --- T-e -------------------------------------------------------------------
// Last page reports has_more=false and an empty next_cursor.
func TestDurablePaging_LastPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	appendN(t, store, 5)
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	srv.transitionStore = store

	_, got := getPage(t, srv, token, "source=durable&limit=1000")
	page := got["page"].(map[string]any)
	if page["has_more"] != false {
		t.Fatalf("P32: last page must report has_more=false, got %v", page["has_more"])
	}
	if nc, _ := page["next_cursor"].(string); nc != "" {
		t.Fatalf("P32: last page next_cursor must be empty, got %q", nc)
	}
	if page["returned"] != float64(5) {
		t.Fatalf("P32: returned must be 5, got %v", page["returned"])
	}
}

// --- T-b / T-b2 ------------------------------------------------------------
// P32-I9 (append stability) and P32-I12 (cursor survives an eviction rewrite):
// take page 1, then mutate the durable file — appends and a full rewrite — and
// continue with the SAME next_cursor. No duplication, no omission, and no
// spurious expiry just because offsets moved.
func TestDurablePaging_SurvivesAppendAndRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	appendN(t, store, 1500)
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	srv.transitionStore = store

	// Page 1 = newest 1000.
	_, p1 := getPage(t, srv, token, "source=durable&limit=1000")
	page1 := p1["page"].(map[string]any)
	if page1["has_more"] != true {
		t.Fatal("precondition: 1500 records must yield a second page")
	}
	cursor := page1["next_cursor"].(string)
	first := p1["transitions"].([]map[string]any)
	if len(first) != 1000 {
		t.Fatalf("page 1 must hold 1000, got %d", len(first))
	}

	// (1) Append new edges after page 1 — must not move the older boundary.
	appendN(t, store, 20)

	// (2) Force a full rewrite of the durable file (same content, new offsets)
	//     to emulate an eviction rewrite moving every byte offset.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(path+".tmp", raw, 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatalf("rename: %v", err)
	}

	// Continue with the OLD cursor. P32-I12: offsets moved but the record is
	// still retained, so this must NOT be a 410 cursor_expired.
	code, p2 := getPage(t, srv, token, "source=durable&limit=1000&before="+cursor)
	if code != http.StatusOK {
		t.Fatalf("P32-I12: old cursor must survive a rewrite, got %d (body=%q)",
			code, p2["history_stats"])
	}
	second := p2["transitions"].([]map[string]any)
	if len(second) != 500 {
		t.Fatalf("P32-I9: page 2 must still return the remaining 500, got %d", len(second))
	}
	// No overlap with page 1.
	seen := map[string]bool{}
	for _, m := range first {
		seen[m["at"].(string)] = true
	}
	for _, m := range second {
		if seen[m["at"].(string)] {
			t.Fatalf("P32-I9: record %q appeared in both pages (duplication)", m["at"])
		}
	}
}

// --- T-b3 -----------------------------------------------------------------
// R146=B: records whose canonical fields are byte-identical are LEGAL and may
// repeat. A cursor minted on one of them must still resolve to THAT occurrence
// after a rewrite — it must never collapse into 409 cursor_ambiguous while the
// record is still retained (that would violate P32-I12).
func TestDurablePaging_DuplicateRecordsSurviveRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Three records with IDENTICAL canonical fields (same instant, same rates),
	// then a distinct one so the page has a boundary.
	same := time.Now()
	for i := 0; i < 3; i++ {
		if err := store.Append(context.Background(), protection.AlertTransition{
			At: same, From: false, To: true, UnknownRate: 7, Threshold: 50,
		}); err != nil {
			t.Fatalf("append dup %d: %v", i, err)
		}
	}
	if err := store.Append(context.Background(), protection.AlertTransition{
		At: same.Add(time.Second), From: true, To: false, UnknownRate: 0, Threshold: 50,
	}); err != nil {
		t.Fatalf("append distinct: %v", err)
	}

	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	srv.transitionStore = store

	// Page 1 (newest 2) mints a cursor on one of the duplicate occurrences.
	_, p1 := getPage(t, srv, token, "source=durable&limit=2")
	page1 := p1["page"].(map[string]any)
	if page1["has_more"] != true {
		t.Fatal("precondition: 4 records must yield a second page")
	}
	cursor := page1["next_cursor"].(string)
	if cursor == "" {
		t.Fatal("precondition: need a non-empty cursor")
	}

	// Rewrite the whole file so every byte offset moves (eviction-rewrite shape).
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(path+".tmp", raw, 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatalf("rename: %v", err)
	}

	// The same cursor must still resolve — NOT 409, NOT 410.
	code, p2 := getPage(t, srv, token, "source=durable&limit=1000&before="+cursor)
	if code != http.StatusOK {
		t.Fatalf("R146/P32-I12: duplicate-record cursor must survive a rewrite, got %d (body=%q)",
			code, p2["history_stats"])
	}
	rest := p2["transitions"].([]map[string]any)
	if len(rest) != 2 {
		t.Fatalf("P32: expected the 2 older records, got %d", len(rest))
	}
}

// --- T-c -------------------------------------------------------------------
// P32-I10: an expired cursor is explicit 410 and NEVER restarts from the
// newest page.
func TestDurablePaging_CursorExpiredIsExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	appendN(t, store, 10)
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	srv.transitionStore = store

	// Mint a cursor from the current file...
	_, p1 := getPage(t, srv, token, "source=durable&limit=5")
	page1 := p1["page"].(map[string]any)
	cursor := page1["next_cursor"].(string)
	if cursor == "" {
		t.Fatal("precondition: need a non-empty cursor")
	}
	// ...then wipe the durable file so the cursor's record is gone.
	if err := os.WriteFile(path, []byte("{\"_meta\":true,\"file_dropped\":0}\n"), 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?source=durable&before="+cursor, token)
	if w.Code != http.StatusGone {
		t.Fatalf("P32-I10: expired cursor must be 410, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cursor_expired") {
		t.Fatalf("P32-I10: reason must be cursor_expired, body=%q", w.Body.String())
	}
	// Must NOT silently restart from the newest page.
	var body struct {
		Transitions []map[string]any `json:"transitions"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Transitions) != 0 {
		t.Fatalf("P32-I10: expired cursor must not return records (got %d)", len(body.Transitions))
	}
}

// --- T-d -------------------------------------------------------------------
// A malformed cursor is a 400, never an ignored parameter.
func TestDurablePaging_InvalidCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	appendN(t, store, 5)
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	srv.transitionStore = store

	w := doReq(srv.ProtectionReadMux(), http.MethodGet,
		"/management/v1/protection/alerts/history?source=durable&before=not-a-cursor", token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("P32: malformed cursor must be 400, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_cursor") {
		t.Fatalf("P32: reason must be invalid_cursor, body=%q", w.Body.String())
	}
}

// --- T-g -------------------------------------------------------------------
// P32-I8 (revised): the durable record format may evolve ONCE, in the storage
// layer only, and only to carry the stable sequence. The API-facing
// AlertTransition shape must remain untouched (P32-I5).
func TestDurablePaging_FormatEvolutionAndAPIShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.jsonl")
	store, err := NewFileBackedTransitionStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	appendN(t, store, 1)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want meta + 1 record, got %d lines", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil {
		t.Fatalf("record must be valid JSON: %v", err)
	}
	// Storage envelope: a stable, explicitly-tagged contract = the Phase 30
	// fields plus the durable sequence.
	wantStorage := map[string]bool{
		"seq": true, "at": true, "from": true, "to": true, "unknown_rate": true, "threshold": true,
	}
	if len(rec) != len(wantStorage) {
		t.Fatalf("P32-I8: unexpected storage envelope, got %+v", rec)
	}
	for k := range rec {
		if !wantStorage[k] {
			t.Fatalf("P32-I8: unexpected field %q in the durable envelope", k)
		}
	}

	// P32-I5: the API response shape is UNCHANGED — the sequence is a
	// storage-layer concern and must never leak into the read surface.
	srv, token, _ := newObsTestServer(t, true)
	srv.alertTracker = protection.NewAlertTracker()
	srv.transitionStore = store
	_, got := getPage(t, srv, token, "source=durable&limit=10")
	txs := got["transitions"].([]map[string]any)
	if len(txs) != 1 {
		t.Fatalf("want 1 transition, got %d", len(txs))
	}
	wantAPI := map[string]bool{
		"at": true, "from": true, "to": true, "kind": true, "unknown_rate": true, "threshold": true,
	}
	if len(txs[0]) != len(wantAPI) {
		t.Fatalf("P32-I5: API transition shape changed, got %+v", txs[0])
	}
	for k := range txs[0] {
		if !wantAPI[k] {
			t.Fatalf("P32-I5: unexpected field %q exposed by the API (seq must stay internal)", k)
		}
	}
}
