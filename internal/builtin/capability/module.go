package capability

import (
	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
)

// module is the capability builtin family (compile-time plugin).
type module struct{}

// NewModule returns the capability builtin Module.
func NewModule() builtin.Module { return module{} }

func (module) Name() string { return "capability" }

func (module) Register(reg *core.Registry) {
	reg.Register(core.Operation{
		Name:       "system.host.capability.list",
		Permission: core.Permission{ResourceType: "system.host", Action: "read"},
		Risk:       core.RiskLow,
		Handler:    NewListHandler(),
	})
}
