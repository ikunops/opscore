package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Provider-side safety limits (GPT Round 10 MUST-3: the manifest source is
// untrusted). A malformed/hostile manifest must never be able to stall or
// OOM Bootstrap.
const (
	// MaxManifestSize caps the on-disk manifest.json size (1 MiB). Read
	// rejects anything larger before parsing.
	MaxManifestSize = 1 << 20
	// MaxOperations caps declared operations per plugin.
	MaxOperations = 128
	// MaxPermissions is reserved for when OperationDecl gains an explicit
	// permission list (Phase 3.5 Compatibility Gate); declared now so the
	// limit is a single source of truth. Not yet enforced (no permission
	// field on OperationDecl).
	MaxPermissions = 256
	// MaxSignatureSize caps a detached .sig file (4 KiB is ample for ed25519).
	MaxSignatureSize = 4 << 10
)

// Provider is the abstract SOURCE of plugin manifests. It decouples the
// Loader (which turns manifests into runtime Modules) from WHERE manifests
// come from. A FileProvider reads them from a directory; future Git / OCI /
// HTTP providers implement the SAME interface, so the Loader never has to
// change (GPT Round 9: define Provider BEFORE FileLoader, so Git/OCI/HTTP
// are drop-in implementations and the Loader stays stable).
//
// The contract:
//   - List returns the stable plugin KEYS available from this source (the
//     subdirectory / slug names the source knows).
//   - Read returns the full Manifest for one such key, PARSED and VALIDATED
//     at the boundary, so a malformed manifest fails here — before the Loader
//     ever sees it.
type Provider interface {
	// List returns the plugin keys this source can supply.
	List() ([]string, error)
	// Read returns the validated Manifest for key, or an error if the key
	// is unknown or its manifest is malformed.
	Read(key string) (*Manifest, error)
}

// fileProvider reads manifests from a directory on disk. Layout:
//
//	<Dir>/<key>/manifest.json
//
// Every immediate subdirectory of Dir that contains a manifest.json is one
// plugin, keyed by the subdirectory name. This leaves room for future
// assets (icon, README, schema) alongside the manifest without inventing a
// second format.
type fileProvider struct {
	dir string
	// verifier, when non-nil, enables detached-signature verification (Phase
	// 5.1). It is PURELY PERIPHERAL: it never changes the Provider interface
	// or the Manifest/Descriptor contract — it only adds a gate at the source
	// boundary. nil = no verification (backward compatible with every existing
	// caller, including tests).
	verifier Verifier
	// sigExt is the detached-signature file extension, searched next to
	// manifest.json (e.g. <key>/manifest.json.sig).
	sigExt string
}

// NewFileProvider builds a Provider backed by a directory of plugin
// subdirectories. It performs NO disk access at construction — List/Read are
// lazy, so a misconfigured path fails at discovery time, not at startup.
// No signature verification is performed (use NewSignedFileProvider to require it).
func NewFileProvider(dir string) Provider {
	return &fileProvider{dir: dir}
}

// NewSignedFileProvider builds a Provider that REQUIRES a valid detached
// signature for every manifest (fail-closed). It is the Phase 5.1 peripheral
// capability: a manifest with a missing or invalid <key>/manifest.json.sig is
// rejected at the source boundary, before Parse / Compatibility / Load. This
// does not alter the Provider contract or the Runtime Contract — callers swap
// in this Provider at assembly time and the Loader/Manager are unchanged.
func NewSignedFileProvider(dir string, v Verifier) Provider {
	return &fileProvider{dir: dir, verifier: v, sigExt: ".sig"}
}

// List scans Dir for immediate subdirectories that contain a manifest.json.
func (p *fileProvider) List() ([]string, error) {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return nil, fmt.Errorf("manifest provider: list %q: %w", p.dir, err)
	}
	var keys []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mf := filepath.Join(p.dir, e.Name(), "manifest.json")
		if info, err := os.Stat(mf); err == nil && !info.IsDir() {
			keys = append(keys, e.Name())
		}
	}
	sort.Strings(keys) // Round 10 SHOULD: deterministic order for logs/audit
	return keys, nil
}

// Read loads, parses and validates <Dir>/<key>/manifest.json.
func (p *fileProvider) Read(key string) (*Manifest, error) {
	if key == "" {
		return nil, fmt.Errorf("manifest provider: empty key")
	}
	// Reject path traversal: a valid key is a single path segment.
	if filepath.Base(key) != key || key == "." || key == ".." {
		return nil, fmt.Errorf("manifest provider: invalid key %q", key)
	}
	mf := filepath.Join(p.dir, key, "manifest.json")
	data, err := os.ReadFile(mf)
	if err != nil {
		return nil, fmt.Errorf("manifest provider: read %q: %w", key, err)
	}
	if len(data) > MaxManifestSize {
		return nil, fmt.Errorf("manifest provider: %q manifest too large (%d > %d bytes)", key, len(data), MaxManifestSize)
	}
	// Phase 5.3 peripheral trust gate: verify the detached signature over the
	// raw manifest bytes and enforce the signature policy BEFORE Parse /
	// Compatibility / Load. Fail-closed when a verifier is configured. The sig
	// lives next to manifest.json on disk.
	if p.verifier != nil {
		sig, serr := os.ReadFile(mf + p.sigExt)
		if serr != nil {
			return nil, fmt.Errorf("manifest provider: %q: %w", key, ErrSignatureMissing)
		}
		if _, verr := VerifyManifest(key, data, sig, p.verifier); verr != nil {
			return nil, fmt.Errorf("manifest provider: %q: %w", key, verr)
		}
	}
	m, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("manifest provider: validate %q: %w", key, err)
	}
	if err := m.ValidateManifestLimits(); err != nil {
		return nil, fmt.Errorf("manifest provider: limits %q: %w", key, err)
	}
	return m, nil
}
