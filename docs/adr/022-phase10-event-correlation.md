# ADR-022 — Phase 10 Event Correlation Architecture

- **Status**: Accepted — signed off at Round 53; Phase 10.2 implementation complete at Round 54 (internal/correlation, gate green), implementation sign-off requested
- **Date**: 2026-08-07
- **Supersedes / Extends**: ADR-019 (Phase 9 scope), ADR-020 (Platform Integration), ADR-021 (Baseline / evolution charter)
- **Phase**: 10.0 (scope review PASS, R52) → 10.1 (this ADR, spec only, R53) → 10.2 (implementation, R54)
- **Author**: OpsCore relay (Senior Developer + ChatGPT co-design)

## 1. Context & positioning

Round 51 declared **OpsCore v1 Architecture Baseline Frozen** (ADR-021). The evolution charter
(ADR-021 §6) requires a **Major Evolution ADR** for any change touching *Cross-capability
interaction* — Phase 10 is exactly that: it correlates the four closed Phase 8 capabilities.

Round 52 scope review concluded Phase 10 is a **Composition Extension, not an Architecture
Rewrite**: it adds no execution path, modifies no frozen contract, and owns nothing. It composes
the existing read-only event sources into cross-capability *Correlation Views*.

This ADR is the **final spec ADR** (spec only). Per the R52 verdict, implementation is deferred to
Round 54; the Architecture-First sequence is preserved:

```
R52  Phase 10 scope review      → PASS
R53  ADR-022 final spec ADR     → (this document, sign-off requested)
R54  internal/correlation impl  → after sign-off
```

## 2. Correlation Ownership Matrix

| Capability | Owns | Correlation owns |
|---|---|---|
| Observability | Observation (events) | nothing |
| Cluster | Placement / Membership | nothing |
| Enterprise | PolicyAttachment | nothing |
| Governance | Verdict / Rule | nothing |
| **Correlation** | **nothing** | **nothing** — it is a pure projection |

Correlation has no ownership. It reads from the four capabilities via injected *Reader* interfaces
(Dependency Inversion) and returns composed, versioned value objects.

## 3. Event Source Contract Table (read-only, frozen)

Correlation consumes these **frozen read-only entities** (composition only; never mutated):

| Source | Owner | Key IDs it exposes | View it yields |
|---|---|---|---|
| `Observation` (`internal/observability`) | Observability | `ObsID`, `ExecutionID`, `RequestID`, `PluginID`, `AuditID`, `TraceID` | `ObservationView` |
| `Placement` / `Member` (`internal/cluster`) | Cluster | `HostRef`, `Version`, `Targets`, `Reason`, `Groups`, `Labels` | `PlacementView` |
| `PolicyAttachment` (`internal/enterprise`) | Enterprise | `AttachID`, `TargetKind`, `TargetRef`, `Kind` | `AttachmentView` |
| `Verdict` / `Rule` (`internal/governance`) | Governance | `PolicyID`, `RuleID`, `MatchedPolicy`, `MatchedRule`, `Code`, `Evidence` | `VerdictView` / `RuleView` |
| Frozen core events | Runtime Core | `execution.ExecutionEvent`, `sandbox.Decision`, `manifest.SignatureResult`, `core.AuditEvent` | referenced by ID only |

Correlation reuses these IDs; it **mints no new global identity** (see MUST-2).

## 4. Correlation View Model (projection, not entity)

A `CorrelationView` is a **projection**, never a domain entity. It composes references to the
owning capabilities plus a single `Meta` (explainability). No `CorrelatedEntity` /
`OperationalIncident` / `UnifiedOperation` type is defined.

```go
// ViewVersion freezes the read contract (SHOULD-2).
const ViewVersion = "correlation/view/v1"   // distinct from platform/view/v1

type Meta struct {
    SourceRefs  []string  // IDs of the events joined
    Reason      string    // why these events are correlated
    CorrelatedAt time.Time // captured at query time (SHOULD-3, fresh)
}

// Scope declares the correlation boundary (SHOULD-5). No "give me everything" queries.
type Scope struct {
    Kind  string // "execution" | "plugin" | "host" | "policy"
    Ref   string // the concrete id within that kind
}

type CorrelationView struct {
    Scope        Scope          // explicit boundary (SHOULD-5)
    ExecutionRef string         // reuse, never invent
    ObservationRefs []string    // ObsID list
    VerdictRefs     []string    // PolicyID·RuleID list
    PlacementRefs  []string    // HostRef list
    PolicyRefs     []string    // AttachID list
    Meta           Meta
}
```

Each query returns a `CorrelationView` value object. Composition of references only — no new
entity, no stored history.

## 5. Scope rules (SHOULD-5 — Correlation Scope Explicitness)

Every correlation query **must declare its scope**. Unbounded correlation is forbidden (it would
silently become a global knowledge graph).

| Scope kind | `Scope.Kind` | Bound by |
|---|---|---|
| Execution scope | `execution` | `ExecutionID` |
| Plugin scope | `plugin` | `PluginID` |
| Host scope | `host` | `HostRef` |
| Policy scope | `policy` | `PolicyID` |

Forbidden: "correlate everything", "global incident graph", unbounded fan-out joins.

## 6. Frozen boundaries (MUST-0…5)

- **MUST-0** — Inherits Phase 9 freeze: **no new execution path**, **no Control Plane**, no
  modification of Runtime Contract, the four Phase 8 capabilities, or `internal/platformview`.
- **MUST-1** — **Read-only correlation**: a Reader → `CorrelationView` pipeline. Correlation holds
  no data; it never becomes an event store (`Source → Reader → View`, never `Source → DB → owns
  history`).
- **MUST-2** — **No new identity**: only reuses existing IDs (`ExecutionID`, `RequestID`,
  `PluginID`, `AuditID`, `ObsID`, `AttachID`, `PolicyID`, `RuleID`, `HostRef`). No `CorrelationID`
  / `GlobalIncidentID` / `UnifiedEventID`.
- **MUST-3** — **No command surface**: only correlation *query / view*. No `ResolveIncident()`,
  `ReplayExecution()`, `ApplyRemediation()` — these would slide toward Control Plane.
- **MUST-4** — **Ownership preserved**: correlation owns nothing; it projects references (Composition
  over Modification).
- **MUST-5** — **Correlation ≠ new model**: a `CorrelationView` is a projection of references +
  read-only copies, never a new domain entity (`CorrelatedExecution`, `OperationalIncident`,
  `UnifiedOperation` are forbidden).

## 7. Package structure (proposed — not implemented this round)

A single peripheral package, consistent with the four Phase 8 packages and `internal/platformview`:

```
internal/correlation/
    model.go          # CorrelationView value objects (projection, read-only) + Scope + Meta
    facade.go         # Correlator: Correlate(ctx, scope) → CorrelationView (Reader interfaces)
    errors.go         # input/scope errors only (no execution errors)
    correlation_test.go # AST guard + TestNoExecMethod + determinism + scope + explainability
```

> **Naming note (R50/R52 lesson):** `internal/correlation` is confirmed free of collision with
> existing Runtime/Platform packages. Package naming must avoid collision with frozen packages.

## 8. Constraints table

| ID | Constraint | Notes |
|---|---|---|
| MUST-0 | No new execution path / no Control Plane / frozen boundaries intact | inherits Phase 9 |
| MUST-1 | Read-only correlation; owns no data | `Source → Reader → View` |
| MUST-2 | Reuse existing IDs; mint no new identity | no CorrelationID etc. |
| MUST-3 | No command surface | query/view only |
| MUST-4 | Ownership preserved (projection only) | owns nothing |
| MUST-5 | Correlation ≠ new model | projection, not entity |
| SHOULD-1 | Explainable correlation | every view carries `Meta{SourceRefs, Reason, CorrelatedAt}` |
| SHOULD-2 | Stable read contract | `correlation/view/v1` (distinct from `platform/view/v1`) |
| SHOULD-3 | Lazy aggregation | no cache / DB / background worker / event-queue ownership |
| **SHOULD-4** | **Correlation Determinism** | same `{events + state}` set → identical `CorrelationView`; sort inputs (e.g. `sort.SliceStable`) to avoid map-iteration / time-order drift |
| **SHOULD-5** | **Correlation Scope Explicitness** | every query declares `Scope{Kind, Ref}`; unbounded correlation forbidden |

## 9. AST / Test enforcement plan

Mirrors the established peripheral-package discipline (`observability` / `cluster` / `enterprise` /
`governance` / `platformview`):

- **AST guard** (`correlation_test.go`): forbid importing frozen systems —
  `internal/plugin/runtime`, `internal/plugin/isolation`, `internal/controlplane/hostregistry`,
  and cross-package replication of any capability.
- **`TestNoExecMethod`**: forbid `Run` / `Exec` / `Invoke` / `Apply` / `Execute` / `Command` /
  `Emit` / `Dispatch` / `Rollback` / `Kill` / `Schedule`.
- **Determinism test** (SHOULD-4): run `Correlate` 30× over the same inputs; assert identical
  `CorrelationView` (field-by-field; `map` fields compared explicitly).
- **Scope test** (SHOULD-5): assert every public method requires a `Scope`; an empty/unknown
  `Scope.Kind` returns `ErrInvalidScope`.
- **Explainability test** (SHOULD-1): assert `Meta.SourceRefs` is non-empty and `Reason` is set.
- **Lazy test** (SHOULD-3): assert `Meta.CorrelatedAt` is refreshed on every call (no cached state).

## 10. Out-of-scope (explicit "Correlation owns nothing")

- Correlation **does not own Event** — it reads frozen events by ID.
- Correlation **does not own History** — no stored timeline; recomputed on each query.
- Correlation **does not own Identity** — reuses existing IDs; mints none.
- Correlation **does not own Action** — no remediation / replay / resolve commands.
- No new Capability, no new Execution Path, no Control Plane, no Runtime Contract change.

## 11. Authorization chain

| Step | Deliverable | Status |
|---|---|---|
| 10.0 | Phase 10 scope review (prompt in R52) | Scope Review PASS (R52) |
| **10.1** | **Event Correlation Architecture (this ADR-022)** | **PASS (Round 53)** |
| 10.2 | Event Correlation Implementation (`internal/correlation`) | **Implemented (Round 54)** — gofmt/build/vet/test all green |

## 12. Sign-off placeholder (Round 53)

| Item | Verdict | Round |
|---|---|---|
| MUST-0 No new execution path / no Control Plane / frozen boundaries | — | 53 |
| MUST-1 Read-only correlation (owns no data) | — | 53 |
| MUST-2 No new identity (reuse existing IDs) | — | 53 |
| MUST-3 No command surface (query/view only) | — | 53 |
| MUST-4 Ownership preserved (projection only) | — | 53 |
| MUST-5 Correlation ≠ new model | — | 53 |
| SHOULD-1 Explainable correlation | — | 53 |
| SHOULD-2 Stable read contract `correlation/view/v1` | — | 53 |
| SHOULD-3 Lazy aggregation (no cache/DB/worker) | — | 53 |
| SHOULD-4 Correlation Determinism | — | 53 |
| SHOULD-5 Correlation Scope Explicitness | — | 53 |

*Phase 10 Event Correlation — a pure Composition Extension over the four closed Phase 8
capabilities and `internal/platformview`; owns nothing, executes nothing, correlates only.*

## 13. Implementation summary (Round 54 — `internal/correlation`)

Implemented exactly to the spec above, mirroring the established peripheral-package discipline
(`observability` / `cluster` / `enterprise` / `governance` / `platformview`):

- **`model.go`** — `ViewVersion = "correlation/view/v1"` (SHOULD-2, distinct from
  `platform/view/v1`); `Meta{SourceRefs, Reason, CorrelatedAt}` (SHOULD-1/3); `Scope{Kind, Ref}`
  with kind constants `execution|plugin|host|policy` (SHOULD-5); `CorrelationView` projection of
  reference lists only (MUST-2/4/5). No `CorrelationID` / `GlobalIncidentID` / `UnifiedEventID`.
- **`facade.go`** — `Correlator` with a single public method `Correlate(ctx, scope)` (MUST-3: no
  command surface). Dependency-inverted `Reader` interfaces (`ObservabilityReader` /
  `ClusterReader` / `EnterpriseReader` / `GovernanceReader`) take a `Scope` and return existing
  reference IDs — the facade imports **no frozen system** (MUST-0/1). Every input slice is
  `sort.Strings` before projection and `SourceRefs` is sorted, guaranteeing determinism (SHOULD-4).
  No cached state; `CorrelatedAt` is refetched every call (SHOULD-3 lazy). `validateScope` rejects
  empty/unknown/empty-ref scopes (SHOULD-5 — no unbounded joins).
- **`errors.go`** — `ErrInvalidScope` only (input/scope error, no execution error; MUST-2/3).
- **`correlation_test.go`** — `TestASTGuardNoFrozenImports` (forbids `internal/plugin/runtime`,
  `internal/plugin/isolation`, `internal/controlplane/hostregistry`); `TestNoExecMethod` forbids
  `Run/Exec/Invoke/Apply/Execute/Command/Emit/Dispatch/Rollback/Kill/Schedule` **plus
  `Resolve/Replay/Remediate`** (R53 sign-off additions); determinism run ×30 (SHOULD-4); scope
  rejection of unbounded `everything`/`global` queries (SHOULD-5); explainability (SHOULD-1); lazy
  `CorrelatedAt` refresh (SHOULD-3); stable `ViewVersion` (SHOULD-2).

Gate (Round 54): `gofmt -w` clean · `go build ./...` green · `go vet ./...` green · `go test
./internal/correlation/...` green · full-module regression `go build ./... && go vet ./...` green.

Phase 10 status after Round 54: 10.0 Scope PASS · 10.1 ADR-022 PASS · 10.2 Impl green. Per R54
implementation sign-off (GPT verdict `(A) 实现签字 PASS`), **Phase 10 Event Correlation is CLOSED**
— Architecture ✅ · Implementation ✅ · Validation ✅. ADR-022 Status: Accepted / Completed. The
five-tier system is now end-to-end closed and frozen:
`Runtime Core → Plugin Ecosystem → Platform Operations → Platform Integration → Event Correlation`.
Next Major Evolution proceeds via a new Scope ADR (ADR-023, Phase 11).
