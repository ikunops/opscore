// Package oci defines the OCI Distribution SPECIFICATION for the OpsCore
// third-party Executable Plugin ecosystem (Phase 7.6, GPT Round 35).
//
// It is a SPECIFICATION package. It models how a built Package — already
// described by ecosystem/packaging + ecosystem/tooling — is carried as an OCI
// artifact: the image layout, manifest, media types, content digest, and tag
// conventions. It NEVER pushes, pulls, logs in, uploads, or downloads anything;
// those are future implementation phases (MUST-4 bounds the transport to a
// spec-only shape, not a concrete Docker/OCI client).
//
// The Runtime Core is entirely unaware of OCI. An OCI artifact is unpacked by a
// transport that is out of scope here, then handed to packaging.Load() →
// isolation.AddFromPackage(), exactly like any other distribution source. OCI
// is a Carrier, not a new Package: plugin.json / release.json / PackageRef are
// reused wholesale (MUST-2), and the Phase 5 Trust Pipeline (ADR-011) remains
// the single authority on signatures — an OCI digest is content addressing, not
// trust (MUST-5).
//
// Dependency direction (pinned by an AST guard forbidding internal/):
//
//	oci → ecosystem/packaging  (reuses the frozen Package format)
//	oci → ecosystem/registry    (reuses PackageRef, does not redefine it)
//
// Neither peer is the Runtime Core; both are sibling ecosystem packages.
package oci

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/YuDong999/opscore/ecosystem/packaging"
	"github.com/YuDong999/opscore/ecosystem/registry"
)

// Media types for OpsCore plugin artifacts. They are namespaced under
// application/vnd.opscore.plugin.* so they cannot collide with generic OCI
// image media types.
const (
	// MediaTypePluginManifest is the OCI manifest media type for an opscore
	// plugin artifact.
	MediaTypePluginManifest = "application/vnd.opscore.plugin.manifest.v1+json"
	// MediaTypePluginConfig is the config blob (the package descriptor carrying
	// plugin.json + release.json references).
	MediaTypePluginConfig = "application/vnd.opscore.plugin.config.v1+json"
	// MediaTypePluginLayer is the media type for a generic layer carrying part
	// of the package file tree produced by ecosystem/tooling (the dist/
	// directory) that is neither the config nor a binary/resource (e.g. docs).
	MediaTypePluginLayer = "application/vnd.opscore.plugin.layer.v1.tar"
	// MediaTypePluginReleaseV1 is the release metadata blob (release.json).
	MediaTypePluginReleaseV1 = "application/vnd.opscore.plugin.release.v1+json"
	// MediaTypePluginBinary is a layer carrying an executable binary.
	MediaTypePluginBinary = "application/vnd.opscore.plugin.binary"
	// MediaTypePluginResource is a layer carrying a non-executable resource.
	MediaTypePluginResource = "application/vnd.opscore.plugin.resource"
	// ImageLayoutVersion is the OCI image-layout version this spec targets.
	ImageLayoutVersion = "1.0.0"
)

// ManifestSchemaVersion is the OCI image manifest schema version used here.
const ManifestSchemaVersion = 2

// Digest is an OCI content digest, e.g. "sha256:<64-hex>". It is content
// addressing only — NOT a trust signal (see MUST-5 / ADR-011).
type Digest struct {
	Algorithm string
	Hex       string
	Size      int64
}

// ParseDigest validates and parses an OCI digest string. Only sha256 is
// accepted in this spec (MUST-1: intentionally minimal).
func ParseDigest(s string) (Digest, error) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return Digest{}, fmt.Errorf("oci: malformed digest %q: missing algorithm separator", s)
	}
	algo, hex := s[:idx], s[idx+1:]
	if algo == "" || hex == "" {
		return Digest{}, fmt.Errorf("oci: malformed digest %q", s)
	}
	if algo != "sha256" {
		return Digest{}, fmt.Errorf("oci: unsupported digest algorithm %q (only sha256)", algo)
	}
	if len(hex) != 64 {
		return Digest{}, fmt.Errorf("oci: sha256 digest must be 64 hex chars, got %d", len(hex))
	}
	for _, c := range hex {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return Digest{}, fmt.Errorf("oci: invalid hex char %q in digest", c)
		}
	}
	return Digest{Algorithm: algo, Hex: hex}, nil
}

// String renders the canonical "algo:hex" form.
func (d Digest) String() string {
	if d.Algorithm == "" {
		return ""
	}
	return d.Algorithm + ":" + d.Hex
}

// IsZero reports whether the digest is unset.
func (d Digest) IsZero() bool { return d.Algorithm == "" && d.Hex == "" }

// FileHash pairs a content digest with the byte length of the file it covers.
type FileHash struct {
	Digest Digest
	Size   int64
}

// Descriptor identifies a content-addressable blob by media type + digest.
type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Manifest is the OCI image manifest for a plugin artifact.
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// Validate enforces the structural invariants this spec requires. It performs
// no network I/O.
func (m *Manifest) Validate() error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("oci: manifest schemaVersion must be %d, got %d", ManifestSchemaVersion, m.SchemaVersion)
	}
	if m.MediaType != MediaTypePluginManifest {
		return fmt.Errorf("oci: manifest mediaType must be %q, got %q", MediaTypePluginManifest, m.MediaType)
	}
	if m.Config.Digest == "" {
		return fmt.Errorf("oci: manifest config digest must not be empty")
	}
	if _, err := ParseDigest(m.Config.Digest); err != nil {
		return fmt.Errorf("oci: manifest config digest invalid: %w", err)
	}
	for i, l := range m.Layers {
		if _, err := ParseDigest(l.Digest); err != nil {
			return fmt.Errorf("oci: layer %d digest invalid: %w", i, err)
		}
	}
	return nil
}

// MarshalManifest serializes a validated manifest to its JSON form.
func MarshalManifest(m *Manifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(m, "", "  ")
}

// ParseManifest decodes and validates a manifest from its JSON form.
func ParseManifest(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("oci: cannot parse manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Layout is the content of the OCI "oci-layout" marker file.
type Layout struct {
	ImageLayoutVersion string `json:"imageLayoutVersion"`
}

// NewLayout returns a Layout pinned to the spec's image-layout version.
func NewLayout() Layout { return Layout{ImageLayoutVersion: ImageLayoutVersion} }

// MarshalLayout serializes the oci-layout marker.
func MarshalLayout(l Layout) ([]byte, error) {
	if l.ImageLayoutVersion == "" {
		return nil, fmt.Errorf("oci: imageLayoutVersion must not be empty")
	}
	return json.MarshalIndent(l, "", "  ")
}

// ParseLayout decodes the oci-layout marker.
func ParseLayout(b []byte) (Layout, error) {
	var l Layout
	if err := json.Unmarshal(b, &l); err != nil {
		return Layout{}, fmt.Errorf("oci: cannot parse layout: %w", err)
	}
	if l.ImageLayoutVersion == "" {
		return Layout{}, fmt.Errorf("oci: imageLayoutVersion must not be empty")
	}
	return l, nil
}

// Tag is a parsed OCI reference for a plugin artifact. Exactly one of Version
// or Digest is non-empty; a bare Name carries neither (an unpinned reference).
type Tag struct {
	Name    string
	Version string // non-empty when pinned by version (name:version)
	Digest  string // non-empty when pinned by digest (name@sha256:...)
}

// FormatTag renders a "name:version" reference.
func FormatTag(name, version string) string {
	if version == "" {
		return name
	}
	return name + ":" + version
}

// FormatDigestRef renders a "name@sha256:..." pinned reference.
func FormatDigestRef(name, digest string) string {
	return name + "@" + digest
}

// ParseRef parses an OCI reference into a Tag. It accepts:
//   - "name:version"
//   - "name@sha256:<hex>"
//   - "name" (bare, unpinned)
func ParseRef(ref string) (Tag, error) {
	if ref == "" {
		return Tag{}, fmt.Errorf("oci: empty reference")
	}
	if at := strings.Index(ref, "@"); at >= 0 {
		name, digest := ref[:at], ref[at+1:]
		if name == "" || digest == "" {
			return Tag{}, fmt.Errorf("oci: malformed digest reference %q", ref)
		}
		if _, err := ParseDigest(digest); err != nil {
			return Tag{}, err
		}
		return Tag{Name: name, Digest: digest}, nil
	}
	if colon := strings.Index(ref, ":"); colon >= 0 {
		name, ver := ref[:colon], ref[colon+1:]
		if name == "" || ver == "" {
			return Tag{}, fmt.Errorf("oci: malformed tag reference %q", ref)
		}
		return Tag{Name: name, Version: ver}, nil
	}
	return Tag{Name: ref}, nil
}

// Artifact is the OCI carrier view of a built Package. It does NOT redefine the
// Package format — it references the existing plugin.json / release.json and the
// dist/ tree as OCI descriptors. The Runtime never sees this type.
type Artifact struct {
	Ref   Tag
	Blobs []Descriptor
}

// FromPackage builds the OCI carrier view of a Package already loaded from
// disk. files maps a path (relative to the package dir, e.g. "plugin.json",
// "release.json", "bin/helper", "resources/help.txt") to its content hash and
// size. The caller (a transport out of scope here) supplies the hashes, so oci
// stays free of all I/O (MUST-3: Runtime-unaware, MUST-4: transport-free).
//
// The config blob is the plugin.json digest; release.json and every other file
// become layers. Order is preserved by insertion for deterministic output.
func FromPackage(pkg *packaging.Package, files map[string]FileHash) Artifact {
	var blobs []Descriptor
	for _, name := range sortedKeys(files) {
		fh := files[name]
		d := fh.Digest.String()
		mt := mediaTypeForFile(name)
		blobs = append(blobs, Descriptor{
			MediaType:   mt,
			Digest:      d,
			Size:        fh.Size,
			Annotations: map[string]string{"opscore.file": name, "org.opencontainers.image.title": name},
		})
	}
	return Artifact{
		Ref:   Tag{Name: pkg.Name, Version: pkg.Version},
		Blobs: blobs,
	}
}

// PackageMeta carries the Registry-level metadata the OCI carrier cannot
// itself assert (the runtime compatibility window lives on PackageRef, not on
// the package bytes). It is supplied by the host that published the artifact.
type PackageMeta struct {
	SDKVersion       string
	SupportedRuntime string
	MinRuntime       string
	MaxRuntime       string
	Description      string
	Tags             []string
	DownloadURL      string
	SignatureRef     string
}

// ToPackageRef bridges an OCI artifact into a registry.PackageRef, REUSING the
// existing Registry metadata model rather than inventing a new package
// identity (MUST-2). Only the artifact identity + caller metadata fill the ref;
// the format itself is unchanged. The Runtime still only ever meets the package
// through isolation.AddFromPackage.
func (a Artifact) ToPackageRef(meta PackageMeta) registry.PackageRef {
	return registry.PackageRef{
		ID:                a.Ref.Name,
		Name:              a.Ref.Name,
		Description:       meta.Description,
		LatestVersion:     a.Ref.Version,
		AvailableVersions: []string{a.Ref.Version},
		SDKVersion:        meta.SDKVersion,
		SupportedRuntime:  meta.SupportedRuntime,
		MinRuntime:        meta.MinRuntime,
		MaxRuntime:        meta.MaxRuntime,
		Tags:              meta.Tags,
		DownloadURL:       meta.DownloadURL,
		SignatureRef:      meta.SignatureRef,
	}
}

// mediaTypeForFile picks the OCI media type for a package-relative file by its
// path, so binary vs resource vs metadata layers stay distinguishable (Round 36
// SHOULD: centralize AND classify media types rather than scattering strings).
func mediaTypeForFile(name string) string {
	switch {
	case name == "plugin.json":
		return MediaTypePluginConfig
	case name == "release.json":
		return MediaTypePluginReleaseV1
	case strings.HasPrefix(name, "bin/"):
		return MediaTypePluginBinary
	case strings.HasPrefix(name, "resources/"):
		return MediaTypePluginResource
	default:
		return MediaTypePluginLayer
	}
}

// Manifest assembles an OCI image manifest from the carrier view. The config
// is the plugin.json descriptor; every other descriptor is a layer. oci stays
// I/O free — the manifest is built purely from the descriptors it already holds,
// ready for a transport (Phase 7.7) to marshal and store.
func (a Artifact) Manifest() (*Manifest, error) {
	var config Descriptor
	var layers []Descriptor
	for _, b := range a.Blobs {
		if b.Annotations["opscore.file"] == "plugin.json" {
			config = b
		} else {
			layers = append(layers, b)
		}
	}
	if config.Digest == "" {
		return nil, fmt.Errorf("oci: artifact %q has no plugin.json config descriptor", a.Ref.Name)
	}
	return &Manifest{
		SchemaVersion: ManifestSchemaVersion,
		MediaType:     MediaTypePluginManifest,
		Config:        config,
		Layers:        layers,
		Annotations:   map[string]string{"opscore.artifact": a.Ref.Name + ":" + a.Ref.Version},
	}, nil
}

// ArtifactFromManifest reconstructs the carrier view from a manifest — the
// inverse of Manifest(). Used by the reference transport when pulling.
func ArtifactFromManifest(m *Manifest) Artifact {
	blobs := append([]Descriptor{m.Config}, m.Layers...)
	var name, ver string
	if a, ok := m.Annotations["opscore.artifact"]; ok {
		if c := strings.Index(a, ":"); c >= 0 {
			name, ver = a[:c], a[c+1:]
		} else {
			name = a
		}
	}
	return Artifact{Ref: Tag{Name: name, Version: ver}, Blobs: blobs}
}

// sortedKeys returns the file-map keys in deterministic order so FromPackage
// emits stable descriptors (important for reproducible OCI layouts). plugin.json
// is placed first, the rest follow lexicographically.
func sortedKeys(m map[string]FileHash) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ai, bi := keys[i] == "plugin.json", keys[j] == "plugin.json"
		if ai != bi {
			return ai // plugin.json sorts first
		}
		return keys[i] < keys[j]
	})
	return keys
}
