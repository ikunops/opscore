package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Phase 40 — anchor persistence, identity and window.
// ---------------------------------------------------------------------------

type anchorFixture struct {
	dir    string
	signer *exportSigner
	trust  *exportTrustStore
}

func newAnchorFixture(t *testing.T) *anchorFixture {
	t.Helper()
	signer := deterministicSigner(t)
	pub, ok := signer.priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("test key is not Ed25519")
	}
	return &anchorFixture{
		dir:    t.TempDir(),
		signer: signer,
		trust:  &exportTrustStore{keys: map[string]ed25519.PublicKey{signer.keyID: pub}},
	}
}

// anchorTS is a fixed, human-readable recorded_at.
func anchorTS(i int) time.Time {
	return time.Date(2026, 9, 10, i, 0, 0, 0, time.UTC)
}

// signed builds and signs one anchor entry in the fixture's directory.
func (f *anchorFixture) signed(t *testing.T, e anchorEntry) anchorEntry {
	t.Helper()
	if err := f.signer.signAnchorEntry(&e, f.dir, anchorTS(int(e.AnchorSeq))); err != nil {
		t.Fatal(err)
	}
	return e
}

// statement is a fresh (unsigned) anchor statement for publication pub at seq.
func statement(pub, seq int64, digest string, state string, attempts int) anchorEntry {
	return anchorEntry{
		PublicationID:  pub,
		AnchorSeq:      seq,
		ManifestDigest: digest,
		RecordedAt:     anchorTS(int(seq)).UTC().Format(time.RFC3339Nano),
		State:          state,
		Attempts:       attempts,
	}
}

func (f *anchorFixture) path() string { return filepath.Join(f.dir, chainAnchorFile) }

func (f *anchorFixture) bytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(f.path())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// ---------------------------------------------------------------------------
// T146 — the signed payload covers identity + business fields ONLY: the
// delivery state is mutable and is never signed (ADR-053 §1.3).
// ---------------------------------------------------------------------------
func TestAnchorPayloadExcludesDeliveryState(t *testing.T) {
	f := newAnchorFixture(t)
	e := f.signed(t, statement(100, 12, strings.Repeat("ab", 32), anchorStatePending, 1))

	payload, err := canonicalAnchorPayload(&e)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"state"`, `"attempts"`, `"ack_id"`, `"last_error"`, `"anchored_at"`, `"sig"`} {
		if bytes.Contains(payload, []byte(banned)) {
			t.Fatalf("payload must not carry %s:\n%s", banned, payload)
		}
	}
	for _, required := range []string{`"publication_id"`, `"anchor_seq"`, `"manifest_digest"`, `"key_id"`, `"stream_id"`} {
		if !bytes.Contains(payload, []byte(required)) {
			t.Fatalf("payload must carry %s:\n%s", required, payload)
		}
	}

	// The real consequence: a state advance does NOT invalidate the proof.
	advanced := e
	advanced.State = anchorStateAnchored
	advanced.Attempts = 2
	advanced.AckID = "ack-12-2"
	advanced.AnchoredAt = anchorTS(12).UTC().Format(time.RFC3339Nano)
	if v := verifyAnchorEntrySignature(&advanced, f.trust, f.dir); v.Verdict != sigVerdictOK {
		t.Fatalf("a state advance must not invalidate the signature: %+v", v)
	}

	// And a business-field change DOES invalidate it.
	tampered := e
	tampered.ManifestDigest = strings.Repeat("ff", 32)
	if v := verifyAnchorEntrySignature(&tampered, f.trust, f.dir); v.Verdict != sigVerdictInvalid {
		t.Fatalf("changing a signed field must invalidate the signature: %+v", v)
	}
}

// ---------------------------------------------------------------------------
// T147 — an anchor_seq is consumed ONLY by a successful append (ADR-053 §4.2).
// ---------------------------------------------------------------------------
func TestAnchorSeqIsConsumedOnlyOnSuccess(t *testing.T) {
	f := newAnchorFixture(t)

	seq, err := nextAnchorSeq(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Fatalf("empty log ⇒ first seq = %d, want 1", seq)
	}

	e1 := f.signed(t, statement(100, 1, strings.Repeat("aa", 32), anchorStatePending, 1))
	if err := appendAnchorEntry(f.dir, 0, e1); err != nil {
		t.Fatal(err)
	}
	if seq, err = nextAnchorSeq(f.dir); err != nil || seq != 2 {
		t.Fatalf("after one append seq = %d, want 2 (err=%v)", seq, err)
	}

	// A refused append must NOT consume a seq.
	conflict := f.signed(t, statement(100, 1, strings.Repeat("bb", 32), anchorStatePending, 1))
	before := f.bytes(t)
	if err := appendAnchorEntry(f.dir, 0, conflict); err == nil {
		t.Fatal("same seq + different payload must be refused")
	}
	if !bytes.Equal(f.bytes(t), before) {
		t.Fatal("a refused append must not touch the file")
	}
	if seq, err = nextAnchorSeq(f.dir); err != nil || seq != 2 {
		t.Fatalf("a refused append consumed a seq: next = %d, want 2 (err=%v)", seq, err)
	}

	// A successful state advance on the same seq does not consume one either.
	advance := e1
	advance.State = anchorStateAnchored
	advance.Attempts = 1
	advance.AckID = "ack-1"
	if err := appendAnchorEntry(f.dir, 0, advance); err != nil {
		t.Fatal(err)
	}
	if seq, err = nextAnchorSeq(f.dir); err != nil || seq != 2 {
		t.Fatalf("a state advance must not consume a seq: next = %d, want 2 (err=%v)", seq, err)
	}
}

// ---------------------------------------------------------------------------
// T148 — same seq + same payload is a state ADVANCE, and the last line of the
// group wins (ADR-053 §1.2 / I5).
// ---------------------------------------------------------------------------
func TestAnchorStateAdvanceIsNotARewrite(t *testing.T) {
	f := newAnchorFixture(t)
	e1 := f.signed(t, statement(100, 7, strings.Repeat("aa", 32), anchorStatePending, 1))
	if err := appendAnchorEntry(f.dir, 0, e1); err != nil {
		t.Fatal(err)
	}
	before := f.bytes(t)

	anchored := e1
	anchored.State = anchorStateAnchored
	anchored.Attempts = 2
	anchored.AckID = "ack-7-2"
	anchored.AnchoredAt = anchorTS(7).UTC().Format(time.RFC3339Nano)
	if err := appendAnchorEntry(f.dir, 0, anchored); err != nil {
		t.Fatal(err)
	}

	after := f.bytes(t)
	if !bytes.HasPrefix(after, before) {
		t.Fatalf("the state advance rewrote existing bytes:\nbefore %q\nafter %q", before, after)
	}
	st, err := loadAnchorState(f.dir, f.trust)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.latest[7].State; got != anchorStateAnchored {
		t.Fatalf("latest state = %q, want anchored (last line of the group wins)", got)
	}
	if st.window.Entries != 1 {
		t.Fatalf("window entries = %d, want 1 (one seq, two lines)", st.window.Entries)
	}
	if v := validateAnchorState(st); len(v) != 0 {
		t.Fatalf("a legal state advance must be invariant-clean: %v", v)
	}
}

// ---------------------------------------------------------------------------
// T149 — same seq + different payload is a CONFLICT, never last-write-wins
// (ADR-053 I5).
// ---------------------------------------------------------------------------
func TestAnchorSameSeqDifferentPayloadIsConflict(t *testing.T) {
	f := newAnchorFixture(t)
	e1 := f.signed(t, statement(100, 7, strings.Repeat("aa", 32), anchorStatePending, 1))
	if err := appendAnchorEntry(f.dir, 0, e1); err != nil {
		t.Fatal(err)
	}
	before := f.bytes(t)

	other := f.signed(t, statement(100, 7, strings.Repeat("bb", 32), anchorStateAnchored, 1))
	err := appendAnchorEntry(f.dir, 0, other)
	if err == nil {
		t.Fatal("same seq + different payload must be refused")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("error must name the conflict, got %q", err.Error())
	}
	if !bytes.Equal(f.bytes(t), before) {
		t.Fatalf("a refused append mutated the file:\n got %q\nwant %q", f.bytes(t), before)
	}

	// A hand-written conflicting pair is reported, not silently resolved.
	raw, merr := serializeAnchorEntryBytes(&other)
	if merr != nil {
		t.Fatal(merr)
	}
	if err := os.WriteFile(f.path(), append(before, raw...), 0o644); err != nil {
		t.Fatal(err)
	}
	st, lerr := loadAnchorState(f.dir, f.trust)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(st.conflicts) != 1 || st.conflicts[0] != 7 {
		t.Fatalf("conflicts = %v, want [7]", st.conflicts)
	}
	if _, kept := st.latest[7]; kept {
		t.Fatal("a conflicted seq must not be usable as a statement")
	}
	if v := validateAnchorState(st); len(v) == 0 {
		t.Fatal("a conflicted log must fail the invariant self-check")
	}
}

// ---------------------------------------------------------------------------
// T150 — the evidence window reports the surviving contiguous run (R40-1).
// ---------------------------------------------------------------------------
func TestAnchorWindowTracksSurvivingRun(t *testing.T) {
	f := newAnchorFixture(t)
	for i := 1; i <= 3; i++ {
		e := f.signed(t, statement(int64(100+i), int64(i), strings.Repeat("aa", 32), anchorStateAnchored, 1))
		if err := appendAnchorEntry(f.dir, 0, e); err != nil {
			t.Fatal(err)
		}
	}
	st, err := loadAnchorState(f.dir, f.trust)
	if err != nil {
		t.Fatal(err)
	}
	if st.window.MinSeq != 1 || st.window.MaxSeq != 3 || st.window.Entries != 3 || !st.window.Continuous {
		t.Fatalf("window = %+v, want [1,3] entries=3 continuous", st.window)
	}

	// A legal prefix compaction moves the lower bound and keeps continuity.
	e4 := f.signed(t, statement(104, 4, strings.Repeat("aa", 32), anchorStateAnchored, 1))
	if err := appendAnchorEntry(f.dir, 2, e4); err != nil {
		t.Fatal(err)
	}
	st, err = loadAnchorState(f.dir, f.trust)
	if err != nil {
		t.Fatal(err)
	}
	if st.window.MinSeq != 3 || st.window.MaxSeq != 4 || st.window.Entries != 2 || !st.window.Continuous {
		t.Fatalf("after compaction window = %+v, want [3,4] entries=2 continuous", st.window)
	}
}

// ---------------------------------------------------------------------------
// T151 — a gap inside the surviving run is detected: legal operations (true
// append + whole-group prefix compaction) can never produce one (ADR-053 I4).
// ---------------------------------------------------------------------------
func TestAnchorWindowDiscontinuityIsDetected(t *testing.T) {
	f := newAnchorFixture(t)
	for i := 1; i <= 3; i++ {
		e := f.signed(t, statement(int64(100+i), int64(i), strings.Repeat("aa", 32), anchorStateAnchored, 1))
		if err := appendAnchorEntry(f.dir, 0, e); err != nil {
			t.Fatal(err)
		}
	}
	// Remove the middle group the way an attacker would (never a legal op).
	lines := strings.Split(strings.TrimRight(string(f.bytes(t)), "\n"), "\n")
	kept := append([]string{}, lines[0], lines[2])
	if err := os.WriteFile(f.path(), []byte(strings.Join(kept, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := loadAnchorState(f.dir, f.trust)
	if err != nil {
		t.Fatal(err)
	}
	if st.window.Continuous {
		t.Fatalf("window = %+v, want continuous=false (seq 2 is missing)", st.window)
	}
	if v := validateAnchorState(st); len(v) == 0 {
		t.Fatal("a gapped log must fail the invariant self-check")
	}
}

// ---------------------------------------------------------------------------
// T152 (T123b anchor leg) — an unclassifiable line makes the ANCHOR refuse to
// append and leave the file byte-identical: both consumers share the branch.
// ---------------------------------------------------------------------------
func TestAnchorRefusesAppendWhenUnclassifiablePresent(t *testing.T) {
	f := newAnchorFixture(t)
	e1 := f.signed(t, statement(100, 1, strings.Repeat("aa", 32), anchorStatePending, 1))
	if err := appendAnchorEntry(f.dir, 0, e1); err != nil {
		t.Fatal(err)
	}
	poisoned := append(f.bytes(t), []byte("{ this-is-not-json\n")...)
	if err := os.WriteFile(f.path(), poisoned, 0o644); err != nil {
		t.Fatal(err)
	}

	e2 := f.signed(t, statement(101, 2, strings.Repeat("bb", 32), anchorStatePending, 1))
	err := appendAnchorEntry(f.dir, 0, e2)
	if err == nil {
		t.Fatal("the anchor must refuse to append while an unclassifiable line is present")
	}
	if !strings.Contains(err.Error(), "unclassifiable") {
		t.Fatalf("error must name the reason, got %q", err.Error())
	}
	if !bytes.Equal(f.bytes(t), poisoned) {
		t.Fatalf("a refused append mutated the anchor log:\n got %q\nwant %q", f.bytes(t), poisoned)
	}
	// The refusal must also block evaluation (no skip-and-continue).
	if _, lerr := loadAnchorState(f.dir, f.trust); lerr == nil {
		t.Fatal("load must fail closed on an unclassifiable line")
	}
}

// ---------------------------------------------------------------------------
// T153 — [R40-5] stream_id is derived from the ABSOLUTE export directory, so a
// moved/renamed directory is a different stream. The old entries become
// `foreign_stream` — unverifiable, and NEVER tamper evidence.
// ---------------------------------------------------------------------------
func TestAnchorStreamIDBindsTheExportDirectory(t *testing.T) {
	f := newAnchorFixture(t)
	e := f.signed(t, statement(100, 1, strings.Repeat("aa", 32), anchorStateAnchored, 1))
	if err := appendAnchorEntry(f.dir, 0, e); err != nil {
		t.Fatal(err)
	}

	// Same directory ⇒ same stream.
	id1, err := deriveAnchorIdentity(f.signer, f.trust, f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if id1.StreamID == "" || id1.StreamID != e.StreamID {
		t.Fatalf("derived stream id = %q, entry carries %q", id1.StreamID, e.StreamID)
	}
	st, err := loadAnchorState(f.dir, f.trust)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.unusable) != 0 {
		t.Fatalf("entries must be usable in their own directory: %+v", st.unusable)
	}

	// A different directory ⇒ a different stream, and never `signature_invalid`.
	other := t.TempDir()
	if v := verifyAnchorEntrySignature(&e, f.trust, other); v.Verdict != sigVerdictForeignStream {
		t.Fatalf("verdict in a foreign directory = %q, want %q (never tamper evidence)", v.Verdict, sigVerdictForeignStream)
	}
	if st.window.Entries != 1 {
		t.Fatalf("the entry still occupies its seq: window = %+v", st.window)
	}
}

// ---------------------------------------------------------------------------
// Scheduler-level fixture: a real scheduler whose witness is the offline dev
// backend, plus a scripted transport for the failure paths.
// ---------------------------------------------------------------------------

// scriptedAnchorTransport replays one fixed response and records every request.
type scriptedAnchorTransport struct {
	status int
	body   string
	err    error
	reqs   []anchorRequest
}

func (t *scriptedAnchorTransport) deliver(ctx context.Context, req anchorRequest) (int, []byte, error) {
	t.reqs = append(t.reqs, req)
	if t.err != nil {
		return 0, nil, t.err
	}
	return t.status, []byte(t.body), nil
}

func newAnchorChainFixture(t *testing.T, capacity, maxAttempts int) (*chainFixture, string) {
	t.Helper()
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
		SignKeyPath: privPath, TrustKeyPaths: []string{pubPath},
		AnchorEndpoint: "file://" + witDir, AnchorMaxAttempts: maxAttempts, AnchorCapacity: capacity,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &chainFixture{snapDir: snapDir, sched: s, store: st, privPath: privPath, pubPath: pubPath}, witDir
}

// witnessSequence reads what the dev witness actually received — the exact input
// shape the reconcile surface is fed.
func witnessSequence(t *testing.T, witDir string) []witnessItem {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(witDir, witnessFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []witnessItem
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var w witnessItem
		if err := json.Unmarshal([]byte(ln), &w); err != nil {
			t.Fatalf("witness line is not readable: %v (%s)", err, ln)
		}
		out = append(out, w)
	}
	return out
}

// ---------------------------------------------------------------------------
// T125 — anchoring OFF by default: no file, no transport, no new output
// (ADR-053 I9).
// ---------------------------------------------------------------------------
func TestAnchorDisabledLeavesNoTrace(t *testing.T) {
	f := newChainFixture(t) // no AnchorEndpoint
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	if _, err := os.Stat(filepath.Join(f.snapDir, chainAnchorFile)); !os.IsNotExist(err) {
		t.Fatalf("no anchor log may exist when the endpoint is empty (stat err=%v)", err)
	}
	st := f.sched.Status()
	if st.AnchorEnabled || st.AnchorError != "" || st.AnchorWindow != nil || st.AnchoredCount != 0 {
		t.Fatalf("a disabled anchor must leave the status untouched: enabled=%v err=%q window=%+v", st.AnchorEnabled, st.AnchorError, st.AnchorWindow)
	}
	// The ledger (Phase 39) still works exactly as before.
	if ids := f.ledgerIDs(t); len(ids) != 2 {
		t.Fatalf("ledger ids = %v, want 2 entries (Phase 39 unaffected)", ids)
	}
}

// ---------------------------------------------------------------------------
// T126 — a confirmed delivery yields `anchored`, and seqs are monotonic.
// ---------------------------------------------------------------------------
func TestAnchorConfirmedDeliveryIsAnchored(t *testing.T) {
	f, witDir := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	st, err := loadAnchorState(f.snapDir, mustTrust(t, f.pubPath))
	if err != nil {
		t.Fatal(err)
	}
	seqs := st.seqs()
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("anchor seqs = %v, want [1 2] (monotonic)", seqs)
	}
	for _, seq := range seqs {
		if got := st.latest[seq].State; got != anchorStateAnchored {
			t.Fatalf("seq %d state = %q, want anchored", seq, got)
		}
		if st.latest[seq].AckID == "" {
			t.Fatalf("seq %d has no ack_id", seq)
		}
	}
	seq := witnessSequence(t, witDir)
	if len(seq) != 2 {
		t.Fatalf("witness received %d requests, want 2", len(seq))
	}
	// The witness never had to trust us: it received a self-contained statement.
	if seq[0].KeyID == "" || seq[0].StreamID == "" || seq[0].AnchorDigest == "" || seq[0].Sig == "" {
		t.Fatalf("witness request is not self-contained: %+v", seq[0])
	}
}

// ---------------------------------------------------------------------------
// T127 — a 5xx leaves the entry `pending`, the publication is NOT rolled back,
// and the queue keeps the record for the next tick.
// ---------------------------------------------------------------------------
func TestAnchorFailureKeepsPendingAndNeverRollsBack(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.sched.anchorTransport = &scriptedAnchorTransport{status: 503, body: `{}`}

	manifestsBefore := countManifests(t, f.snapDir)
	f.tick(t, at(0), 1, 10)

	trust := mustTrust(t, f.pubPath)
	st, err := loadAnchorState(f.snapDir, trust)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.seqs()) != 1 {
		t.Fatalf("anchor seqs = %v, want exactly one queued record", st.seqs())
	}
	if got := st.latest[1].State; got != anchorStatePending {
		t.Fatalf("state = %q, want pending", got)
	}
	if got := st.latest[1].Attempts; got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	if status := f.sched.Status(); status.AnchorError == "" {
		t.Fatal("a failed delivery must be surfaced as anchor_error")
	}
	// No rollback: the manifest AND the ledger entry survive.
	if after := countManifests(t, f.snapDir); after != manifestsBefore+1 {
		t.Fatalf("manifests = %d, want %d (the publication must not be rolled back)", after, manifestsBefore+1)
	}
	if ids := f.ledgerIDs(t); len(ids) != 1 {
		t.Fatalf("ledger ids = %v, want 1 (ledger append precedes anchoring)", ids)
	}
}

// ---------------------------------------------------------------------------
// T128 — capacity exhausted with an unconfirmed oldest group: the new entry is
// recorded `unanchored` and the oldest group is NOT evicted (ADR-053 §4.4).
// ---------------------------------------------------------------------------
func TestAnchorCapacityFullNeverEvictsUnconfirmed(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 2, 8)
	f.sched.anchorTransport = &scriptedAnchorTransport{status: 503, body: `{}`}

	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20) // two pending groups: capacity reached
	f.tick(t, at(2), 21, 30) // third: must NOT evict seq 1

	trust := mustTrust(t, f.pubPath)
	st, err := loadAnchorState(f.snapDir, trust)
	if err != nil {
		t.Fatal(err)
	}
	seqs := st.seqs()
	if len(seqs) != 3 || seqs[0] != 1 {
		t.Fatalf("anchor seqs = %v, want [1 2 3] — seq 1 must not be evicted to make room", seqs)
	}
	if got := st.latest[3].State; got != anchorStateUnanchored {
		t.Fatalf("the overflow entry state = %q, want unanchored", got)
	}
	if status := f.sched.Status(); status.AnchorError == "" || !strings.Contains(status.AnchorError, "capacity") {
		t.Fatalf("anchor_error = %q, want a capacity diagnostic", status.AnchorError)
	}
}

// ---------------------------------------------------------------------------
// T129 — re-delivering an id the witness already holds is idempotent.
// ---------------------------------------------------------------------------
func TestAnchorDuplicateWitnessIsIdempotent(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)

	// The witness now holds it; a re-delivery answers 409 duplicate.
	f.sched.anchorTransport = &scriptedAnchorTransport{status: 409, body: `{"reason":"duplicate","ack_id":"dup-1"}`}
	if derr := f.sched.dispatchAnchor(context.Background(), mustLatest(t, f.snapDir, f.pubPath, 1)); derr != nil {
		t.Fatalf("a duplicate must be treated as confirmed, got %v", derr)
	}
	st, err := loadAnchorState(f.snapDir, mustTrust(t, f.pubPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.seqs()) != 1 {
		t.Fatalf("anchor seqs = %v, want 1 (a duplicate adds no record)", st.seqs())
	}
	if got := st.latest[1].State; got != anchorStateAnchored {
		t.Fatalf("state = %q, want anchored", got)
	}
}

// ---------------------------------------------------------------------------
// T130 — a witness conflict is NEVER resolved by overwriting: nothing more is
// recorded and the problem is surfaced (ADR-052 A4/A5).
// ---------------------------------------------------------------------------
func TestAnchorWitnessConflictIsNotOverwritten(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)

	before := mustAnchorBytes(t, f.snapDir)
	f.sched.anchorTransport = &scriptedAnchorTransport{status: 409, body: `{"reason":"conflict"}`}
	if derr := f.sched.dispatchAnchor(context.Background(), mustLatest(t, f.snapDir, f.pubPath, 1)); derr == nil {
		t.Fatal("a witness conflict must be reported, not swallowed")
	}
	if !bytes.Equal(mustAnchorBytes(t, f.snapDir), before) {
		t.Fatal("a conflict must not append a new statement")
	}
	st, err := loadAnchorState(f.snapDir, mustTrust(t, f.pubPath))
	if err != nil {
		t.Fatal(err)
	}
	if got := st.latest[1].State; got != anchorStateAnchored {
		t.Fatalf("state = %q, want the previously confirmed state (no overwrite)", got)
	}
}

// ---------------------------------------------------------------------------
// T131 — an unclassifiable line in the anchor log blocks recording and leaves
// the file byte-identical; the problem is surfaced (ADR-053 I2).
// ---------------------------------------------------------------------------
func TestAnchorUnclassifiableLineBlocksRecording(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)

	path := filepath.Join(f.snapDir, chainAnchorFile)
	poisoned := append(mustAnchorBytes(t, f.snapDir), []byte("{ this-is-not-json\n")...)
	if err := os.WriteFile(path, poisoned, 0o644); err != nil {
		t.Fatal(err)
	}

	f.tick(t, at(1), 11, 20)

	if !bytes.Equal(mustAnchorBytes(t, f.snapDir), poisoned) {
		t.Fatalf("recording mutated a poisoned anchor log:\n got %q\nwant %q", mustAnchorBytes(t, f.snapDir), poisoned)
	}
	if status := f.sched.Status(); status.AnchorError == "" {
		t.Fatal("a blocked recording must be surfaced as anchor_error")
	}
	// The publication itself still happened (no rollback).
	if ids := f.ledgerIDs(t); len(ids) != 2 {
		t.Fatalf("ledger ids = %v, want 2 — anchoring never blocks publishing", ids)
	}
}

// ---------------------------------------------------------------------------
// T132 — ordering: publish → ledger → anchor. The manifest is the only success
// point; an anchor failure cannot undo it (ADR-053 I6).
// ---------------------------------------------------------------------------
func TestAnchorRunsAfterPublishAndLedger(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.sched.anchorTransport = &scriptedAnchorTransport{err: errors.New("witness unreachable")}

	f.tick(t, at(0), 1, 10)

	// 1. the manifest is published …
	if n := countManifests(t, f.snapDir); n != 1 {
		t.Fatalf("manifests = %d, want 1", n)
	}
	// 2. … the ledger got its entry …
	if ids := f.ledgerIDs(t); len(ids) != 1 {
		t.Fatalf("ledger ids = %v, want 1", ids)
	}
	// 3. … and only then the anchor record exists (pending, unconfirmed).
	st, err := loadAnchorState(f.snapDir, mustTrust(t, f.pubPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.seqs()) != 1 || st.latest[1].State != anchorStatePending {
		t.Fatalf("anchor state = %+v, want one pending record", st.latest)
	}
	// Nothing was rolled back: the snapshot files are still there.
	if n := len(f.snapFiles(t)); n == 0 {
		t.Fatal("the published snapshot files must survive an anchor failure")
	}
}

// ---------------------------------------------------------------------------
// T154 (R40-6 / I10) — at the moment of dispatch the `pending` line for that
// seq is ALREADY durable. Without this, a crash between dispatch and recording
// would present a confirmed witness as `witness_ahead_of_window`.
// ---------------------------------------------------------------------------
func TestAnchorPendingIsDurableBeforeDispatch(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.sched.anchorTransport = &scriptedAnchorTransport{status: 200, body: `{"ack_id":"a-1"}`}

	var seen int
	f.sched.beforeAnchorDispatch = func(dir string, seq int64) {
		seen++
		st, err := loadAnchorState(dir, mustTrust(t, f.pubPath))
		if err != nil {
			t.Fatalf("at dispatch time the anchor log must be loadable: %v", err)
		}
		e, ok := st.latest[seq]
		if !ok {
			t.Fatalf("at dispatch time seq %d is not on disk yet — the pending line must be durable first", seq)
		}
		if e.State != anchorStatePending {
			t.Fatalf("at dispatch time seq %d state = %q, want pending", seq, e.State)
		}
	}
	f.tick(t, at(0), 1, 10)
	if seen != 1 {
		t.Fatalf("dispatch happened %d times, want 1", seen)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustTrust(t *testing.T, pubPath string) *exportTrustStore {
	t.Helper()
	trust, err := newExportTrustStore([]string{pubPath})
	if err != nil {
		t.Fatal(err)
	}
	return trust
}

func mustAnchorBytes(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, chainAnchorFile))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustLatest(t *testing.T, dir, pubPath string, seq int64) anchorEntry {
	t.Helper()
	st, err := loadAnchorState(dir, mustTrust(t, pubPath))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := st.latest[seq]
	if !ok {
		t.Fatalf("anchor seq %d is absent", seq)
	}
	return e
}

func countManifests(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".manifest.json") {
			n++
		}
	}
	return n
}

func (f *chainFixture) snapFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.snapDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}
