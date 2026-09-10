package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// ---------------------------------------------------------------------------
// Phase 37 helpers
// ---------------------------------------------------------------------------

// genKeyPair writes a PKCS#8 PEM private key and a PKIX PEM public key into
// keyDir and returns their paths plus the raw material.
func genKeyPair(t *testing.T, keyDir, base string) (privPath, pubPath string, priv ed25519.PrivateKey, pub ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	privPath = filepath.Join(keyDir, base+".key")
	if err := os.WriteFile(privPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPath = filepath.Join(keyDir, base+".pub")
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o644); err != nil {
		t.Fatal(err)
	}
	return privPath, pubPath, priv, pub
}

type sigFixture struct {
	snapDir  string
	exp      time.Time
	sched    *HistoryExportScheduler
	privA    ed25519.PrivateKey
	pubA     ed25519.PublicKey
	keyIDA   string
	pubPathB string
	keyIDB   string
}

// signedSnapshot publishes ONE signed snapshot (key A, trusting A and B).
func signedSnapshot(t *testing.T) *sigFixture {
	t.Helper()
	root := t.TempDir()
	snapDir := filepath.Join(root, "snapshots")
	keyDir := filepath.Join(root, "keys")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	privPath, pubPathA, privA, pubA := genKeyPair(t, keyDir, "keyA")
	_, pubPathB, _, pubB := genKeyPair(t, keyDir, "keyB")

	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{
		Transitions: sampleTransitions(), ExportedAt: exp, MinSeq: 1, MaxSeq: 3,
	}}
	s, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store:         st,
		Dir:           snapDir,
		Interval:      time.Hour,
		Formats:       []string{"json"},
		SignKeyPath:   privPath,
		TrustKeyPaths: []string{pubPathA, pubPathB},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	return &sigFixture{
		snapDir: snapDir, exp: exp, sched: s,
		privA: privA, pubA: pubA, keyIDA: keyIDForPublicKey(pubA),
		pubPathB: pubPathB, keyIDB: keyIDForPublicKey(pubB),
	}
}

func (f *sigFixture) manifestName() string {
	return "alert-transitions-" + safeTS(f.exp) + ".manifest.json"
}

func (f *sigFixture) artifactName() string {
	return "alert-transitions-" + safeTS(f.exp) + ".json"
}

// editManifest applies an in-place edit to the manifest JSON on disk. The map
// round-trip changes whitespace and key order, which is semantically
// irrelevant to signing (T73).
func editManifest(t *testing.T, dir, name string, edit func(m map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	out, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func onlyResult(t *testing.T, res []VerifyResult) VerifyResult {
	t.Helper()
	if len(res) != 1 {
		t.Fatalf("want exactly 1 verify result, got %+v", res)
	}
	return res[0]
}

// ---------------------------------------------------------------------------
// T64 — a valid signature is signature_ok and leaves the P35 verdict intact
// ---------------------------------------------------------------------------
func TestSignatureValidVerifiesOK(t *testing.T) {
	f := signedSnapshot(t)
	m := readManifestFile(t, f.snapDir, f.manifestName())
	if v, _ := m["schema_version"].(float64); int(v) != manifestSchemaVersionV3 {
		t.Fatalf("signed manifest must be schema v3, got %v", m["schema_version"])
	}
	res := onlyResult(t, mustVerify(t, f.sched))
	if res.Signature == nil || res.Signature.Verdict != "signature_ok" {
		t.Fatalf("signature = %+v, want signature_ok", res.Signature)
	}
	if res.Signature.KeyID != f.keyIDA {
		t.Fatalf("key_id = %q, want the DERIVED id %q", res.Signature.KeyID, f.keyIDA)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (a good signature never changes the verdict)", res.Status)
	}
}

func mustVerify(t *testing.T, s *HistoryExportScheduler) []VerifyResult {
	t.Helper()
	res, err := s.VerifySnapshots(10)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// ---------------------------------------------------------------------------
// T65 — the layers do not blur: a tampered ARTIFACT keeps signature_ok but is
// caught by the digest check
// ---------------------------------------------------------------------------
func TestSignatureDoesNotMaskArtifactTampering(t *testing.T) {
	f := signedSnapshot(t)
	path := filepath.Join(f.snapDir, f.artifactName())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	res := onlyResult(t, mustVerify(t, f.sched))
	if res.Signature == nil || res.Signature.Verdict != "signature_ok" {
		t.Fatalf("signature = %+v, want signature_ok (signature covers the manifest only)", res.Signature)
	}
	if res.Status != "mismatch" {
		t.Fatalf("status = %q, want mismatch (the digest layer must catch it)", res.Status)
	}
}

// ---------------------------------------------------------------------------
// T66 — a tampered BUSINESS field is tamper evidence
// ---------------------------------------------------------------------------
func TestSignatureInvalidOnManifestFieldTamper(t *testing.T) {
	f := signedSnapshot(t)
	editManifest(t, f.snapDir, f.manifestName(), func(m map[string]any) {
		m["max_seq"] = float64(9999)
	})
	res := onlyResult(t, mustVerify(t, f.sched))
	if res.Signature == nil || res.Signature.Verdict != "signature_invalid" {
		t.Fatalf("signature = %+v, want signature_invalid", res.Signature)
	}
	if res.Status != "mismatch" {
		t.Fatalf("status = %q, want mismatch", res.Status)
	}
}

// ---------------------------------------------------------------------------
// T67 — no signing key ⇒ schema v2 and signature_absent (default unchanged)
// ---------------------------------------------------------------------------
func TestSignatureAbsentWhenNotConfigured(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, err := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Status().SigningEnabled {
		t.Fatal("signing must be disabled by default")
	}
	s.Tick(context.Background())
	m := readManifestFile(t, dir, "alert-transitions-"+safeTS(exp)+".manifest.json")
	if v, _ := m["schema_version"].(float64); int(v) != manifestSchemaVersionV2 {
		t.Fatalf("unsigned manifest must stay schema v2, got %v", m["schema_version"])
	}
	if _, ok := m["signature"]; ok {
		t.Fatal("v2 manifest must carry no signature block")
	}
	res := onlyResult(t, mustVerify(t, s))
	if res.Signature == nil || res.Signature.Verdict != "signature_absent" {
		t.Fatalf("signature = %+v, want signature_absent", res.Signature)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (v2 stays read-only compatible)", res.Status)
	}
}

// ---------------------------------------------------------------------------
// T68 — configured but signing fails ⇒ NOTHING is published (fail-closed)
// ---------------------------------------------------------------------------
func TestSignatureFailurePublishesNothing(t *testing.T) {
	root := t.TempDir()
	snapDir := filepath.Join(root, "snapshots")
	keyDir := filepath.Join(root, "keys")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	privPath, pubPathA, _, _ := genKeyPair(t, keyDir, "keyA")
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store: st, Dir: snapDir, Interval: time.Hour, Formats: []string{"json"},
		SignKeyPath: privPath, TrustKeyPaths: []string{pubPathA},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Inject a signing failure inside the publish window.
	s.beforeManifestSign = func(string) { s.signer.priv = nil }

	s.Tick(context.Background())

	// No manifest may exist: an unsigned/unverifiable manifest is never published.
	if _, err := os.Stat(filepath.Join(snapDir, "alert-transitions-"+safeTS(exp)+".manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("manifest must NOT be published when signing fails (stat err=%v)", err)
	}
	if !strings.Contains(s.Status().ManifestError, "manifest: sign:") {
		t.Fatalf("manifest_error = %q, want a signing failure", s.Status().ManifestError)
	}
	if s.Status().SignatureError == "" {
		t.Fatal("signature_error must surface the fail-closed signing failure")
	}
	res := onlyResult(t, mustVerify(t, s))
	if res.Status != "orphan_artifact" {
		t.Fatalf("status = %q, want orphan_artifact", res.Status)
	}
}

// ---------------------------------------------------------------------------
// T69 — re-signing identical content with the same key is idempotent
// ---------------------------------------------------------------------------
func TestSignatureIsDeterministic(t *testing.T) {
	keyDir := t.TempDir()
	privPath, _, _, _ := genKeyPair(t, keyDir, "keyA")
	signer, err := newExportSigner(privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	build := func() *snapshotManifest {
		return &snapshotManifest{
			SchemaVersion: manifestSchemaVersionV3, PublicationID: 1,
			Snapshot: "20260910T000001Z", ExportedAt: at.Format(time.RFC3339Nano),
			GeneratedAt: at.Format(time.RFC3339Nano), Source: "durable",
			SeqContinuity: seqContinuityValue, MinSeq: 1, MaxSeq: 10, Records: 10,
		}
	}
	a, b := build(), build()
	if err := signer.signManifest(a, at); err != nil {
		t.Fatal(err)
	}
	if err := signer.signManifest(b, at); err != nil {
		t.Fatal(err)
	}
	if a.Signature.Sig != b.Signature.Sig {
		t.Fatalf("signature is not deterministic: %q vs %q", a.Signature.Sig, b.Signature.Sig)
	}
}

// ---------------------------------------------------------------------------
// T70 / T76 — an unknown key is NOT tamper evidence
// ---------------------------------------------------------------------------
func TestUnknownKeyIsNotInvalidSignature(t *testing.T) {
	f := signedSnapshot(t)
	m := mustLoadManifest(t, f.snapDir, f.manifestName())

	// (a) The signing key is simply not in the anchor ⇒ key_unknown.
	other := t.TempDir()
	_, otherPub, _, _ := genKeyPair(t, other, "other")
	trustOther, err := newExportTrustStore([]string{otherPub})
	if err != nil {
		t.Fatal(err)
	}
	if v := verifyManifestSignature(m, trustOther); v.Verdict != "key_unknown" {
		t.Fatalf("verdict = %q, want key_unknown (unknown key ≠ invalid signature)", v.Verdict)
	}
	// (b) The key IS known and the signature IS wrong ⇒ signature_invalid.
	m.Signature.Sig = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if v := verifyManifestSignature(m, f.sched.trust); v.Verdict != "signature_invalid" {
		t.Fatalf("verdict = %q, want signature_invalid", v.Verdict)
	}
}

func mustLoadManifest(t *testing.T, dir, name string) *snapshotManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseSnapshotManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// ---------------------------------------------------------------------------
// T71 — a v2 manifest keeps working (read-only compatibility)
// ---------------------------------------------------------------------------
func TestV2ManifestStillVerifies(t *testing.T) {
	dir := t.TempDir()
	writeCovManifest(t, dir, "20260910T000001Z", 101, 1, 10, 10, false) // schema v2 writer
	res, err := covScheduler(t, dir).VerifySnapshots(10)
	if err != nil {
		t.Fatal(err)
	}
	r := onlyResult(t, res)
	if r.Signature == nil || r.Signature.Verdict != "signature_absent" {
		t.Fatalf("signature = %+v, want signature_absent", r.Signature)
	}
}

// ---------------------------------------------------------------------------
// T72 — verification is strictly read-only
// ---------------------------------------------------------------------------
func TestSignatureVerifyIsReadOnly(t *testing.T) {
	f := signedSnapshot(t)
	before := dirFingerprint(t, f.snapDir)
	mustVerify(t, f.sched)
	if after := dirFingerprint(t, f.snapDir); after != before {
		t.Fatal("verification mutated the snapshot directory")
	}
}

// ---------------------------------------------------------------------------
// T73 — canonicalization is SEMANTIC: equivalent JSON (whitespace / key order)
// still verifies
// ---------------------------------------------------------------------------
func TestSignatureCanonicalizationIsSemantic(t *testing.T) {
	f := signedSnapshot(t)
	// Rewrite the file with different whitespace AND a different key order
	// while keeping the structure identical.
	editManifest(t, f.snapDir, f.manifestName(), func(m map[string]any) {})
	res := onlyResult(t, mustVerify(t, f.sched))
	if res.Signature == nil || res.Signature.Verdict != "signature_ok" {
		t.Fatalf("signature = %+v, want signature_ok (canonical bytes are unchanged)", res.Signature)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok", res.Status)
	}
}

// ---------------------------------------------------------------------------
// T74 / T80 — every signature-metadata field is cryptographically bound, and
// `alg` never selects a verification implementation
// ---------------------------------------------------------------------------
func TestSignatureMetadataIsBound(t *testing.T) {
	cases := []struct {
		name string
		edit func(m map[string]any)
		want string
	}{
		{"signed_at", func(m map[string]any) { m["signature"].(map[string]any)["signed_at"] = "2001-01-01T00:00:00Z" }, "signature_invalid"},
		{"sig", func(m map[string]any) {
			m["signature"].(map[string]any)["sig"] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		}, "signature_invalid"},
		{"alg", func(m map[string]any) { m["signature"].(map[string]any)["alg"] = "rsa" }, "signature_invalid"},
		{"key_id→unknown", func(m map[string]any) { m["signature"].(map[string]any)["key_id"] = "0000000000000000" }, "key_unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := signedSnapshot(t)
			editManifest(t, f.snapDir, f.manifestName(), tc.edit)
			res := onlyResult(t, mustVerify(t, f.sched))
			if res.Signature == nil || res.Signature.Verdict != tc.want {
				t.Fatalf("verdict = %+v, want %s", res.Signature, tc.want)
			}
			wantStatus := "mismatch"
			if tc.want == "key_unknown" {
				wantStatus = "unknown"
			}
			if res.Status != wantStatus {
				t.Fatalf("status = %q, want %q", res.Status, wantStatus)
			}
		})
	}
}

// T74b — swapping key_id to ANOTHER TRUSTED key is tamper evidence
func TestSignatureKeyIDSwapToTrustedKeyIsInvalid(t *testing.T) {
	f := signedSnapshot(t)
	editManifest(t, f.snapDir, f.manifestName(), func(m map[string]any) {
		m["signature"].(map[string]any)["key_id"] = f.keyIDB
	})
	res := onlyResult(t, mustVerify(t, f.sched))
	if res.Signature == nil || res.Signature.Verdict != "signature_invalid" {
		t.Fatalf("verdict = %+v, want signature_invalid", res.Signature)
	}
	if res.Status != "mismatch" {
		t.Fatalf("status = %q, want mismatch", res.Status)
	}
}

// ---------------------------------------------------------------------------
// T75 — a malformed signature block is NEVER reported as tamper evidence
// ---------------------------------------------------------------------------
func TestMalformedSignatureBlockIsNotInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(m map[string]any)
	}{
		{"missing_sig", func(m map[string]any) { delete(m["signature"].(map[string]any), "sig") }},
		{"missing_signed_at", func(m map[string]any) { delete(m["signature"].(map[string]any), "signed_at") }},
		{"bad_base64", func(m map[string]any) { m["signature"].(map[string]any)["sig"] = "!!!not-base64!!!" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := signedSnapshot(t)
			editManifest(t, f.snapDir, f.manifestName(), tc.edit)
			res := onlyResult(t, mustVerify(t, f.sched))
			if res.Signature == nil || res.Signature.Verdict != "signature_malformed" {
				t.Fatalf("verdict = %+v, want signature_malformed (never signature_invalid)", res.Signature)
			}
			if res.Status != "unknown" {
				t.Fatalf("status = %q, want unknown (unverifiable ≠ ok, and ≠ tamper evidence)", res.Status)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// T77 — the trust anchor is never granted by the manifest itself
// ---------------------------------------------------------------------------
func TestTrustAnchorIsIndependentOfManifest(t *testing.T) {
	f := signedSnapshot(t)
	m := mustLoadManifest(t, f.snapDir, f.manifestName())

	trustA, err := newExportTrustStore([]string{filepath.Join(filepath.Dir(f.pubPathB), "keyA.pub")})
	if err != nil {
		t.Fatal(err)
	}
	if v := verifyManifestSignature(m, trustA); v.Verdict != "signature_ok" {
		t.Fatalf("with the CORRECT anchor the verdict = %q, want signature_ok", v.Verdict)
	}
	trustB, err := newExportTrustStore([]string{f.pubPathB})
	if err != nil {
		t.Fatal(err)
	}
	if v := verifyManifestSignature(m, trustB); v.Verdict == "signature_ok" {
		t.Fatal("with a DIFFERENT anchor the same manifest must not verify — the anchor is not derived from the manifest")
	}
}

// T78 — signed (v3) snapshots must not break the Phase 36 coverage surface
func TestV3ManifestDoesNotBreakCoverage(t *testing.T) {
	f := signedSnapshot(t)
	res, err := f.sched.Coverage(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.SnapshotsConsidered != 1 {
		t.Fatalf("snapshots_considered = %d, want 1", res.SnapshotsConsidered)
	}
	if len(res.Unusable) != 0 {
		t.Fatalf("unusable = %+v, want none (a v3 manifest is still parseable)", res.Unusable)
	}
	if res.Completeness != "complete" {
		t.Fatalf("completeness = %q, want complete", res.Completeness)
	}
	// Coverage must stay blind to the signature dimension.
	if res.ProvenanceUncertain {
		t.Fatal("coverage must not read the signature state (M5 方案 a)")
	}
}

// ---------------------------------------------------------------------------
// T79 — signer key identity: private → public → key_id, never configurable
// ---------------------------------------------------------------------------
func TestSignerKeyIdentityConsistency(t *testing.T) {
	keyDir := t.TempDir()
	privPath, pubPath, _, pub := genKeyPair(t, keyDir, "keyA")

	signer, err := newExportSigner(privPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if signer.keyID != keyIDForPublicKey(pub) {
		t.Fatalf("derived key_id = %q, want %q", signer.keyID, keyIDForPublicKey(pub))
	}
	// The derived id is also what the trust anchor resolves.
	ts, err := newExportTrustStore([]string{pubPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ts.keys[signer.keyID]; !ok {
		t.Fatal("the derived key id must resolve in the trust anchor built from the same key")
	}
	// A disagreeing configured key id is refused (never "sign with A, claim B").
	if _, err := newExportSigner(privPath, "deadbeefdeadbeef"); err == nil {
		t.Fatal("a configured key id that disagrees with the derived one must fail fast")
	}
	if _, err := newExportSigner(privPath, signer.keyID); err != nil {
		t.Fatalf("a matching configured key id must be accepted: %v", err)
	}
}

// T79b — signing with a key our own anchor cannot resolve is refused at start.
func TestSignKeyAbsentFromTrustAnchorFailsFast(t *testing.T) {
	root := t.TempDir()
	keyDir := filepath.Join(root, "keys")
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	privPath, _, _, _ := genKeyPair(t, keyDir, "keyA")
	_, otherPub, _, _ := genKeyPair(t, keyDir, "other")

	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions()}}
	_, err := NewHistoryExportScheduler(HistoryExportConfig{
		Store: st, Dir: root, Interval: time.Hour, Formats: []string{"json"},
		SignKeyPath: privPath, TrustKeyPaths: []string{otherPub},
	})
	if err == nil {
		t.Fatal("a sign key missing from the trust anchor must fail fast")
	}
	if !strings.Contains(err.Error(), "trust keys") {
		t.Fatalf("err = %v, want a trust-anchor diagnostic", err)
	}
}

// T79c — a raw 64-byte private key and a raw 32-byte public key work too.
func TestRawKeyFormats(t *testing.T) {
	keyDir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privRaw := filepath.Join(keyDir, "raw.key")
	pubRaw := filepath.Join(keyDir, "raw.pub")
	if err := os.WriteFile(privRaw, priv, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pubRaw, pub, 0o644); err != nil {
		t.Fatal(err)
	}
	signer, err := newExportSigner(privRaw, "")
	if err != nil {
		t.Fatal(err)
	}
	if signer.keyID != keyIDForPublicKey(pub) {
		t.Fatalf("raw private key derived %q, want %q", signer.keyID, keyIDForPublicKey(pub))
	}
	ts, err := newExportTrustStore([]string{pubRaw})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ts.keys[signer.keyID]; !ok {
		t.Fatal("raw public key must be usable as a trust anchor")
	}
}
