package server

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Phase 36 helpers
// ---------------------------------------------------------------------------

// covScheduler wires a scheduler over `dir` with a non-nil store (R161 requires
// a Store to construct one at all).
func covScheduler(t *testing.T, dir string) *HistoryExportScheduler {
	t.Helper()
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions()}}
	s, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store:    st,
		Dir:      dir,
		Interval: time.Hour,
		Formats:  []string{"json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// writeCovManifest writes a schema-v2 manifest with explicit seq provenance.
func writeCovManifest(t *testing.T, dir, identity string, pubID, minSeq, maxSeq, records int64, truncated bool) {
	t.Helper()
	m := snapshotManifest{
		SchemaVersion: manifestSchemaVersion,
		PublicationID: pubID,
		Snapshot:      identity,
		ExportedAt:    "2026-09-10T00:00:00Z",
		GeneratedAt:   "2026-09-10T00:00:00Z",
		Source:        "durable",
		Truncated:     truncated,
		SeqContinuity: seqContinuityValue,
		MinSeq:        minSeq,
		MaxSeq:        maxSeq,
		Records:       records,
	}
	data, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alert-transitions-"+identity+".manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func i64p(v int64) *int64 { return &v }

func covIntervals(t *testing.T, got []CoverageInterval, want ...CoverageInterval) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("intervals = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("intervals[%d] = %+v, want %+v (all=%+v)", i, got[i], want[i], got)
		}
	}
}

func covUnusableReason(t *testing.T, res *CoverageResult, snapshot string) CoverageUnusable {
	t.Helper()
	for _, u := range res.Unusable {
		if u.Snapshot == snapshot {
			return u
		}
	}
	t.Fatalf("unusable entry for %q missing: %+v", snapshot, res.Unusable)
	return CoverageUnusable{}
}

// dirFingerprint hashes the FULL file set + contents of the snapshot dir, so a
// read-only surface can be proven not to have touched anything (T50).
func dirFingerprint(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var b []byte
	for _, n := range names {
		data, rerr := os.ReadFile(filepath.Join(dir, n))
		if rerr != nil {
			t.Fatal(rerr)
		}
		b = append(b, []byte(n)...)
		b = append(b, data...)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// T45 — contiguous coverage ⇒ complete, no gaps
// ---------------------------------------------------------------------------
func TestCoverageContiguousIsComplete(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 50, 50, false)
	writeCovManifest(t, dir, "20260910T000002Z", 102, 51, 100, 50, false)
	writeCovManifest(t, dir, "20260910T000003Z", 103, 101, 200, 100, false)

	res, err := covScheduler(t, dir).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.SnapshotsConsidered != 3 {
		t.Fatalf("snapshots_considered = %d, want 3", res.SnapshotsConsidered)
	}
	covIntervals(t, res.Covered, CoverageInterval{1, 200})
	covIntervals(t, res.Gaps)
	covIntervals(t, res.OutOfScope)
	if res.Completeness != "complete" {
		t.Fatalf("completeness = %q, want complete (gaps=%+v)", res.Completeness, res.Gaps)
	}
	if res.Bounded || res.SourceTruncated || res.ProvenanceUncertain {
		t.Fatalf("flags = bounded:%v truncated:%v uncertain:%v, want all false", res.Bounded, res.SourceTruncated, res.ProvenanceUncertain)
	}
	if len(res.Indeterminate.PublicationGaps) != 0 {
		t.Fatalf("publication_gaps = %+v, want none", res.Indeterminate.PublicationGaps)
	}
}

// ---------------------------------------------------------------------------
// T46 — gaps are reported exactly
// ---------------------------------------------------------------------------
func TestCoverageGapsAreExact(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 10, 50, 41, false)
	writeCovManifest(t, dir, "20260910T000002Z", 102, 60, 90, 31, false)
	writeCovManifest(t, dir, "20260910T000003Z", 103, 100, 200, 101, false)

	res, err := covScheduler(t, dir).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	covIntervals(t, res.Covered,
		CoverageInterval{10, 50}, CoverageInterval{60, 90}, CoverageInterval{100, 200})
	covIntervals(t, res.Gaps,
		CoverageInterval{51, 59}, CoverageInterval{91, 99})
	if res.Completeness != "gaps_present" {
		t.Fatalf("completeness = %q, want gaps_present", res.Completeness)
	}
	if res.Bounded {
		t.Fatalf("bounded = true, want false (window defaults to the observed extent)")
	}
}

// ---------------------------------------------------------------------------
// T47 — publication_id holes are INDETERMINATE, never "deleted"
// ---------------------------------------------------------------------------
func TestCoveragePublicationGapsAreIndeterminate(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 10, 10, false)
	writeCovManifest(t, dir, "20260910T000002Z", 102, 11, 20, 10, false)
	writeCovManifest(t, dir, "20260910T000003Z", 105, 21, 30, 10, false)

	res, err := covScheduler(t, dir).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	covIntervals(t, res.Indeterminate.PublicationGaps, CoverageInterval{103, 104})
	// The hole must NOT be turned into a seq gap nor into a deletion report.
	covIntervals(t, res.Gaps)
	if res.Completeness != "complete" {
		t.Fatalf("completeness = %q, want complete (a publication hole is not an evidence defect)", res.Completeness)
	}
	raw, _ := json.Marshal(res)
	if s := string(raw); containsAny(s, "retention_deleted", "deleted", "lost") {
		t.Fatalf("response must not interpret a publication hole as deletion/loss: %s", s)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// T48 — a corrupt manifest makes the verdict UNKNOWN (never false-clean)
// ---------------------------------------------------------------------------
func TestCoverageCorruptManifestIsUnknown(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 100, 100, false)
	if err := os.WriteFile(filepath.Join(dir, "alert-transitions-20260910T000002Z.manifest.json"),
		[]byte("{ this is not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := covScheduler(t, dir).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	u := covUnusableReason(t, res, "20260910T000002Z")
	if u.Reason != "unparseable" || u.Class != "coverage_uncertain" {
		t.Fatalf("unusable = %+v, want unparseable/coverage_uncertain", u)
	}
	if !res.ProvenanceUncertain {
		t.Fatal("provenance_uncertain must be true")
	}
	if res.Completeness != "unknown" {
		t.Fatalf("completeness = %q, want unknown", res.Completeness)
	}
}

// ---------------------------------------------------------------------------
// T49 — a limit-truncated snapshot set is `bounded`
// ---------------------------------------------------------------------------
func TestCoverageLimitTruncationIsBounded(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 10, 10, false)
	writeCovManifest(t, dir, "20260910T000002Z", 102, 11, 20, 10, false)
	writeCovManifest(t, dir, "20260910T000003Z", 103, 21, 30, 10, false)

	res, err := covScheduler(t, dir).Coverage(nil, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.SnapshotsConsidered != 2 {
		t.Fatalf("snapshots_considered = %d, want 2", res.SnapshotsConsidered)
	}
	if !res.Bounded {
		t.Fatal("bounded must be true when the snapshot set was truncated by limit")
	}
	// The NEWEST two are kept (identity DESC): 103 and 102.
	covIntervals(t, res.Covered, CoverageInterval{11, 30})
	if res.Completeness != "complete" {
		t.Fatalf("completeness = %q, want complete (bounded alone never downgrades)", res.Completeness)
	}
}

// ---------------------------------------------------------------------------
// T50 — strictly read-only: the directory is byte-identical afterwards
// ---------------------------------------------------------------------------
func TestCoverageIsStrictlyReadOnly(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 50, 50, false)
	writeCovManifest(t, dir, "20260910T000002Z", 102, 60, 100, 41, false)
	if err := os.WriteFile(filepath.Join(dir, "alert-transitions-20260910T000001Z.json"), []byte(`{"transitions":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	before := dirFingerprint(t, dir)
	entriesBefore, _ := os.ReadDir(dir)

	s := covScheduler(t, dir)
	if _, err := s.Coverage(nil, nil, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Coverage(i64p(1), i64p(100), 10); err != nil {
		t.Fatal(err)
	}

	if after := dirFingerprint(t, dir); after != before {
		t.Fatalf("coverage mutated the snapshot directory (read-only violated)")
	}
	entriesAfter, _ := os.ReadDir(dir)
	if len(entriesAfter) != len(entriesBefore) {
		t.Fatalf("file count changed: %d -> %d", len(entriesBefore), len(entriesAfter))
	}
}

// ---------------------------------------------------------------------------
// T51 — disabled scheduler ⇒ 503; malformed window ⇒ 400
// ---------------------------------------------------------------------------
func TestCoverageRoute503And400(t *testing.T) {
	srv, token, _ := newObsTestServer(t, true)
	srv.historyScheduler = nil

	w := doReq(srv.ProtectionReadMux(), "GET",
		"/management/v1/protection/alerts/history/export/coverage", token)
	if w.Code != 503 {
		t.Fatalf("disabled scheduler must be 503, got %d (%s)", w.Code, w.Body.String())
	}

	dir := t.TempDir()
	srv.historyScheduler = covScheduler(t, dir)
	for _, path := range []string{
		"/management/v1/protection/alerts/history/export/coverage?since=abc",
		"/management/v1/protection/alerts/history/export/coverage?until=1.5",
		"/management/v1/protection/alerts/history/export/coverage?since=50&until=10",
		"/management/v1/protection/alerts/history/export/coverage?limit=0",
	} {
		w := doReq(srv.ProtectionReadMux(), "GET", path, token)
		if w.Code != 400 {
			t.Fatalf("%s must be 400, got %d (%s)", path, w.Code, w.Body.String())
		}
	}

	w = doReq(srv.ProtectionReadMux(), "GET",
		"/management/v1/protection/alerts/history/export/coverage?since=1&until=500", token)
	if w.Code != 200 {
		t.Fatalf("valid window must be 200, got %d (%s)", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// T52 — union runs on the SEQ axis, never by snapshot time order
// ---------------------------------------------------------------------------
func TestCoverageUnionIsSeqAxisNotTime(t *testing.T) {
	dir := t.TempDir()
	// Deliberately NON-monotonic: the newest snapshot holds the LOWEST range.
	writeCovManifest(t, dir, "20260910T000001Z", 101, 100, 200, 101, false)
	writeCovManifest(t, dir, "20260910T000002Z", 102, 10, 50, 41, false)
	writeCovManifest(t, dir, "20260910T000003Z", 103, 60, 90, 31, false)

	res, err := covScheduler(t, dir).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	covIntervals(t, res.Covered,
		CoverageInterval{10, 50}, CoverageInterval{60, 90}, CoverageInterval{100, 200})
	// The two holes stay SEPARATE; a merge-by-time implementation would emit [51,99].
	covIntervals(t, res.Gaps,
		CoverageInterval{51, 59}, CoverageInterval{91, 99})
}

// ---------------------------------------------------------------------------
// T53 — the observation boundary never manufactures a gap
// ---------------------------------------------------------------------------
func TestCoverageBoundaryNeverCreatesGap(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 100, 200, 101, false)

	res, err := covScheduler(t, dir).Coverage(i64p(50), i64p(300), 10)
	if err != nil {
		t.Fatal(err)
	}
	covIntervals(t, res.Covered, CoverageInterval{100, 200})
	covIntervals(t, res.Gaps)
	covIntervals(t, res.OutOfScope,
		CoverageInterval{50, 99}, CoverageInterval{201, 300})
	if !res.Bounded {
		t.Fatal("bounded must be true when the window exceeds the observed extent")
	}
	if res.Completeness != "complete" {
		t.Fatalf("completeness = %q, want complete (bounded is a scope property only)", res.Completeness)
	}
}

// ---------------------------------------------------------------------------
// T54 — no usable evidence at all ⇒ unknown, never complete
// ---------------------------------------------------------------------------
func TestCoverageNoUsableEvidenceIsUnknown(t *testing.T) {
	dir := t.TempDir()
	// A declared range that carries zero records: KNOWN to hold no coverage
	// evidence (coverage_irrelevant), unlike a 0/0 missing range.
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 10, 0, false) // empty_range

	res, err := covScheduler(t, dir).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Completeness != "unknown" {
		t.Fatalf("completeness = %q, want unknown", res.Completeness)
	}
	covIntervals(t, res.Covered)
	covIntervals(t, res.Gaps)
	u := covUnusableReason(t, res, "20260910T000001Z")
	if u.Reason != "empty_range" || u.Class != "coverage_irrelevant" {
		t.Fatalf("unusable = %+v, want empty_range/coverage_irrelevant", u)
	}
	if res.ProvenanceUncertain {
		t.Fatal("coverage_irrelevant must NOT set provenance_uncertain")
	}
	if res.SnapshotsConsidered != 1 {
		t.Fatalf("snapshots_considered = %d, want 1 (an empty snapshot is still considered)", res.SnapshotsConsidered)
	}
}

// ---------------------------------------------------------------------------
// T55 — the four unusable reasons classify exactly as frozen
// ---------------------------------------------------------------------------
func TestCoverageUnusableClassification(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 10, 10, false)  // usable
	writeCovManifest(t, dir, "20260910T000002Z", 102, 0, 0, 5, false)    // missing_range
	writeCovManifest(t, dir, "20260910T000003Z", 103, 50, 10, 5, false)  // invalid_range
	writeCovManifest(t, dir, "20260910T000004Z", 104, 20, 30, 0, false)  // empty_range
	writeCovManifest(t, dir, "20260910T000005Z", 105, 40, 60, 21, false) // usable

	res, err := covScheduler(t, dir).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for snapshot, want := range map[string][2]string{
		"20260910T000002Z": {"missing_range", "coverage_uncertain"},
		"20260910T000003Z": {"invalid_range", "coverage_uncertain"},
		"20260910T000004Z": {"empty_range", "coverage_irrelevant"},
	} {
		u := covUnusableReason(t, res, snapshot)
		if u.Reason != want[0] || u.Class != want[1] {
			t.Fatalf("%s unusable = %+v, want %s/%s", snapshot, u, want[0], want[1])
		}
	}
	// The invalid range is never swapped into a usable [10,50].
	covIntervals(t, res.Covered, CoverageInterval{1, 10}, CoverageInterval{40, 60})
	if res.Completeness != "unknown" {
		t.Fatalf("completeness = %q, want unknown", res.Completeness)
	}
}

// ---------------------------------------------------------------------------
// T56 — a truncated source contributes but can NEVER be `complete`
// ---------------------------------------------------------------------------
func TestCoverageTruncatedNeverComplete(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 100, 100, true)

	res, err := covScheduler(t, dir).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	covIntervals(t, res.Covered, CoverageInterval{1, 100})
	covIntervals(t, res.Gaps)
	if !res.SourceTruncated {
		t.Fatal("source_truncated must be true")
	}
	if res.Completeness != "unknown" {
		t.Fatalf("completeness = %q, want unknown (no gap but dropped data)", res.Completeness)
	}
}

// ---------------------------------------------------------------------------
// T57 — unknown provenance blocks a definitive verdict
// ---------------------------------------------------------------------------
func TestCoverageUnknownProvenanceBlocksDefinitiveGap(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 10, 10, false)
	if err := os.WriteFile(filepath.Join(dir, "alert-transitions-20260910T000002Z.manifest.json"),
		[]byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCovManifest(t, dir, "20260910T000003Z", 103, 21, 30, 10, false)

	res, err := covScheduler(t, dir).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	covIntervals(t, res.Covered, CoverageInterval{1, 10}, CoverageInterval{21, 30})
	u := covUnusableReason(t, res, "20260910T000002Z")
	if u.Reason != "unparseable" || u.Class != "coverage_uncertain" {
		t.Fatalf("unusable = %+v, want unparseable/coverage_uncertain", u)
	}
	if !res.ProvenanceUncertain {
		t.Fatal("provenance_uncertain must be true")
	}
	if res.Completeness != "unknown" {
		t.Fatalf("completeness = %q, want unknown — never gaps_present (gaps=%+v)", res.Completeness, res.Gaps)
	}
}

// ---------------------------------------------------------------------------
// T58 — int64 upper bound: overlap / adjacency / non-adjacency, no overflow
// ---------------------------------------------------------------------------
func TestCoverageInt64UpperBoundAdjacency(t *testing.T) {
	max := int64(math.MaxInt64)

	// Unit level: the merge predicate itself must never evaluate MaxInt64+1.
	cases := []struct {
		name string
		in   []CoverageInterval
		want int
	}{
		{"overlap", []CoverageInterval{{max - 10, max}, {max, max}}, 1},
		{"adjacent", []CoverageInterval{{max - 10, max - 1}, {max, max}}, 1},
		{"non-adjacent", []CoverageInterval{{max - 10, max - 2}, {max, max}}, 2},
	}
	for _, tc := range cases {
		if got := len(mergeCoverageIntervals(tc.in)); got != tc.want {
			t.Fatalf("%s: merged into %d intervals, want %d (%+v)", tc.name, got, tc.want, tc.in)
		}
	}
	if !coverageMergeable(max, max) {
		t.Fatal("overlap at the bound must merge")
	}
	if !coverageMergeable(max-1, max) {
		t.Fatal("adjacency at the bound must merge")
	}
	if coverageMergeable(max-2, max) {
		t.Fatal("non-adjacency at the bound must NOT merge")
	}

	// End to end: a hole just below MaxInt64 is reported exactly, no panic.
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, max-10, max-2, 9, false)
	writeCovManifest(t, dir, "20260910T000002Z", 102, max, max, 1, false)
	res, err := covScheduler(t, dir).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	covIntervals(t, res.Covered,
		CoverageInterval{max - 10, max - 2}, CoverageInterval{max, max})
	covIntervals(t, res.Gaps, CoverageInterval{max - 1, max - 1})
}

// ---------------------------------------------------------------------------
// T59 — an unknown extent is never washed into `complete` by "no gap right now"
// ---------------------------------------------------------------------------
func TestCoverageUncertainNeverWashedToComplete(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 100, 100, false)
	if err := os.WriteFile(filepath.Join(dir, "alert-transitions-20260910T000002Z.manifest.json"),
		[]byte("not a manifest"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := covScheduler(t, dir).Coverage(i64p(1), i64p(100), 10)
	if err != nil {
		t.Fatal(err)
	}
	covIntervals(t, res.Covered, CoverageInterval{1, 100})
	covIntervals(t, res.Gaps)
	if !res.ProvenanceUncertain {
		t.Fatal("provenance_uncertain must be true")
	}
	if res.Completeness != "unknown" {
		t.Fatalf("completeness = %q, want unknown — the union being complete today proves nothing", res.Completeness)
	}
}

// TestCoverageNarrowWindowClipsCovered pins the window clip itself.
func TestCoverageNarrowWindowClipsCovered(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 1000, 1000, false)

	res, err := covScheduler(t, dir).Coverage(i64p(100), i64p(200), 10)
	if err != nil {
		t.Fatal(err)
	}
	covIntervals(t, res.Covered, CoverageInterval{100, 200})
	covIntervals(t, res.Gaps)
	covIntervals(t, res.OutOfScope)
	if res.Bounded {
		t.Fatal("a window fully inside the observed extent is not bounded")
	}
	if res.Completeness != "complete" {
		t.Fatalf("completeness = %q, want complete", res.Completeness)
	}
}

// TestCoverageNoManifestsIsUnknown drives the empty-directory case.
func TestCoverageNoManifestsIsUnknown(t *testing.T) {
	res, err := covScheduler(t, t.TempDir()).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.SnapshotsConsidered != 0 || res.Completeness != "unknown" {
		t.Fatalf("empty dir = considered:%d completeness:%q, want 0/unknown", res.SnapshotsConsidered, res.Completeness)
	}
	covIntervals(t, res.Covered)
	covIntervals(t, res.Gaps)
}

// ---------------------------------------------------------------------------
// T60 — an uncertain snapshot provably OUTSIDE the window must NOT downgrade
// (R187/B): this is the counter-example the frozen architecture demands.
// ---------------------------------------------------------------------------
func TestCoverageUncertainOutsideWindowDoesNotDowngrade(t *testing.T) {
	dir := t.TempDir()
	// usable [100,200] alone defines the (defaulted) window [100,200].
	writeCovManifest(t, dir, "20260910T000001Z", 101, 100, 200, 101, false)
	// invalid_range whose ENVELOPE is [10,50] — provably outside the window.
	writeCovManifest(t, dir, "20260910T000002Z", 102, 50, 10, 5, false)

	res, err := covScheduler(t, dir).Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	// The entry is still reported for diagnostics ...
	u := covUnusableReason(t, res, "20260910T000002Z")
	if u.Reason != "invalid_range" || u.Class != "coverage_uncertain" {
		t.Fatalf("unusable = %+v, want invalid_range/coverage_uncertain", u)
	}
	// ... but it cannot influence THIS window's verdict.
	if res.ProvenanceUncertain {
		t.Fatal("an envelope wholly outside the window must NOT be relevant (R187/B)")
	}
	covIntervals(t, res.Covered, CoverageInterval{100, 200})
	covIntervals(t, res.Gaps)
	if res.Completeness != "complete" {
		t.Fatalf("completeness = %q, want complete", res.Completeness)
	}
}

// T60b — relevance is a property of the QUERY, not of the file: widening the
// window to reach the same snapshot's envelope makes it relevant again.
func TestCoverageUncertainRelevanceDependsOnWindow(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 100, 200, 101, false)
	writeCovManifest(t, dir, "20260910T000002Z", 102, 50, 10, 5, false) // envelope [10,50]

	res, err := covScheduler(t, dir).Coverage(i64p(1), i64p(200), 10)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ProvenanceUncertain {
		t.Fatal("the same snapshot IS relevant once the window reaches its envelope")
	}
	if res.Completeness != "unknown" {
		t.Fatalf("completeness = %q, want unknown", res.Completeness)
	}
}

// T63 — uncertainty outside snapshots_considered (excluded by limit) is not
// relevant either: `relevant` is scoped to the observation set.
func TestCoverageUncertainOutsideConsideredSet(t *testing.T) {
	dir := t.TempDir()
	// The OLDER snapshot is corrupt; limit=1 keeps only the newest (usable).
	if err := os.WriteFile(filepath.Join(dir, "alert-transitions-20260910T000001Z.manifest.json"),
		[]byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCovManifest(t, dir, "20260910T000002Z", 102, 1, 100, 100, false)

	res, err := covScheduler(t, dir).Coverage(nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.SnapshotsConsidered != 1 {
		t.Fatalf("snapshots_considered = %d, want 1", res.SnapshotsConsidered)
	}
	if res.ProvenanceUncertain {
		t.Fatal("uncertainty outside snapshots_considered must NOT be relevant")
	}
	if res.Completeness != "complete" {
		t.Fatalf("completeness = %q, want complete", res.Completeness)
	}
	if !res.Bounded {
		t.Fatal("bounded must be true (the set was truncated by limit)")
	}
}

// ---------------------------------------------------------------------------
// T62 — clipping happens BEFORE the merge, so `covered` never leaks outside
// the window (R187/B frozen pipeline order).
// ---------------------------------------------------------------------------
func TestCoverageClipPrecedesMerge(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 50, 50, false)
	writeCovManifest(t, dir, "20260910T000002Z", 102, 60, 100, 41, false)

	res, err := covScheduler(t, dir).Coverage(i64p(40), i64p(80), 10)
	if err != nil {
		t.Fatal(err)
	}
	// Only the in-window parts survive: [40,50] and [60,80] — never [1,50].
	covIntervals(t, res.Covered, CoverageInterval{40, 50}, CoverageInterval{60, 80})
	covIntervals(t, res.Gaps, CoverageInterval{51, 59})
	covIntervals(t, res.OutOfScope) // the window sits inside the observed extent
	if res.Bounded {
		t.Fatal("bounded must be false: the window is inside the observed extent")
	}
}

// T61 — a window wholly outside the observed extent is reported exactly (both
// sides), and never manufactures a gap.
func TestCoverageDisjointWindowIsReportedExactly(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 100, 100, false)
	s := covScheduler(t, dir)

	// Right-disjoint.
	res, err := s.Coverage(i64p(500), i64p(600), 10)
	if err != nil {
		t.Fatal(err)
	}
	covIntervals(t, res.Covered)
	covIntervals(t, res.Gaps)
	covIntervals(t, res.OutOfScope, CoverageInterval{500, 600})
	if !res.Bounded || res.Completeness != "complete" {
		t.Fatalf("right-disjoint = bounded:%v completeness:%q, want true/complete", res.Bounded, res.Completeness)
	}

	// Left-disjoint: the observed extent sits entirely ABOVE the window.
	dir2 := t.TempDir()
	writeCovManifest(t, dir2, "20260910T000001Z", 101, 500, 600, 101, false)
	res, err = covScheduler(t, dir2).Coverage(i64p(1), i64p(100), 10)
	if err != nil {
		t.Fatal(err)
	}
	covIntervals(t, res.Covered)
	covIntervals(t, res.Gaps)
	covIntervals(t, res.OutOfScope, CoverageInterval{1, 100})
}
