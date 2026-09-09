package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Phase 35 discrimination tests (T23–T40, R176 frozen Architecture)
// ---------------------------------------------------------------------------

func fixedExportClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func readManifestFile(t *testing.T, dir, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read manifest %s: %v", name, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest %s not json: %v", name, err)
	}
	return m
}

func fileSHA256(t *testing.T, path string) (string, int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data))
}

func writeTestManifest(t *testing.T, dir, identity string, pubID int64, formats []manifestFormatInfo) {
	t.Helper()
	m := snapshotManifest{
		SchemaVersion: manifestSchemaVersion,
		PublicationID: pubID,
		Snapshot:      identity,
		ExportedAt:    "2026-09-10T00:00:00Z",
		GeneratedAt:   "2026-09-10T00:00:00Z",
		Source:        "durable",
		SeqContinuity: seqContinuityValue,
		Formats:       formats,
	}
	data, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alert-transitions-"+identity+".manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// T23 — manifest content correctness: schema v2, provenance, hashes, seq
// fields, and the frozen seq_continuity constant.
func TestManifestContentAndProvenance(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 123456789, time.UTC)
	st := &fakeExportStore{
		res: protection.TransitionReadResult{
			Transitions: sampleTransitions(),
			ExportedAt:  exp,
			MinSeq:      10,
			MaxSeq:      14,
		},
	}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	s.Tick(context.Background())

	name := "alert-transitions-" + safeTS(exp) + ".manifest.json"
	m := readManifestFile(t, dir, name)
	if v, _ := m["schema_version"].(float64); int(v) != manifestSchemaVersion {
		t.Fatalf("schema_version = %v, want %d", m["schema_version"], manifestSchemaVersion)
	}
	if v, _ := m["publication_id"].(float64); int64(v) != 1 {
		t.Fatalf("publication_id = %v, want 1 (fresh watermark)", m["publication_id"])
	}
	if m["snapshot"] != safeTS(exp) {
		t.Fatalf("snapshot = %v, want %v", m["snapshot"], safeTS(exp))
	}
	if m["exported_at"] != exp.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("exported_at = %v (store provenance required)", m["exported_at"])
	}
	if m["source"] != "durable" {
		t.Fatalf("source = %v", m["source"])
	}
	if m["seq_continuity"] != seqContinuityValue {
		t.Fatalf("seq_continuity = %v, want %v", m["seq_continuity"], seqContinuityValue)
	}
	if v, _ := m["min_seq"].(float64); int64(v) != 10 || func() int64 { v, _ := m["max_seq"].(float64); return int64(v) }() != 14 {
		t.Fatalf("min/max seq = %v/%v, want 10/14", m["min_seq"], m["max_seq"])
	}
	if v, _ := m["records"].(float64); int64(v) != int64(len(sampleTransitions())) {
		t.Fatalf("records = %v", m["records"])
	}
	fmts, _ := m["formats"].([]any)
	if len(fmts) != 1 {
		t.Fatalf("formats = %v", m["formats"])
	}
	f0 := fmts[0].(map[string]any)
	wantFile := "alert-transitions-" + safeTS(exp) + ".json"
	if f0["file"] != wantFile {
		t.Fatalf("format file = %v, want %v", f0["file"], wantFile)
	}
	sum, size := fileSHA256(t, filepath.Join(dir, wantFile))
	if f0["sha256"] != sum || int64(f0["bytes"].(float64)) != size {
		t.Fatalf("digest/bytes = %v/%v, want %s/%d", f0["sha256"], f0["bytes"], sum, size)
	}
}

// T24 — manifest no-replace (M2): a same-ts second tick publishes the renamed
// manifest and never touches the first one.
func TestManifestNoReplaceSameTs(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	s.Tick(context.Background())
	first := "alert-transitions-" + safeTS(exp) + ".manifest.json"
	orig, _ := os.ReadFile(filepath.Join(dir, first))
	s.Tick(context.Background())
	after, _ := os.ReadFile(filepath.Join(dir, first))
	if string(orig) != string(after) {
		t.Fatal("first manifest was overwritten")
	}
	if _, err := os.Stat(filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".1.manifest.json")); err != nil {
		t.Fatalf("renamed manifest missing: %v", err)
	}
}

// T25 — M1: a PARTIAL export publishes no manifest; the successful artifact is
// honestly reported as orphan_artifact by verify.
func TestPartialExportPublishesNoManifest(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	if err := os.Mkdir(filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".csv.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json", "csv"}})
	s.Tick(context.Background())

	for _, fn := range formalFiles(t, dir) {
		if strings.HasSuffix(fn, ".manifest.json") {
			t.Fatalf("partial export must not publish a manifest, got %v", fn)
		}
	}
	if s.Status().ManifestError != "" {
		t.Fatalf("M1 path is not a manifest error: %q", s.Status().ManifestError)
	}
	results, err := s.VerifySnapshots(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range results {
		if r.Snapshot == safeTS(exp) {
			found = true
			if r.Status != verifyStatusOrphanArtifact {
				t.Fatalf("verify status = %q, want orphan_artifact", r.Status)
			}
		}
	}
	if !found {
		t.Fatalf("snapshot %s missing from verify results: %+v", safeTS(exp), results)
	}
}

// T26 — verify ok on an intact snapshot.
func TestVerifyOK(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json", "csv"}})
	s.Tick(context.Background())
	results, err := s.VerifySnapshots(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != verifyStatusOK {
		t.Fatalf("verify = %+v, want single ok", results)
	}
}

// T27 — a declared artifact deleted from disk ⇒ missing (M5).
func TestVerifyMissingArtifact(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json", "csv"}})
	s.Tick(context.Background())
	if err := os.Remove(filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".csv")); err != nil {
		t.Fatal(err)
	}
	results, _ := s.VerifySnapshots(10)
	if results[0].Status != verifyStatusMissing {
		t.Fatalf("verify = %+v, want missing", results[0])
	}
}

// T28 — manifest present but EVERY declared artifact gone ⇒ orphan_manifest.
func TestVerifyOrphanManifest(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	s.Tick(context.Background())
	if err := os.Remove(filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".json")); err != nil {
		t.Fatal(err)
	}
	results, _ := s.VerifySnapshots(10)
	if results[0].Status != verifyStatusOrphanManifest {
		t.Fatalf("verify = %+v, want orphan_manifest", results[0])
	}
}

// T29 — corrupt manifest provenance ⇒ unknown, never ok (M5).
func TestVerifyCorruptManifestUnknown(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	s.Tick(context.Background())
	if err := os.WriteFile(filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".manifest.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	results, _ := s.VerifySnapshots(10)
	if results[0].Status != verifyStatusUnknown {
		t.Fatalf("verify = %+v, want unknown", results[0])
	}
}

// T30 — M7 fail-closed: a lost publication watermark means the tick publishes
// NOTHING (no snapshot, no manifest) and surfaces the error explicitly.
func TestWatermarkLostFailClosed(t *testing.T) {
	dir := t.TempDir()
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: time.Now()}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	if err := os.Remove(filepath.Join(dir, publicationStateFile)); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if got := formalFiles(t, dir); len(got) != 0 {
		t.Fatalf("fail-closed tick must publish nothing, got %v", got)
	}
	if s.Status().PublicationStateError == "" {
		t.Fatal("PublicationStateError must be set on lost watermark")
	}
	if s.Status().Published != 0 || s.Status().Failed != 0 {
		t.Fatalf("published/failed = %d/%d, want 0/0", s.Status().Published, s.Status().Failed)
	}
}

// T31 — artifact-only orphan discovery: verify MUST find snapshots that have
// artifacts but no manifest (it cannot just enumerate manifests).
func TestVerifyArtifactOnlyOrphanDiscovered(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"alert-transitions-20260829T180000Z.json", "alert-transitions-20260829T180000Z.csv"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions()}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	results, err := s.VerifySnapshots(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range results {
		if r.Snapshot == "20260829T180000Z" {
			found = true
			if r.Status != verifyStatusOrphanArtifact {
				t.Fatalf("status = %q, want orphan_artifact", r.Status)
			}
		}
	}
	if !found {
		t.Fatalf("orphan_artifact not discovered: %+v", results)
	}
}

// T32 — manifest-only group ⇒ orphan_manifest.
func TestVerifyManifestOnlyOrphanDiscovered(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "20260829T180000Z", 7, nil)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions()}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	results, err := s.VerifySnapshots(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range results {
		if r.Snapshot == "20260829T180000Z" {
			found = true
			if r.Status != verifyStatusOrphanManifest {
				t.Fatalf("status = %q, want orphan_manifest", r.Status)
			}
		}
	}
	if !found {
		t.Fatalf("orphan_manifest not discovered: %+v", results)
	}
}

// T33 — paging with same-ts collision ordinals: two manifests with the SAME
// ExportedAt but different ordinals must page exactly once each, ordinal first
// in DESC order.
func TestManifestPagingSameTsOrdinals(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	s.Tick(context.Background())
	s.Tick(context.Background()) // same ts → ordinal 1

	page1, cur1, err := s.ListManifests(1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 1 || page1[0].PublicationID != 2 {
		t.Fatalf("page1 = %+v, want publication_id 2 (ordinal 1, newest first)", page1)
	}
	page2, _, err := s.ListManifests(1, cur1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].PublicationID != 1 {
		t.Fatalf("page2 = %+v, want publication_id 1", page2)
	}
	if page1[0].Snapshot == page2[0].Snapshot {
		t.Fatal("same snapshot identity returned twice")
	}
}

// T34 — digest provenance (frozen sentence): the manifest digest is a
// generation-time observation, so post-publication tampering is DETECTED as a
// mismatch — it is not an authenticity proof that silently keeps saying ok.
func TestVerifyDetectsPostPublicationTampering(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	s.Tick(context.Background())
	f, err := os.OpenFile(filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".json"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("TAMPERED\n")
	f.Close()
	results, _ := s.VerifySnapshots(10)
	if results[0].Status != verifyStatusMismatch {
		t.Fatalf("verify = %+v, want mismatch after tampering", results[0])
	}
}

// T35 — undeclared artifact has EXACTLY ONE owner (R171/R176): T.extra.csv is
// mismatch+extra_files of T, and is NOT also a T.extra orphan group; the
// registered T.1 identity wins over parent-prefix extra classification.
func TestVerifyUndeclaredArtifactUniqueOwner(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json", "csv"}})
	s.Tick(context.Background())

	// Undeclared artifact owned by T.
	if err := os.WriteFile(filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".extra.csv"), []byte("EXTRA"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A registered T.1 snapshot with its own manifest + artifact.
	sum, _ := fileSHA256(t, filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".json"))
	self1 := safeTS(exp) + ".1"
	// A valid (empty) envelope so the recount succeeds.
	if err := os.WriteFile(filepath.Join(dir, "alert-transitions-"+self1+".json"), []byte(`{"transitions":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sum1, size1 := fileSHA256(t, filepath.Join(dir, "alert-transitions-"+self1+".json"))
	writeTestManifest(t, dir, self1, 99, []manifestFormatInfo{{
		Format: "json", File: "alert-transitions-" + self1 + ".json", SHA256: sum1, Bytes: size1, Records: 0,
	}})
	_ = sum

	results, err := s.VerifySnapshots(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("checked = %d results (%+v), want exactly 2 groups (T and T.1)", len(results), results)
	}
	for _, r := range results {
		switch r.Snapshot {
		case safeTS(exp):
			if r.Status != verifyStatusMismatch {
				t.Fatalf("T status = %q, want mismatch", r.Status)
			}
			if len(r.ExtraFiles) != 1 || r.ExtraFiles[0] != "alert-transitions-"+safeTS(exp)+".extra.csv" {
				t.Fatalf("T extra_files = %v", r.ExtraFiles)
			}
		case self1:
			if r.Status != verifyStatusOK {
				t.Fatalf("T.1 status = %q (%s), want ok", r.Status, r.Detail)
			}
		default:
			t.Fatalf("unexpected group %q in results", r.Snapshot)
		}
	}
}

// T36 — canonical owner == retention owner (R172): prune removes the whole
// unit, including an undeclared extra artifact; extras never survive alone.
func TestPruneRemovesUndeclaredExtraWithUnit(t *testing.T) {
	dir := t.TempDir()
	oldTS := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: oldTS}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json", "csv"}, Retain: 1})
	s.Tick(context.Background()) // unit T (json+csv+manifest), the OLDEST
	extra := "alert-transitions-" + safeTS(oldTS) + ".extra.csv"
	if err := os.WriteFile(filepath.Join(dir, extra), []byte("EXTRA"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Two newer units force T's unit out of the Retain=1 window.
	for _, off := range []int{1, 2} {
		ts := time.Date(2026, 8, 29, 12, 0, off, 0, time.UTC)
		name := "alert-transitions-" + safeTS(ts) + ".json"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.prune(); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{
		"alert-transitions-" + safeTS(oldTS) + ".json",
		"alert-transitions-" + safeTS(oldTS) + ".manifest.json",
		extra,
	} {
		if _, err := os.Stat(filepath.Join(dir, fn)); !os.IsNotExist(err) {
			t.Fatalf("%s survived prune; the undeclared extra must go with its unit", fn)
		}
	}
}

// T37 — cursor non-reuse (M7): a cursor for a pruned snapshot must NEVER
// resolve to a later snapshot that reuses the same identity.
func TestManifestCursorNonReuse(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	s.Tick(context.Background()) // A: identity T, publication_id 1
	page, cur, err := s.ListManifests(1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].PublicationID != 1 {
		t.Fatalf("page = %+v", page)
	}
	// Prune A away (simulate by direct removal).
	for _, fn := range formalFiles(t, dir) {
		if err := os.Remove(filepath.Join(dir, fn)); err != nil {
			t.Fatal(err)
		}
	}
	s.Tick(context.Background()) // B: SAME identity T, publication_id 2
	_, _, err = s.ListManifests(1, cur)
	if !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("old cursor resolved to %v, want cursor_expired", err)
	}
}

// T37b — identical-manifest reuse: B's manifest is byte-identical to A's
// except publication_id, and the old cursor STILL must not resolve to B.
func TestManifestCursorIdenticalReuse(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	gen := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{
		Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}, Clock: fixedExportClock(gen),
	})
	s.Tick(context.Background()) // A (id 1)
	aBytes, err := os.ReadFile(filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	page, cur, err := s.ListManifests(1, "")
	if err != nil || page[0].PublicationID != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	for _, fn := range formalFiles(t, dir) {
		if err := os.Remove(filepath.Join(dir, fn)); err != nil {
			t.Fatal(err)
		}
	}
	s.Tick(context.Background()) // B (id 2): everything identical except the id
	bBytes, err := os.ReadFile(filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Prove the maximal-ambiguity setup: identical modulo publication_id.
	var am, bm map[string]any
	json.Unmarshal(aBytes, &am)
	json.Unmarshal(bBytes, &bm)
	delete(am, "publication_id")
	delete(bm, "publication_id")
	aj, _ := json.Marshal(am)
	bj, _ := json.Marshal(bm)
	if string(aj) != string(bj) {
		t.Fatalf("setup broken: manifests differ beyond publication_id:\n%s\n%s", aj, bj)
	}
	if _, _, err = s.ListManifests(1, cur); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("old cursor resolved to %v, want cursor_expired despite identical bytes", err)
	}
}

// T38 — publication_id is the anchor identity, NEVER the sort key (A8 v4):
// a clock rollback makes id order and ts order disagree; paging still follows
// the (ts, ordinal) DESC position.
func TestManifestPagingFollowsSortOrderNotIDOrder(t *testing.T) {
	dir := t.TempDir()
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions()}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})

	// publication 1 → ts 13:00 (newer); publication 2 → ts 12:00 (older).
	st.res.ExportedAt = time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	s.Tick(context.Background())
	st.res.ExportedAt = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	s.Tick(context.Background())

	entries, _, err := s.ListManifests(10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].PublicationID != 1 || entries[1].PublicationID != 2 {
		t.Fatalf("unexpected ids: %+v", entries)
	}
	// The FIRST entry must be the 13:00 snapshot even though it has the LOWER id.
	if entries[0].Snapshot != safeTS(time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("sort followed id order, not (ts, ordinal) DESC: %+v", entries)
	}
	// Anchor at publication 1 → strictly AFTER its sorted position → pub 2.
	// (The full-page cursor points at the LAST entry, so anchor explicitly.)
	page, _, err := s.ListManifests(10, base64urlEncode("v2:1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].PublicationID != 2 {
		t.Fatalf("after anchor page = %+v, want publication 2", page)
	}
}

// T39 — artifact-only groups never occupy manifest paging positions
// (A8-MANIFEST-1): the list domain is manifest-bearing groups only.
func TestManifestPagingIgnoresArtifactOnlyGroups(t *testing.T) {
	dir := t.TempDir()
	// A (manifest, oldest) < B (orphan artifacts, middle) < C (manifest, newest)
	writeTestManifest(t, dir, "20260829T100000Z", 101, nil)
	for _, n := range []string{"alert-transitions-20260829T110000Z.json", "alert-transitions-20260829T110000Z.csv"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTestManifest(t, dir, "20260829T120000Z", 102, nil)

	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions()}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	entries, _, err := s.ListManifests(10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("manifest list must contain ONLY manifest-bearing groups, got %+v", entries)
	}
	if entries[0].Snapshot != "20260829T120000Z" || entries[1].Snapshot != "20260829T100000Z" {
		t.Fatalf("unexpected order: %+v", entries)
	}
	// Anchor A (the OLDEST entry): paging must terminate cleanly, having
	// skipped the orphan B entirely (no hole, no phantom position).
	page, _, err := s.ListManifests(10, base64urlEncode("v2:101"))
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 0 {
		t.Fatalf("page after oldest = %+v, want empty", page)
	}
	// Anchor C (newest): strictly after its position comes A — B never appears.
	page, _, err = s.ListManifests(10, base64urlEncode("v2:102"))
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Snapshot != "20260829T100000Z" {
		t.Fatalf("page after C = %+v, want exactly A (orphan B must not occupy a position)", page)
	}
}

// T40 — duplicate publication_id: the cursor namespace is ambiguous, so BOTH
// the homepage and an anchored request fail loudly; verify reports unknown.
func TestDuplicatePublicationIDCursorAmbiguous(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "20260829T100000Z", 101, nil)
	writeTestManifest(t, dir, "20260829T100000Z.1", 101, nil)

	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions()}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})

	if _, _, err := s.ListManifests(10, ""); !errors.Is(err, ErrCursorAmbiguous) {
		t.Fatalf("homepage err = %v, want cursor_ambiguous", err)
	}
	cur := base64urlEncode("v2:101")
	if _, _, err := s.ListManifests(10, cur); !errors.Is(err, ErrCursorAmbiguous) {
		t.Fatalf("anchored err = %v, want cursor_ambiguous", err)
	}
	results, err := s.VerifySnapshots(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status != verifyStatusUnknown || !strings.Contains(r.Detail, "duplicate_publication_id") {
			t.Fatalf("verify = %+v, want unknown/duplicate_publication_id", r)
		}
	}
}

// T41 — actual record count is an INDEPENDENT recount (R177/B): tampering that
// adds a record must surface actual_records != declared_records, proving the
// value is recomputed from the artifact rather than copied from the manifest.
func TestVerifyActualRecordsIndependentlyRecounted(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	s.Tick(context.Background())

	// Tamper: add ONE extra transition record to the artifact envelope.
	path := filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	trs := env["transitions"].([]any)
	trs = append(trs, map[string]any{
		"at": "2026-08-29T12:00:03Z", "from": true, "to": false,
		"kind": "resolve", "unknown_rate": 9.0, "threshold": 30.0,
	})
	env["transitions"] = trs
	out, _ := json.MarshalIndent(env, "", "  ")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}

	results, verr := s.VerifySnapshots(10)
	if verr != nil {
		t.Fatal(verr)
	}
	var fr *VerifyFormatResult
	for _, r := range results {
		for i := range r.Formats {
			if r.Formats[i].Format == "json" {
				f := r.Formats[i]
				fr = &f
			}
		}
	}
	if fr == nil {
		t.Fatalf("json format result missing: %+v", results)
	}
	if fr.ActualRecords != int64(len(sampleTransitions())+1) {
		t.Fatalf("actual_records = %d, want a REAL recount (%d)", fr.ActualRecords, len(sampleTransitions())+1)
	}
	if fr.DeclaredRecords != int64(len(sampleTransitions())) {
		t.Fatalf("declared_records = %d, want %d", fr.DeclaredRecords, len(sampleTransitions()))
	}
	if fr.Status != verifyStatusMismatch {
		t.Fatalf("status = %q, want mismatch", fr.Status)
	}
}

// T42 — duplicate publication_id OUTSIDE the requested limit window must still
// poison the namespace (R177/B): the integrity scan precedes limit truncation.
func TestDuplicateIDDetectionNotLimitedByWindow(t *testing.T) {
	dir := t.TempDir()
	writeTestManifest(t, dir, "20260829T130000Z", 101, nil) // newest, inside window
	writeTestManifest(t, dir, "20260829T080000Z", 101, nil) // older duplicate, OUTSIDE limit=1

	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions()}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	results, err := s.VerifySnapshots(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("window must cap RESULTS to 1, got %+v", results)
	}
	if results[0].Status != verifyStatusUnknown || !strings.Contains(results[0].Detail, "duplicate_publication_id") {
		t.Fatalf("status = %q/%q, want unknown via duplicate_publication_id", results[0].Status, results[0].Detail)
	}
}
