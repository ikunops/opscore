package runtime

import (
	"context"
	"log/slog"
)

// ReloadInfo is the read-only payload passed to Hot-Reload Hooks. It deliberately
// exposes NO Manager/Registry write surface: a Hook can only OBSERVE a reload,
// never mutate Runtime State (Phase 4.1 MUST-1, GPT Round 19).
type ReloadInfo struct {
	// PluginID is the stable identity (name@version) of the reloaded plugin.
	PluginID string
	// Name is the plugin's display name at reload time.
	Name string
	// Version is the (unchanged) plugin version.
	Version string
	// Err is non-nil on AfterRollback, carrying the reload failure; nil on
	// BeforeCommit and AfterCommit.
	Err error
}

// Hook is the observer/extension seam for the Phase 3.6 two-phase Reload
// (Phase 4.1, GPT Round 19). Hooks MUST NOT mutate Runtime State — they run
// INSIDE Reload's Commit/Rollback and are never a state-management entry point.
//
// All three methods are best-effort: a Hook panic, error, or timeout is recorded
// and NEVER aborts the Reload (MUST-2 isolation, MUST-3 timeout boundary).
type Hook interface {
	// BeforeCommit fires after Phase A validation succeeds and just before
	// Reload's external commit mutations (Storage/Registry projection).
	BeforeCommit(ctx context.Context, info ReloadInfo) error
	// AfterCommit fires after a successful commit (in-memory swap + persist).
	AfterCommit(ctx context.Context, info ReloadInfo) error
	// AfterRollback fires on any reload failure (validation or commit).
	AfterRollback(ctx context.Context, info ReloadInfo) error
}

// RegisterHook adds an observer Hook fired around two-phase Reload (Phase 4.1).
// Hooks are best-effort: a panic/error/timeout is recorded but never aborts the
// Reload. Safe to call concurrently with Reload.
func (m *Manager) RegisterHook(h Hook) {
	if h == nil {
		return
	}
	m.hookMu.Lock()
	m.hooks = append(m.hooks, h)
	m.hookMu.Unlock()
}

// fireHook runs a single Hook method under a timeout (MUST-3) and isolates any
// panic/error (MUST-2) so a misbehaving Hook can never affect the Reload.
func (m *Manager) fireHook(ctx context.Context, phase string, h Hook, info ReloadInfo) {
	hctx, cancel := context.WithTimeout(ctx, m.hookTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.logHook(phase, info.PluginID, "panic recovered", "recovered", r)
			}
		}()
		var err error
		switch phase {
		case "before":
			err = h.BeforeCommit(hctx, info)
		case "commit":
			err = h.AfterCommit(hctx, info)
		case "rollback":
			err = h.AfterRollback(hctx, info)
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			m.logHook(phase, info.PluginID, "hook returned error", "err", err)
		}
	case <-hctx.Done():
		m.logHook(phase, info.PluginID, "hook timed out", "timeout", m.hookTimeout.String())
	}
}

// fireHooks fans the given phase out to all registered Hooks, each isolated.
func (m *Manager) fireHooks(ctx context.Context, phase string, info ReloadInfo) {
	m.hookMu.RLock()
	hs := make([]Hook, len(m.hooks))
	copy(hs, m.hooks)
	m.hookMu.RUnlock()
	for _, h := range hs {
		m.fireHook(ctx, phase, h, info)
	}
}

func (m *Manager) logHook(phase, id, msg string, args ...any) {
	kv := append([]any{"phase", phase, "plugin", id}, args...)
	slog.Warn("plugin reload hook: "+msg, kv...)
}
