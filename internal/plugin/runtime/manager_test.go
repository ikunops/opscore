package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/plugin/manifest"
	"github.com/YuDong999/opscore/internal/storage"
)

// syncCall records a SyncFunc invocation (the Permission Sync projection).
type syncCall struct {
	name string
	n    int
}

// newTestManager wires a Manager against MemoryStorage with a SyncFunc that
// simply registers the ops into the core Registry (miroring what the real
// control-plane synchronizer does on top of the Storage projection).
func newTestManager() (*Manager, *storage.MemoryStorage, *[]syncCall) {
	stor := storage.NewMemoryStorage()
	reg := core.NewRegistry()
	calls := &[]syncCall{}
	mgr := NewManager(reg, stor, func(name string, ops []core.Operation) error {
		*calls = append(*calls, syncCall{name, len(ops)})
		for _, op := range ops {
			// Mirror controlplane/sync.SyncPlugin: register into the core
			// Registry AND persist into Storage.Operations() so Enable/
			// Disable can flip the op's enabled flag.
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

func testModule(name string) *StaticModule {
	m := &manifest.Manifest{
		Name:    name,
		Version: "1.0.0",
		Operations: []manifest.OperationDecl{
			{Name: "plugin." + name + ".backup.execute", Resource: "backup", Action: "execute", Risk: "high"},
		},
	}
	return NewStaticModule(m, nil)
}

func testModuleCaps(name string, caps []string) *StaticModule {
	m := &manifest.Manifest{
		Name:       name,
		Version:    "1.0.0",
		Capabilities: caps,
		Operations: []manifest.OperationDecl{
			{Name: "plugin." + name + ".backup.execute", Resource: "backup", Action: "execute", Risk: "high"},
		},
	}
	return NewStaticModule(m, nil)
}

func TestManager_Lifecycle(t *testing.T) {
	mgr, stor, calls := newTestManager()
	loader := NewStaticLoader(map[string]Module{"mysql": testModule("mysql")})

	mods, errs := mgr.DiscoverAndLoad(context.Background(), loader, []string{"linux"})
	if len(errs) != 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	if len(mods) != 1 {
		t.Fatalf("want 1 module, got %d", len(mods))
	}

	p, err := stor.Plugins().Get("mysql@1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != string(StateLoaded) {
		t.Fatalf("want loaded, got %s", p.Status)
	}

	if err := mgr.Register("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mgr.Get("mysql@1.0.0"); !ok {
		t.Fatal("mysql should exist after register")
	}
	if len(*calls) != 1 {
		t.Fatalf("sync should be called once, got %d", len(*calls))
	}
	if p2, _ := stor.Plugins().Get("mysql@1.0.0"); p2.Status != string(StateRegistered) {
		t.Fatalf("want registered, got %s", p2.Status)
	}

	if err := mgr.Enable("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}
	if p3, _ := stor.Plugins().Get("mysql@1.0.0"); !p3.Enabled || p3.Status != string(StateEnabled) {
		t.Fatalf("want enabled, got %+v", p3)
	}

	// ENABLED -> UNLOAD must be rejected (must disable first).
	if err := mgr.Unload("mysql@1.0.0"); err == nil {
		t.Fatal("expected unload-from-enabled to be rejected")
	}

	if err := mgr.Disable("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}
	if p4, _ := stor.Plugins().Get("mysql@1.0.0"); p4.Enabled || p4.Status != string(StateDisabled) {
		t.Fatalf("want disabled, got %+v", p4)
	}

	if err := mgr.Unload("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}
	if p5, _ := stor.Plugins().Get("mysql@1.0.0"); p5.Status != string(StateUnloaded) {
		t.Fatalf("want unloaded, got %s", p5.Status)
	}
	if _, ok := mgr.Get("mysql@1.0.0"); ok {
		t.Fatal("mysql should be gone from manager after unload")
	}
}

func TestManager_CapabilitySkip(t *testing.T) {
	mgr, _, _ := newTestManager()
	// requires "linux", host satisfies it -> loaded
	ok, errs := mgr.DiscoverAndLoad(context.Background(),
		NewStaticLoader(map[string]Module{"mysql": testModuleCaps("mysql", []string{"linux"})}),
		[]string{"linux"})
	if len(errs) != 0 || len(ok) != 1 {
		t.Fatalf("expected load with satisfied cap, errs=%v mods=%d", errs, len(ok))
	}

	// requires "docker", host lacks it -> skipped with error
	skip, errs := mgr.DiscoverAndLoad(context.Background(),
		NewStaticLoader(map[string]Module{"mysql": testModuleCaps("mysql", []string{"docker"})}),
		[]string{"linux"})
	if len(skip) != 0 {
		t.Fatalf("expected skip, got %d modules", len(skip))
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 capability error, got %v", errs)
	}
}

func testModuleVer(name, version string) *StaticModule {
	m := &manifest.Manifest{
		Name:    name,
		Version: version,
		Operations: []manifest.OperationDecl{
			{Name: "plugin." + name + ".backup.execute", Resource: "backup", Action: "execute", Risk: "high"},
		},
	}
	return NewStaticModule(m, nil)
}

// idKeyedLoader is a test Loader keyed by the STABLE plugin ID (name@version)
// rather than the display Name. StaticLoader keys by Name and therefore
// cannot hold two modules that share a Name (mysql@1.0.0 vs mysql@2.0.0), so
// this loader exercises the multi-version coexistence path directly.
type idKeyedLoader struct {
	mods map[string]Module
}

func (l idKeyedLoader) Discover(ctx context.Context) []Descriptor {
	out := make([]Descriptor, 0, len(l.mods))
	for _, m := range l.mods {
		d := m.Descriptor()
		d.State = StateDiscovered
		d.frozen = false
		out = append(out, d)
	}
	return out
}

func (l idKeyedLoader) Load(desc Descriptor) (Module, error) {
	if m, ok := l.mods[desc.ID]; ok {
		return m, nil
	}
	return nil, fmt.Errorf("idKeyedLoader: no module for %q", desc.ID)
}

func (l idKeyedLoader) Unload(name string) error { return nil }

// TestManager_CoexistDifferentVersions proves Phase 3.4.1: two plugins with the
// SAME display Name but DIFFERENT versions coexist. Manager is keyed by the
// stable ID (name@version), not the mutable display Name, so mysql@1.0.0 and
// mysql@2.0.0 load as two distinct artifacts instead of clobbering each other.
func TestManager_CoexistDifferentVersions(t *testing.T) {
	mgr, stor, _ := newTestManager()
	loader := idKeyedLoader{mods: map[string]Module{
		"mysql@1.0.0": testModuleVer("mysql", "1.0.0"),
		"mysql@2.0.0": testModuleVer("mysql", "2.0.0"),
	}}

	mods, errs := mgr.DiscoverAndLoad(context.Background(), loader, []string{"linux"})
	if len(errs) != 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	if len(mods) != 2 {
		t.Fatalf("want 2 modules (two versions coexist), got %d", len(mods))
	}

	// Both IDs are present in the Manager — NOT overwritten by the shared
	// display Name "mysql".
	if _, ok := mgr.Get("mysql@1.0.0"); !ok {
		t.Fatal("mysql@1.0.0 missing after load")
	}
	if _, ok := mgr.Get("mysql@2.0.0"); !ok {
		t.Fatal("mysql@2.0.0 missing after load")
	}

	// Durable storage keeps BOTH rows under distinct IDs.
	all, err := stor.Plugins().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 persisted rows, got %d", len(all))
	}
}

// TestManager_CapabilityAudit proves Phase 3.7: a plugin whose required
// capability is missing is SKIPPED AND a "capability" audit event is emitted
// with result=failure and code=missing-capability (so operators can trace
// per-plugin negotiation outcomes).
func TestManager_CapabilityAudit(t *testing.T) {
	mgr, stor, _ := newTestManager()
	_, errs := mgr.DiscoverAndLoad(context.Background(),
		NewStaticLoader(map[string]Module{"mysql": testModuleCaps("mysql", []string{"docker"})}),
		[]string{"linux"})
	if len(errs) != 1 {
		t.Fatalf("expected 1 capability error, got %v", errs)
	}
	events, err := stor.Audit().List(1000)
	if err != nil {
		t.Fatal(err)
	}
	var capEvt bool
	for _, e := range events {
		if e.Action == "capability" && e.Result == "failure" && contains(e.Detail, "missing-capability") {
			capEvt = true
		}
	}
	if !capEvt {
		t.Fatalf("expected a capability audit event with code=missing-capability; events=%+v", events)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

var _ storage.Storage = (*storage.MemoryStorage)(nil)
