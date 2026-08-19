package process

import (
	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
)

// module is the process builtin family (compile-time plugin).
type module struct{}

// NewModule returns the process builtin Module.
func NewModule() builtin.Module { return module{} }

func (module) Name() string { return "process" }

func (module) Register(reg *core.Registry) {
	reg.Register(core.Operation{
		Name:       "system.process.list",
		Permission: core.Permission{ResourceType: "system.process", Action: "list"},
		Risk:       core.RiskLow,
		Handler:    NewListHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.process.kill",
		Permission: core.Permission{ResourceType: "system.process", Action: "kill"},
		Risk:       core.RiskHigh,
		Handler:    NewKillHandler(),
	})
}
