// Package dist is the Phase 7.7 REFERENCE DISTRIBUTION IMPLEMENTATION for the
// OpsCore third-party Executable Plugin ecosystem (GPT Round 36).
//
// It is the first phase that actually MOVES bytes — earlier phases (7.1–7.6)
// were specifications. Its single purpose is to PROVE the whole spec set is
// implementable end to end:
//
//	Build → Package → OCI Push → OCI Pull → Unpack → Load → (AddFromPackage at Runtime)
//
// Design laws from Round 36, pinned here:
//
//   - MUST-1  Push / Pull / Fetch Manifest / Fetch Blob / Resolve Digest are all
//     expressed through a Transport interface. The reference transport is
//     a LOCAL OCI layout — NO Docker Hub / Harbor / GHCR / ECR vendor is
//     bound as the only implementation.
//   - MUST-2  The flow reuses packaging.Load → oci.Artifact → registry.PackageRef
//     → isolation.AddFromPackage, never bypassing the single legal entry.
//     The final AddFromPackage() handoff is performed by the host (the
//     Runtime), not this client.
//   - MUST-3  Reference Implementation stays in the ecosystem layer. dist NEVER
//     imports the Runtime Core (ast guard forbids "internal/").
//   - MUST-4  Trust / Compatibility / Registry are COMPOSED, not re-implemented.
//     The client calls packaging, oci and registry wholesale.
//   - MUST-5  The client is pure artifact transport. It is NOT an Installer,
//     Updater, Dependency Resolver or Marketplace.
//
// Digest ≠ Trust (MUST-5 of 7.6) is preserved: dist only carries content
// digests; it never treats a valid digest as a trust signal.
package dist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/YuDong999/opscore/ecosystem/oci"
	"github.com/YuDong999/opscore/ecosystem/packaging"
	"github.com/YuDong999/opscore/ecosystem/registry"
)

// Transport is the artifact transport boundary for Phase 7.7. A reference
// implementation MUST NOT bind to a specific registry vendor (MUST-1); it only
// moves content-addressable blobs described by an oci.Manifest.
type Transport interface {
	// Push stores a manifest plus its referenced blobs (keyed by digest).
	Push(ctx context.Context, ref oci.Tag, m *oci.Manifest, blobs map[oci.Digest][]byte) error
	// Pull returns the manifest and every blob it references.
	Pull(ctx context.Context, ref oci.Tag) (*oci.Manifest, map[oci.Digest][]byte, error)
	// FetchManifest resolves the ref to a manifest (no blob bodies).
	FetchManifest(ctx context.Context, ref oci.Tag) (*oci.Manifest, error)
	// FetchBlob returns a single blob body by digest.
	FetchBlob(ctx context.Context, ref oci.Tag, digest oci.Digest) ([]byte, error)
	// ResolveDigest maps a ref (tag or digest) to its manifest digest.
	ResolveDigest(ctx context.Context, ref oci.Tag) (oci.Digest, error)
}

// Client is the Phase 7.7 reference distribution client. It composes the
// existing ecosystem modules into the complete push/pull flow and stops at the
// Runtime boundary (MUST-2/3/4).
type Client struct {
	t Transport
}

// NewClient builds a reference client over a transport.
func NewClient(t Transport) *Client { return &Client{t: t} }

// PushPackage reads the built files from disk, builds the OCI carrier view via
// oci.Artifact.FromPackage, and pushes it through the transport. files maps a
// package-relative path (e.g. "plugin.json", "bin/helper", "resources/help.txt")
// to its absolute on-disk location.
func (c *Client) PushPackage(ctx context.Context, ref oci.Tag, files map[string]string) error {
	art, blobBytes, err := buildArtifact(ref, files)
	if err != nil {
		return err
	}
	m, err := art.Manifest()
	if err != nil {
		return err
	}
	return c.t.Push(ctx, ref, m, blobBytes)
}

// PullPackage pulls an artifact, materializes its files into outDir, and reloads
// it via packaging.Load — exactly the existing single legal entry path up to the
// Runtime boundary (MUST-2). The final isolation.AddFromPackage() handoff is
// performed by the host, not this client.
func (c *Client) PullPackage(ctx context.Context, ref oci.Tag, outDir string) (*packaging.Package, error) {
	m, blobs, err := c.t.Pull(ctx, ref)
	if err != nil {
		return nil, err
	}
	for _, d := range append([]oci.Descriptor{m.Config}, m.Layers...) {
		rel := d.Annotations["opscore.file"]
		if rel == "" {
			continue
		}
		data, ok := blobs[dg(d.Digest)]
		if !ok {
			return nil, fmt.Errorf("dist: missing blob %s for %q", d.Digest, rel)
		}
		p := filepath.Join(outDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return nil, err
		}
	}
	return packaging.Load(outDir)
}

// ResolveRef pulls an artifact and bridges it into a registry.PackageRef using
// caller-supplied metadata (MUST-4: reuses registry, does not redefine it).
func (c *Client) ResolveRef(ctx context.Context, ref oci.Tag, meta oci.PackageMeta) (registry.PackageRef, error) {
	m, _, err := c.t.Pull(ctx, ref)
	if err != nil {
		return registry.PackageRef{}, err
	}
	return oci.ArtifactFromManifest(m).ToPackageRef(meta), nil
}

// buildArtifact reads file bytes from disk, computes content digests, and builds
// the oci carrier view + content store. It performs no network I/O.
func buildArtifact(ref oci.Tag, paths map[string]string) (oci.Artifact, map[oci.Digest][]byte, error) {
	fh := map[string]oci.FileHash{}
	blobs := map[oci.Digest][]byte{}
	for rel, abs := range paths {
		data, err := os.ReadFile(abs)
		if err != nil {
			return oci.Artifact{}, nil, fmt.Errorf("dist: read %q: %w", abs, err)
		}
		sum := sha256.Sum256(data)
		d := oci.Digest{Algorithm: "sha256", Hex: hex.EncodeToString(sum[:])}
		fh[rel] = oci.FileHash{Digest: d, Size: int64(len(data))}
		blobs[d] = data
	}
	// The carrier identity comes from the ref; the package bytes are just blobs.
	pkg := &packaging.Package{Name: ref.Name, Version: ref.Version}
	return oci.FromPackage(pkg, fh), blobs, nil
}

// dg parses a digest string into an oci.Digest for use as a map key.
func dg(s string) oci.Digest {
	d, _ := oci.ParseDigest(s)
	return d
}

// LocalTransport is the Phase 7.7 reference transport. It stores artifacts in a
// local OCI image layout directory — NO network, NO vendor client. It exists to
// prove the whole Build→Package→Push→Pull→Unpack→Load flow works end to end
// (the only thing it cannot do is reach a remote registry, which is out of scope
// for a reference implementation).
type LocalTransport struct {
	// Root holds oci-layout, blobs/sha256/<hex>, and index.json.
	Root string
}

type indexFile struct {
	SchemaVersion int             `json:"schemaVersion"`
	Manifests     []indexManifest `json:"manifests"`
}

type indexManifest struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Annotations map[string]string `json:"annotations"`
}

func (t *LocalTransport) blobsDir() string   { return filepath.Join(t.Root, "blobs", "sha256") }
func (t *LocalTransport) layoutPath() string { return filepath.Join(t.Root, "oci-layout") }
func (t *LocalTransport) indexPath() string  { return filepath.Join(t.Root, "index.json") }

func (t *LocalTransport) init() error {
	if err := os.MkdirAll(t.blobsDir(), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(t.layoutPath()); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		b, err := oci.MarshalLayout(oci.NewLayout())
		if err != nil {
			return err
		}
		if err := os.WriteFile(t.layoutPath(), b, 0o644); err != nil {
			return err
		}
	}
	return t.ensureIndex()
}

func (t *LocalTransport) ensureIndex() error {
	if _, err := os.Stat(t.indexPath()); err == nil {
		return nil
	}
	return t.writeIndex(indexFile{SchemaVersion: oci.ManifestSchemaVersion})
}

func (t *LocalTransport) readIndex() (indexFile, error) {
	var idx indexFile
	b, err := os.ReadFile(t.indexPath())
	if err != nil {
		return idx, err
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return idx, fmt.Errorf("dist: cannot parse index.json: %w", err)
	}
	return idx, nil
}

func (t *LocalTransport) writeIndex(idx indexFile) error {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.indexPath(), b, 0o644)
}

func (t *LocalTransport) writeBlob(d oci.Digest, data []byte) error {
	return os.WriteFile(filepath.Join(t.blobsDir(), d.Hex), data, 0o644)
}

func (t *LocalTransport) readBlob(d oci.Digest) ([]byte, error) {
	return os.ReadFile(filepath.Join(t.blobsDir(), d.Hex))
}

func (t *LocalTransport) linkTag(ref oci.Tag, md oci.Digest) error {
	idx, err := t.readIndex()
	if err != nil {
		return err
	}
	tag := oci.FormatTag(ref.Name, ref.Version)
	entry := indexManifest{
		MediaType:   oci.MediaTypePluginManifest,
		Digest:      md.String(),
		Annotations: map[string]string{"org.opencontainers.image.ref.name": tag},
	}
	for i, m := range idx.Manifests {
		if m.Annotations["org.opencontainers.image.ref.name"] == tag {
			idx.Manifests[i] = entry
			return t.writeIndex(idx)
		}
	}
	idx.Manifests = append(idx.Manifests, entry)
	return t.writeIndex(idx)
}

// Push implements Transport.
func (t *LocalTransport) Push(ctx context.Context, ref oci.Tag, m *oci.Manifest, blobs map[oci.Digest][]byte) error {
	if err := t.init(); err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("dist: invalid manifest: %w", err)
	}
	for d, data := range blobs {
		if err := t.writeBlob(d, data); err != nil {
			return err
		}
	}
	mb, err := oci.MarshalManifest(m)
	if err != nil {
		return err
	}
	md, err := digestBytes(mb)
	if err != nil {
		return err
	}
	if err := t.writeBlob(md, mb); err != nil {
		return err
	}
	return t.linkTag(ref, md)
}

// Pull implements Transport.
func (t *LocalTransport) Pull(ctx context.Context, ref oci.Tag) (*oci.Manifest, map[oci.Digest][]byte, error) {
	md, err := t.ResolveDigest(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	mb, err := t.readBlob(md)
	if err != nil {
		return nil, nil, err
	}
	m, err := oci.ParseManifest(mb)
	if err != nil {
		return nil, nil, err
	}
	store := map[oci.Digest][]byte{}
	for _, d := range append([]oci.Digest{dg(m.Config.Digest)}, digestsOf(m.Layers)...) {
		b, e := t.readBlob(d)
		if e != nil {
			return nil, nil, e
		}
		store[d] = b
	}
	return m, store, nil
}

// FetchManifest implements Transport.
func (t *LocalTransport) FetchManifest(ctx context.Context, ref oci.Tag) (*oci.Manifest, error) {
	md, err := t.ResolveDigest(ctx, ref)
	if err != nil {
		return nil, err
	}
	mb, err := t.readBlob(md)
	if err != nil {
		return nil, err
	}
	return oci.ParseManifest(mb)
}

// FetchBlob implements Transport.
func (t *LocalTransport) FetchBlob(ctx context.Context, ref oci.Tag, digest oci.Digest) ([]byte, error) {
	if _, err := t.ResolveDigest(ctx, ref); err != nil {
		return nil, err
	}
	return t.readBlob(digest)
}

// ResolveDigest implements Transport.
func (t *LocalTransport) ResolveDigest(ctx context.Context, ref oci.Tag) (oci.Digest, error) {
	if ref.Digest != "" {
		return oci.ParseDigest(ref.Digest)
	}
	idx, err := t.readIndex()
	if err != nil {
		return oci.Digest{}, err
	}
	tag := oci.FormatTag(ref.Name, ref.Version)
	for _, m := range idx.Manifests {
		if m.Annotations["org.opencontainers.image.ref.name"] == tag {
			return oci.ParseDigest(m.Digest)
		}
	}
	return oci.Digest{}, fmt.Errorf("dist: no manifest for ref %q", tag)
}

func digestsOf(ds []oci.Descriptor) []oci.Digest {
	out := make([]oci.Digest, 0, len(ds))
	for _, d := range ds {
		out = append(out, dg(d.Digest))
	}
	return out
}

func digestBytes(b []byte) (oci.Digest, error) {
	sum := sha256.Sum256(b)
	return oci.Digest{Algorithm: "sha256", Hex: hex.EncodeToString(sum[:])}, nil
}
