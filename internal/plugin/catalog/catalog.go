// Package catalog implements Phase 6.2 Marketplace / Catalog: a READ-ONLY
// aggregate index over one or more manifest Providers (File / Git / OCI).
//
// Frozen scope (GPT Round 26 directive) — the catalog is an INDEX, NOT an
// INSTALLER. It provides exactly:
//
//  1. Plugin Index        — id, version, description, author, tags
//  2. Metadata Discovery  — capabilities, operations, risk, pluginApi, minKernel
//  3. Version Listing     — e.g. mysql 1.0 / 1.1 / 2.0
//  4. Search              — namespace / tag / keyword (+ paging, Round 27)
//  5. Source              — File / Git / OCI unified behind one catalog
//
// Round 27 sign-off added three catalog-layer SHOULDs, all zero-Contract:
// result-window pagination (Query.Offset/Limit + Page.Total), a content
// Digest for cache/diff/sync, and Source.Priority for presentation ranking.
//
// Explicitly OUT OF SCOPE for 6.2 (must NOT appear in this package):
//
//	Install · Enable · Trust · Signature Decision · Download · Upgrade
//	Auto Update · Dependency Resolution · Marketplace Account
//
// Architectural invariant (GPT Round 26): the dependency direction is
//
//	Catalog -> Provider (File/Git/OCI) -> PluginMetadata
//
// and NEVER Catalog -> Manager. The catalog does not know the Runtime exists:
// this package imports only internal/plugin/manifest, never
// internal/plugin/runtime or internal/core. That is enforced by a test.
//
// Runtime Contract impact: ZERO. Nothing here modifies the Manifest schema,
// the Provider interface, the Loader, the Descriptor, the Module contract,
// the Manager lifecycle, the Compatibility Gate, Capability Negotiation or
// Reload/Watcher. The catalog is a pure read-side projection.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

// OperationInfo is the read-only catalog projection of one declared operation.
type OperationInfo struct {
	Name     string `json:"name"`
	Resource string `json:"resource,omitempty"`
	Action   string `json:"action,omitempty"`
	Risk     string `json:"risk,omitempty"`
}

// PluginMetadata is the read-only catalog view of ONE plugin version.
//
// Description / Author / Tags are CATALOG-LAYER fields. They are intentionally
// NOT read from the Manifest: adding them to the manifest schema would be a
// Runtime Contract change, which is frozen. They stay empty when derived from
// a manifest and exist so a future, purely peripheral catalog-side metadata
// source (e.g. a sidecar catalog.json) can populate them without touching the
// Contract.
type PluginMetadata struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Tags        []string `json:"tags,omitempty"`

	// --- Metadata discovery (read-only manifest projections) ---
	Capabilities  []string        `json:"capabilities,omitempty"`
	Operations    []OperationInfo `json:"operations,omitempty"`
	PluginAPI     string          `json:"pluginApi,omitempty"`
	MinKernel     string          `json:"minKernel,omitempty"`
	SchemaVersion int             `json:"schemaVersion,omitempty"`

	// Digest is the content identity of this entry, "sha256:<hex>" over the
	// canonical JSON encoding of the source manifest (Round 27 / SHOULD-2).
	//
	// It exists for catalog-side cache / diff / sync and for showing "which
	// content version is this" in a UI. It is explicitly NOT a security or
	// supply-chain digest: trust decisions live in the Phase 5 signature
	// pipeline (Verify -> TrustRoot -> RequiredSigner), never here. A catalog
	// digest proves "same bytes as last time", not "these bytes are trusted".
	Digest string `json:"digest,omitempty"`

	// --- Provenance (which source supplied this entry) ---
	Source string `json:"source"`
	Key    string `json:"key"`
	// SourcePriority is copied from Source.Priority so a UI can rank entries
	// (e.g. official < community < experimental) without re-joining sources.
	SourcePriority int `json:"sourcePriority,omitempty"`
}

// Source binds a named manifest Provider into the catalog. The name is purely
// a label for provenance and Query.Source filtering (e.g. "file", "git", "oci").
type Source struct {
	Name     string
	Provider manifest.Provider
	// Priority ranks sources when the same plugin version is offered by more
	// than one of them: LOWER sorts first (0 = highest priority, the default).
	// This is presentation ranking only — it never implies trust, and it never
	// selects "which one to install", because the catalog does not install
	// (Round 27 / SHOULD-3).
	Priority int
}

// Query describes a catalog search. Zero value matches everything.
type Query struct {
	// Keyword matches (case-insensitive substring) against id, description,
	// tags and operation names.
	Keyword string
	// Tag matches one catalog tag exactly (case-insensitive).
	Tag string
	// Namespace matches plugins declaring at least one operation whose name
	// starts with this prefix, e.g. "system." or "plugin.mysql.".
	Namespace string
	// Source restricts results to one named source.
	Source string

	// --- Pagination (Round 27 / SHOULD-1) ---
	// Offset skips the first N matches; Limit caps the page size (0 = no cap).
	//
	// HONEST BOUNDARY: this is result-window pagination applied AFTER the
	// providers have been queried, not pushdown into the provider. True
	// pushdown would require paging parameters on the manifest.Provider
	// interface, and Provider is part of the FROZEN Runtime Contract. So the
	// window bounds what a caller/UI has to render and transfer, not what the
	// registry walk costs. Cheap paging for a huge OCI registry needs a
	// provider-level change and is deliberately deferred.
	Offset int
	Limit  int
}

// Page is a paginated Search result. Total is the number of matches BEFORE the
// offset/limit window is applied, so a UI can render a pager.
type Page struct {
	Items  []PluginMetadata `json:"items"`
	Total  int              `json:"total"`
	Offset int              `json:"offset"`
	Limit  int              `json:"limit"`
}

// Catalog is a read-only aggregate over one or more manifest Providers.
// It performs no caching: every call reflects the current provider state,
// which keeps it correct across Git/OCI ref moves without inventing an
// invalidation protocol.
type Catalog struct {
	sources []Source
}

// New builds a catalog over the given sources, in priority order.
func New(sources ...Source) *Catalog {
	return &Catalog{sources: append([]Source(nil), sources...)}
}

// List returns every plugin entry from every source, sorted by (id, version,
// source) for deterministic output. Unreadable individual entries are skipped
// rather than failing the whole listing — a catalog is a discovery surface,
// and one malformed manifest must not blind the operator to the rest. A source
// whose List() fails IS reported, because that is an infrastructure problem.
func (c *Catalog) List() ([]PluginMetadata, error) {
	var out []PluginMetadata
	for _, s := range c.sources {
		keys, err := s.Provider.List()
		if err != nil {
			return nil, fmt.Errorf("catalog: source %q list: %w", s.Name, err)
		}
		for _, k := range keys {
			m, err := s.Provider.Read(k)
			if err != nil || m == nil {
				continue // skip malformed entry, keep the catalog usable
			}
			out = append(out, fromManifest(m, s, k))
		}
	}
	sortEntries(out)
	return out, nil
}

// Get returns one plugin entry. An empty version means "any version"; when
// several match, the first in sorted order is returned.
func (c *Catalog) Get(id, version string) (*PluginMetadata, error) {
	all, err := c.List()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id && (version == "" || all[i].Version == version) {
			e := all[i]
			return &e, nil
		}
	}
	return nil, fmt.Errorf("catalog: plugin %q version %q not found", id, version)
}

// Versions lists the distinct versions available for one plugin id.
func (c *Catalog) Versions(id string) ([]string, error) {
	all, err := c.List()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var vs []string
	for _, e := range all {
		if e.ID == id && !seen[e.Version] {
			seen[e.Version] = true
			vs = append(vs, e.Version)
		}
	}
	sort.Strings(vs)
	return vs, nil
}

// Search filters the catalog by namespace / tag / keyword / source and applies
// the Offset/Limit window.
func (c *Catalog) Search(q Query) ([]PluginMetadata, error) {
	p, err := c.SearchPage(q)
	if err != nil {
		return nil, err
	}
	return p.Items, nil
}

// SearchPage is Search plus the pre-window match Total, for pagers.
func (c *Catalog) SearchPage(q Query) (*Page, error) {
	all, err := c.List()
	if err != nil {
		return nil, err
	}
	var hits []PluginMetadata
	for _, e := range all {
		if matches(e, q) {
			hits = append(hits, e)
		}
	}
	return &Page{
		Items:  window(hits, q.Offset, q.Limit),
		Total:  len(hits),
		Offset: q.Offset,
		Limit:  q.Limit,
	}, nil
}

// window applies offset/limit defensively: a negative or past-the-end offset
// yields an empty page rather than a panic, and Limit <= 0 means "no cap".
func window(v []PluginMetadata, offset, limit int) []PluginMetadata {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(v) {
		return nil
	}
	v = v[offset:]
	if limit > 0 && limit < len(v) {
		v = v[:limit]
	}
	return v
}

func matches(e PluginMetadata, q Query) bool {
	if q.Source != "" && !strings.EqualFold(e.Source, q.Source) {
		return false
	}
	if q.Namespace != "" {
		hit := false
		for _, op := range e.Operations {
			if strings.HasPrefix(op.Name, q.Namespace) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if q.Tag != "" {
		hit := false
		for _, t := range e.Tags {
			if strings.EqualFold(t, q.Tag) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if q.Keyword != "" {
		kw := strings.ToLower(q.Keyword)
		hay := []string{strings.ToLower(e.ID), strings.ToLower(e.Description)}
		for _, t := range e.Tags {
			hay = append(hay, strings.ToLower(t))
		}
		for _, op := range e.Operations {
			hay = append(hay, strings.ToLower(op.Name))
		}
		hit := false
		for _, h := range hay {
			if strings.Contains(h, kw) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func fromManifest(m *manifest.Manifest, s Source, key string) PluginMetadata {
	ops := make([]OperationInfo, 0, len(m.Operations))
	for _, od := range m.Operations {
		ops = append(ops, OperationInfo{
			Name:     od.Name,
			Resource: od.Resource,
			Action:   od.Action,
			Risk:     od.Risk,
		})
	}
	return PluginMetadata{
		ID:             m.Name,
		Version:        m.Version,
		Capabilities:   append([]string(nil), m.Capabilities...),
		Operations:     ops,
		PluginAPI:      m.PluginAPI,
		MinKernel:      m.MinKernel,
		SchemaVersion:  m.SchemaVersion,
		Digest:         digestOf(m),
		Source:         s.Name,
		Key:            key,
		SourcePriority: s.Priority,
	}
}

// digestOf hashes the canonical JSON encoding of the manifest. Go's
// encoding/json emits struct fields in declaration order and map keys in
// sorted order, so the encoding is stable for a given manifest value. An
// unencodable manifest yields an empty digest rather than an error: a digest
// is an optional catalog convenience and must never break discovery.
func digestOf(m *manifest.Manifest) string {
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sortEntries(v []PluginMetadata) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].ID != v[j].ID {
			return v[i].ID < v[j].ID
		}
		if v[i].Version != v[j].Version {
			return v[i].Version < v[j].Version
		}
		if v[i].SourcePriority != v[j].SourcePriority {
			return v[i].SourcePriority < v[j].SourcePriority
		}
		return v[i].Source < v[j].Source
	})
}
