package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/plugin/compat"
	"github.com/YuDong999/opscore/internal/plugin/manifest"
	"github.com/YuDong999/opscore/internal/storage"
)

// recHook is a test Hook that counts which lifecycle methods fired and can be
// instructed to fail / panic / sleep on a given phase.
type recHook struct {
	mu       sync.Mutex
	before   int
	commit   int
	rollback int

	failOn  string // "before" | "commit" | "rollback" -> return error
	panicOn string
	sleepOn string
	sleepFor time.Duration
}

func (h *recHook) BeforeCommit(ctx context.Context, _ ReloadInfo) error {
	h.mu.Lock(); h.before++; h.mu.Unlock()
	return h.act(ctx, "before")
}
func (h *recHook) AfterCommit(ctx context.Context, _ ReloadInfo) error {
	h.mu.Lock(); h.commit++; h.mu.Unlock()
	return h.act(ctx, "commit")
}
func (h *recHook) AfterRollback(ctx context.Context, _ ReloadInfo) error {
	h.mu.Lock(); h.rollback++; h.mu.Unlock()
	return h.act(ctx, "rollback")
}
func (h *recHook) act(ctx context.Context, phase string) (err error) {
	if h.panicOn == phase {
		panic("boom-" + phase)
	}
	if h.sleepOn == phase {
		select {
		case <-time.After(h.sleepFor):
		case <-ctx.Done():
		}
	}
	if h.failOn == phase {
		return errors.New("hook failed on " + phase)
	}
	return nil
}

// testLoader is a minimal Loader that always re-discovers one fixed descriptor.
type testLoader struct {
	desc Descriptor
	mod  Module
}

func (l *testLoader) Discover(ctx context.Context) []Descriptor { return []Descriptor{l.desc} }
func (l *testLoader) Load(d Descriptor) (Module, error)         { return l.mod, nil }
func (l *testLoader) Unload(name string) error                 { return nil }

// setupReload wires a Manager with one already-loaded ("demo@1.0.0") plugin so a
// Reload can run without going through Bootstrap. The old module and descriptor
// are injected directly (in-package test) to keep the harness minimal.
func setupReload(t *testing.T) (*Manager, Loader) {
	t.Helper()
	reg := core.NewRegistry()
	store := storage.NewMemoryStorage()
	m := NewManager(reg, store, func(string, []core.Operation) error { return nil })
	m.SetKernel(compat.KernelInfo{Version: "0.1.0", SupportedAPIs: []string{"opscore.plugin/v1"}})
	m.SetGate(compat.DefaultGate{})

	oldMan := &manifest.Manifest{
		SchemaVersion: 1, Name: "demo", Version: "1.0.0",
		PluginAPI: "opscore.plugin/v1", MinKernel: "0.1.0",
	}
	oldMod := NewStaticModule(oldMan, nil)
	oldDesc := NewDescriptor(oldMan)
	oldDesc.State = StateEnabled
	m.descs["demo@1.0.0"] = oldDesc
	m.modules["demo@1.0.0"] = oldMod

	newMan := &manifest.Manifest{
		SchemaVersion: 1, Name: "demo", Version: "1.0.0",
		PluginAPI: "opscore.plugin/v1", MinKernel: "0.1.0",
	}
	newMod := NewStaticModule(newMan, nil)
	newDesc := NewDescriptor(newMan)
	loader := &testLoader{desc: newDesc, mod: newMod}
	return m, loader
}

// TestReloadHooks_CommitFires verifies the happy path: BeforeCommit and
// AfterCommit fire once, AfterRollback does not.
func TestReloadHooks_CommitFires(t *testing.T) {
	m, loader := setupReload(t)
	h := &recHook{}
	m.RegisterHook(h)

	if err := m.Reload(context.Background(), "demo@1.0.0", loader, nil); err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.before != 1 || h.commit != 1 {
		t.Fatalf("expected before=1 commit=1, got before=%d commit=%d", h.before, h.commit)
	}
	if h.rollback != 0 {
		t.Fatalf("unexpected AfterRollback count=%d", h.rollback)
	}
}

// TestReloadHooks_ErrorIsolated verifies MUST-2: a Hook error (and a Hook panic)
// is recorded but NEVER aborts the Reload.
func TestReloadHooks_ErrorIsolated(t *testing.T) {
	// (a) Hook returns an error on AfterCommit.
	m, loader := setupReload(t)
	h := &recHook{failOn: "commit"}
	m.RegisterHook(h)
	if err := m.Reload(context.Background(), "demo@1.0.0", loader, nil); err != nil {
		t.Fatalf("reload must succeed despite hook error: %v", err)
	}
	h.mu.Lock(); defer h.mu.Unlock()
	if h.commit != 1 {
		t.Fatalf("AfterCommit should have fired once, got %d", h.commit)
	}

	// (b) Hook panics on AfterCommit.
	m2, loader2 := setupReload(t)
	h2 := &recHook{panicOn: "commit"}
	m2.RegisterHook(h2)
	if err := m2.Reload(context.Background(), "demo@1.0.0", loader2, nil); err != nil {
		t.Fatalf("reload must survive a hook panic: %v", err)
	}
}

// TestReloadHooks_RollbackFires verifies AfterRollback fires when Reload fails.
func TestReloadHooks_RollbackFires(t *testing.T) {
	m, _ := setupReload(t)
	// A loader that re-discovers a DIFFERENT id forces an identity-mismatch
	// failure (an Upgrade, not a Reload) — exercising a rollback path.
	bad := &manifest.Manifest{
		SchemaVersion: 1, Name: "demo", Version: "2.0.0",
		PluginAPI: "opscore.plugin/v1", MinKernel: "0.1.0",
	}
	loader := &testLoader{desc: NewDescriptor(bad), mod: NewStaticModule(bad, nil)}

	h := &recHook{}
	m.RegisterHook(h)
	if err := m.Reload(context.Background(), "demo@1.0.0", loader, nil); err == nil {
		t.Fatal("expected reload to fail on id mismatch")
	}
	h.mu.Lock(); defer h.mu.Unlock()
	if h.rollback != 1 {
		t.Fatalf("expected AfterRollback=1, got %d", h.rollback)
	}
	if h.commit != 0 {
		t.Fatalf("AfterCommit must not fire on rollback, got %d", h.commit)
	}
}

// TestReloadHooks_TimeoutBound verifies MUST-3: a slow Hook is cut off at the
// timeout boundary and the Reload still completes promptly.
func TestReloadHooks_TimeoutBound(t *testing.T) {
	m, loader := setupReload(t)
	m.hookTimeout = 50 * time.Millisecond // shorten so the test is fast
	h := &recHook{sleepOn: "commit", sleepFor: 2 * time.Second}
	m.RegisterHook(h)

	start := time.Now()
	if err := m.Reload(context.Background(), "demo@1.0.0", loader, nil); err != nil {
		t.Fatalf("reload must succeed despite a slow hook: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("reload blocked too long by a slow hook: %v", elapsed)
	}
	h.mu.Lock(); defer h.mu.Unlock()
	if h.commit != 1 {
		t.Fatalf("AfterCommit should have fired once, got %d", h.commit)
	}
}
