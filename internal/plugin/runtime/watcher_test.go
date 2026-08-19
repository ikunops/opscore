package runtime

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

// nOpManifest builds a plugin manifest with n distinct operations so each
// content count produces a different hash (used to prove reloads picked up the
// LATEST edit, not a stale one). The op names carry the required
// "plugin.<name>." namespace prefix so manifest.Validate passes.
func nOpManifest(name string, n int) manifest.Manifest {
	actions := []string{"execute", "restore", "flush", "verify", "rotate", "export"}
	ops := make([]manifest.OperationDecl, 0, n)
	for i := 0; i < n; i++ {
		a := actions[i%len(actions)]
		ops = append(ops, manifest.OperationDecl{
			Name:     fmt.Sprintf("plugin.%s.backup.%s", name, a),
			Resource: "backup",
			Action:   a,
			Risk:     "high",
		})
	}
	return manifest.Manifest{Name: name, Version: "1.0.0", Operations: ops}
}

// loadEnable discovers, registers and enables a plugin via the real FileLoader.
func loadEnable(t *testing.T, mgr *Manager, loader Loader, id string) {
	t.Helper()
	if _, errs := mgr.DiscoverAndLoad(context.Background(), loader, []string{"linux"}); len(errs) != 0 {
		t.Fatalf("load: %v", errs)
	}
	if err := mgr.Register(id); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Enable(id); err != nil {
		t.Fatal(err)
	}
}

// flakyLoader wraps a Loader whose Load fails for selected plugin ids. It lets
// a test force a Reload error while keeping the SAME id loaded, to prove the
// Watcher survives a failed reload (MUST-2 failure isolation at the watch
// layer).
type flakyLoader struct {
	Loader
	mu      sync.Mutex
	failFor map[string]bool
}

func (f *flakyLoader) Load(d Descriptor) (Module, error) {
	f.mu.Lock()
	fail := f.failFor[d.ID]
	f.mu.Unlock()
	if fail {
		return nil, fmt.Errorf("simulated load failure for %s", d.ID)
	}
	return f.Loader.Load(d)
}

// TestWatcher_ReloadsOnManifestChange proves the core contract: a content
// change to a loaded plugin's manifest triggers exactly one Reload that
// refreshes the in-memory definition.
func TestWatcher_ReloadsOnManifestChange(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "mysql", nOpManifest("mysql", 1))

	mgr, _, _ := newTestManager()
	loader := NewFileLoader(manifest.NewFileProvider(dir))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	loadEnable(t, mgr, loader, "mysql@1.0.0")

	w := NewWatcher(mgr, loader, []string{"linux"}, WatcherOptions{
		Interval: 50 * time.Millisecond,
		Debounce: 100 * time.Millisecond,
	})
	w.Start(ctx)
	defer w.Stop()

	time.Sleep(150 * time.Millisecond) // let the baseline seed

	// Edit the manifest on disk: add a second operation.
	writeManifest(t, dir, "mysql", nOpManifest("mysql", 2))

	deadline := time.Now().Add(3 * time.Second)
	for {
		if mgr.ReloadCount("mysql@1.0.0") >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher did not reload after manifest change")
		}
		time.Sleep(20 * time.Millisecond)
	}

	d, ok := mgr.Get("mysql@1.0.0")
	if !ok {
		t.Fatal("mysql disappeared")
	}
	if len(d.Manifest.Operations) != 2 {
		t.Fatalf("want 2 ops after reload, got %d", len(d.Manifest.Operations))
	}
	if d.State != StateEnabled {
		t.Fatalf("reload must preserve State (Enabled), got %s", d.State)
	}
}

// TestWatcher_NoReloadForUnloadedPlugin proves the Watcher never auto-onboards
// a plugin that exists on disk but was never loaded into the runtime, and
// never calls Reload for an unknown id (MUST-2: no bypass; no silent exec).
func TestWatcher_NoReloadForUnloadedPlugin(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "redis", nOpManifest("redis", 1))

	mgr, _, _ := newTestManager()
	loader := NewFileLoader(manifest.NewFileProvider(dir))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Deliberately NOT calling DiscoverAndLoad — redis is on disk but never
	// onboarded into the runtime.

	w := NewWatcher(mgr, loader, []string{"linux"}, WatcherOptions{
		Interval: 50 * time.Millisecond,
		Debounce: 50 * time.Millisecond,
	})
	w.Start(ctx)
	defer w.Stop()

	time.Sleep(300 * time.Millisecond) // several poll cycles

	if _, ok := mgr.Get("redis@1.0.0"); ok {
		t.Fatal("redis must not be auto-onboarded by the Watcher")
	}
	if c := mgr.ReloadCount("redis@1.0.0"); c != 0 {
		t.Fatalf("watcher must not reload an unloaded plugin, got generation=%d", c)
	}
}

// TestWatcher_DebounceCollapsesRapidChanges proves SHOULD-1: two distinct
// content changes within the debounce window collapse into a SINGLE Reload
// that reads the LATEST content (not a storm of reloads).
func TestWatcher_DebounceCollapsesRapidChanges(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "mysql", nOpManifest("mysql", 1))

	mgr, _, _ := newTestManager()
	loader := NewFileLoader(manifest.NewFileProvider(dir))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	loadEnable(t, mgr, loader, "mysql@1.0.0")

	w := NewWatcher(mgr, loader, []string{"linux"}, WatcherOptions{
		Interval: 20 * time.Millisecond,
		Debounce: 300 * time.Millisecond,
	})
	w.Start(ctx)
	defer w.Stop()

	time.Sleep(80 * time.Millisecond) // seed baseline

	// Two rapid distinct edits within the 300ms debounce window.
	writeManifest(t, dir, "mysql", nOpManifest("mysql", 2))
	writeManifest(t, dir, "mysql", nOpManifest("mysql", 3))

	deadline := time.Now().Add(3 * time.Second)
	for {
		if mgr.ReloadCount("mysql@1.0.0") >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher did not reload after rapid changes")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Ensure the collapse held: exactly one reload, and it picked up the
	// latest content (3 ops), not an earlier one.
	time.Sleep(400 * time.Millisecond)
	if c := mgr.ReloadCount("mysql@1.0.0"); c != 1 {
		t.Fatalf("debounce should collapse to 1 reload, got %d", c)
	}
	d, _ := mgr.Get("mysql@1.0.0")
	if len(d.Manifest.Operations) != 3 {
		t.Fatalf("reload should reflect latest edit (3 ops), got %d", len(d.Manifest.Operations))
	}
}

// TestWatcher_NoReloadWhenContentUnchanged proves the edge trigger only fires
// on a real content delta: rewriting identical bytes (e.g. a chmod/touch with
// no content change) does NOT reload.
func TestWatcher_NoReloadWhenContentUnchanged(t *testing.T) {
	dir := t.TempDir()
	m0 := nOpManifest("mysql", 1)
	writeManifest(t, dir, "mysql", m0)

	mgr, _, _ := newTestManager()
	loader := NewFileLoader(manifest.NewFileProvider(dir))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	loadEnable(t, mgr, loader, "mysql@1.0.0")

	w := NewWatcher(mgr, loader, []string{"linux"}, WatcherOptions{
		Interval: 30 * time.Millisecond,
		Debounce: 50 * time.Millisecond,
	})
	w.Start(ctx)
	defer w.Stop()

	time.Sleep(80 * time.Millisecond)

	// Rewrite IDENTICAL content (no semantic change).
	writeManifest(t, dir, "mysql", m0)

	time.Sleep(400 * time.Millisecond)
	if c := mgr.ReloadCount("mysql@1.0.0"); c != 0 {
		t.Fatalf("unchanged content must not trigger reload, got generation=%d", c)
	}
}

// TestWatcher_SurvivesReloadError proves failure isolation (MUST-2 at the watch
// layer): when a Reload fails (simulated load error), the Watcher logs it, does
// NOT bump the generation, keeps the original plugin intact, and STAYS ALIVE
// so a subsequent valid change still reloads.
func TestWatcher_SurvivesReloadError(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "mysql", nOpManifest("mysql", 1))

	mgr, _, _ := newTestManager()
	base := NewFileLoader(manifest.NewFileProvider(dir))
	flaky := &flakyLoader{Loader: base, failFor: map[string]bool{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	loadEnable(t, mgr, base, "mysql@1.0.0")

	w := NewWatcher(mgr, flaky, []string{"linux"}, WatcherOptions{
		Interval: 30 * time.Millisecond,
		Debounce: 60 * time.Millisecond,
	})
	w.Start(ctx)
	defer w.Stop()

	time.Sleep(80 * time.Millisecond)

	// Force the next reload to fail at Load.
	flaky.mu.Lock()
	flaky.failFor["mysql@1.0.0"] = true
	flaky.mu.Unlock()
	writeManifest(t, dir, "mysql", nOpManifest("mysql", 2))

	time.Sleep(400 * time.Millisecond) // reload attempt fails, logged, no crash
	if _, ok := mgr.Get("mysql@1.0.0"); !ok {
		t.Fatal("original plugin must survive a failed reload")
	}
	if c := mgr.ReloadCount("mysql@1.0.0"); c != 0 {
		t.Fatalf("failed reload must not bump generation, got %d", c)
	}

	// Watcher still alive: fix the loader, a valid change reloads.
	flaky.mu.Lock()
	flaky.failFor["mysql@1.0.0"] = false
	flaky.mu.Unlock()
	writeManifest(t, dir, "mysql", nOpManifest("mysql", 3))

	deadline := time.Now().Add(3 * time.Second)
	for {
		if mgr.ReloadCount("mysql@1.0.0") >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher died after a reload error")
		}
		time.Sleep(20 * time.Millisecond)
	}
	d, _ := mgr.Get("mysql@1.0.0")
	if len(d.Manifest.Operations) != 3 {
		t.Fatalf("valid change after error should reload (3 ops), got %d", len(d.Manifest.Operations))
	}
}
