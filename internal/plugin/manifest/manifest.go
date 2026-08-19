// Package manifest defines the EXTERNAL declaration format of a plugin
// (e.g. a JSON file on disk or fetched from a registry). It is deliberately
// NOT the runtime model — see runtime.Descriptor (ADR: Manifest/Descriptor
// separation, GPT Round 6/7). JSON never reaches the runtime.
package manifest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Manifest is the external, declarative description of a plugin.
type Manifest struct {
	// Name is the plugin's unique id, e.g. "mysql". It is the {name}
	// segment of every operation namespace plugin.<name>.<resource>.<action>.
	Name string `json:"name"`
	// Version is the semantic version declared in the manifest.
	Version string `json:"version"`
	// Capabilities are the plugin's OWN runtime requirements (Round 7 SHOULD),
	// e.g. ["linux","systemd","docker"]. These are NOT Execution
	// Capabilities (observed host facts); they let the Loader refuse to Load a
	// plugin whose requirements the host cannot satisfy, BEFORE any work.
	Capabilities []string `json:"capabilities,omitempty"`
	// Permissions is informational documentation of what the plugin needs; the
	// authoritative capability graph is derived from Operations below.
	Permissions []PermissionDecl `json:"permissions,omitempty"`
	// Operations are the capabilities the plugin contributes.
	Operations []OperationDecl `json:"operations"`

	// SchemaVersion is the manifest schema version this document was authored
	// against (Phase 3.5 / GPT Round 12). Lets the kernel reject manifests
	// written for a future, incompatible schema. 0 means "legacy/unversioned".
	SchemaVersion int `json:"schemaVersion,omitempty"`
	// PluginAPI is the plugin API contract this plugin was built against,
	// e.g. "opscore.plugin/v1". The kernel rejects plugins whose PluginAPI it
	// does not support (Compatibility Gate, Phase 3.5).
	PluginAPI string `json:"pluginApi,omitempty"`
	// MinKernel is the minimum kernel version required by this plugin,
	// e.g. "0.2.0". The Compatibility Gate rejects the plugin if the running
	// kernel is older (Phase 3.5).
	MinKernel string `json:"minKernel,omitempty"`
}

// PermissionDecl is a documented permission requirement (informational).
type PermissionDecl struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

// OperationDecl is one capability the plugin contributes.
type OperationDecl struct {
	// Name is the FULL operation name: plugin.<pluginName>.<resource>.<action>.
	Name string `json:"name"`
	// Resource is the plugin-scoped resource type. It must NOT be a reserved
	// system resource — enforced by plugin.ValidateOperation at load time.
	Resource string `json:"resource"`
	// Action is the verb, e.g. "execute" / "backup".
	Action string `json:"action"`
	// Risk is "low" | "medium" | "high" | "critical" (informational; the
	// loader maps it to core.RiskLevel).
	Risk string `json:"risk,omitempty"`
	// Description is free-form documentation.
	Description string `json:"description,omitempty"`
}

// Parser decodes a Manifest from raw JSON for ONE schema version. Phase 3.8
// (GPT Round 13) freezes the entry point as: SchemaVersion -> Select Parser
// -> Manifest. Registering a Parser per schema version lets the on-disk
// format evolve (schema=2/3) without changing any caller — Parse dispatches
// by the document's own schemaVersion, so an old reader still understands old
// docs and a new reader can still understand old docs (dual-read during rollout).
type Parser func(data []byte) (*Manifest, error)

// parsers is the schema-version -> Parser registry. The set of keys IS the
// set of schema versions this kernel can PARSE (see SupportedSchemaVersions);
// an unknown schema version is rejected at Parse time, so an unparseable
// manifest never reaches the runtime (Contract Frozen: this is the last piece
// of the Manifest Contract).
//
// FROZEN AFTER KERNEL INIT (GPT Round 14 SHOULD): the registry is a
// compile-time Contract, not a runtime knob. Registration happens at kernel
// init (typically via init()/MustRegisterParser), never mid-process — allowing
// ad-hoc runtime registration lets a schema that failed to parse once suddenly
// parse later, which makes parse behavior depend on call order. See
// MustRegisterParser.
var parsers = map[int]Parser{
	0: parseV0, // legacy / unversioned (schemaVersion omitted => 0)
	1: parseV1, // current schema
}

// MustRegisterParser registers the Parser for a schema version. It is intended
// for use at KERNEL INITIALIZATION (e.g. via an init() function) — NOT at
// runtime (GPT Round 14 SHOULD: the Parser Registry is a compile-time
// Contract; allowing ad-hoc runtime registration lets parse behavior shift
// mid-process, so a schema that failed once could parse later, which is
// surprising). It panics if the schema is ALREADY registered, turning an
// accidental double-registration into a loud startup failure instead of a
// silent overwrite.
func MustRegisterParser(schema int, p Parser) {
	if _, ok := parsers[schema]; ok {
		panic(fmt.Sprintf("manifest: parser for schema version %d already registered", schema))
	}
	parsers[schema] = p
}

// SupportedSchemaVersions returns the schema versions this kernel can parse,
// sorted ascending. Exposed so the Manager / Compatibility Gate / docs can
// advertise "this kernel understands schema v0 and v1".
func SupportedSchemaVersions() []int {
	out := make([]int, 0, len(parsers))
	for s := range parsers {
		out = append(out, s)
	}
	sort.Ints(out)
	return out
}

// peekSchemaVersion does a SHALLOW decode to read the document's
// schemaVersion before committing to a full parse. Malformed JSON fails here
// with the same error the old Parse would have returned.
func peekSchemaVersion(data []byte) (int, error) {
	var probe struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return 0, fmt.Errorf("plugin manifest: %w", err)
	}
	return probe.SchemaVersion, nil
}

// Parse is the ENTRY POINT (GPT Round 13 / Phase 3.8): peek the document's
// schemaVersion, dispatch to the registered Parser for that version, and
// return the decoded Manifest. Unknown schema versions are rejected up front
// so an unparseable manifest never reaches the Loader or runtime.
func Parse(data []byte) (*Manifest, error) {
	sv, err := peekSchemaVersion(data)
	if err != nil {
		return nil, err
	}
	p, ok := parsers[sv]
	if !ok {
		return nil, fmt.Errorf("plugin manifest: unsupported schema version %d (supported: %v)", sv, SupportedSchemaVersions())
	}
	return p(data)
}

// parseV0 decodes a legacy / unversioned manifest (schemaVersion omitted or 0).
func parseV0(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("plugin manifest: %w", err)
	}
	return &m, nil
}

// parseV1 decodes the current schema. It is currently identical to parseV0,
// but kept SEPARATE so a future schema=2/3 can diverge (rename fields, make
// keys required, add new structures) without breaking v1 documents — the
// whole point of the per-version Parser registry.
func parseV1(data []byte) (*Manifest, error) {
	return parseV0(data)
}

// Validate checks the manifest is internally consistent:
//   - Name and Version are non-empty
//   - every OperationDecl has Name/Resource/Action
//   - every OperationDecl.Name carries the plugin namespace prefix
//     "plugin.<Name>." (so the manifest cannot lie about ownership)
//
// Deeper contract checks (reserved resources, reserved prefixes) live in
// plugin.ValidateOperation, which runs when the op is converted to a
// core.Operation at load time.
func (m *Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("plugin manifest: empty name")
	}
	if m.Version == "" {
		return fmt.Errorf("plugin manifest %q: empty version", m.Name)
	}
	prefix := "plugin." + m.Name + "."
	for _, op := range m.Operations {
		if op.Name == "" || op.Resource == "" || op.Action == "" {
			return fmt.Errorf("plugin manifest %q: operation with empty name/resource/action", m.Name)
		}
		if !strings.HasPrefix(op.Name, prefix) {
			return fmt.Errorf("plugin manifest %q: operation %q must use namespace %q",
				m.Name, op.Name, prefix)
		}
	}
	return nil
}

// ValidateManifestLimits enforces the provider-side SAFETY LIMITS (GPT Round
// 10 MUST-3): operation and permission counts. It is the SINGLE entry point
// for structural limit checks so they are not scattered across the Provider.
// Byte-size limits stay in the Provider (it owns the raw bytes); this method
// owns the in-memory structural limits.
func (m *Manifest) ValidateManifestLimits() error {
	if len(m.Operations) > MaxOperations {
		return fmt.Errorf("plugin manifest %q: %d operations exceeds limit %d", m.Name, len(m.Operations), MaxOperations)
	}
	if len(m.Permissions) > MaxPermissions {
		return fmt.Errorf("plugin manifest %q: %d permissions exceeds limit %d", m.Name, len(m.Permissions), MaxPermissions)
	}
	return nil
}
