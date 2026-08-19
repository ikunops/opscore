# ADR-024 — Phase 11.1: External Interface Architecture (Read-only Public Contract)

- **Status**: Accepted (Round 56) — implementation in Phase 11.2 (Round 57)
- **Date**: 2026-08-07
- **Companion to**: ADR-023 (Phase 11.0 Scope — Accepted, R55), ADR-022 (Phase 10 Event Correlation,
  CLOSED), ADR-020/019 (Phase 9 Platform Integration, CLOSED), ADR-021 (Architecture Baseline, frozen)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 11 — External Interface (consumption boundary over the frozen baseline)

---

## 0. Abstract

Per GPT (Round 55) ADR-023 is **Accepted** and direction **(A) External Interface Architecture** is
selected. This ADR is the **architecture ADR for Phase 11.1** — the concrete design of a stable,
versioned, **read-only Public Contract** that exposes `platformview` + `correlation` to external
consumers (Read API / CLI / SDK / Integration Adapter).

This ADR is **spec only**. Per GPT (R55): *"Round 56 submit ADR-024 (spec only, no code)."* No
implementation lands until this ADR is signed; the implementation follows in Phase 11.2 (Round 57)
after sign-off.

The External Interface **reads** the two closed read-facades and returns **versioned read DTOs**. It
owns no domain data, creates no new execution path, and never becomes a Control Plane — the same
invariants ADR-023 §1 (MUST-0) established for all of Phase 11:

> *Interface exposes capability, it does not become capability.*

GPT (R55) added three non-blocking suggestions, folded into this ADR as **MUST-4 (Contract
Ownership)**, **SHOULD-2 (Public Contract Compatibility)**, and **SHOULD-3 (Read Boundary
Enforcement)**.

---

## 1. Positioning — read-only consumption boundary, not a new system

Phase 11.1 sits *above* `platformview` + `correlation` as a **contract boundary**:

```
Runtime Core        Plugin Ecosystem      Platform Operations
        \                 |                     |
         \                |                     |
          v               v                     v
        (all frozen / closed — ADR-010..018)
                        |
                        v
        ┌──────────────────────────────────────┐
        │  Platform Integration  platformview    │   reads only  (ADR-020)
        │  Event Correlation    correlation      │   reads only  (ADR-022)
        └──────────────────────────────────────┘
                        |
                        v   (THE ONLY COMPLIANT READ SOURCE — MUST-4/ADR-023 MUST-4)
        ┌──────────────────────────────────────┐
        │  Phase 11.1 External Interface          │   read contract only
        │  (internal/external — external/v1)       │   no execution
        └──────────────────────────────────────┘
                        |
                        v
        Read API  |  CLI  |  SDK  |  Integration Adapter   (all bind to external/v1)
```

It is the natural consumption face of the closed five-tier system: `platformview` and `correlation`
already aggregate the four Phase 8 capabilities; the External Interface gives external systems one
**stable, versioned, read-only** projection instead of reaching into either facade.

---

## 2. Freeze boundaries (inherited + this ADR)

- **MUST-0 (Phase 11 一级冻结, ADR-023)** — The External Interface introduces no new execution
  entry, does not replace the Runtime API, and does not become a Control Plane. Read / expose only.
  It MUST NOT wrap an Executor, trigger execution, or act as a command surface.
- **MUST-1 — Read-only contract.** The layer *reads* `platformview` / `correlation`; it owns no data
  source. `External → Facade.Query` only; never `External → Capability.Mutate`.
- **MUST-2 — No new identity.** Reuse the existing ID space (`ExecutionID` / `PluginID` / `HostID` /
  `PolicyID` / `Scope` refs / `CorrelationView` refs). It must **not** mint `ExternalID` /
  `APITokenID-as-entity` or any new domain entity identifier. (Authn produces a *credential*, not a
  domain entity — see MUST-5/Tenant seam.)
- **MUST-3 — No command surface.** The contract exposes only `GET` / `query` / `view`. It forbids
  `POST execute` / `POST run` / `POST apply` (and any `Execute`/`Schedule`/`ApproveAndRun`/
  `ApplyPolicy`/`Mutate` method). Same rule as ADR-020 MUST-3.
- **MUST-4 — Contract ownership only (folded from R55 SHOULD-1).** `internal/external` owns the
  **DTO contract** (`external/v1` types) and the mapping *from* facade views *to* DTOs. It does
  **not** own, redefine, or evolve the Domain Model, Event Model, or Runtime Model. The DTO is a
  projection of `platform/view/v1` + `correlation/view/v1`; changes to core models flow *up* into
  the DTO via a view-version bump, never *down* into the core. This prevents DTO evolution from
  reverse-polluting the frozen core.
- **MUST-5 — Contract ≠ Core Model.** An `external/v1` DTO may *compose* facade views; it must **not
  redefine** them. Field shapes come from `platformview` / `correlation`; the External Interface only
  assembles references + copies read-only (mirrors ADR-020 MUST-5).
- **MUST-6 — Frozen, versioned contract.** The read contract is frozen under `external/v1`. Evolution
  happens by bumping `external/v2`, never by silently editing `external/v1` (see SHOULD-2).

Plus the R55 enhancements:

- **SHOULD-1 — Explainable DTO.** Every external DTO carries the originating `ViewVersion`
  (`platform/view/v1` / `correlation/view/v1`) and `sourceRefs` / `correlatedAt` provenance, so a
  consumer can trace each field back to its owning facade. (Consistent with ADR-020 SHOULD-1.)
- **SHOULD-2 — Public Contract Compatibility (folded from R55 SHOULD-2).** Versioning rules:
  - `external/v1` is the committed stable line. Adding a *new* field or resource is a **v2** bump;
    removing/renaming/changing-type of an existing `v1` field is a **breaking change** and MUST NOT
    occur on `v1`.
  - A breaking change requires a new `external/vN+1` to coexist with `v1` for a deprecation window;
    `v1` is frozen for the window. No silent breaking change on a live version.
- **SHOULD-3 — Read Boundary Enforcement (folded from R55 SHOULD-3).** The package's AST guard is
  extended beyond the peripheral-package baseline (ADR-020 §3): `internal/external` forbids importing
  - the Runtime execution path (`runtime`, `isolation`, `hostregistry`, host lifecycle),
  - the Plugin runtime (`plugin/runtime`, `plugin/isolation`, loader),
  - and any `executor` surface.
  It may import `platformview` / `correlation` **only to call their public query APIs** — never to
  re-implement or mutate them. The External Interface is the *outermost* consumption face.

---

## 3. Package structure (proposed — not implemented this round)

A single peripheral package plus a thin CLI entrypoint, consistent with the peripheral-package
discipline (ADR-020 §3, ADR-022 §3).

```
internal/external/
    model.go              # external/v1 DTO value objects (read-only projections of facade views)
    facade.go             # Server: Query methods over platformview+correlation via Reader interfaces
    authn.go              # Authn/Tenant seam (interface; v1 = single-tenant / no-auth stub)
    errors.go             # contract/query errors only (no execution errors)
    external_test.go      # AST guard (SHOULD-3) + TestNoExecMethod + determinism + authn seam

cmd/opscore-cli/
    main.go               # R57: thin CLI binding to internal/external (same external/v1 contract)
```

The `Server` holds **no stored state** of its own. Each query:
1. Takes an existing ID / `Scope` ref (MUST-2).
2. Calls the owning facade's *existing* query API (`platformview` / `correlation`) (MUST-1 / MUST-4).
3. Maps the facade `View` → an `external/v1` DTO (MUST-4 / MUST-5).
4. Returns it. No mutation, no side effect, no execution.

**Authn / Tenant seam (MUST-0 / MUST-5).** `authn.go` defines an `Authenticator` interface with a
single method `Authenticate(ctx) (Tenant, error)`. The **v1 implementation is a single-tenant /
no-auth stub** — it returns a constant tenant and never rejects. The seam MUST exist so v2 can plug
real authn without reshaping the contract. Tenant is a *passthrough* on v1 (no multi-tenant
isolation logic); the boundary is declared, not exercised.

**AST guard (MUST-0 / MUST-3 / SHOULD-3):** the package forbids importing the frozen systems
(Runtime execution path, Plugin runtime, host lifecycle, any executor surface) and forbids exec
methods (`Run`/`Exec`/`Apply`/`Schedule`/`Emit`/`Execute`/`Mutate`/`Invoke`). It may import
`platformview` / `correlation` **only to call their public query APIs**.

---

## 4. Read DTOs (sketch — not implementation)

```
// All fields are copied read-only from the owning facade view. No new fields invented.
// DTO owns the wire shape only; the model stays in platformview/correlation (MUST-4).

type ExecutionView struct {        // source: platformview.ExecutionOverview
    ExecutionID  string
    Observation  *ObservationDTO    // from platformview
    Placement    *PlacementDTO      // from platformview / cluster
    Attachments  []AttachmentDTO    // from platformview / enterprise
    Verdict      *VerdictDTO        // from platformview / governance
    ViewMeta     DTOViewMeta        // SHOULD-1: ViewVersion + sourceRefs + correlatedAt
}

type CorrelationView struct {       // source: correlation.CorrelationView
    Scope       ScopeDTO
    SourceRefs  []string
    Reason      string
    CorrelatedAt string
    ViewMeta    DTOViewMeta
}

type HostView struct {              // source: platformview.HostPolicyStatus / ClusterPlacementView
    HostID    string
    Groups    []string
    Labels    map[string]string
    Attachments []AttachmentDTO
    Verdicts    []VerdictDTO
    ViewMeta  DTOViewMeta
}

type PolicyView struct {            // source: platformview.GovernanceSummary
    PolicyID      string
    MatchedRules  []RuleDTO
    RecentVerdicts []VerdictDTO
    ViewMeta      DTOViewMeta
}

type DTOViewMeta struct {           // SHOULD-1 provenance
    ViewVersion   string   // "external/v1"
    SourceView    string   // "platform/view/v1" | "correlation/view/v1"
    SourceRefs    []string
    CorrelatedAt  string
}
```

The `Server` query methods (names from GPT R55): `GetExecution(id)`, `GetHost(host)`,
`GetPolicy(policy)`, `GetCorrelation(scope)`. Each returns the corresponding `external/v1` DTO by
querying the owning facade. **No `Execute`/`Schedule`/`Apply`/`Mutate` methods exist.**

**CLI / SDK contract strategy.** `external/v1` is the single source of truth for the wire shape.
The CLI (`cmd/opscore-cli`) and any future SDK are *derived from* the same contract definitions
(generated or hand-bound to `internal/external/model.go`), so all three surfaces (API / CLI / SDK)
stay byte-compatible on `external/v1`. No surface invents its own schema.

---

## 5. Stable read contract (MUST-6 / SHOULD-2)

The read contract is versioned and frozen:

```
external/v1   // read DTOs over platformview + correlation; evolve by bumping v2, not by editing v1
```

A consumer (ops dashboard, CLI, SDK, integration adapter) binds to `external/v1`. Adding a resource
or field is a `v2` bump; the facades underneath (`platform/view/v1`, `correlation/view/v1`) and the
frozen capabilities stay untouched. A breaking change on a live version is forbidden — coexists as a
new `external/vN+1` for a deprecation window (SHOULD-2).

---

## 6. Out of scope — explicitly forbidden for Phase 11.1

- Any `Execute` / `Run` / `Schedule` / `ApproveAndRun` / `ApplyPolicy` / `Mutate` (MUST-3 / MUST-0).
- Creating `ExternalID` / `APITokenID-as-entity` or any new *domain* entity (MUST-2). Credentials are
  authn artifacts, not domain entities.
- Ownership transfer: `internal/external` never takes ownership of Observation / Membership /
  Attachment / Verdict / Execution / Correlation (MUST-4). It owns DTOs only.
- Redefining facade or core models; the External Interface only composes (MUST-5).
- Modifying Runtime Contract / Plugin Ecosystem / any Phase 8 capability / `platformview` /
  `correlation` (MUST-0, ADR-014 §2 / ADR-019/020/022).
- A new Control Plane or event bus (MUST-0).
- Real multi-tenant authz logic in v1 — v1 ships a single-tenant / no-auth stub; the seam exists,
  isolation is deferred (MUST-5 Authn seam).
- A write/mutate Public API (that is a distinct Major Evolution, not Phase 11).

---

## 7. Phase 11 authorization chain

| Step | Deliverable | Status |
|---|---|---|
| 11.0 | Phase 11 External Interface Scope (ADR-023) | Accepted (R55) — direction A |
| **11.1** | **External Interface Architecture (this ADR-024)** | **Accepted (R56)** |
| 11.2 | External Interface Implementation (`internal/external` + `cmd/opscore-cli`) | **Implemented (R57)** |

Phase 11.1 is signed before any code (Architecture First, ADR-023 MUST-5). Implementation lands in
11.2 only after GPT signs this ADR.

---

## 8. Sign-off (Round 56)

| Item | Verdict | Round |
|---|---|---|
| MUST-0 No new execution path / no Control Plane / no capability mutation | ✅ PASS | 56 |
| MUST-1 Read-only contract (reads platformview+correlation only) | ✅ PASS | 56 |
| MUST-2 No new domain identity (reuse existing IDs) | ✅ PASS | 56 |
| MUST-3 No command surface (GET/query/view only) | ✅ PASS | 56 |
| MUST-4 Contract ownership only (DTO, not domain/event/runtime model) | ✅ PASS | 56 |
| MUST-5 Contract ≠ Core Model (compose facades) | ✅ PASS | 56 |
| MUST-6 Frozen, versioned contract `external/v1` | ✅ PASS | 56 |
| SHOULD-1 Explainable DTO (ViewVersion + provenance) | ✅ PASS | 56 |
| SHOULD-2 Public Contract Compatibility (`v1`+`v2`, no silent break) | ✅ PASS | 56 |
| SHOULD-3 Read Boundary Enforcement (extended AST guard) | ✅ PASS | 56 |
| Authn/Tenant seam present (v1 single-tenant stub) | ✅ PASS | 56 |

GPT (R56) verified each boundary against the ADR text and signed off Phase 11.1 (PASS, with a
document-consistency note: MUST-0..6 are contiguous — MUST-4 Contract Ownership, MUST-5 Contract ≠
Core Model, MUST-6 Versioned Public Contract — which this ADR already follows). **Authorized to
implement `internal/external` + `cmd/opscore-cli` in Round 57 (Phase 11.2).**

---

## 9. Implementation sign-off placeholder (Round 57)

| Item | Verdict | Round |
|---|---|---|
| `internal/external` implements query mapping only (no execution) | ✅ PASS | 57 |
| AST guard forbids frozen-system + executor imports (SHOULD-3) | ✅ PASS | 57 |
| `TestNoExecMethod` forbids Run/Exec/Invoke/Apply/Execute/Command/Emit/Dispatch/Rollback/Kill/Schedule/Mutate | ✅ PASS | 57 |
| No new domain ID / Store / Cache / Runtime Dependency | ✅ PASS | 57 |
| DTOs own wire shape only; facades + core unmodified (MUST-4/5) | ✅ PASS | 57 |
| ViewVersion = `external/v1`; explainability; all DTOs value objects | ✅ PASS | 57 |
| Authn seam interface present; v1 single-tenant stub | ✅ PASS | 57 |
| CLI/SDK bind to the same `external/v1` contract | ✅ PASS | 57 |

GPT (R57) confirmed each boundary against the stated implementation and signed off Phase 11.2. The
package-path `internal/external` is the External Interface; the CLI `cmd/opscore-cli` binds only to
`external/v1` through `external.Server` (its own AST guard forbids bypassing the contract). **Phase 11
is CLOSED (R57).**

*Phase 11.1 External Interface Architecture — one stable, versioned, read-only Public Contract over
`platformview` + `correlation`; owns DTOs only, executes nothing, exposes capability without becoming
one (Accepted, Round 56; implemented, Round 57).*

---

## 10. Phase 11 completion record (Round 57)

R57 (2026-08-07) signed off the implementation: `internal/external` + `cmd/opscore-cli` **PASS**; all
MUST-0..6 and SHOULD-1..3 verified against the code; CLI confirmed as an `external/v1` *consumer
example*, not a second control plane. **Phase 11 External Interface CLOSED.**

Resulting six-tier architecture (top tier is a read-only consumer boundary):

```
Runtime Core → Plugin Ecosystem → Platform Operations
            → Platform Integration → Event Correlation → External Interface (read-only contract)
```

Next-step guidance from GPT (R57), per ADR-021 evolution charter: prefer **Phase 12 — Deployment &
Distribution Architecture** (single/multi-node topology, upgrade/migration, backup/restore, config
distribution, secret boundary) before extending the API further. Any new capability must re-enter via
Problem → Scope ADR → Architecture ADR → Implementation; no direct coding.
