package server

// Phase 46 — Witness Reconciliation tests (T258~T273; ADR-067 §5, ADR-068 §7).
//
// The fixture family drives ALL five anchored families through their REAL
// write paths (publications, a lifecycle event, an attestation, a
// compaction-accounted destruction, acceptance commits) into the offline dev
// witness, so every projection the reconcile face consumes is a REAL dispatch
// byte — the anchorRequest JSON the witness verbatim appended (ADR-067 §2-1).
// Nothing in this suite hand-writes an anchorRequest from an anchorEntry: the
// projections are read back from witness.jsonl and split per family by digest
// cross-reference against the family anchor logs.

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
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

var p46T0 = time.Date(2026, 9, 18, 6, 0, 0, 0, time.UTC)

func p46At(n int) time.Time { return p46T0.Add(time.Duration(n) * time.Hour) }

type p46Fixture struct {
	t          *testing.T
	root       string
	dir        string // export dir
	storePath  string
	store      *FileBackedTransitionStore
	sched      *HistoryExportScheduler
	cfg        HistoryExportConfig
	witnessDir string
	privPath   string
	pubPath    string
	seq        int64 // last durable store seq allocated via append()
}

// newP46Fixture builds a store + scheduler with EVERY anchored family on:
// publications, key lifecycle (KAK), verification attestation, destruction
// accountability (required by G4 when acceptance is on) and the acceptance
// ledger. tune may adjust the config before construction.
func newP46Fixture(t *testing.T, tune func(cfg *HistoryExportConfig)) *p46Fixture {
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
		Clock:                  func() time.Time { return p46T0 },
		SignKeyPath:            signPriv,
		TrustKeyPaths:          []string{signPub},
		KeyAuthorityPath:       kakPriv,
		KeyAuthorityTrustPaths: []string{kakPub},
		AnchorEndpoint:         "file://" + witnessDir,
		AnchorMaxAttempts:      8,
		AcceptanceLog:          true,
		DestructionLog:         true,
		VerifyAttest:           true,
	}
	if tune != nil {
		tune(&cfg)
	}
	sched, err := NewHistoryExportScheduler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &p46Fixture{
		t: t, root: root, dir: dir, storePath: storePath, store: store,
		sched: sched, cfg: cfg, witnessDir: witnessDir,
		privPath: signPriv, pubPath: signPub,
	}
}

// append commits one transition (and, with the acceptance ledger on, one KAK-
// signed acceptance entry) and returns its durable seq.
func (f *p46Fixture) append(at time.Time, unknownRate int64) int64 {
	f.t.Helper()
	f.seq++
	err := f.store.Append(context.Background(), protection.AlertTransition{
		At: at, From: false, To: true, UnknownRate: unknownRate, Threshold: 50,
	})
	if err != nil {
		f.t.Fatalf("append: %v", err)
	}
	return f.seq
}

// publish runs one tick that exports the store's current content as one
// publication (manifest + ledger entry + anchor).
func (f *p46Fixture) publish(exp time.Time, minSeq, maxSeq int64) {
	f.t.Helper()
	f.sched.Tick(context.Background())
}

// driveAllFamilies populates EVERY family through its real write path:
//
//	ledger       seq 1..3   (three publications; #3 compacts the ledger with capacity 2)
//	acceptance   3 entries  (anchored at the tick tails)
//	key_lifecycle 1 event
//	verification 1 report
//	destruction  1 record   (the ledger compaction at publication #3)
func (f *p46Fixture) driveAllFamilies() {
	f.t.Helper()
	f.append(p46At(1), 3)
	f.append(p46At(2), 5)
	if _, err := f.sched.AppendKeyLifecycleEvent(keyLifecycleRequest{
		EventType: lifecycleEventActivated, KeyID: f.sched.signer.keyID, NotBefore: klTS(p46T0),
	}); err != nil {
		f.t.Fatalf("lifecycle event: %v", err)
	}
	f.publish(p46At(1), 1, 2)
	f.publish(p46At(2), 3, 4)
	if _, err := f.sched.AttestVerification(10); err != nil {
		f.t.Fatalf("attestation: %v", err)
	}
	f.append(p46At(3), 7)
	// LedgerCapacity=2 ⇒ publication #3 pushes the chain ledger over capacity,
	// the oldest entry is dropped, and the destruction is recorded AND anchored.
	f.publish(p46At(3), 5, 6)
}

// rawWitnessItems reads witness.jsonl verbatim — the REAL dispatch bytes.
func (f *p46Fixture) rawWitnessItems() []anchorRequest {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.witnessDir, witnessFile))
	if err != nil {
		f.t.Fatalf("read witness: %v", err)
	}
	var out []anchorRequest
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var req anchorRequest
		if jerr := json.Unmarshal([]byte(ln), &req); jerr != nil {
			f.t.Fatalf("witness line is not an anchorRequest: %v (%s)", jerr, ln)
		}
		out = append(out, req)
	}
	return out
}

// witnessByFamily splits the REAL dispatch bytes per family by digest
// cross-reference: each family's local anchor log derives the digests of its
// own entries, and a projection belongs to the family whose entry set contains
// its anchor_digest (payloads differ across families by construction). This is
// the same grouping decision a real caller makes from the witness it holds.
func (f *p46Fixture) witnessByFamily() map[string][]anchorRequest {
	f.t.Helper()
	out := map[string][]anchorRequest{}
	byDigest := map[string]string{}
	for _, fam := range witnessFamilyRegistry() {
		st, err := loadAnchorStatePath(fam.AnchorPath(f.dir), f.dir, nil)
		if err != nil {
			continue
		}
		for _, e := range st.latest {
			dg, derr := anchorDigestOf(&e)
			if derr != nil {
				f.t.Fatalf("digest: %v", derr)
			}
			byDigest[dg] = fam.Name
		}
	}
	for _, req := range f.rawWitnessItems() {
		fam, ok := byDigest[req.AnchorDigest]
		if !ok {
			f.t.Fatalf("witness projection (seq %d) matches no family anchor entry — fixture integrity broken", req.AnchorSeq)
		}
		out[fam] = append(out[fam], req)
	}
	return out
}

// expectFamilyCount pins the fixture's shape so a silent write-path regression
// cannot make the discriminators vacuous.
func (f *p46Fixture) expectFamilyCount(byFamily map[string][]anchorRequest, name string, want int) {
	f.t.Helper()
	if got := len(byFamily[name]); got != want {
		f.t.Fatalf("fixture: family %q holds %d witness projections, want %d", name, got, want)
	}
}

// postReconcile drives the POST handler through the registered mux route with
// admin auth + same-origin (the route's own CSRF discipline).
func (f *p46Fixture) postReconcile(sched *HistoryExportScheduler, submitted map[string][]anchorRequest) (*httptest.ResponseRecorder, witnessReconcileResult) {
	f.t.Helper()
	srv, token := newProtectionTestServer(f.t, false)
	srv.historyScheduler = sched
	body, err := json.Marshal(map[string]any{"families": submitted})
	if err != nil {
		f.t.Fatal(err)
	}
	h := srv.ProtectionReadMux()
	r := httptest.NewRequest(http.MethodPost, "/management/v1/protection/alerts/history/export/witness-reconcile", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var res witnessReconcileResult
	if w.Code == http.StatusOK {
		if jerr := json.Unmarshal(w.Body.Bytes(), &res); jerr != nil {
			f.t.Fatalf("decode reconcile response: %v (body=%s)", jerr, w.Body.String())
		}
	}
	return w, res
}

// getView drives the GET handler through the registered mux route.
func (f *p46Fixture) getView(sched *HistoryExportScheduler) (*httptest.ResponseRecorder, witnessReconcileLocalView) {
	f.t.Helper()
	srv, token := newProtectionTestServer(f.t, false)
	srv.historyScheduler = sched
	h := srv.ProtectionReadMux()
	r := httptest.NewRequest(http.MethodGet, "/management/v1/protection/alerts/history/export/witness-reconcile", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var view witnessReconcileLocalView
	if w.Code == http.StatusOK {
		if jerr := json.Unmarshal(w.Body.Bytes(), &view); jerr != nil {
			f.t.Fatalf("decode view response: %v (body=%s)", jerr, w.Body.String())
		}
	}
	return w, view
}

// drop removes one file, tolerating only "already gone".
func (f *p46Fixture) drop(path string) {
	f.t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
}

// filesSnapshot hashes every file under dir (recursive) — the zero-side-effect
// witness.
func p46FilesSnapshot(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range dirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			sum := sha256.Sum256(data)
			out[path] = hex.EncodeToString(sum[:])
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("snapshot %s: %v", dir, err)
		}
	}
	return out
}

// p46AlarmTransport fails the test if anything tries to reach the witness.
type p46AlarmTransport struct{ t *testing.T }

func (a p46AlarmTransport) deliver(_ context.Context, _ anchorRequest) (int, []byte, error) {
	a.t.Error("the reconcile face must never touch the witness transport (I5)")
	return 0, nil, fmt.Errorf("alarm: network use during reconciliation")
}

// p46NormalizeDigests masks the 64-hex digest values in a response body. The
// digests are DERIVED from the deployment identity (R40-2 binds stream_id —
// and with it every anchor digest — to the absolute export directory), so they
// legitimately differ between temp directories. Everything else in the
// response — field order, verdicts, windows, states, lists — must be and is
// compared byte-for-byte against the golden captured at the base commit.
// ---------------------------------------------------------------------------

var p46T258T0 = p46T0

var p46DigestRe = regexp.MustCompile(`[0-9a-f]{64}`)

func p46NormalizeDigests(body string) string {
	return p46DigestRe.ReplaceAllString(body, "DIGEST")
}

const p40GoldenGetAnchor = `{"enabled":true,"witness_trust":"not_provided","verdict":"anchor_incomplete","anchor_window":{"min_seq":1,"max_seq":2,"entries":2,"continuous":true},"orphan_witness_ids":[],"outside_window_ids":[],"ahead_of_window_ids":[],"divergent_ids":[],"missing_witness_ids":[],"digest_mismatch_ids":[],"pending_ids":[],"unanchored_ids":[],"local_anchor_entries":2,"items":[{"publication_id":1,"anchor_state":"anchored","anchor_seq":1,"anchor_digest":"f446a330f7d7a8931b61b761a25129728750b3db3066747049a50632add3777d","witness":"not_evaluated"},{"publication_id":2,"anchor_state":"anchored","anchor_seq":2,"anchor_digest":"1cf27421ba0c96bcea3914fce4ef41003fbea7efc9b776e5595371fd1657efdf","witness":"not_evaluated"}]}` + "\n"

const p40GoldenPostReconcile = `{"enabled":true,"witness_trust":"aligned","verdict":"anchor_ok","anchor_window":{"min_seq":1,"max_seq":2,"entries":2,"continuous":true},"orphan_witness_ids":[],"outside_window_ids":[],"ahead_of_window_ids":[],"divergent_ids":[],"missing_witness_ids":[],"digest_mismatch_ids":[],"pending_ids":[],"unanchored_ids":[],"local_anchor_entries":2,"items":[{"publication_id":1,"anchor_state":"anchored","anchor_seq":1,"anchor_digest":"f446a330f7d7a8931b61b761a25129728750b3db3066747049a50632add3777d","witness":"witnessed"},{"publication_id":2,"anchor_state":"anchored","anchor_seq":2,"anchor_digest":"1cf27421ba0c96bcea3914fce4ef41003fbea7efc9b776e5595371fd1657efdf","witness":"witnessed"}]}` + "\n"

func TestP46T258P40ReconcileFaceByteFrozen(t *testing.T) {
	root := t.TempDir()
	snapDir := filepath.Join(root, "snapshots")
	witDir := filepath.Join(root, "witness")
	keyDir := filepath.Join(root, "keys")
	for _, d := range []string{snapDir, witDir, keyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	privPath, pubPath, _, _ := genKeyPair(t, keyDir, "keyA")
	st := &fakeExportStore{}
	s, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store: st, Dir: snapDir, Interval: time.Hour, Formats: []string{"json"},
		Clock:       func() time.Time { return p46T258T0 },
		SignKeyPath: privPath, TrustKeyPaths: []string{pubPath},
		AnchorEndpoint: "file://" + witDir, AnchorMaxAttempts: 8, AnchorCapacity: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.res = protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: p46T258T0, MinSeq: 1, MaxSeq: 10}
	st.mu.Unlock()
	s.Tick(context.Background())
	st.mu.Lock()
	st.res = protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: p46T258T0.Add(time.Hour), MinSeq: 11, MaxSeq: 20}
	st.mu.Unlock()
	s.Tick(context.Background())

	srv, token := newProtectionTestServer(t, false)
	srv.historyScheduler = s
	h := srv.ProtectionReadMux()

	// GET /anchor — the Phase 40 local-state projection.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/management/v1/protection/alerts/history/export/anchor", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("P40 GET /anchor: want 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	if got, want := p46NormalizeDigests(w.Body.String()), p46NormalizeDigests(p40GoldenGetAnchor); got != want {
		t.Fatalf("I1 VIOLATION: the Phase 40 GET /anchor response is not byte-frozen:\n got %q\nwant %q", got, want)
	}

	// POST /anchor/reconcile — the Phase 40 reconcile face with the honest
	// witness sequence (rebuilt from the local log, as the fixture always did).
	trust := mustTrust(t, pubPath)
	ast, lerr := loadAnchorState(snapDir, trust)
	if lerr != nil {
		t.Fatal(lerr)
	}
	var parts []string
	for _, sq := range ast.seqs() {
		e := ast.latest[sq]
		dg, derr := anchorDigestOf(&e)
		if derr != nil {
			t.Fatal(derr)
		}
		parts = append(parts, fmt.Sprintf(`{"publication_id":%d,"anchor_seq":%d,"anchor_digest":%q,"key_id":%q,"stream_id":%q}`,
			e.PublicationID, e.AnchorSeq, dg, e.KeyID, e.StreamID))
	}
	body := `{"sequence":[` + strings.Join(parts, ",") + `]}`
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/management/v1/protection/alerts/history/export/anchor/reconcile", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("P40 POST reconcile: want 200, got %d (body=%s)", w2.Code, w2.Body.String())
	}
	if got, want := p46NormalizeDigests(w2.Body.String()), p46NormalizeDigests(p40GoldenPostReconcile); got != want {
		t.Fatalf("I1 VIOLATION: the Phase 40 reconcile response is not byte-frozen:\n got %q\nwant %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// T259 — real dispatch bytes × local files all present ⇒ every family intact.
// ---------------------------------------------------------------------------

func TestP46T259AllFamiliesIntactFromRealDispatchBytes(t *testing.T) {
	f := newP46Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	f.driveAllFamilies()

	byFamily := f.witnessByFamily()
	f.expectFamilyCount(byFamily, witnessFamilyLedger, 3)
	f.expectFamilyCount(byFamily, witnessFamilyAcceptance, 3)
	f.expectFamilyCount(byFamily, witnessFamilyKeyLifecycle, 1)
	f.expectFamilyCount(byFamily, witnessFamilyVerification, 1)
	f.expectFamilyCount(byFamily, witnessFamilyDestruction, 1)

	_, res := f.postReconcile(f.sched, byFamily)
	if len(res.Families) != 5 {
		t.Fatalf("the aggregate must cover the five registered families, got %v", res.Families)
	}
	for _, name := range []string{witnessFamilyLedger, witnessFamilyKeyLifecycle, witnessFamilyDestruction, witnessFamilyVerification, witnessFamilyAcceptance} {
		row := res.Families[name]
		if row.Verdict != witnessVerdictIntact {
			t.Fatalf("T259: family %q verdict = %q (err=%q, row=%+v), want family_intact", name, row.Verdict, row.Error, row)
		}
		if !row.IdentityOK || row.MissingCount != 0 || row.OutsideCount != 0 || len(row.DivergentSeqs) != 0 || len(row.TruncatedRanges) != 0 {
			t.Fatalf("T259: family %q must be fully aligned: %+v", name, row)
		}
	}
}

// ---------------------------------------------------------------------------
// T260 — THE CORE DISCRIMINATOR: deleting the acceptance ledger AND its anchor
// log silences P35~P45 (P45 reads input_absent — "locally indistinguishable"),
// and ONLY P46 asserts family_ledger_deleted.
// ---------------------------------------------------------------------------

func TestP46T260AcceptanceDeletedOnlyP46Speaks(t *testing.T) {
	f := newP46Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	f.driveAllFamilies()
	byFamily := f.witnessByFamily()
	f.expectFamilyCount(byFamily, witnessFamilyAcceptance, 3)

	// The deletion.
	f.drop(acceptanceLogPath(f.dir))
	f.drop(acceptanceAnchorPath(f.dir))

	// P35~P45 stay silent about the deletion:
	//  - P45 input integrity: the honest "locally indistinguishable" state.
	if v := f.sched.InputIntegrityView(context.Background()); v.Verdict != inputVerdictAbsent {
		t.Fatalf("T260: P45 must stay silent (input_absent), got %q", v.Verdict)
	}
	//  - P40 publication reconcile: the publication family is unaffected and
	//    its face has no vocabulary that could name the acceptance deletion.
	res40, rerr := f.sched.ReconcileAnchor(nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if res40.Verdict != anchorVerdictIncomplete {
		t.Fatalf("T260: P40 must stay on its own vocabulary (anchor_incomplete), got %q", res40.Verdict)
	}

	// P46 is the ONLY surface that asserts the deletion.
	_, res := f.postReconcile(f.sched, byFamily)
	row := res.Families[witnessFamilyAcceptance]
	if row.Verdict != witnessVerdictDeleted {
		t.Fatalf("T260: P46 must assert the deletion, got %q (row=%+v)", row.Verdict, row)
	}
	if !row.IdentityOK || row.WitnessEntries != 3 {
		t.Fatalf("T260: the deleted verdict requires identity-matched witness entries: %+v", row)
	}
	if len(row.TruncatedRanges) != 0 || len(row.DivergentSeqs) != 0 || row.MissingCount != 0 || row.OutsideCount != 0 {
		t.Fatalf("T260: a deleted verdict carries NO window assertions: %+v", row)
	}
	// ...and the other families are untouched by the acceptance deletion.
	if res.Families[witnessFamilyLedger].Verdict != witnessVerdictIntact {
		t.Fatalf("T260: the ledger family must be unaffected: %+v", res.Families[witnessFamilyLedger])
	}
}

// ---------------------------------------------------------------------------
// T261 — the second family: deleting the lifecycle ledger AND its anchor log
// ⇒ family_ledger_deleted.
// ---------------------------------------------------------------------------

func TestP46T261LifecycleDeleted(t *testing.T) {
	f := newP46Fixture(t, func(cfg *HistoryExportConfig) { cfg.LedgerCapacity = 2 })
	f.driveAllFamilies()
	byFamily := f.witnessByFamily()
	f.expectFamilyCount(byFamily, witnessFamilyKeyLifecycle, 1)

	f.drop(keyLifecycleLogPath(f.dir))
	f.drop(keyLifecycleAnchorPath(f.dir))

	_, res := f.postReconcile(f.sched, byFamily)
	if row := res.Families[witnessFamilyKeyLifecycle]; row.Verdict != witnessVerdictDeleted {
		t.Fatalf("T261: want family_ledger_deleted for key_lifecycle, got %q (row=%+v)", row.Verdict, row)
	} else if !row.IdentityOK || row.WitnessEntries != 1 {
		t.Fatalf("T261: the deleted verdict requires identity-matched entries: %+v", row)
	}
}

// ---------------------------------------------------------------------------
// T262 — witness seq ABOVE the local anchor window's upper bound ⇒
// family_ledger_truncated, no waiver layer. The construction IS the proof of
// mechanical unreachability: the only way this state exists is a physically
// truncated tail (R40-6 + prefix-only compaction leave no legal exit).
// ---------------------------------------------------------------------------

func TestP46T262TailTruncationAsserted(t *testing.T) {
	f := newP46Fixture(t, nil)
	f.driveAllFamilies()
	byFamily := f.witnessByFamily()
	f.expectFamilyCount(byFamily, witnessFamilyAcceptance, 3)

	// Physically truncate the tail: drop every anchor-log line of the seq
	// groups ABOVE 1. (The witness bytes were captured BEFORE the truncation —
	// the witness keeps what it received, the local log no longer does.)
	p46DropAnchorTailGroups(t, acceptanceAnchorPath(f.dir), 1)

	_, res := f.postReconcile(f.sched, byFamily)
	row := res.Families[witnessFamilyAcceptance]
	if row.Verdict != witnessVerdictTruncated {
		t.Fatalf("T262: want family_ledger_truncated (no waiver layer), got %q (row=%+v)", row.Verdict, row)
	}
	if len(row.TruncatedRanges) != 1 || row.TruncatedRanges[0] != (witnessSeqRange{From: 2, To: 3}) {
		t.Fatalf("T262: truncated_ranges must be [{2,3}], got %+v", row.TruncatedRanges)
	}
}

// p46DropAnchorTailGroups rewrites an anchor log keeping only seq groups
// <= keepMax — a faithful tail truncation.
func p46DropAnchorTailGroups(t *testing.T, path string, keepMax int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var e anchorEntry
		if jerr := json.Unmarshal([]byte(ln), &e); jerr != nil {
			t.Fatalf("anchor line unparseable: %v", jerr)
		}
		if e.AnchorSeq <= keepMax {
			kept = append(kept, ln)
		}
	}
	body := ""
	if len(kept) > 0 {
		body = strings.Join(kept, "\n") + "\n"
	}
	if werr := os.WriteFile(path, []byte(body), 0o644); werr != nil {
		t.Fatal(werr)
	}
}

// ---------------------------------------------------------------------------
// T263 — same seq inside the window, different digest ⇒ family_divergent,
// distinguishable from truncated (the local window still covers the seq).
// ---------------------------------------------------------------------------

func TestP46T263InWindowDivergentDigest(t *testing.T) {
	f := newP46Fixture(t, nil)
	f.driveAllFamilies()
	byFamily := f.witnessByFamily()
	f.expectFamilyCount(byFamily, witnessFamilyAcceptance, 3)

	// Tamper the LOCAL anchor log's seq-1 group payload (every line of the
	// group, so the log stays conflict-free): the derived digest of the local
	// entry changes, the witness-held digest does not.
	p46TamperAnchorGroup(t, acceptanceAnchorPath(f.dir), 1)

	_, res := f.postReconcile(f.sched, byFamily)
	row := res.Families[witnessFamilyAcceptance]
	if row.Verdict != witnessVerdictDivergent {
		t.Fatalf("T263: want family_divergent, got %q (row=%+v)", row.Verdict, row)
	}
	if len(row.DivergentSeqs) != 1 || row.DivergentSeqs[0] != 1 {
		t.Fatalf("T263: divergent_seqs must be [1], got %v", row.DivergentSeqs)
	}
	if len(row.TruncatedRanges) != 0 {
		t.Fatalf("T263: divergence must be distinguishable from truncation: %+v", row.TruncatedRanges)
	}
}

// p46TamperAnchorGroup rewrites every line of one seq group with a poisoned
// manifest_digest (same shape, different payload ⇒ different derived digest).
func p46TamperAnchorGroup(t *testing.T, path string, seq int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var e anchorEntry
		if jerr := json.Unmarshal([]byte(ln), &e); jerr != nil {
			t.Fatalf("anchor line unparseable: %v", jerr)
		}
		if e.AnchorSeq == seq {
			e.ManifestDigest = "p46-tampered-digest"
			fresh, merr := json.Marshal(e)
			if merr != nil {
				t.Fatal(merr)
			}
			ln = string(fresh)
		}
		out = append(out, ln)
	}
	if werr := os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}
}

// ---------------------------------------------------------------------------
// T264 — the witness misses part of what the local log holds ⇒
// family_incomplete. NEVER broken: witness retention is unknowable.
// ---------------------------------------------------------------------------

func TestP46T264MissingWitnessReadsIncomplete(t *testing.T) {
	f := newP46Fixture(t, nil)
	f.driveAllFamilies()
	byFamily := f.witnessByFamily()

	// Submit ONLY the oldest acceptance projection.
	submitted := map[string][]anchorRequest{
		witnessFamilyAcceptance: {byFamily[witnessFamilyAcceptance][0]},
	}
	_, res := f.postReconcile(f.sched, submitted)
	row := res.Families[witnessFamilyAcceptance]
	if row.Verdict != witnessVerdictIncomplete {
		t.Fatalf("T264: want family_incomplete, got %q (row=%+v)", row.Verdict, row)
	}
	if row.MissingCount != 2 {
		t.Fatalf("T264: missing_count must be 2, got %d", row.MissingCount)
	}
	if strings.Contains(row.Verdict, "broken") {
		t.Fatalf("T264: a missing witness must NEVER read broken: %q", row.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T265 — ONE foreign-stream projection poisons the WHOLE family (I2): zero
// assertions survive, not even partial ones.
// ---------------------------------------------------------------------------

func TestP46T265ForeignStreamPoisonsFamily(t *testing.T) {
	f := newP46Fixture(t, nil)
	f.driveAllFamilies()
	byFamily := f.witnessByFamily()

	items := append([]anchorRequest(nil), byFamily[witnessFamilyAcceptance]...)
	items[0].StreamID = "0000000000000000" // a different deployment's stream
	_, res := f.postReconcile(f.sched, map[string][]anchorRequest{witnessFamilyAcceptance: items})

	row := res.Families[witnessFamilyAcceptance]
	if row.Verdict != witnessVerdictUnverifiable {
		t.Fatalf("T265: one foreign-stream item must poison the family, got %q (row=%+v)", row.Verdict, row)
	}
	if row.IdentityOK {
		t.Fatal("T265: identity_ok must be false on a poisoned family")
	}
	if row.OutsideCount != 0 || len(row.DivergentSeqs) != 0 || len(row.TruncatedRanges) != 0 || row.MissingCount != 0 {
		t.Fatalf("T265: a poisoned family must carry ZERO assertions: %+v", row)
	}
}

// ---------------------------------------------------------------------------
// T266 — main ledger present ∧ anchor log absent ⇒ family_incomplete
// (review MAJOR-2 discriminator): a missing anchor log must NEVER read
// truncated, even though the local window is gone.
// ---------------------------------------------------------------------------

func TestP46T266AnchorAbsentLedgerPresentIsIncomplete(t *testing.T) {
	f := newP46Fixture(t, nil)
	f.driveAllFamilies()
	byFamily := f.witnessByFamily() // captured while both files still exist

	f.drop(acceptanceAnchorPath(f.dir)) // anchor log gone, ledger intact

	_, res := f.postReconcile(f.sched, byFamily)
	row := res.Families[witnessFamilyAcceptance]
	if row.Verdict != witnessVerdictIncomplete {
		t.Fatalf("T266: want family_incomplete, got %q (row=%+v)", row.Verdict, row)
	}
	if row.Verdict == witnessVerdictTruncated || len(row.TruncatedRanges) != 0 {
		t.Fatalf("T266: a missing anchor log must NEVER raise the truncated false alarm: %+v", row)
	}
}

// ---------------------------------------------------------------------------
// T267 — cross-family isolation: a poisoned acceptance submission must not
// touch the lifecycle family's verdict in the SAME aggregate.
// ---------------------------------------------------------------------------

func TestP46T267CrossFamilyIsolation(t *testing.T) {
	f := newP46Fixture(t, nil)
	f.driveAllFamilies()
	byFamily := f.witnessByFamily()

	poisoned := append([]anchorRequest(nil), byFamily[witnessFamilyAcceptance]...)
	poisoned[0].StreamID = "0000000000000000"
	submitted := map[string][]anchorRequest{
		witnessFamilyAcceptance:   poisoned,
		witnessFamilyKeyLifecycle: byFamily[witnessFamilyKeyLifecycle],
	}
	_, res := f.postReconcile(f.sched, submitted)
	if row := res.Families[witnessFamilyAcceptance]; row.Verdict != witnessVerdictUnverifiable {
		t.Fatalf("T267: the poisoned family must read unverifiable, got %q", row.Verdict)
	}
	if row := res.Families[witnessFamilyKeyLifecycle]; row.Verdict != witnessVerdictIntact {
		t.Fatalf("T267: the healthy family must be untouched (I7), got %q (row=%+v)", row.Verdict, row)
	}
}

// ---------------------------------------------------------------------------
// T268 — the reconcile face is zero-side-effect (I5): no file changes, no
// witness re-dispatch (the transport is wired to an alarm), no audit write
// (the handler never touches an audit sink — and the server under test has
// none to touch).
// ---------------------------------------------------------------------------

func TestP46T268ReconcileHasZeroSideEffects(t *testing.T) {
	f := newP46Fixture(t, nil)
	f.driveAllFamilies()
	byFamily := f.witnessByFamily()

	f.sched.anchorTransport = p46AlarmTransport{t: f.t}
	before := p46FilesSnapshot(t, f.dir, f.witnessDir, f.root)

	if w, _ := f.postReconcile(f.sched, byFamily); w.Code != http.StatusOK {
		t.Fatalf("reconcile POST: want 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	after := p46FilesSnapshot(t, f.dir, f.witnessDir, f.root)
	if len(before) != len(after) {
		t.Fatalf("I5: the reconcile must not create or remove files: before=%d after=%d", len(before), len(after))
	}
	for path, sum := range before {
		if after[path] != sum {
			t.Fatalf("I5: %s changed across a reconcile (zero side effects violated)", path)
		}
	}
}

// ---------------------------------------------------------------------------
// T269 — the frozen load-bearing premise (ADR-067 A7-7 / review MAJOR-3):
// acceptance enabled → entries anchored → phase flag OFF → the files REMAIN,
// the GET view reads not_enabled with both files present, and the POST face
// cannot and does not say deleted. Legal disabling is never a false deletion.
// ---------------------------------------------------------------------------

func TestP46T269LegalDisableNeverReadsDeleted(t *testing.T) {
	f := newP46Fixture(t, nil)
	f.driveAllFamilies()
	byFamily := f.witnessByFamily()
	f.expectFamilyCount(byFamily, witnessFamilyAcceptance, 3)

	// Rebuild the deployment over the SAME directory with the acceptance flag
	// off (and anchoring off): the files stay, the gates change.
	offCfg := f.cfg
	offCfg.AcceptanceLog = false
	offCfg.AnchorEndpoint = ""
	offCfg.DestructionLog = false
	schedOff, err := NewHistoryExportScheduler(offCfg)
	if err != nil {
		t.Fatal(err)
	}

	// The GET view: not_enabled, but both files PRESENT — and no deleted word.
	_, view := f.getView(schedOff)
	row := view.Families[witnessFamilyAcceptance]
	if !row.NotEnabled || !row.LedgerPresent || !row.AnchorPresent {
		t.Fatalf("T269: the disabled-but-present family must read not_enabled with both files present: %+v", row)
	}

	// The POST face: files present ⇒ the deleted verdict is unreachable.
	_, res := f.postReconcile(schedOff, map[string][]anchorRequest{witnessFamilyAcceptance: byFamily[witnessFamilyAcceptance]})
	got := res.Families[witnessFamilyAcceptance]
	if got.Verdict == witnessVerdictDeleted {
		t.Fatalf("T269: a legally disabled family that kept its files must NEVER read deleted")
	}
	if got.Verdict != witnessVerdictIntact {
		t.Fatalf("T269: with files intact and the witness aligned the family is intact, got %q (row=%+v)", got.Verdict, got)
	}
}

// ---------------------------------------------------------------------------
// T270 — projections BELOW the local window's lower bound ⇒ outside, no
// assertion (R40-1): a legal prefix compaction moved the lower bound past
// them. Produced the honest way — real capacity compaction, real witness.
// ---------------------------------------------------------------------------

func TestP46T270BelowLowerBoundIsOutsideNotAsserted(t *testing.T) {
	f := newP46Fixture(t, func(cfg *HistoryExportConfig) {
		cfg.AcceptanceLog = false
		cfg.VerifyAttest = false
		cfg.LedgerCapacity = 0
		cfg.AnchorCapacity = 2 // compaction drops the oldest anchor group
	})
	f.publish(p46At(1), 1, 2)
	f.publish(p46At(2), 3, 4)
	f.publish(p46At(3), 5, 6)

	// The local window moved to [2,3]; the witness still holds every dispatch.
	st, err := loadAnchorState(f.dir, mustTrust(t, f.pubPath))
	if err != nil {
		t.Fatal(err)
	}
	if st.window.MinSeq != 2 || st.window.MaxSeq != 3 {
		t.Fatalf("T270 fixture: the local window must be [2,3], got %+v", st.window)
	}
	// The anchor log's own capacity compaction is ACCOUNTED (a fourth-family
	// destruction record), so the witness legitimately holds one destruction
	// projection too. The ledger family is the publication_id > 0 subset.
	var items []anchorRequest
	for _, req := range f.rawWitnessItems() {
		if req.PublicationID > 0 {
			items = append(items, req)
		}
	}
	if len(items) != 3 {
		t.Fatalf("T270 fixture: the witness must hold all three publication dispatches, got %d", len(items))
	}

	_, res := f.postReconcile(f.sched, map[string][]anchorRequest{witnessFamilyLedger: items})
	row := res.Families[witnessFamilyLedger]
	if row.OutsideCount != 1 {
		t.Fatalf("T270: the compacted-away seq must read outside, got %d (row=%+v)", row.OutsideCount, row)
	}
	if row.Verdict != witnessVerdictIntact {
		t.Fatalf("T270: outside takes part in NO assertion — the family stays intact, got %q (row=%+v)", row.Verdict, row)
	}
	if len(row.TruncatedRanges) != 0 || len(row.DivergentSeqs) != 0 {
		t.Fatalf("T270: a legal compaction must not raise truncated/divergent: %+v", row)
	}
}

// ---------------------------------------------------------------------------
// T271 — the frozen surface pins (I9): the two load-bearing frozen files, the
// five frozen server files and go.mod/go.sum hash exactly as they did at the
// P46 base commit (3cc0131). A deliberate future change must update the pin
// consciously — that is the point.
// ---------------------------------------------------------------------------

func TestP46T271FrozenSurfaceByteIdentical(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	serverDir := filepath.Dir(thisFile)
	moduleRoot := filepath.Dir(filepath.Dir(filepath.Dir(serverDir)))
	pins := map[string]string{
		filepath.Join(serverDir, "snapshot_anchor.go"):         "eca582354359fde49c3e2b1f6e664551dbcd31ff300a310938ea2429acdebc63",
		filepath.Join(serverDir, "appendonly_log.go"):          "f4ae703323c3c9c49f5209f2b70f287dacc8ad61cb8ff84c1c1c9aa93fcb81ee",
		filepath.Join(serverDir, "snapshot_signature.go"):      "17d9ced5dff8b31be576a9473e5175083ce03e60f21b3cfbd12f78960d406d23",
		filepath.Join(serverDir, "snapshot_chain.go"):          "736c29b9b6ce114ca0efeb4068586b42c32af832dad20b699aadea8c7c9a47b2",
		filepath.Join(serverDir, "snapshot_ledger.go"):         "e9efe6fd1a7013639f0539cfb2a2940957edb9201173df005b39f2a80f50d325",
		filepath.Join(serverDir, "history_export_coverage.go"): "478f0ad9bd9f6189f04f198b90f2493c138ad1678a0c2aaed6364d89fe133999",
		filepath.Join(serverDir, "history_export_manifest.go"): "86d56214e4e69a8a33b3f4293fa72e2a3c69148e300ee1f1f8f980b335892588",
		filepath.Join(moduleRoot, "go.mod"):                    "6dfc9eea3dcba0f32b4ef8229b1a9a912dddf6db1891ed3b6623538f987e6b32",
		filepath.Join(moduleRoot, "go.sum"):                    "48a94c452c1c0b794722b90652f70a8bd6ea4a3a82f86b50b31bb0683bd9cd27",
	}
	for path, want := range pins {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("frozen file %s: %v", path, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("I9 VIOLATION: frozen file %s changed (sha256 %s, want %s) — the P46 freeze promise is broken", path, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// T272 — the mutation discriminators in ONE place: removing the deleted
// verdict (M1), the truncated upper bound (M2), the identity poison (M3) or
// the digest comparison (M4) MUST turn this test red. (The mutations
// themselves are applied out-of-band with sha256-verified restore.)
// ---------------------------------------------------------------------------

func TestP46T272MutationDiscriminators(t *testing.T) {
	// M1 target: the deleted verdict is asserted, not implied.
	f1 := newP46Fixture(t, nil)
	f1.driveAllFamilies()
	w1 := f1.witnessByFamily()
	f1.drop(acceptanceLogPath(f1.dir))
	f1.drop(acceptanceAnchorPath(f1.dir))
	_, r1 := f1.postReconcile(f1.sched, w1)
	if row := r1.Families[witnessFamilyAcceptance]; row.Verdict != witnessVerdictDeleted {
		t.Fatalf("T272/M1: the deleted verdict is load-bearing, got %q (row=%+v)", row.Verdict, row)
	}

	// M2 target: the above-upper-bound branch is load-bearing.
	f2 := newP46Fixture(t, nil)
	f2.driveAllFamilies()
	w2 := f2.witnessByFamily()
	p46DropAnchorTailGroups(t, acceptanceAnchorPath(f2.dir), 1)
	_, r2 := f2.postReconcile(f2.sched, w2)
	if row := r2.Families[witnessFamilyAcceptance]; row.Verdict != witnessVerdictTruncated {
		t.Fatalf("T272/M2: the truncated upper bound is load-bearing, got %q (row=%+v)", row.Verdict, row)
	}

	// M3 target: the identity poison is load-bearing.
	f3 := newP46Fixture(t, nil)
	f3.driveAllFamilies()
	items3 := f3.witnessByFamily()[witnessFamilyAcceptance]
	items3[0].StreamID = "0000000000000000"
	_, r3 := f3.postReconcile(f3.sched, map[string][]anchorRequest{witnessFamilyAcceptance: items3})
	if row := r3.Families[witnessFamilyAcceptance]; row.Verdict != witnessVerdictUnverifiable || row.IdentityOK {
		t.Fatalf("T272/M3: the identity poison is load-bearing, got %q identity_ok=%v", row.Verdict, row.IdentityOK)
	}

	// M4 target: the in-window digest comparison is load-bearing.
	f4 := newP46Fixture(t, nil)
	f4.driveAllFamilies()
	w4 := f4.witnessByFamily()
	p46TamperAnchorGroup(t, acceptanceAnchorPath(f4.dir), 1)
	_, r4 := f4.postReconcile(f4.sched, w4)
	if row := r4.Families[witnessFamilyAcceptance]; row.Verdict != witnessVerdictDivergent || len(row.DivergentSeqs) != 1 {
		t.Fatalf("T272/M4: the digest comparison is load-bearing, got %q divergent=%v", row.Verdict, row.DivergentSeqs)
	}
}

// ---------------------------------------------------------------------------
// T273 — GET and POST speak one vocabulary (I6 / review MAJOR-3): the POST
// deleted verdict is reachable EXACTLY when both files are absent (and the
// identity matched), and the GET face has no deleted word at all.
// ---------------------------------------------------------------------------

func TestP46T273GetPostVocabularyUnified(t *testing.T) {
	present := newP46Fixture(t, nil)
	present.driveAllFamilies()
	gone := newP46Fixture(t, nil)
	gone.driveAllFamilies()
	goneByFamily := gone.witnessByFamily() // captured while both files exist
	gone.drop(acceptanceLogPath(gone.dir))
	gone.drop(acceptanceAnchorPath(gone.dir))

	cases := []struct {
		name   string
		f      *p46Fixture
		family string
	}{
		{"files-present", present, witnessFamilyAcceptance},
		{"other-family-present", present, witnessFamilyKeyLifecycle},
		{"files-deleted", gone, witnessFamilyAcceptance},
	}
	for _, tc := range cases {
		_, view := tc.f.getView(tc.f.sched)
		row := view.Families[tc.family]
		submitted := goneByFamily
		if tc.f != gone {
			submitted = tc.f.witnessByFamily()
		}
		_, res := tc.f.postReconcile(tc.f.sched, submitted)
		post := res.Families[tc.family]
		bothAbsent := !row.LedgerPresent && !row.AnchorPresent
		deleted := post.Verdict == witnessVerdictDeleted
		if deleted != bothAbsent {
			t.Fatalf("T273/%s: vocabulary split — GET present=(%t,%t) but POST verdict=%q; deleted must hold EXACTLY when both files are absent",
				tc.name, row.LedgerPresent, row.AnchorPresent, post.Verdict)
		}
	}
	// The GET face carries NO deleted vocabulary, in either state.
	for _, tc := range cases {
		w, _ := tc.f.getView(tc.f.sched)
		if strings.Contains(w.Body.String(), "deleted") {
			t.Fatalf("T273/%s: the GET view must never speak the deleted vocabulary: %s", tc.name, w.Body.String())
		}
	}
}

// ---------------------------------------------------------------------------
// T274/T275 — the review-major discriminator pair: the shared loader
// (loadAnchorStatePath, snapshot_anchor.go) deletes a CONFLICTED seq from
// latest BEFORE computing the window, so a same-seq contradiction inside the
// anchor log shifts the window boundary and the three-segment loop misfiles
// the witness's seq:
//
//   - conflict at the window's MAX boundary (T274): the window shrinks, the
//     witness's seq falls into the above branch and reads a FALSE
//     family_ledger_truncated — while the local log still physically holds
//     the seq (twice, contradicting itself). ADR-068 §3's upper-bound
//     uniqueness proof presumes a SELF-CONSISTENT local log; a log the
//     family's own I5 discipline calls a violation falsifies it.
//   - conflict at the window's MIN boundary (T275): the witness's seq falls
//     BELOW the lower bound into the outside branch (R40-1 silence) and the
//     family reads a clean family_intact over a self-contradictory log.
//
// The window gate must fail closed: len(st.conflicts) > 0 ⇒ unverifiable,
// zero assertions. (Produced the only way the state is reachable — a byte
// edit / dual writer, per ADR-053 I5: the append face REFUSES to write a
// conflicting payload itself.)
//
// RED@07a19a2 (pre-fix): T274 got "family_ledger_truncated"
// TruncatedRanges=[{3,3}] and T275 got "family_intact" OutsideCount=1 —
// exactly the false readings the window gate's conflict blindness produces.
// ---------------------------------------------------------------------------

// p46TamperOneAnchorLine rewrites exactly ONE line of one seq group with a
// poisoned manifest_digest (same shape, different signed payload), so the
// group then holds the same anchor_seq with TWO contradictory statements.
// Differs from p46TamperAnchorGroup, which rewrites EVERY line of the group
// and therefore keeps the log conflict-free.
func p46TamperOneAnchorLine(t *testing.T, path string, seq int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	var group []anchorEntry
	tampered := false
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var e anchorEntry
		if jerr := json.Unmarshal([]byte(ln), &e); jerr != nil {
			t.Fatalf("anchor line unparseable: %v", jerr)
		}
		if e.AnchorSeq == seq && !tampered {
			e.ManifestDigest = "p46-conflicted-digest"
			fresh, merr := json.Marshal(e)
			if merr != nil {
				t.Fatal(merr)
			}
			ln = string(fresh)
			tampered = true
		}
		lines = append(lines, ln)
		if e.AnchorSeq == seq {
			group = append(group, e)
		}
	}
	if !tampered {
		t.Fatalf("fixture: anchor log %s holds no anchor_seq %d group", path, seq)
	}
	// Fixture integrity: the group must now genuinely CONTRADICT itself —
	// ≥2 lines, and the tampered statement differs from a surviving one. A
	// one-line group cannot contradict itself (the tamper would just be a
	// divergence, a different scenario entirely).
	if len(group) < 2 {
		t.Fatalf("fixture: anchor_seq %d group holds %d line(s); a one-line group cannot contradict itself", seq, len(group))
	}
	contradicts := false
	for _, e := range group[1:] {
		if !anchorPayloadEqual(group[0], e) {
			contradicts = true
		}
	}
	if !contradicts {
		t.Fatalf("fixture: anchor_seq %d group is not self-contradictory after the one-line tamper", seq)
	}
	if werr := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}
}

func TestP46T274ConflictedMaxBoundaryReadsUnverifiable(t *testing.T) {
	f := newP46Fixture(t, nil)
	f.driveAllFamilies()
	byFamily := f.witnessByFamily()
	f.expectFamilyCount(byFamily, witnessFamilyAcceptance, 3)

	// ONE contradictory line inside the TOP acceptance anchor group (seq 3):
	// the loader's conflict deletion shrinks the window to [1,2] and the
	// witness's seq 3 would misfile as a truncated tail.
	p46TamperOneAnchorLine(t, acceptanceAnchorPath(f.dir), 3)

	_, res := f.postReconcile(f.sched, byFamily)
	row := res.Families[witnessFamilyAcceptance]
	if row.Verdict != witnessVerdictUnverifiable {
		t.Fatalf("T274: a conflicted max-boundary seq must fail closed (family_unverifiable), got %q (row=%+v)", row.Verdict, row)
	}
	if !strings.Contains(row.Error, "contradictory") {
		t.Fatalf("T274: the refusal must name the local contradiction loudly, got error=%q", row.Error)
	}
	if len(row.TruncatedRanges) != 0 || len(row.DivergentSeqs) != 0 || row.OutsideCount != 0 || row.MissingCount != 0 {
		t.Fatalf("T274: an unverifiable family must carry ZERO assertions (no false truncated range): %+v", row)
	}
}

func TestP46T275ConflictedMinBoundaryReadsUnverifiable(t *testing.T) {
	f := newP46Fixture(t, nil)
	f.driveAllFamilies()
	byFamily := f.witnessByFamily()
	f.expectFamilyCount(byFamily, witnessFamilyAcceptance, 3)

	// ONE contradictory line inside the OLDEST acceptance anchor group
	// (seq 1): the window becomes [2,3] and the witness's seq 1 falls below
	// the lower bound — the silent outside branch — over a log the family's
	// own I5 discipline calls a violation.
	p46TamperOneAnchorLine(t, acceptanceAnchorPath(f.dir), 1)

	_, res := f.postReconcile(f.sched, byFamily)
	row := res.Families[witnessFamilyAcceptance]
	if row.Verdict != witnessVerdictUnverifiable {
		t.Fatalf("T275: a conflicted min-boundary seq must fail closed (family_unverifiable), got %q (row=%+v)", row.Verdict, row)
	}
	if !strings.Contains(row.Error, "contradictory") {
		t.Fatalf("T275: the refusal must name the local contradiction loudly, got error=%q", row.Error)
	}
	if len(row.TruncatedRanges) != 0 || len(row.DivergentSeqs) != 0 || row.OutsideCount != 0 || row.MissingCount != 0 {
		t.Fatalf("T275: an unverifiable family must carry ZERO assertions (no silent outside): %+v", row)
	}
}
