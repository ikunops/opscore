package runtime

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

// PluginError isolates a single plugin's load failure so one bad plugin never
// aborts Bootstrap (GPT Round 10 MUST-1: error isolation). The Loader records
// these and the Manager folds them into Bootstrap's errs; redis keeps loading
// while mysql is broken.
type PluginError struct {
	Key string // the provider key (subdirectory slug)
	ID  string // the resolved plugin ID (name@version), if known
	Err error
}

func (e PluginError) Error() string {
	if e.ID != "" {
		return fmt.Sprintf("plugin %q (%s): %v", e.Key, e.ID, e.Err)
	}
	return fmt.Sprintf("plugin %q: %v", e.Key, e.Err)
}

// ErrorReporter is an OPTIONAL capability a Loader may implement to expose
// per-plugin load errors that occurred during Discover — where the Loader
// interface can only return descriptors. The Manager type-asserts to this
// after Discover; if present, those errors are folded into Bootstrap's errs.
// This keeps the Loader interface stable (GPT Round 10 MUST-1: "keep the
// existing interface, capture at the Manager layer").
type ErrorReporter interface {
	LoadErrors() []PluginError
}

// FileLoader implements Loader by sourcing manifests from a manifest.Provider.
// Per the frozen seam (GPT Round 9/10): the Loader decides HOW to load, the
// Provider decides WHERE from. FileLoader holds exactly one Provider; a future
// OCI/Git provider swaps in with the Loader unchanged. It MUST NOT grow a
// Load(path) method — that would re-blur the Provider/Loader boundary
// (anti-.so slide, Round 6/7).
type FileLoader struct {
	provider manifest.Provider

	// mu guards lastErrors. Once a Watcher is active, Discover runs on TWO
	// concurrent paths — the poller goroutine (Watcher.Start → poll) and the
	// debounced reload (enqueueReload → runReload → Manager.Reload) — and both
	// would otherwise write lastErrors unsynchronized (caught by the race detector
	// on 2026-10-10 via TestWatcher_SurvivesReloadError; ADR-079). The mutex is a
	// LEAF: nothing else is locked while it is held, so it cannot participate in a
	// lock-order cycle.
	mu         sync.Mutex
	lastErrors []PluginError
}

// NewFileLoader builds a Loader over a Provider.
func NewFileLoader(provider manifest.Provider) *FileLoader {
	return &FileLoader{provider: provider}
}

// LoadErrors returns plugin failures captured during the most recent Discover.
// Implements ErrorReporter. The slice is COPIED so a caller holding it cannot
// race with the next Discover's store.
func (l *FileLoader) LoadErrors() []PluginError {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]PluginError, len(l.lastErrors))
	copy(out, l.lastErrors)
	return out
}

// storeLastErrors publishes one Discover pass's errors under the mutex.
func (l *FileLoader) storeLastErrors(errs []PluginError) {
	l.mu.Lock()
	l.lastErrors = errs
	l.mu.Unlock()
}

// Discover reads every plugin the provider exposes. It:
//   - parses+validates each (the Provider already validated at the boundary),
//   - isolates per-plugin read failures (MUST-1): a bad manifest is recorded
//     and discovery continues, it does NOT abort the whole pass,
//   - detects duplicate plugin IDs (MUST-2): an ID (name@version) claimed by
//     more than one key is a conflict and ALL claimants are rejected.
//
// Only successfully discovered descriptors are returned.
func (l *FileLoader) Discover(ctx context.Context) []Descriptor {
	// Errors accumulate LOCALLY and are published once, at the end — see mu on
	// FileLoader (two concurrent Discover paths share this loader, ADR-079).
	errs := []PluginError(nil)
	keys, err := l.provider.List()
	if err != nil {
		// Provider-level failure: nothing to discover. The Manager treats an
		// empty Discover result as "nothing to load" and continues.
		errs = append(errs, PluginError{Key: "<provider>", Err: err})
		l.storeLastErrors(errs)
		return nil
	}
	sort.Strings(keys) // Round 10 SHOULD: deterministic order

	type entry struct {
		key string
		m   *manifest.Manifest
	}
	byID := map[string][]entry{} // id -> entries claiming it
	for _, key := range keys {
		m, err := l.provider.Read(key)
		if err != nil {
			errs = append(errs, PluginError{Key: key, Err: err})
			continue
		}
		d := NewDescriptor(m)
		byID[d.ID] = append(byID[d.ID], entry{key: key, m: m})
	}

	// MUST-2: an ID claimed by more than one key is a conflict — reject ALL
	// claimants (never load a duplicate plugin).
	var descs []Descriptor
	for id, es := range byID {
		if len(es) > 1 {
			keys := make([]string, len(es))
			for i, e := range es {
				keys[i] = e.key
			}
			errs = append(errs, PluginError{
				ID:  id,
				Err: fmt.Errorf("duplicate plugin ID %q across keys %v", id, keys),
			})
			continue
		}
		descs = append(descs, NewDescriptor(es[0].m))
	}
	// Deterministic output order (by ID) for stable logs/audit.
	sort.Slice(descs, func(i, j int) bool { return descs[i].ID < descs[j].ID })
	l.storeLastErrors(errs)
	return descs
}

// Load resolves a Discovered descriptor into a running Module (state Loaded).
// The skeleton's manifests are METADATA ONLY (Operation-as-Code is supplied
// out-of-band in a real loader); here we close the loop with no-op handlers
// so Discover -> Load -> Register works end-to-end without .so. A real loader
// would bind the plugin's Go handlers here.
func (l *FileLoader) Load(desc Descriptor) (Module, error) {
	if desc.Manifest == nil {
		return nil, fmt.Errorf("file loader: descriptor %q has nil manifest", desc.ID)
	}
	return NewStaticModule(desc.Manifest, nil), nil
}

// Unload is a no-op for an external-provider source: lifecycle is owned by the
// manifest directory (remove the dir to unload). The Manager performs its own
// internal teardown; the provider source has nothing process-local to release.
func (l *FileLoader) Unload(name string) error {
	return nil
}
