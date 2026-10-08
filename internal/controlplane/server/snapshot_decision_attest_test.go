package server

// Phase 48 — Runtime Decision Attestation tests (T297~T316; ADR-071 §5,
// ADR-072 §7).
//
// The fixture drives the REAL scheduler and the REAL Gate-facing sink. Every
// record under test is produced by production code: the tests emit decisions
// through the sink (exactly as the Gate does), run a REAL tick, and then read
// the REAL faces. Tampering and truncation are done by rewriting bytes on disk,
// which is the only way those states are reachable.
//
// The core discriminator of this Phase is a SPLIT, and every test here keeps it
// visible: P46 reconciles the ANCHOR stream and is blind to the main ledger's
// content, while the new local recompute walks the hash chain itself. T299/T300
// assert BOTH readings in one case — local divergent/truncated AND P46
// family_intact — precisely so that "this is a new mechanism" is a measured
// claim, not a narrative one.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/controlplane/auth"
	"github.com/YuDong999/opscore/internal/protection"
	"github.com/YuDong999/opscore/internal/storage"
)

var p48T0 = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type p48Fixture struct {
	t          *testing.T
	root       string
	dir        string
	witnessDir string
	ring       *protection.RecordingProvenanceSink
	sink       *DecisionAttestSink
	sched      *HistoryExportScheduler
	cfg        HistoryExportConfig
}

// newP48Fixture builds a scheduler with anchoring ON and the sixth family's sink
// wired. `tune` runs before construction, so a test can change any field
// (including dropping the sink to exercise the disabled family).
func newP48Fixture(t *testing.T, tune func(cfg *HistoryExportConfig, f *p48Fixture)) *p48Fixture {
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
	privPath, pubPath, _, _ := genKeyPair(t, keyDir, "signer")
	st := &fakeExportStore{}
	st.res = protection.TransitionReadResult{
		Transitions: sampleTransitions(), ExportedAt: p48T0, MinSeq: 1, MaxSeq: 3,
	}
	f := &p48Fixture{t: t, root: root, dir: dir, witnessDir: witnessDir}
	f.ring = protection.NewRecordingProvenanceSink(64)
	f.sink = NewDecisionAttestSink(f.ring, dir)
	cfg := HistoryExportConfig{
		Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"},
		Clock:          func() time.Time { return p48T0 },
		SignKeyPath:    privPath,
		TrustKeyPaths:  []string{pubPath},
		AnchorEndpoint: "file://" + witnessDir,
		DecisionAttest: f.sink,
	}
	if tune != nil {
		tune(&cfg, f)
	}
	sched, err := NewHistoryExportScheduler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.sched = sched
	f.cfg = cfg
	return f
}

// rebuild constructs a FRESH scheduler + FRESH sink + FRESH ring over the same
// export directory — the restart shape (ADR-071 A5: what survives is what is on
// disk, not what is in memory).
func (f *p48Fixture) rebuild() *HistoryExportScheduler {
	f.t.Helper()
	cfg := f.cfg
	f.ring = protection.NewRecordingProvenanceSink(64)
	f.sink = NewDecisionAttestSink(f.ring, f.dir)
	cfg.DecisionAttest = f.sink
	s, err := NewHistoryExportScheduler(cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	f.sched = s
	f.cfg = cfg
	return s
}

func (f *p48Fixture) tick() {
	f.t.Helper()
	f.sched.Tick(context.Background())
}

// p48Prov builds one decision record. Every field is distinct per index so the
// digests are distinct and a reordering is detectable.
func p48Prov(i int) protection.DecisionProvenance {
	return protection.DecisionProvenance{
		TraceID:       fmt.Sprintf("trace-%04d", i),
		CapabilityID:  "cap.exec",
		PrincipalHash: strings.Repeat("ab", 32),
		Guard:         "admit",
		Decision:      "admit",
		Action:        "protection.admit",
		Threshold:     "",
		Observed:      "0",
		Detail:        fmt.Sprintf("admitted-%04d", i),
		LatencyMicros: int64(i),
		At:            p48T0.Add(time.Duration(i) * time.Microsecond),
	}
}

// emit queues n decisions through the sink — the exact call the Gate makes.
// shrinkDecisionCaps makes the two retention caps small for ONE test. What T303,
// T304, T305 and T315 pin is the OVERFLOW / PREFIX-COMPACTION DISCIPLINE, and the
// caps' SIZE is incidental to it — but every drained record costs an fsync'd
// append, so with the production caps each of those tests spent ~40-60s in disk
// latency on this host. Every assertion that names a cap names the VARIABLE, so
// shrinking it changes no expectation; the values are restored when the test ends.
func shrinkDecisionCaps(t *testing.T) {
	t.Helper()
	q, l := decisionQueueCap, decisionLogCapacity
	// The QUEUE must stay larger than the LOG (that is the production
	// relationship, and T315's head-seq expectation rests on it: a record dropped
	// by the queue never reaches the log at all). 64/16 preserves the relation
	// while cutting the drained records — and therefore the fsync'd appends — by
	// more than an order of magnitude.
	decisionQueueCap, decisionLogCapacity = 64, 16
	t.Cleanup(func() { decisionQueueCap, decisionLogCapacity = q, l })
}

func (f *p48Fixture) emit(n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		f.sink.Emit(context.Background(), p48Prov(i))
	}
}

func (f *p48Fixture) row() anchorDeliveryStreamStatus {
	f.t.Helper()
	return p47Row(f.t, f.sched, witnessFamilyProtectionDecision)
}

func (f *p48Fixture) state() string { return f.row().DecisionLogState }

func (f *p48Fixture) attested() bool {
	f.t.Helper()
	r := f.row()
	if r.DecisionAttested == nil {
		f.t.Fatalf("the sixth family's row must always carry decision_attested, got %+v", r)
	}
	return *r.DecisionAttested
}

func p48Read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func p48Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// p48WitnessItems rebuilds the witness-held projection of the sixth family from
// the LOCAL anchor log — the honest bytes the witness actually received, which is
// exactly what a caller feeds to the P46 reconcile face.
func p48WitnessItems(t *testing.T, f *p48Fixture) []anchorRequest {
	t.Helper()
	st, err := loadAnchorStatePath(decisionAnchorPath(f.dir), f.dir, nil)
	if err != nil {
		t.Fatalf("load sixth-family anchor state: %v", err)
	}
	var out []anchorRequest
	for _, seq := range st.seqs() {
		e := st.latest[seq]
		dg, derr := anchorDigestOf(&e)
		if derr != nil {
			t.Fatal(derr)
		}
		out = append(out, anchorRequest{
			V: 1, KeyID: e.KeyID, StreamID: e.StreamID,
			PublicationID: e.PublicationID, AnchorSeq: e.AnchorSeq,
			AnchorDigest: dg, ManifestDigest: e.ManifestDigest,
			RecordedAt: e.RecordedAt, Sig: e.Sig,
		})
	}
	return out
}

// p48P46Verdict runs the P46 reconcile face for the sixth family over the given
// projections and returns its verdict.
func p48P46Verdict(t *testing.T, f *p48Fixture, items []anchorRequest) witnessFamilyResult {
	t.Helper()
	res, err := f.sched.ReconcileWitnessFamilies(map[string][]anchorRequest{
		witnessFamilyProtectionDecision: items,
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	row, ok := res.Families[witnessFamilyProtectionDecision]
	if !ok {
		t.Fatalf("the aggregate must carry the sixth family, got %v", res.Families)
	}
	return row
}

// p48RewriteLine applies mutate to one parsed JSON line and writes it back
// WITHOUT touching the digest — the in-place field edit an adversary would make.
func p48RewriteLine(t *testing.T, path string, idx int, mutate func(map[string]any)) {
	t.Helper()
	lines := p47RawLines(t, path)
	if idx < 0 || idx >= len(lines) {
		t.Fatalf("line %d out of range (%d lines)", idx, len(lines))
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(lines[idx]), &obj); err != nil {
		t.Fatal(err)
	}
	mutate(obj)
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	lines[idx] = string(raw)
	var buf bytes.Buffer
	for _, l := range lines {
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	if werr := os.WriteFile(path, buf.Bytes(), 0o600); werr != nil {
		t.Fatal(werr)
	}
}

// p48TruncateTail removes the last k lines of a file.
func p48TruncateTail(t *testing.T, path string, k int) {
	t.Helper()
	lines := p47RawLines(t, path)
	if k <= 0 || k > len(lines) {
		t.Fatalf("truncate %d of %d lines", k, len(lines))
	}
	kept := lines[:len(lines)-k]
	var buf bytes.Buffer
	for _, l := range kept {
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// p48DirSnapshot captures the file set (names + sizes) of the export directory,
// for the zero-side-effect assertions.
func p48DirSnapshot(t *testing.T, dir string) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, e := range ents {
		info, ierr := e.Info()
		if ierr != nil {
			t.Fatal(ierr)
		}
		parts = append(parts, fmt.Sprintf("%s:%d", e.Name(), info.Size()))
	}
	return strings.Join(parts, "|")
}

// ---------------------------------------------------------------------------
// T297 — the five pre-existing families' carrier bytes are unchanged (I1/I8)
// ---------------------------------------------------------------------------

func TestP48T297ExistingFamiliesCarrierUnchanged(t *testing.T) {
	f := newP46Fixture(t, func(cfg *HistoryExportConfig) {
		cfg.LedgerCapacity = 2
		cfg.DecisionAttest = NewDecisionAttestSink(protection.NewRecordingProvenanceSink(64), cfg.Dir)
	})
	f.driveAllFamilies()

	// The four NON-publication families historically never set manifest_digest,
	// and the sixth family's reuse of that slot must not have changed them: their
	// lines still carry manifest_digest == "" and their own kind.
	for _, tc := range []struct {
		name string
		path func(string) string
		kind string
	}{
		{witnessFamilyKeyLifecycle, keyLifecycleAnchorPath, anchorKindKeyLifecycle},
		{witnessFamilyVerification, verificationAnchorPath, anchorKindVerification},
		{witnessFamilyDestruction, destructionAnchorPath, anchorKindDestruction},
		{witnessFamilyAcceptance, acceptanceAnchorPath, anchorKindAcceptance},
	} {
		lines := p47RawLines(t, tc.path(f.dir))
		if len(lines) == 0 {
			t.Fatalf("T297: family %q produced no anchor entry — the fixture is vacuous", tc.name)
		}
		for _, ln := range lines {
			var e anchorEntry
			if err := json.Unmarshal([]byte(ln), &e); err != nil {
				t.Fatal(err)
			}
			if e.Kind != tc.kind {
				t.Fatalf("T297: family %q kind = %q, want %q", tc.name, e.Kind, tc.kind)
			}
			if e.ManifestDigest != "" {
				t.Fatalf("T297: family %q now sets manifest_digest=%q — the Phase 48 slot reuse must not have changed an existing family", tc.name, e.ManifestDigest)
			}
		}
	}

	// The publication family still carries its manifest digest and still has NO
	// `kind` key at all (the Phase 40 bytes are frozen).
	pubLines := p47RawLines(t, anchorLogPath(f.dir))
	if len(pubLines) == 0 {
		t.Fatal("T297: the publication family produced no anchor entry")
	}
	for _, ln := range pubLines {
		if strings.Contains(ln, `"kind"`) {
			t.Fatalf("T297: a publication anchor line grew a `kind` key: %s", ln)
		}
		var e anchorEntry
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			t.Fatal(err)
		}
		if e.ManifestDigest == "" {
			t.Fatalf("T297: a publication anchor line lost its manifest_digest: %s", ln)
		}
	}
}

// ---------------------------------------------------------------------------
// T298 — the P24.2 read faces are byte-frozen and ProvenanceStore is the ring (I5)
// ---------------------------------------------------------------------------

// p48ObsServer builds a server whose gate emits into the given sink.
func p48ObsServer(t *testing.T, prov protection.ProvenanceSink) (*Server, string) {
	t.Helper()
	mem := storage.NewMemoryStorage()
	role, err := mem.Roles().Save(storage.Role{Name: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	authSvc := auth.NewAuthService(mem, "test-access-secret", "test-refresh-secret")
	admin, err := authSvc.Register("admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Users().AddRole(admin.ID, role.ID); err != nil {
		t.Fatal(err)
	}
	access, _, _, err := authSvc.Login("admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	ks := protection.NewKillStore(fakeKillPersistence{}, time.Now)
	if err := ks.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	srv := &Server{stor: mem, auth: authSvc, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// A FIXED salt and clock, so the two runs below emit byte-comparable records
	// (the salt is otherwise random per Gate, and every principal hash would then
	// differ for a reason that has nothing to do with this Phase).
	srv.gate = protection.New(protection.Config{
		KillStore:  ks,
		Audit:      fakeAuditWriter{},
		Salt:       []byte("phase48-fixed-salt"),
		Clock:      func() time.Time { return p48T0 },
		Provenance: prov,
	})
	return srv, access
}

func TestP48T298P242ReadFacesByteFrozen(t *testing.T) {
	run := func(prov protection.ProvenanceSink) (string, string, string) {
		srv, token := p48ObsServer(t, prov)
		for i := 0; i < 5; i++ {
			srv.gate.Check(context.Background(), "cap.exec", fmt.Sprintf("principal-%d", i))
		}
		h := srv.ProtectionReadMux()
		decisions := doReq(h, http.MethodGet, "/management/v1/protection/decisions", token)
		exportJSON := doReq(h, http.MethodGet, "/management/v1/protection/decisions/export?format=json", token)
		exportCSV := doReq(h, http.MethodGet, "/management/v1/protection/decisions/export?format=csv", token)
		if decisions.Code != http.StatusOK || exportJSON.Code != http.StatusOK || exportCSV.Code != http.StatusOK {
			t.Fatalf("P24.2 faces must answer 200: %d/%d/%d", decisions.Code, exportJSON.Code, exportCSV.Code)
		}
		return decisions.Body.String(), exportJSON.Body.String(), exportCSV.Body.String()
	}

	// (a) the bare ring — the pre-Phase-48 wiring.
	bareRing := protection.NewRecordingProvenanceSink(64)
	d1, j1, c1 := run(bareRing)

	// (b) the delegating sink.
	ring := protection.NewRecordingProvenanceSink(64)
	sink := NewDecisionAttestSink(ring, t.TempDir())
	d2, j2, c2 := run(sink)

	// Final-review fix: the two captures are minutes apart in the full suite,
	// and the export faces carry the STORE's wall clock (exported_at,
	// P34-CLOCK-1 — real time by design, this fixture has no injected clock)
	// plus engine-measured latency_micros (0 idle, nonzero under load). Both
	// are generated by the ENGINE/store, not by the attest sink — normalizing
	// them keeps the test's actual claim (the sink does not alter the faces)
	// while removing the straddle-a-second flake. The decisions' own `at`
	// fields are fixture-frozen and stay compared byte-for-byte.
	// F2 (final review round 2): normalize ONLY the store-generated wall-clock
	// values. A blanket RFC3339 sweep also normalized the decisions' own `at`
	// fields, which masked a sink that tampers with record content before
	// delegation (probe-verified). The store clock appears in TWO forms:
	//   JSON: "exported_at":"<ts>"     CSV: a "# exported_at=<ts>" comment line
	// Both are live (the store's wall clock, P34-CLOCK-1) and both must be
	// normalized or the CSV face straddles a second under load (observed). The
	// remaining `latency_micros` normalization is defensive: elapsed time is 0
	// for an instant gate call but can tick under preemption, and the field is
	// engine-generated, never sink-altered.
	expJSONRe := regexp.MustCompile(`"exported_at":"[^"]*"`)
	expCSVRe := regexp.MustCompile(`(?m)^# exported_at=.*$`)
	latRe := regexp.MustCompile(`"latency_micros":\d+`)
	freeze := func(s string) string {
		s = expJSONRe.ReplaceAllString(s, `"exported_at":"TS"`)
		s = expCSVRe.ReplaceAllString(s, "# exported_at=TS")
		s = latRe.ReplaceAllString(s, `"latency_micros":"LAT"`)
		return s
	}
	d1, d2, j1, j2, c1, c2 = freeze(d1), freeze(d2), freeze(j1), freeze(j2), freeze(c1), freeze(c2)

	if d1 != d2 {
		t.Fatalf("T298/I5: /decisions is not byte-identical:\n bare=%q\n sink=%q", d1, d2)
	}
	if j1 != j2 {
		t.Fatalf("T298/I5: /decisions/export (json) is not byte-identical:\n bare=%q\n sink=%q", j1, j2)
	}
	if c1 != c2 {
		t.Fatalf("T298/I5: /decisions/export (csv) is not byte-identical:\n bare=%q\n sink=%q", c1, c2)
	}

	// The store the Gate exposes satisfies ProvenanceStore, and every read role
	// is served by the RING: a caller cannot tell the two apart. A FRESH sink is
	// used here so the counts below are unambiguous.
	freshRing := protection.NewRecordingProvenanceSink(64)
	freshSink := NewDecisionAttestSink(freshRing, t.TempDir())
	srv, _ := p48ObsServer(t, freshSink)
	store := srv.gate.ProvenanceStore()
	if store == nil {
		t.Fatal("T298: the delegating sink must still satisfy ProvenanceStore")
	}
	if got, want := len(store.Recent(10)), 0; got != want {
		t.Fatalf("T298: Recent must read the ring (empty here), got %d", got)
	}
	freshSink.Emit(context.Background(), p48Prov(1))
	if got := len(store.Recent(10)); got != 1 {
		t.Fatalf("T298: Recent must observe the ring the sink wrote to, got %d", got)
	}
	if got := len(store.ByCapability("cap.exec")); got != 1 {
		t.Fatalf("T298: ByCapability must be the ring's, got %d", got)
	}
	if got := len(store.ByTraceID("trace-0001")); got != 1 {
		t.Fatalf("T298: ByTraceID must be the ring's, got %d", got)
	}
	if st := store.Stats(); st.Capacity != freshRing.Stats().Capacity || st.Buffered != 1 {
		t.Fatalf("T298: Stats must be the ring's, got %+v want capacity=%d buffered=1", st, freshRing.Stats().Capacity)
	}
}

// ---------------------------------------------------------------------------
// T299 — CORE RED CASE (tamper): local recompute diverges, P46 stays intact (I7)
// ---------------------------------------------------------------------------

// RED (MU1: the local recompute removed, sha256 restored): without the head
// comparison the sixth family's row reads `attested` over a rewritten record, so
// this test fails on its first assertion — while the P46 assertion still passes.
// That is exactly the discrimination the test exists to pin, and exactly the
// blindness P46 has (ADR-071 §2 M1).
func TestP48T299TamperedDecisionLogIsDivergent(t *testing.T) {
	f := newP48Fixture(t, nil)
	f.emit(4)
	f.tick()

	if got := f.state(); got != decisionLogStateAttested {
		t.Fatalf("T299 precondition: honest chain must be attested, got %q", got)
	}
	if !f.attested() {
		t.Fatal("T299 precondition: an honest, anchored, loss-free chain must be attested")
	}
	witnessItems := p48WitnessItems(t, f)

	// The tamper: rewrite ONE recorded field, leaving the stored digest alone.
	p48RewriteLine(t, decisionLogPath(f.dir), 1, func(o map[string]any) {
		o["detail"] = "rewritten-after-the-fact"
	})

	if got := f.state(); got != decisionLogStateDivergent {
		t.Fatalf("T299: a rewritten record must read divergent, got %q", got)
	}
	if f.attested() {
		t.Fatal("T299: a divergent chain must never be attested")
	}

	// The SPLIT: P46 reconciles the ANCHOR stream only (its LedgerPath is an
	// existence probe and :244-282 compares local ANCHOR digests), so the very
	// same tamper is invisible there. Asserting both readings in one case is what
	// makes "this is a new mechanism, not a P46 reuse" a measurement.
	if row := p48P46Verdict(t, f, witnessItems); row.Verdict != witnessVerdictIntact {
		t.Fatalf("T299: P46 must be SILENT about a rewritten main ledger (family_intact), got %q (err=%q)", row.Verdict, row.Error)
	}
}

// ---------------------------------------------------------------------------
// T300 — CORE RED CASE (tail deletion): local recompute truncates, P46 intact (I7)
// ---------------------------------------------------------------------------

func TestP48T300TruncatedDecisionLogIsTruncated(t *testing.T) {
	f := newP48Fixture(t, nil)
	f.emit(4)
	f.tick()
	if got := f.state(); got != decisionLogStateAttested {
		t.Fatalf("T300 precondition: honest chain must be attested, got %q", got)
	}
	witnessItems := p48WitnessItems(t, f)

	p48TruncateTail(t, decisionLogPath(f.dir), 2)

	if got := f.state(); got != decisionLogStateTruncated {
		t.Fatalf("T300: a removed tail must read truncated, got %q", got)
	}
	if f.attested() {
		t.Fatal("T300: a truncated chain must never be attested")
	}
	if row := p48P46Verdict(t, f, witnessItems); row.Verdict != witnessVerdictIntact {
		t.Fatalf("T300: P46 must be SILENT about a truncated main ledger (family_intact), got %q (err=%q)", row.Verdict, row.Error)
	}
}

// ---------------------------------------------------------------------------
// T301 — the P46 reuse half: deleting BOTH files is family_ledger_deleted (§3)
// ---------------------------------------------------------------------------

func TestP48T301BothFilesDeletedIsP46sHalf(t *testing.T) {
	f := newP48Fixture(t, nil)
	f.emit(3)
	f.tick()
	witnessItems := p48WitnessItems(t, f)
	if len(witnessItems) == 0 {
		t.Fatal("T301: the fixture must have produced at least one anchor entry")
	}
	if row := p48P46Verdict(t, f, witnessItems); row.Verdict != witnessVerdictIntact {
		t.Fatalf("T301 precondition: want family_intact, got %q (err=%q)", row.Verdict, row.Error)
	}

	if err := os.Remove(decisionLogPath(f.dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(decisionAnchorPath(f.dir)); err != nil {
		t.Fatal(err)
	}

	// This is the half P46 DOES cover (ADR-071 §3): both files gone, witness holds
	// identity-matched entries ⇒ deleted. It is not the new mechanism.
	if row := p48P46Verdict(t, f, witnessItems); row.Verdict != witnessVerdictDeleted {
		t.Fatalf("T301: deleting both files must read family_ledger_deleted, got %q", row.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T302 — decision_attested is reachable AND non-vacuous, and the carrier is
// kind="protection_decision" / manifest_digest=<chain head> (§4, I8)
// ---------------------------------------------------------------------------

func TestP48T302AttestedIsReachableAndCarrierIsRight(t *testing.T) {
	f := newP48Fixture(t, nil)
	f.emit(3)
	f.tick()

	r := f.row()
	if r.DecisionLogState != decisionLogStateAttested {
		t.Fatalf("T302: want attested, got %q (%+v)", r.DecisionLogState, r)
	}
	if r.DecisionQueueDropped == nil || *r.DecisionQueueDropped != 0 {
		t.Fatalf("T302: want decision_queue_dropped == 0, got %+v", r.DecisionQueueDropped)
	}
	if !f.attested() {
		t.Fatal("T302: the four-way conjunction must hold in the honest state")
	}
	if !r.Converged || r.Pending != 0 {
		t.Fatalf("T302: the delivery face must also be at rest, got %+v", r)
	}

	// The carrier: the newest anchor entry carries the chain head in the two
	// reused slots, and identityID() falls back to PublicationID (no 0 fallback)
	// exactly as the verification family already does.
	headSeq, headDigest, ok := walkDecisionChain(decisionLogPath(f.dir))
	if !ok || headSeq != 3 {
		t.Fatalf("T302: want a sound 3-record chain, got seq=%d ok=%t", headSeq, ok)
	}
	st, err := loadAnchorStatePath(decisionAnchorPath(f.dir), f.dir, f.sched.trust)
	if err != nil {
		t.Fatal(err)
	}
	e, found := st.latest[st.window.MaxSeq]
	if !found {
		t.Fatal("T302: the sixth family has no anchor entry")
	}
	if e.Kind != anchorKindProtectionDecision {
		t.Fatalf("T302: anchor kind = %q, want %q", e.Kind, anchorKindProtectionDecision)
	}
	if e.ManifestDigest != headDigest {
		t.Fatalf("T302: manifest_digest = %q, want the chain head digest %q", e.ManifestDigest, headDigest)
	}
	if e.PublicationID != headSeq {
		t.Fatalf("T302: publication_id = %d, want the chain head seq %d", e.PublicationID, headSeq)
	}
	if e.identityID() != headSeq || e.identityID() == 0 {
		t.Fatalf("T302: identityID() = %d, want the chain head seq (never the 0 fallback)", e.identityID())
	}
	if _, ok := st.byID[headSeq]; !ok {
		t.Fatalf("T302: byID must be keyed by the chain head seq, keys=%v", st.byID)
	}
	if !anchorDigestCoversManifestDigest(t, &e) {
		t.Fatal("T302: manifest_digest must be INSIDE the signature coverage (anchorSignedFields copies it)")
	}

	// NON-VACUITY (review M2): the criterion must be able to be false. Deleting
	// the log (with the anchor entry still present) drops it out of attested.
	if err := os.Remove(decisionLogPath(f.dir)); err != nil {
		t.Fatal(err)
	}
	if f.state() != decisionLogStateTruncated || f.attested() {
		t.Fatalf("T302: an emptied log must leave attested, got state=%q attested=%t", f.state(), f.attested())
	}
}

// anchorDigestCoversManifestDigest pins that ManifestDigest participates in the
// signed payload: mutating it must change anchorDigestOf.
func anchorDigestCoversManifestDigest(t *testing.T, e *anchorEntry) bool {
	t.Helper()
	base, err := anchorDigestOf(e)
	if err != nil {
		t.Fatal(err)
	}
	mutated := *e
	mutated.ManifestDigest = "0" + strings.TrimPrefix(e.ManifestDigest, "0")
	if mutated.ManifestDigest == e.ManifestDigest {
		mutated.ManifestDigest = "different"
	}
	other, err := anchorDigestOf(&mutated)
	if err != nil {
		t.Fatal(err)
	}
	return base != other
}

// ---------------------------------------------------------------------------
// T303 — honest loss SUPPRESSES attested (I4)
// ---------------------------------------------------------------------------

// RED (MU3: the loss conjunct removed, sha256 restored): with `dropped == 0`
// taken out of the conjunction an overflowed window reads `attested`, so this
// test fails — i.e. "we lost a decision" would read exactly like "nothing
// happened".
func TestP48T303QueueOverflowSuppressesAttested(t *testing.T) {
	shrinkDecisionCaps(t)
	f := newP48Fixture(t, nil)
	f.emit(decisionQueueCap + 5)
	if got := f.sink.Dropped(); got != 5 {
		t.Fatalf("T303 precondition: want 5 evicted, got %d", got)
	}
	f.tick()

	r := f.row()
	if r.DecisionQueueDropped == nil || *r.DecisionQueueDropped != 5 {
		t.Fatalf("T303: the loss must be surfaced, got %+v", r.DecisionQueueDropped)
	}
	if r.DecisionLogState != decisionLogStateAttested {
		t.Fatalf("T303: the chain itself is sound, got state %q", r.DecisionLogState)
	}
	if f.attested() {
		t.Fatal("T303/I4: a window that lost a decision must NEVER be attested")
	}
}

// ---------------------------------------------------------------------------
// T304 — the loss count is cross-process (I3/A5)
// ---------------------------------------------------------------------------

// RED (MU2: loss persistence removed, sha256 restored): nothing is written
// through to disk, so the restarted sink reports 0 and this test fails — the
// loss would be laundered into "no loss" by a restart.
func TestP48T304LossSurvivesRestart(t *testing.T) {
	shrinkDecisionCaps(t)
	f := newP48Fixture(t, nil)
	f.emit(decisionQueueCap + 3)
	f.tick()
	if got := f.row().DecisionQueueDropped; got == nil || *got != 3 {
		t.Fatalf("T304 precondition: want 3 lost, got %v", got)
	}

	// Restart: a fresh scheduler, a fresh ring and a fresh sink over the SAME dir.
	f.rebuild()
	if got := f.sink.Dropped(); got != 3 {
		t.Fatalf("T304: the restarted sink must continue the count, got %d", got)
	}
	if got := f.row().DecisionQueueDropped; got == nil || *got != 3 {
		t.Fatalf("T304: the restarted read face must report the persisted loss, got %v", got)
	}
	if f.attested() {
		t.Fatal("T304: a persisted loss must still suppress attested after a restart")
	}
}

// ---------------------------------------------------------------------------
// T305 — R24-4: a saturated sink never blocks or changes a decision (I2)
// ---------------------------------------------------------------------------

func TestP48T305SaturatedSinkNeverBlocksDecisions(t *testing.T) {
	shrinkDecisionCaps(t)
	dir := t.TempDir()
	ring := protection.NewRecordingProvenanceSink(8)
	sink := NewDecisionAttestSink(ring, dir)
	// Saturate far past the cap: every further Emit takes the eviction path.
	for i := 0; i < decisionQueueCap+500; i++ {
		sink.Emit(context.Background(), p48Prov(i))
	}
	if got := sink.Dropped(); got != 500 {
		t.Fatalf("T305 precondition: want 500 evicted, got %d", got)
	}

	ks := protection.NewKillStore(fakeKillPersistence{}, time.Now)
	if err := ks.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	audit := &recordingAudit{}
	gate := protection.New(protection.Config{KillStore: ks, Audit: audit, Provenance: sink})
	if err := ks.SetKilled("cap.exec", true); err != nil {
		t.Fatal(err)
	}

	// The decision is the same one the un-sunk Gate makes: 403 / kill.
	if _, rej := gate.Check(context.Background(), "cap.exec", "alice"); rej == nil || rej.HTTPStatus != 403 {
		t.Fatalf("T305: a saturated sink must not change the decision, got %+v", rej)
	}
	if len(audit.events) != 1 || audit.events[0].Action != protection.ActionKilled {
		t.Fatalf("T305: the audit row must be unchanged, got %+v", audit.events)
	}

	// Bounded, not merely "eventually": a blocking/backpressuring sink would hang
	// here and trip the package timeout; this bound makes the property explicit.
	start := time.Now()
	for i := 0; i < 5000; i++ {
		gate.Check(context.Background(), "cap.exec", "alice")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("T305/I2: 5000 decisions against a saturated sink took %s — the sink is not non-blocking", elapsed)
	}
}

// ---------------------------------------------------------------------------
// T306 — the decision vocabulary is untouched (I1/A2)
// ---------------------------------------------------------------------------

func TestP48T306DecisionSemanticsUnchanged(t *testing.T) {
	// internal/protection is zero-diff in this Phase (proved by blob hash in the
	// frozen-face self-check); this test pins the observable half: the guard
	// vocabulary and the status codes a sink-equipped Gate produces.
	ring := protection.NewRecordingProvenanceSink(64)
	sink := NewDecisionAttestSink(ring, t.TempDir())
	ks := protection.NewKillStore(fakeKillPersistence{}, time.Now)
	if err := ks.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	gate := protection.New(protection.Config{
		KillStore:  ks,
		Audit:      fakeAuditWriter{},
		Buckets:    protection.NewTokenBucketSet(protection.TokenBucketConfig{Capacity: 1, Refill: 0}, func() time.Time { return p48T0 }),
		Provenance: sink,
	})
	ctx := context.Background()

	// 1. admit (the bucket's single token).
	if _, rej := gate.Check(ctx, "cap.a", "alice"); rej != nil {
		t.Fatalf("T306: first check must admit, got %+v", rej)
	}
	// 2. rate (the bucket is empty).
	if _, rej := gate.Check(ctx, "cap.a", "alice"); rej == nil || rej.HTTPStatus != 429 || rej.Action != protection.ActionRateLimited {
		t.Fatalf("T306: an exhausted bucket must be 429/rate_limited, got %+v", rej)
	}
	// 3. kill (403), which short-circuits everything above it.
	if err := ks.SetKilled("cap.b", true); err != nil {
		t.Fatal(err)
	}
	if _, rej := gate.Check(ctx, "cap.b", "alice"); rej == nil || rej.HTTPStatus != 403 || rej.Action != protection.ActionKilled {
		t.Fatalf("T306: a killed capability must be 403/killed, got %+v", rej)
	}

	// The recorded vocabulary is exactly the Gate's.
	guards := map[string]bool{}
	for _, p := range ring.Recent(10) {
		guards[p.Guard+"/"+p.Decision] = true
	}
	for _, want := range []string{"admit/admit", "rate/reject", "kill/reject"} {
		if !guards[want] {
			t.Fatalf("T306: the sink must record the Gate's own vocabulary, missing %q in %v", want, guards)
		}
	}
}

// ---------------------------------------------------------------------------
// T307 — the delivery face is reused, not rebuilt (P47)
// ---------------------------------------------------------------------------

func TestP48T307DeliveryFaceReused(t *testing.T) {
	f := newP48Fixture(t, nil)
	f.emit(2)
	f.tick()
	r := f.row()
	if r.Family != witnessFamilyProtectionDecision {
		t.Fatalf("T307: family = %q", r.Family)
	}
	if r.SweptBy != anchorSweeperDeliverySweep {
		t.Fatalf("T307: swept_by = %q, want %q", r.SweptBy, anchorSweeperDeliverySweep)
	}
	if r.Anchored != 1 || r.Pending != 0 || r.Unanchored != 0 || !r.Converged {
		t.Fatalf("T307: the delivery face must report the anchored entry at rest, got %+v", r)
	}
	if !r.Present {
		t.Fatal("T307: the sixth family's anchor stream must be present")
	}
}

// ---------------------------------------------------------------------------
// T308 — the partition and the enable case (I10, D6/D7)
// ---------------------------------------------------------------------------

// RED (MU5: the sixth stream removed from the partition, sha256 restored): the
// partition holds five rows and this test fails on its first assertion. The
// enable case is guarded by the same test: with the `witnessFamilyEnabled` case
// gone the 5-case switch's `default false` reports `not_enabled` while the
// producer runs (D7).
func TestP48T308PartitionAndEnableGate(t *testing.T) {
	// (a) the partition: six streams, exactly one sweeper each, the sixth is the
	// FOURTH delivery_sweep stream with its own mutex.
	streams := anchorDeliveryStreams()
	if len(streams) != 6 {
		t.Fatalf("T308: want six streams, got %d", len(streams))
	}
	var mine *anchorDeliveryStream
	for i := range streams {
		if streams[i].Family == witnessFamilyProtectionDecision {
			mine = &streams[i]
		}
	}
	if mine == nil {
		t.Fatal("T308: the sixth family is not registered")
	}
	if mine.SweptBy != anchorSweeperDeliverySweep {
		t.Fatalf("T308: sixth family swept_by = %q", mine.SweptBy)
	}
	if mine.DispatchMu == nil {
		t.Fatal("T308/I10: the sixth stream needs its own dispatch mutex (two dispatchers)")
	}
	if mine.Observer == nil || mine.Observer(nil) != nil {
		t.Fatal("T308: the sixth ANCHOR stream's compaction is deliberately unobserved")
	}
	swept := deliverySweepStreams()
	if len(swept) != 4 {
		t.Fatalf("T308: delivery_sweep must own four streams, got %d", len(swept))
	}
	seen := map[*sync.Mutex]bool{}
	for _, st := range swept {
		if st.DispatchMu == nil {
			t.Fatalf("T308: swept stream %q has no mutex", st.Family)
		}
		if seen[st.DispatchMu] {
			t.Fatalf("T308: two swept streams share a mutex (%q)", st.Family)
		}
		seen[st.DispatchMu] = true
	}

	// (b) the enable gate: ENABLED when the sink is wired...
	on := newP48Fixture(t, nil)
	if !witnessFamilyEnabled(on.sched, witnessFamilyProtectionDecision) {
		t.Fatal("T308/D7: the sixth family's own gate must be ON when the sink is wired")
	}
	view := on.sched.WitnessReconcileLocalView()
	if view.Families[witnessFamilyProtectionDecision].NotEnabled {
		t.Fatal("T308/D7: the GET probe must NOT report not_enabled while the producer runs")
	}

	// ...and OFF without one, so a disabled family reads "never ran", not
	// "deleted".
	off := newP48Fixture(t, func(cfg *HistoryExportConfig, f *p48Fixture) {
		cfg.DecisionAttest = nil
		f.sink = nil
	})
	if witnessFamilyEnabled(off.sched, witnessFamilyProtectionDecision) {
		t.Fatal("T308: without a sink the sixth family has no producer and must be disabled")
	}
	if !off.sched.WitnessReconcileLocalView().Families[witnessFamilyProtectionDecision].NotEnabled {
		t.Fatal("T308: the GET probe must report not_enabled for a disabled sixth family")
	}
}

// ---------------------------------------------------------------------------
// T309 — delivery uniqueness: the sixth family gains EXACTLY one attempt per tick
// ---------------------------------------------------------------------------

func TestP48T309ExactlyOneAttemptPerTick(t *testing.T) {
	f := newP48Fixture(t, nil)
	// A REALISTIC sixth-family entry: publication_id is the chain head seq (> 0).
	head := anchorEntry{
		AnchorSeq: 1, Kind: anchorKindProtectionDecision,
		PublicationID: 7, ManifestDigest: strings.Repeat("cd", 32),
		RecordedAt: p48T0.UTC().Format(time.RFC3339Nano),
	}
	if err := f.sched.signer.signAnchorEntry(&head, f.dir, p48T0); err != nil {
		t.Fatal(err)
	}
	head.State = anchorStatePending
	if err := appendAnchorEntryPath(decisionAnchorPath(f.dir), 0, head); err != nil {
		t.Fatal(err)
	}

	tr := &scriptedAnchorTransport{status: 200, body: `{"ack_id":"a-1"}`}
	f.sched.anchorTransport = tr
	f.tick()

	st, err := loadAnchorStatePath(decisionAnchorPath(f.dir), f.dir, f.sched.trust)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.latest[1].Attempts; got != 1 {
		t.Fatalf("T309: the sixth family must gain exactly one attempt in a tick, got %d", got)
	}
	dispatches := 0
	for _, r := range tr.reqs {
		if r.PublicationID == 7 && r.AnchorSeq == 1 {
			dispatches++
		}
	}
	if dispatches != 1 {
		t.Fatalf("T309: want exactly one dispatch for the sixth stream, got %d", dispatches)
	}
}

// ---------------------------------------------------------------------------
// T310 — the P46 reuse: window, absence matrix and the outside boundary
// ---------------------------------------------------------------------------

func TestP48T310P46ReuseWindowAndAbsenceMatrix(t *testing.T) {
	// AnchorCapacity = 2 so the third tick compacts the anchor PREFIX away: that
	// is what gives the sixth family a window whose lower bound is not 1, i.e. a
	// reachable "outside" boundary (a valid projection always has seq > 0).
	f := newP48Fixture(t, func(cfg *HistoryExportConfig, fx *p48Fixture) {
		cfg.AnchorCapacity = 2
	})
	for i := 0; i < 3; i++ {
		f.emit(1)
		f.tick()
	}

	items := p48WitnessItems(t, f)
	if len(items) != 2 {
		t.Fatalf("T310: the compacted window must hold two anchor entries, got %d", len(items))
	}
	if row := p48P46Verdict(t, f, items); row.Verdict != witnessVerdictIntact {
		t.Fatalf("T310: in-window projections must read family_intact, got %q (err=%q)", row.Verdict, row.Error)
	}

	// The OUTSIDE boundary (R40-1): a seq BELOW the local window is silence, never
	// a claim — legal prefix compaction and deletion are indistinguishable there.
	below := items[0]
	below.AnchorSeq = items[0].AnchorSeq - 1
	below.AnchorDigest = strings.Repeat("ab", 32)
	row := p48P46Verdict(t, f, append(append([]anchorRequest{}, items...), below))
	if row.Verdict != witnessVerdictIntact || row.OutsideCount != 1 {
		t.Fatalf("T310: a below-window seq must be counted outside and assert nothing, got verdict=%q outside=%d", row.Verdict, row.OutsideCount)
	}

	// The INCOMPLETE quadrant is one file gone (the DELETED quadrant is T301's).
	if err := os.Remove(decisionLogPath(f.dir)); err != nil {
		t.Fatal(err)
	}
	if row := p48P46Verdict(t, f, items); row.Verdict != witnessVerdictIncomplete {
		t.Fatalf("T310: one file gone must read family_incomplete, got %q", row.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T311 — the read face has ZERO side effects (§4)
// ---------------------------------------------------------------------------

func TestP48T311ReadFaceZeroSideEffects(t *testing.T) {
	f := newP48Fixture(t, nil)
	f.emit(3)
	f.tick()
	// Drain a second tick so every producer has settled and the file set is final.
	f.tick()

	before := p48DirSnapshot(t, f.dir)
	_ = f.sched.Status()
	_ = f.sched.AnchorDeliveryStatus()
	_ = f.sched.WitnessReconcileLocalView()
	after := p48DirSnapshot(t, f.dir)

	if before != after {
		t.Fatalf("T311: the read faces changed the export directory:\n before=%s\n after =%s", before, after)
	}
}

// ---------------------------------------------------------------------------
// T312 — fail-closed: an unclassifiable line stops the family, not the others (I9)
// ---------------------------------------------------------------------------

// RED (MU4: fail-closed removed, sha256 restored): the drain then appends past
// an unclassifiable line, so the decision log's bytes change and this test fails
// on its first byte comparison — the invariant requires a byte-ZERO change.
func TestP48T312UnclassifiableLineFailsClosed(t *testing.T) {
	f := newP48Fixture(t, nil)
	f.emit(2)
	f.tick()

	// A line the classifier cannot read. It is appended verbatim, so the file
	// physically holds it (exactly the state a byte edit or a foreign writer
	// produces).
	logPath := decisionLogPath(f.dir)
	withJunk := append(p48Read(t, logPath), []byte("this is not a decision record\n")...)
	if err := os.WriteFile(logPath, withJunk, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeLog := p48Read(t, logPath)
	beforeAnchor := p48Read(t, decisionAnchorPath(f.dir))

	// A tick with new records queued: the family must refuse to sweep.
	f.emit(2)
	f.tick()

	if !bytes.Equal(beforeLog, p48Read(t, logPath)) {
		t.Fatal("T312/I9: an unclassifiable line must leave the decision log byte-identical")
	}
	if !bytes.Equal(beforeAnchor, p48Read(t, decisionAnchorPath(f.dir))) {
		t.Fatal("T312/I9: an unclassifiable line must leave the anchor stream byte-identical")
	}

	// The state is loud and fail-closed.
	if got := f.state(); got != decisionLogStateDivergent {
		t.Fatalf("T312: want divergent over an unreadable log, got %q", got)
	}
	if f.attested() {
		t.Fatal("T312: an unreadable log must never be attested")
	}

	// Family isolation: the OTHER families' faces are unaffected.
	if row := p47Row(t, f.sched, witnessFamilyLedger); row.Error != "" || !row.Converged {
		t.Fatalf("T312: the ledger family must be unaffected, got %+v", row)
	}
}

// ---------------------------------------------------------------------------
// T313 — the secret boundary is inherited, not widened (R24-7)
// ---------------------------------------------------------------------------

func TestP48T313NoCleartextOrKeyInTheChain(t *testing.T) {
	f := newP48Fixture(t, nil)
	// A decision whose ONLY identity material is the hash; the cleartext
	// principal never enters the record.
	f.sink.Emit(context.Background(), protection.DecisionProvenance{
		TraceID: "trace-secret", CapabilityID: "cap.exec",
		PrincipalHash: strings.Repeat("9f", 32),
		Guard:         "admit", Decision: "admit", Action: "protection.admit",
		Detail: "admitted", At: p48T0,
	})
	f.tick()

	raw := string(p48Read(t, decisionLogPath(f.dir)))
	allowed := map[string]bool{
		"seq": true, "prev_digest": true, "digest": true, "at": true,
		"trace_id": true, "capability_id": true, "principal_hash": true,
		"guard": true, "decision": true, "action": true, "threshold": true,
		"observed": true, "detail": true, "latency_micros": true,
	}
	for _, ln := range p47RawLines(t, decisionLogPath(f.dir)) {
		var obj map[string]any
		if err := json.Unmarshal([]byte(ln), &obj); err != nil {
			t.Fatal(err)
		}
		for k := range obj {
			if !allowed[k] {
				t.Fatalf("T313: the record carries an unvetted field %q: %s", k, ln)
			}
		}
	}
	// No private key material, no bearer token, no cleartext principal.
	for _, banned := range []string{"PRIVATE KEY", "Bearer ", "alice", "password"} {
		if strings.Contains(raw, banned) {
			t.Fatalf("T313: the chain leaks %q", banned)
		}
	}
}

// ---------------------------------------------------------------------------
// T314 — no retro-attestation (I11 / A7-⑧)
// ---------------------------------------------------------------------------

func TestP48T314NoRetroAttestationFromAudit(t *testing.T) {
	// A deployment whose export face ran WITHOUT the sixth family: decisions were
	// taken (and audited), but nothing durable about them exists.
	f := newP48Fixture(t, func(cfg *HistoryExportConfig, fx *p48Fixture) {
		cfg.DecisionAttest = nil
		fx.sink = nil
	})
	f.tick()
	if p48Exists(decisionLogPath(f.dir)) {
		t.Fatal("T314: a disabled family must not create a decision log")
	}
	r := f.row()
	if r.DecisionLogState != decisionLogStateNotEnabled {
		t.Fatalf("T314: want not_enabled, got %q", r.DecisionLogState)
	}
	if r.DecisionAttested == nil || *r.DecisionAttested {
		t.Fatal("T314: a disabled family can never be attested")
	}

	// Enabling it later does NOT invent history for the pre-enable decisions: the
	// queue is empty and no record is retro-generated.
	f.cfg.DecisionAttest = NewDecisionAttestSink(f.ring, f.dir)
	s, err := NewHistoryExportScheduler(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.sched = s
	s.Tick(context.Background())
	if p48Exists(decisionLogPath(f.dir)) {
		t.Fatal("T314/I11: enabling the family must not fabricate records for pre-enable decisions")
	}
	if got := f.state(); got != decisionLogStateNoAnchor {
		t.Fatalf("T314: an enabled but empty family must read no_anchor, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// T315 — the two kinds of "fewer records" are discriminated (I6)
// ---------------------------------------------------------------------------

// RED (MU1, sha256 restored): without the head-vs-anchored-head comparison there
// is no way to tell a legal prefix compaction from a removed tail — both read as
// an existing anchor entry, i.e. the criterion is near-tautological and the
// truncation half of this test fails.
func TestP48T315CompactionVersusTailDeletion(t *testing.T) {
	shrinkDecisionCaps(t)
	f := newP48Fixture(t, nil)
	// One more record than the retention cap, so the drain's own compaction drops
	// the oldest whole groups in the SAME tick that anchors the head.
	n := decisionLogCapacity + 8
	f.emit(n)
	f.tick()

	logPath := decisionLogPath(f.dir)
	lines := p47RawLines(t, logPath)
	if len(lines) != decisionLogCapacity {
		t.Fatalf("T315: the log must be bounded to %d groups, got %d", decisionLogCapacity, len(lines))
	}
	headSeq, headDigest, ok := walkDecisionChain(logPath)
	if !ok || headSeq != int64(n) {
		t.Fatalf("T315: compaction must leave the chain head at %d, got seq=%d ok=%t", n, headSeq, ok)
	}
	r := f.row()
	if r.DecisionLogState != decisionLogStateAttested || !f.attested() {
		t.Fatalf("T315/I6: a LEGAL prefix compaction must stay attested, got state=%q attested=%t", r.DecisionLogState, f.attested())
	}
	if r.DecisionQueueDropped == nil || *r.DecisionQueueDropped != 0 {
		t.Fatalf("T315/I6: a legal prefix compaction is bounded RETENTION, never loss; got %v", r.DecisionQueueDropped)
	}
	// The head digest is still the anchored one.
	st, err := loadAnchorStatePath(decisionAnchorPath(f.dir), f.dir, f.sched.trust)
	if err != nil {
		t.Fatal(err)
	}
	e := st.latest[st.window.MaxSeq]
	if e.PublicationID != headSeq || e.ManifestDigest != headDigest {
		t.Fatalf("T315: the anchor entry must still name the head, got %d/%q want %d/%q", e.PublicationID, e.ManifestDigest, headSeq, headDigest)
	}

	// The SAME construction, but the tail is removed instead of the prefix: the
	// head moves off the anchored head and the reading flips.
	p48TruncateTail(t, logPath, 3)
	if got := f.state(); got != decisionLogStateTruncated {
		t.Fatalf("T315/I6: a removed tail must read truncated, got %q", got)
	}
	if f.attested() {
		t.Fatal("T315/I6: the two kinds of 'fewer records' must never be conflated")
	}
}

// ---------------------------------------------------------------------------
// T316 — the mutation campaign (MU1~MU6)
// ---------------------------------------------------------------------------
//
// EXECUTED (sha256 17cbcb40... -> mutate -> RED -> restore -> 17cbcb40...):
// each of the six mutations below was applied to the production source, its own
// test was run, the test turned RED in every case, and the file was restored and
// its sha256 re-checked against the pre-mutation value (both the new file and
// snapshot_anchor_delivery.go, which MU5 edits). The campaign is not a Go test
// (it mutates the code under test); this function documents the mapping so the
// matrix lives next to the tests it guards.
//
//	MU1  drop the local recompute (decisionLogStateAt always returns attested)
//	     ⇒ T299 red (a rewritten record still reads attested).
//	MU2  drop the loss persistence (persistLoss becomes a no-op)
//	     ⇒ T304 red (a restart reports 0 losses).
//	MU3  fold the loss into attested (remove `dropped == 0` from the conjunction)
//	     ⇒ T303 red (an overflow reads attested).
//	MU4  drop fail-closed (append past an unclassifiable line)
//	     ⇒ T312 red (the decision log changes bytes).
//	MU5  break the partition or omit the witnessFamilyEnabled case
//	     ⇒ T308 red (five/six streams or a permanently not_enabled family).
//	MU6  make the sink REPLACE the ring instead of delegating
//	     ⇒ T298 red (the P24.2 faces change bytes).
func TestP48T316MutationMatrixDocumented(t *testing.T) {
	// A structural guard so the matrix cannot rot silently: the six mechanisms
	// the mutations target must all still be present in the shipped code.
	if decisionLogStateAttested == decisionLogStateDivergent {
		t.Fatal("MU1/MU3: the state vocabulary collapsed")
	}
	dir := t.TempDir()
	ring := protection.NewRecordingProvenanceSink(8)
	sink := NewDecisionAttestSink(ring, dir)
	sink.Emit(context.Background(), p48Prov(1))
	// MU6 guard: the ring and the sink must be two views of ONE store.
	if len(ring.Recent(1)) != 1 {
		t.Fatal("MU6: the sink must DELEGATE to the ring, not replace it")
	}
	if sink.Stats().Capacity != ring.Stats().Capacity {
		t.Fatal("MU6: Stats must be the ring's")
	}
	// MU2 guard: the loss counter is a durable artifact, not a field.
	sink.addDropped(1)
	if err := sink.persistLoss(); err != nil {
		t.Fatal(err)
	}
	if !p48Exists(decisionLossPath(dir)) {
		t.Fatal("MU2: the loss counter must be persisted to disk")
	}
	// MU4 guard: the classifier must reject an unreadable line.
	if _, ok := decisionGroupOf([]byte("not json")); ok {
		t.Fatal("MU4: an unreadable line must not classify")
	}
	// MU5 guard: the registry and the partition must both carry the sixth family.
	if !witnessFamilyKnown(witnessFamilyProtectionDecision) {
		t.Fatal("MU5: the sixth family must be registered")
	}
	if len(anchorDeliveryStreams()) != 6 {
		t.Fatal("MU5: the partition must hold six streams")
	}
}

// T305b — F3 (final review): a CORRUPT loss counter must not read as zero.
// The counter is the one artifact without integrity cover; zeroing it would
// launder real drops and re-light decision_attested. Fail-closed: the error is
// sticky and the attested claim is withheld.
func TestP48T305bCorruptLossCounterIsFailClosed(t *testing.T) {
	dir := t.TempDir()
	ring := protection.NewRecordingProvenanceSink(8)
	if err := os.WriteFile(filepath.Join(dir, decisionLossFile), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := NewDecisionAttestSink(ring, dir)
	if sink.lossErr == "" {
		t.Fatalf("T305b: a corrupt loss counter must set the sticky error")
	}
}

// T305c — F1 (final review MAJOR): the pending_anchor branch. A decision log
// that reaches BEYOND the newest ANCHORED anchor entry must never read
// attested — the extra records are durable but not witnessed (the exact state
// a failed anchor dispatch plus a silent tail produces). Fabricated anchor
// state + the real decision log: unit coverage for the branch that the
// reviewer's overlay probes exercised but the repo did not pin.
func TestP48T305cPendingAnchorBranchIsNotAttested(t *testing.T) {
	f := newP48Fixture(t, nil)
	f.emit(1)
	f.tick()

	// The head digest while the log holds exactly record 1.
	head1, digest1, ok := walkDecisionChain(decisionLogPath(f.dir))
	if !ok || head1 != 1 {
		t.Fatalf("T305c precondition: want head=1, got head=%d ok=%v", head1, ok)
	}

	// Extend the log to 3 records (the fixture anchors them too, but we pass a
	// fabricated anchor state below; only decisionLogStateAt's comparison is
	// under test).
	f.emit(2)
	f.tick()

	fabricated := func(anchoredPubID int64, anchoredDigest string) *anchorState {
		return &anchorState{
			latest: map[int64]anchorEntry{
				1: {AnchorSeq: 1, PublicationID: anchoredPubID, State: anchorStateAnchored, ManifestDigest: anchoredDigest},
			},
			window: anchorWindow{MinSeq: 1, MaxSeq: 1, Entries: 1},
		}
	}

	// (a) anchored head BEHIND the log head (anchor dispatch failed, no newer
	// entry covers the tail): NOT attested.
	if got := f.sched.decisionLogStateAt(fabricated(1, digest1)); got != decisionLogStatePendingAnchor {
		t.Fatalf("T305c: head beyond the anchored point must read pending_anchor, got %q", got)
	}

	// (b) anchored head AT the log head with the matching digest: attested.
	head3, digest3, ok := walkDecisionChain(decisionLogPath(f.dir))
	if !ok || head3 != 3 {
		t.Fatalf("T305c precondition: want head=3, got head=%d ok=%v", head3, ok)
	}
	if got := f.sched.decisionLogStateAt(fabricated(3, digest3)); got != decisionLogStateAttested {
		t.Fatalf("T305c: a matching anchored head must read attested, got %q", got)
	}

	// (c) same length, different digest: divergent (unchanged behaviour).
	if got := f.sched.decisionLogStateAt(fabricated(3, digest1)); got != decisionLogStateDivergent {
		t.Fatalf("T305c: a digest mismatch at the same length must read divergent, got %q", got)
	}
}
