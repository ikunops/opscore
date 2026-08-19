package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
	"github.com/YuDong999/opscore/internal/storage"
)

// testModuleOps builds a StaticModule with an explicit operation set (still
// version 1.0.0, so its stable ID stays "<name>@1.0.0" — used to test a
// SAME-ID definition refresh, i.e. a Reload rather than an Upgrade).
func testModuleOps(name string, ops []manifest.OperationDecl) *StaticModule {
	m := &manifest.Manifest{Name: name, Version: "1.0.0", Operations: ops}
	return NewStaticModule(m, nil)
}

func testModuleOpsVer(name, version string, ops []manifest.OperationDecl) *StaticModule {
	m := &manifest.Manifest{Name: name, Version: version, Operations: ops}
	return NewStaticModule(m, nil)
}

func findAudit(t *testing.T, stor *storage.MemoryStorage, action, result string) storage.AuditEvent {
	t.Helper()
	events, err := stor.Audit().List(1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Action == action && e.Result == result {
			return e
		}
	}
	t.Fatalf("audit event action=%q result=%q not found (got %d events)", action, result, len(events))
	return storage.AuditEvent{}
}

// TestManager_Reload_Success proves the happy path: a same-ID definition
// refresh swaps the in-memory Module, keeps the original lifecycle State
// (Enabled stays Enabled), bumps the generation, and audits ReloadSucceeded.
func TestManager_Reload_Success(t *testing.T) {
	mgr, stor, _ := newTestManager()
	loader := NewStaticLoader(map[string]Module{"mysql": testModule("mysql")})

	if _, errs := mgr.DiscoverAndLoad(context.Background(), loader, []string{"linux"}); len(errs) != 0 {
		t.Fatalf("load errs: %v", errs)
	}
	if err := mgr.Register("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Enable("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}

	// Reload with a definition that adds a second operation (same ID).
	reloadMod := testModuleOps("mysql", []manifest.OperationDecl{
		{Name: "plugin.mysql.backup.execute", Resource: "backup", Action: "execute", Risk: "high"},
		{Name: "plugin.mysql.backup.restore", Resource: "backup", Action: "restore", Risk: "high"},
	})
	reloadLoader := NewStaticLoader(map[string]Module{"mysql": reloadMod})

	if err := mgr.Reload(context.Background(), "mysql@1.0.0", reloadLoader, []string{"linux"}); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// Definition refreshed: 2 operations now.
	d, ok := mgr.Get("mysql@1.0.0")
	if !ok {
		t.Fatal("mysql gone after reload")
	}
	if len(d.Manifest.Operations) != 2 {
		t.Fatalf("want 2 ops after reload, got %d", len(d.Manifest.Operations))
	}
	// State preserved (was Enabled).
	if d.State != StateEnabled {
		t.Fatalf("reload must keep State, want enabled got %s", d.State)
	}
	if p, _ := stor.Plugins().Get("mysql@1.0.0"); !p.Enabled || p.Status != string(StateEnabled) {
		t.Fatalf("reload must keep enabled, got %+v", p)
	}
	// New op projected into core/Storage.
	ops, _ := stor.Operations().List()
	if len(ops) != 2 {
		t.Fatalf("want 2 stored ops after reload, got %d", len(ops))
	}
	// Generation bumped + audit success.
	if g := mgr.ReloadCount("mysql@1.0.0"); g != 1 {
		t.Fatalf("want generation 1, got %d", g)
	}
	ev := findAudit(t, stor, "reload", "success")
	if !strings.Contains(ev.Detail, "generation=1") {
		t.Fatalf("success audit missing generation: %q", ev.Detail)
	}
}

// TestManager_Reload_MUST1_IDMismatch proves a version bump at the source is
// rejected as an UPGRADE (not a Reload) and the old plugin is left intact.
func TestManager_Reload_MUST1_IDMismatch(t *testing.T) {
	mgr, stor, _ := newTestManager()
	loader := NewStaticLoader(map[string]Module{"mysql": testModule("mysql")})
	if _, errs := mgr.DiscoverAndLoad(context.Background(), loader, []string{"linux"}); len(errs) != 0 {
		t.Fatalf("load errs: %v", errs)
	}
	if err := mgr.Register("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Enable("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}

	// Source now declares mysql@1.3.0 (different ID) — this is an Upgrade.
	upgradeMod := testModuleOpsVer("mysql", "1.3.0", []manifest.OperationDecl{
		{Name: "plugin.mysql.backup.execute", Resource: "backup", Action: "execute", Risk: "high"},
	})
	upgradeLoader := NewStaticLoader(map[string]Module{"mysql": upgradeMod})

	err := mgr.Reload(context.Background(), "mysql@1.0.0", upgradeLoader, []string{"linux"})
	if err == nil || !strings.Contains(err.Error(), "Upgrade") {
		t.Fatalf("expected Upgrade rejection, got %v", err)
	}
	// Old plugin intact: still present, still Enabled, still 1 op.
	d, ok := mgr.Get("mysql@1.0.0")
	if !ok {
		t.Fatal("old plugin should remain")
	}
	if len(d.Manifest.Operations) != 1 || d.State != StateEnabled {
		t.Fatalf("old plugin mutated: %+v", d)
	}
	ev := findAudit(t, stor, "reload", "failure")
	if !strings.Contains(ev.Detail, "id-mismatch") {
		t.Fatalf("want id-mismatch audit, got %q", ev.Detail)
	}
}

// TestManager_Reload_MUST3_FailureKeepsState proves strong exception safety:
// a Load failure aborts the Reload and leaves the previously-Enabled plugin
// fully working with its State untouched (no Disabled/Unloaded half-state).
func TestManager_Reload_MUST3_FailureKeepsState(t *testing.T) {
	mgr, stor, _ := newTestManager()
	loader := NewStaticLoader(map[string]Module{"mysql": testModule("mysql")})
	if _, errs := mgr.DiscoverAndLoad(context.Background(), loader, []string{"linux"}); len(errs) != 0 {
		t.Fatalf("load errs: %v", errs)
	}
	if err := mgr.Register("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Enable("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}

	// Loader whose Load always fails (MUST-3).
	failLoader := errLoader{Loader: NewStaticLoader(map[string]Module{"mysql": testModuleOps("mysql", []manifest.OperationDecl{
		{Name: "plugin.mysql.backup.execute", Resource: "backup", Action: "execute", Risk: "high"},
		{Name: "plugin.mysql.backup.restore", Resource: "backup", Action: "restore", Risk: "high"},
	})})}

	err := mgr.Reload(context.Background(), "mysql@1.0.0", failLoader, []string{"linux"})
	if err == nil {
		t.Fatal("expected reload to fail")
	}
	// Old plugin still Enabled + 1 op + still in core (storage has 1 op).
	d, ok := mgr.Get("mysql@1.0.0")
	if !ok || d.State != StateEnabled || len(d.Manifest.Operations) != 1 {
		t.Fatalf("old plugin broken by failed reload: %+v", d)
	}
	ops, _ := stor.Operations().List()
	if len(ops) != 1 {
		t.Fatalf("old op should remain in core, got %d", len(ops))
	}
	ev := findAudit(t, stor, "reload", "failure")
	if !strings.Contains(ev.Detail, "load-error") {
		t.Fatalf("want load-error audit, got %q", ev.Detail)
	}
}

// TestManager_Reload_RemovedOp proves a definition refresh that DROPS an
// operation tears down the removed capability (unregistered from core +
// disabled in Storage) while the retained op stays enabled.
func TestManager_Reload_RemovedOp(t *testing.T) {
	mgr, stor, _ := newTestManager()
	orig := testModuleOps("mysql", []manifest.OperationDecl{
		{Name: "plugin.mysql.backup.execute", Resource: "backup", Action: "execute", Risk: "high"},
		{Name: "plugin.mysql.backup.restore", Resource: "backup", Action: "restore", Risk: "high"},
	})
	loader := NewStaticLoader(map[string]Module{"mysql": orig})
	if _, errs := mgr.DiscoverAndLoad(context.Background(), loader, []string{"linux"}); len(errs) != 0 {
		t.Fatalf("load errs: %v", errs)
	}
	if err := mgr.Register("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Enable("mysql@1.0.0"); err != nil {
		t.Fatal(err)
	}

	// Reload dropping the "restore" op.
	reloadMod := testModuleOps("mysql", []manifest.OperationDecl{
		{Name: "plugin.mysql.backup.execute", Resource: "backup", Action: "execute", Risk: "high"},
	})
	if err := mgr.Reload(context.Background(), "mysql@1.0.0",
		NewStaticLoader(map[string]Module{"mysql": reloadMod}), []string{"linux"}); err != nil {
		t.Fatalf("reload: %v", err)
	}

	ops, _ := stor.Operations().List()
	if len(ops) != 2 {
		t.Fatalf("want 2 op rows (1 retained enabled, 1 removed disabled), got %d", len(ops))
	}
	for _, o := range ops {
		if o.Name == "plugin.mysql.backup.execute" && !o.Enabled {
			t.Fatalf("retained op should stay enabled")
		}
		if o.Name == "plugin.mysql.backup.restore" && o.Enabled {
			t.Fatalf("removed op should be disabled")
		}
	}
	if g := mgr.ReloadCount("mysql@1.0.0"); g != 1 {
		t.Fatalf("want generation 1, got %d", g)
	}
}

// errLoader wraps a Loader whose Load always fails (used for MUST-3).
type errLoader struct {
	Loader
}

func (e errLoader) Load(d Descriptor) (Module, error) {
	return nil, fmt.Errorf("simulated load failure")
}
