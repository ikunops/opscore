package server

// Phase 41 — Signing Key Lifecycle tests (T155~T179).
//
// The discriminating cases here all follow the same shape: build a ledger that
// states a fact, then show that a signature on one side of the boundary is held
// and one on the other side is DOWNGRADED — and, crucially, that a ledger we
// cannot fully see never produces an assertion at all.

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type lifecycleFixture struct {
	dir      string
	signer   *exportSigner     // the P37 manifest signing key (the SUBJECT)
	trust    *exportTrustStore // P37 trust anchor (who)
	ka       *keyAuthority     // the key authority (KAK) — a DIFFERENT key
	streamID string
}

// testSignerWithSeed builds a deterministic Ed25519 signer. Phase 41 needs two
// INDEPENDENT keys, so the seed is part of the helper's contract.
func testSignerWithSeed(t *testing.T, seed byte) *exportSigner {
	t.Helper()
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = seed * 13
	}
	priv := ed25519.NewKeyFromSeed(s)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("test key is not Ed25519")
	}
	return &exportSigner{priv: priv, keyID: keyIDForPublicKey(pub)}
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	dir := t.TempDir()
	signer := testSignerWithSeed(t, 3)
	pub, _ := signer.priv.Public().(ed25519.PublicKey)
	kak := testSignerWithSeed(t, 7)
	kakPub, _ := kak.priv.Public().(ed25519.PublicKey)
	if signer.keyID == kak.keyID {
		t.Fatal("fixture keys must differ")
	}
	sid, err := streamIDForPublicKey(pub, dir)
	if err != nil {
		t.Fatal(err)
	}
	return &lifecycleFixture{
		dir:      dir,
		signer:   signer,
		trust:    &exportTrustStore{keys: map[string]ed25519.PublicKey{signer.keyID: pub}},
		ka:       &keyAuthority{signer: kak, trust: &exportTrustStore{keys: map[string]ed25519.PublicKey{kak.keyID: kakPub}}},
		streamID: sid,
	}
}

func (f *lifecycleFixture) cfg() keyLifecycleConfig {
	return keyLifecycleConfig{
		dir:          f.dir,
		capacity:     0,
		ka:           f.ka,
		signingTrust: f.trust,
		streamID:     f.streamID,
	}
}

// lifecycle times: the whole Phase is about a boundary, so every test uses the
// same four points.
var (
	klT0 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)  // activation (not_before)
	klT1 = time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)  // inside the interval
	klT5 = time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC) // terminal bound (not_after)
	klT6 = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC) // after the bound
)

func klTS(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (f *lifecycleFixture) mustAppend(t *testing.T, req keyLifecycleRequest) keyLifecycleEntry {
	t.Helper()
	e, err := appendKeyLifecycleEvent(f.cfg(), req, klT0)
	if err != nil {
		t.Fatalf("append %+v: %v", req, err)
	}
	return e
}

// authorize resolves the subject key's interval and runs the Phase 41 check
// exactly as the verify surface does (P37 ok ⇒ P41).
func (f *lifecycleFixture) authorize(t *testing.T, signedAt string) (SignatureVerdict, keyAuthorization) {
	t.Helper()
	st, err := loadKeyLifecycleState(f.cfg())
	if err != nil {
		t.Fatal(err)
	}
	a := st.authorizationFor(f.signer.keyID)
	v := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: f.signer.keyID}, signedAt, a)
	return v, a
}

func (f *lifecycleFixture) activate(t *testing.T) {
	t.Helper()
	f.mustAppend(t, keyLifecycleRequest{EventType: lifecycleEventActivated, KeyID: f.signer.keyID, NotBefore: klTS(klT0)})
}

// ---------------------------------------------------------------------------
// T155 / T169 — disabled ⇒ zero regression and no file is created.
// ---------------------------------------------------------------------------
func TestKeyLifecycleDisabledCreatesNothingAndAssertsNothing(t *testing.T) {
	f := newLifecycleFixture(t)
	off := keyLifecycleConfig{dir: f.dir, signingTrust: f.trust, streamID: f.streamID} // no KAK

	if off.enabled() || off.writable() {
		t.Fatal("a config with no key authority must be neither enabled nor writable")
	}
	if _, err := appendKeyLifecycleEvent(off, keyLifecycleRequest{EventType: lifecycleEventActivated, KeyID: f.signer.keyID}, klT0); err == nil {
		t.Fatal("appending without a KAK must fail")
	}
	if !keyLifecycleLedgerAbsent(f.dir) {
		t.Fatal("a disabled Phase 41 must not create signing-key-log.jsonl")
	}
	// Evaluating an absent ledger must not fail and must not create it either.
	st, err := loadKeyLifecycleState(off)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || len(st.entries) != 0 {
		t.Fatal("absent ledger must load as empty")
	}
	if !keyLifecycleLedgerAbsent(f.dir) {
		t.Fatal("loading must never create the file")
	}

	// And the verify surface is unchanged: even an unparseable signed_at keeps
	// `signature_ok` when Phase 41 is off (R41-7 must not leak into the
	// disabled path — that would break byte-identical zero regression).
	v := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: f.signer.keyID}, "not-a-time", unboundedKeyAuthorization(f.signer.keyID))
	if v.Verdict != sigVerdictOK || v.Validity != nil {
		t.Fatalf("disabled Phase 41 must hold signature_ok and add no validity block: %+v", v)
	}
	if s := keyLifecycleSummary(off); s.Enabled {
		t.Fatal("summary must report disabled")
	}
}

// ---------------------------------------------------------------------------
// T156 / T158 — revocation and rotation PRESERVE the history before the bound.
// This is the whole point of the Phase: P37 alone could only choose between
// "forgery power stands" and "history is unverifiable".
// ---------------------------------------------------------------------------
func TestKeyLifecyclePreservesHistoryBeforeTheBound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		event    string
		wantLate string
	}{
		{"revoked", lifecycleEventRevoked, sigVerdictAfterRevocation},
		{"rotated_out", lifecycleEventRotatedOut, sigVerdictAfterRotation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.activate(t)
			f.mustAppend(t, keyLifecycleRequest{EventType: tc.event, KeyID: f.signer.keyID, NotAfter: klTS(klT5)})

			// BEFORE the bound: the signature stays fully valid. The public key
			// was never removed from the trust set, so P37 still says ok and P41
			// adds nothing.
			v, a := f.authorize(t, klTS(klT1))
			if v.Verdict != sigVerdictOK {
				t.Fatalf("a signature inside the interval must stay signature_ok: %+v", v)
			}
			if a.Source != lifecycleSourceLedger || !a.Complete {
				t.Fatalf("authorization must come from the ledger: %+v", a)
			}
			if v.Validity == nil || v.Validity.AuthorizedUntil != klTS(klT5) {
				t.Fatalf("validity must project the bound: %+v", v.Validity)
			}
			// AT/after the bound: assertable.
			v2, _ := f.authorize(t, klTS(klT6))
			if v2.Verdict != tc.wantLate {
				t.Fatalf("after the bound: got %s, want %s", v2.Verdict, tc.wantLate)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// T157 / T174 — the two after-verdicts are SEPARATE names, never folded into
// `invalid` or `key_unknown` (R41-1).
// ---------------------------------------------------------------------------
func TestKeyLifecycleAfterVerdictsAreIndependentlyNamed(t *testing.T) {
	f := newLifecycleFixture(t)
	f.activate(t)
	f.mustAppend(t, keyLifecycleRequest{EventType: lifecycleEventRotatedOut, KeyID: f.signer.keyID, NotAfter: klTS(klT5)})
	v, _ := f.authorize(t, klTS(klT6))
	if v.Verdict != sigVerdictAfterRotation {
		t.Fatalf("rotation leg: got %s, want %s", v.Verdict, sigVerdictAfterRotation)
	}
	for _, banned := range []string{sigVerdictInvalid, sigVerdictKeyUnknown} {
		if v.Verdict == banned {
			t.Fatalf("a lifecycle verdict must never be folded into %s", banned)
		}
	}

	// A second key whose revocation is recorded at the same instant yields the
	// OTHER name — the two are distinguishable from each other, not just from ok.
	g := newLifecycleFixture(t)
	g.activate(t)
	g.mustAppend(t, keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: g.signer.keyID, NotAfter: klTS(klT5)})
	v2, _ := g.authorize(t, klTS(klT6))
	if v2.Verdict != sigVerdictAfterRevocation {
		t.Fatalf("revocation leg: got %s, want %s", v2.Verdict, sigVerdictAfterRevocation)
	}
	if v.Verdict == v2.Verdict {
		t.Fatal("rotation and revocation must be distinguishable")
	}
}

// ---------------------------------------------------------------------------
// T175 — loudness lives in the STATUS: all four new verdicts are `mismatch`,
// the same tier as signature_invalid (R41-1).
// ---------------------------------------------------------------------------
func TestKeyLifecycleVerdictsDegradeToMismatch(t *testing.T) {
	for _, verdict := range []string{
		sigVerdictAfterRevocation, sigVerdictAfterRotation,
		sigVerdictBeforeActivation, sigVerdictTimeUnparseable,
	} {
		got := applySignatureVerdict(verifyStatusOK, SignatureVerdict{Verdict: verdict}, manifestSchemaVersionV4)
		if got != verifyStatusMismatch {
			t.Fatalf("%s must degrade ok → mismatch, got %s", verdict, got)
		}
		// …and it never upgrades a worse status either.
		if got := applySignatureVerdict(verifyStatusMismatch, SignatureVerdict{Verdict: verdict}, manifestSchemaVersionV4); got != verifyStatusMismatch {
			t.Fatalf("%s must not change an existing mismatch", verdict)
		}
	}
	// The frozen P37 rows are untouched.
	if got := applySignatureVerdict(verifyStatusOK, SignatureVerdict{Verdict: sigVerdictKeyUnknown}, manifestSchemaVersionV4); got != verifyStatusUnknown {
		t.Fatalf("key_unknown must stay at most unknown, got %s", got)
	}
}

// ---------------------------------------------------------------------------
// T159 — the four new verdicts are mutually exclusive and each is reachable by
// its own distinct input (a test that could not tell them apart would be
// decoration, not evidence).
// ---------------------------------------------------------------------------
func TestKeyLifecycleVerdictsAreMutuallyExclusive(t *testing.T) {
	seen := map[string]string{}
	f := newLifecycleFixture(t)
	f.activate(t)
	f.mustAppend(t, keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: f.signer.keyID, NotAfter: klTS(klT5)})

	v1, _ := f.authorize(t, klTS(klT6))
	seen[sigVerdictAfterRevocation] = v1.Verdict
	v2, _ := f.authorize(t, klTS(klT0.Add(-time.Hour))) // before activation
	seen[sigVerdictBeforeActivation] = v2.Verdict
	// An unparseable declaration is its own verdict even though the key is
	// fully bounded (R41-7).
	v3, _ := f.authorize(t, "not-a-time")
	seen[sigVerdictTimeUnparseable] = v3.Verdict
	v4, _ := f.authorize(t, klTS(klT1))
	if v4.Verdict != sigVerdictOK {
		t.Fatalf("inside the interval: got %s, want signature_ok", v4.Verdict)
	}

	for want, got := range seen {
		if got != want {
			t.Fatalf("verdict mismatch: want %s, got %s", want, got)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("expected three distinct non-ok verdicts, got %v", seen)
	}
}

// ---------------------------------------------------------------------------
// R41-7 (new) — an unparseable `signed_at` must NEVER keep `signature_ok`.
// This is the bypass the judge identified: a forger who cannot move the
// signature out of the interval can simply corrupt the clock it is measured
// against. Cheaper than every other attack in this Phase, so it has to close.
// ---------------------------------------------------------------------------
func TestKeyLifecycleUnparseableSignedAtIsAssertable(t *testing.T) {
	f := newLifecycleFixture(t)
	f.activate(t)
	f.mustAppend(t, keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: f.signer.keyID, NotAfter: klTS(klT5)})

	v, a := f.authorize(t, "not-a-time")
	if v.Verdict != sigVerdictTimeUnparseable {
		t.Fatalf("unparseable signed_at: got %s, want %s", v.Verdict, sigVerdictTimeUnparseable)
	}
	if applySignatureVerdict(verifyStatusOK, v, manifestSchemaVersionV4) != verifyStatusMismatch {
		t.Fatal("unparseable signed_at must be loud (mismatch)")
	}
	// The interval is still reported, so an auditor can see WHY it is not ok.
	if v.Validity == nil || v.Validity.Source != lifecycleSourceLedger {
		t.Fatalf("validity must still be projected: %+v", v.Validity)
	}
	if !a.Complete {
		t.Fatal("the authorization itself is fine — only the time declaration is defective")
	}
}

// ---------------------------------------------------------------------------
// T160 — fail-closed: an unclassifiable line blocks evaluation AND appending,
// and the file is left byte-identical.
// ---------------------------------------------------------------------------
func TestKeyLifecycleFailClosedOnUnreadableLine(t *testing.T) {
	f := newLifecycleFixture(t)
	f.activate(t)
	before, err := os.ReadFile(keyLifecycleLogPath(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyLifecycleLogPath(f.dir), append([]byte("{not json}\n"), before...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKeyLifecycleState(f.cfg()); err == nil {
		t.Fatal("an unclassifiable line must fail the load")
	}
	if _, err := appendKeyLifecycleEvent(f.cfg(), keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: f.signer.keyID, NotAfter: klTS(klT5)}, klT0); err == nil {
		t.Fatal("an unclassifiable line must refuse the append")
	}
	after, _ := os.ReadFile(keyLifecycleLogPath(f.dir))
	if string(after) != "{not json}\n"+string(before) {
		t.Fatal("a refused append must leave the file byte-identical")
	}
}

// ---------------------------------------------------------------------------
// T161 — the hash chain is verified: tampering with a middle entry's digest
// poisons the ledger instead of silently narrowing it.
// ---------------------------------------------------------------------------
func TestKeyLifecycleHashChainIsVerified(t *testing.T) {
	f := newLifecycleFixture(t)
	f.activate(t)
	f.mustAppend(t, keyLifecycleRequest{EventType: lifecycleEventRotatedOut, KeyID: f.signer.keyID, NotAfter: klTS(klT5)})

	path := keyLifecycleLogPath(f.dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"event_type":"rotated_out"`, `"event_type":"revoked"`, 1)
	if tampered == string(data) {
		t.Fatal("fixture: tamper did not apply")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := loadKeyLifecycleState(f.cfg())
	if err != nil {
		t.Fatal(err)
	}
	if st.verifiable {
		t.Fatal("a tampered entry must make the ledger unverifiable")
	}
	// …and an unverifiable ledger asserts NOTHING (it degrades to unbounded,
	// which is the honest statement, not a hidden revocation).
	a := st.authorizationFor(f.signer.keyID)
	if a.Source != lifecycleSourceUnbounded {
		t.Fatalf("unverifiable ledger must be unbounded, got %+v", a)
	}
	v := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: f.signer.keyID}, klTS(klT6), a)
	if v.Verdict != sigVerdictOK {
		t.Fatalf("unverifiable ledger must not assert: got %s", v.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T177 — the "rotten ledger" red example (ADR-055 §12-6 / R41-4). Stated
// honestly, with no beautification: an attacker with filesystem access can
// DELETE the ledger, and every after_* verdict silently reverts to
// signature_ok. That is the accepted cost; the test pins it so nobody can
// later mistake it for coverage.
// ---------------------------------------------------------------------------
func TestKeyLifecycleDeletedLedgerRevertsToSignatureOK(t *testing.T) {
	f := newLifecycleFixture(t)
	f.activate(t)
	f.mustAppend(t, keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: f.signer.keyID, NotAfter: klTS(klT5)})
	if v, _ := f.authorize(t, klTS(klT6)); v.Verdict != sigVerdictAfterRevocation {
		t.Fatalf("precondition: want after_revocation, got %s", v.Verdict)
	}
	if err := os.Remove(keyLifecycleLogPath(f.dir)); err != nil {
		t.Fatal(err)
	}
	v, a := f.authorize(t, klTS(klT6))
	if a.Source != lifecycleSourceUnbounded {
		t.Fatalf("a deleted ledger must be unbounded, got %+v", a)
	}
	if v.Verdict != sigVerdictOK {
		t.Fatalf("KNOWN WEAKNESS must be pinned: a deleted ledger reverts to %s, want signature_ok", v.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T162 — resurrection is refused: a revoked/rotated key cannot be activated
// again, and the file is untouched.
// ---------------------------------------------------------------------------
func TestKeyLifecycleResurrectionIsRefused(t *testing.T) {
	f := newLifecycleFixture(t)
	f.activate(t)
	f.mustAppend(t, keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: f.signer.keyID, NotAfter: klTS(klT5)})
	before, _ := os.ReadFile(keyLifecycleLogPath(f.dir))

	if _, err := appendKeyLifecycleEvent(f.cfg(), keyLifecycleRequest{EventType: lifecycleEventActivated, KeyID: f.signer.keyID, NotBefore: klTS(klT6)}, klT0); err == nil {
		t.Fatal("re-activating a revoked key must be refused")
	}
	// A duplicate activation is refused by the same rule.
	if _, err := appendKeyLifecycleEvent(f.cfg(), keyLifecycleRequest{EventType: lifecycleEventActivated, KeyID: f.signer.keyID, NotBefore: klTS(klT0)}, klT0); err == nil {
		t.Fatal("a second activation must be refused")
	}
	after, _ := os.ReadFile(keyLifecycleLogPath(f.dir))
	if string(after) != string(before) {
		t.Fatal("a refused event must leave the file byte-identical")
	}

	// The first event of a key must be its activation.
	if _, err := appendKeyLifecycleEvent(f.cfg(), keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: "deadbeefdeadbeef", NotAfter: klTS(klT5)}, klT0); err == nil {
		t.Fatal("an unknown key must be refused (who is decided by P37)")
	}
}

// ---------------------------------------------------------------------------
// T163 — compaction: when the ACTIVATION is gone the lower bound is lost, and
// that is indistinguishable from "never recorded" ⇒ unbounded, never revoked.
// ---------------------------------------------------------------------------
func TestKeyLifecycleLostActivationNeverMeansRevoked(t *testing.T) {
	f := newLifecycleFixture(t)
	c := f.cfg()
	c.capacity = 1 // keep only the newest group
	f.activate(t)
	if _, err := appendKeyLifecycleEvent(c, keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: f.signer.keyID, NotAfter: klTS(klT5)}, klT0); err != nil {
		t.Fatal(err)
	}
	st, err := loadKeyLifecycleState(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.entries) != 1 || st.entries[0].EventType != lifecycleEventRevoked {
		t.Fatalf("compaction should have left only the revocation: %+v", st.entries)
	}
	a := st.authorizationFor(f.signer.keyID)
	if a.Source != lifecycleSourceUnbounded || a.NotAfter != "" {
		t.Fatalf("a lost activation must yield an unbounded key: %+v", a)
	}
	v := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: f.signer.keyID}, klTS(klT6), a)
	if v.Verdict == sigVerdictAfterRevocation {
		t.Fatal("must not assert after_revocation from an incomplete window")
	}
}

// ---------------------------------------------------------------------------
// T164 — a discontinuous window is loud and disables every assertion.
// ---------------------------------------------------------------------------
func TestKeyLifecycleDiscontinuousWindowDisablesAssertions(t *testing.T) {
	f := newLifecycleFixture(t)
	f.activate(t)
	f.mustAppend(t, keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: f.signer.keyID, NotAfter: klTS(klT5)})

	path := keyLifecycleLogPath(f.dir)
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	// Remove the FIRST event: the surviving seqs are then {2}, which is
	// contiguous — so to make a real hole we keep only the first and forge a
	// jump by re-writing the second entry's seq.
	forge := strings.Replace(lines[1], `"event_seq":2`, `"event_seq":9`, 1)
	if forge == lines[1] {
		t.Fatal("fixture: seq rewrite did not apply")
	}
	if err := os.WriteFile(path, []byte(lines[0]+"\n"+forge+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := loadKeyLifecycleState(f.cfg())
	if err != nil {
		t.Fatal(err)
	}
	if st.window.Continuous {
		t.Fatal("the window must be reported discontinuous")
	}
	if st.verifiable {
		t.Fatal("a discontinuous window must disable the ledger")
	}
	found := false
	for _, e := range st.errs {
		if strings.Contains(e, "window_discontinuous") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the reason must be surfaced, got %v", st.errs)
	}
}

// ---------------------------------------------------------------------------
// R41-6 (new) — event_seq allocation and duplicate rejection are LEDGER-ORIENTED.
// The crash window is: append(seq=N) succeeds, the caller dies before it can
// acknowledge, and it retries. A memory-only duplicate check would allocate
// seq=N again ⇒ conflict ⇒ every future event conflicts ⇒ the log is bricked.
// ---------------------------------------------------------------------------
func TestKeyLifecycleDuplicateSubmissionIsRejectedFromTheLedger(t *testing.T) {
	f := newLifecycleFixture(t)
	f.activate(t)
	c := f.cfg()

	revoked := keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: f.signer.keyID, NotAfter: klTS(klT5)}
	first, err := appendKeyLifecycleEvent(c, revoked, klT0)
	if err != nil {
		t.Fatal(err)
	}
	if first.EventSeq != 2 {
		t.Fatalf("event_seq must be derived from the ledger, got %d", first.EventSeq)
	}
	before, _ := os.ReadFile(keyLifecycleLogPath(f.dir))

	// The retry after a crash-before-ack.
	if _, err := appendKeyLifecycleEvent(c, revoked, klT0); err == nil {
		t.Fatal("a duplicate submission must be rejected by scanning the ledger")
	}
	after, _ := os.ReadFile(keyLifecycleLogPath(f.dir))
	if string(after) != string(before) {
		t.Fatal("the retry must leave the file byte-identical")
	}

	// …and the log is NOT bricked: the next distinct event takes N+1.
	next, err := appendKeyLifecycleEvent(c, keyLifecycleRequest{EventType: lifecycleEventRotatedOut, KeyID: f.signer.keyID, NotAfter: klTS(klT5.Add(time.Hour))}, klT0)
	if err != nil {
		t.Fatalf("the log must remain usable: %v", err)
	}
	if next.EventSeq != 3 {
		t.Fatalf("want event_seq 3, got %d", next.EventSeq)
	}
	// No watermark file exists: the ledger is the only allocator.
	if _, err := os.Stat(filepath.Join(f.dir, "event-seq.state")); !os.IsNotExist(err) {
		t.Fatal("R41-6 forbids a separate seq watermark file")
	}
}

// ---------------------------------------------------------------------------
// T173 — genesis must be KAK-signed, and the KAK may never BE the signing key.
// A self-signed genesis is the re-baseline attack: delete the ledger, write a
// clean genesis, and the forgery window disappears without a trace.
// ---------------------------------------------------------------------------
func TestKeyLifecycleGenesisRequiresTheAuthorityKey(t *testing.T) {
	f := newLifecycleFixture(t)

	// 1. A ledger entry signed by the SIGNING key (self-signed) is not accepted:
	//    the KAK trust anchor does not know that key.
	rogue := keyLifecycleEntry{
		V: 1, EventSeq: 1, EventType: lifecycleEventActivated, KeyID: f.signer.keyID,
		RecordedAt: klTS(klT0), AuthorityKeyID: f.signer.keyID, StreamID: f.streamID,
	}
	if err := f.signer.signKeyLifecycleEntry(&rogue, klT0); err != nil {
		t.Fatal(err)
	}
	if v := verifyKeyLifecycleEntrySignature(&rogue, f.ka.trust); v.Verdict == sigVerdictOK {
		t.Fatal("a self-signed genesis must never verify (R41-2)")
	}

	// 2. The KAK may not be the signing key: construction refuses it.
	root := t.TempDir()
	keyDir := filepath.Join(root, "keys")
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	privPath := writeRawKey(t, keyDir, "same.key", f.signer.priv)
	pubPath := writeRawPub(t, keyDir, "same.pub", f.signer.priv.Public().(ed25519.PublicKey))
	_, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store:                  &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: klT0, MinSeq: 1, MaxSeq: 3}},
		Dir:                    filepath.Join(root, "snap"),
		Interval:               time.Hour,
		Formats:                []string{"json"},
		SignKeyPath:            privPath,
		TrustKeyPaths:          []string{pubPath},
		KeyAuthorityPath:       privPath, // the SAME key
		KeyAuthorityTrustPaths: []string{pubPath},
	})
	if err == nil || !strings.Contains(err.Error(), "SAME key") {
		t.Fatalf("KAK == signing key must fail fast, got %v", err)
	}
}

func writeRawKey(t *testing.T, dir, name string, priv ed25519.PrivateKey) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, priv, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeRawPub(t *testing.T, dir, name string, pub ed25519.PublicKey) string {
	t.Helper()
	p := filepath.Join(dir, name)
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---------------------------------------------------------------------------
// T176 — the same key in another export directory (R41-3): no fan-out, so the
// revocation is invisible there. This pins the accepted cost instead of
// pretending the ledger is global.
// ---------------------------------------------------------------------------
func TestKeyLifecycleDoesNotCrossExportDirectories(t *testing.T) {
	f := newLifecycleFixture(t)
	f.activate(t)
	f.mustAppend(t, keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: f.signer.keyID, NotAfter: klTS(klT5)})

	// Same key, same KAK, different directory ⇒ different stream id.
	other := t.TempDir()
	data, err := os.ReadFile(keyLifecycleLogPath(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyLifecycleLogPath(other), data, 0o644); err != nil {
		t.Fatal(err)
	}
	otherStream, err := streamIDForPublicKey(f.signer.priv.Public().(ed25519.PublicKey), other)
	if err != nil {
		t.Fatal(err)
	}
	if otherStream == f.streamID {
		t.Fatal("fixture: the two directories must be different streams")
	}
	c := keyLifecycleConfig{dir: other, ka: f.ka, signingTrust: f.trust, streamID: otherStream}
	st, err := loadKeyLifecycleState(c)
	if err != nil {
		t.Fatal(err)
	}
	if st.verifiable {
		t.Fatal("a ledger from another export directory must not be accepted here")
	}
	a := st.authorizationFor(f.signer.keyID)
	v := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: f.signer.keyID}, klTS(klT6), a)
	if v.Verdict == sigVerdictAfterRevocation {
		t.Fatal("a foreign-directory ledger must never produce the revocation verdict here (R41-3: no fan-out)")
	}
}

// ---------------------------------------------------------------------------
// T171 / T172 — the assertions are DISCRIMINATING: remove the evidence and the
// verdict collapses. (The code-level mutations M1..M4 are run out of band;
// these two pin the data-level dependence inside the test binary.)
// ---------------------------------------------------------------------------
func TestKeyLifecycleAssertionsAreDiscriminating(t *testing.T) {
	f := newLifecycleFixture(t)
	f.activate(t)
	f.mustAppend(t, keyLifecycleRequest{EventType: lifecycleEventRevoked, KeyID: f.signer.keyID, NotAfter: klTS(klT5)})
	st, err := loadKeyLifecycleState(f.cfg())
	if err != nil {
		t.Fatal(err)
	}
	a := st.authorizationFor(f.signer.keyID)
	base := SignatureVerdict{Verdict: sigVerdictOK, KeyID: f.signer.keyID}

	// M1 (data): drop the bound ⇒ the verdict must vanish.
	m1 := a
	m1.NotAfter = ""
	if got := authorizeByLifecycle(base, klTS(klT6), m1); got.Verdict != sigVerdictOK {
		t.Fatalf("M1: without the bound the verdict must disappear, got %s", got.Verdict)
	}
	// M2 (data): drop the terminal classification ⇒ the verdict must vanish.
	m2 := a
	m2.Terminal = ""
	if got := authorizeByLifecycle(base, klTS(klT6), m2); got.Verdict != sigVerdictOK {
		t.Fatalf("M2: without the terminal event the verdict must disappear, got %s", got.Verdict)
	}
	// M3 (data): mark the window incomplete ⇒ the verdict must vanish even
	// though every field is still populated.
	m3 := a
	m3.Complete = false
	m3.Source = lifecycleSourceUnbounded
	if got := authorizeByLifecycle(base, klTS(klT6), m3); got.Verdict != sigVerdictOK {
		t.Fatalf("M3: an incomplete window must never assert, got %s", got.Verdict)
	}
	// …and with everything intact it is there.
	if got := authorizeByLifecycle(base, klTS(klT6), a); got.Verdict != sigVerdictAfterRevocation {
		t.Fatalf("intact evidence must assert, got %s", got.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T165 — anchor wiring: a lifecycle event is anchored into its OWN record
// stream, and an anchor failure never rolls the recorded fact back.
// ---------------------------------------------------------------------------
func TestKeyLifecycleAnchorWiring(t *testing.T) {
	root := t.TempDir()
	snapDir := filepath.Join(root, "snapshots")
	keyDir := filepath.Join(root, "keys")
	witnessDir := filepath.Join(root, "witness")
	for _, d := range []string{snapDir, keyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	privPath, pubPath, _, _ := genKeyPair(t, keyDir, "signer")
	kakPrivPath, kakPubPath, _, _ := genKeyPair(t, keyDir, "kak")

	s, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store:                  &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: klT0, MinSeq: 1, MaxSeq: 3}},
		Dir:                    snapDir,
		Interval:               time.Hour,
		Formats:                []string{"json"},
		SignKeyPath:            privPath,
		TrustKeyPaths:          []string{pubPath},
		AnchorEndpoint:         "file://" + filepath.ToSlash(witnessDir),
		AnchorCapacity:         0,
		KeyAuthorityPath:       kakPrivPath,
		KeyAuthorityTrustPaths: []string{kakPubPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendKeyLifecycleEvent(keyLifecycleRequest{
		EventType: lifecycleEventActivated, KeyID: s.signer.keyID, NotBefore: klTS(klT0),
	}); err != nil {
		t.Fatal(err)
	}

	// The lifecycle anchor lives in its OWN file and does not touch the
	// publication anchor stream.
	anchorPath := keyLifecycleAnchorPath(snapDir)
	if _, err := os.Stat(anchorPath); err != nil {
		t.Fatalf("the lifecycle event must be anchored: %v", err)
	}
	if _, err := os.Stat(anchorLogPath(snapDir)); !os.IsNotExist(err) {
		t.Fatal("the publication anchor log must not be created by a lifecycle event")
	}
	ast, err := loadAnchorStatePath(anchorPath, snapDir, s.trust)
	if err != nil {
		t.Fatal(err)
	}
	if len(ast.latest) != 1 {
		t.Fatalf("want exactly one anchored lifecycle event, got %d", len(ast.latest))
	}
	var only anchorEntry
	for _, e := range ast.latest {
		only = e
	}
	if only.Kind != anchorKindKeyLifecycle {
		t.Fatalf("anchor kind: got %q, want %q", only.Kind, anchorKindKeyLifecycle)
	}
	if only.State != anchorStateAnchored {
		t.Fatalf("anchor state: got %s, want anchored (%s)", only.State, only.LastError)
	}
	if only.EventSeq != 1 || only.EventDigest == "" {
		t.Fatalf("the anchor must carry the lifecycle identity: %+v", only)
	}
	// …and the ledger fact itself is recorded and visible on the status face.
	sum, err := s.KeyLifecycleStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Enabled || sum.EventCount != 1 {
		t.Fatalf("status must report one recorded event: %+v", sum)
	}
	if sum.Error != "" {
		t.Fatalf("unexpected lifecycle error: %s", sum.Error)
	}
}

// ---------------------------------------------------------------------------
// T166 — cross-dimension non-interference: with Phase 41 configured but the
// subject key unbounded, the P37 verdict set is unchanged.
// ---------------------------------------------------------------------------
func TestKeyLifecycleDoesNotDisturbPhase37Verdicts(t *testing.T) {
	f := newLifecycleFixture(t)
	// No ledger at all for this key ⇒ unbounded ⇒ no assertion.
	for _, in := range []string{sigVerdictOK, sigVerdictInvalid, sigVerdictKeyUnknown, sigVerdictMalformed, sigVerdictAbsent} {
		st, err := loadKeyLifecycleState(f.cfg())
		if err != nil {
			t.Fatal(err)
		}
		a := st.authorizationFor(f.signer.keyID)
		got := authorizeByLifecycle(SignatureVerdict{Verdict: in, KeyID: f.signer.keyID}, klTS(klT6), a)
		if got.Verdict != in {
			t.Fatalf("P41 must never change a P37 verdict: %s → %s", in, got.Verdict)
		}
	}
}
