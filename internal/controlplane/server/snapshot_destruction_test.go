package server

// Phase 43 — Evidence Destruction Accountability tests (T200~T221).
//
// The discriminating cases all have the same shape: build a destruction log that
// does or does not account for a piece of evidence, delete the evidence, and
// show that the log — not the silence around it — decides the verdict. T213 is
// the core red case; T217 is the R43-1 discriminator (a record signed by the
// EXPORT key is not an authorization and must not wash a disappearance away).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type destructionFixture struct {
	dir      string
	keyDir   string
	signer   *exportSigner     // the P37 manifest signing key (the ADVERSARY's key in T217)
	trust    *exportTrustStore // P37 trust anchor
	ka       *keyAuthority     // the key authority (KAK) — a DIFFERENT key
	streamID string
}

func newDestructionFixture(t *testing.T) *destructionFixture {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "export")
	keyDir := filepath.Join(root, "keys")
	for _, d := range []string{dir, keyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	privPath, pubPath, _, pub := genKeyPair(t, keyDir, "signer")
	kakPrivPath, kakPubPath, _, _ := genKeyPair(t, keyDir, "kak")
	signer, err := newExportSigner(privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	trust, err := newExportTrustStore([]string{pubPath})
	if err != nil {
		t.Fatal(err)
	}
	kakSigner, err := newExportSigner(kakPrivPath, "")
	if err != nil {
		t.Fatal(err)
	}
	kakTrust, err := newExportTrustStore([]string{kakPubPath})
	if err != nil {
		t.Fatal(err)
	}
	if signer.keyID == kakSigner.keyID {
		t.Fatal("fixture keys must differ (G3)")
	}
	sid, err := streamIDForPublicKey(pub, dir)
	if err != nil {
		t.Fatal(err)
	}
	return &destructionFixture{
		dir:      dir,
		keyDir:   keyDir,
		signer:   signer,
		trust:    trust,
		ka:       &keyAuthority{signer: kakSigner, trust: kakTrust},
		streamID: sid,
	}
}

func (f *destructionFixture) cfg() destructionConfig {
	return destructionConfig{dir: f.dir, capacity: 0, ka: f.ka, streamID: f.streamID, on: true}
}

var (
	dsT0 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dsT1 = time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	dsT2 = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
)

func pubTargets(ids ...int64) []destructionTarget {
	out := make([]destructionTarget, 0, len(ids))
	for _, id := range ids {
		out = append(out, destructionTarget{PublicationID: id, ManifestDigest: "digest-" + strconv.FormatInt(id, 10)})
	}
	return out
}

func (f *destructionFixture) recordOne(c destructionConfig, t *testing.T, id int64, at time.Time) *destructionEntry {
	t.Helper()
	intent, err := beginDestruction(c, destructionKindSnapshotRetention, "capacity=1", nil, pubTargets(id), 1, at)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := completeDestruction(c, intent.DestructionSeq, destructionStateCompleted, at.Add(time.Minute)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	return intent
}

// rawLines reads the log back as physical lines.
func (f *destructionFixture) rawLines(t *testing.T) [][]byte {
	t.Helper()
	return readDestructionLines(t, f.dir)
}

func readDestructionLines(t *testing.T, dir string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(destructionLogPath(dir))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var out [][]byte
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, []byte(strings.TrimSpace(l)))
	}
	return out
}

func (f *destructionFixture) entryAt(t *testing.T, i int) destructionEntry {
	t.Helper()
	var e destructionEntry
	if err := json.Unmarshal(f.rawLines(t)[i], &e); err != nil {
		t.Fatalf("unmarshal line %d: %v", i, err)
	}
	return e
}

// dropGroup simulates a legal prefix compaction of whole groups.
func dropGroup(t *testing.T, dir string, group int64) {
	t.Helper()
	lines, ok, err := readLogLines(destructionLogPath(dir), destructionGroupOf)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("unexpected unclassifiable line")
	}
	var kept []logLine
	for _, l := range lines {
		if l.group != group {
			kept = append(kept, l)
		}
	}
	if err := rewriteLogLines(destructionLogPath(dir), kept); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// T200 — disabled ⇒ zero regression: no file, no record, no assertion.
// ---------------------------------------------------------------------------
func TestDestructionDisabledCreatesNothing(T *testing.T) {
	f := newDestructionFixture(T)
	off := destructionConfig{dir: f.dir, ka: f.ka, streamID: f.streamID} // on=false
	if off.enabled() || off.writable() {
		T.Fatal("a config with the switch off must be neither enabled nor writable")
	}
	if _, err := beginDestruction(off, destructionKindSnapshotRetention, "retain=1", nil, pubTargets(1), 1, dsT0); err == nil {
		T.Fatal("recording with the switch off must fail")
	}
	if !destructionLedgerAbsent(f.dir) {
		T.Fatal("a disabled Phase 43 must not create destruction-log.jsonl")
	}
	st, err := loadDestructionState(off)
	if err != nil || st == nil || len(st.groups) != 0 {
		T.Fatalf("absent log must load empty: %v", err)
	}
	if !destructionLedgerAbsent(f.dir) {
		T.Fatal("loading must never create the file")
	}
}

// ---------------------------------------------------------------------------
// T201 / T218 — the legal advance: same payload, same signature bytes, and the
// two lines are still distinguishable ON THE CHAIN (I8 + I9).
// ---------------------------------------------------------------------------
func TestDestructionAdvanceReusesSignatureButDiffersOnChain(T *testing.T) {
	f := newDestructionFixture(T)
	c := f.cfg()
	f.recordOne(c, T, 7, dsT0)

	st, err := loadDestructionState(c)
	if err != nil {
		T.Fatal(err)
	}
	g := st.bySeq[1]
	if g == nil || g.state != destructionStateCompleted || g.conflict {
		T.Fatalf("group state=%v conflict=%v; want completed", g.state, g.conflict)
	}
	lines := f.rawLines(T)
	if len(lines) != 2 {
		T.Fatalf("want 2 physical lines, got %d", len(lines))
	}
	a, b := f.entryAt(T, 0), f.entryAt(T, 1)
	if a.EntryDigest != b.EntryDigest {
		T.Fatal("I8: the advance must reuse the intended line's entry_digest")
	}
	if a.Signature == nil || b.Signature == nil || a.Signature.Sig != b.Signature.Sig {
		T.Fatal("I8: the advance must reuse the intended line's signature bytes verbatim")
	}
	if a.State != destructionStateIntended || b.State != destructionStateCompleted {
		T.Fatalf("states: %q → %q", a.State, b.State)
	}
	ldA, _ := destructionLineDigest(&a)
	ldB, _ := destructionLineDigest(&b)
	if ldA == ldB {
		T.Fatal("I9: line_digest must carry `state`, or deleting the completed line is invisible")
	}
	// The group's incoming pointer is part of the payload, so both lines of a
	// group MUST carry the same one — that is what keeps the payloads identical
	// and the signature reusable (I8 / I1').
	if b.PrevDigest != a.PrevDigest {
		T.Fatal("the advance must carry the group's incoming prev_digest")
	}
	if ldB == ldA {
		T.Fatal("I9: the two lines must differ on the chain")
	}
	// The NEXT group chains to the last line of this one.
	next, err := beginDestruction(c, destructionKindSnapshotRetention, "capacity=1", nil, pubTargets(8), 1, dsT1)
	if err != nil {
		T.Fatal(err)
	}
	if next.PrevDigest != ldB {
		T.Fatal("the next group must chain to the previous group's LAST line digest")
	}
}

// ---------------------------------------------------------------------------
// T219 — same seq, different payload ⇒ conflict (I1').
// ---------------------------------------------------------------------------
func TestDestructionSecondLineWithDifferentPayloadIsConflict(T *testing.T) {
	f := newDestructionFixture(T)
	c := f.cfg()
	intent, err := beginDestruction(c, destructionKindSnapshotRetention, "retain=1", nil, pubTargets(7), 1, dsT0)
	if err != nil {
		T.Fatal(err)
	}
	bad := *intent
	bad.Targets = pubTargets(8)
	bad.State = destructionStateCompleted
	if err := appendDestructionEntry(c, &bad, dsT1); err == nil {
		T.Fatal("I1': a second line with a different payload must be refused")
	}
	st, err := loadDestructionState(c)
	if err != nil {
		T.Fatal(err)
	}
	if g := st.bySeq[intent.DestructionSeq]; g.state != destructionStateIntended {
		T.Fatalf("the group must stay at intended, got %q", g.state)
	}
}

// ---------------------------------------------------------------------------
// T220 — a terminal state is terminal: a third line is refused even with the
// same payload (I8).
// ---------------------------------------------------------------------------
func TestDestructionTerminalStateRefusesThirdLine(T *testing.T) {
	f := newDestructionFixture(T)
	c := f.cfg()
	f.recordOne(c, T, 7, dsT0)
	var second destructionEntry
	if err := json.Unmarshal(f.rawLines(T)[1], &second); err != nil {
		T.Fatal(err)
	}
	third := second
	third.State = destructionStateCompleted
	if err := appendDestructionEntry(c, &third, dsT2); err == nil {
		T.Fatal("I8: a third line after a terminal state must be refused")
	}
}

// ---------------------------------------------------------------------------
// T202 — I2: appending to a log we cannot evaluate is refused, never laundered.
// ---------------------------------------------------------------------------
func TestDestructionRefusesAppendWhenLogUnverifiable(T *testing.T) {
	f := newDestructionFixture(T)
	forge := &destructionEntry{
		V: 1, DestructionSeq: 1, Kind: destructionKindSnapshotRetention,
		Targets: pubTargets(42), DestroyedCount: 1, Policy: "retain=1",
		RecordedAt: dsT1.UTC().Format(time.RFC3339Nano), AuthorityKeyID: f.signer.keyID,
		StreamID: f.streamID, State: destructionStateIntended,
	}
	dg, _ := destructionEntryDigest(forge)
	forge.EntryDigest = dg
	if serr := f.signer.signDestructionEntry(forge, dsT1); serr != nil { // the EXPORT key, not the KAK
		T.Fatal(serr)
	}
	raw, _ := serializeDestructionEntryBytes(forge)
	if aerr := appendLogLine(destructionLogPath(f.dir), raw); aerr != nil {
		T.Fatal(aerr)
	}

	c := f.cfg()
	if _, err := beginDestruction(c, destructionKindSnapshotRetention, "retain=1", nil, pubTargets(7), 1, dsT1); err == nil {
		T.Fatal("I2: appending to a log holding an unauthorized entry must be refused")
	}
	st, err := loadDestructionState(c)
	if err != nil {
		T.Fatal(err)
	}
	// R43-4 (review finding 2): an unauthorized entry is QUARANTINED per group —
	// the on-disk order is intact, so the log stays order-verifiable and every
	// OTHER group keeps counting. Global silence would forfeit T217's payoff.
	if !st.verifiable {
		T.Fatalf("an unauthorized entry must not poison the log's order: %v", st.errs)
	}
	g := st.bySeq[1]
	if g == nil || g.usable || g.verdict != destructionVerdictUnauthorized {
		T.Fatalf("the forged group must be quarantined as %s, got %+v", destructionVerdictUnauthorized, g)
	}
	if !strings.Contains(strings.Join(st.errs, ";"), sigVerdictKeyUnknown) {
		T.Fatalf("the reason must name the KAK trust failure: %v", st.errs)
	}
}

// ---------------------------------------------------------------------------
// T203 — I3: after a LEGAL prefix compaction the first surviving line's prev
// pointer is exempt; otherwise every legal compaction self-inflicts a break.
// ---------------------------------------------------------------------------
func TestDestructionFirstSurvivingLineIsExemptFromPrevCheck(T *testing.T) {
	f := newDestructionFixture(T)
	c := f.cfg()
	f.recordOne(c, T, 1, dsT0)
	f.recordOne(c, T, 2, dsT1)
	dropGroup(T, f.dir, 1)

	st, err := loadDestructionState(c)
	if err != nil {
		T.Fatal(err)
	}
	if !st.verifiable {
		T.Fatalf("I3: a legal prefix compaction must not read as tampering: %v", st.errs)
	}
	if st.window.MinSeq != 2 {
		T.Fatalf("window min=%d, want 2", st.window.MinSeq)
	}
}

// ---------------------------------------------------------------------------
// T204 — I4: a hole in the surviving seq run forbids any assertion.
// ---------------------------------------------------------------------------
func TestDestructionWindowDiscontinuousForbidsAssertion(T *testing.T) {
	f := newDestructionFixture(T)
	c := f.cfg()
	f.recordOne(c, T, 1, dsT0)
	f.recordOne(c, T, 2, dsT1)
	f.recordOne(c, T, 3, dsT2)
	dropGroup(T, f.dir, 2) // a hole, which prefix compaction can never produce

	st, err := loadDestructionState(c)
	if err != nil {
		T.Fatal(err)
	}
	if st.window.Continuous {
		T.Fatal("the surviving run must be reported as discontinuous")
	}
	if st.verifiable {
		T.Fatal("a discontinuous window must make the log non-verifiable")
	}
	if !strings.Contains(strings.Join(st.errs, ";"), "window_discontinuous") {
		T.Fatalf("expected a window_discontinuous reason: %v", st.errs)
	}
}

// ---------------------------------------------------------------------------
// T205 — I5: the log accounts for its OWN compaction and the recursion stops.
// ---------------------------------------------------------------------------
func TestDestructionSelfCompactionIsRecordedAndTerminates(T *testing.T) {
	f := newDestructionFixture(T)
	c := f.cfg()
	c.capacity = 1
	f.recordOne(c, T, 1, dsT0)
	f.recordOne(c, T, 2, dsT1)

	st, err := loadDestructionState(c)
	if err != nil {
		T.Fatal(err)
	}
	found := false
	for _, g := range st.groups {
		if g.entry.Kind == destructionKindSelfCompaction {
			found = true
			if g.state != destructionStateCompleted {
				T.Fatalf("self_compaction group must be closed, got %q", g.state)
			}
			if len(g.entry.Targets) != 1 || g.entry.Targets[0].PrefixDigest == "" {
				T.Fatal("the dropped prefix must be committed by digest")
			}
		}
	}
	if !found {
		T.Fatal("I5: the log's own compaction must be recorded as self_compaction")
	}
	if len(st.groups) > c.capacity {
		T.Fatalf("capacity is not being honoured: %d groups (cap %d)", len(st.groups), c.capacity)
	}
}

// ---------------------------------------------------------------------------
// T207~T210 — every compaction path is observed: the hook fires and records.
// ---------------------------------------------------------------------------
func TestDestructionObserverRecordsCompactionOfEveryLog(T *testing.T) {
	f := newDestructionFixture(T)
	sched := f.scheduler(T, true)
	for _, kind := range []string{
		destructionKindLedgerCompaction,
		destructionKindAnchorCompaction,
		destructionKindKeyLifecycleCompaction,
		destructionKindVerificationCompaction,
	} {
		obs := sched.destructionObserver(kind)
		if obs == nil {
			T.Fatalf("%s: the observer must be installed when the Phase is on", kind)
		}
		raw := []byte(`{"report_seq":9}`)
		complete, err := obs("some-log.jsonl", []int64{9}, []logLine{{raw: raw, group: 9, classified: true}})
		if err != nil {
			T.Fatalf("%s: observe failed: %v", kind, err)
		}
		if complete == nil {
			T.Fatalf("%s: a completion callback is required", kind)
		}
		if err := complete(); err != nil {
			T.Fatalf("%s: completion failed: %v", kind, err)
		}
	}
	st, err := loadDestructionState(sched.destructionConfig())
	if err != nil {
		T.Fatal(err)
	}
	seen := map[string]bool{}
	for _, g := range st.groups {
		seen[g.entry.Kind] = true
		if g.state != destructionStateCompleted {
			T.Fatalf("%s: group %d is %q, want completed", g.entry.Kind, g.seq, g.state)
		}
	}
	for _, kind := range []string{
		destructionKindLedgerCompaction,
		destructionKindAnchorCompaction,
		destructionKindKeyLifecycleCompaction,
		destructionKindVerificationCompaction,
	} {
		if !seen[kind] {
			T.Fatalf("%s: no record was written", kind)
		}
	}
}

// When the Phase cannot write, no observer is installed, so the four compaction
// paths behave exactly as they did in Phase 42 (default zero regression).
func TestDestructionObserverAbsentWhenNotWritable(T *testing.T) {
	f := newDestructionFixture(T)
	sched := f.scheduler(T, true)
	if sched.destructionObserver(destructionKindLedgerCompaction) == nil {
		T.Fatal("an enabled Phase must install an observer")
	}
	sched.cfg.DestructionLog = false
	if sched.destructionObserver(destructionKindLedgerCompaction) != nil {
		T.Fatal("a disabled Phase must install no observer")
	}
}

// ---------------------------------------------------------------------------
// T206 / T211 — the retention path records, and a legal prune is `accounted`
// (the green case that proves the Phase does not cry wolf).
// ---------------------------------------------------------------------------
func TestDestructionPruneIsRecordedAndAccounted(T *testing.T) {
	f := newDestructionFixture(T)
	sched := f.scheduler(T, true)
	sched.publishMany(T, 3)

	sched.cfg.Retain = 1 // ⇒ two units are destroyed by prune
	if err := sched.prune(); err != nil {
		T.Fatalf("prune: %v", err)
	}
	st, err := loadDestructionState(sched.destructionConfig())
	if err != nil {
		T.Fatal(err)
	}
	recorded := 0
	for _, g := range st.groups {
		if g.entry.Kind == destructionKindSnapshotRetention {
			recorded++
			if g.state != destructionStateCompleted {
				T.Fatalf("group %d is %q, want completed", g.seq, g.state)
			}
		}
	}
	if recorded != 2 {
		T.Fatalf("want 2 retention records (one per destroyed unit), got %d", recorded)
	}
	view, err := sched.DestructionView()
	if err != nil {
		T.Fatal(err)
	}
	if view.Verdict == destructionVerdictUnaccounted || len(view.Unaccounted) != 0 {
		T.Fatalf("T211: a legal prune must be accounted, got verdict=%q unaccounted=%v", view.Verdict, view.Unaccounted)
	}
	if len(view.Missing) == 0 {
		T.Fatal("the pruned publications must appear as missing-but-recorded")
	}
	for _, id := range view.Missing {
		if !containsInt(view.Accounted, id) {
			T.Fatalf("publication %d is missing but not accounted", id)
		}
	}
}

// ---------------------------------------------------------------------------
// T213 ★ — the core red case: evidence is deleted with NO record. The ledger
// still remembers the publication, which is precisely why the earlier Phases
// hold the knowledge but cannot turn it into an assertion.
// ---------------------------------------------------------------------------
func TestDestructionUnaccountedDisappearanceIsAsserted(T *testing.T) {
	f := newDestructionFixture(T)
	sched := f.scheduler(T, true)
	ids := sched.publishMany(T, 2)

	// One legal destruction first, so the log exists and is non-empty.
	sched.cfg.Retain = 1
	if err := sched.prune(); err != nil {
		T.Fatalf("prune: %v", err)
	}
	sched.cfg.Retain = 0

	victim := ids[len(ids)-1]
	// The ledger still knows the victim (that knowledge is what P38/P39 hold but
	// cannot accuse with).
	ls, err := loadLedgerState(sched.cfg.Dir, sched.trust)
	if err != nil {
		T.Fatal(err)
	}
	if _, ok := ls.usable[victim]; !ok {
		T.Fatalf("fixture: the ledger must still remember publication %d", victim)
	}
	// The attack: delete the publication itself, with no record.
	sched.removePublication(T, victim)

	view, err := sched.DestructionView()
	if err != nil {
		T.Fatal(err)
	}
	if view.Verdict != destructionVerdictUnaccounted {
		T.Fatalf("T213: verdict=%q missing=%v unaccounted=%v — the core assertion is missing",
			view.Verdict, view.Missing, view.Unaccounted)
	}
	if !containsInt(view.Unaccounted, victim) {
		T.Fatalf("T213: the victim %d must be named as unaccounted (unaccounted=%v)", victim, view.Unaccounted)
	}
	// Cross-check (ADR-061 §5): the same attack must stay INVISIBLE to P35~P42 —
	// the chain dimension must NOT read as broken (chain_ok with a ledger, or
	// chain_absent when no chain-bearing manifest exists — both assert nothing
	// about the deletion), which is precisely why P43's assertion is NEW
	// information and not a restatement of an earlier Phase's verdict.
	_, chain := mustVerifyDetailed(T, sched)
	if chain.Verdict == "chain_broken" {
		T.Fatalf("T213 cross-check: P38/P39 must stay non-asserting under this attack, got %+v", chain)
	}
}

// ---------------------------------------------------------------------------
// T216 — deleting the log yields `destruction_absent`, never `accounted`.
// ---------------------------------------------------------------------------
func TestDestructionAbsentIsNeverBeautified(T *testing.T) {
	f := newDestructionFixture(T)
	sched := f.scheduler(T, true)
	sched.publishMany(T, 2)
	sched.cfg.Retain = 1
	if err := sched.prune(); err != nil {
		T.Fatal(err)
	}
	if err := os.Remove(destructionLogPath(sched.cfg.Dir)); err != nil {
		T.Fatal(err)
	}
	view, err := sched.DestructionView()
	if err != nil {
		T.Fatal(err)
	}
	if !view.Absent || view.Verdict != destructionVerdictAbsent {
		T.Fatalf("T216: want destruction_absent, got %q (absent=%v)", view.Verdict, view.Absent)
	}
}

// ---------------------------------------------------------------------------
// T217 ★ — the R43-1 discriminator: a forged record signed by the EXPORT key is
// unauthorized, so it does NOT wash the disappearance away.
// ---------------------------------------------------------------------------
func TestDestructionForgedWithExportKeyIsUnauthorized(T *testing.T) {
	f := newDestructionFixture(T)
	sched := f.scheduler(T, true)
	ids := sched.publishMany(T, 2)
	sched.cfg.Retain = 1
	if err := sched.prune(); err != nil {
		T.Fatal(err)
	}
	sched.cfg.Retain = 0

	victim := ids[len(ids)-1]
	sched.removePublication(T, victim)

	c := sched.destructionConfig()
	forged := sched.forgeDestructionWithExportKey(T, victim) // signed by the EXPORT key
	if forged == nil {
		T.Fatal("forge failed")
	}
	if v := verifyDestructionEntrySignature(forged, c.ka.trust); v.Verdict == sigVerdictOK {
		T.Fatal("a record signed by the export key must not verify against the KAK trust anchor")
	}
	// The forged intent sits in the log (the attacker wrote it directly). The
	// WRITE path refuses to advance it — I2: building on a line the KAK cannot
	// vouch for would launder the forgery into the operator's history.
	if err := completeDestruction(c, forged.DestructionSeq, destructionStateCompleted, dsT2); err == nil {
		T.Fatal("I2: the write path must refuse to advance a group the KAK cannot verify")
	}
	st, err := loadDestructionState(c)
	if err != nil {
		T.Fatal(err)
	}
	// R43-4 (review finding 2): the forgery is QUARANTINED, not global silence —
	// the on-disk order is intact, so the log stays order-verifiable.
	if !st.verifiable {
		T.Fatalf("a forged entry must not poison the log's order: %v", st.errs)
	}
	if g := st.bySeq[forged.DestructionSeq]; g == nil || g.usable || g.verdict != destructionVerdictUnauthorized {
		T.Fatalf("the forged group must be quarantined as %s, got %+v", destructionVerdictUnauthorized, g)
	}
	view, err := sched.DestructionView()
	if err != nil {
		T.Fatal(err)
	}
	// T217's PAYOFF (ADR-061 §5/§11): the forged `accounted` claim is refused,
	// so the victim REMAINS an unaccounted disappearance.
	if !containsInt(view.Unaccounted, victim) {
		T.Fatalf("T217: victim %d must stay unaccounted (unaccounted=%v, problems=%d) — the forgery must not wash it", victim, view.Unaccounted, len(view.Problems))
	}
	found := false
	for _, p := range view.Problems {
		if p.DestructionSeq == forged.DestructionSeq && p.Verdict == destructionVerdictUnauthorized {
			found = true
		}
	}
	if !found {
		T.Fatalf("T217: the forged group must surface as %s in problems, got %+v", destructionVerdictUnauthorized, view.Problems)
	}
}

// ---------------------------------------------------------------------------
// T215 — a record inserted INTO the chain breaks the line chain.
// ---------------------------------------------------------------------------
func TestDestructionInsertedRecordBreaksTheChain(T *testing.T) {
	f := newDestructionFixture(T)
	c := f.cfg()
	f.recordOne(c, T, 1, dsT0)
	f.recordOne(c, T, 2, dsT1)

	lines := f.rawLines(T)
	var first destructionEntry
	if err := json.Unmarshal(lines[0], &first); err != nil {
		T.Fatal(err)
	}
	lda, _ := destructionLineDigest(&first)
	extra := first
	extra.DestructionSeq = 99
	extra.PrevDigest = lda
	extra.RecordedAt = dsT1.UTC().Format(time.RFC3339Nano)
	extra.State = destructionStateIntended
	dg, _ := destructionEntryDigest(&extra)
	extra.EntryDigest = dg
	if serr := c.ka.signer.signDestructionEntry(&extra, dsT1); serr != nil {
		T.Fatal(serr)
	}
	raw, _ := serializeDestructionEntryBytes(&extra)
	var out []string
	out = append(out, string(lines[0]), strings.TrimSpace(string(raw)))
	for _, l := range lines[1:] {
		out = append(out, string(l))
	}
	if err := os.WriteFile(destructionLogPath(c.dir), []byte(strings.Join(out, "\n")+"\n"), 0o600); err != nil {
		T.Fatal(err)
	}
	st, err := loadDestructionState(c)
	if err != nil {
		T.Fatal(err)
	}
	if st.verifiable {
		T.Fatal("a record inserted into the chain must be detected")
	}
	if !strings.Contains(strings.Join(st.errs, ";"), "chain") {
		T.Fatalf("expected a chain break reason: %v", st.errs)
	}
}

// ---------------------------------------------------------------------------
// T212 — I4 at the view level: with a discontinuous window the view reports
// `destruction_window_discontinuous` and asserts no disappearance.
// ---------------------------------------------------------------------------
func TestDestructionViewRefusesAssertionOnDiscontinuousWindow(T *testing.T) {
	f := newDestructionFixture(T)
	sched := f.scheduler(T, true)
	ids := sched.publishMany(T, 4)
	sched.cfg.Retain = 1
	if err := sched.prune(); err != nil {
		T.Fatal(err)
	}
	sched.cfg.Retain = 0
	sched.removePublication(T, ids[0])
	dropGroup(T, sched.cfg.Dir, 2) // a hole the log itself can never produce

	view, err := sched.DestructionView()
	if err != nil {
		T.Fatal(err)
	}
	if !view.WindowDiscontinuous {
		T.Fatal("the view must report a discontinuous window")
	}
	if view.Verdict != destructionVerdictWindowDiscontinuous {
		T.Fatalf("I4: verdict=%q, want %q", view.Verdict, destructionVerdictWindowDiscontinuous)
	}
	if len(view.Unaccounted) != 0 {
		T.Fatalf("I4: nothing may be asserted on a discontinuous window, got %v", view.Unaccounted)
	}
}

// ---------------------------------------------------------------------------
// T221 — the fourth anchor family must not change one byte of the first three.
// ---------------------------------------------------------------------------
func TestDestructionAnchorFamilyKeepsEarlierFamiliesByteIdentical(T *testing.T) {
	pub := anchorEntry{
		PublicationID: 7, AnchorSeq: 1, ManifestDigest: "d", PrevPublicationID: 0,
		RecordedAt: "t", KeyID: "k", StreamID: "s", State: anchorStatePending, Attempts: 0,
	}
	got, err := json.Marshal(&pub)
	if err != nil {
		T.Fatal(err)
	}
	want := `{"publication_id":7,"anchor_seq":1,"manifest_digest":"d","prev_publication_id":0,"recorded_at":"t","key_id":"k","stream_id":"s","state":"pending","attempts":0}`
	if string(got) != want {
		T.Fatalf("T221: a Phase 40 entry changed bytes:\n got %s\nwant %s", got, want)
	}
	kl := anchorEntry{AnchorSeq: 2, Kind: anchorKindKeyLifecycle, EventSeq: 5, EventDigest: "e",
		EventType: lifecycleEventRevoked, NotAfter: "na", RecordedAt: "t", KeyID: "k", StreamID: "s",
		State: anchorStatePending}
	gotKL, _ := json.Marshal(&kl)
	wantKL := `{"publication_id":0,"anchor_seq":2,"manifest_digest":"","prev_publication_id":0,"recorded_at":"t","key_id":"k","stream_id":"s","kind":"key_lifecycle","event_seq":5,"event_digest":"e","event_type":"revoked","not_after":"na","state":"pending","attempts":0}`
	if string(gotKL) != wantKL {
		T.Fatalf("T221: a Phase 41 entry changed bytes:\n got %s\nwant %s", gotKL, wantKL)
	}
	de := anchorEntry{AnchorSeq: 3, Kind: anchorKindDestruction, DestructionSeq: 11, DestructionDigest: "dd",
		DestructionVerdict: destructionStateCompleted, RecordedAt: "t", KeyID: "k", StreamID: "s", State: anchorStatePending}
	if de.identityID() != 11 {
		T.Fatalf("identityID for the destruction family = %d, want 11", de.identityID())
	}
	if pub.identityID() != 7 || kl.identityID() != 5 {
		T.Fatal("identityID of the earlier families must be unchanged")
	}
}

// ---------------------------------------------------------------------------
// Status roll-up and the construction guards (G1/G2).
// ---------------------------------------------------------------------------
func TestDestructionStatusRollUpAndConstructionGuards(T *testing.T) {
	f := newDestructionFixture(T)
	off := f.scheduler(T, false)
	if st := off.Status(); st.Destruction != nil {
		T.Fatal("the status roll-up must be absent when the Phase is off")
	}
	on := f.scheduler(T, true)
	if st := on.Status(); st.Destruction == nil || !st.Destruction.Enabled {
		T.Fatal("the status roll-up must be present when the Phase is on")
	}
	// G1: the switch with no KAK private key is refused at construction.
	bad := on.cfg
	bad.DestructionLog = true
	bad.KeyAuthorityPath = ""
	if _, err := NewHistoryExportScheduler(bad); err == nil {
		T.Fatal("G1: destruction log without a KAK must be refused at construction")
	}
	// G2: the switch with no KAK trust anchor is refused at construction.
	bad2 := on.cfg
	bad2.DestructionLog = true
	bad2.KeyAuthorityTrustPaths = nil
	if _, err := NewHistoryExportScheduler(bad2); err == nil {
		T.Fatal("G2: destruction log without a KAK trust anchor must be refused at construction")
	}
}

// ---------------------------------------------------------------------------
// Scheduler fixture helpers
// ---------------------------------------------------------------------------

func (f *destructionFixture) scheduler(t *testing.T, destruction bool) *HistoryExportScheduler {
	t.Helper()
	privPath, pubPath, _, _ := genKeyPair(t, f.keyDir, "sched-signer")
	kakPrivPath, kakPubPath, _, _ := genKeyPair(t, f.keyDir, "sched-kak")
	store := &fakeExportStore{res: protection.TransitionReadResult{
		Transitions: sampleTransitions(), ExportedAt: dsT0, MinSeq: 1, MaxSeq: 3,
	}}
	cfg := HistoryExportConfig{
		Store:                  store,
		Dir:                    f.dir,
		Interval:               time.Hour,
		Formats:                []string{"json"},
		Retain:                 0,
		SignKeyPath:            privPath,
		TrustKeyPaths:          []string{pubPath},
		LedgerCapacity:         0,
		KeyAuthorityPath:       kakPrivPath,
		KeyAuthorityTrustPaths: []string{kakPubPath},
		DestructionLog:         destruction,
		DestructionCapacity:    0,
		Clock:                  func() time.Time { return dsT0 },
	}
	s, err := NewHistoryExportScheduler(cfg)
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	return s
}

// publishMany ticks the scheduler n times and returns the publication ids.
func (s *HistoryExportScheduler) publishMany(t *testing.T, n int) []int64 {
	t.Helper()
	var ids []int64
	for i := 0; i < n; i++ {
		at := dsT0.Add(time.Duration(i) * time.Hour)
		s.cfg.Store.(*fakeExportStore).res.ExportedAt = at
		s.clock = func() time.Time { return at }
		s.Tick(context.Background())
	}
	ents, _, err := s.ListManifests(100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.PublicationID != 0 {
			ids = append(ids, e.PublicationID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (s *HistoryExportScheduler) removePublication(t *testing.T, id int64) {
	t.Helper()
	entries, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		p := filepath.Join(s.cfg.Dir, e.Name())
		data, rerr := os.ReadFile(p)
		if rerr != nil || !strings.HasSuffix(e.Name(), ".manifest.json") {
			continue
		}
		var m snapshotManifest
		if jerr := json.Unmarshal(data, &m); jerr == nil && m.PublicationID == id {
			os.Remove(p)
			for _, f := range m.Formats {
				os.Remove(filepath.Join(s.cfg.Dir, f.File))
			}
		}
	}
}

// forgeDestructionWithExportKey appends a destruction record signed by the
// scheduler's EXPORT signing key — exactly the P40 adversary's capability.
func (s *HistoryExportScheduler) forgeDestructionWithExportKey(t *testing.T, ids ...int64) *destructionEntry {
	t.Helper()
	c := s.destructionConfig()
	lines, _, err := readLogLines(destructionLogPath(c.dir), destructionGroupOf)
	if err != nil {
		t.Fatal(err)
	}
	prev := ""
	maxSeq := int64(0)
	for i := range lines {
		var p destructionEntry
		if jerr := json.Unmarshal(lines[i].raw, &p); jerr != nil {
			t.Fatal(jerr)
		}
		ld, lerr := destructionLineDigest(&p)
		if lerr != nil {
			t.Fatal(lerr)
		}
		prev = ld
		if p.DestructionSeq > maxSeq {
			maxSeq = p.DestructionSeq
		}
	}
	e := &destructionEntry{
		V: 1, DestructionSeq: maxSeq + 1, Kind: destructionKindSnapshotRetention,
		Targets: pubTargets(ids...), DestroyedCount: len(ids), Policy: "retain=1",
		RecordedAt: dsT2.UTC().Format(time.RFC3339Nano), AuthorityKeyID: s.signer.keyID,
		StreamID: c.streamID, PrevDigest: prev, State: destructionStateIntended,
	}
	dg, _ := destructionEntryDigest(e)
	e.EntryDigest = dg
	if serr := s.signer.signDestructionEntry(e, dsT2); serr != nil {
		t.Fatal(serr)
	}
	raw, _ := serializeDestructionEntryBytes(e)
	if aerr := appendLogLine(destructionLogPath(c.dir), raw); aerr != nil {
		t.Fatal(aerr)
	}
	return e
}

func containsInt(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// R43-3 (review finding 7) — the observer-refusal path is load-bearing: a
// prefix whose destruction cannot be accounted for must NOT be destroyed. The
// original suite never drove it, so a regression that ignored the observer's
// error would destroy unaccounted prefixes with a green suite.
// ---------------------------------------------------------------------------
func TestDestructionObserverRefusalLeavesFileByteIdentical(T *testing.T) {
	f := newDestructionFixture(T)
	c := f.cfg()
	f.recordOne(c, T, 1, dsT0)
	f.recordOne(c, T, 2, dsT1)
	f.recordOne(c, T, 3, dsT2)
	path := destructionLogPath(f.dir)
	before, rerr := os.ReadFile(path)
	if rerr != nil {
		T.Fatal(rerr)
	}
	sawDrop := false
	err := compactLogPrefixGroupsObserved(path, 1, destructionGroupOf, func(_ string, dropped []int64, _ []logLine) (func() error, error) {
		sawDrop = len(dropped) > 0
		return nil, errors.New("bookkeeping unavailable")
	})
	if err == nil {
		T.Fatal("an observer error must refuse the compaction")
	}
	if !sawDrop {
		T.Fatal("the observer must see the drop set before refusing")
	}
	after, rerr := os.ReadFile(path)
	if rerr != nil {
		T.Fatal(rerr)
	}
	if string(after) != string(before) {
		T.Fatal("a refused compaction must leave the file byte-identical")
	}
	// The same call without a refusal proceeds — the observer is
	// one-directional: it sees and may veto, it never decides WHAT is dropped.
	if err := compactLogPrefixGroupsObserved(path, 1, destructionGroupOf, nil); err != nil {
		T.Fatalf("compaction without an observer must proceed unchanged: %v", err)
	}
}

// ---------------------------------------------------------------------------
// R43-3 (review finding 7) — the observers must be wired for REAL: a
// capacity-triggered ledger compaction must land a ledger_compaction group in
// the destruction log with destroyed_count = actual dropped lines. Calling the
// observer closure directly (as the earlier test does) cannot catch a mis-wired
// or forgotten observe field.
// ---------------------------------------------------------------------------
func TestDestructionRealLedgerCompactionIsAccountedEndToEnd(T *testing.T) {
	f := newDestructionFixture(T)
	sched := f.scheduler(T, true)
	sched.cfg.LedgerCapacity = 1
	sched.publishMany(T, 3)

	st, err := loadDestructionState(sched.destructionConfig())
	if err != nil {
		T.Fatal(err)
	}
	var g *destructionGroup
	for _, grp := range st.groups {
		if grp.entry.Kind == destructionKindLedgerCompaction {
			g = grp
		}
	}
	if g == nil {
		T.Fatal("a real capacity-triggered ledger compaction must be recorded in the destruction log")
	}
	if !g.usable || g.state != destructionStateCompleted {
		T.Fatalf("the destruction group must be usable and completed, got usable=%v state=%q", g.usable, g.state)
	}
	if g.entry.DestroyedCount <= 0 {
		T.Fatalf("destroyed_count must record the actual dropped line count, got %d", g.entry.DestroyedCount)
	}
	if len(g.entry.Targets) != 1 || g.entry.Targets[0].PrefixDigest == "" {
		T.Fatalf("the log-face target must carry the prefix digest, got %+v", g.entry.Targets)
	}
}
