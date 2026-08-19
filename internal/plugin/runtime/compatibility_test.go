package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/plugin/compat"
	"github.com/YuDong999/opscore/internal/plugin/manifest"
	"github.com/YuDong999/opscore/internal/storage"
)

// incompatibleLoader returns a single descriptor whose MinKernel is higher
// than the kernel we configure on the Manager, so the Compatibility Gate
// must reject it before Load.
func incompatibleLoader() Loader {
	m := &manifest.Manifest{
		Name:    "legacy",
		Version: "1.0.0",
		// Requires a newer kernel than the Manager will be configured with.
		MinKernel: "0.2.0",
		Operations: []manifest.OperationDecl{
			{Name: "plugin.legacy.backup.execute", Resource: "backup", Action: "execute", Risk: "high"},
		},
	}
	return NewStaticLoader(map[string]Module{"legacy": NewStaticModule(m, nil)})
}

// TestManager_CompatibilityGateRejects proves Phase 3.5.2: a plugin the Gate
// refuses is (a) never loaded into the Manager, (b) persisted with status
// "rejected", and (c) recorded by an audit event with Action="load" /
// Result="failure" so an operator can see WHY it is absent.
func TestManager_CompatibilityGateRejects(t *testing.T) {
	mgr, stor, _ := newTestManager()
	// Kernel is OLDER than the plugin's MinKernel (0.2.0) -> must reject.
	mgr.SetKernel(compat.KernelInfo{Version: "0.1.0", SupportedAPIs: []string{"opscore.plugin/v1"}})

	mods, errs := mgr.DiscoverAndLoad(context.Background(), incompatibleLoader(), []string{"linux"})
	if len(mods) != 0 {
		t.Fatalf("expected 0 loaded modules, got %d", len(mods))
	}
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 compatibility rejection error, got %v", errs)
	}
	if !strings.Contains(errs[0].Error(), "compatibility gate rejected") {
		t.Fatalf("error should mention the gate: %v", errs[0])
	}

	// The plugin must NOT be in the runtime Manager.
	if _, ok := mgr.Get("legacy@1.0.0"); ok {
		t.Fatal("rejected plugin must not enter the Manager")
	}

	// Durable status must be "rejected".
	p, err := stor.Plugins().Get("legacy@1.0.0")
	if err != nil {
		t.Fatalf("rejected plugin should still be persisted: %v", err)
	}
	if p.Status != storage.PluginRejected {
		t.Fatalf("status = %q, want %q", p.Status, storage.PluginRejected)
	}
	if p.Enabled {
		t.Fatal("rejected plugin must not be enabled")
	}

	// Audit: a PluginLoadRejected event must exist.
	events, err := stor.Audit().List(100)
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.Action == "load" && e.Result == "failure" && strings.Contains(e.Detail, "compatibility gate rejected") && e.Operation == "legacy@1.0.0" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a compatibility-rejection audit event, got %+v", events)
	}
}

// TestManager_CompatibilityGateAllows proves the Gate does NOT interfere when
// the plugin is compatible: kernel >= MinKernel and PluginAPI supported.
func TestManager_CompatibilityGateAllows(t *testing.T) {
	mgr, _, _ := newTestManager()
	mgr.SetKernel(compat.KernelInfo{Version: "0.3.0", SupportedAPIs: []string{"opscore.plugin/v1"}})

	// Reuse twoPluginLoader but the manifests have no MinKernel/PluginAPI, so
	// they pass the Gate and load normally.
	mods, errs := mgr.DiscoverAndLoad(context.Background(), twoPluginLoader(), []string{"linux"})
	if len(errs) != 0 {
		t.Fatalf("expected no gate errors for unconstrained plugins, got %v", errs)
	}
	if len(mods) != 2 {
		t.Fatalf("want 2 modules, got %d", len(mods))
	}
}
