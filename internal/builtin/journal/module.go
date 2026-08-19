package journal

import (
	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
)

// module is the journal builtin family (compile-time plugin).
type module struct{}

// NewModule returns the journal builtin Module.
func NewModule() builtin.Module { return module{} }

func (module) Name() string { return "journal" }

func (module) Register(reg *core.Registry) {
	reg.Register(core.Operation{
		Name:       "system.journal.log",
		Permission: core.Permission{ResourceType: "system.journal", Action: "read"},
		Risk:       core.RiskLow,
		Handler:    NewLogHandler(),
	})
}
