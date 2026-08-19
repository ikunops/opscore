package firewall

import (
	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
)

// module is the firewall builtin family (compile-time plugin).
type module struct{}

// NewModule returns the firewall builtin Module.
func NewModule() builtin.Module { return module{} }

func (module) Name() string { return "firewall" }

func (module) Register(reg *core.Registry) {
	reg.Register(core.Operation{
		Name:       "firewall.rule.add",
		Permission: core.Permission{ResourceType: "firewall.rule", Action: "add"},
		Risk:       core.RiskHigh,
		Handler:    NewAddHandler(),
	})
	reg.Register(core.Operation{
		Name:       "firewall.rule.remove",
		Permission: core.Permission{ResourceType: "firewall.rule", Action: "remove"},
		Risk:       core.RiskHigh,
		Handler:    NewRemoveHandler(),
	})
	reg.Register(core.Operation{
		Name:       "firewall.rule.list",
		Permission: core.Permission{ResourceType: "firewall.rule", Action: "list"},
		Risk:       core.RiskLow,
		Handler:    NewListHandler(),
	})
}
