// Package plugin is the Phase 3 Plugin SDK skeleton.
//
// Design contract (from the OpsCore architecture / 00-设计原则):
//   - A plugin is a compile-time-OR-runtime Module that the kernel cannot tell
//     apart from an in-tree builtin. Both satisfy builtin.Module, so the
//     Dispatcher and Control Plane never branch on "builtin vs plugin".
//   - The Manifest declares METADATA ONLY (name, version, the operations it
//     contributes, their permissions/risk). It does NOT contain executable
//     logic. Operation handling is Go code (Operation-as-Code), supplied at
//     registration time — this is the "零代码接入 / Manifest 只管元数据" rule.
//   - This skeleton provides the Manifest types, validation, JSON (de)serialization
//     (so a future runtime loader can read a plugin's manifest file), and the
//     Module adapter that turns a Manifest + handler map into a builtin.Module.
//
// What is intentionally OUT of scope for this skeleton (later Phase 3.x): the
// dynamic .so / RPC loader, sandboxing, and host-registry capability hints.
package plugin

import (
	"encoding/json"
	"fmt"

	"github.com/YuDong999/opscore/internal/core"
)

// Risk is the serialized form of core.RiskLevel in a manifest. It marshals to
// the human-readable string ("low" | "medium" | "high" | "critical") used in
// plugin manifest files, and converts to core.RiskLevel via Level().
type Risk string

const (
	RiskLow      Risk = "low"
	RiskMedium   Risk = "medium"
	RiskHigh     Risk = "high"
	RiskCritical Risk = "critical"
)

// Level converts the manifest risk string to the kernel's core.RiskLevel.
func (r Risk) Level() (core.RiskLevel, error) {
	switch r {
	case RiskLow:
		return core.RiskLow, nil
	case RiskMedium:
		return core.RiskMedium, nil
	case RiskHigh:
		return core.RiskHigh, nil
	case RiskCritical:
		return core.RiskCritical, nil
	}
	return 0, fmt.Errorf("unknown risk %q (want low|medium|high|critical)", r)
}

// OperationDecl is the metadata for a single operation a plugin contributes.
// It carries no executable logic — the handler is supplied separately when the
// Module is built (see NewModule). This keeps the Manifest declarative.
type OperationDecl struct {
	Name         string `json:"name"`          // unique operation id, e.g. "demo.greet"
	ResourceType string `json:"resource_type"` // for Permission = ResourceType x Action
	Action       string `json:"action"`
	Risk         Risk   `json:"risk"` // low | medium | high | critical
	Description  string `json:"description,omitempty"`
}

// Manifest is the declarative identity of a plugin. Pure metadata; no code.
type Manifest struct {
	// SchemaVersion is the manifest format version (MUST fix, GPT review). Bump
	// it when the manifest shape changes so a future runtime loader can detect
	// and migrate older manifests. Must be >= 1.
	SchemaVersion int             `json:"schema_version"`
	Name          string          `json:"name"`    // plugin id, e.g. "demo-plugin"
	Version       string          `json:"version"` // semver
	Description   string          `json:"description,omitempty"`
	Operations    []OperationDecl `json:"operations"`
}

// Validate checks the Manifest is internally consistent: a non-empty name, a
// valid schema_version, and every operation carrying a name + permission + risk
// with no duplicates.
func (m Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("plugin manifest: missing name")
	}
	if m.SchemaVersion < 1 {
		return fmt.Errorf("plugin manifest: schema_version must be >= 1")
	}
	seen := map[string]bool{}
	for _, op := range m.Operations {
		if op.Name == "" {
			return fmt.Errorf("plugin %q: operation with empty name", m.Name)
		}
		if op.ResourceType == "" || op.Action == "" {
			return fmt.Errorf("plugin %q: operation %q missing resource_type/action", m.Name, op.Name)
		}
		if _, err := op.Risk.Level(); err != nil {
			return fmt.Errorf("plugin %q: operation %q: %w", m.Name, op.Name, err)
		}
		if seen[op.Name] {
			return fmt.Errorf("plugin %q: duplicate operation %q", m.Name, op.Name)
		}
		seen[op.Name] = true
	}
	return nil
}

// ModuleDescriptor bundles everything needed to register a module with the
// kernel (MUST fix, GPT review). It exists so Register's signature never has to
// grow with new concerns (resources, permissions, capabilities...): a future
// runtime loader hands the kernel a single Descriptor instead of N arguments.
// Today it carries the Manifest plus the Go handler map (Operation-as-Code);
// Resources/Permissions are declared inside the Manifest and will be surfaced
// here in a later 3.x step.
type ModuleDescriptor struct {
	Manifest Manifest
	Handlers map[string]core.Handler
}

// MarshalJSON exposes the manifest in the canonical on-disk format.
func (m Manifest) MarshalJSON() ([]byte, error) {
	type alias Manifest // avoid recursion
	return json.Marshal(alias(m))
}

// ParseManifest decodes a JSON manifest (metadata only). Handlers are supplied
// separately by the loader (Go functions cannot live in JSON).
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("plugin manifest: %w", err)
	}
	return m, nil
}
