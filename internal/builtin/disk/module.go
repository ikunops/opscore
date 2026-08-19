// Package diskop is the "disk" builtin family (compile-time plugin). It provides
// read-only inspection of the target's storage: filesystem usage, block devices,
// and active mounts. All operations are low-risk and never mutate state.
package diskop

import (
	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
)

// module is the disk builtin family. Registered at startup by main.
type module struct{}

// NewModule returns the disk builtin Module (compile-time plugin).
func NewModule() builtin.Module { return module{} }

func (module) Name() string { return "disk" }

func (module) Register(reg *core.Registry) {
	reg.Register(core.Operation{
		Name:       "system.disk.usage",
		Permission: core.Permission{ResourceType: "system.disk", Action: "usage"},
		Risk:       core.RiskLow,
		Handler:    NewUsageHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.disk.list",
		Permission: core.Permission{ResourceType: "system.disk", Action: "list"},
		Risk:       core.RiskLow,
		Handler:    NewListHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.disk.mounts",
		Permission: core.Permission{ResourceType: "system.disk", Action: "mounts"},
		Risk:       core.RiskLow,
		Handler:    NewMountsHandler(),
	})
}
