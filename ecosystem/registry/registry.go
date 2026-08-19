// Package registry defines the SPECIFICATION for a third-party Executable
// Plugin Registry / Marketplace (Phase 7.4, GPT Round 33).
//
// It defines exactly three things and nothing more:
//
//  1. The Registry Metadata model (PackageRef) — what a Marketplace advertises.
//  2. The Search API surface (Registry interface) — discovery, transport-free.
//  3. The index format (Index / catalog.json) — a serializable package list.
//
// Per the Phase 7.4 freeze this package is SPECIFICATION ONLY:
//
//	MUST-1  It depends only on the standard library. It introduces NO new wire
//	        format and does NOT import any internal/ package (pinned by an AST
//	        guard in registry_test.go). ecosystem/sdk + ecosystem/packaging are
//	        the only peer packages it may reference by type, but the discovery
//	        metadata is intentionally self-contained.
//	MUST-2  It NEVER touches the Runtime Core, Contract, Manifest, Provider, or
//	        Trust decision. Resolving a PackageRef into a running plugin is
//	        someone else's job (download → unpack → isolation.AddFromPackage).
//	MUST-3  It defines the discovery path; it does NOT implement transport
//	        (HTTP / OCI pull / Git clone), download, install, enable, upgrade,
//	        or dependency resolution.
//
// Forbidden (explicitly out of scope for 7.4): Registry Server / HTTP transport,
// auto-install / auto-upgrade, dependency resolution, package enable, any trust
// decision (the Phase 5 Trust Pipeline is reused, not reimplemented), and any
// Provider or Manifest modification.
//
// Discovery Flow (the lifecycle this spec enables):
//
//	Registry → Package Metadata (PackageRef) → Download Package → Unpack →
//	AddFromPackage() → Runtime
//
// 7.4 owns only the first arrow. The remaining arrows are Phase 7.5+ / host
// bootstrap concerns that consume this specification.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PackageRef is one entry a Registry advertises so a host can DISCOVER a
// package. It is metadata only — NOT the package bytes and NOT the Runtime
// Manifest. The host downloads + unpacks to obtain a packaging.Package, then
// routes it through the single legal entry isolation.AddFromPackage.
//
// SignatureRef points at the Phase 5 Trust Pipeline record for this artifact;
// it is a reference, never a trust decision made in this package.
type PackageRef struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Description       string            `json:"description,omitempty"`
	LatestVersion     string            `json:"latestVersion"`
	AvailableVersions []string          `json:"availableVersions,omitempty"`
	SDKVersion        string            `json:"sdkVersion"`
	SupportedRuntime  string            `json:"supportedRuntime,omitempty"` // human-readable summary; prefer Min/Max for machine checks
	MinRuntime        string            `json:"minRuntime,omitempty"`       // inclusive lower bound on host Runtime version
	MaxRuntime        string            `json:"maxRuntime,omitempty"`       // inclusive upper bound on host Runtime version
	Tags              []string          `json:"tags,omitempty"`
	DownloadURL       string            `json:"downloadURL"`
	Checksums         map[string]string `json:"checksums,omitempty"`
	SignatureRef      string            `json:"signatureRef,omitempty"`
}

// Validate enforces the minimum a PackageRef must carry to be discoverable.
// It is a structural check only — it performs no network fetch and makes no
// trust decision.
func (r PackageRef) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("registry: PackageRef missing id")
	}
	if r.Name == "" {
		return fmt.Errorf("registry: PackageRef %q missing name", r.ID)
	}
	if r.LatestVersion == "" {
		return fmt.Errorf("registry: PackageRef %q missing latestVersion", r.ID)
	}
	if r.SDKVersion == "" {
		return fmt.Errorf("registry: PackageRef %q missing sdkVersion", r.ID)
	}
	if r.DownloadURL == "" {
		return fmt.Errorf("registry: PackageRef %q missing downloadURL", r.ID)
	}
	return nil
}

// Registry is the SPECIFICATION of the discovery surface. These four methods
// ARE the Search API; the interface carries no transport. A concrete Registry
// may be backed by HTTP, a local catalog.json, or a git repo — none of that is
// built here (Phase 7.4 is spec only).
type Registry interface {
	Search(ctx context.Context, q SearchQuery) (SearchResult, error)
	List(ctx context.Context) ([]PackageRef, error)
	Get(ctx context.Context, id string) (PackageRef, error)
	Versions(ctx context.Context, id string) ([]string, error)
}

// SearchQuery is the input to Registry.Search. Term matches name/description/id
// (case-insensitive); Tags is an AND filter; SDK filters by sdkVersion. Offset /
// Limit are honest windowing — the registry returns a window, it does not push
// the filter down to a frozen Contract.
type SearchQuery struct {
	Term   string   `json:"term,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	SDK    string   `json:"sdk,omitempty"`
	Offset int      `json:"offset,omitempty"`
	Limit  int      `json:"limit,omitempty"`
}

// SearchResult is a windowed page of PackageRefs. Total is the pre-window match
// count — honest, mirroring the Phase 6.2 catalog convention.
type SearchResult struct {
	Items  []PackageRef `json:"items"`
	Total  int          `json:"total"`
	Offset int          `json:"offset"`
	Limit  int          `json:"limit"`
}

// Index is the serializable Registry catalog — the on-disk form of a Registry's
// package list (index.json / catalog.json). It is descriptive only.
type Index struct {
	Registry RegistryMeta `json:"registry"`
	Packages []PackageRef `json:"packages"`
}

// RegistryMeta describes the Registry that published an Index.
type RegistryMeta struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

// ParseIndex decodes an index.json / catalog.json byte stream into an Index and
// structurally validates every PackageRef. It performs no network I/O.
func ParseIndex(data []byte) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("registry: parse index: %w", err)
	}
	for i, p := range idx.Packages {
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("registry: index package[%d]: %w", i, err)
		}
	}
	return &idx, nil
}

// Marshal serializes the Index back to the index.json / catalog.json form.
func (idx *Index) Marshal() ([]byte, error) {
	return json.MarshalIndent(idx, "", "  ")
}

// MemoryRegistry is an in-memory reference implementation of Registry. It is
// SPEC-ONLY: it performs no network fetch. It exists so the Search API semantics
// are testable without a transport. A production Registry (HTTP / git / OCI) is
// a later-phase concern, not 7.4.
type MemoryRegistry struct {
	pkgs []PackageRef
}

// NewMemory builds a MemoryRegistry from a slice of PackageRefs. The slice is
// copied so callers cannot mutate the registry after construction.
func NewMemory(pkgs []PackageRef) *MemoryRegistry {
	out := make([]PackageRef, len(pkgs))
	copy(out, pkgs)
	return &MemoryRegistry{pkgs: out}
}

// FromIndex builds a MemoryRegistry from a parsed Index.
func FromIndex(idx *Index) *MemoryRegistry {
	return NewMemory(idx.Packages)
}

// matches reports whether a PackageRef satisfies a SearchQuery.
func matches(r PackageRef, q SearchQuery) bool {
	if q.Term != "" {
		t := strings.ToLower(q.Term)
		hay := strings.ToLower(r.Name + " " + r.Description + " " + r.ID)
		if !strings.Contains(hay, t) {
			return false
		}
	}
	if q.SDK != "" && r.SDKVersion != q.SDK {
		return false
	}
	for _, want := range q.Tags {
		if !containsTag(r.Tags, want) {
			return false
		}
	}
	return true
}

func containsTag(tags []string, want string) bool {
	for _, tg := range tags {
		if strings.EqualFold(tg, want) {
			return true
		}
	}
	return false
}

// Search applies the query and returns a windowed result.
func (m *MemoryRegistry) Search(_ context.Context, q SearchQuery) (SearchResult, error) {
	var matched []PackageRef
	for _, p := range m.pkgs {
		if matches(p, q) {
			matched = append(matched, p)
		}
	}
	total := len(matched)
	offset, limit := q.Offset, q.Limit
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		// No limit requested: return the full matched set (still honest — Total
		// reports the true count).
		return SearchResult{Items: matched, Total: total, Offset: offset, Limit: 0}, nil
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return SearchResult{Items: matched[offset:end], Total: total, Offset: offset, Limit: limit}, nil
}

// List returns every PackageRef known to the registry.
func (m *MemoryRegistry) List(_ context.Context) ([]PackageRef, error) {
	out := make([]PackageRef, len(m.pkgs))
	copy(out, m.pkgs)
	return out, nil
}

// Get returns the PackageRef with the given id, or an error.
func (m *MemoryRegistry) Get(_ context.Context, id string) (PackageRef, error) {
	for _, p := range m.pkgs {
		if p.ID == id {
			return p, nil
		}
	}
	return PackageRef{}, fmt.Errorf("registry: package %q not found", id)
}

// Versions returns the available versions for a PackageRef.
func (m *MemoryRegistry) Versions(_ context.Context, id string) ([]string, error) {
	p, err := m.Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(p.AvailableVersions))
	copy(out, p.AvailableVersions)
	return out, nil
}
