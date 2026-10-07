package server

// Phase 47 — Anchor Delivery Reliability tests (T276~T296; ADR-069 §5,
// ADR-070 §7).
//
// The fixture drives the REAL scheduler with anchoring on. Recovery is exercised
// by hand-writing a signed `pending` line (the crash state: the pending line is
// durable, the state advance is not) and then running a REAL tick / sweep.
// Nothing here reimplements the state machine: every state-advance line under
// test is produced by the production code.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// p47CountReqs counts dispatches that belong to a swept stream (PublicationID
// 0 distinguishes them from the publication/ledger stream the tick also writes).
func p47CountReqs(tr *scriptedAnchorTransport, seq int64) int {
	n := 0
	for _, r := range tr.reqs {
		if r.PublicationID == 0 && r.AnchorSeq == seq {
			n++
		}
	}
	return n
}

var p47T0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func p47TS() string { return p47T0.UTC().Format(time.RFC3339Nano) }

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type p47Fixture struct {
	t          *testing.T
	root       string
	dir        string
	witnessDir string
	store      *fakeExportStore
	sched      *HistoryExportScheduler
	cfg        HistoryExportConfig
	privPath   string
	pubPath    string
}

func newP47Fixture(t *testing.T, tune func(cfg *HistoryExportConfig)) *p47Fixture {
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
		Transitions: sampleTransitions(), ExportedAt: p47T0, MinSeq: 1, MaxSeq: 3,
	}
	cfg := HistoryExportConfig{
		Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"},
		Clock:          func() time.Time { return p47T0 },
		SignKeyPath:    privPath,
		TrustKeyPaths:  []string{pubPath},
		AnchorEndpoint: "file://" + witnessDir,
	}
	if tune != nil {
		tune(&cfg)
	}
	sched, err := NewHistoryExportScheduler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &p47Fixture{
		t: t, root: root, dir: dir, witnessDir: witnessDir,
		store: st, sched: sched, cfg: cfg, privPath: privPath, pubPath: pubPath,
	}
}

// rebuild constructs a FRESH scheduler over the same export dir (the crash /
// restart shape: the durable pending line survives, the process does not).
func (f *p47Fixture) rebuild() *HistoryExportScheduler {
	f.t.Helper()
	s, err := NewHistoryExportScheduler(f.cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *p47Fixture) useScripted(status int, body string) *scriptedAnchorTransport {
	f.t.Helper()
	tr := &scriptedAnchorTransport{status: status, body: body}
	f.sched.anchorTransport = tr
	return tr
}

func (f *p47Fixture) tick(s *HistoryExportScheduler) {
	f.t.Helper()
	s.Tick(context.Background())
}

// seedPending signs and appends ONE durable `pending` anchor line — the crash
// state (the pending line exists, no dispatch ever happened).
func (f *p47Fixture) seedPending(path string, e anchorEntry) {
	f.t.Helper()
	if err := f.sched.signer.signAnchorEntry(&e, f.dir, p47T0); err != nil {
		f.t.Fatal(err)
	}
	e.State = anchorStatePending
	if err := appendAnchorEntryPath(path, 0, e); err != nil {
		f.t.Fatalf("seed pending: %v", err)
	}
}

func p47ReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func p47RawLines(t *testing.T, path string) []string {
	t.Helper()
	data := p47ReadFile(t, path)
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func p47AnchorLines(t *testing.T, path string) []anchorEntry {
	t.Helper()
	var out []anchorEntry
	for _, l := range p47RawLines(t, path) {
		var e anchorEntry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("anchor line unparseable: %v (%s)", err, l)
		}
		out = append(out, e)
	}
	return out
}

// p47CountState counts the lines of one seq in one state.
func p47CountState(t *testing.T, path string, seq int64, state string) int {
	t.Helper()
	n := 0
	for _, e := range p47AnchorLines(t, path) {
		if e.AnchorSeq == seq && e.State == state {
			n++
		}
	}
	return n
}

func p47Latest(t *testing.T, s *HistoryExportScheduler, path string, seq int64) anchorEntry {
	t.Helper()
	st, err := loadAnchorStatePath(path, s.cfg.Dir, s.trust)
	if err != nil {
		t.Fatalf("load anchor state: %v", err)
	}
	e, ok := st.latest[seq]
	if !ok {
		t.Fatalf("anchor seq %d is not in the state (conflicts=%v)", seq, st.conflicts)
	}
	return e
}

func p47Row(t *testing.T, s *HistoryExportScheduler, family string) anchorDeliveryStreamStatus {
	t.Helper()
	sum := s.AnchorDeliveryStatus()
	for _, r := range sum.Families {
		if r.Family == family {
			return r
		}
	}
	t.Fatalf("family %q missing from the delivery face", family)
	return anchorDeliveryStreamStatus{}
}

func p47TopLevelKeys(t *testing.T, data []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if tok != json.Delim('{') {
		t.Fatalf("status is not a JSON object: %v", tok)
	}
	var keys []string
	for dec.More() {
		kt, kerr := dec.Token()
		if kerr != nil {
			t.Fatal(kerr)
		}
		keys = append(keys, kt.(string))
		var raw json.RawMessage
		if derr := dec.Decode(&raw); derr != nil {
			t.Fatal(derr)
		}
	}
	return keys
}

// ---------------------------------------------------------------------------
// T276 — default deployment byte-frozen + P47 group appended at the tail (I7/I9)
// ---------------------------------------------------------------------------

func TestP47T276DefaultStatusByteFrozen(t *testing.T) {
	off := newP47Fixture(t, func(cfg *HistoryExportConfig) { cfg.AnchorEndpoint = "" })
	offJSON, err := json.Marshal(off.sched.Status())
	if err != nil {
		t.Fatal(err)
	}
	// Anchoring off: the P47 group (and every Phase 40+ anchor field) is absent,
	// so the document is exactly the pre-P47 one.
	for _, banned := range []string{"anchor_delivery", "pending_retryable", "pending_exhausted", "swept_by"} {
		if bytes.Contains(offJSON, []byte(banned)) {
			t.Fatalf("I9 VIOLATION: a disabled anchor must leave the status byte-identical, found %q in %s", banned, offJSON)
		}
	}
	if st := off.sched.Status(); st.AnchorDelivery != nil {
		t.Fatalf("a disabled anchor must leave anchor_delivery nil, got %+v", st.AnchorDelivery)
	}

	on := newP47Fixture(t, nil)
	onJSON, err := json.Marshal(on.sched.Status())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(onJSON, []byte(`"anchor_delivery"`)) {
		t.Fatalf("an enabled anchor must expose the delivery group: %s", onJSON)
	}
	// The group is the LAST top-level key (append-only discipline).
	keys := p47TopLevelKeys(t, onJSON)
	if len(keys) == 0 || keys[len(keys)-1] != "anchor_delivery" {
		t.Fatalf("anchor_delivery must be the last top-level key, got %v", keys)
	}
}

// ---------------------------------------------------------------------------
// T277 — static partition: SIX streams, one sweeper each, no double coverage
// ---------------------------------------------------------------------------

func TestP47T277StaticPartition(t *testing.T) {
	streams := anchorDeliveryStreams()
	if len(streams) != 6 {
		t.Fatalf("the partition table must hold six streams, got %d", len(streams))
	}
	want := map[string]string{
		witnessFamilyLedger:             anchorSweeperHousekeeping,
		witnessFamilyKeyLifecycle:       anchorSweeperDeliverySweep,
		witnessFamilyVerification:       anchorSweeperDeliverySweep,
		witnessFamilyDestruction:        anchorSweeperDeliverySweep,
		witnessFamilyAcceptance:         anchorSweeperAcceptancePending,
		witnessFamilyProtectionDecision: anchorSweeperDeliverySweep,
	}
	seen := map[string]int{}
	for _, st := range streams {
		seen[st.Family]++
		if got, ok := want[st.Family]; !ok || got != st.SweptBy {
			t.Fatalf("stream %q swept_by = %q, want %q", st.Family, st.SweptBy, want[st.Family])
		}
	}
	for fam, n := range seen {
		if n != 1 {
			t.Fatalf("family %q is covered %d times (I1 requires exactly one sweeper)", fam, n)
		}
	}
	// The FOUR delivery_sweep streams carry distinct, non-nil mutexes.
	swept := deliverySweepStreams()
	if len(swept) != 4 {
		t.Fatalf("delivery_sweep must own exactly four streams, got %d", len(swept))
	}
	order := []string{witnessFamilyKeyLifecycle, witnessFamilyVerification, witnessFamilyDestruction, witnessFamilyProtectionDecision}
	mus := map[*sync.Mutex]bool{}
	for i, st := range swept {
		if st.Family != order[i] {
			t.Fatalf("delivery sweep order = %v, want %v (frozen)", swept[i].Family, order)
		}
		if st.DispatchMu == nil {
			t.Fatalf("swept stream %q has no dispatch mutex (I8)", st.Family)
		}
		if mus[st.DispatchMu] {
			t.Fatalf("two swept streams share one dispatch mutex (%q)", st.Family)
		}
		mus[st.DispatchMu] = true
	}
	// The read face names the same owner per family.
	f := newP47Fixture(t, nil)
	for fam, owner := range want {
		if row := p47Row(t, f.sched, fam); row.SweptBy != owner {
			t.Fatalf("read face swept_by for %q = %q, want %q", fam, row.SweptBy, owner)
		}
	}
}

// ---------------------------------------------------------------------------
// T278/T279/T280 — THE CORE RED CASE: a stranded pending (crash) converges on a
// fresh scheduler's tick, with attempts EXACTLY 1 (single sweeper).
// ---------------------------------------------------------------------------

func p47PendingEntry(kind string) anchorEntry {
	e := anchorEntry{AnchorSeq: 1, RecordedAt: p47TS()}
	switch kind {
	case witnessFamilyDestruction:
		e.Kind = anchorKindDestruction
		e.DestructionSeq = 1
		e.DestructionDigest = strings.Repeat("ab", 32)
		e.DestructionVerdict = destructionStateCompleted
	case witnessFamilyKeyLifecycle:
		e.Kind = anchorKindKeyLifecycle
		e.EventSeq = 1
		e.EventDigest = strings.Repeat("cd", 32)
		e.EventType = "activated"
	case witnessFamilyVerification:
		e.Kind = anchorKindVerification
		e.ReportSeq = 1
		e.ReportDigest = strings.Repeat("ef", 32)
		e.Overall = "attested"
	case witnessFamilyProtectionDecision:
		// Phase 48: the sixth family's carrier is Kind + PublicationID (chain head
		// seq) + ManifestDigest (chain head digest). The seeded head seq is left 0
		// here so the shared dispatch counter (which keys on PublicationID == 0)
		// keeps counting exactly the swept streams; T309 seeds a REALISTIC
		// non-zero head instead.
		e.Kind = anchorKindProtectionDecision
		e.ManifestDigest = strings.Repeat("12", 32)
	}
	return e
}

func p47StreamPath(fam string) func(string) string {
	switch fam {
	case witnessFamilyDestruction:
		return destructionAnchorPath
	case witnessFamilyKeyLifecycle:
		return keyLifecycleAnchorPath
	case witnessFamilyVerification:
		return verificationAnchorPath
	case witnessFamilyProtectionDecision:
		// Phase 48: the SIXTH family's anchor stream, the fourth swept stream.
		return decisionAnchorPath
	}
	panic("unknown family " + fam)
}

func TestP47T278CrashRecoveryDestruction(t *testing.T) {
	f := newP47Fixture(t, nil)
	path := destructionAnchorPath(f.dir)
	f.seedPending(path, p47PendingEntry(witnessFamilyDestruction))

	// A FRESH scheduler over the same dir: the durable pending survives.
	s := f.rebuild()
	tr := &scriptedAnchorTransport{status: 200, body: `{"ack_id":"a-1"}`}
	s.anchorTransport = tr
	f.tick(s)

	e := p47Latest(t, s, path, 1)
	if e.State != anchorStateAnchored || e.Attempts != 1 {
		t.Fatalf("T278: stranded pending must converge with attempts=1, got state=%q attempts=%d", e.State, e.Attempts)
	}
	if got := p47CountReqs(tr, 1); got != 1 {
		t.Fatalf("T278: exactly one dispatch of the stranded seq, got %d", got)
	}
}

func TestP47T279CrashRecoveryKeyLifecycle(t *testing.T) {
	f := newP47Fixture(t, nil)
	path := keyLifecycleAnchorPath(f.dir)
	f.seedPending(path, p47PendingEntry(witnessFamilyKeyLifecycle))

	s := f.rebuild()
	tr := &scriptedAnchorTransport{status: 200, body: `{"ack_id":"a-1"}`}
	s.anchorTransport = tr
	f.tick(s)

	e := p47Latest(t, s, path, 1)
	if e.State != anchorStateAnchored || e.Attempts != 1 {
		t.Fatalf("T279: stranded pending must converge with attempts=1, got state=%q attempts=%d", e.State, e.Attempts)
	}
}

func TestP47T280CrashRecoveryVerification(t *testing.T) {
	f := newP47Fixture(t, nil)
	path := verificationAnchorPath(f.dir)
	f.seedPending(path, p47PendingEntry(witnessFamilyVerification))

	s := f.rebuild()
	tr := &scriptedAnchorTransport{status: 200, body: `{"ack_id":"a-1"}`}
	s.anchorTransport = tr
	f.tick(s)

	e := p47Latest(t, s, path, 1)
	if e.State != anchorStateAnchored || e.Attempts != 1 {
		t.Fatalf("T280: stranded pending must converge with attempts=1, got state=%q attempts=%d", e.State, e.Attempts)
	}
}

// ---------------------------------------------------------------------------
// T281 — boundedness: +1 attempt per tick, terminal `unanchored` at max, and no
// further dispatch afterwards.
// ---------------------------------------------------------------------------

func TestP47T281BoundedAttemptsTerminalise(t *testing.T) {
	f := newP47Fixture(t, func(cfg *HistoryExportConfig) { cfg.AnchorMaxAttempts = 3 })
	path := keyLifecycleAnchorPath(f.dir)
	f.seedPending(path, p47PendingEntry(witnessFamilyKeyLifecycle))
	tr := f.useScripted(503, `{}`)

	for round := 1; round <= 3; round++ {
		f.tick(f.sched)
		e := p47Latest(t, f.sched, path, 1)
		if round < 3 {
			if e.State != anchorStatePending || e.Attempts != round {
				t.Fatalf("T281 round %d: want pending attempts=%d, got state=%q attempts=%d", round, round, e.State, e.Attempts)
			}
			continue
		}
		if e.State != anchorStateUnanchored || e.Attempts != 3 {
			t.Fatalf("T281 round 3: want unanchored attempts=3, got state=%q attempts=%d", e.State, e.Attempts)
		}
	}
	before := p47CountReqs(tr, 1)
	f.tick(f.sched) // max+1: the terminal entry is never dispatched again
	if got := p47CountReqs(tr, 1); got != before {
		t.Fatalf("T281: an unanchored entry must not be dispatched again, witness grew by %d", got-before)
	}
	if e := p47Latest(t, f.sched, path, 1); e.Attempts != 3 {
		t.Fatalf("T281: attempts must stay at the cap, got %d", e.Attempts)
	}
}

// ---------------------------------------------------------------------------
// T282 — `unanchored` is terminal and never revived (ADR-052 §6-12)
// ---------------------------------------------------------------------------

func TestP47T282UnanchoredNeverRevived(t *testing.T) {
	f := newP47Fixture(t, nil)
	path := keyLifecycleAnchorPath(f.dir)
	e := p47PendingEntry(witnessFamilyKeyLifecycle)
	if err := f.sched.signer.signAnchorEntry(&e, f.dir, p47T0); err != nil {
		t.Fatal(err)
	}
	e.State = anchorStateUnanchored
	e.Attempts = 1
	e.LastError = "witness refused"
	if err := appendAnchorEntryPath(path, 0, e); err != nil {
		t.Fatal(err)
	}
	before := p47ReadFile(t, path)
	tr := f.useScripted(200, `{"ack_id":"a-1"}`)
	f.tick(f.sched)
	if !bytes.Equal(before, p47ReadFile(t, path)) {
		t.Fatalf("T282: an unanchored entry must not be revived; the file changed")
	}
	if got := p47CountReqs(tr, 1); got != 0 {
		t.Fatalf("T282: no dispatch may target an unanchored entry, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// T283 — zero double dispatch: an entry pending at tick start gains EXACTLY +1
// attempt in one tick (all four swept streams).
// ---------------------------------------------------------------------------

func TestP47T283ExactlyOneAttemptPerTick(t *testing.T) {
	f := newP47Fixture(t, nil)
	sweptFamilies := []string{witnessFamilyKeyLifecycle, witnessFamilyVerification, witnessFamilyDestruction, witnessFamilyProtectionDecision}
	for _, fam := range sweptFamilies {
		f.seedPending(p47StreamPath(fam)(f.dir), p47PendingEntry(fam))
	}
	tr := f.useScripted(200, `{"ack_id":"a-1"}`)
	f.tick(f.sched)
	// All four swept streams share seq 1 (independent sequence spaces), so the
	// witness must have seen exactly one dispatch per stream — four, not eight.
	if got := p47CountReqs(tr, 1); got != 4 {
		t.Fatalf("T283: four streams must dispatch once each, got %d", got)
	}
	for _, fam := range sweptFamilies {
		e := p47Latest(t, f.sched, p47StreamPath(fam)(f.dir), 1)
		if e.Attempts != 1 {
			t.Fatalf("T283: stream %q attempts = %d, want exactly 1 (no double dispatch)", fam, e.Attempts)
		}
	}
}

// ---------------------------------------------------------------------------
// T284 — visibility: the three previously-invisible streams now report pending,
// and the pre-existing status document is byte-unchanged.
// ---------------------------------------------------------------------------

func TestP47T284PendingVisibleAndExistingStatusUnchanged(t *testing.T) {
	f := newP47Fixture(t, func(cfg *HistoryExportConfig) {
		kakPriv, kakPub, _, _ := genKeyPair(t, filepath.Join(filepath.Dir(cfg.Dir), "keys"), "kak")
		cfg.KeyAuthorityPath = kakPriv
		cfg.KeyAuthorityTrustPaths = []string{kakPub}
		cfg.VerifyAttest = true
		cfg.DestructionLog = true
	})
	pre := f.sched.Status()
	preJSON := p47WithoutDelivery(t, pre)

	for _, fam := range []string{witnessFamilyKeyLifecycle, witnessFamilyVerification, witnessFamilyDestruction} {
		f.seedPending(p47StreamPath(fam)(f.dir), p47PendingEntry(fam))
	}
	post := f.sched.Status()
	for _, fam := range []string{witnessFamilyKeyLifecycle, witnessFamilyVerification, witnessFamilyDestruction} {
		row := p47Row(t, f.sched, fam)
		if row.Pending != 1 || row.Converged {
			t.Fatalf("T284: stream %q must report pending=1, converged=false; got %+v", fam, row)
		}
	}
	if post.AnchorDelivery == nil || post.AnchorDelivery.Converged {
		t.Fatalf("T284: the global converged must be false while a stream is pending: %+v", post.AnchorDelivery)
	}
	// The pre-existing document (every field but the Phase 47 and Phase 49
	// groups) is byte-identical.
	if got := p47WithoutDelivery(t, post); got != preJSON {
		t.Fatalf("T284: seeding anchor streams must not change the pre-existing status:\n got %s\nwant %s", got, preJSON)
	}
	// The five family summaries keep their own (unrelated) values.
	if post.KeyLifecycle == nil || post.Verification == nil || post.Destruction == nil {
		t.Fatalf("T284: the family summaries must still be present")
	}
}

// p47WithoutDelivery marshals the status with the Phase 47 group removed.
//
// Phase 49 (ADR-074 §4) adds a SECOND new top-level group, `anchor_realization`,
// and it legitimately moves with the anchor-stream content: T284 seeds three
// `pending` anchor entries claiming evidence no main ledger holds, so the new
// face reads those families `unrealized` — which is exactly its job. T284's
// assertion is that the P47 ADDITION does not perturb the PRE-P47 document, so
// both post-P47 groups are dropped here; every other field is still compared
// byte-for-byte.
func p47WithoutDelivery(t *testing.T, st HistoryExportStatus) string {
	t.Helper()
	st.AnchorDelivery = nil
	st.AnchorRealization = nil
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// T285 — convergence after the sweep: converged=true ∧ pending=0, with
// anchored/unanchored counted honestly (A8-①).
// ---------------------------------------------------------------------------

func TestP47T285ConvergedAfterSweep(t *testing.T) {
	f := newP47Fixture(t, func(cfg *HistoryExportConfig) { cfg.AnchorMaxAttempts = 1 })
	// lifecycle: pending → unanchored (503 exhausts the single attempt).
	f.seedPending(keyLifecycleAnchorPath(f.dir), p47PendingEntry(witnessFamilyKeyLifecycle))
	// verification: already anchored (a legal state advance on disk).
	ve := p47PendingEntry(witnessFamilyVerification)
	if err := f.sched.signer.signAnchorEntry(&ve, f.dir, p47T0); err != nil {
		t.Fatal(err)
	}
	ve.State = anchorStatePending
	if err := appendAnchorEntryPath(verificationAnchorPath(f.dir), 0, ve); err != nil {
		t.Fatal(err)
	}
	anchored := ve
	anchored.State = anchorStateAnchored
	anchored.AckID = "a-0"
	if err := appendAnchorEntryPath(verificationAnchorPath(f.dir), 0, anchored); err != nil {
		t.Fatal(err)
	}
	f.useScripted(503, `{}`)
	f.tick(f.sched)

	sum := f.sched.AnchorDeliveryStatus()
	if !sum.Converged {
		t.Fatalf("T285: every stream must converge, got %+v", sum)
	}
	kl := p47Row(t, f.sched, witnessFamilyKeyLifecycle)
	if kl.Pending != 0 || kl.Unanchored != 1 || kl.Anchored != 0 || !kl.Converged {
		t.Fatalf("T285: lifecycle must be unanchored=1/pending=0/converged, got %+v", kl)
	}
	veRow := p47Row(t, f.sched, witnessFamilyVerification)
	if veRow.Anchored != 1 || veRow.Pending != 0 || !veRow.Converged {
		t.Fatalf("T285: verification must be anchored=1/converged, got %+v", veRow)
	}
}

// ---------------------------------------------------------------------------
// T286 — fail-closed on an unclassifiable line + stream isolation
// ---------------------------------------------------------------------------

func TestP47T286UnclassifiableLineFailClosed(t *testing.T) {
	f := newP47Fixture(t, nil)
	// Two healthy streams will be swept; the verification stream is poisoned.
	// Distinct anchor_seq per stream so a dispatch ATTEMPT is attributable.
	kl := p47PendingEntry(witnessFamilyKeyLifecycle)
	kl.AnchorSeq = 1
	f.seedPending(keyLifecycleAnchorPath(f.dir), kl)
	de := p47PendingEntry(witnessFamilyDestruction)
	de.AnchorSeq = 3
	de.DestructionSeq = 3
	f.seedPending(destructionAnchorPath(f.dir), de)
	ve := p47PendingEntry(witnessFamilyVerification)
	ve.AnchorSeq = 2
	vpath := verificationAnchorPath(f.dir)
	f.seedPending(vpath, ve)
	if err := appendLogLine(vpath, []byte("this is not an anchor entry")); err != nil {
		t.Fatal(err)
	}
	before := p47ReadFile(t, vpath)
	tr := f.useScripted(200, `{"ack_id":"a-1"}`)
	f.tick(f.sched)

	if !bytes.Equal(before, p47ReadFile(t, vpath)) {
		t.Fatalf("T286: an unclassifiable line must leave the stream byte-identical (fail-closed)")
	}
	// Fail-closed means NO dispatch is even attempted for the poisoned stream
	// (the network is a side effect that must not happen).
	if got := p47CountReqs(tr, 2); got != 0 {
		t.Fatalf("T286: the poisoned stream must not be dispatched, got %d attempts", got)
	}
	// Isolation: the other two streams still converged.
	if e := p47Latest(t, f.sched, keyLifecycleAnchorPath(f.dir), 1); e.State != anchorStateAnchored {
		t.Fatalf("T286: key_lifecycle must still be swept, got %q", e.State)
	}
	if e := p47Latest(t, f.sched, destructionAnchorPath(f.dir), 3); e.State != anchorStateAnchored {
		t.Fatalf("T286: destruction must still be swept, got %q", e.State)
	}
	row := p47Row(t, f.sched, witnessFamilyVerification)
	if row.Error == "" || row.Converged {
		t.Fatalf("T286: the poisoned stream must be loud and not converged, got %+v", row)
	}
	if f.sched.AnchorDeliveryStatus().Converged {
		t.Fatalf("T286: a stream error must make the global converged false")
	}
}

// ---------------------------------------------------------------------------
// T287 — the self-contradictory pending state (attempts >= max) is terminalised
// by the sweep, and the read face lists it separately BEFORE the sweep.
// ---------------------------------------------------------------------------

func TestP47T287ExhaustedPendingTerminalised(t *testing.T) {
	f := newP47Fixture(t, func(cfg *HistoryExportConfig) { cfg.AnchorMaxAttempts = 8 })
	path := keyLifecycleAnchorPath(f.dir)
	e := p47PendingEntry(witnessFamilyKeyLifecycle)
	if err := f.sched.signer.signAnchorEntry(&e, f.dir, p47T0); err != nil {
		t.Fatal(err)
	}
	e.State = anchorStatePending
	e.Attempts = 8
	if err := appendAnchorEntryPath(path, 0, e); err != nil {
		t.Fatal(err)
	}
	// Read face FIRST: the contradictory state is listed, never folded into pending.
	row := p47Row(t, f.sched, witnessFamilyKeyLifecycle)
	if row.Pending != 1 || row.PendingExhausted != 1 || row.PendingRetryable != 0 || row.Converged {
		t.Fatalf("T287: exhausted pending must be single-listed, got %+v", row)
	}
	tr := f.useScripted(200, `{"ack_id":"a-1"}`)
	f.tick(f.sched)
	got := p47Latest(t, f.sched, path, 1)
	if got.State != anchorStateUnanchored || got.Attempts != 8 {
		t.Fatalf("T287: exhausted pending must terminalise to unanchored, got state=%q attempts=%d", got.State, got.Attempts)
	}
	if got.LastError != "attempts exhausted (8)" {
		t.Fatalf("T287: terminal reason = %q, want %q", got.LastError, "attempts exhausted (8)")
	}
	if got := p47CountReqs(tr, 1); got != 0 {
		t.Fatalf("T287: an exhausted entry must NOT be dispatched, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// T288 — compaction liveness: a stranded pending makes the stream uncompactible;
// the sweep restores compaction.
// ---------------------------------------------------------------------------

func TestP47T288CompactionLivenessRestored(t *testing.T) {
	f := newP47Fixture(t, func(cfg *HistoryExportConfig) { cfg.AnchorCapacity = 2 })
	path := destructionAnchorPath(f.dir)
	for seq := int64(1); seq <= 3; seq++ {
		e := p47PendingEntry(witnessFamilyDestruction)
		e.AnchorSeq = seq
		e.DestructionSeq = seq
		f.seedPending(path, e)
	}
	if err := compactAnchorPrefixPathObserved(path, 2, nil); err == nil {
		t.Fatalf("T288: a stranded pending must make the over-capacity stream uncompactible")
	}
	f.useScripted(200, `{"ack_id":"a-1"}`)
	f.tick(f.sched)

	if err := compactAnchorPrefixPathObserved(path, 2, nil); err != nil {
		t.Fatalf("T288: after convergence the stream must compact again, got %v", err)
	}
	st, err := loadAnchorStatePath(path, f.dir, f.sched.trust)
	if err != nil {
		t.Fatal(err)
	}
	if st.window.Entries > 2 {
		t.Fatalf("T288: group count must fall back to <= capacity, got %d", st.window.Entries)
	}
}

// ---------------------------------------------------------------------------
// T289 — serialization: concurrent sweeps on one stream never overlap and
// produce exactly one terminal line.
// ---------------------------------------------------------------------------

type p47ConcurrencyTransport struct {
	mu       sync.Mutex
	cur, max int
	reqs     int
}

func (t *p47ConcurrencyTransport) deliver(_ context.Context, _ anchorRequest) (int, []byte, error) {
	t.mu.Lock()
	t.cur++
	if t.cur > t.max {
		t.max = t.cur
	}
	t.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	t.mu.Lock()
	t.cur--
	t.reqs++
	t.mu.Unlock()
	return 200, []byte(`{"ack_id":"a-1"}`), nil
}

func TestP47T289PerStreamDispatchSerialized(t *testing.T) {
	f := newP47Fixture(t, nil)
	path := keyLifecycleAnchorPath(f.dir)
	f.seedPending(path, p47PendingEntry(witnessFamilyKeyLifecycle))
	tr := &p47ConcurrencyTransport{}
	f.sched.anchorTransport = tr

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.sched.sweepAnchorDelivery(context.Background())
		}()
	}
	wg.Wait()

	if tr.max != 1 {
		t.Fatalf("T289: per-stream dispatch mutex must serialize deliveries, observed max concurrency %d", tr.max)
	}
	if tr.reqs != 1 {
		t.Fatalf("T289: only the first sweep may dispatch; got %d deliveries", tr.reqs)
	}
	if got := p47CountState(t, path, 1, anchorStateAnchored); got != 1 {
		t.Fatalf("T289: exactly one terminal line expected, got %d", got)
	}
	st, err := loadAnchorStatePath(path, f.dir, f.sched.trust)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.conflicts) != 0 {
		t.Fatalf("T289: no conflict seq may appear, got %v", st.conflicts)
	}
}

// ---------------------------------------------------------------------------
// T290 — the read face has ZERO side effects (no file byte changes, no network)
// ---------------------------------------------------------------------------

func TestP47T290ReadFaceZeroSideEffects(t *testing.T) {
	f := newP47Fixture(t, nil)
	for _, fam := range []string{witnessFamilyKeyLifecycle, witnessFamilyVerification, witnessFamilyDestruction} {
		f.seedPending(p47StreamPath(fam)(f.dir), p47PendingEntry(fam))
	}
	f.sched.anchorTransport = p46AlarmTransport{t: f.t} // any dispatch is a failure
	before := p46FilesSnapshot(f.t, f.dir, f.witnessDir)
	for i := 0; i < 3; i++ {
		_ = f.sched.Status()
		_ = f.sched.AnchorDeliveryStatus()
	}
	after := p46FilesSnapshot(f.t, f.dir, f.witnessDir)
	if len(before) != len(after) {
		t.Fatalf("T290: the read face changed the file set: %d -> %d", len(before), len(after))
	}
	for p, h := range before {
		if after[p] != h {
			t.Fatalf("T290: the read face changed %s", p)
		}
	}
}

// ---------------------------------------------------------------------------
// T291 — differential: the sweep's state advance is byte-identical to the
// producer path's (the only freedom is the family observer).
// ---------------------------------------------------------------------------

func TestP47T291DispatchDifferential(t *testing.T) {
	for _, fam := range []string{witnessFamilyKeyLifecycle, witnessFamilyVerification, witnessFamilyDestruction} {
		t.Run(fam, func(t *testing.T) {
			f := newP47Fixture(t, nil)
			path := p47StreamPath(fam)(f.dir)
			e := p47PendingEntry(fam)
			f.seedPending(path, e)
			original := p47ReadFile(t, path)
			f.useScripted(200, `{"ack_id":"a-1"}`)

			// Path A — the sweep.
			f.sched.sweepAnchorDelivery(context.Background())
			sweepLines := p47RawLines(t, path)
			if len(sweepLines) != 2 {
				t.Fatalf("T291: sweep must append exactly one state line, got %d", len(sweepLines)-1)
			}
			sweepAdvance := sweepLines[len(sweepLines)-1]

			// Reset to the exact crash state and replay through the PRODUCER
			// primitive (dispatchAnchorPath — what every producer calls).
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			signed := e
			if err := f.sched.signer.signAnchorEntry(&signed, f.dir, p47T0); err != nil {
				t.Fatal(err)
			}
			signed.State = anchorStatePending
			if derr := f.sched.dispatchAnchorPath(context.Background(), path, signed); derr != nil {
				t.Fatalf("T291: producer dispatch: %v", derr)
			}
			producerLines := p47RawLines(t, path)
			producerAdvance := producerLines[len(producerLines)-1]

			if sweepAdvance != producerAdvance {
				t.Fatalf("T291: the sweep and producer state advances drifted:\n sweep    %s\n producer %s", sweepAdvance, producerAdvance)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// T293 — conflicting seq: the WHOLE stream is not swept, byte-for-byte, and the
// read face is loud (review M1/M5).
// ---------------------------------------------------------------------------

func TestP47T293ConflictingSeqFailClosed(t *testing.T) {
	f := newP47Fixture(t, nil)
	path := keyLifecycleAnchorPath(f.dir)
	// Two lines, same anchor_seq, DIFFERENT payloads ⇒ a log conflict.
	a := p47PendingEntry(witnessFamilyKeyLifecycle)
	if err := f.sched.signer.signAnchorEntry(&a, f.dir, p47T0); err != nil {
		t.Fatal(err)
	}
	a.State = anchorStatePending
	b := a
	b.EventDigest = strings.Repeat("99", 32) // different signed payload
	if err := f.sched.signer.signAnchorEntry(&b, f.dir, p47T0); err != nil {
		t.Fatal(err)
	}
	b.State = anchorStatePending
	rawA, _ := serializeAnchorEntryBytes(&a)
	rawB, _ := serializeAnchorEntryBytes(&b)
	if err := appendLogLine(path, rawA); err != nil {
		t.Fatal(err)
	}
	if err := appendLogLine(path, rawB); err != nil {
		t.Fatal(err)
	}
	// A SECOND, healthy pending entry in the SAME stream: without the explicit
	// conflict check the sweep would advance it (and thereby "sweep a stream it
	// cannot evaluate"), so this is what makes the fail-closed observable.
	healthy := p47PendingEntry(witnessFamilyKeyLifecycle)
	healthy.AnchorSeq = 2
	healthy.EventSeq = 2
	f.seedPending(path, healthy)
	// A healthy OTHER stream so isolation is observable.
	f.seedPending(destructionAnchorPath(f.dir), p47PendingEntry(witnessFamilyDestruction))

	before := p47ReadFile(t, path)
	tr := f.useScripted(200, `{"ack_id":"a-1"}`)
	f.tick(f.sched)

	if !bytes.Equal(before, p47ReadFile(t, path)) {
		t.Fatalf("T293: a conflicting stream must not be swept (byte-identical)")
	}
	if got := p47CountReqs(tr, 2); got != 0 {
		t.Fatalf("T293: the conflicting stream's healthy seq must not be dispatched, got %d", got)
	}
	row := p47Row(t, f.sched, witnessFamilyKeyLifecycle)
	if len(row.Conflicts) == 0 || row.Error == "" || row.Converged {
		t.Fatalf("T293: the read face must report the conflict loudly, got %+v", row)
	}
	if f.sched.AnchorDeliveryStatus().Converged {
		t.Fatalf("T293: a conflict must make the global converged false")
	}
	if e := p47Latest(t, f.sched, destructionAnchorPath(f.dir), 1); e.State != anchorStateAnchored {
		t.Fatalf("T293: stream isolation broken — destruction must still be swept, got %q", e.State)
	}
}

// ---------------------------------------------------------------------------
// T294 — the in-tick residual boundary (A8-⑧). Deterministic model: a
// producer-dispatched entry (attempt 1) followed by ONE sweep in the same tick
// reaches at most attempt 2; each later tick adds exactly one.
// ---------------------------------------------------------------------------

func TestP47T294InTickResidualBoundary(t *testing.T) {
	f := newP47Fixture(t, func(cfg *HistoryExportConfig) { cfg.AnchorMaxAttempts = 8 })
	path := keyLifecycleAnchorPath(f.dir)
	f.seedPending(path, p47PendingEntry(witnessFamilyKeyLifecycle))
	f.useScripted(503, `{}`)

	// The producer's own dispatch (what AppendKeyLifecycleEvent does, under L1).
	e := p47Latest(t, f.sched, path, 1)
	keyLifecycleDispatchMu.Lock()
	_ = f.sched.dispatchAnchorPath(context.Background(), path, e)
	keyLifecycleDispatchMu.Unlock()
	if got := p47Latest(t, f.sched, path, 1).Attempts; got != 1 {
		t.Fatalf("T294: producer dispatch must leave attempts=1, got %d", got)
	}
	// The SAME tick's sweep may append at most one more attempt.
	f.sched.sweepAnchorDelivery(context.Background())
	if got := p47Latest(t, f.sched, path, 1).Attempts; got != 2 {
		t.Fatalf("T294: the same-tick sweep must cap attempts at 2, got %d", got)
	}
	// From the next tick on it is exactly +1 per sweep.
	f.sched.sweepAnchorDelivery(context.Background())
	if got := p47Latest(t, f.sched, path, 1).Attempts; got != 3 {
		t.Fatalf("T294: subsequent ticks must add exactly one attempt, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// T295 — ordering: drainDestructionAnchorQueue creates a destruction obligation
// IN-TICK, and the sweep (which runs BEFORE it) never re-dispatches it.
// ---------------------------------------------------------------------------

func TestP47T295SweepRunsBeforeInTickProducer(t *testing.T) {
	f := newAcceptanceFixture(t, func(cfg *HistoryExportConfig) {
		cfg.AnchorEndpoint = "file://" + filepath.Join(filepath.Dir(cfg.Dir), "witness")
		cfg.DestructionLog = true
		cfg.DestructionCapacity = 16
		cfg.AcceptanceCapacity = 2 // the 3rd append compacts the acceptance ledger
	})
	f.append(acceptT1, 3)
	f.append(acceptT2, 5)
	f.append(acceptT3, 7) // the compaction queues a destruction anchor

	// 503: the first dispatch does not confirm, so the entry stays pending. If the
	// sweep ran AFTER drain, it would re-dispatch it in the same tick (attempts 2).
	f.sched.anchorTransport = &scriptedAnchorTransport{status: 503, body: `{}`}
	f.sched.Tick(context.Background())

	// One destruction GROUP (seq 1); its LAST line carries the delivery state.
	path := destructionAnchorPath(f.dir)
	st, err := loadAnchorStatePath(path, f.dir, f.sched.trust)
	if err != nil {
		t.Fatal(err)
	}
	if st.window.Entries != 1 {
		t.Fatalf("T295: exactly one destruction group expected, got %d", st.window.Entries)
	}
	e := st.latest[1]
	if e.State != anchorStatePending || e.Attempts != 1 {
		t.Fatalf("T295: the in-tick destruction obligation must have attempts=1 (sweep runs before drain), got state=%q attempts=%d", e.State, e.Attempts)
	}
}

// ---------------------------------------------------------------------------
// T296 — the dispatch result never participates in converged: both arrival
// paths of `unanchored` converge with error=="" and a visible reason (M6).
// ---------------------------------------------------------------------------

func TestP47T296DispatchResultNeverFeedsConverged(t *testing.T) {
	t.Run("4xx-rejection", func(t *testing.T) {
		f := newP47Fixture(t, nil)
		path := keyLifecycleAnchorPath(f.dir)
		f.seedPending(path, p47PendingEntry(witnessFamilyKeyLifecycle))
		f.useScripted(403, `{}`)
		f.tick(f.sched)

		row := p47Row(t, f.sched, witnessFamilyKeyLifecycle)
		if !row.Converged || row.Error != "" || row.Pending != 0 || row.Unanchored != 1 {
			t.Fatalf("T296/4xx: an abandoned delivery must still converge, got %+v", row)
		}
		if row.LastUnanchoredReason == "" {
			t.Fatalf("T296/4xx: the abandonment reason must be visible")
		}
		if !f.sched.AnchorDeliveryStatus().Converged {
			t.Fatalf("T296/4xx: the global converged must be true")
		}
	})
	t.Run("attempts-exhausted", func(t *testing.T) {
		f := newP47Fixture(t, func(cfg *HistoryExportConfig) { cfg.AnchorMaxAttempts = 1 })
		path := keyLifecycleAnchorPath(f.dir)
		f.seedPending(path, p47PendingEntry(witnessFamilyKeyLifecycle))
		f.useScripted(503, `{}`)
		f.tick(f.sched)

		row := p47Row(t, f.sched, witnessFamilyKeyLifecycle)
		if !row.Converged || row.Error != "" || row.Pending != 0 || row.Unanchored != 1 {
			t.Fatalf("T296/exhausted: an exhausted delivery must still converge, got %+v", row)
		}
		if row.LastUnanchoredReason == "" {
			t.Fatalf("T296/exhausted: the abandonment reason must be visible")
		}
		if !f.sched.AnchorDeliveryStatus().Converged {
			t.Fatalf("T296/exhausted: the global converged must be true")
		}
	})
}
