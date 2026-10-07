package server

// Phase 50 — Verifier Authority Lifecycle tests (T340~T361; ADR-075 §5,
// ADR-076 §7).
//
// The discriminating cases all follow one shape: the SAME key signs records on
// both sides of an authorization boundary and the two records must come out
// OPPOSITE — so an implementation that only "looks for a revoked event" (time
// blind) collapses them to one value and fails. Three further properties are
// pinned in the same cases rather than asserted in prose: the P42 report and
// the P44 verdicts stay byte-identical while the NEW face says something else
// (I5), deletion of the ledger tail is caught by P49 and NOT re-implemented
// here (A8-⑤), and "the evidence is broken" (indeterminate) never merges with
// "the evidence supports no assertion" (unbounded) (I4).

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Fixture: a REAL scheduler with the manifest signer, the KAK, the VAK and
// attestation all configured, so every record under test is produced by
// production code. Forgeries and malformed rows are written as bytes on disk —
// the only way those states are reachable.
// ---------------------------------------------------------------------------

var (
	p50T0 = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)  // activation (not_before)
	p50T1 = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)  // inside the interval
	p50T2 = time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)  // also inside
	p50T5 = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC) // terminal bound (not_after)
	p50T6 = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) // after the bound
)

type p50Fixture struct {
	t      *testing.T
	root   string
	dir    string // export directory
	keyDir string
	store  *fakeExportStore
	sched  *HistoryExportScheduler
	vak    *exportSigner // the verification-report signer (A)
	kak    *exportSigner // the key authority
	mu     sync.Mutex
	now    time.Time
}

func newP50Fixture(t *testing.T, tune func(*HistoryExportConfig)) *p50Fixture {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "snapshots")
	keyDir := filepath.Join(root, "keys")
	for _, d := range []string{dir, keyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	signPriv, signPub, _, _ := genKeyPair(t, keyDir, "signer")
	kakPriv, kakPub, _, _ := genKeyPair(t, keyDir, "kak")
	vakPriv, vakPub, _, _ := genKeyPair(t, keyDir, "vak")
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: p50T0, MinSeq: 1, MaxSeq: 3}}
	f := &p50Fixture{t: t, root: root, dir: dir, keyDir: keyDir, store: st, now: p50T1}
	cfg := HistoryExportConfig{
		Store:                  st,
		Dir:                    dir,
		Interval:               time.Hour,
		Formats:                []string{"json"},
		SignKeyPath:            signPriv,
		TrustKeyPaths:          []string{signPub},
		KeyAuthorityPath:       kakPriv,
		KeyAuthorityTrustPaths: []string{kakPub},
		VerifierKeyPath:        vakPriv,
		VerifierTrustPaths:     []string{vakPub},
		VerifyAttest:           true,
		Clock:                  f.clock,
	}
	if tune != nil {
		tune(&cfg)
	}
	s, err := NewHistoryExportScheduler(cfg)
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	f.sched = s
	if vakPriv != "" {
		if f.vak, err = newExportSigner(vakPriv, ""); err != nil {
			t.Fatal(err)
		}
	}
	if f.kak, err = newExportSigner(kakPriv, ""); err != nil {
		t.Fatal(err)
	}
	_ = vakPub
	return f
}

func (f *p50Fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *p50Fixture) setNow(t time.Time) {
	f.mu.Lock()
	f.now = t
	f.mu.Unlock()
}

// publish materializes one signed snapshot at the fixture's current clock.
func (f *p50Fixture) publish(at time.Time) {
	f.t.Helper()
	f.setNow(at)
	f.store.mu.Lock()
	f.store.res = protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: at, MinSeq: 1, MaxSeq: 3}
	f.store.mu.Unlock()
	f.sched.Tick(context.Background())
}

// attest records ONE honest verification report at `at`: it is signed by the
// VAK, so its `signed_at` is `at` (production path only).
func (f *p50Fixture) attest(at time.Time) VerificationReport {
	f.t.Helper()
	f.setNow(at)
	rep, err := f.sched.AttestVerification(100)
	if err != nil {
		f.t.Fatalf("attest: %v", err)
	}
	return rep
}

func (f *p50Fixture) face() verifierAuthorityStatusSummary {
	return f.sched.VerifierAuthorityStatus()
}

func (f *p50Fixture) lifecycle(req keyLifecycleRequest) keyLifecycleEntry {
	f.t.Helper()
	e, err := f.sched.AppendKeyLifecycleEvent(req)
	if err != nil {
		f.t.Fatalf("lifecycle %+v: %v", req, err)
	}
	return e
}

func (f *p50Fixture) activate(keyID string, notBefore time.Time) {
	f.lifecycle(keyLifecycleRequest{EventType: lifecycleEventActivated, KeyID: keyID, NotBefore: klTS(notBefore)})
}

func (f *p50Fixture) terminal(keyID, event string, notAfter time.Time) {
	f.lifecycle(keyLifecycleRequest{EventType: event, KeyID: keyID, NotAfter: klTS(notAfter)})
}

func (f *p50Fixture) activateVAK(notBefore time.Time) { f.activate(f.vak.keyID, notBefore) }
func (f *p50Fixture) terminalVAK(event string, notAfter time.Time) {
	f.terminal(f.vak.keyID, event, notAfter)
}

// p50Row fetches one signer's row, fataling if it is absent.
func p50Row(t *testing.T, sum verifierAuthorityStatusSummary, keyID string) verifierAuthorityKeyStatus {
	t.Helper()
	row, ok := sum.Keys[keyID]
	if !ok {
		t.Fatalf("no row for signer %s (keys: %v)", keyID, sum.Keys)
	}
	return row
}

// p50AppendEntry writes ONE hand-built verification log row and returns it. The
// row is fully self-consistent (prev digest, entry digest, stream id) and its
// signature is a REAL Ed25519 signature over the canonical payload by
// `signingKey` — so the ONLY thing that can give it away is the signer's
// identity. `signedAt` (when non-empty) replaces the signature block's
// timestamp AFTER signing: the signature block is not covered by the signature
// or by the entry digest, which is exactly why R41-7 has to exist.
func p50AppendEntry(t *testing.T, f *p50Fixture, verifiedAt time.Time, claimedKeyID string, signingKey *exportSigner, signedAt string) verificationLogEntry {
	t.Helper()
	c := f.sched.verificationConfig()
	lines, ok, err := readLogLines(verificationLogPath(c.dir), verificationGroupOf)
	if err != nil || !ok {
		t.Fatalf("read verification log: err=%v ok=%v", err, ok)
	}
	prev := ""
	maxSeq := int64(0)
	for i := range lines {
		var p verificationLogEntry
		if jerr := json.Unmarshal(lines[i].raw, &p); jerr != nil {
			t.Fatal(jerr)
		}
		prev = p.Digest
		if p.ReportSeq > maxSeq {
			maxSeq = p.ReportSeq
		}
	}
	items := []VerificationItem{{
		PublicationID: 1,
		Status:        verifyStatusOK,
		Chain:         chainPosPredecessorVerified,
		ChainSource:   "disk+ledger",
		Overall:       verificationAttested,
	}}
	ev := DimensionAvailability{Status: true, Chain: true, ChainSource: true}
	e := verificationLogEntry{
		V:          1,
		ReportSeq:  maxSeq + 1,
		Subject:    VerificationSubject{Publications: []int64{1}, MinID: 1, MaxID: 1, Limit: verificationSubjectLimit},
		Evaluable:  ev,
		Items:      items,
		Overall:    verificationAttested,
		Reasons:    buildVerificationReasons(items, ev, ""),
		VerifiedAt: verifiedAt.UTC().Format(time.RFC3339Nano),
		StreamID:   c.streamID,
		KeyID:      claimedKeyID,
		PrevDigest: prev,
	}
	dg, derr := verificationEntryDigest(&e)
	if derr != nil {
		t.Fatal(derr)
	}
	e.Digest = dg
	if serr := signingKey.signVerificationEntry(&e, verifiedAt); serr != nil {
		t.Fatal(serr)
	}
	if signedAt != "" {
		e.Signature.SignedAt = signedAt
	}
	raw, merr := serializeVerificationEntryBytes(&e)
	if merr != nil {
		t.Fatal(merr)
	}
	if aerr := appendLogLine(verificationLogPath(c.dir), raw); aerr != nil {
		t.Fatal(aerr)
	}
	return e
}

// p50Keys returns the JSON object keys of a marshalled document, in order.
func p50Keys(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		t.Fatalf("not a JSON object: %s", raw)
	}
	var out []string
	depth := 0
	for dec.More() {
		k, kerr := dec.Token()
		if kerr != nil {
			t.Fatal(kerr)
		}
		if depth == 0 {
			out = append(out, k.(string))
		}
		// Skip the value.
		var skip json.RawMessage
		if serr := dec.Decode(&skip); serr != nil {
			t.Fatal(serr)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// T340 — frozen face (by content) + P41 ledger bytes.
// ---------------------------------------------------------------------------

func TestP50T340FrozenFaceAndLedgerBytes(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	frozenSHA256 := map[string]string{
		"internal/controlplane/server/appendonly_log.go":             "f4ae703323c3c9c49f5209f2b70f287dacc8ad61cb8ff84c1c1c9aa93fcb81ee",
		"internal/controlplane/server/snapshot_anchor.go":            "eca582354359fde49c3e2b1f6e664551dbcd31ff300a310938ea2429acdebc63",
		"internal/controlplane/server/snapshot_witness_reconcile.go": "4313822e3bdfb3b2bace3eed1643469be23611097ea728e17412c4b39200f9b2",
		"internal/controlplane/server/snapshot_signature.go":         "17d9ced5dff8b31be576a9473e5175083ce03e60f21b3cfbd12f78960d406d23",
		"internal/controlplane/server/snapshot_chain.go":             "736c29b9b6ce114ca0efeb4068586b42c32af832dad20b699aadea8c7c9a47b2",
		"internal/controlplane/server/snapshot_ledger.go":            "e9efe6fd1a7013639f0539cfb2a2940957edb9201173df005b39f2a80f50d325",
		"internal/controlplane/server/history_export_coverage.go":    "478f0ad9bd9f6189f04f198b90f2493c138ad1678a0c2aaed6364d89fe133999",
		"internal/controlplane/server/history_export_manifest.go":    "86d56214e4e69a8a33b3f4293fa72e2a3c69148e300ee1f1f8f980b335892588",
		// Phase 50 adds two more zero-diff files (ADR-076 §1): the verification
		// face (where the identity check IS NOT added — it lives in the new file,
		// so I11 does not violate A3) and the realization face.
		"internal/controlplane/server/snapshot_verification.go":      "2d35d9d3d267c1cfd025653b83a68e34cb4f9aeb8d4e3daa65931d9714202b27",
		"internal/controlplane/server/snapshot_anchor_realization.go": "6a59a5f1d3b7b32681eca8573fa80d765a5e94d39d3479388a2870a20d8a1e5a",
		"go.mod": "6dfc9eea3dcba0f32b4ef8229b1a9a912dddf6db1891ed3b6623538f987e6b32",
		"go.sum": "48a94c452c1c0b794722b90652f70a8bd6ea4a3a82f86b50b31bb0683bd9cd27",
	}
	for rel, want := range frozenSHA256 {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("frozen file %s unreadable: %v", rel, err)
		}
		sum := sha256.Sum256(src)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("T340/I1 VIOLATION: frozen file %s changed:\n got %s\nwant %s", rel, got, want)
		}
	}

	// P41 ledger byte equivalence: an already-recorded row still hashes to the
	// digest it stores, and its JSON key set is unchanged — the Phase widened WHO
	// may be given a window, not what a window looks like (A2: zero schema change).
	// Phase 51 (ADR-077 §4 A8-⑦③a, ADR-078 §6 步 7(a)): the one DECLARED addition
	// is the trailing `role` domain carrier, so the row's key set is now 12 —
	// every other key, and the digest algorithm, are untouched.
	f := newP50Fixture(t, nil)
	f.activateVAK(p50T0)
	data, err := os.ReadFile(keyLifecycleLogPath(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 lifecycle line, got %d", len(lines))
	}
	var e keyLifecycleEntry
	if jerr := json.Unmarshal([]byte(lines[0]), &e); jerr != nil {
		t.Fatal(jerr)
	}
	dg, derr := lifecycleEventDigest(&e)
	if derr != nil {
		t.Fatal(derr)
	}
	if dg != e.EventDigest {
		t.Fatalf("T340: a stored event_digest must still recompute byte-for-byte: stored %s recomputed %s", e.EventDigest, dg)
	}
	var obj map[string]json.RawMessage
	if jerr := json.Unmarshal([]byte(lines[0]), &obj); jerr != nil {
		t.Fatal(jerr)
	}
	got := make([]string, 0, len(obj))
	for k := range obj {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"authority_key_id", "event_digest", "event_seq", "event_type", "key_id", "not_before", "pubkey_fingerprint", "recorded_at", "role", "signature", "stream_id", "v"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("T340/A2: the lifecycle row's field set changed:\n got %v\nwant %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// T341 — default deployment: the group is ABSENT, and no new top-level key
// appears anywhere in the status document.
// ---------------------------------------------------------------------------

func TestP50T341DefaultDeploymentOmitsTheGroup(t *testing.T) {
	// No VAK: the deployment has no second signing identity, so there is no
	// verifier authority to judge — "not applicable", never "indeterminate".
	f := newP50Fixture(t, func(cfg *HistoryExportConfig) {
		cfg.VerifierKeyPath = ""
		cfg.VerifierTrustPaths = nil
	})
	f.publish(p50T1)
	st := f.sched.Status()
	if st.VerifierAuthority != nil {
		t.Fatal("a deployment with no verifier identity must not carry the verifier_authority group")
	}
	if st.Verification == nil {
		t.Fatal("precondition: attestation must be on so the comparison is not vacuous")
	}
	doc, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"verifier_authority", `"verifier_authorized"`, "verifier_authority_claims"} {
		if strings.Contains(string(doc), banned) {
			t.Fatalf("default deployment status document must not mention %q: %s", banned, doc)
		}
	}
	// The top-level key set is pinned: no key was added, removed or reordered by
	// Phase 50 for a deployment that has no verifier identity.
	keys := p50Keys(t, st)
	if got := strings.Join(keys, ","); got != strings.Join(p50BaselineStatusKeys(), ",") {
		t.Fatalf("the default deployment's status document changed shape:\n got %v\nwant %v", keys, p50BaselineStatusKeys())
	}

	// With a VAK the group IS there — so the omission above is a decision, not a
	// dead field. It is the ONLY added key, and `anchor_delivery` stays last
	// wherever it is present.
	g := newP50Fixture(t, nil)
	g.publish(p50T1)
	st2 := g.sched.Status()
	if st2.VerifierAuthority == nil {
		t.Fatal("a deployment with a VAK must carry the verifier_authority group")
	}
	doc2, _ := json.Marshal(st2)
	if !strings.Contains(string(doc2), `"verifier_authority"`) {
		t.Fatalf("the configured deployment must serialize the group: %s", doc2)
	}
	wantWithVAK := append(append([]string{}, p50BaselineStatusKeys()...), "verifier_authority")
	if got := p50Keys(t, st2); strings.Join(got, ",") != strings.Join(wantWithVAK, ",") {
		t.Fatalf("the group must be the ONLY added key:\n got %v\nwant %v", got, wantWithVAK)
	}
	// …and the group itself is judgeable (nothing was observed: silence, loudly).
	if st2.VerifierAuthority.State != verifierAuthorityNothingAssessed {
		t.Fatalf("no report was recorded, so the group must say nothing_assessed: %+v", st2.VerifierAuthority)
	}
}

// p50BaselineStatusKeys is the EXACT top-level key sequence a default deployment
// (attestation + lifecycle on, VAK off, anchoring off) produced before Phase 50.
// It is the byte-shape the new group must not disturb (I3/T341).
//
// Phase 51 (ADR-077 §4 A8-⑦③b/c, ADR-078 §6 步 7(d)): the lifecycle ledger's
// domain group shares the `key_lifecycle` enable gate, so an ENABLED ledger adds
// exactly one key here, immediately after the group it derives from. A
// deployment with the ledger DISABLED still has both groups absent, and the
// sequence below is therefore the enabled baseline.
func p50BaselineStatusKeys() []string {
	return []string{
		"enabled", "running", "last_run_at", "last_exported_at",
		"key_lifecycle", "key_lifecycle_domains", "verification",
		"signing_enabled", "signer_key_id", "trusted_keys",
		"skip_count", "published", "failed", "dir", "interval", "formats", "retain",
	}
}

// ---------------------------------------------------------------------------
// T342 — the core red example: revocation and preserved history are BOTH true.
// A VAK that has been revoked while it stays in the verifier trust anchor...
// ---------------------------------------------------------------------------

func TestP50T342RevocationAndPreservedHistoryCoexist(t *testing.T) {
	wd := ""
	f := newP50Fixture(t, func(cfg *HistoryExportConfig) {
		wd = filepath.Join(cfg.Dir, "..", "witness")
		if err := os.MkdirAll(wd, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg.AnchorEndpoint = "file://" + filepath.ToSlash(wd)
		cfg.AnchorMaxAttempts = 8
	})
	f.publish(p50T1)
	f.activateVAK(p50T0)
	// The offline KAK revokes the VAK at T5; the OLD VAK stays in
	// --export-verifier-trust, so every historical report still verifies (P44).
	f.terminalVAK(lifecycleEventRevoked, p50T5)

	// ② The record BEFORE the bound is NOT accused: history is preserved.
	repEarly := f.attest(p50T1)
	early := f.face()
	if early.State != verifierAuthorityAuthorized {
		t.Fatalf("a window whose only record is inside it must read authorized, got %s (%s)", early.State, early.Reason)
	}
	if !early.Authorized || early.Claims != 1 || early.Violations != 0 {
		t.Fatalf("authorized must be derived and non-vacuous: %+v", early)
	}
	rowEarly := p50Row(t, early, f.vak.keyID)
	if rowEarly.Verdict != verifierAuthorityAuthorized || rowEarly.Authorized != 1 || rowEarly.Checked != 1 {
		t.Fatalf("early row: %+v", rowEarly)
	}

	// …and on that SAME record P44 still says signature_ok: the old VAK is still
	// trusted, which is exactly why P50 has to exist.
	vs, err := loadVerificationState(f.sched.verificationConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !vs.verifiable || len(vs.entries) != 1 {
		t.Fatalf("precondition: one usable record, got verifiable=%v entries=%d", vs.verifiable, len(vs.entries))
	}
	if v := verifyVerificationEntrySignatureIn(&vs.entries[0], f.sched.verifierTrust, f.sched.verifierForeignKeys()); v.Verdict != sigVerdictOK {
		t.Fatalf("P44 must still accept the pre-revocation record: got %s", v.Verdict)
	}

	// P42's report is untouched: no P50 vocabulary, and its `lifecycle` ruler
	// still describes the MANIFEST signer (which has no window here ⇒ unbounded),
	// never the VAK (which is bounded). A Phase that wrote its new judgement into
	// the report would flip this to "bounded".
	rawReport, rerr := json.Marshal(repEarly)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(string(rawReport), "verifier_authority") || strings.Contains(string(rawReport), "after_revocation") {
		t.Fatalf("P42's report must carry no Phase 50 vocabulary: %s", rawReport)
	}
	if len(repEarly.Items) == 0 {
		t.Fatal("precondition: the report must have at least one item")
	}
	for _, it := range repEarly.Items {
		if it.Lifecycle != "unbounded" {
			t.Fatalf("P42's lifecycle dimension must still name the MANIFEST signer (unbounded here), got %q", it.Lifecycle)
		}
	}

	// ① The record AT/AFTER the bound is a violation — and the early record is
	// still counted as authorized, not as an accusation.
	f.attest(p50T6)
	late := f.face()
	if late.State != verifierAuthorityViolated {
		t.Fatalf("a record signed after revocation must read violated, got %s", late.State)
	}
	if late.Authorized || late.Violations != 1 {
		t.Fatalf("violated: authorized must be false and the violation counted once: %+v", late)
	}
	rowLate := p50Row(t, late, f.vak.keyID)
	if rowLate.Verdict != verifierAuthorityViolated {
		t.Fatalf("the row must be violated: %+v", rowLate)
	}
	if rowLate.AfterRevocation != 1 || rowLate.Authorized != 1 || rowLate.Checked != 2 {
		t.Fatalf("both records must be classified on their own side of the bound: %+v", rowLate)
	}
	if rowLate.AfterRotation != 0 {
		t.Fatal("a revocation must never be reported as a rotation")
	}

	// ③ P49 still reads `realized` for this family: P50 is a NEW mechanism, not a
	// reuse, and it does not disturb the realization face.
	rz := f.sched.AnchorRealizationStatus()
	if rz.Verification.Verdict != realizationFamilyRealized {
		t.Fatalf("P49's verification family must stay realized: %+v (%s)", rz.Verification, rz.Verification.Reason)
	}

	// …and P42/P44 are still silent about "when": the SAME log that P50 calls
	// violated is fully verifiable to them (I5's "both readings on one input").
	vs2, err := loadVerificationState(f.sched.verificationConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !vs2.verifiable || len(vs2.entries) != 2 {
		t.Fatalf("P44 must still accept both records: verifiable=%v entries=%d errs=%v", vs2.verifiable, len(vs2.entries), vs2.errs)
	}
	if v := verifyVerificationEntrySignatureIn(&vs2.entries[1], f.sched.verifierTrust, f.sched.verifierForeignKeys()); v.Verdict != sigVerdictOK {
		t.Fatalf("P44 must still read signature_ok on the violating record: got %s", v.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T343 — rotation is its OWN word (R41-1): never folded into revocation.
// ---------------------------------------------------------------------------

func TestP50T343RotationIsNotFoldedIntoRevocation(t *testing.T) {
	f := newP50Fixture(t, nil)
	f.publish(p50T1)
	f.activateVAK(p50T0)
	f.terminalVAK(lifecycleEventRotatedOut, p50T5)
	f.attest(p50T6)

	sum := f.face()
	row := p50Row(t, sum, f.vak.keyID)
	if row.AfterRotation != 1 || row.AfterRevocation != 0 {
		t.Fatalf("a rotation must be counted as a rotation: %+v", row)
	}
	if row.Verdict != verifierAuthorityViolated || sum.State != verifierAuthorityViolated {
		t.Fatalf("a record after rotation is a violation: row=%+v state=%s", row, sum.State)
	}
	if row.AfterRotation == row.AfterRevocation {
		t.Fatal("the two terminal meanings must remain distinguishable")
	}
	// An unparseable bound does not silently widen the interval: P41 refuses to
	// assert from a broken declaration (I2).
	g := newP50Fixture(t, nil)
	g.publish(p50T1)
	g.activateVAK(p50T0)
	g.terminalVAK(lifecycleEventRotatedOut, p50T5)
	g.attest(p50T6)
	st, err := loadKeyLifecycleState(g.sched.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	a := st.authorizationFor(g.vak.keyID)
	if a.Source != lifecycleSourceLedger || !a.Complete {
		t.Fatalf("precondition: the interval must be assertable: %+v", a)
	}
}

// ---------------------------------------------------------------------------
// T344 — before activation: the identity did not exist yet.
// ---------------------------------------------------------------------------

func TestP50T344BeforeActivationIsAViolation(t *testing.T) {
	f := newP50Fixture(t, nil)
	f.publish(p50T1)
	f.activateVAK(p50T5)
	f.attest(p50T1) // signed BEFORE the activation

	sum := f.face()
	row := p50Row(t, sum, f.vak.keyID)
	if row.BeforeActivation != 1 || row.Checked != 1 {
		t.Fatalf("the record must be checked and classified: %+v", row)
	}
	if row.Verdict != verifierAuthorityViolated || sum.State != verifierAuthorityViolated || sum.Authorized {
		t.Fatalf("a record before activation is a violation: row=%+v sum=%+v", row, sum)
	}
	if len(sum.ViolatingKeys) != 1 || sum.ViolatingKeys[0] != f.vak.keyID {
		t.Fatalf("the violating signer must be named: %v", sum.ViolatingKeys)
	}
	// The bound is reported, so an auditor can see WHY.
	if row.Validity == nil || row.Validity.AuthorizedFrom != klTS(p50T5) {
		t.Fatalf("the resolved interval must be projected: %+v", row.Validity)
	}
}

// ---------------------------------------------------------------------------
// T345 — unjudgeable is LOUD (I4/I7), and "unavailable" is not "unbounded"'s
// twin: no ledger / no event / lost lower bound are all `unbounded`.
// ---------------------------------------------------------------------------

func TestP50T345UnboundedIsLoudAndIsNotAuthorized(t *testing.T) {
	t.Run("lifecycle ledger not enabled", func(t *testing.T) {
		f := newP50Fixture(t, func(cfg *HistoryExportConfig) {
			cfg.KeyAuthorityPath = ""
			cfg.KeyAuthorityTrustPaths = nil
		})
		f.publish(p50T1)
		f.attest(p50T1)
		sum := f.face()
		row := p50Row(t, sum, f.vak.keyID)
		if row.Verdict != verifierAuthorityUnbounded {
			t.Fatalf("no lifecycle ledger ⇒ unbounded, got %s (%s)", row.Verdict, row.Reason)
		}
		if row.Checked != 0 || sum.Claims != 0 {
			t.Fatalf("an unjudgeable row is not a checked one: row=%+v claims=%d", row, sum.Claims)
		}
		if row.Reason == "" {
			t.Fatalf("unbounded must be loud at the row level: %+v", row)
		}
		if sum.Reason == "" {
			t.Fatalf("unbounded must be loud at the face level: %+v", sum)
		}
		if sum.State != verifierAuthorityNothingAssessed || sum.Authorized {
			t.Fatalf("a face with no assertion must be nothing_assessed: %+v", sum)
		}
		if len(sum.UnboundedKeys) != 1 {
			t.Fatalf("the unjudgeable signer must be named: %v", sum.UnboundedKeys)
		}
	})

	t.Run("key has no event at all", func(t *testing.T) {
		f := newP50Fixture(t, nil)
		f.publish(p50T1)
		// Give the MANIFEST signer a window so the ledger is not empty — the VAK
		// simply is not in it. "No event for this key" is NOT "the ledger is bad".
		f.activate(f.sched.signer.keyID, p50T0)
		f.attest(p50T1)
		sum := f.face()
		row := p50Row(t, sum, f.vak.keyID)
		if row.Verdict != verifierAuthorityUnbounded {
			t.Fatalf("a key with no event must be unbounded, got %s (%s)", row.Verdict, row.Reason)
		}
		if row.Checked != 0 {
			t.Fatalf("unbounded must never contribute a checked count: %+v", row)
		}
		if sum.State != verifierAuthorityNothingAssessed || sum.Authorized {
			t.Fatalf("nothing_assessed expected: %+v", sum)
		}
	})

	t.Run("lower bound compacted away", func(t *testing.T) {
		f := newP50Fixture(t, func(cfg *HistoryExportConfig) { cfg.KeyLifecycleCapacity = 1 })
		f.publish(p50T1)
		f.activateVAK(p50T0)
		f.terminalVAK(lifecycleEventRevoked, p50T5)
		f.attest(p50T6)
		sum := f.face()
		row := p50Row(t, sum, f.vak.keyID)
		if row.Verdict != verifierAuthorityUnbounded {
			t.Fatalf("a lost activation must be unbounded (never violated, never authorized), got %s", row.Verdict)
		}
		if row.AfterRevocation != 0 {
			t.Fatalf("the terminal fact must NOT be asserted from an incomplete window: %+v", row)
		}
	})
}

// ---------------------------------------------------------------------------
// T346 — fail-closed (A6/I8): a broken ledger is `indeterminate`, never
// `authorized`, and the neighbouring faces are untouched.
// ---------------------------------------------------------------------------

func TestP50T346FailClosedOnBrokenEvidenceAndDomainAmbiguity(t *testing.T) {
	t.Run("verification log holds an unclassifiable line", func(t *testing.T) {
		f := newP50Fixture(t, nil)
		f.publish(p50T1)
		f.activateVAK(p50T0)
		f.attest(p50T1)
		path := verificationLogPath(f.dir)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if werr := os.WriteFile(path, append([]byte("{not json}\n"), before...), 0o644); werr != nil {
			t.Fatal(werr)
		}
		sum := f.face()
		if sum.State != verifierAuthorityIndeterminate || sum.Authorized {
			t.Fatalf("an unreadable verification line is fail-closed: %+v", sum)
		}
		if sum.Reason == "" {
			t.Fatal("fail-closed must be loud")
		}
	})

	t.Run("lifecycle ledger holds an unclassifiable line", func(t *testing.T) {
		f := newP50Fixture(t, nil)
		f.publish(p50T1)
		f.activateVAK(p50T0)
		f.attest(p50T1)
		path := keyLifecycleLogPath(f.dir)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if werr := os.WriteFile(path, append([]byte("{not json}\n"), before...), 0o644); werr != nil {
			t.Fatal(werr)
		}
		sum := f.face()
		if sum.State != verifierAuthorityIndeterminate || sum.Authorized {
			t.Fatalf("a broken lifecycle ledger is fail-closed: %+v", sum)
		}
	})

	t.Run("a key sits in two trust anchors", func(t *testing.T) {
		f := newP50Fixture(t, nil)
		f.publish(p50T1)
		f.activateVAK(p50T0)
		f.attest(p50T1)
		// A directory whose configuration changed under it carries a combination
		// the construction guard (V2) refuses. The guard is a construction-time
		// fact; the ledger comes from disk — so the read face re-checks.
		f.sched.verifierTrust.keys[f.sched.signer.keyID] = f.sched.signer.priv.Public().(ed25519.PublicKey)
		sum := f.face()
		if sum.State != verifierAuthorityIndeterminate || sum.Authorized {
			t.Fatalf("domain ambiguity (A6) is fail-closed: %+v", sum)
		}
		if !strings.Contains(sum.Reason, f.sched.signer.keyID) {
			t.Fatalf("the ambiguous key must be named: %s", sum.Reason)
		}
		// I8: the other faces keep their values.
		if st := f.sched.Status(); st.Verification == nil || st.Verification.Error != "" {
			t.Fatalf("P44's face must be unaffected: %+v", st.Verification)
		}
	})

	t.Run("lifecycle ledger present but unverifiable", func(t *testing.T) {
		f := newP50Fixture(t, nil)
		f.publish(p50T1)
		f.activateVAK(p50T0)
		f.attest(p50T1)
		// Tamper with the recorded event type: the digest no longer matches.
		path := keyLifecycleLogPath(f.dir)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		tampered := strings.Replace(string(data), `"event_type":"activated"`, `"event_type":"rotated_out"`, 1)
		if tampered == string(data) {
			t.Fatal("fixture: the tamper did not apply")
		}
		if werr := os.WriteFile(path, []byte(tampered), 0o644); werr != nil {
			t.Fatal(werr)
		}
		sum := f.face()
		if sum.State != verifierAuthorityIndeterminate {
			t.Fatalf("an unverifiable lifecycle ledger is fail-closed, got %s", sum.State)
		}
	})
}

// ---------------------------------------------------------------------------
// T347 — non-vacuity (I6) and "the empty set is not true" (I9).
// ---------------------------------------------------------------------------

func TestP50T347AuthorizedIsEarnedAndTheEmptySetIsNotTrue(t *testing.T) {
	f := newP50Fixture(t, nil)
	f.publish(p50T1)
	f.activateVAK(p50T0)
	f.attest(p50T1)

	sum := f.face()
	if sum.State != verifierAuthorityAuthorized || !sum.Authorized {
		t.Fatalf("a record inside an assertable window must read authorized: %+v", sum)
	}
	if sum.Claims == 0 {
		t.Fatal("I6: authorized ⇒ claims > 0 (the judgement comes from a checked interval, not from the absence of a counter-example)")
	}
	if sum.Claims != 1 || sum.Entries != 1 {
		t.Fatalf("claims is the checked count and entries the witness count: %+v", sum)
	}
	if sum.Violations != 0 || len(sum.ViolatingKeys) != 0 {
		t.Fatalf("nothing is violated here: %+v", sum)
	}

	// An EMPTY observation set: nothing was observed ⇒ nothing is asserted, and
	// the lifecycle ledger is not even needed (I9).
	g := newP50Fixture(t, nil)
	g.activateVAK(p50T0) // the ledger exists and is perfectly verifiable
	empty := g.face()
	if empty.State != verifierAuthorityNothingAssessed || empty.Authorized {
		t.Fatalf("an empty observation set is never true: %+v", empty)
	}
	if empty.Claims != 0 || empty.Entries != 0 {
		t.Fatalf("no record may be counted: %+v", empty)
	}
	if empty.Reason == "" {
		t.Fatal("nothing_assessed must be loud")
	}
}

// ---------------------------------------------------------------------------
// T348 — no folding (A5/I5): P44's "not of this family" and P50's "out of its
// window" are different words, both visible on the same input.
// ---------------------------------------------------------------------------

func TestP50T348ForeignKeyAndOutOfWindowAreNeverFolded(t *testing.T) {
	if verificationVerdictUnauthorized == sigVerdictAfterRevocation ||
		verificationVerdictUnauthorized == verifierAuthorityViolated {
		t.Fatal("P44's family verdict must not be P50's verdict")
	}
	// ① P44's foreign track: a record signed by the KAK — a key this deployment
	// KNOWS from another anchor, which is not allowed to sign reports.
	f := newP50Fixture(t, nil)
	f.publish(p50T1)
	f.activateVAK(p50T0)
	p50AppendEntry(t, f, p50T1, f.kak.keyID, f.kak, "")
	vs, err := loadVerificationState(f.sched.verificationConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(vs.entries) != 0 {
		t.Fatal("precondition: the foreign-signed record must not be usable")
	}
	found := ""
	for _, p := range vs.problems {
		if p.Verdict == verificationVerdictUnauthorized {
			found = p.Verdict
		}
	}
	if found != verificationVerdictUnauthorized {
		t.Fatalf("P44 must name the foreign key's record %s, got %+v", verificationVerdictUnauthorized, vs.problems)
	}
	// The face never emits that word: it answers a different question.
	sumForeign := f.face()
	for k, row := range sumForeign.Keys {
		if row.Verdict == verificationVerdictUnauthorized {
			t.Fatalf("signer %s: P50 must never reuse P44's verdict name", k)
		}
	}

	// ② P50's track, on a world where P44 is completely happy: the same record is
	// `signature_ok` to P44 and `after_revocation` to P50 — both visible.
	g := newP50Fixture(t, nil)
	g.publish(p50T1)
	g.activateVAK(p50T0)
	g.terminalVAK(lifecycleEventRevoked, p50T5)
	g.attest(p50T6)
	vs2, err := loadVerificationState(g.sched.verificationConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !vs2.verifiable || len(vs2.entries) != 1 {
		t.Fatalf("P44 must accept the record: verifiable=%v errs=%v", vs2.verifiable, vs2.errs)
	}
	if v := verifyVerificationEntrySignatureIn(&vs2.entries[0], g.sched.verifierTrust, g.sched.verifierForeignKeys()); v.Verdict != sigVerdictOK {
		t.Fatalf("P44 says %s, want signature_ok", v.Verdict)
	}
	sum := g.face()
	row := p50Row(t, sum, g.vak.keyID)
	if row.AfterRevocation != 1 || row.Verdict != verifierAuthorityViolated {
		t.Fatalf("P50 must name the same record's window violation: %+v", row)
	}
	if row.Verdict == verificationVerdictUnauthorized {
		t.Fatal("the two tracks must never collapse")
	}
}

// ---------------------------------------------------------------------------
// T349 — the write-face subject set (A2/I10): signing ∪ verifier, and the two
// must not intersect.
// ---------------------------------------------------------------------------

func TestP50T349SubjectSetWidensToTheVerifierAnchorOnly(t *testing.T) {
	f := newP50Fixture(t, nil)
	// ① A verifier-anchor key_id is accepted (before Phase 50 this was structurally
	// impossible: the ledger only ever accepted manifest signing keys).
	e, err := f.sched.AppendKeyLifecycleEvent(keyLifecycleRequest{
		EventType: lifecycleEventActivated, KeyID: f.vak.keyID, NotBefore: klTS(p50T0),
	})
	if err != nil {
		t.Fatalf("a verifier key_id must be recordable: %v", err)
	}
	if e.KeyID != f.vak.keyID || e.EventSeq != 1 {
		t.Fatalf("the recorded event must carry the verifier identity: %+v", e)
	}
	data, rerr := os.ReadFile(keyLifecycleLogPath(f.dir))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(data), `"key_id":"`+f.vak.keyID+`"`) {
		t.Fatal("the verifier event must be durable")
	}

	// ② A key in NEITHER anchor is refused, and the file is untouched. The event
	// is a legal `activated` one, so ONLY the subject set can refuse it.
	before, _ := os.ReadFile(keyLifecycleLogPath(f.dir))
	if _, aerr := f.sched.AppendKeyLifecycleEvent(keyLifecycleRequest{
		EventType: lifecycleEventActivated, KeyID: "deadbeefdeadbeef", NotBefore: klTS(p50T0),
	}); aerr == nil {
		t.Fatal("a key in neither anchor must be refused")
	}
	after, _ := os.ReadFile(keyLifecycleLogPath(f.dir))
	if string(after) != string(before) {
		t.Fatal("a refused event must leave the file byte-identical")
	}

	// ③ A key in BOTH anchors is refused: its domain would be undecidable.
	f.sched.verifierTrust.keys[f.sched.signer.keyID] = f.sched.signer.priv.Public().(ed25519.PublicKey)
	before2, _ := os.ReadFile(keyLifecycleLogPath(f.dir))
	if _, aerr := f.sched.AppendKeyLifecycleEvent(keyLifecycleRequest{
		EventType: lifecycleEventActivated, KeyID: f.sched.signer.keyID, NotBefore: klTS(p50T0),
	}); aerr == nil {
		t.Fatal("a key sitting in both anchors must be refused (fail-closed)")
	}
	after2, _ := os.ReadFile(keyLifecycleLogPath(f.dir))
	if string(after2) != string(before2) {
		t.Fatal("the refused ambiguous event must leave the file untouched")
	}
}

// ---------------------------------------------------------------------------
// T350 — the per-key state machine is reused VERBATIM for verifier events.
// ---------------------------------------------------------------------------

func TestP50T350StateMachineIsReusedForVerifierEvents(t *testing.T) {
	f := newP50Fixture(t, nil)
	// The first fact about a key must be its activation.
	if _, err := f.sched.AppendKeyLifecycleEvent(keyLifecycleRequest{
		EventType: lifecycleEventRevoked, KeyID: f.vak.keyID, NotAfter: klTS(p50T5),
	}); err == nil {
		t.Fatal("a revocation with no baseline must be refused")
	}
	f.activateVAK(p50T0)
	if _, err := f.sched.AppendKeyLifecycleEvent(keyLifecycleRequest{
		EventType: lifecycleEventActivated, KeyID: f.vak.keyID, NotBefore: klTS(p50T0),
	}); err == nil {
		t.Fatal("resurrection must be refused")
	}
	if _, err := f.sched.AppendKeyLifecycleEvent(keyLifecycleRequest{
		EventType: lifecycleEventRotatedOut, KeyID: f.vak.keyID, NotAfter: klTS(p50T0.Add(-time.Hour)),
	}); err == nil {
		t.Fatal("a terminal bound before the activation bound must be refused")
	}
	f.terminalVAK(lifecycleEventRotatedOut, p50T5)
	if _, err := f.sched.AppendKeyLifecycleEvent(keyLifecycleRequest{
		EventType: lifecycleEventActivated, KeyID: f.vak.keyID, NotBefore: klTS(p50T6),
	}); err == nil {
		t.Fatal("re-activating a rotated-out key must be refused")
	}
	// The terminal meaning resolves to the STRONGER one (revoked > rotated_out).
	f.terminalVAK(lifecycleEventRevoked, p50T5.Add(time.Hour))
	st, err := loadKeyLifecycleState(f.sched.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	a := st.authorizationFor(f.vak.keyID)
	if a.Terminal != lifecycleEventRevoked {
		t.Fatalf("terminal must be revoked, got %q", a.Terminal)
	}
	if a.NotAfter != klTS(p50T5) {
		t.Fatalf("the EARLIEST terminal bound must win: %q", a.NotAfter)
	}
}

// ---------------------------------------------------------------------------
// T351 — P41 is untouched (I1/A3): the signing roll-up never lists a verifier
// subject and never lets one become active_key_id.
// ---------------------------------------------------------------------------

func TestP50T351Phase41RollupIsNotDisturbedByVerifierEvents(t *testing.T) {
	f := newP50Fixture(t, nil)
	f.publish(p50T1)
	signerID := f.sched.signer.keyID
	f.activate(signerID, p50T0)

	sum1 := keyLifecycleSummary(f.sched.keyLifecycleConfig())
	if len(sum1.Authorities) != 1 || sum1.Authorities[0].KeyID != signerID {
		t.Fatalf("before the verifier event the roll-up lists the signing subject: %+v", sum1.Authorities)
	}
	if sum1.ActiveKeyID != signerID {
		t.Fatalf("active_key_id must be the signing subject: %q", sum1.ActiveKeyID)
	}

	// Now a verifier window IS recorded (the write face widened in this Phase).
	f.activateVAK(p50T0)
	ledger, err := loadKeyLifecycleState(f.sched.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.byKey) != 2 {
		t.Fatalf("precondition: the verifier event must really be in the ledger, got %d key(s)", len(ledger.byKey))
	}
	sum2 := keyLifecycleSummary(f.sched.keyLifecycleConfig())
	if len(sum2.Authorities) != 1 || sum2.Authorities[0].KeyID != signerID {
		t.Fatalf("the signing roll-up must still list ONLY the signing subject: %+v", sum2.Authorities)
	}
	if sum2.ActiveKeyID != signerID {
		t.Fatalf("a verifier event must never rewrite active_key_id: %q", sum2.ActiveKeyID)
	}
	if sum2.EventCount != 2 {
		t.Fatalf("the event count is the ledger's, not the signing view's: %d", sum2.EventCount)
	}

	// And P41's manifest verdicts are the same function of the same interval.
	a := ledger.authorizationFor(signerID)
	if got := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: signerID}, klTS(p50T1), a); got.Verdict != sigVerdictOK {
		t.Fatalf("P41 must still hold inside the window: %s", got.Verdict)
	}
	if got := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: signerID}, "not-a-time", a); got.Verdict != sigVerdictTimeUnparseable {
		t.Fatalf("P41's fourth word must still fire: %s", got.Verdict)
	}
	f.terminal(signerID, lifecycleEventRevoked, p50T5)
	ledger2, err := loadKeyLifecycleState(f.sched.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	a2 := ledger2.authorizationFor(signerID)
	if got := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: signerID}, klTS(p50T6), a2); got.Verdict != sigVerdictAfterRevocation {
		t.Fatalf("P41's revocation verdict must be unchanged: %s", got.Verdict)
	}
	// The verifier subject is STILL absent from the signing roll-up.
	sum3 := keyLifecycleSummary(f.sched.keyLifecycleConfig())
	for _, row := range sum3.Authorities {
		if row.KeyID == f.vak.keyID {
			t.Fatalf("a verifier subject must never appear in authorizations: %+v", sum3.Authorities)
		}
	}
}

// ---------------------------------------------------------------------------
// T352 — P44 is untouched (A7-②/③): closed mode stays byte-identical, and the
// log's own bytes are produced by the verifier key without any new field.
// ---------------------------------------------------------------------------

func TestP50T352Phase44ClosedModeUnchanged(t *testing.T) {
	f := newP50Fixture(t, func(cfg *HistoryExportConfig) {
		cfg.VerifierKeyPath = ""
		cfg.VerifierTrustPaths = nil
	})
	f.publish(p50T1)
	f.attest(p50T2)

	data, err := os.ReadFile(verificationLogPath(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"key_id":"`+f.sched.signer.keyID+`"`) {
		t.Fatalf("closed mode must sign with the evidence key: %s", data)
	}
	for _, banned := range []string{"verifier", "verifier_authority", "after_revocation"} {
		if strings.Contains(string(data), banned) {
			t.Fatalf("closed-mode log must carry no %q: %s", banned, data)
		}
	}
	view, verr := f.sched.VerificationView()
	if verr != nil {
		t.Fatal(verr)
	}
	if len(view.Problems) != 0 {
		t.Fatalf("I6: closed mode keeps the problems channel empty: %+v", view.Problems)
	}
	if view.VerifierIndependent || view.VerifierKeyID != "" {
		t.Fatalf("closed mode must expose no verifier identity: %+v", view)
	}
	// The two-track verdicts are the frozen P44 ones.
	vs, lerr := loadVerificationState(f.sched.verificationConfig())
	if lerr != nil {
		t.Fatal(lerr)
	}
	if !vs.verifiable || len(vs.entries) != 1 {
		t.Fatalf("precondition: one verifiable report: %v %v", vs.verifiable, vs.errs)
	}
	if v := verifyVerificationEntrySignature(&vs.entries[0], f.sched.trust); v.Verdict != sigVerdictOK {
		t.Fatalf("the evidence-key record must read signature_ok: %s", v.Verdict)
	}
	// A record signed by an unknown key still reads key_unknown — not
	// verification_unauthorized (there is no verifier anchor to be foreign to).
	ghost := testSignerWithSeed(t, 41)
	p50AppendEntry(t, f, p50T1, ghost.keyID, ghost, "")
	vs2, lerr2 := loadVerificationState(f.sched.verificationConfig())
	if lerr2 != nil {
		t.Fatal(lerr2)
	}
	if vs2.verifiable {
		t.Fatal("an unknown signer must poison the log")
	}
	joined := strings.Join(vs2.errs, "; ")
	if !strings.Contains(joined, sigVerdictKeyUnknown) {
		t.Fatalf("closed mode must read key_unknown, got %s", joined)
	}
	if strings.Contains(joined, verificationVerdictUnauthorized) {
		t.Fatalf("closed mode must never reach the unauthorized track: %s", joined)
	}
	if len(vs2.problems) != 0 {
		t.Fatalf("I6: closed mode keeps the problems channel nil: %+v", vs2.problems)
	}
}

// ---------------------------------------------------------------------------
// T353 — the boundary semantics are P41's, character for character: `<` on
// not_before, `>=` on not_after.
// ---------------------------------------------------------------------------

func TestP50T353BoundaryComparisonIsP41s(t *testing.T) {
	f := newP50Fixture(t, nil)
	f.publish(p50T1)
	f.activateVAK(p50T0)
	f.terminalVAK(lifecycleEventRevoked, p50T5)

	// t == not_before ⇒ IN position; t == not_after ⇒ OUT of position. The two
	// records are signed by the SAME key, so only a time-aware comparison can
	// tell them apart.
	f.attest(p50T0)
	f.attest(p50T5)
	sum := f.face()
	row := p50Row(t, sum, f.vak.keyID)
	if row.Authorized != 1 {
		t.Fatalf("a record exactly at not_before is in position: %+v", row)
	}
	if row.AfterRevocation != 1 {
		t.Fatalf("a record exactly at not_after is out of position: %+v", row)
	}
	if row.BeforeActivation != 0 {
		t.Fatalf("nothing here is before activation: %+v", row)
	}
	if row.Checked != 2 {
		t.Fatalf("both records must be checked: %+v", row)
	}
	if row.Authorized == 0 || row.AfterRevocation == 0 {
		t.Fatal("the two sides of the boundary must not share a value")
	}

	// Two adjacent intervals do not bleed into each other: key B's revocation must
	// be invisible to key A. Here the manifest signer is bounded on its own.
	f.activate(f.sched.signer.keyID, p50T0)
	f.terminal(f.sched.signer.keyID, lifecycleEventRotatedOut, p50T5)
	st, err := loadKeyLifecycleState(f.sched.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	aA := st.authorizationFor(f.sched.signer.keyID)
	aB := st.authorizationFor(f.vak.keyID)
	if aA.Terminal != lifecycleEventRotatedOut || aB.Terminal != lifecycleEventRevoked {
		t.Fatalf("the two keys must keep their own terminal meanings: A=%+v B=%+v", aA, aB)
	}
}

// ---------------------------------------------------------------------------
// T354 — R41-7's fourth word, applied to the observation (I2/I7).
// ---------------------------------------------------------------------------

func TestP50T354UnparseableSignedAtIsIndeterminateNotNothingAssessed(t *testing.T) {
	f := newP50Fixture(t, nil)
	f.publish(p50T1)
	f.activateVAK(p50T0)
	p50AppendEntry(t, f, p50T1, f.vak.keyID, f.vak, "not-a-time")

	sum := f.face()
	row := p50Row(t, sum, f.vak.keyID)
	if row.Verdict != verifierAuthorityIndeterminate {
		t.Fatalf("an unparseable time declaration is fail-closed, got %s", row.Verdict)
	}
	if row.Checked != 0 {
		t.Fatalf("a record that could not be evaluated is not a checked one: %+v", row)
	}
	if sum.State == verifierAuthorityAuthorized || sum.State == verifierAuthorityNothingAssessed {
		t.Fatalf("T354: it must be neither authorized nor nothing_assessed, got %s", sum.State)
	}
	if row.Reason == "" {
		t.Fatal("the defect must be named")
	}
	if sum.Claims != 0 || sum.Authorized {
		t.Fatalf("no claim may survive a defective declaration: %+v", sum)
	}
}

// ---------------------------------------------------------------------------
// T355 — zero side effects: the face reads and nothing else.
// ---------------------------------------------------------------------------

func TestP50T355TheFaceHasNoSideEffects(t *testing.T) {
	f := newP50Fixture(t, nil)
	f.publish(p50T1)
	f.activateVAK(p50T0)
	f.attest(p50T1)

	paths := []string{
		verificationLogPath(f.dir),
		keyLifecycleLogPath(f.dir),
		anchorLogPath(f.dir),
	}
	before := map[string][]byte{}
	beforeInfo := map[string]os.FileInfo{}
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			before[p] = b
		}
		if fi, err := os.Stat(p); err == nil {
			beforeInfo[p] = fi
		}
	}
	listBefore := p50DirListing(t, f.dir)

	for i := 0; i < 3; i++ {
		sum := f.face()
		if sum.State == verifierAuthorityIndeterminate {
			t.Fatalf("precondition: the fixture must be judgeable: %s", sum.Reason)
		}
	}

	for p := range before {
		after, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s disappeared: %v", p, err)
		}
		if string(after) != string(before[p]) {
			t.Fatalf("%s was modified by a read face", p)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.ModTime().Equal(beforeInfo[p].ModTime()) {
			t.Fatalf("%s mtime moved: %v → %v", p, beforeInfo[p].ModTime(), fi.ModTime())
		}
	}
	if got := p50DirListing(t, f.dir); got != listBefore {
		t.Fatalf("the face created or removed a file:\n before %s\n after  %s", listBefore, got)
	}
}

func p50DirListing(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// ---------------------------------------------------------------------------
// T356 — zero new families / streams / routes (A3).
// ---------------------------------------------------------------------------

func TestP50T356NoNewFamilyStreamOrRoute(t *testing.T) {
	if got := len(witnessFamilyRegistry()); got != 6 {
		t.Fatalf("the registry must still hold 6 families, got %d", got)
	}
	if got := len(anchorDeliveryStreams()); got != 6 {
		t.Fatalf("the delivery partition must still hold 6 streams, got %d", got)
	}
	if got := len(deliverySweepStreams()); got != 4 {
		t.Fatalf("the sweep set must still hold 4 streams, got %d", got)
	}
	// A verifier authority event is a `key_lifecycle` event: the three faces that
	// cover that family cover the new events with no change at all.
	f := newP50Fixture(t, nil)
	f.publish(p50T1)
	f.activateVAK(p50T0)
	f.attest(p50T1)
	enabled := 0
	for _, fam := range witnessFamilyRegistry() {
		if fam.Name == witnessFamilyKeyLifecycle {
			enabled++
		}
	}
	if enabled != 1 {
		t.Fatalf("key_lifecycle must appear exactly once in the registry, got %d", enabled)
	}
	// The face itself registers nothing and routes nothing: it is a read-derived
	// group built from the two existing loads.
	src := strings.ToLower(p50ReadSource(t, "snapshot_verifier_authority.go"))
	for _, banned := range []string{"witnessfamilyregistry(", "anchordeliverystreams(", "deliverysweepstreams(", "handlefunc", "newservemux", "mux.handle"} {
		if strings.Contains(src, banned) {
			t.Fatalf("the new face must register no family and no route, found %q", banned)
		}
	}
}

func p50ReadSource(t *testing.T, relative string) string {
	t.Helper()
	src, err := os.ReadFile(relative)
	if err != nil {
		t.Fatal(err)
	}
	return string(src)
}

// ---------------------------------------------------------------------------
// T357 — mutation sensitivity, pinned at the DATA level inside the binary. The
// ten CODE-level mutations (MU1~MU10) are applied out of band against this
// implementation and each one's named test is shown RED before it is restored
// by sha256; this test pins the discriminating dependencies they exploit.
// ---------------------------------------------------------------------------

func TestP50T357MutationSensitivity(t *testing.T) {
	// MU1 (drop the interval check) — the verdict exists ONLY because the two
	// records fall on different sides of the bound. Remove the bound and it must
	// vanish (this is the same assertion P41's data-level mutants make).
	f := newP50Fixture(t, nil)
	f.publish(p50T1)
	f.activateVAK(p50T0)
	f.terminalVAK(lifecycleEventRevoked, p50T5)
	f.attest(p50T6)
	st, err := loadKeyLifecycleState(f.sched.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	a := st.authorizationFor(f.vak.keyID)
	if got := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: f.vak.keyID}, klTS(p50T6), a); got.Verdict != sigVerdictAfterRevocation {
		t.Fatalf("MU1 baseline: want after_revocation, got %s", got.Verdict)
	}
	m1 := a
	m1.NotAfter = ""
	m1.Terminal = ""
	if got := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: f.vak.keyID}, klTS(p50T6), m1); got.Verdict != sigVerdictOK {
		t.Fatalf("MU1: without the bound the verdict must disappear, got %s", got.Verdict)
	}

	// MU2 (fold unbounded into authorized) — an unbounded row must never be
	// authorized and must never contribute a claim.
	g := newP50Fixture(t, func(cfg *HistoryExportConfig) {
		cfg.KeyAuthorityPath = ""
		cfg.KeyAuthorityTrustPaths = nil
	})
	g.publish(p50T1)
	g.attest(p50T1)
	sum := g.face()
	if sum.Authorized || sum.Claims != 0 {
		t.Fatalf("MU2: unbounded must not be authorized: %+v", sum)
	}

	// MU6 (compare `<=` on the boundary) — the boundary cases are pinned by T353;
	// here the data-level dependence is that the interval comparison is what puts
	// the boundary record outside.
	h := newP50Fixture(t, nil)
	h.publish(p50T1)
	h.activateVAK(p50T0)
	h.terminalVAK(lifecycleEventRotatedOut, p50T5)
	hs, err := loadKeyLifecycleState(h.sched.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	ha := hs.authorizationFor(h.vak.keyID)
	if got := authorizeByLifecycle(SignatureVerdict{Verdict: sigVerdictOK, KeyID: h.vak.keyID}, klTS(p50T5), ha); got.Verdict != sigVerdictAfterRotation {
		t.Fatalf("MU6 baseline: a record AT not_after is out of position, got %s", got.Verdict)
	}

	// MU10 (assign the row verdict sequentially) — one authorized record plus one
	// defective record must stay indeterminate.
	f2 := newP50Fixture(t, nil)
	f2.publish(p50T1)
	f2.activateVAK(p50T0)
	f2.attest(p50T1)
	p50AppendEntry(t, f2, p50T1, f2.vak.keyID, f2.vak, "not-a-time")
	sum2 := f2.face()
	row := p50Row(t, sum2, f2.vak.keyID)
	if row.Authorized == 0 || row.Indeterminate == 0 {
		t.Fatalf("MU10 baseline: the mixed row must carry both counters: %+v", row)
	}
	if row.Verdict != verifierAuthorityIndeterminate {
		t.Fatalf("MU10: the defective record must win the total order, got %s", row.Verdict)
	}
}

// ---------------------------------------------------------------------------
// T358 — malformed input never panics (I8's defensive edge).
// ---------------------------------------------------------------------------

func TestP50T358MalformedInputNeverPanics(t *testing.T) {
	// An empty verification log: nothing observed.
	f := newP50Fixture(t, nil)
	if sum := f.face(); sum.State != verifierAuthorityNothingAssessed {
		t.Fatalf("an empty log must be nothing_assessed, got %s", sum.State)
	}
	// An empty (but present) lifecycle ledger plus one valid record.
	f.activateVAK(p50T0)
	f.publish(p50T1)
	f.attest(p50T1)
	if sum := f.face(); sum.State == "" {
		t.Fatal("the face must always carry a state")
	}

	for i, tc := range []struct {
		name       string
		claimedKey string
		signedAt   string
	}{
		{"empty key_id", "", ""},
		{"empty signed_at", "", ""},
		{"oversized field", strings.Repeat("k", 5000), ""},
	} {
		g := newP50Fixture(t, nil)
		g.publish(p50T1)
		g.activateVAK(p50T0)
		kid := tc.claimedKey
		if tc.name == "empty signed_at" {
			kid = g.vak.keyID
			p50AppendEntry(t, g, p50T1, kid, g.vak, " ")
		} else {
			p50AppendEntry(t, g, p50T1, kid, g.vak, tc.signedAt)
		}
		sum := g.face()
		if sum.State == "" {
			t.Fatalf("case %d (%s): no state", i, tc.name)
		}
		if sum.Authorized {
			t.Fatalf("case %d (%s): malformed input must never read authorized", i, tc.name)
		}
	}

	// Invalid JSON on either ledger: fail-closed, no panic.
	g := newP50Fixture(t, nil)
	g.publish(p50T1)
	g.activateVAK(p50T0)
	if err := os.WriteFile(verificationLogPath(g.dir), []byte("{invalid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if sum := g.face(); sum.State != verifierAuthorityIndeterminate {
		t.Fatalf("invalid JSON must be fail-closed, got %s", sum.State)
	}

	// A zero-value entry (every field empty) is a conflict-free but unusable
	// record: it must not panic, and it must not assert authority.
	h := newP50Fixture(t, nil)
	h.publish(p50T1)
	h.activateVAK(p50T0)
	p50AppendEntry(t, h, p50T1, "zero", h.vak, "")
	if sum := h.face(); sum.Authorized {
		t.Fatalf("must not assert authority: %+v", sum)
	}
}

// ---------------------------------------------------------------------------
// T359 — deletion protection REUSES P49 (A8-⑤), it is not re-implemented here.
// ---------------------------------------------------------------------------

func TestP50T359TailDeletionIsCaughtByP49NotHere(t *testing.T) {
	f := newP50Fixture(t, func(cfg *HistoryExportConfig) {
		wd := filepath.Join(cfg.Dir, "..", "witness")
		if err := os.MkdirAll(wd, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg.AnchorEndpoint = "file://" + filepath.ToSlash(wd)
		cfg.AnchorMaxAttempts = 8
	})
	f.publish(p50T1)
	f.activateVAK(p50T0)
	f.terminalVAK(lifecycleEventRevoked, p50T5)
	f.attest(p50T6)

	if sum := f.face(); sum.Violations != 1 {
		t.Fatalf("precondition: the violation must be visible before the deletion: %+v", sum)
	}
	if rz := f.sched.AnchorRealizationStatus(); rz.KeyLifecycle.Verdict != realizationFamilyRealized {
		t.Fatalf("precondition: P49 must read realized before the deletion: %+v (%s)", rz.KeyLifecycle, rz.KeyLifecycle.Reason)
	}

	// Remove the ledger's TAIL — the revocation event — while its anchor entry,
	// written after the row, stays. No legal producer of that state exists.
	p49KeepLines(t, keyLifecycleLogPath(f.dir), 1, keyLifecycleGroupOf)

	// This face can only report what the surviving evidence supports: the interval
	// is now open above, so the record reads authorized. Honest, and REGISTERED.
	sum := f.face()
	if sum.State != verifierAuthorityAuthorized && sum.State != verifierAuthorityNothingAssessed {
		t.Fatalf("after a tail deletion this face may only be authorized/nothing_assessed, got %s", sum.State)
	}
	if sum.Authorized && sum.Violations != 0 {
		t.Fatalf("no violation can survive the deletion: %+v", sum)
	}
	// P49 catches it, and it is P49 that catches it — the same case, both true.
	rz := f.sched.AnchorRealizationStatus()
	if rz.KeyLifecycle.Verdict != realizationFamilyUnrealized {
		t.Fatalf("P49 must report anchor_unrealized for the key_lifecycle family, got %s (%s)", rz.KeyLifecycle.Verdict, rz.KeyLifecycle.Reason)
	}
	if rz.State != realizationStateUnrealized {
		t.Fatalf("the global realization scalar must reflect it: %s", rz.State)
	}
}

// ---------------------------------------------------------------------------
// T360 — identity binding (I11/A9, review M1): the signer is
// `Signature.KeyID`; a row whose `key_id` names a DIFFERENT (in-window) key is
// fail-closed, and the named key's interval is NEVER consulted.
// ---------------------------------------------------------------------------

func TestP50T360KeyIDMismatchIsFailClosedAndNeverChecksTheNamedKey(t *testing.T) {
	// B is a second VERIFIER-anchor identity with a perfectly assertable and
	// in-window authorization. A is the VAK, revoked before the row.
	alt := testSignerWithSeed(t, 77)
	f := newP50Fixture(t, func(cfg *HistoryExportConfig) {
		altPath := writeRawPub(t, filepath.Dir(cfg.VerifierTrustPaths[0]), "alt.pub", alt.priv.Public().(ed25519.PublicKey))
		cfg.VerifierTrustPaths = append(cfg.VerifierTrustPaths, altPath)
	})
	f.publish(p50T1)
	f.activate(f.vak.keyID, p50T0)
	f.terminal(f.vak.keyID, lifecycleEventRevoked, p50T5)
	f.activate(alt.keyID, p50T0) // B: assertable, and in position at p50T6

	// The row CLAIMS key_id = B (in window) but is SIGNED by A (revoked). Both
	// digests, prev and stream are self-consistent, so only the binding gives it
	// away — the M1 bypass in its exact form.
	p50AppendEntry(t, f, p50T6, alt.keyID, f.vak, "")

	// The record IS usable to P44 (A is still in the verifier trust anchor).
	vs, err := loadVerificationState(f.sched.verificationConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(vs.entries) != 1 || !vs.verifiable {
		t.Fatalf("precondition: the mismatched row must be usable to P44: verifiable=%v entries=%d errs=%v", vs.verifiable, len(vs.entries), vs.errs)
	}
	if vs.entries[0].KeyID == vs.entries[0].Signature.KeyID {
		t.Fatal("fixture: the row must disagree")
	}

	sum := f.face()
	if sum.State != verifierAuthorityIndeterminate {
		t.Fatalf("① a key_id/signer disagreement is fail-closed, got %s (%s)", sum.State, sum.Reason)
	}
	if sum.Authorized {
		t.Fatal("② verifier_authorized must be false")
	}
	// ③ THE BYPASS MUST NOT EXIST: B's interval is in-window, so an implementation
	// that checked `e.KeyID` would report authorized for B.
	rowAlt, ok := sum.Keys[alt.keyID]
	if ok && rowAlt.Verdict == verifierAuthorityAuthorized {
		t.Fatalf("③ the named key's window must never be checked: %+v", rowAlt)
	}
	if ok && rowAlt.Checked != 0 {
		t.Fatalf("③ no record may be checked against the named key: %+v", rowAlt)
	}

	// The HONEST path is unaffected: a record whose key_id IS its signer is judged
	// normally (that is the deployment's only real write path).
	g := newP50Fixture(t, nil)
	g.publish(p50T1)
	g.activateVAK(p50T0)
	g.attest(p50T1)
	gsum := g.face()
	if gsum.State != verifierAuthorityAuthorized {
		t.Fatalf("the honest path must still be judged: %+v", gsum)
	}
	if row := p50Row(t, gsum, g.vak.keyID); row.Checked != 1 || row.Authorized != 1 {
		t.Fatalf("the honest row: %+v", row)
	}
}

// ---------------------------------------------------------------------------
// T361 — the row verdict is a TOTAL ORDER, first hit (I12, review M2): one
// authorized record must never overwrite a defective one.
// ---------------------------------------------------------------------------

func TestP50T361RowVerdictIsATotalOrderNotSequentialAssignment(t *testing.T) {
	f := newP50Fixture(t, nil)
	f.publish(p50T1)
	f.activateVAK(p50T0)
	// A: a perfectly good record inside the window.
	f.attest(p50T1)
	// B: a record whose time declaration is defective — signed by the same key, so
	// both land in ONE row.
	f.attest(p50T2)
	p50AppendEntry(t, f, p50T2, f.vak.keyID, f.vak, "not-a-time")

	sum := f.face()
	row := p50Row(t, sum, f.vak.keyID)
	if row.Entries != 3 {
		t.Fatalf("precondition: three records of one signer, got %d", row.Entries)
	}
	if row.Authorized == 0 || row.Indeterminate == 0 {
		t.Fatalf("the row must carry both counters: %+v", row)
	}
	if row.Verdict != verifierAuthorityIndeterminate {
		t.Fatalf("indeterminate must win the total order, got %s", row.Verdict)
	}
	if sum.State != verifierAuthorityIndeterminate || sum.Authorized {
		t.Fatalf("the global scalar must be indeterminate and never authorized: %+v", sum)
	}
	// The claims definition stays narrow: only records whose interval was really
	// evaluated count.
	if row.Checked != 2 {
		t.Fatalf("checked must count only the evaluated records: %+v", row)
	}
	if sum.Claims != 2 {
		t.Fatalf("claims must equal the checked sum: %d", sum.Claims)
	}
}

// ---------------------------------------------------------------------------
// T362 — the Phase-50 roll-up filter must be the IDENTITY on the SIGNING side
// (ADR-075 §4 A3 / ADR-076 §5 I1 / §8-⑦). A manifest key decommissioned by a
// trust-FILE edit — same directory, same ledger, same signer — must keep
// reading exactly as it did on the run before the edit. The pre-Phase-50
// `keyLifecycleSummary` had NO trust filter (it enumerated every key in
// `st.byKey`), so the roll-up is a pure function of the ledger, never of the
// current trust file. T351 only ever writes a VERIFIER-subject event, so it
// pins the `∉ verifierTrust` half and never exercises the `∈ signingTrust`
// half; this case pins the latter.
// ---------------------------------------------------------------------------

func TestP50T362Phase41RollupSurvivesManifestKeyDecommission(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "snapshots")
	keyDir := filepath.Join(root, "keys")
	for _, d := range []string{dir, keyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	signPriv, signPub, _, _ := genKeyPair(t, keyDir, "signer")
	kakPriv, kakPub, _, _ := genKeyPair(t, keyDir, "kak")
	_, extraPub, _, extraPubKey := genKeyPair(t, keyDir, "extra")

	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: p50T0, MinSeq: 1, MaxSeq: 3}}
	build := func(trust []string) *HistoryExportScheduler {
		s, err := NewHistoryExportScheduler(HistoryExportConfig{
			Store:                  st,
			Dir:                    dir,
			Interval:               time.Hour,
			Formats:                []string{"json"},
			SignKeyPath:            signPriv,
			TrustKeyPaths:          trust,
			KeyAuthorityPath:       kakPriv,
			KeyAuthorityTrustPaths: []string{kakPub},
			Clock:                  func() time.Time { return p50T1 },
		})
		if err != nil {
			t.Fatalf("scheduler: %v", err)
		}
		return s
	}

	// Run 1: the operator trusts TWO manifest signing keys and opens a window for
	// the second one.
	s1 := build([]string{signPub, extraPub})
	extraID := keyIDForPublicKey(extraPubKey)
	if _, err := s1.AppendKeyLifecycleEvent(keyLifecycleRequest{
		EventType: lifecycleEventActivated, KeyID: extraID, NotBefore: klTS(p50T0),
	}); err != nil {
		t.Fatalf("activate the second manifest key: %v", err)
	}
	sum1 := keyLifecycleSummary(s1.keyLifecycleConfig())
	if len(sum1.Authorities) != 1 || sum1.Authorities[0].KeyID != extraID {
		t.Fatalf("precondition: run 1 must list the second manifest key, got %+v", sum1.Authorities)
	}
	if sum1.ActiveKeyID != extraID {
		t.Fatalf("precondition: run 1's active_key_id must be %q, got %q", extraID, sum1.ActiveKeyID)
	}

	// Run 2: the operator decommissions that key — SAME directory, SAME ledger,
	// SAME signer; only the trust FILE changed (an ordinary decommission). The
	// ledger still owns the key's row and is still verifiable.
	s2 := build([]string{signPub})
	loaded, err := loadKeyLifecycleState(s2.keyLifecycleConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.byKey) != 1 {
		t.Fatalf("precondition: the ledger must still own the decommissioned key's row, got %d key(s)", len(loaded.byKey))
	}
	// MU11 discriminator: the decommissioned key is genuinely GONE from the
	// signing anchor while still owning its row — so re-adding the `∈ signingTrust`
	// half of the filter (the pre-fix mutant) blanks the roll-up here. This makes
	// the case red under the mutant and green under the fix.
	if c := s2.keyLifecycleConfig(); c.signingTrust != nil {
		if _, stillTrusted := c.signingTrust.keys[extraID]; stillTrusted {
			t.Fatalf("precondition: the trust-file edit must remove %s from the signing anchor", extraID)
		}
	}
	sum2 := keyLifecycleSummary(s2.keyLifecycleConfig())

	// A3/I1/§8-⑦: both runs read the same ledger and must agree.
	if len(sum2.Authorities) != len(sum1.Authorities) ||
		(len(sum2.Authorities) == 1 && sum2.Authorities[0].KeyID != extraID) {
		t.Fatalf("decommissioning a manifest key (trust-file edit, same dir, same ledger) changed `authorizations` from %+v to %+v",
			sum1.Authorities, sum2.Authorities)
	}
	if sum2.ActiveKeyID != sum1.ActiveKeyID {
		t.Fatalf("decommissioning a manifest key changed `active_key_id` from %q to %q", sum1.ActiveKeyID, sum2.ActiveKeyID)
	}
}
