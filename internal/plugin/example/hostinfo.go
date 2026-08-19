// Package example is a minimal, end-to-end reference plugin for the OpsCore
// Runtime Contract (ADR-010). It demonstrates how a real plugin — a manifest
// plus ordinary Go handler code — plugs into the frozen Runtime Contract and
// is driven all the way through to Execution + Audit, WITHOUT any change to
// the contract itself (Phase 4.2 / GPT Round 18).
//
// The chain exercised here (GPT Round 18):
//
//	Manifest -> Provider -> Loader -> Compatibility Gate -> Capability
//	Negotiation -> Register -> Enable -> Dispatcher -> Execution -> Audit
//
// No Runtime Contract field, interface, or lifecycle state is added; the
// example uses only the public surfaces frozen in ADR-010 §1-§12.
package example

import (
	"context"
	"runtime"
	"time"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/plugin/manifest"
	pluginruntime "github.com/YuDong999/opscore/internal/plugin/runtime"
)

// Manifest returns the external declaration of the hostinfo plugin. It is the
// data a future FileProvider/OCI provider would read from disk; here it is
// returned directly by the in-memory Provider (see staticProvider).
func Manifest() *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: 1,
		Name:          "hostinfo",
		Version:       "1.0.0",
		// The plugin's OWN runtime requirement (Phase 3.5 Capability
		// Negotiation): it needs the "os.linux" capability on the host.
		Capabilities: []string{"os.linux"},
		// Compatibility Gate inputs (Phase 3.5): built against plugin API v1,
		// needs kernel >= 0.1.0.
		PluginAPI: "opscore.plugin/v1",
		MinKernel: "0.1.0",
		Operations: []manifest.OperationDecl{
			{Name: "plugin.hostinfo.collect", Resource: "hostinfo", Action: "collect", Risk: "low"},
		},
	}
}

// collectHandler is the plugin's real Go operation handler (Operation-as-Code).
// Per the Isolation Boundary (ADR-010 §4) it only PLANS: it returns
// ExecutionSteps that the Executor — the sole command runner — executes.
type collectHandler struct{}

func (h *collectHandler) Plan(_ core.Context, _ map[string]any) (*core.ExecutionPlan, error) {
	var steps []core.ExecutionStep
	if runtime.GOOS == "windows" {
		// Portable fallback so the example runs on a Windows dev box; a
		// production Linux plugin would target "uname"/"uptime"/"free".
		steps = append(steps, &core.CommandStep{
			Name: "os-version", Executable: "cmd", Args: []string{"/c", "ver"}, Timeout: 10 * time.Second,
		})
	} else {
		steps = append(steps,
			&core.CommandStep{Name: "kernel", Executable: "uname", Args: []string{"-a"}, Timeout: 10 * time.Second},
			&core.CommandStep{Name: "uptime", Executable: "uptime", Timeout: 10 * time.Second},
			&core.CommandStep{Name: "memory", Executable: "free", Args: []string{"-h"}, Timeout: 10 * time.Second},
		)
	}
	// Handlers return an EMPTY Permission/Risk: the Dispatcher stamps
	// plan.Permission from the registered operation, and Risk is a property of
	// the declared operation. A handler that tried to return a mismatched or
	// escalated permission/risk is denied by the Phase 6.1 sandbox envelope.
	return &core.ExecutionPlan{
		OperationName: "plugin.hostinfo.collect",
		Steps:         steps,
	}, nil
}

// Loader is the example's runtime.Loader. It sources the manifest from an
// in-memory Provider (mirroring how FileLoader uses a Provider) and binds the
// plugin's Go handlers when loading — the one piece FileLoader cannot supply,
// because handlers are Go code, not JSON.
type Loader struct {
	provider manifest.Provider
	handlers map[string]core.Handler
}

// NewLoader builds the example Loader with its single collect operation.
func NewLoader() *Loader {
	return &Loader{
		provider: &staticProvider{man: Manifest()},
		handlers: map[string]core.Handler{
			"plugin.hostinfo.collect": &collectHandler{},
		},
	}
}

// Discover implements runtime.Loader.
func (l *Loader) Discover(ctx context.Context) []pluginruntime.Descriptor {
	keys, err := l.provider.List()
	if err != nil {
		return nil
	}
	var descs []pluginruntime.Descriptor
	for _, k := range keys {
		m, err := l.provider.Read(k)
		if err != nil {
			continue
		}
		descs = append(descs, pluginruntime.NewDescriptor(m))
	}
	return descs
}

// Load implements runtime.Loader. It closes the loop with REAL handlers so the
// operation can actually execute (FileLoader passes nil handlers — metadata
// only).
func (l *Loader) Load(d pluginruntime.Descriptor) (pluginruntime.Module, error) {
	return pluginruntime.NewStaticModule(d.Manifest, l.handlers), nil
}

// Unload implements runtime.Loader (no process-local state to release for an
// in-memory source).
func (l *Loader) Unload(name string) error { return nil }

// staticProvider is a tiny in-memory manifest.Provider for the example.
type staticProvider struct{ man *manifest.Manifest }

func (p *staticProvider) List() ([]string, error) { return []string{p.man.Name}, nil }
func (p *staticProvider) Read(key string) (*manifest.Manifest, error) {
	return p.man, nil
}
