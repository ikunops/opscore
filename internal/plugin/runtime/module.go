package runtime

import (
	"fmt"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/plugin/manifest"
	"github.com/YuDong999/opscore/internal/plugin/sandbox"
)

// Module is a loaded plugin's runtime handle. The Manager calls Operations()
// to register the plugin's capabilities into the core Registry; each returned
// Operation's Handler MUST build an ExecutionPlan that the Dispatcher routes
// to the Executor (SSOT).
//
// A plugin Handler receives core.Context, which exposes NO exec/SSH/shell
// primitive — it can only PLAN. That type-level fact IS the Isolation
// Boundary (see ADR-010): a plugin literally cannot run a command except
// by returning steps that the Executor (the sole command runner) executes.
type Module interface {
	// Descriptor returns the plugin's runtime descriptor.
	Descriptor() Descriptor
	// Operations returns the plugin's capabilities as core.Operations, ready
	// to register into the core Registry. Each Handler.Plan builds an
	// ExecutionPlan (never calls the OS directly).
	Operations() []core.Operation
}

// riskFromString maps a manifest risk string to core.RiskLevel.
func riskFromString(s string) core.RiskLevel {
	switch s {
	case "low":
		return core.RiskLow
	case "medium":
		return core.RiskMedium
	case "high":
		return core.RiskHigh
	case "critical":
		return core.RiskCritical
	default:
		return core.RiskLow
	}
}

// StaticModule is a test/contract Module: it wraps a Manifest plus a map of
// operation-name -> handler. It lets the contract be exercised end-to-end
// (Discover -> Load -> Register -> Dispatcher.Plan) without any real plugin
// code or .so loading.
type StaticModule struct {
	desc    Descriptor
	handlers map[string]core.Handler
}

// NewStaticModule builds a Module from a manifest and a handler map. Any
// declared operation without a supplied handler gets a no-op handler so the
// contract never panics at plan time. The descriptor is constructed via
// NewDescriptor (stable ID) and Freeze()d — a static module IS already
// loaded, so its state is StateLoaded and its definition is immutable.
func NewStaticModule(m *manifest.Manifest, handlers map[string]core.Handler) *StaticModule {
	desc := NewDescriptor(m)
	desc.State = StateLoaded
	desc.Freeze()
	if handlers == nil {
		handlers = map[string]core.Handler{}
	}
	return &StaticModule{desc: desc, handlers: handlers}
}

func (m *StaticModule) Descriptor() Descriptor { return m.desc }

func (m *StaticModule) Operations() []core.Operation {
	ops := make([]core.Operation, 0, len(m.desc.Manifest.Operations))
	for _, od := range m.desc.Manifest.Operations {
		h, ok := m.handlers[od.Name]
		if !ok {
			h = noopHandler{}
		}
		op := core.Operation{
			Name:       od.Name,
			Permission: core.Permission{ResourceType: od.Resource, Action: od.Action},
			Risk:       riskFromString(od.Risk),
			Handler:    h,
		}
		// Phase 6.1: wrap the handler in the peripheral isolation envelope so
		// the plan is bounded by an exec timeout and checked against the
		// plugin's declared permission/risk envelope before the Executor runs
		// any step. Zero Runtime Contract change; Wrap is idempotent.
		op.Handler = sandbox.Wrap(op.Handler, sandbox.DefaultEnvelope(op))
		ops = append(ops, op)
	}
	return ops
}

// noopHandler is a safe default for declared-but-unimplemented operations.
type noopHandler struct{}

func (noopHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	return nil, fmt.Errorf("plugin operation not implemented")
}
