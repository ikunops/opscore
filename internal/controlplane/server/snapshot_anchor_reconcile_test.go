package server

// ---------------------------------------------------------------------------
// Phase 40 — reconciliation: the discriminating cases.
// ---------------------------------------------------------------------------

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// witnessOf turns a local anchor entry into the witness record an honest
// witness would hold.
func witnessOf(t *testing.T, e anchorEntry) witnessItem {
	t.Helper()
	dg, err := anchorDigestOf(&e)
	if err != nil {
		t.Fatal(err)
	}
	return witnessItem{
		PublicationID: e.PublicationID,
		AnchorSeq:     e.AnchorSeq,
		AnchorDigest:  dg,
		KeyID:         e.KeyID,
		StreamID:      e.StreamID,
	}
}

// localWitnessSequence rebuilds the full witness sequence from the local log
// (i.e. what the witness received).
func localWitnessSequence(t *testing.T, f *chainFixture) []witnessItem {
	t.Helper()
	st, err := loadAnchorState(f.snapDir, mustTrust(t, f.pubPath))
	if err != nil {
		t.Fatal(err)
	}
	var seq []witnessItem
	for _, s := range st.seqs() {
		seq = append(seq, witnessOf(t, st.latest[s]))
	}
	return seq
}

// anchorLines returns the raw anchor lines (for attack simulation).
func anchorLines(t *testing.T, dir string) []string {
	t.Helper()
	data := mustAnchorBytes(t, dir)
	var out []string
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

func writeAnchorLines(t *testing.T, dir string, lines []string) {
	t.Helper()
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, chainAnchorFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// T133 — no witness sequence ⇒ `not_provided` and NO conclusion of any kind.
// ---------------------------------------------------------------------------
func TestAnchorReconcileWithoutSequenceMakesNoClaim(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	res, err := f.sched.ReconcileAnchor(nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.WitnessTrust != anchorTrustNotProvided {
		t.Fatalf("witness_trust = %q, want not_provided", res.WitnessTrust)
	}
	if len(res.OrphanWitnessIDs) != 0 || len(res.MissingWitnessIDs) != 0 || len(res.MismatchIDs) != 0 || len(res.DivergentIDs) != 0 {
		t.Fatalf("an absent sequence must produce no conclusions: %+v", res)
	}
	if res.Verdict == anchorVerdictBroken {
		t.Fatalf("verdict = %q, must not assert broken without a witness", res.Verdict)
	}
	// Disabled anchoring is `anchor_absent`, still with no conclusions.
	plain := newChainFixture(t)
	res2, err := plain.sched.ReconcileAnchor([]witnessItem{{PublicationID: 1, AnchorSeq: 1, AnchorDigest: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Verdict != anchorVerdictAbsent || res2.Enabled {
		t.Fatalf("a disabled anchor must read anchor_absent: %+v", res2)
	}
}

// ---------------------------------------------------------------------------
// T134 — a foreign / non-overlapping sequence ⇒ `untrusted`, lists empty.
// ---------------------------------------------------------------------------
func TestAnchorReconcileForeignOrNonOverlappingIsUntrusted(t *testing.T) {
	f, witDir := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)
	_ = witDir

	// (a) no overlap at all: right identity, unknown publications.
	res, err := f.sched.ReconcileAnchor([]witnessItem{
		{PublicationID: 900, AnchorSeq: 90, AnchorDigest: "aaaa", KeyID: keyIDOf(t, f.pubPath), StreamID: streamOf(t, f.pubPath, f.snapDir)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.WitnessTrust != anchorTrustUntrusted || res.TrustReason != trustReasonNoOverlap {
		t.Fatalf("trust = %q/%q, want untrusted/no_overlap", res.WitnessTrust, res.TrustReason)
	}
	if res.Verdict != anchorVerdictUnverifiable {
		t.Fatalf("verdict = %q, want anchor_unverifiable (no assertion)", res.Verdict)
	}
	if len(res.OrphanWitnessIDs) != 0 || len(res.MissingWitnessIDs) != 0 {
		t.Fatalf("an untrusted sequence must produce no lists: %+v", res)
	}

	// (b) a foreign stream: same key, different export directory.
	seq := localWitnessSequence(t, f)
	seq[0].StreamID = strings.Repeat("0", 16)
	res2, err := f.sched.ReconcileAnchor(seq)
	if err != nil {
		t.Fatal(err)
	}
	if res2.WitnessTrust != anchorTrustUntrusted || res2.TrustReason != trustReasonForeignStrm {
		t.Fatalf("trust = %q/%q, want untrusted/foreign_stream", res2.WitnessTrust, res2.TrustReason)
	}

	// (c) malformed input is a refusal, never a verdict.
	res3, err := f.sched.ReconcileAnchor([]witnessItem{{PublicationID: 0, AnchorSeq: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if res3.TrustReason != trustReasonMalformed || res3.Verdict != anchorVerdictUnverifiable {
		t.Fatalf("trust = %q verdict = %q, want malformed_sequence/unverifiable", res3.TrustReason, res3.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T135 — aligned and fully matching ⇒ anchor_ok.
// ---------------------------------------------------------------------------
func TestAnchorReconcileAlignedIsOK(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	seq := localWitnessSequence(t, f)
	res, err := f.sched.ReconcileAnchor(seq)
	if err != nil {
		t.Fatal(err)
	}
	if res.WitnessTrust != anchorTrustAligned {
		t.Fatalf("witness_trust = %q (reason %q), want aligned", res.WitnessTrust, res.TrustReason)
	}
	if res.Verdict != anchorVerdictOK {
		t.Fatalf("verdict = %q, want anchor_ok: %+v", res.Verdict, res)
	}
	if res.Window == nil || !res.Window.Continuous || res.Window.Entries != 2 {
		t.Fatalf("window = %+v, want a continuous 2-entry window", res.Window)
	}
}

// ---------------------------------------------------------------------------
// T136 — the core payoff: the witness holds a publication the local window says
// should still be here ⇒ orphan_witness ⇒ anchor_broken.
//
// The fixture is the real attack shape: the attacker owns the key, so they can
// drop one publication's record and fill its seq slot with a forged one — which
// keeps the local window CONTINUOUS. Continuity alone therefore cannot be the
// discriminator; the orphan check is.
// ---------------------------------------------------------------------------
func TestAnchorReconcileOrphanWitnessInsideWindowIsBroken(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.tick(t, at(2), 21, 30)
	seq := localWitnessSequence(t, f) // the witness holds all three

	// Attacker: replace the group at seq 2 with a forged entry for a different
	// publication (same key ⇒ it verifies locally).
	signer, err := newExportSigner(f.privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	forged := anchorEntry{
		PublicationID:  999,
		AnchorSeq:      2,
		ManifestDigest: strings.Repeat("cd", 32),
		PrevPublicationID: 1,
		RecordedAt:     at(1).UTC().Format(time.RFC3339Nano),
		State:          anchorStateAnchored,
		Attempts:       1,
		AckID:          "forged",
	}
	if err := signer.signAnchorEntry(&forged, f.snapDir, at(1)); err != nil {
		t.Fatal(err)
	}
	raw, err := serializeAnchorEntryBytes(&forged)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the WHOLE group at seq 2 (it holds one line per state advance)
	// with a single forged line for a different publication.
	lines := anchorLines(t, f.snapDir)
	out := []string{}
	inserted := false
	for _, ln := range lines {
		var e anchorEntry
		if jerr := json.Unmarshal([]byte(ln), &e); jerr != nil {
			t.Fatal(jerr)
		}
		if e.AnchorSeq == 2 {
			if !inserted {
				out = append(out, strings.TrimSpace(string(raw)))
				inserted = true
			}
			continue
		}
		out = append(out, ln)
	}
	if !inserted {
		t.Fatal("precondition: seq 2 must exist in the anchor log")
	}
	writeAnchorLines(t, f.snapDir, out)

	res, err := f.sched.ReconcileAnchor(seq)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Window.Continuous {
		t.Fatalf("fixture is invalid: the forged log must stay continuous (got %+v)", res.Window)
	}
	if len(res.OrphanWitnessIDs) != 1 || res.OrphanWitnessIDs[0] != 2 {
		t.Fatalf("orphan_witness_ids = %v, want [2]", res.OrphanWitnessIDs)
	}
	if res.Verdict != anchorVerdictBroken {
		t.Fatalf("verdict = %q, want anchor_broken", res.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T137 — a local record the witness lacks is `missing_witness`: at most
// INCOMPLETE, never broken (it is indistinguishable from a delivery in flight).
// ---------------------------------------------------------------------------
func TestAnchorMissingWitnessIsNeverBroken(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	seq := localWitnessSequence(t, f)
	seq = seq[:1] // the witness never received publication 2
	res, err := f.sched.ReconcileAnchor(seq)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MissingWitnessIDs) != 1 || res.MissingWitnessIDs[0] != 2 {
		t.Fatalf("missing_witness_ids = %v, want [2]", res.MissingWitnessIDs)
	}
	if res.Verdict == anchorVerdictBroken {
		t.Fatalf("verdict = %q: a missing witness must never be asserted as broken", res.Verdict)
	}
	if res.Verdict != anchorVerdictIncomplete {
		t.Fatalf("verdict = %q, want anchor_incomplete", res.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T138 — the same publication with a different digest ⇒ broken.
// ---------------------------------------------------------------------------
func TestAnchorDigestMismatchIsBroken(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	seq := localWitnessSequence(t, f)
	seq[1].AnchorDigest = strings.Repeat("ee", 32)
	res, err := f.sched.ReconcileAnchor(seq)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MismatchIDs) != 1 || res.MismatchIDs[0] != 2 {
		t.Fatalf("digest_mismatch_ids = %v, want [2]", res.MismatchIDs)
	}
	if res.Verdict != anchorVerdictBroken {
		t.Fatalf("verdict = %q, want anchor_broken", res.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T139 — a `pending` local record never takes part in witnessed/missing
// assertions (it was never confirmed).
// ---------------------------------------------------------------------------
func TestAnchorPendingTakesPartInNoAssertion(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.sched.anchorTransport = &scriptedAnchorTransport{status: 503, body: `{}`}
	f.tick(t, at(0), 1, 10)

	st, err := loadAnchorState(f.snapDir, mustTrust(t, f.pubPath))
	if err != nil {
		t.Fatal(err)
	}
	if st.latest[1].State != anchorStatePending {
		t.Fatalf("precondition: state = %q, want pending", st.latest[1].State)
	}
	seq := []witnessItem{witnessOf(t, st.latest[1])}

	res, err := f.sched.ReconcileAnchor(seq)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing overlaps on the ANCHORED set ⇒ no conclusion at all.
	if res.WitnessTrust != anchorTrustUntrusted || res.TrustReason != trustReasonNoOverlap {
		t.Fatalf("trust = %q/%q, want untrusted/no_overlap — a pending record proves nothing", res.WitnessTrust, res.TrustReason)
	}
	if len(res.PendingIDs) != 1 || res.PendingIDs[0] != 1 {
		t.Fatalf("pending_ids = %v, want [1] (reported, not asserted on)", res.PendingIDs)
	}
}

// ---------------------------------------------------------------------------
// T140 — the anchor dimension is ORTHOGONAL: it changes no Phase 35 status,
// no Phase 37 signature verdict, no Phase 38 chain verdict and no Phase 39
// ledger value (ADR-053 I8).
// ---------------------------------------------------------------------------
func TestAnchorDoesNotChangeOtherDimensions(t *testing.T) {
	root := t.TempDir()
	keyDir := filepath.Join(root, "keys")
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	privPath, pubPath, _, _ := genKeyPair(t, keyDir, "shared")

	build := func(anchor bool) *chainFixture {
		snapDir := filepath.Join(root, "snap-"+fmt.Sprintf("%v", anchor))
		witDir := filepath.Join(root, "wit-"+fmt.Sprintf("%v", anchor))
		for _, d := range []string{snapDir, witDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cfg := HistoryExportConfig{
			Store: &fakeExportStore{}, Dir: snapDir, Interval: time.Hour, Formats: []string{"json"},
			SignKeyPath: privPath, TrustKeyPaths: []string{pubPath},
		}
		if anchor {
			cfg.AnchorEndpoint = "file://" + witDir
		}
		s, err := NewHistoryExportScheduler(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return &chainFixture{snapDir: snapDir, sched: s, store: cfg.Store.(*fakeExportStore), privPath: privPath, pubPath: pubPath}
	}

	plain, anchored := build(false), build(true)
	for _, f := range []*chainFixture{plain, anchored} {
		f.tick(t, at(0), 1, 10)
		f.tick(t, at(1), 11, 20)
		f.tick(t, at(2), 21, 30)
	}
	a, av := mustVerifyDetailed(t, plain.sched)
	b, bv := mustVerifyDetailed(t, anchored.sched)
	ja, _ := json.Marshal(map[string]any{"results": a, "chain": av})
	jb, _ := json.Marshal(map[string]any{"results": b, "chain": bv})
	if !bytes.Equal(ja, jb) {
		t.Fatalf("the verify surface changed when anchoring was enabled:\nplain    %s\nanchored %s", ja, jb)
	}
	if len(a) == 0 {
		t.Fatal("precondition: the verify surface must have produced results")
	}
	// And the Phase 39 ledger carries the same commitments. (Byte comparison is
	// impossible here: `recorded_at` is the scheduler clock, which differs
	// between two runs — so compare the COMMITMENTS, not the timestamps.)
	idsA, idsB := plain.ledgerIDs(t), anchored.ledgerIDs(t)
	if len(idsA) != len(idsB) {
		t.Fatalf("ledger ids differ: %v vs %v", idsA, idsB)
	}
	for i := range idsA {
		if idsA[i] != idsB[i] {
			t.Fatalf("the Phase 39 ledger changed when anchoring was enabled: %v vs %v", idsA, idsB)
		}
	}
}

// ---------------------------------------------------------------------------
// T143 — [R40-1] a witness record BELOW the window is `witness_outside_window`
// and is NOT broken; a witness record INSIDE the window IS broken. One test,
// two cases, so the two are provably distinguishable.
// ---------------------------------------------------------------------------
func TestAnchorOutsideWindowIsNotBrokenButInsideIs(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 2, 8) // capacity 2 ⇒ the oldest group is compacted away
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.tick(t, at(2), 21, 30)

	st, err := loadAnchorState(f.snapDir, mustTrust(t, f.pubPath))
	if err != nil {
		t.Fatal(err)
	}
	if st.window.MinSeq != 2 {
		t.Fatalf("precondition: a legal compaction must have moved the window to [2,3], got %+v", st.window)
	}

	// The witness still holds seq 1 (it was anchored before the compaction).
	var seq []witnessItem
	for _, s := range st.seqs() {
		seq = append(seq, witnessOf(t, st.latest[s]))
	}
	stale := witnessItem{
		PublicationID: 1, AnchorSeq: 1, AnchorDigest: strings.Repeat("11", 32),
		KeyID: st.latest[2].KeyID, StreamID: st.latest[2].StreamID,
	}
	outside := append([]witnessItem{stale}, seq...)

	res, err := f.sched.ReconcileAnchor(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.OutsideWindowIDs) != 1 || res.OutsideWindowIDs[0] != 1 {
		t.Fatalf("outside_window_ids = %v, want [1]", res.OutsideWindowIDs)
	}
	if len(res.OrphanWitnessIDs) != 0 {
		t.Fatalf("orphan_witness_ids = %v, want empty (seq 1 is below the window)", res.OrphanWitnessIDs)
	}
	if res.Verdict == anchorVerdictBroken {
		t.Fatalf("verdict = %q: a witness below the window must NOT be broken (legal compaction is indistinguishable)", res.Verdict)
	}

	// Same fixture, but now a witness record whose seq is INSIDE the window and
	// whose publication the local log does not carry ⇒ orphan ⇒ broken.
	inside := append(append([]witnessItem{}, seq...), witnessItem{
		PublicationID: 777, AnchorSeq: 3, AnchorDigest: strings.Repeat("22", 32),
		KeyID: st.latest[2].KeyID, StreamID: st.latest[2].StreamID,
	})
	res2, err := f.sched.ReconcileAnchor(inside)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.OrphanWitnessIDs) != 1 || res2.OrphanWitnessIDs[0] != 777 {
		t.Fatalf("orphan_witness_ids = %v, want [777]", res2.OrphanWitnessIDs)
	}
	if res2.Verdict != anchorVerdictBroken {
		t.Fatalf("verdict = %q, want anchor_broken for an in-window orphan", res2.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T144 — [R40-2] a shared publication whose triple never matches is
// `divergent` — assertable, unlike the silent `untrusted` variants.
// ---------------------------------------------------------------------------
func TestAnchorDivergentIsAssertable(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)

	seq := localWitnessSequence(t, f)
	// Same identity, same publications, but NO triple matches: this is a real
	// disagreement, not a failure to compare.
	for i := range seq {
		seq[i].AnchorDigest = strings.Repeat("ff", 32)
	}
	res, err := f.sched.ReconcileAnchor(seq)
	if err != nil {
		t.Fatal(err)
	}
	if res.WitnessTrust != anchorTrustDivergent {
		t.Fatalf("witness_trust = %q (reason %q), want divergent", res.WitnessTrust, res.TrustReason)
	}
	if res.Verdict != anchorVerdictBroken {
		t.Fatalf("verdict = %q, want anchor_broken (divergent is assertable)", res.Verdict)
	}
	if len(res.DivergentIDs) != 2 {
		t.Fatalf("divergent_ids = %v, want both publications", res.DivergentIDs)
	}
	if res.Verdict == anchorVerdictUnverifiable {
		t.Fatal("divergent must never collapse into unverifiable")
	}

	// Contrast: unknown publications (no_overlap) stay untrusted with NO ids.
	unknown := localWitnessSequence(t, f)
	for i := range unknown {
		unknown[i].PublicationID = 5000 + int64(i)
	}
	res2, err := f.sched.ReconcileAnchor(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if res2.WitnessTrust != anchorTrustUntrusted || len(res2.DivergentIDs) != 0 {
		t.Fatalf("no_overlap must stay untrusted with empty lists: %+v", res2)
	}
}

// ---------------------------------------------------------------------------
// T145 — non-vacuousness (R207): the key-holder attack that leaves
// P35~P39 COMPLETELY GREEN is still observed by Phase 40.
//
// Attack: delete a published snapshot, repair the chain and the ledger with the
// signing key, and remove the deleted publication's anchor record. Every local
// Phase then reports success — only the witness remembers the publication.
// ---------------------------------------------------------------------------
func TestAnchorObservesWhatOlderPhasesCannotSee(t *testing.T) {
	f, _ := newAnchorChainFixture(t, 0, 8)
	f.tick(t, at(0), 1, 10)
	f.tick(t, at(1), 11, 20)
	f.tick(t, at(2), 21, 30)
	witnessSeq := localWitnessSequence(t, f)
	trust := mustTrust(t, f.pubPath)

	// --- the attack (the attacker holds the signing key) ---
	// 1. delete snapshot 2 (manifest + artifacts).
	f.drop(t, at(1))
	// 2. re-point snapshot 3 at snapshot 1 and re-sign it.
	m1 := mustLoadManifest(t, f.snapDir, "alert-transitions-"+safeTS(at(0))+".manifest.json")
	m3 := mustLoadManifest(t, f.snapDir, "alert-transitions-"+safeTS(at(2))+".manifest.json")
	dg1, err := ledgerDigestOf(m1)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := newExportSigner(f.privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	m3.Chain = &manifestChain{PrevPublicationID: m1.PublicationID, PrevManifestDigest: dg1}
	writeSignedManifest(t, f.snapDir, signer, m3)
	// 3. drop the ledger entry of publication 2 and re-commit publication 3.
	ledger := []string{}
	for _, ln := range f.ledgerLines(t) {
		var e ledgerEntry
		if jerr := json.Unmarshal([]byte(ln), &e); jerr != nil {
			continue
		}
		if e.PublicationID == 2 {
			continue
		}
		if e.PublicationID == 3 {
			e.PrevPublicationID = m1.PublicationID
			e.PrevManifestDigest = dg1
			e.ManifestDigest, err = ledgerDigestOf(m3)
			if err != nil {
				t.Fatal(err)
			}
			e.Signature = nil
			if serr := signer.signLedgerEntry(&e, at(2)); serr != nil {
				t.Fatal(serr)
			}
			raw, merr := serializeLedgerEntryBytes(&e)
			if merr != nil {
				t.Fatal(merr)
			}
			ln = strings.TrimSpace(string(raw))
		}
		ledger = append(ledger, ln)
	}
	f.writeLedgerLines(t, ledger)
	// 4. remove the deleted publication's anchor record (the attacker controls
	//    the host, but NOT the witness).
	kept := []string{}
	for _, ln := range anchorLines(t, f.snapDir) {
		var e anchorEntry
		if jerr := json.Unmarshal([]byte(ln), &e); jerr != nil {
			t.Fatal(jerr)
		}
		if e.PublicationID == 2 {
			continue
		}
		kept = append(kept, ln)
	}
	writeAnchorLines(t, f.snapDir, kept)

	// --- Phase 35~39 are all green: nothing local sees the deletion ---
	results, chain := mustVerifyDetailed(t, f.sched)
	if chain.Verdict != "chain_ok" {
		t.Fatalf("the attack must leave Phase 38 green, got %+v", chain)
	}
	for _, r := range results {
		if !strings.HasPrefix(r.Status, "ok") {
			t.Fatalf("the attack must leave Phase 35 green, got %+v", r)
		}
	}
	if st := f.sched.Status(); st.LedgerError != "" {
		t.Fatalf("the attack must leave Phase 39 green, got ledger_error=%q", st.LedgerError)
	}

	// --- Phase 40 sees it ---
	res, err := f.sched.ReconcileAnchor(witnessSeq)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != anchorVerdictBroken {
		t.Fatalf("verdict = %q: Phase 40 must observe the deletion (%+v)", res.Verdict, res)
	}
	if len(res.OrphanWitnessIDs) != 1 || res.OrphanWitnessIDs[0] != 2 {
		t.Fatalf("orphan_witness_ids = %v, want [2] (the deleted publication)", res.OrphanWitnessIDs)
	}
	_ = trust
}

// helpers for the identity fields used in hand-built witness items
func keyIDOf(t *testing.T, pubPath string) string {
	t.Helper()
	trust := mustTrust(t, pubPath)
	for id := range trust.keys {
		return id
	}
	t.Fatal("no trusted key")
	return ""
}

func streamOf(t *testing.T, pubPath, dir string) string {
	t.Helper()
	trust := mustTrust(t, pubPath)
	for _, pub := range trust.keys {
		sid, err := streamIDForPublicKey(pub, dir)
		if err != nil {
			t.Fatal(err)
		}
		return sid
	}
	t.Fatal("no trusted key")
	return ""
}
