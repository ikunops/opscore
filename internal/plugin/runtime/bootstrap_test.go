package runtime

import (
	"context"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/storage"
)

// twoPluginLoader returns a StaticLoader with two contract plugins.
func twoPluginLoader() Loader {
	return NewStaticLoader(map[string]Module{
		"mysql": testModule("mysql"),
		"redis": testModule("redis"),
	})
}

// TestBootstrap_FirstBoot enables every newly-discovered plugin so the
// skeleton closes the loop: after Bootstrap, each plugin's op is in the
// core Registry (Dispatcher can Plan) and its Storage projection is enabled
// (RBAC authorizes).
func TestBootstrap_FirstBoot(t *testing.T) {
	mgr, stor, _ := newTestManager()

	enabled, errs := mgr.Bootstrap(context.Background(), twoPluginLoader(), nil, BootstrapPolicy{AutoEnableNewPlugin: true})
	if len(errs) != 0 {
		t.Fatalf("bootstrap errors: %v", errs)
	}
	if len(enabled) != 2 {
		t.Fatalf("want 2 enabled, got %d: %v", len(enabled), enabled)
	}

	for _, name := range []string{"mysql", "redis"} {
		id := name + "@1.0.0"
		if _, ok := mgr.Get(id); !ok {
			t.Fatalf("plugin %q should be managed after bootstrap", id)
		}
		p, err := stor.Plugins().Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if !p.Enabled || p.Status != string(StateEnabled) {
			t.Fatalf("plugin %q: want enabled, got %+v", name, p)
		}
		// Permission Sync projection: the op exists in Storage and is enabled.
		opName := "plugin." + name + ".backup.execute"
		ops, err := stor.Operations().List()
		if err != nil {
			t.Fatalf("plugin %q op list: %v", name, err)
		}
		var found bool
		for _, o := range ops {
			if o.Name == opName {
				found = true
				if !o.Enabled {
					t.Fatalf("plugin %q op should be enabled", name)
				}
			}
		}
		if !found {
			t.Fatalf("plugin %q op %q missing in storage", name, opName)
		}
	}
}

// TestBootstrap_RestartRestoresState simulates a process restart: a brand-new
// Manager over the SAME durable Storage. The Loader re-supplies the
// DEFINITION; the plugin_registry row restores only STATE (enabled). Both
// plugins were enabled on first boot, so they come back enabled (ADR-010).
func TestBootstrap_RestartRestoresState(t *testing.T) {
	mgr1, stor, _ := newTestManager()
	if _, errs := mgr1.Bootstrap(context.Background(), twoPluginLoader(), nil, BootstrapPolicy{AutoEnableNewPlugin: true}); len(errs) != 0 {
		t.Fatalf("first boot: %v", errs)
	}

	// Restart: fresh Manager, same Storage, same Loader.
	mgr2, _, _ := newTestManagerFrom(stor)
	if _, errs := mgr2.Bootstrap(context.Background(), twoPluginLoader(), nil, BootstrapPolicy{AutoEnableNewPlugin: true}); len(errs) != 0 {
		t.Fatalf("restart boot: %v", errs)
	}
	for _, name := range []string{"mysql", "redis"} {
		id := name + "@1.0.0"
		p, err := stor.Plugins().Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if !p.Enabled || p.Status != string(StateEnabled) {
			t.Fatalf("restart: %q want enabled, got %+v", name, p)
		}
	}
}

// TestBootstrap_DisabledStaysDisabled proves the Registry restores STATE only:
// a plugin disabled before restart is NOT re-enabled on reboot (a disabled
// plugin stays disabled across restarts).
func TestBootstrap_DisabledStaysDisabled(t *testing.T) {
	mgr1, stor, _ := newTestManager()
	if _, errs := mgr1.Bootstrap(context.Background(), twoPluginLoader(), nil, BootstrapPolicy{AutoEnableNewPlugin: true}); len(errs) != 0 {
		t.Fatalf("first boot: %v", errs)
	}
	// Operator disables redis before the "restart".
	if err := mgr1.Disable("redis@1.0.0"); err != nil {
		t.Fatal(err)
	}
	if p, _ := stor.Plugins().Get("redis@1.0.0"); p.Enabled {
		t.Fatal("redis should be disabled before restart")
	}

	// Restart.
	mgr2, _, _ := newTestManagerFrom(stor)
	enabled, errs := mgr2.Bootstrap(context.Background(), twoPluginLoader(), nil, BootstrapPolicy{AutoEnableNewPlugin: true})
	if len(errs) != 0 {
		t.Fatalf("restart boot: %v", errs)
	}
	if len(enabled) != 1 || enabled[0] != "mysql@1.0.0" {
		t.Fatalf("restart: want only mysql enabled, got %v", enabled)
	}
	// redis: known + disabled -> stays Registered/Disabled, NOT enabled.
	p, err := stor.Plugins().Get("redis@1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled {
		t.Fatal("redis must stay disabled across restart")
	}
	if p.Status != string(StateDisabled) && p.Status != string(StateRegistered) {
		t.Fatalf("redis status = %q, want disabled/registered", p.Status)
	}
	// Definition still present (Dispatcher can Plan; RBAC denies via enabled=false).
	if _, ok := mgr2.Get("redis@1.0.0"); !ok {
		t.Fatal("redis definition must survive restart (Loader supplies it)")
	}
}

// newTestManagerFrom reuses an existing Storage (to simulate a restart over
// the same durable store) while registering ops into a fresh core Registry.
func newTestManagerFrom(stor *storage.MemoryStorage) (*Manager, *storage.MemoryStorage, *[]syncCall) {
	reg := core.NewRegistry()
	calls := &[]syncCall{}
	mgr := NewManager(reg, stor, func(name string, ops []core.Operation) error {
		for _, op := range ops {
			reg.Register(op)
			if _, err := stor.Operations().Save(storage.Operation{
				Name:         op.Name,
				ResourceType: op.Permission.ResourceType,
				ActionType:   op.Permission.Action,
				Risk:         op.Risk.String(),
				Source:       op.Source,
				Enabled:      true,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return mgr, stor, calls
}
