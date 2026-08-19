# ADR-010: Plugin Runtime Contract & Isolation Boundary

**Status**: Accepted — **Phase 3 Runtime Contract FROZEN (Round 17, 2026-07-26)**
**Date**: 2026-07-24

> **PHASE 3 RUNTIME CONTRACT — FROZEN.**
> As of Round 17, all sixteen contract surfaces are finalized and immutable:
> Manifest, Schema Version, Descriptor, Stable Identity, Loader, Provider,
> Bootstrap, Plugin Registry, Compatibility Gate, Reload, Capability
> Negotiation, Audit, Migration, CAS, Execution SSOT, and the Lifecycle
> State Machine. Future capabilities — Marketplace, OCI, Git, Signature,
> Sandbox, Watcher, Hot-Reload Hooks — MUST be built *on top of* these
> contracts, never by modifying them. (See §12 and the Freeze Notice below.)

## Context

Phase 3.0 prep (GPT Round 6 MUST set) established the three guards that keep
plugins from eroding the core RBAC / Audit / Execution boundaries:

- **MUST-1** `Operation.Source` — every capability knows its origin
  (`builtin` | `system` | `plugin:<name>`), so Audit never needs a Storage join.
- **MUST-2** Plugin Permission Namespace — a plugin op MUST be
  `plugin.<name>.<resource>.<action>`, may not claim a reserved system
  resource (`execution/system/host/...`), and may not use a reserved
  *prefix* as its name (`builtin./system./core./internal.`, Round 7 SHOULD).
- **MUST-3** `plugin_registry` persistence — plugin lifecycle state
  survives a restart.

With those in place, Round 6/7 froze the **Phase 3.1 Plugin Runtime
Contract**: the surfaces a plugin integration MUST satisfy — *without* any
`.so` loading, hot-reload, or real plugin discovery yet. The contract's
job is to make future plugin code impossible to use *incorrectly*, so the
core is never polluted by "convenient" shortcuts.

## Decision

Adopt the following Runtime Contract (implemented in `internal/plugin/runtime`,
`internal/plugin/manifest`). It is **contract, not implementation**.

### 1. Manifest / Descriptor separation (Round 6 MUST-be-clean)

- `manifest.Manifest` is the **external** declaration format (JSON: name,
  version, capabilities, permissions, operations). It lives in its own
  package and never reaches the runtime.
- `runtime.Descriptor` is the **runtime-internal** model: `Manifest` +
  `Source` + `State`. JSON is parsed at the boundary and never threaded
  through the runtime. Two facts, one source of truth.

### 2. Loader abstraction — NO file paths, NO `.so`

```go
type Loader interface {
    Discover(ctx) []Descriptor
    Load(desc Descriptor) (Module, error)
    Unload(name string) error
}
```

The input is an abstract plugin **source**, never a filesystem path. A future
`FileLoader` / `OCIRegistryLoader` / `GitLoader` all implement this
interface. There is deliberately **no `Load(path)` / `LoadFile()`** — that
signature is the slippery slope to a `.so` loader, which is explicitly
out of scope for Phase 3.1.

### 3. Lifecycle state machine (frozen)

```
Discovered -> Loaded -> Registered -> Enabled -> Disabled -> Unloaded
```

- `Unload` is legal only from `Registered` (never granted) or `Disabled`.
  It is **forbidden from `Enabled`** — you must `Disable` first. This
  prevents yanking a *granted* capability out from under a running
  Execution.
- Re-enable from `Disabled` is allowed. `Unloaded` is terminal.

### 4. Module / Handler contract — the Isolation Boundary

- A `Module` exposes `Descriptor()` + `Operations() []core.Operation`.
- Each operation's `Handler` is a plain `core.Handler`: `Plan(ctx, input)
  (*ExecutionPlan, error)`.
- **The Isolation Boundary is enforced by the type system**: `core.Context`
  (passed to every Handler) exposes **no** exec / SSH / shell primitive.
  A plugin Handler can only *plan* — it returns `ExecutionStep`s that the
  single Executor runs. There is no API for a plugin to shell out directly.
- Mandatory routing (never bypassed):

```
Plugin -> Handler.Plan -> Dispatcher -> Execution -> Executor
```

No plugin code may call OS/exec, SSH, or spawn a process outside this chain.

### 5. Permission Sync (admin grants, never adds)

On `Register`, the plugin's declared operations are projected into Storage
(`Manifest -> OperationMetadata -> RoleOperation`) by the injected
`SyncFunc` (bound to `controlplane/sync.MetadataSynchronizer.SyncPlugin`).
The admin may **grant** the plugin's operations to a role, but cannot
**author new operations** beyond what the manifest declares. The namespace
contract (MUST-2) is re-checked at load time so a malicious/buggy
plugin can never register a system-scoped permission.

### 6. Capability requirement (Round 7 SHOULD)

`Manifest.Capabilities []string` declares the plugin's **own runtime
requirements** (e.g. `linux`, `systemd`, `docker`). These are NOT the
Execution Capability snapshot (observed host facts); they are what the plugin
needs to *run*. The Loader refuses to `Load` a plugin whose required
capability the host lacks, before any work.

### 7. Plugin Identity — `Descriptor.ID` (Round 8 / MUST-A)

`Descriptor` carries a STABLE identity `ID = "<Name>@<Version>"`
(e.g. `mysql@1.4.2`), computed once at construction. `Manifest.Name`
is demoted to a **Display Name** that MAY later be renamed without
breaking Audit / Execution / PluginRegistry references. Every persisted
row (audit events, execution records, plugin_registry) should reference
`Descriptor.ID` so the artifact is unambiguous even after a rename.

```go
type Descriptor struct {
    ID      string            // stable: "mysql@1.4.2"
    Manifest *manifest.Manifest  // Name is display-only
    Source   string
    State    LifecycleState
    frozen  bool
}
func NewDescriptor(m *manifest.Manifest) Descriptor {
    return Descriptor{ID: m.Name + "@" + m.Version, ...}
}
```

### 8. Descriptor is immutable after Load (Round 8 / MUST-B)

A `Descriptor` is two distinct things — and only ONE of them freezes
(GPT Round 9 tightening of the wording):

```
Descriptor
├── Definition  (FROZEN after Load)
│     ID, Source, Manifest (incl. Manifest.Version)
└── RuntimeState (MUTABLE)
      State  (Discovered→…→Unloaded)
```

Once a `Descriptor` reaches `Loaded`, its **Definition** (`ID`,
`Source`, `Manifest` — including `Manifest.Version`) is **frozen**.
Only `RuntimeState.State` (the lifecycle position) may change thereafter.
This is why `State` can still transition even though the descriptor is
"immutable" — the immutable part is the *definition*, not the *state*.

Runtime code MUST NOT reassign `descriptor.Manifest.Version = "2"`
or otherwise mutate the definition. The manager stores the *module's*
descriptor, which is the Loaded + frozen copy; nothing is read back
from `plugin_registry` as a definition. A `Freeze()` method marks the
definition locked and `IsFrozen()` reports it (dev-time guard).

> Future state additions (e.g. a v2 lifecycle) must be a new
> `Lifecycle` version, never an in-place mutation of these six states:
> `Discovered → Loaded → Registered → Enabled → Disabled → Unloaded`.

### 9. Bootstrap & restart recovery (Round 8 / Phase 3.2)

The single plugin-startup composition root is **`Manager.Bootstrap`**
(NOT `PluginManager.Start()`). It runs:

```
Loader.Discover() -> Descriptor -> Manager stores (Loaded+frozen)
   -> Register (definition enters core.Registry; Dispatcher can Plan)
   -> Enable  (per persisted state)
```

**Recovery rule (write this down):** the `plugin_registry` table
stores **state only** (`enabled`), never the plugin *definition*. The
Loader **always** supplies the definition. On restart the flow is:

```
Bootstrap
  -> read plugin_registry (enabled state, BEFORE DiscoverAndLoad
     overwrites enabled=false)
  -> DiscoverAndLoad (re-supplies every definition)
  -> Register all loaded
  -> Enable per persisted state:
       NEW plugin (no prior row)   -> Enable  (close the loop on first boot)
       KNOWN + enabled            -> Enable  (restored)
       KNOWN + disabled           -> stay Registered/Disabled (RBAC denies)
```

This keeps a single source of truth for *what* a plugin is (the Loader)
and a single source of truth for *whether* it is active (the Registry).
A disabled plugin therefore survives a restart disabled; a newly added
plugin is enabled automatically so the skeleton closes the loop.

> **Registry freeze (GPT Round 9 — formally frozen):** `plugin_registry`
> stores lifecycle *state only*. NEVER add `manifest_json` /
> `descriptor_json` / `operations_json` columns. The moment the Registry
> holds a definition, you get TWO sources of truth (`Loader` ≠ `Registry`)
> and they WILL drift. The Loader is the single authority on *what* a
> plugin is; the Registry is the single authority on *whether* it is active.

### 10. Manifest Provider abstraction (Round 9 / Phase 3.3)

GPT Round 9 split "real plugin discovery" into distinct layers and asked
us to define the **Provider** seam BEFORE the Loader consumes it:

> Define `ManifestProvider` (List / Read); `FileProvider` is just ONE
> implementation. The Loader should CONSUME a Provider, so Git / OCI /
> HTTP later become drop-in Providers and the Loader never changes.

So `manifest.Provider` is introduced in `internal/plugin/manifest`:

```go
type Provider interface {
    List() ([]string, error)        // plugin keys available from this source
    Read(key string) (*Manifest, error) // parsed + VALIDATED at the boundary
}
```

- `List` returns the stable *keys* the source knows (subdir slugs,
  git refs, OCI digests — source-specific).
- `Read` returns the full `Manifest`, **parsed AND validated** here, so a
  malformed manifest fails at the boundary, never inside the Loader.
- `FileProvider` is the first implementation: `<Dir>/<key>/manifest.json`,
  lazy disk access, path-traversal rejected.

The Loader (Phase 3.4) will HOLD a `Provider` and turn each
`Read` result into a `Descriptor`/`Module`. Adding OCI / Git / HTTP
later means adding a `Provider`, **not** touching the Loader — that is
the entire point of the seam.

### 11. Compatibility Gate (Round 12 / 13 — Phase 3.5)

The Compatibility Gate is the LAST safety boundary a plugin must cross
before it enters the runtime. It is deliberately placed in the **Manager**,
not the Loader: the Loader's job is "discover / read the definition"; the
Manager's job is "decide whether it may enter the runtime". Keeping the gate
out of `FileLoader` preserves the Loader Contract (no `Load(path)`, no `.so`).

```go
// internal/plugin/compat
type Gate interface {
    Check(m *manifest.Manifest, kernel KernelInfo) (*Result, error)
}
```

- `Gate` is an **interface**, injected into the Manager (`Manager.SetGate`),
  defaulting to `compat.DefaultGate{}`. Tests / future policy (remote gate,
  deny-by-default) substitute their own implementation without touching the
  Loader or Manager wiring.
- `KernelInfo{Version, SupportedAPIs}` is the running kernel's
  SELF-DESCRIPTION, also injected (`Manager.SetKernel`) — the compatibility
  contract is decoupled from any hardcoded value.
- `DefaultGate` enforces two OPTIONAL constraints (unset = pass):
  `MinKernel` (kernel `Version >=` plugin `MinKernel`, semver-compared) and
  `PluginAPI` (plugin's `PluginAPI` must be in `kernel.SupportedAPIs`).
- `Result` is a **dual channel** (GPT Round 13): `Result` carries the BUSINESS
  judgment (`Compatible`, `Reason`, stable `Code` e.g. `min_kernel` /
  `plugin_api` / `invalid_kernel` / `nil_manifest`, and `Warnings []string`
  for non-fatal advisories); `error` carries a GATE FAILURE (bad config,
  parser crash, future remote-gate network error). `Result=false, err=nil`
  means "checked, incompatible"; `err!=nil` means "the gate did not finish".
  UI / REST / Audit / Telemetry should switch on the stable `Code`, not parse
  `Reason` text.
- Rejection happens in `Manager.DiscoverAndLoad` **BEFORE `Load` / `Register`**,
  so an incompatible plugin never reaches the core `Registry` or `Storage`
  as a live module. A rejected plugin is recorded best-effort as a
  `storage.Plugin` row with `Status = PluginRejected` and an append-only
  `AuditEvent{Action:"load", Result:"failure", Detail:"compatibility gate
  rejected: ..."}` (the `PluginLoadRejected` audit), so operators can see
  *why* it is absent.
- The Manifest carries the compat inputs: `SchemaVersion int` (0 = legacy /
  unversioned), `PluginAPI string`, `MinKernel string`.

> **Manifest Contract is now FROZEN.** Phase 3.8 (Round 13) completed the
> last piece: `manifest.Parse` dispatches by `schemaVersion` through a
> per-version Parser **registry** (`parsers{0:legacy, 1:current}`); an
> unknown schema version is rejected at *parse* time, so the Loader never
> sees an unparseable document. The registry is **frozen after kernel init**
> (GPT Round 14 SHOULD): register via `MustRegisterParser` (panics on
> duplicate) at startup / in an `init()`, never mid-process — ad-hoc runtime
>   registration would let parse behavior depend on call order.

### 12. Capability Negotiation (Round 17 — Phase 3.7)

Phase 3.7 is the LAST runtime negotiation capability; it closes the contract.
Capability is a **runtime-only** concept: the Manifest declares *what the
plugin needs to run*, the `capability` package models it, and the Manager
negotiates it against the host. Capability describes / negotiates /
authorizes — it does **NOT** do resource discovery or plugin distribution
(the Round 16 frozen boundary stands).

```go
// internal/plugin/capability
type Capability struct {
    Name      string // full token, e.g. "os.linux", "fs.zfs", "net.tcp"
    Namespace string // first "." segment, e.g. "os" (derived, not a boundary)
    Version   string // reserved (not yet enforced)
    Required  bool   // default true; false = optional
}
type Provider interface {
    Capabilities() []Capability
}
func Negotiate(required []Capability, host Provider) Result
```

- **Three-layer separation** (explicit Round 17 approval):
  `manifest.Capabilities []string` is the *declaration* (stable, easy to
  write); `capability.Capability` is the *runtime model*; `Manager` is the
  *negotiation entry point*. The Manifest format is untouched — a future
  `Version` / `Scope` / `Vendor` / `OptionalFlag` extends `Capability`
  without changing the Manifest declaration.
- `Negotiate` returns a **three-state** `Result`: `Granted` (host has it),
  `Missing` (`Required` + absent → `AllGranted=false`, plugin is skipped),
  `OptionalMissing` (`Required=false` + absent → recorded, **does NOT block**
  load). This is *negotiation*, not *registration*.
- `Provider` is its own interface (not a direct read of `HostSnapshot`):
  `HostProvider` / `ClusterProvider` / `TenantProvider` / `MockProvider` can
  be substituted without changing the Manager.
- Wired into `Manager.DiscoverAndLoad` and `Manager.Reload`: a missing
  *required* capability skips the plugin with a `capability` audit failure
  (`Action:"capability"`, `Detail:"code=missing-capability missing=..."`);
  a successful negotiation emits `Action:"capability"`
  (`Detail:"granted=N [optional-missing=...]"`), kept as a SHOULD for
  troubleshooting (e.g. `mysql@1.2.0 granted: os.linux, net.tcp;
  optional-missing: fs.zfs`).

> **Capability Negotiation Contract is now FROZEN.** Two documented SHOULD
> principles (Round 17 — doc-level, NOT code changes):
> - **Namespace is a logical grouping of Capability, not a permission
>   boundary.** `Capability.Namespace` (os / fs / net) groups and isolates
>   during negotiation; it is **NOT** an RBAC namespace. A future Namespace
>   Policy MUST be a *separate* Authorization layer —
>   `Capability Namespace → Negotiation → Authorization Policy` — the two
>   MUST NOT be merged into Capability Negotiation.
> - **Capability Version stays forward-compatible.** Current negotiation
>   matches by name only. A future version constraint (e.g. `os.linux >= 2.0`)
>   MUST extend the `Negotiation` logic, NOT change the Manifest declaration
>   format.

## Freeze Notice — Phase 3 Runtime Contract

As of **Round 17 (2026-07-26)** the Phase 3 Runtime Contract is **formally
frozen**. The following sixteen surfaces are finalized and MUST NOT be
modified by future work; new capabilities build *on* them:

1. Manifest / Descriptor separation (§1)
2. Loader abstraction — no paths, no `.so` (§2)
3. Lifecycle state machine (§3)
4. Module / Handler isolation boundary (§4)
5. Permission Sync — admin grants, never adds (§5)
6. Capability requirement (§6)
7. Plugin Identity — `Descriptor.ID` (§7)
8. Descriptor immutable after Load (§8)
9. Bootstrap & restart recovery (§9)
10. Manifest Provider abstraction (§10)
11. Compatibility Gate (§11)
12. Capability Negotiation (§12)
13. Audit (capability / compatibility / reload events)
14. Migration (v3 `plugin_registry`, `id` column, backward-compatible)
15. CAS (compare-and-swap on `plugin_registry.enabled`)
16. Execution SSOT (Dispatcher → Execution → Executor)

Explicitly **out of scope** for the frozen contract (future phases build on
top, never modifying it): real `.so` loading, Hot-Reload Hooks, Watcher,
Marketplace, OCI, Git, Signature, Sandbox.

> **MUST (Round 18 — Phase 4 guardrail): the Runtime Contract is frozen;
> change it only by ADR.**
> Phase 4+ MUST NOT add Runtime Contract fields, interfaces, or lifecycle
> states. If a Contract defect is found, it is fixed via a *new* ADR (or an
> explicit ADR-010 revision), never by an implicit edit during feature work.
> Peripheral capabilities build *on* the contract; they do not mutate it.
> (When Hot-Reload Hooks is eventually reached, every Hook MUST run inside
> 3.6 Reload's two-phase Commit/Rollback — never directly mutating Manager /
> Registry / PluginStore / Runtime State. A Hook is an observer / extension
> point, not a state-management entry.)

## Consequences

- Future plugin code cannot register a system-namespaced permission (MUST-2
  + reserved prefixes), cannot impersonate a builtin source, and cannot
  execute outside the Execution SSOT (type-level Isolation Boundary).
- The core `Registry` stays the runtime source of truth; `plugin_registry`
  persists only lifecycle state (5 columns, no manifest copy — YAGNI per
  Round 7).
- `internal/plugin/runtime` has **zero** dependency on the control-plane
  `sync` package (it receives a `SyncFunc` closure), so there is no
  import cycle and the contract is independently testable.
- Phase 3.1 ships **contract + state machine + Manager orchestration** with
  a `StaticLoader`/`StaticModule` test harness. Real `.so` loading,
  hot-reload, and external plugin discovery remain explicitly out of scope.

## Round 19 Outcome — Phase 4.2 PASS & Phase 4.1 Pre-freeze

Round 19 signed off Phase 4.2 (end-to-end example plugin) as PASS and froze
the Phase 4.1 Hot-Reload Hooks constraints. No Runtime Contract change is
required — Phase 4 builds *on* the frozen contract, it does not reshape it.

### Contract Reference Plugin
- `internal/plugin/example` (plugin `hostinfo`) is promoted from a Demo to a
  **Contract Reference Plugin**: it serves both as a documentation example and
  as the Runtime regression baseline.
- Any future change to Bootstrap / Loader / Registry / Reload / Compatibility /
  Capability must keep `TestHostInfoPlugin_EndToEnd` green — that is the proof
  the Runtime Core was not broken.

### Phase 4.1 Hot-Reload Hooks — MUST pre-freeze
These MUSTs are enforced on top of the frozen Runtime Contract (3.6 two-phase
Commit/Rollback is the SSOT). Hooks attach as extension points, never as
state-management entries.

- **MUST-1 — Observe only.** A Hook MUST NOT mutate Runtime State (Manager /
  Registry / PluginStore / Runtime State). It receives exactly three events:
  `BeforeCommit`, `AfterCommit`, `AfterRollback` (the 3.6 Reload lifecycle).
- **MUST-2 — Failure isolation.** A Hook failure MUST NOT affect Reload. The
  two-phase Commit/Rollback proceeds independently; Hook failures are recorded
  separately and never abort the Reload.
- **MUST-3 — Bounded execution.** A Hook MUST respect a timeout / the passed
  `context.Context` (e.g. ≤ 5s). A slow Hook may not block the Reload Commit —
  otherwise it stops being an observer and becomes part of the execution path.

### Phase 4.1 — review SHOULDs (carried forward)
- **SHOULD — bounded Hook fan-out.** Hook counts are expected to be tiny
  (typically single digits: Marketplace / Metrics / Notification / Webhook /
  Prometheus / Trace). The per-Hook goroutine fan-out is acceptable at that
  scale; a worker pool / bounded executor is a future concern, not now.
- **SHOULD — `RegisterHook` is init-phase only.** `RegisterHook` is append-only
  and called during Manager construction / kernel init; there is deliberately
  **no `UnregisterHook`**. A Hook's lifecycle is bound to the Manager's; dynamic
  unregistration mid-Reload would introduce "who owns the observer" complexity.

## Round 20 Outcome — Phase 4.1 PASS & Phase 4.3 Watcher

Round 20 signed off Phase 4.1 (Hot-Reload Hooks) as **PASS (architecture
sign-off)** and froze the Phase 4.3 Watcher constraints. After Round 19 (4.2)
and Round 20 (4.1 + 4.3), **all of Phase 4 is now implemented on top of the
frozen Runtime Contract with zero Contract changes** — only capabilities built
*on* the contract.

### Phase 4.1 Hot-Reload Hooks — PASS
Architectural sign-off confirmed the three MUSTs are enforced and no Runtime
Contract surface was added or mutated. `Hook` is an observer (read-only
`ReloadInfo`: PluginID / Name / Version / Err — no Manager / Registry / Store /
Descriptor / Module / Runtime Context write surface), failure-isolated
(per-Hook goroutine + `recover()` + `context.WithTimeout` ≤ 5s), and never
aborts the Reload.

### Phase 4.3 Watcher — MUST / SHOULD freeze
`internal/plugin/runtime/watcher.go` is a THIN layer:
**`filesystem change → enqueue reload(id) → Manager.Reload(id)`**. It adds no
Runtime Contract field, interface, or lifecycle state. Constraints:

- **MUST-1 — Observe only.** The Watcher NEVER mutates Runtime State. Its only
  write surface is `Manager.Reload`, which is itself a frozen lifecycle op
  (3.6). It holds no Manager / Registry / Store write handle beyond Reload.
- **MUST-2 — Never bypass Reload.** The Watcher does NOT Load / Register /
  Enable / Unload directly. Every change flows through the existing two-phase
  Reload. (A brand-new plugin on disk is intentionally **not** auto-onboarded —
  onboarding is the Bootstrap path with the explicit `AutoEnableNewPlugin`
  policy — so dropping a file never silently grants execution, per Phase 3.4.)
- **MUST-3 — Edge trigger, not state manager.** The Watcher tracks only a
  content hash per plugin id to detect changes; it maintains NO desired /
  observed state, NO reconciliation loop, NO Kubernetes-controller logic.
- **SHOULD-1 — Debounce.** Rapid successive changes (save + rename + chmod)
  collapse into a single Reload within a debounce window (default 300ms).
- **SHOULD-2 — Same-plugin serialization.** Reloads of one plugin are
  serialized (singleflight) so a slow Reload cannot overlap the next trigger.
- **SHOULD-3 — Thin layer.** The Watcher does not reason about Compatibility /
  Capability / Bootstrap / Generation; it only re-Discovers via the injected
  `Loader` and forwards changed ids to `Reload`. All contract enforcement
  (compat gate, capability negotiation, two-phase commit) stays inside `Reload`.

### Detection design note
Change detection is **polling-based** (no external `fsnotify` dependency): each
interval the Watcher re-Discovers via the injected `Loader` (which already reads
the filesystem) and compares each loaded plugin's manifest content hash. This is
robust to "what changed" semantics and fully decouples the Watcher from the
manifest file layout. A change is a content delta (identical bytes → no reload).

### Phase 4 freeze confirmation
Phase 4.2 (Contract Reference Plugin), Phase 4.1 (Hooks), and Phase 4.3
(Watcher) all build *on* the frozen Runtime Contract (3.6 two-phase Commit /
Rollback is the SSOT). No Runtime Contract field, interface, or lifecycle state
was added or mutated. The freeze (Round 18 MUST) holds.

## Round 21 Outcome — Phase 4.3 PASS & Phase 4 Closed

Round 21 signed off **Phase 4.3 Watcher as PASS (architecture sign-off)** and
closed Phase 4 as a whole.

### Phase 4.3 Watcher — PASS
All three MUSTs and three SHOULDs confirmed; the "no auto-onboarding of brand-new
on-disk plugins" safety trade-off was explicitly endorsed:
- **Discover ≠ Enable.** Detection (Watcher/Discover) and admission (Bootstrap)
  are distinct. A new plugin on disk gains execution only via the Bootstrap path
  (subject to `AutoEnableNewPlugin` / `BootstrapPolicy`), never by the Watcher —
  otherwise `cp plugin` would silently grant execution and bypass `BootstrapPolicy`.
- **Polling vs fsnotify** is a legitimate event-source choice that does not touch
  the Runtime Contract; a future `FsnotifyWatcher` can share the same `Watcher`
  abstraction without Contract changes.

### Phase 4 closed — Runtime Contract is the stable baseline
Phase 4.2 (Contract Reference Plugin) + Phase 4.1 (Hot-Reload Hooks) + Phase 4.3
(Watcher) are a **validation & extension layer on top of** the frozen Runtime
Contract. No Descriptor field, Runtime lifecycle, Loader interface, Bootstrap
semantics, Reload semantics, Compatibility Gate, or Capability Negotiation was
added or mutated. The freeze (Round 17 freeze / 18 guard / 19 reference / 20
hooks+watcher boundary / 21 close) holds across 5 signed rounds.

### Next-phase priority (GPT, Round 21)
Future capabilities must evolve via **new implementations or new ADRs**, never by
implicitly mutating the frozen contract. Recommended priority:
1. **Signature Verification (first priority)** — purely peripheral, does NOT change
   the Runtime Contract; sits *before* `Provider → Manifest → Compatibility`.
   Reusable by File / OCI / Git Providers. Responsibility stays simple:
   `Read Manifest → Verify Signature → Compatibility → Reload/Bootstrap`.
2. **OCI / Git Provider** — the most natural extension of the existing Provider
   abstraction (`MemoryProvider`, `FileProvider`); does not affect Runtime.
3. **Real dynamic loading (.so etc.)** — highest complexity (ABI, lifecycle,
   crash isolation, platform compat); but still just *one implementation of
   Loader* (`dynamic load → build Module → hand to existing Runtime`), not a
   Contract change.

## Round 22 Outcome — Phase 5.1 PASS & Phase 5.2 directive

Round 22 signed off **Phase 5.1 Signature Verification as PASS (architecture
sign-off)**. The design was confirmed to sit at the RIGHT boundary:

### Phase 5.1 Signature Verification — PASS
- **Zero Runtime Contract change** — confirmed against the explicit list:
  `runtime.Loader`, `Descriptor`, `Module`, `Manager`, `Bootstrap`, `Reload`,
  `Manifest schema`, `Capability`, `Compatibility Gate` all UNCHANGED. The gate
  lives inside `Provider.Read()` (raw bytes → `Verify()` → `Parse()` →
  `Validate()`), which is the external trust boundary — the Runtime only ever
  receives an already-trusted definition.
- **Ed25519** — endorsed (stdlib, short keys, fast verify, no cert chain; fits
  the detached `manifest.json` + `manifest.json.sig` model).
- **External `.sig` file** — explicitly endorsed over an in-Manifest signature
  field (avoids self-reference, keeps the frozen `Manifest` schema pure, avoids
  premature Schema-Version bloat). A mature detached-signature pattern.
- **Fail-closed** — `SignedFileProvider` demands a valid `.sig` for every
  manifest when a verifier is configured (no "unsigned = warning" backdoor).
  Two clear modes: `No verifier` = legacy, `Verifier enabled` = strict — like
  `TLS disabled` vs `TLS required`.
- **Single trust-boundary point** — because the gate is inside `Provider.Read`,
  Bootstrap / Reload / Watcher all flow through it automatically (no path can
  forget or bypass verification).

### Phase 5.1 review SHOULDs (carried forward)
- **SHOULD-1 — no detail leakage in user-facing errors.** The sentinel
  `ErrSignatureInvalid` ("manifest: signature verification failed") is already
  generic — it does not expose byte-level mismatch detail. Recommended shape
  (mirroring the Compatibility Gate `Code` + `Reason` pattern): outer message
  generic ("plugin verification failed"), audit log keeps a stable code
  (`signature-invalid`), internal log keeps the detailed cause. Current wrapping
  (`manifest provider: %q: %w`) already satisfies the generic outer layer; the
  structured audit/code enrichment is a future hardening, not blocking.
- **SHOULD-2 — Key Identity for later.** When Marketplace / OCI / enterprise
  repos arrive, extend the signature metadata *peripherally* (e.g.
  `manifest.json.sig.meta` carrying `keyID` / `algorithm`) — never inside
  `Manifest`. Not needed now.

### Next-phase directive (GPT, Round 22)
Continue with **Phase 5.2 OCI / Git Provider** ✅:
- The Provider abstraction is now proven ready (`MemoryProvider`,
  `FileProvider`, `SignedFileProvider`); `OCIProvider` and `GitProvider` are the
  natural drop-in implementations — the Loader/Runtime must NOT change.
- **Scope:** pull manifest bytes → feed existing `Verify` → return `Manifest`.
  Do NOT modify Runtime, do NOT introduce Marketplace, do NOT auto-install /
  auto-Enable.
- **5.3 Signature Policy** (required signer / key rotation / trust root) is a
  later concern, only if needed.
- **Defer `.so`** — it is an execution-isolation problem (ABI, crash isolation,
  memory safety, lifecycle, platform diffs), to land only AFTER the Provider
  peripheral layer is stable.

## Round 23 Outcome — Phase 5.2 PASS & Phase 5.3 directive

Round 23 signed off **Phase 5.2 OCI/Git Provider as PASS**.

### Phase 5.2 OCI/Git Provider — PASS
- **Runtime Contract unchanged** — confirmed: `manifest.Provider` interface,
  `Loader`, `Descriptor`/`Module`, `Manager` lifecycle, `Compatibility Gate`,
  `Capability Negotiation`, `Reload`/`Watcher` entry points all UNCHANGED.
  Provider layer now forms `File | Git | OCI` all implementing the same
  `Provider` interface; raw bytes → `Verify` → `Parse` → `Validate` →
  `Descriptor` is the single trust boundary (praised in Round 22).
- **Git via `os/exec`** (not go-git) — endorsed: avoids a heavy dependency, keeps
  the offline build, reuses the host's existing git. `lazy clone
  --no-checkout` + `git show <ref>:<path>` (blob, no checkout) is the right
  call: no startup block, no filesystem pollution, no checkout-state management.
- **OCI via stdlib `net/http` minimal Distribution v2 client** (not oras-go) —
  endorsed: OCI is treated as a *plugin artifact transport*, not a runtime; no
  container runtime, no image management. Bearer auth (401 → token → retry +
  cache) matches the OCI Distribution v2 flow.
- **Zero extra deps** — go.mod still only `golang.org/x/crypto` +
  `modernc.org/sqlite`; the offline-build iron rule holds.

### OCI artifact layout convention (frozen, NOT Runtime Contract)
The OCI layer-annotation convention `org.opencontainers.image.title` to locate
`<key>/manifest.json` and `<key>/manifest.json.sig` is endorsed — **but frozen
as a Provider Convention, not part of the Runtime Contract** (it is artifact
layout, not plugin identity / lifecycle / runtime behavior).
- Convention name: **OCI Provider Artifact Convention v1**.
- Layout: layer with `org.opencontainers.image.title = "<key>/manifest.json"`
  and another `= "<key>/manifest.json.sig"`.
- **SHOULD (future, do NOT add now):** add a fallback by `layer.mediaType`
  (`application/vnd.opscore.plugin.manifest+json` /
  `application/vnd.opscore.plugin.signature`). Keep it simple while validating
  the Provider abstraction.

### Phase 5.3 Signature Policy — APPROVED (next)
Approved as the natural next step (Phase 5.1 = *can we verify?*, Phase 5.2 =
*how sources enter the chain*, Phase 5.3 = *which signatures are trusted?*).
Deliberately **NOT** a full PKI.

**In scope (frozen constraints):**
- Trust Root: `TrustedKeys: [{key-id, public-key}]` (may carry `ValidFrom` /
  `ValidUntil` for rotation).
- Required Signer: policy `plugin namespace "system.*" requires signer "X"`.
- Key Rotation: `old key → transition period → new key` (both valid during
  transition).
- Verification Result (audit/UI/policy-decision), analogous to the Phase 3.5
  `CompatibilityResult`: `{Verified, SignerID, KeyID, Code}`.
- **MUST:** never modify `Manifest`; never modify the `Provider` interface;
  never modify `Loader`/`Manager` Contract; the policy sits **after Verify,
  before Parse/Load**; policy failure is **fail-closed**; audit MUST record
  signer + policy decision.

**Out of scope (Phase 6+):** HSM, KMS integration, Certificate Authority,
Sigstore/Fulcio, Transparency log, Supply-chain attestation.

> Implementation note: the `Verifier` type itself is peripheral (introduced in
> Phase 5.1), so evolving it to carry the trust policy (`SignatureVerifier` with
> `SignaturePolicy` + `AuditSink`) is in-bounds; the frozen Runtime Contract is
> untouched. All three Providers route through `VerifyManifest(key, data, sig,
> verifier)`, preserving the single trust boundary.

### Round 24 Outcome — Phase 5.3 PASS · Phase 5 CLOSED
- **Phase 5.3 Signature Policy: ✅ PASS.** Round 23 freeze bounds held (no Contract
  change); `SignatureVerifier` (TrustRoot + RequiredSigner + KeyRotation +
  `SignatureResult` audit) enforces trust at verify-after / parse-before.
- **One SHOULD adopted (error taxonomy refinement):** `ErrSignatureInvalid`
  (crypto failure: tampered bytes / wrong-or-external key / malformed sig) is now
  separated from `ErrSignatureUntrusted` (trust-root problem: empty or fully-expired
  trust root, or a cryptographically-valid signature under an *expired* trusted key →
  `KEY_EXPIRED`). In a closed trust-root model an "externally-valid-but-untrusted"
  signature is undecidable (no external key), so it is reported as `ErrSignatureInvalid`
  — the correct fail-closed posture. Audit/forensics clarity improved.
- **Phase 5 Overall: Trust Pipeline CLOSED · Runtime Contract UNCHANGED**
  (5.1 Verify ✅ / 5.2 Provider ✅ / 5.3 Policy ✅).
- **Next phase directive (GPT, Round 24):** first freeze a Phase 5 Final ADR /
  Stability Report (ADR-011), then proceed **Phase 6.1 Sandbox / Isolation**
  (recommended, no Contract change) → 6.2 Marketplace → 6.3 `.so` (defer, highest
  risk).

### Round 25 Outcome — Phase 5 Final Sign-off · Phase 5 CLOSED
- **Phase 5 Final ADR (ADR-011 Stability Report) submitted for sign-off.** GPT
  (Round 25) reaffirmed: **Phase 5.3 ✅ PASS**, **Phase 5 Overall CLOSED** —
  Trust Pipeline CLOSED, **Runtime Contract UNCHANGED** (5.1 Verify ✅ /
  5.2 Provider ✅ / 5.3 Policy ✅).
- **The Round 24 SHOULD (separate `ErrSignatureInvalid` from
  `ErrSignatureUntrusted`) was already implemented in commit `cc0897d`
  (Phase 5.3 follow-up).** GPT reiterated the same taxonomy as its only
  remaining note — no new MUST was raised. The error matrix now reads:
  tampered/manifest-changed → `ErrSignatureInvalid`; signature-changed →
  `ErrSignatureInvalid`; valid-but-unknown-key → `ErrSignatureUntrusted`;
  valid-key-but-disallowed-namespace → `ErrSignaturePolicy`; no-sig →
  `ErrSignatureMissing`.
- **Phase 5 is formally marked CLOSED.** ADR-011 is the frozen final doc
  (Trust Boundary, Provider Matrix, Signature Policy Matrix, Out-of-scope,
  Phase 6 candidate order).
- **Phase 6 directive (GPT, Round 25):** enter **6.1 Sandbox / Isolation**
  without changing the Runtime Contract. Suggested boundaries: resource
  boundary, execution timeout, syscall/process isolation, permission envelope —
  all peripheral. 6.1 implemented and proposed for sign-off in Round 26.

### Round 26 Outcome — Phase 6.1 Sandbox Envelope ✅ PASS (no new MUST)

**Sign-off:** *"Phase 6.1 Sandbox Envelope ✅ PASS — no new MUST."*

**Contract boundary audit (GPT, Round 26):** Manifest / Provider / Loader /
Descriptor / Module Contract / Manager lifecycle / Compatibility Gate /
Capability Negotiation / Reload·Watcher — **all unchanged**. The governing
formulation:

> **Sandbox is a Handler Decorator, not a Runtime Controller.**
> `Operation → Sandbox Wrapper → Original Handler`, **never**
> `Manager → Sandbox Manager`.

Per-item verdicts: Execution Timeout ✅ · Permission Envelope ✅ (*"permission
envelope, not permission rewrite"* — empty `Permission` is filled by the
Dispatcher and allowed; a mismatched non-empty one is an escalation and is
rejected) · Risk Envelope ✅ (*"Risk belongs to Plugin Definition, not Runtime
Decision"*) · Resource Boundary ✅ (default-off endorsed: *"this is Deployment
Policy, not Runtime Contract"*) · AuditSink ✅ (peripheral observer, decoupled
from the Runtime Audit Store) · `Wrap` idempotency ✅.

**syscall / process isolation deferred to 6.3 — explicitly endorsed.** In-process
Go cannot isolate syscalls / memory / goroutines / runtime, so the layer *"must
not masquerade as a sandbox — it is an Execution Envelope."* Real isolation
(helper process + RPC, container, or WASM) is Phase 6.3+.

**Two SHOULDs adopted in this round (implemented, not deferred):**
1. **Timeout semantics documented.** `ExecTimeout` is **fail-closed from the
   caller's perspective only**; the background goroutine is *not* guaranteed to
   have terminated (Go cannot force-cancel). Real termination requires
   cooperative cancellation or process isolation → Phase 6.3. This is now stated
   both here and in a code comment at the timeout branch in `sandbox.go`.
2. **`Decision.Code` added.** `Decision` now carries a machine-readable
   `DecisionCode` alongside the human `Reason`, matching the
   `CompatibilityResult` / `SignatureResult` precedent, so UI / Audit / Metrics
   never parse `Reason`. Codes: `allowed`, `timeout`, `permission-escalation`,
   `risk-escalation`, `step-limit`, `input-too-large`, `plan-too-large`,
   `plan-error`, `nil-plan`.

### Phase 6.2 Marketplace / Catalog — frozen scope (GPT directive, Round 26)

> **"Catalog, not Installer. Catalog must not know about the Runtime."**

**Dependency invariant (mandatory):**
`Catalog → Provider (File/Git/OCI) → PluginMetadata` — **never** `Catalog → Manager`.

**In scope (frozen):** (1) Plugin Index — ID / Version / Description / Author /
Tags; (2) Metadata Discovery — capabilities, operations, risk, plugin API,
kernel requirement, **read-only**; (3) Version Listing (e.g. `mysql → 1.0 / 1.1
/ 2.0`); (4) Search — namespace / tag / keyword; (5) Source — File, Git and OCI
unified behind one Catalog.

**Explicitly forbidden in 6.2:** ❌ Install ❌ Enable ❌ Trust ❌ Signature
Decision ❌ Download ❌ Upgrade ❌ Auto Update ❌ Dependency Resolution
❌ Marketplace Account. *"These are not Catalog."*

**Implementation note (Phase 6.2, this repo):** new peripheral package
`internal/plugin/catalog`. It consumes `manifest.Provider` only; a table-driven
`go/parser` test (`TestCatalogDoesNotDependOnRuntime`) fails the build if any
non-test file in the package imports `internal/plugin/runtime`, `internal/core`
or `internal/builtin` — the dependency invariant is enforced mechanically, not
by convention. Catalog-layer presentation fields (`Description`, `Author`,
`Tags`) live on `catalog.PluginMetadata` and are left empty when projected from
a `Manifest`; **the Manifest schema is not extended**, so the frozen Runtime
Contract stays untouched. Multi-source aggregation skips individual malformed
entries but propagates a source-level `List` failure (fail-loud on source
breakage, fail-soft on one bad record).

### Round 27 Outcome — Phase 6.2 Catalog ✅ PASS (no MUST)

**Sign-off:** *"Phase 6.2 Catalog ✅ PASS"* — no rework required. Both open
design questions were explicitly endorsed:

- **Description / Author / Tags stay out of the Manifest — ✅ agreed.** GPT's
  reasoning is now the standing rule: the Manifest describes *what a plugin is,
  what it can do, what it requires* (PluginAPI, Capabilities, Operations,
  Compatibility) — that is Runtime Contract. Author / Description / Tags /
  Logo / Homepage / License / Screenshot / Category are **Catalog Metadata**.
  Folding presentation fields into the Manifest would make the Runtime Contract
  carry Marketplace responsibilities: *"这是边界污染."* If they are ever needed,
  they get a separate `plugin.index.json` / `catalog.json` / OCI annotation.
- **Source fail-loud, single-entry fail-soft — ✅ agreed.** A broken plugin must
  not blind the catalog; a broken registry must not silently return a half
  index that the UI mistakes for the whole world.
- The `go/parser` import guard was singled out as worth keeping: *"不是靠 Code
  Review，而是测试把架构锁死."*

**Three SHOULDs adopted this round (implemented, not deferred):**
1. **Pagination** — `Query.Offset` / `Query.Limit` plus `SearchPage` returning
   `Page{Items, Total, Offset, Limit}`. **Honest boundary, recorded here:** this
   is a *result window applied after the providers are queried*, not pushdown.
   Real pushdown needs paging parameters on `manifest.Provider`, which is
   **frozen Contract** — so cheap paging over a huge OCI registry is explicitly
   deferred rather than faked.
2. **Digest** — `PluginMetadata.Digest = "sha256:<hex>"` over the canonical JSON
   of the manifest, for cache / diff / sync and "which content version is this"
   in a UI. Documented as **not** a security digest: trust lives in the Phase 5
   pipeline. Same content ⇒ same digest across sources, which is what makes it
   useful for sync.
3. **Source priority** — `Source.Priority` (lower sorts first) projected onto
   entries as `SourcePriority`, for presentation ranking only. It never implies
   trust and never selects "which one to install", because the catalog does not
   install.

### Phase 6.3 Process Isolation — frozen scope (GPT directive, Round 27)

Named **Process Isolation** — deliberately *not* "Sandbox" and *not* "Dynamic
Plugin". Goal: move `Handler.Plan` into a helper process.
`Manager → Dispatcher → RPC Client → Plugin Helper → Handler.Plan()`.

- **MUST-1** `Handler.Plan()` signature and interfaces unchanged.
- **MUST-2** Manager / Registry / Reload / Watcher never learn a helper process
  exists; they only ever see a `Module` / `core.Handler`.
- **MUST-3** Timeout is upgraded to **real termination** (kill the process),
  which Phase 6.1 structurally could not do.
- **MUST-4** A helper crash must not affect Manager / Dispatcher / Registry.
- **MUST-5** Failure stays fail-closed.

**Still forbidden:** `.so` / Go plugin, WASM, OCI runtime, containers, seccomp,
ptrace, cgroup, namespace. On `.so` the position is unchanged — deferring is not
about capability but cost: *"它会带来 ABI、Go 版本、平台、CGO、生命周期、崩溃恢复
大量新问题… 没有必要为了 .so 重新打开一个已经稳定的 Contract."*

**Implementation note (Phase 6.3, this repo):** new peripheral package
`internal/plugin/isolation` (`protocol.go` / `host.go` / `helper.go`).

- *Why the boundary is clean:* OpsCore already separates **Plan from Execute**.
  A handler returns an `ExecutionPlan`, which is pure data, and the trusted
  host-side Executor runs the steps. So isolation fences off *planning logic*
  while execution stays exactly where it was — the plan is serializable by
  construction.
- *Wire format:* length-prefixed JSON frames (`OPSCORE-ISO/1 <n>\n<json>`) over
  the helper's stdin/stdout, stdlib only (`os/exec` + `encoding/json`), no new
  dependency. Length prefixing lets the host cap a response **before**
  allocating, and desynchronises rather than silently accepting a plugin that
  scribbles on stdout.
- *Step encoding is a discriminated union.* Only step kinds with a declared wire
  form may cross (`command` in v1); an unknown kind is a hard error, never a
  dropped step. An isolated plugin is therefore confined to steps the trusted
  Executor already knows how to run.
- *Credential firewall (a security property that falls out of isolation).*
  `core.TargetHost` carries SSH `Password` / `KeyPath` / `KeyBytes`. An
  in-process handler can read them off `ctx.Target()`; the projection sent to a
  helper carries **Address / Port / User only**. The plugin plans *what* to run
  against a host without ever holding the secret that reaches it. Pinned by
  `TestCredentialsNeverCrossTheBoundary`.
- *Known v1 boundary:* the rebuilt helper-side context has an **empty
  CapabilityContext**. Capability is host-observed (ADR-009); auto-detection is
  deliberately disabled in the helper because a helper probing its own machine
  would report the *helper's* capabilities, which for a remote target is
  silently wrong. An empty capability set is honest; a locally detected one
  would be a lie. Capability-aware planning for isolated plugins needs a
  projected snapshot — peripheral, but a new wire surface, so it is raised for
  ruling rather than assumed.
- *Process-per-invocation* in v1: no state leaks between operations and no pool
  lifecycle to get wrong. Pooling is a later performance layer that does not
  change this surface.
- *MUST-2 is enforced mechanically in both directions* by
  `TestManagerIsUnawareOfProcessIsolation`: isolation must not import
  `internal/plugin/runtime`, **and** the runtime must not import
  `internal/plugin/isolation`. The design erodes into a red test, not past a
  code review.
- *MUST-3 evidence:* the helper sleeps 30s, `ExecTimeout` is 400ms, and the call
  returns in ~0.4s **after `Wait()` reaps the process** — proof of termination,
  not merely of a caller giving up.
- *MUST-4 evidence:* a panicking helper yields `ErrHelperCrashed` with its panic
  trace captured from stderr, and the very next invocation on a healthy helper
  still succeeds. `Serve` deliberately does **not** recover panics into a
  response: a panicking plugin should take its own process down, which is
  precisely the failure the host is now insulated from.
- *Outcome codes* follow the established convention (`CompatibilityResult` /
  `SignatureResult` / `sandbox.DecisionCode`): `ok`, `spawn-failed`,
  `timeout-killed`, `helper-crash`, `protocol-error`, `plugin-error`,
  `response-too-large`, `unserializable-plan`.

---

## Round 28 Outcome — Phase 6.3 Process Isolation ✅ PASS (no MUST)

GPT signed off Phase 6.3 with no new MUST blocking. Highlights:
- All five MUSTs PASS. MUST-3 (REAL termination) called a qualitative change over
  6.1; MUST-4 (deliberate non-recovery of panics into a Response) explicitly
  endorsed — *"crash and error are different things."*
- Two design byproducts elevated to ADR material:
  - The `ExecutionStep` wire form is formally a **Versioned Wire Protocol** (a
    discriminated union): new step kinds — SQL / HTTP / Docker — must be added
    to the wire EXPLICITLY, never auto-supported.
  - Context projection is *"true least privilege … better than many mature
    frameworks"*; the credential firewall is a first-class security property.
- Three rulings delivered (raised as open questions in the Round 28 prompt):
  - **(a) Capability projection:** stay Capability-blind in v1; IF a snapshot is
    projected later, ONLY a host-generated `CapabilitySnapshot` crosses — the
    helper MUST NOT self-detect. Projection, never detection.
  - **(b) Helper registration:** use a **host-side Deployment Mapping** (`plugin
    id → helper path`), NOT a Manifest field. Process is deployment strategy, not
    plugin identity, so Manifest must not carry `runtime: process` (would be
    Contract drift).
  - **(c) Executable Plugin:** confirmed as the **recommended Phase 7 direction**;
    `.so` becomes an optional backend, not the main route — cross-language, no
    ABI / CGO / Go-plugin, no panic pollution of the host.
- Phase 6.4 proposed: **Execution Projection** — complete the Host→Helper
  read-only projection. Then publish a **Phase 6 Stability Report** and put
  Runtime Core into long-term stable maintenance.

## Phase 6.4 frozen scope — Execution Projection

Goal (single): complete the read-only information projection from Host to Helper,
so an isolated plugin can plan against host-observed reality instead of being
blind. **Projection, never detection** — the helper consumes what the host
observed; it never re-discovers, re-probes, or touches the host directly.

In scope (all peripheral; ZERO Runtime Contract change):
- `ContextProjection` gains OPTIONAL, backward-compatible (`omitempty`) v1 fields:
  - `capabilitySnapshot` — `*snapshot.CapabilitySnapshot`, read-only, host-generated.
  - `hostSnapshot` — `*snapshot.HostSnapshot`, read-only identity
    (ID / Name / Address / OS / Arch / Platform / Version / Kernel / User).
  - `requestId` — from `ctx.ExecutionID()` (already on `core.Context`; no Contract
    change).
- `RebuildContext` consumes them via `WithCapabilitySnapshot` (read-only, no
  detect), `WithHostSnapshot`, `WithExecutionID`. When the host projects nothing,
  the helper stays Capability-blind and auto-detection stays disabled — the
  honest default.
- Host Labels / Inventory Snapshot: NOT projected — those types do not exist in
  ADR-009's snapshot model; recorded as deferred (GPT said *"若已有"*).
- Ruling (b) landing — host-side deployment wiring, NOT Manifest:
  - `Deployment{Operation, Path, Args, Env, Dir, ExecTimeoutSeconds, MaxResponseMB}`.
  - `DeploymentMap` (operation-keyed) + `Map.Handler(op) (core.Handler, bool)` +
    `LoadMap(path)` (JSON, stdlib-only). Manager never imports this package; it
    only ever receives the returned `core.Handler`.
- Wire protocol stays `opscore.isolation/v1`. The new projection fields are
  optional, so an old helper simply stays Capability-blind — no version bump.

Out of scope (unchanged from 6.3): .so / Go plugin / WASM / OCI runtime /
containers / seccomp / ptrace / cgroup / namespace. Also out: ruling (c)
execution — that is the Phase 7 direction, not 6.4.

Runtime Contract impact: ZERO. Manifest / Provider / Loader / Descriptor /
Module / Manager lifecycle / Compatibility Gate / Capability Negotiation /
Reload·Watcher all untouched. The projection reads existing
`ctx.CapabilitySnapshot()` / `ctx.HostSnapshot()` / `ctx.ExecutionID()` — no new
getter, no Contract edit.

Quality gate: `gofmt` clean on the package; `go build ./... && go vet ./... &&
go test ./...` green. Tests cover: capability snapshot projected read-only and
NOT re-detected; host snapshot present when projected; requestId round-trips;
capability-blind when not projected; deployment map routes an op to a working
helper and returns `(nil, false)` for an unmapped op; deployment map loads from
JSON.
