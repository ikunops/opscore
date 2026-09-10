package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"
)

// Phase 37 — cryptographic provenance & tamper evidence for snapshots
// (R191 architecture, frozen).
//
// What this proves, and ONLY this:
//   - the canonical manifest bytes of ONE snapshot were signed by a trusted
//     Ed25519 private key, and those bytes have not changed since.
//
// What this deliberately does NOT prove (Scope 必修 1):
//   - that the publication_id sequence is complete/continuous/deletion-proof.
//     The publication watermark stays outside P37's cryptographic guarantees,
//     and Phase 36's `indeterminate.publication_gaps` semantics are unchanged.
//
// Frozen cryptographic boundary (R190/R191):
//
//	signature payload = canonicalJSON( manifest with signature.sig removed )
//
// i.e. every manifest business field PLUS signature.{alg,key_id,signed_at} is
// covered, and ONLY `sig` itself is excluded. Canonicalization is SEMANTIC:
// re-serializing the same structure through the one canonical serializer gives
// identical bytes, so equivalent JSON (different whitespace / key order)
// still verifies — while any change of value, type, or field presence does not.

const (
	signatureAlgEd25519 = "ed25519"

	// Signature verdicts (A5, frozen order).
	sigVerdictOK         = "signature_ok"
	sigVerdictInvalid    = "signature_invalid"
	sigVerdictAbsent     = "signature_absent"
	sigVerdictMalformed  = "signature_malformed"
	sigVerdictKeyUnknown = "key_unknown"
)

// signatureBlock is the v3 manifest signature metadata. `sig` is EXCLUDED from
// the signed payload; every other field here is bound by the signature.
type signatureBlock struct {
	Alg      string `json:"alg"`
	KeyID    string `json:"key_id"`
	Sig      string `json:"sig,omitempty"`
	SignedAt string `json:"signed_at"`
}

// SignatureVerdict is the orthogonal Phase 37 dimension on the verify surface.
// It never replaces the Phase 35 six-state status; it can only hold or LOWER it
// (a signature never upgrades a verdict).
type SignatureVerdict struct {
	Verdict string `json:"verdict"`
	KeyID   string `json:"key_id,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// exportSigner holds the configured private key and its DERIVED identity. The
// key_id is never configurable: it is always derived from the private key's
// public half (R190 必修 3 / R191).
type exportSigner struct {
	priv  ed25519.PrivateKey
	keyID string
}

// exportTrustStore is the verification trust anchor: a set of trusted public
// keys addressed by derived key_id. It is ALWAYS loaded from an independent
// configuration source — never derived from, and never trusted from, the
// manifest being verified (Scope 必修 3).
type exportTrustStore struct {
	keys map[string]ed25519.PublicKey
}

// keyIDForPublicKey derives the non-configurable key identity:
// SHA-256(public key raw bytes)[:8] in hex (16 characters).
func keyIDForPublicKey(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// newExportSigner loads the signing key. wantKeyID may be provided only as a
// CONSISTENCY check: when non-empty it must equal the derived key_id, otherwise
// construction fails (fail-fast — a "sign with A, claim B" misconfiguration is
// refused rather than silently written into manifests).
func newExportSigner(path, wantKeyID string) (*exportSigner, error) {
	if path == "" {
		return nil, nil // signing disabled — default behaviour is unchanged
	}
	priv, err := loadEd25519PrivateKey(path)
	if err != nil {
		return nil, err
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("sign key: not an Ed25519 key")
	}
	derived := keyIDForPublicKey(pub)
	if wantKeyID != "" && wantKeyID != derived {
		return nil, fmt.Errorf("sign key: configured key id %q does not match the derived key id %q (key_id is derived from the private key and is never configurable)", wantKeyID, derived)
	}
	return &exportSigner{priv: priv, keyID: derived}, nil
}

// loadEd25519PrivateKey accepts a PKCS#8 PEM ("PRIVATE KEY") or a raw 64-byte
// Ed25519 private key file. Both paths converge on the same derivation flow.
func loadEd25519PrivateKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sign key: %w", err)
	}
	if block, _ := pem.Decode(raw); block != nil {
		parsed, perr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if perr != nil {
			return nil, fmt.Errorf("sign key: PKCS#8 parse: %w", perr)
		}
		priv, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("sign key: PEM does not hold an Ed25519 private key")
		}
		return priv, nil
	}
	if len(raw) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(append([]byte(nil), raw...)), nil
	}
	return nil, fmt.Errorf("sign key: unrecognized format (want PKCS#8 PEM or a raw %d-byte Ed25519 key)", ed25519.PrivateKeySize)
}

// newExportTrustStore loads the trusted public keys from independent
// configuration paths. Duplicate key ids are a configuration error.
func newExportTrustStore(paths []string) (*exportTrustStore, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	ts := &exportTrustStore{keys: map[string]ed25519.PublicKey{}}
	for _, p := range paths {
		pub, err := loadEd25519PublicKey(p)
		if err != nil {
			return nil, err
		}
		id := keyIDForPublicKey(pub)
		if _, dup := ts.keys[id]; dup {
			return nil, fmt.Errorf("trust key: duplicate key id %s (%s)", id, p)
		}
		ts.keys[id] = pub
	}
	return ts, nil
}

// loadEd25519PublicKey accepts a PKIX PEM ("PUBLIC KEY") or a raw 32-byte key.
func loadEd25519PublicKey(path string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("trust key %s: %w", path, err)
	}
	if block, _ := pem.Decode(raw); block != nil {
		parsed, perr := x509.ParsePKIXPublicKey(block.Bytes)
		if perr != nil {
			return nil, fmt.Errorf("trust key %s: PKIX parse: %w", path, perr)
		}
		pub, ok := parsed.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("trust key %s: PEM does not hold an Ed25519 public key", path)
		}
		return pub, nil
	}
	if len(raw) == ed25519.PublicKeySize {
		return ed25519.PublicKey(append([]byte(nil), raw...)), nil
	}
	return nil, fmt.Errorf("trust key %s: unrecognized format (want PKIX PEM or a raw %d-byte Ed25519 key)", path, ed25519.PublicKeySize)
}

// canonicalSignaturePayload returns the exact bytes the signature covers:
// every manifest business field plus signature.{alg,key_id,signed_at}, with
// ONLY signature.sig excluded. Because `sig` carries `omitempty`, blanking it
// removes the key from the serialization while the other signature metadata
// stays cryptographically bound (R190/R191).
func canonicalSignaturePayload(m *snapshotManifest) ([]byte, error) {
	if m == nil {
		return nil, errors.New("nil manifest")
	}
	payload := *m
	if m.Signature != nil {
		sb := *m.Signature
		sb.Sig = "" // excluded from the payload
		payload.Signature = &sb
	}
	return serializeSnapshotManifestBytes(&payload)
}

// signManifest fills in the signature block. The verification algorithm is
// ALWAYS Ed25519 (determined by the key type); `alg` is written as a signed
// declaration field only — it never negotiates an implementation (R191).
func (s *exportSigner) signManifest(m *snapshotManifest, signedAt time.Time) error {
	if s == nil {
		return errors.New("no signer configured")
	}
	if len(s.priv) != ed25519.PrivateKeySize {
		return errors.New("sign key: private key is not a valid Ed25519 key")
	}
	if m.Signature == nil {
		m.Signature = &signatureBlock{}
	}
	m.Signature.Alg = signatureAlgEd25519
	m.Signature.KeyID = s.keyID
	m.Signature.SignedAt = signedAt.UTC().Format(time.RFC3339Nano)
	m.Signature.Sig = ""
	payload, err := canonicalSignaturePayload(m)
	if err != nil {
		return err
	}
	m.Signature.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, payload))
	return nil
}

// verifyManifestSignature applies the frozen A5 decision order. It is a pure
// function of (manifest, trust anchor) — it never writes, repairs, or derives a
// key from the manifest.
func verifyManifestSignature(m *snapshotManifest, trust *exportTrustStore) SignatureVerdict {
	if m == nil {
		return SignatureVerdict{Verdict: sigVerdictAbsent, Detail: "manifest unavailable"}
	}
	sb := m.Signature
	if sb == nil {
		return SignatureVerdict{Verdict: sigVerdictAbsent, Detail: "manifest carries no signature block"}
	}
	// 1. structural integrity of the block itself: a broken block is NEVER
	//    reported as tamper evidence (`signature_invalid`).
	if sb.Alg == "" || sb.KeyID == "" || sb.Sig == "" || sb.SignedAt == "" {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "signature block is incomplete"}
	}
	rawSig, derr := base64.StdEncoding.DecodeString(sb.Sig)
	if derr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: "signature is not valid base64"}
	}
	// 2. trust-anchor lookup: not knowing the key means we cannot even attempt
	//    verification — that is NOT tamper evidence (hard boundary, Scope 必修 2).
	if trust == nil || len(trust.keys) == 0 {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "no trusted public keys are configured"}
	}
	pub, known := trust.keys[sb.KeyID]
	if !known {
		return SignatureVerdict{Verdict: sigVerdictKeyUnknown, KeyID: sb.KeyID, Detail: "key_id is not in the trusted key set"}
	}
	// 3. verification, ALWAYS with Ed25519 (key type decides the algorithm).
	payload, perr := canonicalSignaturePayload(m)
	if perr != nil {
		return SignatureVerdict{Verdict: sigVerdictMalformed, KeyID: sb.KeyID, Detail: perr.Error()}
	}
	if !ed25519.Verify(pub, payload, rawSig) {
		return SignatureVerdict{Verdict: sigVerdictInvalid, KeyID: sb.KeyID, Detail: "signature does not verify against the trusted key"}
	}
	return SignatureVerdict{Verdict: sigVerdictOK, KeyID: sb.KeyID}
}

// applySignatureVerdict holds or lowers a Phase 35 status according to the
// frozen table. A signature verdict NEVER upgrades a status.
//
//	signature_ok                       → unchanged
//	signature_invalid                  → mismatch (tamper evidence)
//	key_unknown / signature_malformed  → at most unknown (unverifiable)
//	signature_absent (v2)              → unchanged (read-only compatibility)
//	signature_absent (v3, missing)     → at most unknown (should have been signed)
func applySignatureVerdict(status string, v SignatureVerdict, schemaVersion int) string {
	switch v.Verdict {
	case sigVerdictInvalid:
		return verifyStatusMismatch
	case sigVerdictKeyUnknown, sigVerdictMalformed:
		if status == verifyStatusOK {
			return verifyStatusUnknown
		}
		return status
	case sigVerdictAbsent:
		if schemaVersion >= manifestSchemaVersionV3 && status == verifyStatusOK {
			return verifyStatusUnknown
		}
		return status
	default:
		return status
	}
}

// serializeSnapshotManifestBytes renders the canonical manifest bytes — the ONE
// serializer used by both signing and verification, so the payload is byte-for-
// byte identical on both sides.
func serializeSnapshotManifestBytes(m *snapshotManifest) ([]byte, error) {
	var buf bytes.Buffer
	if err := serializeSnapshotManifest(&buf, m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
