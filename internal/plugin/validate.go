// Package plugin defines the Runtime Contract surfaces that let external
// capabilities join OpsCore WITHOUT eroding the core RBAC / Audit / Execution
// boundaries (Phase 3.0 prep, per the GPT Round 6 MUST set).
//
// This file holds the pure validation rules — no storage, no registry, no
// executor. It is imported by the synchronizer so a plugin can NEVER
// register a system-namespaced permission (e.g. execution.create) and pollute
// the control-plane RBAC graph.
package plugin

import (
	"fmt"
	"strings"

	"github.com/YuDong999/opscore/internal/core"
)

// reservedResources are system-owned resource types. A plugin MUST NOT claim
// any of these — doing so would let a plugin impersonate a builtin/system
// capability and bypass the isolation boundary.
var reservedResources = map[string]bool{
	"execution":  true,
	"system":     true,
	"host":       true,
	"disk":       true,
	"package":    true,
	"user":       true,
	"service":    true,
	"process":    true,
	"role":       true,
	"config":     true,
	"audit":      true,
	"capability": true,
}

// reservedPrefixes are plugin NAME segments that would let a plugin masquerade
// as a first-class system source (Round 7 SHOULD). A plugin named e.g.
// "builtin" would produce ops like "plugin.builtin.x.y" — rejected.
var reservedPrefixes = map[string]bool{
	"builtin":  true,
	"system":   true,
	"core":     true,
	"internal": true,
}

// ValidateOperation enforces the plugin operation namespace contract
// (MUST-2 of the Phase 3.0 prep):
//
//	plugin.<name>.<resource>.<action>
//
// It rejects:
//   - names not prefixed with "plugin."
//   - empty resource / action
//   - a resource type that collides with a reserved system resource
//
// Returns a descriptive error; nil means the operation is admissible.
func ValidateOperation(op core.Operation) error {
	if !strings.HasPrefix(op.Name, "plugin.") {
		return fmt.Errorf(
			"plugin operation %q must use the plugin.<name>.<resource>.<action> namespace",
			op.Name)
	}
	// The plugin NAME (segment right after "plugin.") must not be a reserved
	// system prefix (Round 7 SHOULD) — e.g. "plugin.builtin.x.y" is
	// rejected so a plugin cannot masquerade as a first-class source.
	nameSeg := op.Name[len("plugin."):]
	if i := strings.IndexByte(nameSeg, '.'); i >= 0 {
		nameSeg = nameSeg[:i]
	}
	if reservedPrefixes[nameSeg] {
		return fmt.Errorf(
			"plugin operation %q: plugin name %q is a reserved system prefix",
			op.Name, nameSeg)
	}
	if op.Permission.ResourceType == "" || op.Permission.Action == "" {
		return fmt.Errorf(
			"plugin operation %q: empty resource type or action is not allowed",
			op.Name)
	}
	if reservedResources[op.Permission.ResourceType] {
		return fmt.Errorf(
			"plugin operation %q may not claim reserved system resource %q",
			op.Name, op.Permission.ResourceType)
	}
	return nil
}
