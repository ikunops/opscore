// Package packageop is the "package" builtin family (compile-time plugin).
// It expresses package-management INTENT only; the platform Resolver turns that
// intent into the distro-specific invocation (PackageCommand) based on the
// observed host identity. Handlers never name a distro tool or its flags.
package packageop

import (
	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
)

// module is the package builtin family. Registered at startup by main.
type module struct{}

// NewModule returns the package builtin Module (compile-time plugin).
func NewModule() builtin.Module { return module{} }

func (module) Name() string { return "package" }

func (module) Register(reg *core.Registry) {
	reg.Register(core.Operation{
		Name:       "system.package.install",
		Permission: core.Permission{ResourceType: "system.package", Action: "install"},
		Risk:       core.RiskMedium,
		Handler:    NewInstallHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.package.remove",
		Permission: core.Permission{ResourceType: "system.package", Action: "remove"},
		Risk:       core.RiskHigh,
		Handler:    NewRemoveHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.package.update",
		Permission: core.Permission{ResourceType: "system.package", Action: "update"},
		Risk:       core.RiskMedium,
		Handler:    NewUpdateHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.package.list",
		Permission: core.Permission{ResourceType: "system.package", Action: "list"},
		Risk:       core.RiskLow,
		Handler:    NewListHandler(),
	})
}
