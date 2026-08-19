package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

// Watcher observes a plugin source for definition changes and triggers
// Manager.Reload on edge changes (Phase 4.3 / GPT Round 20). It is a THIN
// layer that implements the freeze boundary agreed with the contract reviewer:
//
//   - MUST-1: it NEVER mutates Runtime state. It is read-only on the Manager;
//     the only write path it touches is Manager.Reload, which is itself a
//     frozen lifecycle operation (Phase 3.6).
//   - MUST-2: it NEVER bypasses Reload. It does not Load/Register/Enable/
//     Unload directly; every change flows through the existing two-phase
//     Reload. A change at the source -> enqueue reload(id) -> Reload(id).
//   - MUST-3: it is an EDGE TRIGGER, not a state manager. It tracks only a
//     content hash per plugin id to detect changes; it maintains no desired/
//     observed state, no reconciliation loop, no Kubernetes-controller logic.
//
// SHOULD notes from the review:
//   - SHOULD-1: debounce collapses rapid successive changes (save+rename+chmod)
//     into a single Reload.
//   - SHOULD-2: same-plugin reloads are serialized (singleflight) so a slow
//     Reload cannot overlap with the next trigger.
//   - SHOULD-3: the Watcher does not reason about Compatibility / Capability /
//     Bootstrap / Generation. It only re-Discovers via the injected Loader and
//     forwards changed ids to Reload; the lifecycle contract is enforced by
//     Reload itself.
//
// Scope: the Watcher hot-reloads DEFINITIONS of plugins that are ALREADY
// loaded. A brand-new plugin (not yet in the runtime) is intentionally NOT
// auto-onboarded — onboarding is the Bootstrap path (with the explicit
// AutoEnableNewPlugin policy), so dropping a file never silently grants
// execution (consistent with the Phase 3.4 security model).
//
// Detection is polling-based (no external fsnotify dependency): each interval
// the Watcher re-Discovers via the injected Loader (which already reads the
// filesystem) and compares each loaded plugin's manifest content hash. This is
// robust to "what changed" semantics and fully decouples the Watcher from the
// manifest file layout.
type Watcher struct {
	mgr      *Manager
	loader   Loader
	hostCaps []string

	interval time.Duration
	debounce time.Duration
	log      *slog.Logger

	mu     sync.Mutex
	seen   map[string]string // id -> last manifest content hash
	timers map[string]*time.Timer

	inflight sync.Map // id -> struct{} (presence = a Reload is running)

	ctx   context.Context
	once  sync.Once
	cancel context.CancelFunc
	done   chan struct{}
}

// WatcherOptions configures a Watcher. Zero values fall back to sensible
// defaults (Interval 2s, Debounce 300ms).
type WatcherOptions struct {
	// Interval is the poll period for change detection.
	Interval time.Duration
	// Debounce collapses changes within a window into one Reload.
	Debounce time.Duration
	// Log receives reload lifecycle events. Defaults to slog.Default().
	Log *slog.Logger
}

// NewWatcher builds a plugin-source Watcher. The loader and hostCaps are the
// SAME ones used by DiscoverAndLoad/Bootstrap — the Watcher forwards them to
// Reload unchanged; it does not interpret them.
func NewWatcher(mgr *Manager, loader Loader, hostCaps []string, opts WatcherOptions) *Watcher {
	interval := opts.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	debounce := opts.Debounce
	if debounce <= 0 {
		debounce = 300 * time.Millisecond
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{
		mgr:      mgr,
		loader:   loader,
		hostCaps: hostCaps,
		interval: interval,
		debounce: debounce,
		log:      log,
		seen:     make(map[string]string),
		timers:   make(map[string]*time.Timer),
	}
}

// Start begins watching. It returns immediately; detection runs in a
// background goroutine until ctx is cancelled or Stop is called. The first
// poll seeds the change baseline WITHOUT triggering reloads.
func (w *Watcher) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	w.ctx = ctx
	w.cancel = cancel
	w.done = make(chan struct{})
	w.poll(ctx) // seed baseline (records current content hashes)
	ticker := time.NewTicker(w.interval)
	go func() {
		defer ticker.Stop()
		defer close(w.done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.poll(ctx)
			}
		}
	}()
}

// Stop cancels the watch and drains pending debounce timers.
func (w *Watcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	if w.done != nil {
		<-w.done
	}
	w.mu.Lock()
	for _, t := range w.timers {
		t.Stop()
	}
	w.timers = make(map[string]*time.Timer)
	w.mu.Unlock()
}

// poll is one change-detection pass. It re-Discovers via the injected Loader
// and, for each currently-loaded plugin whose manifest content changed since
// the last observation, schedules a debounced Reload.
func (w *Watcher) poll(ctx context.Context) {
	descs := w.loader.Discover(ctx)
	for _, d := range descs {
		if d.Manifest == nil {
			continue
		}
		id := d.ID
		hash := hashManifest(d.Manifest)

		_, loaded := w.mgr.Get(id)
		w.mu.Lock()
		prev, ok := w.seen[id]
		changed := false
		if loaded {
			if !ok {
				// First sight of an already-loaded plugin: record baseline, do
				// NOT reload (that would be an initial load, not a refresh).
				w.seen[id] = hash
			} else if prev != hash {
				w.seen[id] = hash
				changed = true
			}
		} else {
			// Not loaded (never onboarded, or unloaded): drop tracking so a
			// future load starts from a clean baseline. The Watcher never
			// auto-onboards new plugins.
			delete(w.seen, id)
		}
		w.mu.Unlock()

		if changed {
			w.enqueueReload(id)
		}
	}
}

// enqueueReload schedules a debounced Reload for id. Repeated calls within the
// debounce window collapse into a single Reload (SHOULD-1).
func (w *Watcher) enqueueReload(id string) {
	w.mu.Lock()
	if t, ok := w.timers[id]; ok {
		t.Stop() // collapse with the pending trigger
	}
	w.timers[id] = time.AfterFunc(w.debounce, func() {
		w.runReload(id)
	})
	w.mu.Unlock()
}

// runReload performs the actual Reload with per-id serialization (SHOULD-2).
// It is failure-isolated: a Reload error (e.g. an UPGRADE/identity change at
// the source, or a transient load failure) is logged, never propagated, and
// never aborts the watch.
func (w *Watcher) runReload(id string) {
	if w.ctx != nil && w.ctx.Err() != nil {
		return // watcher stopped while the timer was pending
	}
	if _, loaded := w.mgr.Get(id); !loaded {
		return // plugin vanished between detection and reload; nothing to do
	}
	if _, running := w.inflight.LoadOrStore(id, struct{}{}); running {
		return // a Reload for this id is already in flight
	}
	defer w.inflight.Delete(id)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.mgr.Reload(ctx, id, w.loader, w.hostCaps); err != nil {
		w.log.Warn("watcher: reload skipped", "id", id, "err", err)
		return
	}
	w.log.Info("watcher: reloaded plugin definition",
		"id", id, "generation", w.mgr.ReloadCount(id))
}

// hashManifest returns a stable content hash of a Manifest for change
// detection. It marshals the struct (field order is deterministic) and
// SHA-256s the bytes. A manifest is treated as unchanged iff its hash is
// unchanged.
func hashManifest(m *manifest.Manifest) string {
	b, err := json.Marshal(m)
	if err != nil {
		// Fallback: identity-by-pointer (extremely unlikely; a valid Manifest
		// always marshals). Keeps the Watcher from crashing on a bad value.
		return fmt.Sprintf("ptr:%p", m)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
