package host

import (
	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
)

// module is the host builtin family (compile-time plugin).
type module struct{}

// NewModule returns the host builtin Module.
func NewModule() builtin.Module { return module{} }

func (module) Name() string { return "host" }

func (module) Register(reg *core.Registry) {
	reg.Register(core.Operation{
		Name:       "system.host.info",
		Permission: core.Permission{ResourceType: "system.host", Action: "info"},
		Risk:       core.RiskLow,
		Handler:    NewInfoHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.host.reboot",
		Permission: core.Permission{ResourceType: "system.host", Action: "reboot"},
		Risk:       core.RiskCritical,
		Handler:    NewRebootHandler(),
	})
}
