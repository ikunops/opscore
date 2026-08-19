# ADR-020 — Phase 9.1: Platform Integration Layer (Read-only Facade)

- **Status**: Accepted (Round 49) — implementation in Phase 9.2 (Round 50)
- **Date**: 2026-08-07
- **Companion to**: ADR-019 (Phase 9.0 Continuation Scope — Accepted, R48), ADR-014 (Phase 8
  CLOSED), ADR-015/016/017/018 (the four Phase 8 capability ADRs)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 9 — Post-Phase-8 Continuation, Direction A

---

## 0. Abstract

Per GPT (Round 48) ADR-019 is **Accepted** and direction **(A) Platform Integration Layer** is
selected. This ADR is the **architecture ADR for Phase 9.1** — the concrete design of the
read-only facade that aggregates the four closed Phase 8 capabilities into composable views.

This ADR is **spec only**. Per GPT (R48): *"Round 49 submit ADR-020 (spec only, no code)."* No
implementation lands until this ADR is signed; the implementation follows in Phase 9.2 (a later
round) after sign-off.

The facade **reads** existing capability state and returns **composed read models**. It owns no
data, creates no new execution path, and never becomes a Control Plane — the same invariants
ADR-019 §1 (MUST-0) established for all of Phase 9.

---

## 1. Positioning — read-only composition, not a new system

Phase 9.1 sits *above* the four closed capabilities as a **query facade**:

```
Observability   Cluster   Enterprise   Governance      (all closed, ADR-015..018)
      \           |           |            /
       \          |           |           /
        v         v           v          v
     ┌──────────────────────────────────────┐
     │   Phase 9.1 Platform Integration      │   reads only
     │   (internal/platformview — read facade)   │   composes views
     └──────────────────────────────────────┘
                     |
                     v
            composed read models
        (ExecutionOverview / HostPolicyStatus /
         GovernanceSummary / ClusterPlacementView)
```

It is the natural productization of Phase 8: the four capabilities each expose a query API
(`Collector.Query*`, `Manager.Members`/`ComputePlacement`, `Service.AttachmentsFor`,
`Engine.Evaluate`); Phase 9.1 gives a single consumer one composed view instead of four.

---

## 2. Freeze boundaries (inherited + this ADR)

- **MUST-0 (Phase 9一级冻结, ADR-019)** — Phase 9.1 introduces no new execution entry, does not
  replace the Runtime API, and does not become a Control Plane. Read / correlate / document only.
- **MUST-1 — Read-only facade.** The layer *reads* existing capability state; it owns no data
  source. `Platform → Capability.Query` only; never `Platform → Capability.Mutate`.
- **MUST-2 — No new identity.** Reuse the existing ID space
  (`ExecutionID` / `RequestID` / `PluginID` / `HostID` / `RuntimeID` / `PolicyID` / `AuditID`).
  It must **not** mint `PlatformID` / `PlatformExecutionID` or any new entity identifier.
- **MUST-3 — No command surface.** The API exposes only `GET` / `query` / `view`. It forbids
  `POST execute` / `POST run` / `POST apply` (and any `Execute`/`Schedule`/`ApproveAndRun`/
  `ApplyPolicy` method).
- **MUST-4 — Ownership preserved.** Data ownership is unchanged; the facade is a *composition
  view* only:

  | Data | Owner (unchanged) |
  |---|---|
  | Observation | Observability |
  | Membership / Placement | Cluster |
  | Attachment | Enterprise |
  | Verdict | Governance |
  | Execution | Runtime |

- **MUST-5 — Aggregation ≠ new model.** A `Platform View` may *compose* existing models; it must
  **not redefine** them. Field shapes come from the owning capability; the facade only assembles
  references + copies read-only.
- **SHOULD-1 — Explainable view.** Every composed view carries `source` / `timestamp` /
  `relatedIDs` / `originCapability`, so an enterprise consumer can trace each field back to its
  owning capability.
- **SHOULD-2 — Stable read contract.** The facade freezes its output schema under a version
  (`platform/view/v1`). UI/API evolution happens at the view version boundary, never by reaching
  into the frozen capabilities.
- **SHOULD-3 — Lazy aggregation (on-demand).** The facade holds **no stored state and no cache**,
  and runs **no background sync** of the four capabilities. Each query calls the owning
  capability's existing query API *at request time*, composes a fresh `View`, and returns. Platform
  never becomes a memory cache or a data warehouse (Round 49 recommendation).

---

## 3. Package structure (proposed — not implemented this round)

A single peripheral package, consistent with the four Phase 8 packages.

> **Package-path note (Round 50 implementation).** The path `internal/platform` is *already taken*
> by the Phase 2.x HostSnapshot **resolver** (frozen legacy, not modified by Phase 9.1 — see
> `git log -- internal/platform/`). To honor MUST-0 / ownership and avoid touching a frozen package,
> the Phase 9.1 read facade is implemented under **`internal/platformview`** instead. The ADR's
> design (View value objects, Reader interfaces, Facade, AST guard) is unchanged; only the import
> path differs. `internal/platform` (resolver) remains untouched.

```
internal/platformview/
    model.go              # View value objects (composed, read-only)
    facade.go             # Facade: GetExecutionOverview / GetHostPolicyStatus /
                          #   GetGovernanceSummary / GetClusterPlacementView
    errors.go             # input/query errors only (no execution errors)
    platformview_test.go  # AST guard + TestNoExecMethod + behavior/determinism
```

The facade holds **no stored state** of its own. Each method:
1. Takes an existing ID (MUST-2).
2. Calls the owning capability's *existing* query API (MUST-1 / MUST-5).
3. Assembles a composed `View` (SHOULD-1 fields).
4. Returns it. No mutation, no side effect.

**AST guard (MUST-0 / MUST-3):** the package forbids importing the frozen systems
(`runtime`, `isolation`, `hostregistry`) and forbids exec methods
(`Run`/`Exec`/`Apply`/`Schedule`/`Emit`/`Execute`). It may import `observability` / `cluster` /
`enterprise` / `governance` **only to call their public query APIs** — never to re-implement or
mutate them.

---

## 4. Composed read models (sketch — not implementation)

```
// All fields are copied read-only from the owning capability. No new fields invented.
type ExecutionOverview struct {
    ExecutionID  string            // from execution / core (MUST-2)
    Observation  *ObservationView // from internal/observability
    Placement    *PlacementView    // from internal/cluster
    Attachments  []AttachmentView  // from internal/enterprise
    Verdict      *VerdictView      // from internal/governance
    ViewMeta     Meta              // SHOULD-1: source/timestamp/relatedIDs/originCapability
}

type HostPolicyStatus struct {
    HostID       string
    Attachments  []AttachmentView  // Enterprise
    Verdicts     []VerdictView     // Governance
    ViewMeta     Meta
}

type GovernanceSummary struct {
    PolicyID     string
    MatchedRules []RuleView        // Governance
    RecentVerdicts []VerdictView   // Governance
    ViewMeta     Meta
}

type ClusterPlacementView struct {
    HostID    string
    Groups    []string
    Labels    map[string]string
    Placement PlacementView        // Cluster
    ViewMeta  Meta
}
```

The facade methods (names from GPT R48): `GetExecutionOverview(id)`, `GetHostPolicyStatus(host)`,
`GetGovernanceSummary(policy)`, `GetClusterPlacementView(host)`. Each returns the corresponding
`View` by querying the owning capability. **No `Execute`/`Schedule`/`Apply` methods exist.**

---

## 5. Stable read contract (SHOULD-2)

The composed view schema is versioned and frozen:

```
platform/view/v1   // composed read models; evolve by bumping v2, not by editing capabilities
```

A consumer (ops dashboard, CLI) binds to `platform/view/v1`. Adding a field is a view-version
bump; the four capabilities underneath stay frozen.

---

## 6. Out of scope — explicitly forbidden for Phase 9.1

- Any `Execute` / `Run` / `Schedule` / `ApproveAndRun` / `ApplyPolicy` (MUST-3).
- Creating `PlatformID` / `PlatformExecutionID` or any new entity (MUST-2).
- Ownership transfer: the facade never takes ownership of Observation / Membership / Attachment /
  Verdict / Execution (MUST-4).
- Redefining capability models; the facade only composes (MUST-5).
- Modifying Runtime Contract / Plugin Ecosystem / any Phase 8 capability (MUST-0, ADR-014 §2).
- A new Control Plane or event bus (MUST-0).

---

## 7. Phase 9 authorization chain

| Step | Deliverable | Status |
|---|---|---|
| 9.0 | Phase 9 Continuation Scope (ADR-019) | Accepted (R48) |
| **9.1** | **Platform Integration Architecture (this ADR-020)** | **Accepted (R49)** |
| 9.2 | Platform Integration Implementation (`internal/platformview`) | **Implemented (R50, `e1c1195`)** |

Phase 9.1 is signed before any code (Architecture First, ADR-019 MUST-5). Implementation lands in
9.2 only after GPT signs this ADR.

---

## 8. Sign-off (Round 49)

| Item | Verdict | Round |
|---|---|---|
| MUST-0 No new execution path / no Control Plane | ✅ PASS | 49 |
| MUST-1 Read-only facade (owns no data) | ✅ PASS | 49 |
| MUST-2 No new identity (reuse existing IDs) | ✅ PASS | 49 |
| MUST-3 No command surface (GET/query/view only) | ✅ PASS | 49 |
| MUST-4 Ownership preserved (composition view) | ✅ PASS | 49 |
| MUST-5 Aggregation ≠ new model | ✅ PASS | 49 |
| SHOULD-1 Explainable view | ✅ PASS | 49 |
| SHOULD-2 Stable read contract `platform/view/v1` | ✅ PASS | 49 |
| SHOULD-3 Lazy aggregation (no state / no cache) | ✅ PASS (added R49) | 49 |

GPT (R49) verified each boundary against the local (un-pushed) ADR text and confirmed the facade
stays a Read Model, not a Control Plane — Runtime remains the sole execution entry. All five MUSTs
and three SHOULDs hold. **Authorized to implement `internal/platformview` in Round 50 (Phase 9.2).**

*Phase 9.1 Platform Integration Layer — one read-only facade over the four closed Phase 8
capabilities, owning nothing, executing nothing, composing only (Accepted, Round 49).*

## 9. Implementation sign-off placeholder (Round 50)

| Item | Verdict | Round |
|---|---|---|
| `internal/platformview` implements Query aggregation only | ✅ PASS | 50 |
| AST guard forbids frozen-system imports | ✅ PASS | 50 |
| `TestNoExecMethod` forbids Run/Exec/Invoke/Apply/Execute/Command/Emit/Dispatch/Rollback/Kill/Schedule | ✅ PASS | 50 |
| No new ID / Store / Cache / Runtime Dependency | ✅ PASS | 50 |
| Runtime Contract + four Capabilities unmodified | ✅ PASS | 50 |
| ViewVersion = `platform/view/v1`; explainability; lazy; all Views value objects | ✅ PASS | 50 |

GPT (R50) confirmed each boundary against the stated implementation and signed off Phase 9.1. The
package-path adjustment `internal/platform` → `internal/platformview` is recognized as an
**Architecture Preserving Refactoring** — only the package path changed to avoid collision with the
frozen Phase 2.x HostSnapshot resolver; the architecture responsibility (read-only facade) is
unchanged. **Phase 9 is CLOSED (R50).**
