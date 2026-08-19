package service

import (
	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
)

// module is the service builtin family. Registered at startup by main.
type module struct{}

// NewModule returns the service builtin Module (compile-time plugin).
func NewModule() builtin.Module { return module{} }

func (module) Name() string { return "service" }

func (module) Register(reg *core.Registry) {
	reg.Register(core.Operation{
		Name:       "system.service.restart",
		Permission: core.Permission{ResourceType: "system.service", Action: "restart"},
		Risk:       core.RiskMedium,
		Handler:    NewRestartHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.service.list",
		Permission: core.Permission{ResourceType: "system.service", Action: "list"},
		Risk:       core.RiskLow,
		Handler:    NewListHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.service.start",
		Permission: core.Permission{ResourceType: "system.service", Action: "start"},
		Risk:       core.RiskMedium,
		Handler:    NewStartHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.service.stop",
		Permission: core.Permission{ResourceType: "system.service", Action: "stop"},
		Risk:       core.RiskMedium,
		Handler:    NewStopHandler(),
	})
}
