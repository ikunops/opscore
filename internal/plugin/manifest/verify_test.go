package manifest_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

// demoManifest is a minimal but VALID manifest (passes Parse+Validate).
const demoManifest = `{"name":"demo","version":"1.0.0","operations":[{"name":"plugin.demo.x.y","resource":"x","action":"y"}]}`

// testKeypair returns a fresh ed25519 private key and a SignatureVerifier whose
// trust root contains the matching public key (KeyID "test-key").
func testKeypair(t *testing.T) (ed25519.PrivateKey, *manifest.SignatureVerifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := manifest.SignaturePolicy{
		TrustRoot: []manifest.TrustedKey{{KeyID: "test-key", PublicKey: pub}},
	}
	return priv, manifest.NewSignatureVerifier(policy, nil)
}

func writePlugin(t *testing.T, dir, key string) (manifestPath, sigPath string) {
	t.Helper()
	kdir := filepath.Join(dir, key)
	if err := os.MkdirAll(kdir, 0o755); err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(kdir, "manifest.json")
	if err := os.WriteFile(mp, []byte(demoManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return mp, mp + ".sig"
}

// --- SignatureVerifier core policy ---

func TestSignatureVerifier_VerifyOK(t *testing.T) {
	priv, v := testKeypair(t)
	data := []byte(demoManifest)
	sig, _ := manifest.Sign(priv, data)
	res, err := v.Verify("demo", data, sig)
	if err != nil {
		t.Fatalf("expected valid signature, got %v", err)
	}
	if !res.Verified || res.SignerID != "test-key" || res.Code != "OK" {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestSignatureVerifier_TamperedFails(t *testing.T) {
	priv, v := testKeypair(t)
	data := []byte(demoManifest)
	sig, _ := manifest.Sign(priv, data)
	tampered := append([]byte(nil), data...)
	tampered[len(tampered)-1] = 'x'
	if _, err := v.Verify("demo", tampered, sig); !errors.Is(err, manifest.ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid for tampered data, got %v", err)
	}
}

func TestSignatureVerifier_WrongKeyFails(t *testing.T) {
	priv, _ := testKeypair(t)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	policy := manifest.SignaturePolicy{
		TrustRoot: []manifest.TrustedKey{{KeyID: "other-key", PublicKey: otherPub}},
	}
	v := manifest.NewSignatureVerifier(policy, nil)
	data := []byte(demoManifest)
	sig, _ := manifest.Sign(priv, data)
	// Signed by `priv` (test-key) but trust root only knows other-key → no
	// trusted key verifies → cryptographic failure → ErrSignatureInvalid.
	if _, err := v.Verify("demo", data, sig); !errors.Is(err, manifest.ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid for untrusted key, got %v", err)
	}
}

func TestSignatureVerifier_NoTrustRoot(t *testing.T) {
	v := manifest.NewSignatureVerifier(manifest.SignaturePolicy{}, nil)
	data := []byte(demoManifest)
	sig, _ := manifest.Sign(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), data)
	if _, err := v.Verify("demo", data, sig); !errors.Is(err, manifest.ErrSignatureUntrusted) {
		t.Fatalf("expected ErrSignatureUntrusted for empty trust root, got %v", err)
	}
}

func TestSignatureVerifier_RequiredSignerSatisfied(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	policy := manifest.SignaturePolicy{
		TrustRoot: []manifest.TrustedKey{{KeyID: "ops-team", PublicKey: pub}},
		Required:  []manifest.RequiredSigner{{NamespacePrefix: "system.*", RequiredSigner: "ops-team"}},
	}
	v := manifest.NewSignatureVerifier(policy, nil)
	data := []byte(demoManifest)
	sig, _ := manifest.Sign(priv, data)
	if _, err := v.Verify("system.foo", data, sig); err != nil {
		t.Fatalf("expected required-signer satisfied, got %v", err)
	}
}

func TestSignatureVerifier_RequiredSignerViolation(t *testing.T) {
	opsPub, _, _ := ed25519.GenerateKey(rand.Reader)
	devPub, devPriv, _ := ed25519.GenerateKey(rand.Reader)
	policy := manifest.SignaturePolicy{
		TrustRoot: []manifest.TrustedKey{
			{KeyID: "ops-team", PublicKey: opsPub},
			{KeyID: "dev-team", PublicKey: devPub},
		},
		Required: []manifest.RequiredSigner{{NamespacePrefix: "system.*", RequiredSigner: "ops-team"}},
	}
	v := manifest.NewSignatureVerifier(policy, nil)
	data := []byte(demoManifest)
	// signed by dev-team, but system.* requires ops-team
	sig, _ := manifest.Sign(devPriv, data)
	if _, err := v.Verify("system.foo", data, sig); !errors.Is(err, manifest.ErrSignaturePolicy) {
		t.Fatalf("expected ErrSignaturePolicy for signer mismatch, got %v", err)
	}
}

func TestSignatureVerifier_KeyRotation(t *testing.T) {
	oldPub, oldPriv, _ := ed25519.GenerateKey(rand.Reader)
	newPub, newPriv, _ := ed25519.GenerateKey(rand.Reader)
	past := time.Now().Add(-48 * time.Hour)
	expired := time.Now().Add(-24 * time.Hour)
	policy := manifest.SignaturePolicy{
		TrustRoot: []manifest.TrustedKey{
			{KeyID: "old-key", PublicKey: oldPub, ValidUntil: &expired},
			{KeyID: "new-key", PublicKey: newPub, ValidFrom: &past},
		},
	}
	v := manifest.NewSignatureVerifier(policy, nil)
	data := []byte(demoManifest)
	oldSig, _ := manifest.Sign(oldPriv, data)
	if _, err := v.Verify("demo", data, oldSig); !errors.Is(err, manifest.ErrSignatureUntrusted) {
		t.Fatalf("expected old (expired) key to be rejected, got %v", err)
	}
	newSig, _ := manifest.Sign(newPriv, data)
	if _, err := v.Verify("demo", data, newSig); err != nil {
		t.Fatalf("expected new key accepted, got %v", err)
	}
}

func TestSignatureVerifier_AuditSinkCalled(t *testing.T) {
	priv, _ := testKeypair(t)
	var got []manifest.SignatureResult
	sink := func(r manifest.SignatureResult) { got = append(got, r) }
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	v := manifest.NewSignatureVerifier(manifest.SignaturePolicy{
		TrustRoot: []manifest.TrustedKey{{KeyID: "test-key", PublicKey: pub}},
	}, sink)
	data := []byte(demoManifest)
	sig, _ := manifest.Sign(priv, data)
	_, _ = v.Verify("demo", data, sig) // fails crypto (wrong key) but sink must fire
	if len(got) != 1 {
		t.Fatalf("audit sink should be called once, got %d", len(got))
	}
	if got[0].Verified {
		t.Fatalf("result should be unverified, got %+v", got[0])
	}
}

// --- SignedFileProvider (gate via Provider.Read) ---

func TestSignedFileProvider_VerifyOK(t *testing.T) {
	priv, v := testKeypair(t)
	dir := t.TempDir()
	mp, sp := writePlugin(t, dir, "demo")
	data, _ := os.ReadFile(mp)
	sig, _ := manifest.Sign(priv, data)
	if err := os.WriteFile(sp, sig, 0o644); err != nil {
		t.Fatal(err)
	}
	p := manifest.NewSignedFileProvider(dir, v)
	m, err := p.Read("demo")
	if err != nil {
		t.Fatalf("expected successful signed read, got %v", err)
	}
	if m.Name != "demo" {
		t.Fatalf("unexpected manifest name %q", m.Name)
	}
}

func TestSignedFileProvider_TamperedFails(t *testing.T) {
	priv, v := testKeypair(t)
	dir := t.TempDir()
	mp, sp := writePlugin(t, dir, "demo")
	data, _ := os.ReadFile(mp)
	sig, _ := manifest.Sign(priv, data)
	if err := os.WriteFile(sp, sig, 0o644); err != nil {
		t.Fatal(err)
	}
	// Tamper with the manifest AFTER signing.
	if err := os.WriteFile(mp, []byte(`{"name":"demo","version":"1.0.0","operations":[{"name":"plugin.demo.x.z","resource":"x","action":"z"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := manifest.NewSignedFileProvider(dir, v)
	if _, err := p.Read("demo"); !errors.Is(err, manifest.ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid for tampered manifest, got %v", err)
	}
}

func TestSignedFileProvider_MissingSigFails(t *testing.T) {
	_, v := testKeypair(t)
	dir := t.TempDir()
	writePlugin(t, dir, "demo") // no .sig written
	p := manifest.NewSignedFileProvider(dir, v)
	if _, err := p.Read("demo"); !errors.Is(err, manifest.ErrSignatureMissing) {
		t.Fatalf("expected ErrSignatureMissing for unsigned manifest, got %v", err)
	}
}

func TestUnsignedFileProvider_NoSigOK(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "demo") // no .sig, no verifier
	p := manifest.NewFileProvider(dir)
	m, err := p.Read("demo")
	if err != nil {
		t.Fatalf("unsigned provider must accept unsigned manifest, got %v", err)
	}
	if m.Name != "demo" {
		t.Fatalf("unexpected manifest name %q", m.Name)
	}
}
