package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/plugin/capability"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/plugin"
	"github.com/YuDong999/opscore/internal/plugin/compat"
	"github.com/YuDong999/opscore/internal/plugin/manifest"
	"github.com/YuDong999/opscore/internal/storage"
)

// SyncFunc projects a plugin's operations into the durable Storage (the
// Permission Sync: Manifest -> OperationMetadata -> RoleOperation). It is
// injected rather than imported to keep this package free of the control-plane
// sync package (no import cycle). Typically bound to
// controlplane/sync.MetadataSynchronizer.SyncPlugin.
type SyncFunc func(pluginName string, ops []core.Operation) error

// Manager owns the plugin lifecycle and its persistence (plugin_registry table,
// Phase 3.0 / MUST-3). It registers plugin operations into the core Registry
// (so the Dispatcher can plan them) and delegates the Storage projection to the
// injected SyncFunc. It enforces the frozen lifecycle state machine.
//
// It holds the ROOT storage.Storage (not just PluginStore) because Enable/
// Disable/Unload must also flip the enabled flag on the plugin's Operation
// rows (the Permission Sync projection), reachable via Storage.Operations().
type Manager struct {
	reg      *core.Registry
	store    storage.Storage
	syncFunc SyncFunc

	// gate is the Compatibility Gate (Phase 3.5). It defaults to
	// compat.DefaultGate and is decoupled from the Loader interface.
	gate compat.Gate
	// kernel is the running kernel's self-description, injected so the Gate
	// can accept/reject plugins against the ACTUAL kernel (not a hardcoded
	// value). Empty until SetKernel is called.
	kernel compat.KernelInfo

	mu      sync.Mutex
	descs   map[string]Descriptor
	modules map[string]Module

	// gen counts successful Reloads per plugin id (Phase 3.6 SHOULD,
	// runtime-only tracing signal; NOT part of the frozen Contract).
	genMu sync.Mutex
	gen   map[string]int

	// hooks are observer extensions fired around Reload (Phase 4.1). Guarded by
	// hookMu; RegisterHook is safe to call concurrently with Reload.
	hooks []Hook
	hookMu sync.RWMutex
	// hookTimeout bounds each Hook invocation (MUST-3, GPT Round 19). Defaults
	// to 5s; callers (e.g. tests) may lower it.
	hookTimeout time.Duration
}

// NewManager builds a plugin Manager.
func NewManager(reg *core.Registry, store storage.Storage, syncFunc SyncFunc) *Manager {
	return &Manager{
		reg:      reg,
		store:    store,
		syncFunc: syncFunc,
		gate:     compat.DefaultGate{},
		descs:    map[string]Descriptor{},
		modules:  map[string]Module{},
		gen:      map[string]int{},
		hooks:    []Hook{},
		hookTimeout: 5 * time.Second,
	}
}

// SetGate overrides the Compatibility Gate. Defaults to compat.DefaultGate.
// Inject a custom compat.Gate in tests or for a stricter (deny-by-default)
// production policy.
func (m *Manager) SetGate(g compat.Gate) { m.gate = g }

// SetKernel sets the running kernel's compatibility info. The Compatibility
// Gate uses it to accept/reject plugins (Phase 3.5). Without it (empty
// version) any plugin declaring a MinKernel is rejected as "unknown kernel".
func (m *Manager) SetKernel(k compat.KernelInfo) { m.kernel = k }

// DiscoverAndLoad runs Discover on the loader, persists each descriptor as
// Discovered, then Loads each into a Module (Loaded). Returns the loaded
// modules and a list of non-fatal errors (e.g. a plugin whose required
// Capability is missing on the host is SKIPPED, not loaded).
func (m *Manager) DiscoverAndLoad(ctx context.Context, loader Loader, hostCaps []string) ([]Module, []error) {
	var mods []Module
	var errs []error
	descs := loader.Discover(ctx)
	// Surface per-plugin load failures recorded by a Loader that implements
	// ErrorReporter (e.g. FileLoader, GPT Round 10 MUST-1: error isolation at
	// the Manager layer without changing the Loader interface).
	if rep, ok := loader.(ErrorReporter); ok {
		for _, pe := range rep.LoadErrors() {
			errs = append(errs, pe)
		}
	}
	for _, d := range descs {
		id := d.ID
		// Compatibility Gate (Phase 3.5): reject incompatible plugins BEFORE
		// Load/Register so a bad plugin never enters the runtime. Runs AFTER
		// the Provider's Validate (Provider.Read) and is decoupled from the
		// Loader interface (GPT Round 12: Gate is an injected dependency, not
		// hardcoded in FileLoader). Rejected plugins are recorded + audited.
		if m.gate != nil {
			res, gerr := m.gate.Check(d.Manifest, m.kernel)
			if gerr != nil || (res != nil && !res.Compatible) {
				reason := ""
				if res != nil {
					reason = res.Reason
				} else if gerr != nil {
					reason = gerr.Error()
				}
				errs = append(errs, fmt.Errorf("plugin %q: compatibility gate rejected: %s", id, reason))
				m.recordRejection(id, d.Manifest, reason)
				continue
			}
		}
		if missing := m.negotiateCapabilities(id, d.Manifest, hostCaps); len(missing) > 0 {
			errs = append(errs, fmt.Errorf("plugin %q requires capabilities %v not satisfied by host", id, missing))
			continue
		}
		mod, err := loader.Load(d)
		if err != nil {
			errs = append(errs, fmt.Errorf("plugin %q: %w", id, err))
			continue
		}
		// Store the MODULE's descriptor: it is the Loaded + Frozen copy
		// (MUST-B: definition immutable after Load; State is the only
		// mutable field and lives on the same struct by design).
		m.mu.Lock()
		m.descs[id] = mod.Descriptor()
		m.modules[id] = mod
		m.mu.Unlock()
		if err := m.store.Plugins().Upsert(storage.Plugin{
			ID: id, Name: d.Manifest.Name, Version: mod.Descriptor().Manifest.Version, Status: string(StateLoaded),
			Enabled: false, LoadedAt: time.Now(),
		}); err != nil {
			errs = append(errs, fmt.Errorf("plugin %q: persist: %w", id, err))
			continue
		}
		mods = append(mods, mod)
	}
	return mods, errs
}

// pluginState is a persisted-row snapshot used by Bootstrap to decide whether
// a re-discovered plugin should be re-enabled (Registry restores STATE, it is
// NOT a manifest store — ADR-010).
type pluginState struct {
	known   bool
	enabled bool
}

// BootstrapPolicy configures lifecycle behavior at Bootstrap time (GPT Round
// 10: Phase 3.4). It is passed to Bootstrap, NOT baked into the Loader,
// because it is a lifecycle/security policy, not a "where manifests come
// from" concern.
type BootstrapPolicy struct {
	// AutoEnableNewPlugin controls whether a brand-new plugin (no prior
	// registry row) is enabled automatically on first discovery.
	//   - false (PRODUCTION DEFAULT, safe): a new plugin is Registered but
	//     stays Disabled; an operator must explicitly Enable it. Prevents
	//     "drop a file -> gain execution" privilege escalation.
	//   - true (dev/test/demo): preserves the skeleton's first-boot loop
	//     where every discovered plugin is enabled so the flow closes.
	AutoEnableNewPlugin bool
}

// Bootstrap is the single plugin startup composition root, shared by CLI,
// serve, and tests (GPT Round 8: not "PluginManager.Start()" but a
// "Bootstrap"). It:
//
//  1. reads the PERSISTED enabled-state from plugin_registry BEFORE
//     DiscoverAndLoad (which upserts enabled=false),
//  2. DiscoverAndLoad: Discover -> Descriptor -> Load (definition enters
//     the runtime; nothing is ever read back FROM the registry as a def),
//  3. Register every loaded plugin (definition enters the core Registry, so
//     the Dispatcher can now Plan it),
//  4. Enable per the persisted state: a NEW plugin (no prior registry row)
//     defaults to enabled so the skeleton closes the loop on first boot; a
//     KNOWN plugin follows its stored enabled flag (so a disabled plugin
//     stays disabled across restarts).
//
// It returns the names successfully ENABLED and a list of non-fatal errors
// (e.g. a plugin whose required Capability is missing was SKIPPED earlier
// in DiscoverAndLoad).
func (m *Manager) Bootstrap(ctx context.Context, loader Loader, hostCaps []string, policy BootstrapPolicy) (enabled []string, errs []error) {
	// 1. Persisted state BEFORE DiscoverAndLoad overwrites enabled=false.
	prev := map[string]pluginState{}
	if rows, err := m.store.Plugins().List(); err == nil {
		for _, p := range rows {
			prev[p.ID] = pluginState{known: true, enabled: p.Enabled}
		}
	}

	// 2. Discover + Load.
	mods, loadErrs := m.DiscoverAndLoad(ctx, loader, hostCaps)
	errs = append(errs, loadErrs...)
	if len(mods) == 0 {
		return enabled, errs
	}

	// 3 + 4. Register, then Enable per persisted state.
	for _, mod := range mods {
		id := mod.Descriptor().ID
		if err := m.Register(id); err != nil {
			errs = append(errs, fmt.Errorf("plugin %q register: %w", id, err))
			continue
		}
		ps := prev[id]
		// Known + enabled -> restore enabled. Known + disabled -> stay
		// Registered/Disabled (RBAC denies). NEW plugin -> follow policy:
		// auto-enable only when BootstrapPolicy.AutoEnableNewPlugin is set
		// (production default false = safe; operator must explicitly enable).
		var shouldEnable bool
		if ps.known {
			shouldEnable = ps.enabled
		} else {
			shouldEnable = policy.AutoEnableNewPlugin
		}
		if shouldEnable {
			if err := m.Enable(id); err != nil {
				errs = append(errs, fmt.Errorf("plugin %q enable: %w", id, err))
				continue
			}
			enabled = append(enabled, id)
		}
	}
	return enabled, errs
}

// Register moves a Loaded plugin to Registered: it validates each operation
// against the plugin namespace contract (MUST-2), registers them into the
// core Registry (so the Dispatcher can plan them), and projects them into
// Storage via the injected SyncFunc (Permission Sync: admin can only GRANT,
// never add new operations beyond what the manifest declares).
func (m *Manager) Register(id string) error {
	m.mu.Lock()
	d, ok := m.descs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q: not loaded", id)
	}
	if err := Transition(d.State, StateRegistered); err != nil {
		m.mu.Unlock()
		return err
	}
	mod := m.modules[id]
	m.mu.Unlock()

	ops := mod.Operations()
	for i := range ops {
		if err := plugin.ValidateOperation(ops[i]); err != nil {
			return fmt.Errorf("plugin %q: %w", id, err)
		}
		// Audit Source carries the STABLE id (name@version), not the mutable
		// display Name (Phase 3.4.1 / GPT Round 11 MUST on PluginID).
		ops[i].Source = d.Source
	}
	// Register each operation into the core Registry so the Dispatcher can
	// plan/execute it. The Registry is the runtime source of truth for
	// capabilities; Storage is only its durable projection (via syncFunc).
	for i := range ops {
		m.reg.Register(ops[i])
	}
	if err := m.syncFunc(id, ops); err != nil {
		return fmt.Errorf("plugin %q: sync: %w", id, err)
	}
	m.setState(id, StateRegistered)
	return nil
}

// Enable moves Registered -> Enabled (or Disabled -> Enabled, re-enable).
// It flips the plugin's granted flag and the enabled flag on each of its
// Storage operations.
func (m *Manager) Enable(id string) error {
	m.mu.Lock()
	d, ok := m.descs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q: unknown", id)
	}
	if err := Transition(d.State, StateEnabled); err != nil {
		m.mu.Unlock()
		return err
	}
	names := operationNames(d.Manifest)
	m.mu.Unlock()

	for _, op := range names {
		if err := m.store.Operations().SetEnabled(op, true); err != nil {
			return fmt.Errorf("plugin %q: enable op %q: %w", id, op, err)
		}
	}
	if err := m.store.Plugins().SetEnabled(id, true); err != nil {
		return fmt.Errorf("plugin %q: enable: %w", id, err)
	}
	m.setState(id, StateEnabled)
	return nil
}

// Disable moves Enabled -> Disabled (revokes the grant).
func (m *Manager) Disable(id string) error {
	m.mu.Lock()
	d, ok := m.descs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q: unknown", id)
	}
	if err := Transition(d.State, StateDisabled); err != nil {
		m.mu.Unlock()
		return err
	}
	names := operationNames(d.Manifest)
	m.mu.Unlock()

	for _, op := range names {
		_ = m.store.Operations().SetEnabled(op, false)
	}
	_ = m.store.Plugins().SetEnabled(id, false)
	m.setState(id, StateDisabled)
	return nil
}

// Unload tears down a plugin (Registered or Disabled -> Unloaded). It is
// FORBIDDEN from Enabled: you must Disable first (otherwise a GRANTED
// capability would vanish mid-flight). It unregisters the plugin's operations
// from the core Registry and disables their Storage rows (history preserved).
func (m *Manager) Unload(id string) error {
	m.mu.Lock()
	d, ok := m.descs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q: unknown", id)
	}
	if err := Transition(d.State, StateUnloaded); err != nil {
		m.mu.Unlock()
		return err
	}
	names := operationNames(d.Manifest)
	delete(m.modules, id)
	delete(m.descs, id)
	m.mu.Unlock()

	for _, op := range names {
		m.reg.Unregister(op)
		_ = m.store.Operations().SetEnabled(op, false)
	}
	_ = m.store.Plugins().SetStatus(id, string(StateUnloaded))
	return nil
}

// Reload re-reads a plugin's definition from the loader and atomically
// replaces its in-memory Module with the re-loaded one (Phase 3.6 / GPT
// Round 15). It is STRICTLY same-ID only:
//
//   - MUST-1: the re-discovered descriptor's ID must equal the requested id.
//     A different ID means the manifest changed identity (e.g. a version
//     bump) which is an UPGRADE, not a Reload, and must go through the normal
//     Discover -> Compat -> Register -> Enable path.
//   - MUST-2: two-phase commit. The new Module is loaded, compatibility-gated,
//     and registered into the core Registry / Storage FIRST; only after every
//     external mutation succeeds is the in-memory Module swapped and the old
//     one unloaded.
//   - MUST-3: strong exception safety. Any failure during Phase A or B leaves
//     the previously-loaded Module fully working with its original lifecycle
//     State untouched (no Disabled/Unloaded half-state).
//   - The plugin's original lifecycle State (Enabled/Disabled/...) is
//     PRESERVED across a successful Reload — a definition refresh is not a
//     lifecycle change.
//   - SHOULD: a per-id generation counter is incremented on success (Runtime
//     tracing only) and the event is audited (ReloadSucceeded / ReloadFailed
//     with Plugin ID + Failure Code + Reason).
func (m *Manager) Reload(ctx context.Context, id string, loader Loader, hostCaps []string) (err error) {
	// --- snapshot old (no mutation yet) ---
	m.mu.Lock()
	oldDesc, okD := m.descs[id]
	oldMod, okM := m.modules[id]
	m.mu.Unlock()
	if !okD || !okM {
		return fmt.Errorf("plugin %q: not loaded, cannot reload", id)
	}
	oldState := oldDesc.State
	oldNames := operationNames(oldDesc.Manifest)
	oldOps := oldMod.Operations()

	// Phase 4.1: build the read-only Hook payload and install the
	// AfterCommit/AfterRollback observer. BeforeCommit fires just before the
	// Phase B external mutations. Hooks can only OBSERVE — no Runtime State
	// write surface is exposed (MUST-1, GPT Round 19).
	info := ReloadInfo{
		PluginID: id,
		Name:     oldDesc.Manifest.Name,
		Version:  oldDesc.Manifest.Version,
	}
	committed := false
	defer func() {
		if committed {
			m.fireHooks(ctx, "commit", info)
		} else {
			info.Err = err
			m.fireHooks(ctx, "rollback", info)
		}
	}()

	// --- Phase A: validate the new definition WITHOUT touching shared state ---
	descs := loader.Discover(ctx)
	var nd Descriptor
	found := false
	for _, d := range descs {
		if d.ID == id {
			nd = d
			found = true
			break
		}
	}
	if !found {
		// The identity at the source may have changed (version bump) — that is
		// an UPGRADE, not a Reload. Detect by name to give a precise error.
		wantName := pluginNameFromID(id)
		for _, d := range descs {
			if d.Manifest.Name == wantName {
				m.auditReload(id, d.Manifest.Name, "failure",
					fmt.Sprintf("code=id-mismatch reason=source now declares %q (use Upgrade, not Reload)", d.ID))
				return fmt.Errorf("plugin %q: source now declares %q — identity changed, this is an Upgrade not a Reload", id, d.ID)
			}
		}
		m.auditReload(id, oldDesc.Manifest.Name, "failure",
			fmt.Sprintf("code=descriptor-not-found reason=plugin %q not present in loader", id))
		return fmt.Errorf("plugin %q: not present in loader for reload", id)
	}
	// MUST-1: identity must be unchanged.
	if nd.ID != id {
		m.auditReload(id, nd.Manifest.Name, "failure",
			fmt.Sprintf("code=id-mismatch reason=reloaded descriptor id %q != %q (use Upgrade, not Reload)", nd.ID, id))
		return fmt.Errorf("plugin %q: reloaded descriptor id %q != requested %q (use Upgrade, not Reload)", nd.ID, id, id)
	}
	// Compatibility Gate (same contract as DiscoverAndLoad).
	if m.gate != nil {
		res, gerr := m.gate.Check(nd.Manifest, m.kernel)
		if gerr != nil || (res != nil && !res.Compatible) {
			reason := ""
			code := "incompatible"
			if res != nil {
				reason = res.Reason
				if res.Code != "" {
					code = res.Code
				}
			} else if gerr != nil {
				reason = gerr.Error()
			}
			m.auditReload(id, nd.Manifest.Name, "failure",
				fmt.Sprintf("code=%s reason=%s", code, reason))
			return fmt.Errorf("plugin %q: compatibility gate rejected: %s", id, reason)
		}
	}
	// Host capability gate (Phase 3.7 Capability Negotiation).
	if missing := m.negotiateCapabilities(id, nd.Manifest, hostCaps); len(missing) > 0 {
		m.auditReload(id, nd.Manifest.Name, "failure",
			fmt.Sprintf("code=missing-capability reason=requires %v", missing))
		return fmt.Errorf("plugin %q: requires capabilities %v not satisfied by host", id, missing)
	}
	// Load the new Module locally; the old one is still fully live.
	nmod, err := loader.Load(nd)
	if err != nil {
		m.auditReload(id, nd.Manifest.Name, "failure",
			fmt.Sprintf("code=load-error reason=%s", err.Error()))
		return fmt.Errorf("plugin %q: load: %w", id, err)
	}
	newOps := nmod.Operations()
	for i := range newOps {
		if verr := plugin.ValidateOperation(newOps[i]); verr != nil {
			m.auditReload(id, nd.Manifest.Name, "failure",
				fmt.Sprintf("code=invalid-operation reason=%s", verr.Error()))
			return fmt.Errorf("plugin %q: %w", id, verr)
		}
		newOps[i].Source = nd.Source
	}
	newNames := operationNames(nd.Manifest)

	// --- Phase B: commit (external mutations FIRST, swap LAST) ---
	// Phase 4.1 Hook: notify observers BEFORE the external commit mutations.
	m.fireHooks(ctx, "before", info)

	// Register the new ops into the core Registry + Storage projection. This
	// overwrites ops that share a name with the old definition. If it fails we
	// best-effort restore the old projection; the in-memory Module is NOT
	// swapped, so the old plugin keeps working (MUST-3).
	if err := m.syncFunc(id, newOps); err != nil {
		_ = m.syncFunc(id, oldOps) // best-effort restore
		m.auditReload(id, nd.Manifest.Name, "failure",
			fmt.Sprintf("code=sync-error reason=%s", err.Error()))
		return fmt.Errorf("plugin %q: sync: %w", id, err)
	}

	// All external mutations succeeded -> atomically swap the in-memory Module
	// and preserve the original lifecycle State.
	m.mu.Lock()
	nd2 := nmod.Descriptor()
	nd2.State = oldState
	m.descs[id] = nd2
	m.modules[id] = nmod
	m.mu.Unlock()

	// Persist the refreshed definition but keep the original status/enabled
	// (definition refresh != lifecycle change).
	if err := m.store.Plugins().Upsert(storage.Plugin{
		ID:       id,
		Name:     nd.Manifest.Name,
		Version:  nd.Manifest.Version,
		Status:   string(oldState),
		Enabled:  oldState == StateEnabled,
		LoadedAt: time.Now(),
	}); err != nil {
		// Rollback: restore the old in-memory Module + re-project old ops.
		m.mu.Lock()
		m.descs[id] = oldDesc
		m.modules[id] = oldMod
		m.mu.Unlock()
		_ = m.syncFunc(id, oldOps)
		m.auditReload(id, nd.Manifest.Name, "failure",
			fmt.Sprintf("code=persist-error reason=%s", err.Error()))
		return fmt.Errorf("plugin %q: persist: %w", id, err)
	}

	// Tear down operations that existed in the OLD definition but not the NEW
	// one (removed capabilities). Shared/added names were already handled by
	// the syncFunc projection above.
	for _, name := range diffNames(oldNames, newNames) {
		m.reg.Unregister(name)
		_ = m.store.Operations().SetEnabled(name, false)
	}

	// SHOULD: bump generation (runtime-only tracing).
	m.genMu.Lock()
	m.gen[id]++
	gen := m.gen[id]
	m.genMu.Unlock()

	// Phase 4.1: the commit succeeded; mark committed so the deferred observer
	// fires AfterCommit (not AfterRollback).
	committed = true
	m.auditReload(id, nd.Manifest.Name, "success", fmt.Sprintf("generation=%d", gen))
	return nil
}

// ReloadCount returns how many successful Reloads a plugin id has had
// (Phase 3.6 SHOULD, runtime-only tracing signal).
func (m *Manager) ReloadCount(id string) int {
	m.genMu.Lock()
	defer m.genMu.Unlock()
	return m.gen[id]
}

// Get returns a plugin's current descriptor.
func (m *Manager) Get(id string) (Descriptor, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.descs[id]
	return d, ok
}

// List returns all currently-managed plugin descriptors.
func (m *Manager) List() []Descriptor {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Descriptor, 0, len(m.descs))
	for _, d := range m.descs {
		out = append(out, d)
	}
	return out
}

// --- helpers ---------------------------------------------------------------

func (m *Manager) setState(id string, s LifecycleState) {
	m.mu.Lock()
	if d, ok := m.descs[id]; ok {
		d.State = s
		m.descs[id] = d
	}
	m.mu.Unlock()
	_ = m.store.Plugins().SetStatus(id, string(s))
}

func operationNames(m *manifest.Manifest) []string {
	names := make([]string, 0, len(m.Operations))
	for _, op := range m.Operations {
		names = append(names, op.Name)
	}
	return names
}

// pluginManifestVersion returns the manifest's version, safely, for audit/
// storage of a plugin that never entered the runtime state machine.
func pluginManifestVersion(man *manifest.Manifest) string {
	if man == nil {
		return ""
	}
	return man.Version
}

// recordRejection persists a plugin the Compatibility Gate refused, so
// operators can SEE why it is absent (GPT Round 12 / Phase 3.5: "rejected
// plugin status" + "Audit PluginLoadRejected"). The plugin never enters the
// runtime lifecycle state machine — it is rejected before Load — so this is a
// storage-only row (status = storage.PluginRejected) plus an append-only
// audit event. Failures here are non-fatal: a rejection has already been
// reported via errs; we just best-effort record it.
func (m *Manager) recordRejection(id string, man *manifest.Manifest, reason string) {
	name := ""
	if man != nil {
		name = man.Name
	}
	_ = m.store.Plugins().Upsert(storage.Plugin{
		ID:       id,
		Name:     name,
		Version:  pluginManifestVersion(man),
		Status:   storage.PluginRejected,
		Enabled:  false,
		LoadedAt: time.Now(),
	})
	_, _ = m.store.Audit().Append(storage.AuditEvent{
		Timestamp: time.Now(),
		Actor:     "system",
		Operation: id,
		Action:    "load",
		Target:    name,
		Result:    "failure",
		Detail:    "compatibility gate rejected: " + reason,
	})
}

// negotiateCapabilities runs Phase 3.7 Capability Negotiation: the plugin's
// declared capability requirements (manifest.Capabilities, plain name tokens)
// are upgraded to capability.Capability descriptors (Required=true by default)
// and resolved against the host Provider. It returns the REQUIRED-but-missing
// capability names (empty == all granted). It also emits a Capability Audit
// event (SHOULD) so operators can see, per plugin, what was negotiated —
// Granted count, and any optional-missing / required-missing details.
//
// An empty manifest.Capabilities means "no requirement" -> always granted, no
// audit event (nothing to report).
func (m *Manager) negotiateCapabilities(id string, man *manifest.Manifest, hostCaps []string) []string {
	if len(man.Capabilities) == 0 {
		return nil
	}
	required := make([]capability.Capability, 0, len(man.Capabilities))
	for _, name := range man.Capabilities {
		if c, err := capability.Parse(name); err == nil {
			required = append(required, c)
		}
	}
	host := capability.NewHostProvider(hostCaps)
	res := capability.Negotiate(required, host)
	if res.AllGranted {
		detail := fmt.Sprintf("granted=%d", len(res.Outcomes))
		if len(res.OptionalMissing) > 0 {
			detail += " optional-missing=" + strings.Join(res.OptionalMissing, ",")
		}
		m.auditCapability(id, man.Name, "success", detail)
		return nil
	}
	m.auditCapability(id, man.Name, "failure",
		fmt.Sprintf("code=missing-capability missing=%s", strings.Join(res.Missing, ",")))
	return res.Missing
}

// auditCapability appends a Capability Negotiation audit event (Phase 3.7
// SHOULD). Detail carries the granted count (success) or the missing list
// (failure) so operators can trace per-plugin capability outcomes.
func (m *Manager) auditCapability(id, name, result, detail string) {
	_, _ = m.store.Audit().Append(storage.AuditEvent{
		Timestamp: time.Now(),
		Actor:     "system",
		Operation: id,
		Action:    "capability",
		Target:    name,
		Result:    result,
		Detail:    detail,
	})
}

// diffNames returns the names present in a but not in b (used by Reload to
// tear down operations removed by a definition refresh).
func diffNames(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, x := range b {
		set[x] = true
	}
	var out []string
	for _, x := range a {
		if !set[x] {
			out = append(out, x)
		}
	}
	return out
}

// pluginNameFromID strips the "@version" suffix from a stable plugin id
// (name@version), returning the display Name. Used by Reload to detect an
// identity change (version bump) at the source.
func pluginNameFromID(id string) string {
	if i := strings.LastIndex(id, "@"); i >= 0 {
		return id[:i]
	}
	return id
}

// auditReload appends a Reload lifecycle audit event (Phase 3.6 SHOULD).
// Detail carries the Failure Code + Reason on failure, or the generation on
// success, so operators can trace reload outcomes without re-parsing logs.
func (m *Manager) auditReload(id, name, result, detail string) {
	_, _ = m.store.Audit().Append(storage.AuditEvent{
		Timestamp: time.Now(),
		Actor:     "system",
		Operation: id,
		Action:    "reload",
		Target:    name,
		Result:    result,
		Detail:    detail,
	})
}
