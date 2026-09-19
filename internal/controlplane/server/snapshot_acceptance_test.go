package server

// Phase 45 — Input Integrity tests (T237~T257; ADR-065 §5, ADR-066 §8).
//
// The discriminating cases all share one fixture family: a durable transition
// store whose Appends are committed to the KAK-signed acceptance ledger, plus a
// scheduler wired for export. The tamper LOCATION decides the verdict (T239 vs
// T241): a middle record reads record_modified, an evicted-range record reads
// outside_span (never asserted), a newest record reads missing_in_span.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type acceptanceFixture struct {
	t          *testing.T
	root       string
	dir        string // export dir
	storePath  string
	store      *FileBackedTransitionStore
	sched      *HistoryExportScheduler
	witnessDir string
	seq        int64 // last durable seq allocated via append()
}

var (
	acceptT0 = time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	acceptT1 = time.Date(2026, 9, 10, 8, 1, 0, 0, time.UTC)
	acceptT2 = time.Date(2026, 9, 10, 8, 2, 0, 0, time.UTC)
	acceptT3 = time.Date(2026, 9, 10, 8, 3, 0, 0, time.UTC)
	acceptT4 = time.Date(2026, 9, 10, 8, 4, 0, 0, time.UTC)
)

// newAcceptanceFixture builds a store + scheduler with the acceptance ledger
// ON. tune may adjust the config before construction (capacity, anchoring,
// destruction, disabling the phase itself).
func newAcceptanceFixture(t *testing.T, tune func(cfg *HistoryExportConfig)) *acceptanceFixture {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "export")
	keyDir := filepath.Join(root, "keys")
	witnessDir := filepath.Join(root, "witness")
	for _, d := range []string{dir, keyDir, witnessDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	signPriv, signPub, _, _ := genKeyPair(t, keyDir, "signer")
	kakPriv, kakPub, _, _ := genKeyPair(t, keyDir, "kak")

	storePath := filepath.Join(root, "alert-transitions.jsonl")
	store, err := NewFileBackedTransitionStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := HistoryExportConfig{
		Store:                  store,
		Dir:                    dir,
		Interval:               time.Hour,
		Formats:                []string{"json"},
		Clock:                  func() time.Time { return acceptT0 },
		SignKeyPath:            signPriv,
		TrustKeyPaths:          []string{signPub},
		KeyAuthorityPath:       kakPriv,
		KeyAuthorityTrustPaths: []string{kakPub},
		AcceptanceLog:          true,
		AcceptanceCapacity:     0,
	}
	if tune != nil {
		tune(&cfg)
	}
	sched, err := NewHistoryExportScheduler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &acceptanceFixture{
		t:          t,
		root:       root,
		dir:        dir,
		storePath:  storePath,
		store:      store,
		sched:      sched,
		witnessDir: witnessDir,
	}
}

// append appends one transition and returns its durable seq.
func (f *acceptanceFixture) append(at time.Time, unknownRate int64) int64 {
	f.t.Helper()
	f.seq++
	err := f.store.Append(context.Background(), protection.AlertTransition{
		At:          at,
		From:        false,
		To:          true,
		UnknownRate: unknownRate,
		Threshold:   50,
	})
	if err != nil {
		f.t.Fatalf("append: %v", err)
	}
	return f.seq
}

// storeLines reads the durable transition file as physical lines (line 0 is meta).
func (f *acceptanceFixture) storeLines() []string {
	f.t.Helper()
	data, err := os.ReadFile(f.storePath)
	if err != nil {
		f.t.Fatalf("read store: %v", err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func (f *acceptanceFixture) writeStoreLines(lines []string) {
	f.t.Helper()
	if err := os.WriteFile(f.storePath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		f.t.Fatalf("write store: %v", err)
	}
}

func (f *acceptanceFixture) ledgerLines() []string {
	f.t.Helper()
	data, err := os.ReadFile(acceptanceLogPath(f.dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		f.t.Fatalf("read acceptance ledger: %v", err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func (f *acceptanceFixture) ledgerEntries() []acceptanceEntry {
	f.t.Helper()
	var out []acceptanceEntry
	for _, l := range f.ledgerLines() {
		var e acceptanceEntry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			f.t.Fatalf("ledger line unparseable: %v", err)
		}
		out = append(out, e)
	}
	return out
}

func (f *acceptanceFixture) writeLedgerLines(lines []string) {
	f.t.Helper()
	if err := os.WriteFile(acceptanceLogPath(f.dir), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		f.t.Fatalf("write acceptance ledger: %v", err)
	}
}

func (f *acceptanceFixture) view() InputIntegrityView {
	f.t.Helper()
	return f.sched.InputIntegrityView(context.Background())
}

// witnessLines counts the lines the offline witness received.
func (f *acceptanceFixture) witnessLines() []string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.witnessDir, witnessFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		f.t.Fatalf("read witness: %v", err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func idsEq(got []int64, want ...int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// T237 — not enabled ⇒ no ledger file, no status field, byte-identical output
// ---------------------------------------------------------------------------

func TestP45T237DisabledZeroRegression(t *testing.T) {
	// (a) no ledger file is created and the status document has no new field.
	f := newAcceptanceFixture(t, func(cfg *HistoryExportConfig) { cfg.AcceptanceLog = false })
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)
	if _, err := os.Stat(acceptanceLogPath(f.dir)); !os.IsNotExist(err) {
		t.Fatalf("acceptance ledger must not exist when the phase is off (err=%v)", err)
	}
	stJSON, err := json.Marshal(f.sched.Status())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stJSON), "input_integrity") {
		t.Fatalf("disabled status document must stay byte-identical to Phase 44, got: %s", stJSON)
	}

	// (b) the serializers never read Seqs ⇒ export output byte-identical.
	res := f.store.ReadAll(context.Background())
	if len(res.Seqs) != len(res.Transitions) {
		t.Fatalf("ReadAll must fill Seqs 1:1 with Transitions")
	}
	var withSeqs, withoutSeqs strings.Builder
	if err := serializeHistoryExportJSON(&withSeqs, res); err != nil {
		t.Fatal(err)
	}
	resNoSeqs := res
	resNoSeqs.Seqs = nil
	if err := serializeHistoryExportJSON(&withoutSeqs, resNoSeqs); err != nil {
		t.Fatal(err)
	}
	if withSeqs.String() != withoutSeqs.String() {
		t.Fatal("T237: the JSON export must be byte-identical with and without per-record Seqs")
	}
	var withSeqsCSV, withoutSeqsCSV strings.Builder
	if err := serializeHistoryExportCSV(&withSeqsCSV, res); err != nil {
		t.Fatal(err)
	}
	if err := serializeHistoryExportCSV(&withoutSeqsCSV, resNoSeqs); err != nil {
		t.Fatal(err)
	}
	if withSeqsCSV.String() != withoutSeqsCSV.String() {
		t.Fatal("T237: the CSV export must be byte-identical with and without per-record Seqs")
	}

	// (c) the read route answers 503 when the phase is off (never a fake 200).
	srv, token := newProtectionTestServer(t, false)
	srv.historyScheduler = f.sched
	req := httptest.NewRequest(http.MethodGet, "/management/v1/protection/alerts/history/export/input-integrity", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.handleHistoryExportInputIntegrity(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled input-integrity route: want 503, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// T238 — Append ⇒ acceptance entry committed (seq aligned, digest, KAK, chain)
// ---------------------------------------------------------------------------

func TestP45T238AppendCommitsAcceptanceEntry(t *testing.T) {
	f := newAcceptanceFixture(t, nil)
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)

	entries := f.ledgerEntries()
	if len(entries) != 2 {
		t.Fatalf("want 2 acceptance entries, got %d", len(entries))
	}
	// record_seq aligned with the durable identity (ReadAll Seqs).
	res := f.store.ReadAll(context.Background())
	if !idsEq(res.Seqs, 2, 1) { // newest-first
		t.Fatalf("ReadAll Seqs must be [2,1] newest-first, got %v", res.Seqs)
	}
	if entries[0].RecordSeq != 1 || entries[1].RecordSeq != 2 {
		t.Fatalf("record_seq must align with durable identity, got %d/%d", entries[0].RecordSeq, entries[1].RecordSeq)
	}
	if entries[0].EntrySeq != 1 || entries[1].EntrySeq != 2 {
		t.Fatalf("entry_seq must be 1,2 (no watermark file), got %d/%d", entries[0].EntrySeq, entries[1].EntrySeq)
	}
	// record_digest = sha256 of the durable line bytes exactly as written.
	lines := f.storeLines()
	for i, e := range entries {
		want := sha256.Sum256([]byte(lines[i+1])) // line 0 is the meta record
		if e.RecordDigest != hex.EncodeToString(want[:]) {
			t.Fatalf("entry %d: record_digest must equal sha256(durable line bytes)", e.EntrySeq)
		}
		if e.RecordedAt == "" {
			t.Fatalf("entry %d: recorded_at must be present (store-side clock)", e.EntrySeq)
		}
		if _, err := time.Parse(time.RFC3339Nano, e.RecordedAt); err != nil {
			t.Fatalf("entry %d: recorded_at must parse as RFC3339Nano: %v", e.EntrySeq, err)
		}
		// KAK signature verifies against the KAK trust anchor.
		if v := verifyAcceptanceEntrySignature(&entries[i], f.sched.keyAuthority.trust); v.Verdict != sigVerdictOK {
			t.Fatalf("entry %d: KAK signature must verify, got %s (%s)", e.EntrySeq, v.Verdict, v.Detail)
		}
		// entry_digest matches its canonical payload.
		dg, err := acceptanceEntryDigest(&entries[i])
		if err != nil || dg != e.EntryDigest {
			t.Fatalf("entry %d: entry_digest must match its canonical payload", e.EntrySeq)
		}
	}
	// hash chain: entry 2 points at entry 1.
	if entries[1].PrevEntryDigest != entries[0].EntryDigest {
		t.Fatalf("the chain must link entry 2 to entry 1")
	}
	if entries[0].PrevEntryDigest != "" {
		t.Fatalf("the genesis entry must carry no prev (got %q)", entries[0].PrevEntryDigest)
	}
	// the view reads input_ok.
	v := f.view()
	if v.Verdict != inputVerdictOK || len(v.ModifiedIDs) > 0 || len(v.MissingInSpanIDs) > 0 || len(v.UnacceptedIDs) > 0 {
		t.Fatalf("untampered records must read input_ok, got %+v", v)
	}
}

// ---------------------------------------------------------------------------
// T239 — CORE RED CASE: tamper a durable record ⇒ P35~P44 stay green, only P45
// observes record_modified
// ---------------------------------------------------------------------------

func TestP45T239TamperedRecordIsObservedOnlyByP45(t *testing.T) {
	f := newAcceptanceFixture(t, nil)
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)
	f.append(acceptT3, 7)

	// Tick 1: the full P30~P44 stack publishes green.
	f.sched.Tick(context.Background())
	st := f.sched.Status()
	if st.LastError != "" || st.Published != 1 || st.Failed != 0 {
		t.Fatalf("the pre-tamper tick must be green, got %+v", st)
	}

	// The store-write-permission attacker rewrites record 2's payload.
	lines := f.storeLines()
	tampered := strings.Replace(lines[2], `"unknown_rate":5`, `"unknown_rate":999`, 1)
	if tampered == lines[2] {
		t.Fatalf("fixture broken: the tamper must change the line")
	}
	lines[2] = tampered
	f.writeStoreLines(lines)

	// Tick 2: the export pipeline is still perfectly legal (it signs whatever
	// the store now holds) — P35~P44 stay green.
	f.sched.Tick(context.Background())
	st = f.sched.Status()
	if st.LastError != "" || st.Published != 1 || st.Failed != 0 {
		t.Fatalf("P35~P44 must stay green after the tamper, got %+v", st)
	}

	// Only P45 observes the modification.
	v := f.view()
	if v.Verdict != inputVerdictModified {
		t.Fatalf("T239 core: the tampered record must read input_modified, got %s (%+v)", v.Verdict, v)
	}
	if !idsEq(v.ModifiedIDs, 2) {
		t.Fatalf("modified_ids must name exactly record 2, got %v", v.ModifiedIDs)
	}
	if len(v.MissingInSpanIDs) > 0 || len(v.UnacceptedIDs) > 0 {
		t.Fatalf("no other assertion may fire: %+v", v)
	}
	if v.InputError != "" {
		t.Fatalf("the ledger itself is healthy: %q", v.InputError)
	}
}

// ---------------------------------------------------------------------------
// T240 — deleting a record inside the span ⇒ record_missing_in_span
// ---------------------------------------------------------------------------

func TestP45T240DeletedRecordMissingInSpan(t *testing.T) {
	f := newAcceptanceFixture(t, nil)
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)
	f.append(acceptT3, 7)

	// Delete the MIDDLE record (seq 2): MinSeq/MaxSeq stay 1..3.
	lines := f.storeLines()
	f.writeStoreLines([]string{lines[0], lines[1], lines[3]})

	v := f.view()
	if !idsEq(v.MissingInSpanIDs, 2) {
		t.Fatalf("T240: the deleted in-span record must read record_missing_in_span, got %v", v.MissingInSpanIDs)
	}
	if v.Verdict != inputVerdictModified {
		t.Fatalf("an assertable deletion is a modification-class verdict, got %s", v.Verdict)
	}
	if len(v.ModifiedIDs) > 0 {
		t.Fatalf("no digest mismatch may be invented: %v", v.ModifiedIDs)
	}
}

// ---------------------------------------------------------------------------
// T241 — deleting the OLDEST record (seq < MinSeq) ⇒ outside_span, NOT asserted
// ---------------------------------------------------------------------------

func TestP45T241BelowMinSeqIsOutsideSpanNotAsserted(t *testing.T) {
	// Same fixture family as T239 — the tamper LOCATION decides the verdict.
	f := newAcceptanceFixture(t, nil)
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)
	f.append(acceptT3, 7)

	// Delete the oldest record (seq 1): indistinguishable from a legal ring
	// eviction (R40-1 boundary discipline).
	lines := f.storeLines()
	f.writeStoreLines([]string{lines[0], lines[2], lines[3]})

	v := f.view()
	if v.OutsideSpanCount != 1 {
		t.Fatalf("T241: ledger entry 1 (< MinSeq 2) must read outside_span, got count %d (%+v)", v.OutsideSpanCount, v)
	}
	if len(v.MissingInSpanIDs) > 0 {
		t.Fatalf("T241: deleting the oldest record must NEVER read missing_in_span, got %v", v.MissingInSpanIDs)
	}
	if v.Verdict != inputVerdictOK {
		t.Fatalf("with nothing assertable the verdict is input_ok, got %s (%+v)", v.Verdict, v)
	}
}

// ---------------------------------------------------------------------------
// T242 — record-first crash window ⇒ record_unaccepted (loud), sticky err
// ---------------------------------------------------------------------------

func TestP45T242CrashWindowRecordUnaccepted(t *testing.T) {
	f := newAcceptanceFixture(t, nil)
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)

	// Simulate the crash window: the recorder can no longer reach its ledger.
	orig := f.store.acceptance
	broken := &acceptanceRecorder{
		path:     filepath.Join(f.root, "no-such-subdir", "record-acceptance.jsonl"),
		capacity: 0,
		ka:       f.sched.keyAuthority,
		clock:    time.Now,
	}
	f.store.installAcceptanceRecorder(broken)

	// The record is DURABLE (record-first) even though recording fails.
	if err := f.store.Append(context.Background(), protection.AlertTransition{At: acceptT3, To: true, UnknownRate: 7, Threshold: 50}); err != nil {
		t.Fatalf("the record itself must persist (P30-I6 best-effort Append), got err %v", err)
	}
	f.seq++
	if got := f.store.AcceptanceError(); got == "" {
		t.Fatal("the recording failure must be surfaced sticky (input_error)")
	}

	v := f.view()
	if !idsEq(v.UnacceptedIDs, 3) {
		t.Fatalf("T242: the unaccepted record must be reported loudly, got %v", v.UnacceptedIDs)
	}
	if v.Verdict != inputVerdictIncomplete {
		t.Fatalf("record_unaccepted ⇒ input_incomplete, got %s", v.Verdict)
	}

	// acceptanceErr is sticky until the next SUCCESSFUL append; the failed
	// record is NEVER back-filled (A8-8), so it stays unaccepted forever.
	f.store.installAcceptanceRecorder(orig)
	f.append(acceptT4, 9)
	if got := f.store.AcceptanceError(); got != "" {
		t.Fatalf("a successful append must clear the sticky error, got %q", got)
	}
	v = f.view()
	if !idsEq(v.UnacceptedIDs, 3) {
		t.Fatalf("the never-back-filled record stays unaccepted, got %v", v.UnacceptedIDs)
	}
	if v.InputError != "" {
		t.Fatalf("the recorder is healthy again, got %q", v.InputError)
	}
	if v.Verdict != inputVerdictIncomplete {
		t.Fatalf("record 3 remains honestly unaccepted ⇒ input_incomplete, got %s", v.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T243 — a bad ledger line ⇒ fail-closed refusal, file bytes untouched
// ---------------------------------------------------------------------------

func TestP45T243BadLedgerLineFailClosed(t *testing.T) {
	f := newAcceptanceFixture(t, nil)
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)

	// Inject one unclassifiable line.
	f.writeLedgerLines(append(f.ledgerLines(), "<<<NOT AN ACCEPTANCE ENTRY>>>"))
	before := f.ledgerLines()

	// The next record persists, but its acceptance is REFUSED and the ledger
	// file stays byte-identical (the unreadable evidence is never trimmed
	// around).
	f.append(acceptT3, 7)
	after := f.ledgerLines()
	if len(before) != len(after) || before[0] != after[0] || before[1] != after[1] {
		t.Fatalf("T243: the ledger must be left byte-identical on refusal")
	}
	if len(f.storeLines()) != 4 { // meta + 3 records
		t.Fatalf("the record itself must still be durable, got %d lines", len(f.storeLines()))
	}
	if got := f.store.AcceptanceError(); got == "" {
		t.Fatal("the refusal must be surfaced (input_error)")
	}

	v := f.view()
	if v.Verdict != inputVerdictUnverifiable {
		t.Fatalf("T243: an unparseable ledger ⇒ input_unverifiable, got %s", v.Verdict)
	}
	if !strings.Contains(v.InputError, "unclassifiable") {
		t.Fatalf("input_error must name the failure, got %q", v.InputError)
	}
}

// ---------------------------------------------------------------------------
// T244 — a second line for one entry_seq ⇒ conflict (fact, not state)
// ---------------------------------------------------------------------------

func TestP45T244SameEntrySeqSecondLineIsConflict(t *testing.T) {
	f := newAcceptanceFixture(t, nil)
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)
	lines := f.ledgerLines()

	// (a) load-side guard: even an IDENTICAL payload is a conflict.
	f.writeLedgerLines(append(lines, lines[1])) // entry_seq 2 twice
	v := f.view()
	if v.Verdict != inputVerdictUnverifiable {
		t.Fatalf("T244: a duplicated entry_seq ⇒ input_unverifiable, got %s", v.Verdict)
	}
	found := false
	for _, p := range v.Problems {
		if p.EntrySeq == 2 && p.Verdict == acceptanceVerdictConflict {
			found = true
		}
	}
	if !found {
		t.Fatalf("T244: the conflict must be observable per entry, got %+v", v.Problems)
	}

	// (b) append-side guard: appending onto a ledger that already holds a
	// duplicate is refused and the file stays byte-identical.
	before := f.ledgerLines()
	line3, err := json.Marshal(newPersisted(3, protection.AlertTransition{At: acceptT3, To: true, UnknownRate: 7, Threshold: 50}))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.acceptance.record(3, line3); err == nil {
		t.Fatal("the append-side guard must refuse a ledger holding a duplicate entry_seq")
	}
	after := f.ledgerLines()
	if len(before) != len(after) {
		t.Fatal("the refused append must leave the ledger byte-identical")
	}
}

// ---------------------------------------------------------------------------
// T245 — fifth anchor stream + acceptance_compaction accounted, own compaction
// not observed (no cycle)
// ---------------------------------------------------------------------------

func TestP45T245FifthAnchorStreamAndAccountedCompaction(t *testing.T) {
	f := newAcceptanceFixture(t, func(cfg *HistoryExportConfig) {
		// The witness dir is a sibling of the export dir (fixture layout).
		cfg.AnchorEndpoint = "file://" + filepath.Join(filepath.Dir(cfg.Dir), "witness")
		cfg.AnchorCapacity = 2 // force the acceptance-anchor stream to rotate
		cfg.DestructionLog = true
		cfg.DestructionCapacity = 16
		cfg.AcceptanceCapacity = 2 // force the acceptance ledger to rotate
	})

	f.append(acceptT1, 3)
	f.append(acceptT2, 5)
	// Record 3 pushes the ledger over capacity ⇒ OBSERVED compaction (entry 1
	// dropped, its destruction accounted).
	f.append(acceptT3, 7)

	// The tick tail dispatches BOTH collect-only sources.
	f.sched.Tick(context.Background())

	// (a) the acceptance ledger's own compaction is accounted in the
	// destruction log as kind=acceptance_compaction.
	destData, err := os.ReadFile(destructionLogPath(f.dir))
	if err != nil {
		t.Fatalf("destruction log must exist: %v", err)
	}
	var kinds []string
	for _, l := range strings.Split(string(destData), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var e destructionEntry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("destruction line unparseable: %v", err)
		}
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) == 0 || kinds[0] != destructionKindAcceptanceCompaction {
		t.Fatalf("T245: the acceptance ledger compaction must be accounted (kind=%s), got %v", destructionKindAcceptanceCompaction, kinds)
	}
	for _, k := range kinds {
		if k == destructionKindAnchorCompaction {
			t.Fatalf("T245: the acceptance-anchor stream's OWN compaction must NOT be observed (I5), got kind %s", k)
		}
	}

	// (b) the fifth anchor stream holds the acceptance entries, anchored.
	accAnchor, err := os.ReadFile(acceptanceAnchorPath(f.dir))
	if err != nil {
		t.Fatalf("acceptance-anchor.jsonl must exist: %v", err)
	}
	acceptanceAnchors := 0
	for _, l := range strings.Split(string(accAnchor), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var e anchorEntry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("anchor line unparseable: %v", err)
		}
		if e.Kind != anchorKindAcceptance {
			t.Fatalf("the fifth stream must carry kind=acceptance, got %q", e.Kind)
		}
		if e.AcceptanceRecordSeq != 2 && e.AcceptanceRecordSeq != 3 {
			t.Fatalf("entry 1 was legally compacted away; its anchor survives but entries 2,3 must be anchored, got record_seq %d", e.AcceptanceRecordSeq)
		}
		if e.State == anchorStateAnchored {
			acceptanceAnchors++
		}
	}
	if acceptanceAnchors == 0 {
		t.Fatal("T245: the acceptance anchors must reach the witness (anchored)")
	}

	// (c) the destruction anchor for the acceptance_compaction was dispatched.
	destAnchor, err := os.ReadFile(destructionAnchorPath(f.dir))
	if err != nil {
		t.Fatalf("destruction-anchor.jsonl must exist: %v", err)
	}
	if !strings.Contains(string(destAnchor), fmt.Sprintf("%q", anchorKindDestruction)) {
		t.Fatalf("T245: the destruction record must be anchored: %s", destAnchor)
	}

	// (d) the witness received the dispatches — and the test COMPLETING is the
	// no-cycle proof (an observation loop would hang or recurse forever).
	if got := len(f.witnessLines()); got < 4 {
		t.Fatalf("T245: the witness must have received the dispatches, got %d lines", got)
	}
}

// ---------------------------------------------------------------------------
// T246 — cross-dimension zero interference: P35~P44 values unchanged
// ---------------------------------------------------------------------------

func TestP45T246CrossDimensionZeroInterference(t *testing.T) {
	type result struct {
		status HistoryExportStatus
		view   InputIntegrityView
	}
	run := func(acceptance bool) result {
		var f *acceptanceFixture
		f = newAcceptanceFixture(t, func(cfg *HistoryExportConfig) {
			cfg.AcceptanceLog = acceptance
			cfg.AnchorEndpoint = "file://" + filepath.Join(filepath.Dir(cfg.Dir), "witness")
			cfg.AnchorCapacity = 16
		})
		f.append(acceptT1, 3)
		f.append(acceptT2, 5)
		f.append(acceptT3, 7)
		f.sched.Tick(context.Background())
		f.sched.Tick(context.Background())
		return result{status: f.sched.Status(), view: f.view()}
	}
	off := run(false)
	on := run(true)

	a, b := off.status, on.status
	if a.LastError != b.LastError || a.PruneError != b.PruneError || a.ManifestError != b.ManifestError ||
		a.PublicationStateError != b.PublicationStateError || a.SignatureError != b.SignatureError ||
		a.LedgerError != b.LedgerError || a.AnchorError != b.AnchorError {
		t.Fatalf("P34~P40 error surfaces must be identical:\noff: %+v\non:  %+v", a, b)
	}
	if a.Published != b.Published || a.Failed != b.Failed || a.SkipCount != b.SkipCount {
		t.Fatalf("P34 counters must be identical: off=%d/%d on=%d/%d", a.Published, a.Failed, b.Published, b.Failed)
	}
	// The two runs generate their own keys, so key IDS legitimately differ;
	// the P37 identity SURFACE (signing on, one trusted key, derived id
	// present) must be identical.
	if a.SigningEnabled != b.SigningEnabled || !b.SigningEnabled ||
		a.TrustedKeys != b.TrustedKeys || b.SignerKeyID == "" {
		t.Fatalf("P37 identity surface must be identical:\noff: %+v\non:  %+v", a.SigningEnabled, b.SigningEnabled)
	}
	if a.AnchorEnabled != b.AnchorEnabled || a.AnchoredCount != b.AnchoredCount ||
		a.PendingCount != b.PendingCount || a.UnanchoredCount != b.UnanchoredCount {
		t.Fatalf("P40 main anchor stream must be identical:\noff: %+v\non:  %+v", a, b)
	}
	if (off.status.InputIntegrity != nil) || on.status.InputIntegrity == nil {
		t.Fatalf("the status roll-up must appear only when the phase is on")
	}
	if off.view.Enabled || !on.view.Enabled {
		t.Fatalf("the view must be enabled only when the phase is on")
	}
	if on.view.Verdict != inputVerdictOK {
		t.Fatalf("the acceptance-on run must read input_ok, got %s", on.view.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T247 — frozen face: the frozen files carry no Phase 45 vocabulary, the
// protection core's only change is the additive Seqs field
// ---------------------------------------------------------------------------

func TestP45T247FrozenFaceUnchanged(t *testing.T) {
	// The test binary runs with the package dir as cwd; the module root is
	// three levels up from internal/controlplane/server.
	root := filepath.Join("..", "..", "..")
	frozenFiles := []string{
		"internal/controlplane/server/appendonly_log.go",
		"internal/controlplane/server/snapshot_signature.go",
		"internal/controlplane/server/snapshot_chain.go",
		"internal/controlplane/server/snapshot_ledger.go",
		"internal/controlplane/server/history_export_coverage.go",
		"internal/controlplane/server/history_export_manifest.go",
	}
	for _, rel := range frozenFiles {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("frozen file %s unreadable: %v", rel, err)
		}
		for _, marker := range []string{"acceptance", "Phase 45", "input_integrity"} {
			if strings.Contains(strings.ToLower(string(src)), strings.ToLower(marker)) {
				t.Fatalf("T247: frozen file %s must not carry Phase 45 vocabulary (%q)", rel, marker)
			}
		}
	}
	// The protection core: ONLY alerting.go may mention Phase 45, and its
	// change is exactly the additive Seqs field.
	entries, err := os.ReadDir(filepath.Join(root, "internal", "protection"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, "internal", "protection", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		hasP45 := strings.Contains(string(src), "Phase 45")
		if e.Name() == "alerting.go" {
			if !hasP45 {
				t.Fatal("alerting.go must carry the additive Seqs field (Phase 45)")
			}
			if !strings.Contains(string(src), "Seqs []int64") {
				t.Fatal("the protection-core change must be exactly the additive Seqs field")
			}
			continue
		}
		if hasP45 {
			t.Fatalf("T247: %s must not mention Phase 45 (protection core: alerting.go Seqs additive ONLY)", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// T248 — concurrent Appends: every record committed, entry_seq gapless
// ---------------------------------------------------------------------------

func TestP45T248ConcurrentAppendsAllCommittedNoGaps(t *testing.T) {
	f := newAcceptanceFixture(t, nil)
	const workers, perWorker = 8, 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if err := f.store.Append(context.Background(), protection.AlertTransition{
					At:          acceptT1.Add(time.Duration(w*perWorker+i) * time.Second),
					To:          true,
					UnknownRate: int64(w*perWorker + i),
					Threshold:   50,
				}); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	res := f.store.ReadAll(context.Background())
	if len(res.Transitions) != workers*perWorker {
		t.Fatalf("all records must be durable, got %d", len(res.Transitions))
	}
	entries := f.ledgerEntries()
	if len(entries) != workers*perWorker {
		t.Fatalf("T248: every append must be committed, got %d entries", len(entries))
	}
	seenRecord := map[int64]string{}
	for i, e := range entries {
		if e.EntrySeq != int64(i+1) {
			t.Fatalf("T248: entry_seq must be gapless 1..N, got %d at position %d", e.EntrySeq, i)
		}
		seenRecord[e.RecordSeq] = e.RecordDigest
	}
	for _, seq := range res.Seqs {
		if _, ok := seenRecord[seq]; !ok {
			t.Fatalf("T248: record %d has no acceptance entry (concurrent append lost)", seq)
		}
	}
	v := f.view()
	if v.Verdict != inputVerdictOK || v.Window == nil || !v.Window.Continuous || v.Window.Entries != workers*perWorker {
		t.Fatalf("T248: the window must be continuous over %d entries, got %+v", workers*perWorker, v)
	}
}

// ---------------------------------------------------------------------------
// T249 — witness I/O never under the store's locks (anchor+destruction+
// acceptance ALL on ⇒ non-vacuous); both dispatch sources deferred
// ---------------------------------------------------------------------------

func TestP45T249WitnessIODispatchedOnlyAtTickTail(t *testing.T) {
	f := newAcceptanceFixture(t, func(cfg *HistoryExportConfig) {
		cfg.AnchorEndpoint = "file://" + filepath.Join(filepath.Dir(cfg.Dir), "witness")
		cfg.AnchorCapacity = 8
		cfg.DestructionLog = true
		cfg.DestructionCapacity = 16
		cfg.AcceptanceCapacity = 3 // the compaction (and its destruction record) fires on the 4th append
	})

	f.append(acceptT1, 3)
	f.append(acceptT2, 5)
	f.append(acceptT3, 7)

	// The 4th append pushes the ledger over capacity: the compaction fires and
	// its destruction record — plus ALL acceptance anchors — must be COLLECTED
	// ONLY: the append returns while the witness has received NOTHING. Under
	// the pre-B1 inline behaviour the witness would already have been
	// contacted here (holding s.mu + acceptanceMu) and this assertion fails.
	f.append(acceptT4, 9)
	if got := f.witnessLines(); len(got) != 0 {
		t.Fatalf("T249: witness I/O happened under the store's critical section (%d dispatches) — the collect-only discipline is broken", len(got))
	}

	// The tick tail performs the real dispatch.
	f.sched.Tick(context.Background())
	got := f.witnessLines()
	// 4 acceptance + 1 destruction + 1 publication anchors ≥ 5.
	if len(got) < 5 {
		t.Fatalf("T249: the tick tail must dispatch the queued entries, got %d lines", len(got))
	}
	if _, err := os.Stat(acceptanceAnchorPath(f.dir)); err != nil {
		t.Fatalf("the acceptance-anchor stream must exist: %v", err)
	}
	if _, err := os.Stat(destructionAnchorPath(f.dir)); err != nil {
		t.Fatalf("the destruction anchor must exist: %v", err)
	}
}

// ---------------------------------------------------------------------------
// T250 — a LEGAL eviction (drop-oldest) reads outside_span, never missing
// ---------------------------------------------------------------------------

func TestP45T250LegalEvictionReadsOutsideSpan(t *testing.T) {
	f := newAcceptanceFixture(t, nil)
	for i := 0; i < 5; i++ {
		f.append(acceptT1.Add(time.Duration(i)*time.Minute), int64(i))
	}

	// Simulate exactly what evictLocked produces for a legal retention
	// rewrite: meta with file_dropped bumped, newest 3 records kept.
	lines := f.storeLines()
	kept := []string{strings.Replace(lines[0], `"file_dropped":0`, `"file_dropped":2`, 1), lines[3], lines[4], lines[5]}
	f.writeStoreLines(kept)

	v := f.view()
	if v.OutsideSpanCount != 2 {
		t.Fatalf("T250: entries 1,2 (< MinSeq 3) must read outside_span, got %d (%+v)", v.OutsideSpanCount, v)
	}
	if len(v.MissingInSpanIDs) > 0 || len(v.ModifiedIDs) > 0 {
		t.Fatalf("T250: a legal eviction must NEVER read as missing or modified: %+v", v)
	}
	if v.Verdict != inputVerdictOK {
		t.Fatalf("nothing is assertable after a legal eviction ⇒ input_ok, got %s", v.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T251 — window + coverage_floor always output; a hole ⇒ window_discontinuous
// ---------------------------------------------------------------------------

func TestP45T251WindowAndCoverageFloorForcedOutput(t *testing.T) {
	f := newAcceptanceFixture(t, nil)
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)

	v := f.view()
	if v.Window == nil || v.Window.MinEntry != 1 || v.Window.MaxEntry != 2 || !v.Window.Continuous {
		t.Fatalf("T251: the acceptance window must always be output, got %+v", v.Window)
	}
	// coverage_floor is present even when zero (no omitempty).
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"coverage_floor":0`) || !strings.Contains(string(raw), `"acceptance_window"`) {
		t.Fatalf("T251: coverage_floor and acceptance_window are forced output fields, got %s", raw)
	}

	// Punch a hole into the ledger: entry 2 removed, entry 3 survives.
	f.append(acceptT3, 7)
	lines := f.ledgerLines()
	f.writeLedgerLines([]string{lines[0], lines[2]})

	v = f.view()
	if !v.WindowDiscontinuous {
		t.Fatalf("T251: a hole in entry_seq must set window_discontinuous, got %+v", v)
	}
	if v.Verdict != inputVerdictUnverifiable {
		t.Fatalf("a discontinuous window ⇒ the ledger cannot be trusted ⇒ input_unverifiable, got %s", v.Verdict)
	}
	if !strings.Contains(v.InputError, "window_discontinuous") {
		t.Fatalf("input_error must name the discontinuity, got %q", v.InputError)
	}
}

// ---------------------------------------------------------------------------
// T252 — records predating enablement ⇒ outside_coverage, NEVER unaccepted
// ---------------------------------------------------------------------------

func TestP45T252PreEnablementHistoryIsOutsideCoverage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "export")
	keyDir := filepath.Join(root, "keys")
	for _, d := range []string{dir, keyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	signPriv, signPub, _, _ := genKeyPair(t, keyDir, "signer")
	kakPriv, kakPub, _, _ := genKeyPair(t, keyDir, "kak")
	storePath := filepath.Join(root, "alert-transitions.jsonl")
	store, err := NewFileBackedTransitionStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	// Two records accepted BEFORE the phase is enabled (no recorder yet).
	for i, at := range []time.Time{acceptT1, acceptT2} {
		if err := store.Append(context.Background(), protection.AlertTransition{At: at, To: true, UnknownRate: int64(i), Threshold: 50}); err != nil {
			t.Fatal(err)
		}
	}
	sched, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store:                  store,
		Dir:                    dir,
		Interval:               time.Hour,
		Formats:                []string{"json"},
		Clock:                  func() time.Time { return acceptT0 },
		SignKeyPath:            signPriv,
		TrustKeyPaths:          []string{signPub},
		KeyAuthorityPath:       kakPriv,
		KeyAuthorityTrustPaths: []string{kakPub},
		AcceptanceLog:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Two more records accepted AFTER enablement.
	for _, at := range []time.Time{acceptT3, acceptT4} {
		if err := store.Append(context.Background(), protection.AlertTransition{At: at, To: true, UnknownRate: 9, Threshold: 50}); err != nil {
			t.Fatal(err)
		}
	}

	v := sched.InputIntegrityView(context.Background())
	if v.CoverageFloor != 2 {
		t.Fatalf("coverage_floor = first accepted record_seq - 1 = 2, got %d", v.CoverageFloor)
	}
	if v.OutsideCoverageCount != 2 {
		t.Fatalf("T252: records 1,2 predate enablement ⇒ outside_coverage, got %d (%+v)", v.OutsideCoverageCount, v)
	}
	if len(v.UnacceptedIDs) > 0 {
		t.Fatalf("T252: pre-enablement history must NEVER read unaccepted, got %v", v.UnacceptedIDs)
	}
	if v.Verdict != inputVerdictOK {
		t.Fatalf("the post-enablement records are intact ⇒ input_ok, got %s (%+v)", v.Verdict, v)
	}
}

// ---------------------------------------------------------------------------
// T254 — deleting the newest record / rolling the store file back ⇒ the
// ledger's high-seq entries read missing_in_span (NO legal upper-bound eviction)
// ---------------------------------------------------------------------------

func TestP45T254DeletedNewestOrRollbackReadsMissingInSpan(t *testing.T) {
	f := newAcceptanceFixture(t, nil)
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)
	f.append(acceptT3, 7)

	// (a) delete the NEWEST record: the ledger entry 3 sits above MaxSeq.
	lines := f.storeLines()
	f.writeStoreLines([]string{lines[0], lines[1], lines[2]})
	v := f.view()
	if !idsEq(v.MissingInSpanIDs, 3) {
		t.Fatalf("T254: deleting the newest record must read missing_in_span (no legal upper-bound eviction), got %v", v.MissingInSpanIDs)
	}
	if v.Verdict != inputVerdictModified {
		t.Fatalf("an assertable deletion ⇒ input_modified, got %s", v.Verdict)
	}

	// (b) roll the store file back to record 1 only ⇒ entries 2,3 above MaxSeq.
	f.writeStoreLines([]string{lines[0], lines[1]})
	v = f.view()
	if !idsEq(v.MissingInSpanIDs, 2, 3) {
		t.Fatalf("T254: a rolled-back store file must expose the lost records, got %v", v.MissingInSpanIDs)
	}
}

// ---------------------------------------------------------------------------
// T255 — store Corrupt / LoadErr ⇒ input_unverifiable, never input_ok
// ---------------------------------------------------------------------------

func TestP45T255UnverifiableStoreNeverInputOK(t *testing.T) {
	f := newAcceptanceFixture(t, nil)
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)

	// (a) corrupt the durable history (a full middle line that cannot parse).
	lines := f.storeLines()
	f.writeStoreLines([]string{lines[0], lines[1], "<<<CORRUPT>>>", lines[2]})
	v := f.view()
	if v.Verdict != inputVerdictUnverifiable {
		t.Fatalf("T255: a corrupt store ⇒ input_unverifiable, got %s (%+v)", v.Verdict, v)
	}
	if !strings.Contains(v.InputError, "corrupt") {
		t.Fatalf("input_error must name the corruption, got %q", v.InputError)
	}

	// (b) the store file is gone entirely ⇒ LoadErr ⇒ input_unverifiable.
	if err := os.Remove(f.storePath); err != nil {
		t.Fatal(err)
	}
	v = f.view()
	if v.Verdict != inputVerdictUnverifiable {
		t.Fatalf("T255: an unreadable store ⇒ input_unverifiable, got %s (%+v)", v.Verdict, v)
	}
	if v.InputError == "" {
		t.Fatal("the store failure must be named (input_error)")
	}
}

// ---------------------------------------------------------------------------
// T256 — compaction refused (accounting unavailable) ⇒ new entries refused,
// the record stays durable + unaccepted, and NO unaccounted trim happens
// ---------------------------------------------------------------------------

func TestP45T256RefusedCompactionRejectsNewEntries(t *testing.T) {
	f := newAcceptanceFixture(t, func(cfg *HistoryExportConfig) {
		cfg.DestructionLog = true
		cfg.DestructionCapacity = 16
		cfg.AcceptanceCapacity = 2
	})
	f.append(acceptT1, 3)
	f.append(acceptT2, 5) // ledger now holds its capacity of 2 groups

	// Break the destruction ledger: the acceptance ledger's compaction observer
	// cannot account for a trim, so the compaction is REFUSED.
	if err := os.WriteFile(destructionLogPath(f.dir), []byte("<<<NOT A DESTRUCTION ENTRY>>>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destBefore, err := os.ReadFile(destructionLogPath(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	ledgerBefore := f.ledgerLines()

	// Record 3: durable, but its acceptance is refused (I7) — the alternative
	// (an unaccounted trim) is exactly what R43-4 forbids.
	f.append(acceptT3, 7)
	if len(f.storeLines()) != 4 { // meta + 3 records
		t.Fatalf("T256: the record must stay durable, got %d lines", len(f.storeLines()))
	}
	if got := len(f.ledgerLines()); got != 2 {
		t.Fatalf("T256: the ledger must keep exactly its capacity (no unaccounted trim), got %d entries", got)
	}
	ledgerAfter := f.ledgerLines()
	for i := range ledgerBefore {
		if ledgerBefore[i] != ledgerAfter[i] {
			t.Fatalf("T256: the surviving entries must be byte-identical (no rewrite around the refusal)")
		}
	}
	destAfter, err := os.ReadFile(destructionLogPath(f.dir))
	if err != nil || string(destBefore) != string(destAfter) {
		t.Fatalf("T256: the destruction log must stay byte-identical on refusal")
	}
	if got := f.store.AcceptanceError(); got == "" || !strings.Contains(got, "I7") {
		t.Fatalf("T256: the refusal must surface loudly, got %q", got)
	}

	v := f.view()
	if !idsEq(v.UnacceptedIDs, 3) {
		t.Fatalf("T256: the refused record must read unaccepted (loud), got %v", v.UnacceptedIDs)
	}
	if v.Verdict != inputVerdictIncomplete {
		t.Fatalf("record_unaccepted ⇒ input_incomplete, got %s", v.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T257 — golden digest fixture (I2 drift guard) + intact re-export after a
// legal compaction (I4 chain-head exemption)
// ---------------------------------------------------------------------------

func TestP45T257GoldenDigestAndIntactAfterCompaction(t *testing.T) {
	// (a) golden digest: the canonical durable bytes of a FIXED record must
	// hash to a frozen value. If newPersisted's field order or the JSON shape
	// ever drifts across versions, THIS goes red before any false
	// record_modified can reach a user.
	tr := protection.AlertTransition{
		At:          time.Date(2026, 9, 1, 12, 34, 56, 789000000, time.UTC),
		From:        true,
		To:          false,
		UnknownRate: 7,
		Threshold:   50,
	}
	line, err := json.Marshal(newPersisted(42, tr))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(line)
	const golden = "f29f551067a1915918826b1f777dede1aeedc1b88e98394713fa55d6dd89cde9"
	if got := hex.EncodeToString(sum[:]); got != golden {
		t.Fatalf("T257 golden digest drifted — the canonical durable bytes changed (I2):\n got  %s\n want %s\n line %s", got, golden, line)
	}

	// (b) unmodified records re-export intact after a legal compaction: the
	// chain head is exempt (I4) and the rebuilt bytes match (I2).
	f := newAcceptanceFixture(t, func(cfg *HistoryExportConfig) {
		cfg.AcceptanceCapacity = 2 // compaction fires on the 3rd append
		cfg.DestructionLog = true
		cfg.DestructionCapacity = 16
	})
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)
	f.append(acceptT3, 7) // entry 1 legally compacted away (destruction-accounted)

	v := f.view()
	if v.Verdict != inputVerdictOK {
		t.Fatalf("T257: after a legal compaction the surviving records must read intact (input_ok), got %s (%+v)", v.Verdict, v)
	}
	if v.Window == nil || v.Window.MinEntry != 2 {
		t.Fatalf("the surviving window starts at entry 2, got %+v", v.Window)
	}
	if len(v.ModifiedIDs) > 0 {
		t.Fatalf("T257: a re-marshal drift would show here as a false record_modified: %v", v.ModifiedIDs)
	}
}

// ---------------------------------------------------------------------------
// Construction guards (G1/G2) and the read route when enabled
// ---------------------------------------------------------------------------

func TestP45ConstructionGuardsAndRoute(t *testing.T) {
	f := newAcceptanceFixture(t, nil)

	// G1: acceptance on without a KAK private key ⇒ fail-fast construction.
	_, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store: f.store, Dir: f.dir, Interval: time.Hour, Formats: []string{"json"},
		AcceptanceLog: true,
	})
	if err == nil || !strings.Contains(err.Error(), "acceptance") {
		t.Fatalf("G1: construction must refuse a KAK-less acceptance log, got %v", err)
	}

	// G2: acceptance on with a KAK that has no trust anchor ⇒ fail-fast
	// construction. (The Phase 41 block already refuses a KAK without a trust
	// anchor; the P45 G2 guard is defense-in-depth behind it — construction
	// must fail either way, never "enable first, add keys later".)
	keys2 := filepath.Join(f.root, "keys2")
	if err := os.MkdirAll(keys2, 0o755); err != nil {
		t.Fatal(err)
	}
	signPriv, signPub, _, _ := genKeyPair(t, keys2, "s2")
	kakOnly, _, _, _ := genKeyPair(t, keys2, "k2")
	if _, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store: f.store, Dir: f.dir, Interval: time.Hour, Formats: []string{"json"},
		SignKeyPath: signPriv, TrustKeyPaths: []string{signPub},
		KeyAuthorityPath: kakOnly, AcceptanceLog: true,
	}); err == nil {
		t.Fatal("G2: construction must refuse a KAK without a trust anchor when the acceptance log is on")
	}

	// The enabled route answers 200 with the reconciliation.
	f.append(acceptT1, 3)
	srv, token := newProtectionTestServer(t, false)
	srv.historyScheduler = f.sched
	req := httptest.NewRequest(http.MethodGet, "/management/v1/protection/alerts/history/export/input-integrity", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.handleHistoryExportInputIntegrity(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enabled route: want 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	var got InputIntegrityView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Verdict != inputVerdictOK || !got.Enabled {
		t.Fatalf("the route must serve the reconciliation verdict, got %+v", got)
	}
}
