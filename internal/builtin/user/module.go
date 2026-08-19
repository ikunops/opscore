// Package userop is the "user" builtin family (compile-time plugin). It manages
// POSIX user accounts via useradd/userdel/getent — tools that are POSIX-standard
// (present on every Linux distro and Alpine), so the platform Resolver is not
// consulted: there is no distro-specific variant to resolve.
package userop

import (
	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
)

// module is the user builtin family. Registered at startup by main.
type module struct{}

// NewModule returns the user builtin Module (compile-time plugin).
func NewModule() builtin.Module { return module{} }

func (module) Name() string { return "user" }

func (module) Register(reg *core.Registry) {
	reg.Register(core.Operation{
		Name:       "system.user.create",
		Permission: core.Permission{ResourceType: "system.user", Action: "create"},
		Risk:       core.RiskHigh,
		Handler:    NewCreateHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.user.delete",
		Permission: core.Permission{ResourceType: "system.user", Action: "delete"},
		Risk:       core.RiskHigh,
		Handler:    NewDeleteHandler(),
	})
	reg.Register(core.Operation{
		Name:       "system.user.list",
		Permission: core.Permission{ResourceType: "system.user", Action: "list"},
		Risk:       core.RiskLow,
		Handler:    NewListHandler(),
	})
}
