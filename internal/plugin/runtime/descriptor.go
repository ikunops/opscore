package runtime

import (
	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

// Descriptor is the Runtime INTERNAL model of a plugin. It wraps the external
// Manifest with runtime-only fields. The Manifest is the external format; the
// Descriptor is what the Loader hands to the Manager. They are deliberately
// separate types (ADR: Manifest/Descriptor separation, GPT Round 6/7) so JSON
// can never leak into the runtime.
type Descriptor struct {
	// ID is the STABLE plugin identity, e.g. "mysql@1.0.0". Unlike
	// Manifest.Name (a Display Name that MAY later be renamed), the ID pins
	// the exact name@version so Audit / Execution / PluginRegistry always
	// reference the same loaded artifact (GPT Round 8 / MUST-A). It is
	// computed once at construction and never changed.
	ID string
	Manifest *manifest.Manifest
	// Source is the registration origin, "plugin:<name>". Copied onto every
	// core.Operation the plugin contributes (Phase 3.0 / MUST-1) so Audit
	// knows the capability's owner without a Storage join.
	Source string
	// State is the plugin's current position in the lifecycle state machine.
	State LifecycleState
	// frozen locks the DEFINITION (ID/Source/Manifest) after Load. Only
	// State may change thereafter (MUST-B: Descriptor immutable once Loaded).
	// A frozen descriptor must never have its Manifest/Version/ID/Source
	// reassigned at runtime.
	frozen bool
}

// NewDescriptor builds a Discovered descriptor, computing the stable ID from
// name@version. The definition fields are set here and must not be mutated.
func NewDescriptor(m *manifest.Manifest) Descriptor {
	id := m.Name + "@" + m.Version
	return Descriptor{
		ID:      id,
		Manifest: m,
		Source:   "plugin:" + id,
		State:    StateDiscovered,
	}
}

// Freeze locks the definition. Called by the Loader after Load; thereafter
// the descriptor's definition (ID/Source/Manifest) is read-only and only
// State may transition. It is a dev-time guard: code that reassigns
// Manifest/Version on a frozen descriptor is a contract violation.
func (d *Descriptor) Freeze() { d.frozen = true }

// IsFrozen reports whether the definition is locked (post-Load).
func (d *Descriptor) IsFrozen() bool { return d.frozen }
