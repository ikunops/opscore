// Package capability provides the system.host.capability.list Operation — the
// read-only, low-risk entry point into Capability Discovery.
//
// The Handler returns a single builtin CollectStep (NOT a CommandStep): probing
// the host is kernel state, not a system mutation, so it must not be expressed
// as a shell command. See internal/core/capability for the rationale.
package capability

import (
	"github.com/YuDong999/opscore/internal/core"
	corecap "github.com/YuDong999/opscore/internal/core/capability"
)

// ListHandler answers "what can this host do?" as a structured snapshot.
// It is safe to expose to any reader: it changes nothing, only observes.
type ListHandler struct{}

func NewListHandler() *ListHandler { return &ListHandler{} }

func (h *ListHandler) Plan(_ core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // read-only, no parameters
	return &core.ExecutionPlan{
		OperationName: "system.host.capability.list",
		Permission:    core.Permission{ResourceType: "system.host", Action: "read"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			// Builtin step — computes the snapshot in-process (no shell).
			&corecap.CollectStep{},
		},
	}, nil
}
