package plugin

import (
	"fmt"

	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/plugin/sandbox"
)

// Module is a plugin expressed as a builtin.Module. The kernel cannot tell it
// apart from an in-tree builtin: it has a Name() and a Register() that wires its
// operations into the same core.Registry the builtins use. There is never an
// "if plugin" branch in the kernel.
type Module struct {
	manifest Manifest
	handlers map[string]core.Handler // keyed by operation name
}

// NewModule builds a plugin Module from a Manifest plus a handler for every
// declared operation. It enforces the architecture rule that the Manifest is
// metadata only: each declared operation MUST have a matching Go handler, and
// the Manifest must Validate().
func NewModule(m Manifest, handlers map[string]core.Handler) (*Module, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if handlers == nil {
		handlers = map[string]core.Handler{}
	}
	for _, op := range m.Operations {
		if _, ok := handlers[op.Name]; !ok {
			return nil, fmt.Errorf("plugin %q: declared operation %q has no handler", m.Name, op.Name)
		}
	}
	return &Module{manifest: m, handlers: handlers}, nil
}

// NewModuleFromDescriptor builds a plugin Module from a ModuleDescriptor, the
// single-bundle entry point for registration (MUST fix, GPT review). A future
// runtime loader hands the kernel one Descriptor instead of growing Register's
// argument list.
func NewModuleFromDescriptor(d ModuleDescriptor) (*Module, error) {
	return NewModule(d.Manifest, d.Handlers)
}

// Name implements builtin.Module.
func (m *Module) Name() string { return m.manifest.Name }

// Register implements builtin.Module: wires every declared operation (with its
// Go handler) into the kernel Registry. Identical shape to a builtin module.
func (m *Module) Register(reg *core.Registry) {
	for _, op := range m.manifest.Operations {
		lvl, _ := op.Risk.Level() // validated in NewModule
		coreOp := core.Operation{
			Name:       op.Name,
			Permission: core.Permission{ResourceType: op.ResourceType, Action: op.Action},
			Risk:       lvl,
			Handler:    m.handlers[op.Name],
		}
		// Phase 6.1: wrap the handler in the peripheral isolation envelope
		// (exec timeout + permission/risk escalation fail-closed). Zero Runtime
		// Contract change; Wrap is idempotent so this path and the Manager's
		// runtime.Module.Operations() path can never double-wrap.
		coreOp.Handler = sandbox.Wrap(coreOp.Handler, sandbox.DefaultEnvelope(coreOp))
		reg.Register(coreOp)
	}
}

// Manifest returns the plugin's declarative metadata (handy for the Control
// Plane's plugin inventory / UI, which only ever sees metadata).
func (m *Module) Manifest() Manifest { return m.manifest }

var _ builtin.Module = (*Module)(nil)
