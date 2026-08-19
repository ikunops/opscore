package manifest

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrSignatureInvalid is returned when a manifest's detached signature does
// not validate against the trusted key (crypto failure / tampered bytes).
var ErrSignatureInvalid = errors.New("manifest: signature verification failed")

// ErrSignatureMissing is returned by a Signed*Provider when verification is
// configured but the .sig artifact is absent (fail-closed).
var ErrSignatureMissing = errors.New("manifest: signature required but .sig artifact missing")

// ErrSignatureUntrusted is the trust-ROOT class of failure: there is no usable
// trusted identity — empty TrustRoot, or every matching key is expired /
// rotated out. It is distinct from ErrSignatureInvalid (pure crypto failure).
// In a closed trust-root model an "externally-valid-but-untrusted" signature is
// undecidable (we do not possess the external key), so it is reported as
// ErrSignatureInvalid — the correct fail-closed posture.
var ErrSignatureUntrusted = errors.New("manifest: no trusted identity (empty/expired trust root)")

// ErrSignaturePolicy is returned when the signature is cryptographically valid
// but violates the trust policy (e.g. required signer mismatch).
var ErrSignaturePolicy = errors.New("manifest: signature policy violation")

// SignatureResult is the outcome of a signature + policy verification. It is
// emitted to an AuditSink for audit / UI / policy-decision, and is NOT part of
// the frozen Runtime Contract. It is the Phase 5.3 analogue of the Phase 3.5
// CompatibilityResult.
type SignatureResult struct {
	Verified       bool
	SignerID       string // KeyID of the matching trusted key
	KeyID          string // alias of SignerID (historical)
	Code           string // reason code: OK / NO_TRUST_ROOT / MISSING_SIGNATURE / NO_MATCHING_KEY / POLICY_REQUIRES_SIGNER / KEY_EXPIRED
	PolicyDecision string // human-readable decision
}

// AuditSink receives every SignatureResult (success or failure) so the host
// can record signer / policy decisions for audit and surface them in a UI.
type AuditSink func(SignatureResult)

// TrustedKey is one entry in the Phase 5.3 trust root.
type TrustedKey struct {
	KeyID     string
	PublicKey ed25519.PublicKey
	// Rotation windows (optional). A key is only eligible while ValidNow().
	ValidFrom    *time.Time // lower bound (inclusive)
	ValidUntil   *time.Time // upper bound (inclusive); drives key rotation
	SupercededBy string     // informational hint, e.g. the KeyID replacing this one
}

// ValidNow reports whether the key is currently within its rotation window.
func (k TrustedKey) ValidNow() bool {
	now := time.Now()
	if k.ValidFrom != nil && now.Before(*k.ValidFrom) {
		return false
	}
	if k.ValidUntil != nil && now.After(*k.ValidUntil) {
		return false
	}
	return true
}

// RequiredSigner ties a plugin namespace pattern to a required signer KeyID.
// NamespacePrefix supports a trailing "*" glob (e.g. "system.*").
type RequiredSigner struct {
	NamespacePrefix string
	RequiredSigner  string
}

// SignaturePolicy is the Phase 5.3 peripheral trust policy. It lives entirely
// OUTSIDE the frozen Runtime Contract and Manifest schema — it is configuration
// handed to the Verifier at construction time.
type SignaturePolicy struct {
	TrustRoot []TrustedKey
	Required  []RequiredSigner
}

// Verifier verifies a detached signature over data for a given plugin key,
// applying the configured trust policy. The Verifier type itself is
// peripheral (introduced in Phase 5.1) and may evolve; it is NOT the frozen
// Runtime Contract, so extending it to carry trust policy is in-bounds.
type Verifier interface {
	// Verify returns a SignatureResult and an error. On any failure the error
	// is non-nil AND result.Verified is false (fail-closed). key is the plugin
	// key/namespace used for required-signer matching.
	Verify(key string, data, sig []byte) (*SignatureResult, error)
}

// SignatureVerifier enforces, in order:
//  1. crypto validity against the trust root (key rotation: tries every
//     currently-valid trusted key),
//  2. required-signer policy (namespace -> required KeyID).
//
// It fails closed: any unmet condition returns an error before the caller may
// Parse / Load the manifest. This keeps the single trust boundary praised in
// ADR-010 (raw bytes -> Verify+Policy -> Parse) intact.
type SignatureVerifier struct {
	policy SignaturePolicy
	audit  AuditSink
}

// NewSignatureVerifier builds a policy-aware verifier. audit may be nil
// (discarded); pass a sink to capture results for audit/UI.
func NewSignatureVerifier(policy SignaturePolicy, audit AuditSink) *SignatureVerifier {
	if audit == nil {
		audit = func(SignatureResult) {}
	}
	return &SignatureVerifier{policy: policy, audit: audit}
}

func (v *SignatureVerifier) Verify(key string, data, sig []byte) (*SignatureResult, error) {
	res := SignatureResult{}
	defer func() { v.audit(res) }()

	if len(v.policy.TrustRoot) == 0 {
		res.Code = "NO_TRUST_ROOT"
		res.PolicyDecision = "no trusted keys configured"
		return &res, ErrSignatureUntrusted
	}
	if len(sig) == 0 {
		res.Code = "MISSING_SIGNATURE"
		res.PolicyDecision = "detached signature absent"
		return &res, ErrSignatureMissing
	}

	// 1) Crypto: find the first currently-valid trusted key whose signature
	//    validates. Trying all keys (not just one) is what makes key rotation
	//    transparent to the caller.
	var matched *TrustedKey
	var matchedExpired *TrustedKey // crypto matched but key is outside its window
	for i := range v.policy.TrustRoot {
		tk := &v.policy.TrustRoot[i]
		if !ed25519.Verify(tk.PublicKey, data, sig) {
			continue
		}
		if tk.ValidNow() {
			matched = tk
			break
		}
		matchedExpired = tk // remember for a precise trust-root (rotation) verdict
	}
	if matched != nil {
		// crypto OK under a currently-valid trusted key → proceed to policy
	} else if matchedExpired != nil {
		// Signature is cryptographically VALID under a trusted key, but that key
		// has expired / been rotated out. This is a trust-root (identity)
		// problem, not a crypto failure → Untrusted.
		res.Code = "KEY_EXPIRED"
		res.PolicyDecision = "signature valid but trusted key has expired (rotated out)"
		return &res, ErrSignatureUntrusted
	} else {
		// No trusted key verifies the signature. This is a cryptographic
		// failure (tampered bytes, wrong/external key, malformed sig) →
		// Invalid. We cannot possess the external key needed to prove "valid
		// but untrusted", so fail-closed as Invalid is the correct posture.
		res.Code = "NO_MATCHING_KEY"
		res.PolicyDecision = "signature does not match any trusted key (crypto failure)"
		return &res, ErrSignatureInvalid
	}

	// 2) Policy: required signer per namespace.
	for _, r := range v.policy.Required {
		if matchNamespace(key, r.NamespacePrefix) && matched.KeyID != r.RequiredSigner {
			res.Code = "POLICY_REQUIRES_SIGNER"
			res.PolicyDecision = fmt.Sprintf("plugin %q requires signer %q but was signed by %q", key, r.RequiredSigner, matched.KeyID)
			return &res, ErrSignaturePolicy
		}
	}

	res.Verified = true
	res.SignerID = matched.KeyID
	res.KeyID = matched.KeyID
	res.Code = "OK"
	res.PolicyDecision = "signature verified and policy satisfied"
	return &res, nil
}

// matchNamespace reports whether key matches a NamespacePrefix pattern. A
// pattern ending in "*" matches by prefix; "*" or "" matches everything.
func matchNamespace(key, pattern string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(key, strings.TrimSuffix(pattern, "*"))
	}
	return key == pattern
}

// Sign produces a detached ed25519 signature over data. Helper for signing
// tools and tests; the runtime process never calls it.
func Sign(priv ed25519.PrivateKey, data []byte) ([]byte, error) {
	return ed25519.Sign(priv, data), nil
}

// VerifyManifest is the SHARED peripheral gate used by every signed Provider
// (File / Git / OCI). It enforces the size caps and delegates to the Verifier.
// When v is nil, verification is skipped (legacy unsigned mode). The policy
// check sits AFTER raw-bytes verification and BEFORE Parse/Load — the single
// trust boundary praised in ADR-010.
//
// Error wrapping (by callers) yields: ErrSignatureInvalid (crypto failure:
// tampered bytes / wrong or external key / malformed sig), ErrSignatureMissing
// (sig required but absent), ErrSignatureUntrusted (no usable trusted identity:
// empty or fully-expired trust root), ErrSignaturePolicy (valid trusted key but
// required-signer namespace violation).
func VerifyManifest(key string, data, sig []byte, v Verifier) (*SignatureResult, error) {
	if len(data) > MaxManifestSize {
		return nil, fmt.Errorf("manifest too large (%d > %d bytes)", len(data), MaxManifestSize)
	}
	if len(sig) > MaxSignatureSize {
		return nil, fmt.Errorf("signature too large (%d > %d bytes)", len(sig), MaxSignatureSize)
	}
	if v == nil {
		return &SignatureResult{Verified: true, Code: "NO_VERIFIER", PolicyDecision: "verification disabled (legacy unsigned mode)"}, nil
	}
	return v.Verify(key, data, sig)
}
