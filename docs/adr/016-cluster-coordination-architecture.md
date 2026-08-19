# ADR-016 — Phase 8.2: Cluster Coordination Architecture

- **Status**: Accepted (signed Round 41)
- **Date**: 2026-08-03
- **Theme**: Phase 8 — Platform Operations (ADR-014)
- **Companion to**: ADR-010 (Runtime Contract — frozen), ADR-011 (Trust — frozen), ADR-012 (Core Stability — frozen), ADR-013 (Plugin Ecosystem — frozen), ADR-014 (Platform Operations Scope — accepted), ADR-015 (Observability — accepted)
- **Author**: OpsCore Plugin Runtime Workstream

---

## 0. Abstract

ADR-014 (Phase 8.0) opened Platform Operations as the **third architecture axis**, and ADR-015
(8.1) defined its first capability — Observability — as a read-only extension. This ADR (8.2)
specifies the **second capability — Cluster Coordination** — also as a *specification only*
(Architecture First, ADR-014 MUST-5). No implementation code is written until signed off.

The single most important framing (from GPT, Round 40):

> **This is *Cluster Coordination*, not *Cluster Runtime*, and not *Distributed Runtime*.** It is
> *Platform Coordination* — an organizational layer that arranges existing Runtimes into groups,
> memberships, and scheduling metadata. It coordinates execution; it never implements execution,
> never owns Hosts, and never copies the Runtime.

The headline invariant of 8.2:

> **Cluster Coordination is a topology + scheduling-metadata layer that composes the frozen Runtime
> through its existing entry points. It adds no execution protocol, owns no Host, and never becomes
> a second Runtime.**

---

## 1. Scope — what Cluster Coordination actually is

| Concept | Belongs to | Meaning |
|---|---|---|
| **Membership** | Cluster Coordination (this ADR) | Which Runtime instances belong to which group |
| **Group / Label** | Cluster Coordination | Organizational tags for placement and policy scoping |
| **Scheduling Metadata** | Cluster Coordination | Affinity, anti-affinity, weight, zone — *hints*, not commands |
| **Host Ownership / Inventory** | Runtime Inventory (frozen) | The actual machine/process record stays with the Runtime; Cluster only *references* it by ID |
| **Execution** | Runtime Core (frozen) | Actually running a plugin enters the Runtime via `isolation.AddFromPackage`, never via Cluster |

Cluster Coordination describes *relationships and placement intent*; it does not describe *how a
plugin executes*. Execution always funnels through the frozen Runtime Contract (ADR-010/012).

---

## 2. Freeze Boundaries (MUST-1..5, from GPT Round 40)

- **MUST-1 — Cluster does not execute commands directly.** Every execution still enters the Runtime
  through its existing entry point (`isolation.AddFromPackage`). Cluster may *request* a placement,
  but it never opens an SSH/shell/Command path of its own to run a plugin. If it did, it would become
  a second Runtime — forbidden.
- **MUST-2 — Cluster does not own Host.** Host Ownership remains with the **Runtime Inventory** (the
  frozen system that already tracks machines/processes). Cluster is an *organizational relationship*
  over those Host IDs — it references them, it does not re-record or re-author them.
- **MUST-3 — Cluster describes only Membership / Group / Label / Scheduling Metadata.** It models
  topology and placement *hints*. It must **not** describe an execution protocol (no new wire format
  for "run this"), no new lifecycle stage, no new execution handshake.
- **MUST-4 — Cluster may *coordinate* Execution, cannot *implement* Execution.** Cluster can decide
  *where* a plugin should run (pick a member Runtime per scheduling metadata) and *whether* a request
  is admissible under group policy; the actual run is always delegated to the chosen Runtime.
- **MUST-5 — Cluster must compose Runtime, cannot copy Runtime.** The coordination layer is built *on
  top of* the existing Runtime via its public entry points and event outputs (ADR-015). It must not
  re-implement `Manager` / `Loader` / `Provider` / `Manifest` / `isolation`. Duplicating the Runtime
  inside the cluster layer is the cardinal sin of 8.2.
- **SHOULD-1 — Publish an Ownership Matrix (no ownership drift).** State explicitly who owns each
  object so the boundary cannot silently shift over years:
  | Object | Owner | Cluster may modify? |
  |---|---|---|
  | Host | Runtime Inventory (frozen) | **No** |
  | Execution | Runtime Core (frozen) | **No** |
  | Plugin | Plugin Ecosystem (frozen, ADR-013) | **No** |
  | Membership | Cluster Coordination (this ADR) | **Yes** |
  | Group / Label | Cluster Coordination | **Yes** |
  | Placement / Scheduling Metadata | Cluster Coordination | **Yes** |
  Cluster references the first three by ID; it never re-records or re-authors them.
- **SHOULD-2 — Scheduling produces placement, not execution; Membership owns no Host lifecycle.**
  - *Scheduling Metadata vs Scheduling Policy*: Cluster may own scheduling **metadata** (`Priority`,
    `Affinity`, `Group`, `Role`). Scheduling **policy** (`Rolling Strategy`, `Canary`, `Blue-Green`,
    `Maintenance Window`) belongs to higher-level orchestration (8.3 Enterprise Operations) — kept
    out so Cluster never drifts into an Orchestrator. Principle: **Scheduling produces placement
    decisions, not execution.**
  - *Membership vs Host lifecycle*: `Join` / `Leave` / `Member State` belong to Cluster; the Host
    lifecycle (`Provision`, `Register`, `Delete`, `Discovery`) stays with Runtime Inventory. Cluster
    is a membership/organization layer, **not** a Host Registry.

---

## 3. Architecture — coordination over the frozen Runtime

```
Runtime Inventory (frozen)          Runtime Core A (frozen)      Runtime Core B (frozen)
   │ owns Host IDs                     │ emits events               │ emits events
   │                                    │                             │
   └──────────────┬─────────────────────┴──────────────┬──────────────┘
                  ▼                                      ▼
        Cluster Coordination (this ADR)         Event Bus (ADR-015 observability)
           │ Membership / Group / Label                │ reads topology + health
           │ Scheduling Metadata (hints)               │
           │ Placement decision (which Runtime)        │
           ▼                                            │
        Delegate → Runtime Core A ── AddFromPackage ───┘   (execution enters the frozen Runtime)
                  (NEVER an SSH/Command path of Cluster's own)
```

Key properties:

- **Reference, don't own.** Cluster holds `HostID` / `RuntimeID` references into the Inventory; it
  never stores Host facts itself. A guard forbids Cluster from defining its own Host record type.
- **Compose via public entry.** Placement resolves to a Runtime; the run goes through
  `isolation.AddFromPackage` like any other caller. No private/parallel execution channel.
- **Topology is observable (ADR-015).** Membership, health, and placement decisions are *events* the
  observability layer already consumes — Cluster reuses that Read Model rather than building a second
  monitoring stack.
- **AST guards (consistent with 7.x / 8.1).** Cluster may import the Runtime's *event/ID types* and
  its *entry-point signature*, but an AST guard forbids it from importing `internal/plugin/runtime`
  internals in a way that lets it call execution outside the public `AddFromPackage` seam.

---

## 4. Out of Scope — explicitly forbidden for 8.2

- A second Runtime / Distributed Runtime / Cluster Runtime that executes plugins itself (MUST-1,
  MUST-5 — this would re-enter the Runtime Boundary and require a new ADR).
- Owning or re-recording Host facts; Host Ownership stays with Runtime Inventory (MUST-2).
- New execution protocol / new wire format for "run this" / new lifecycle stage (MUST-3).
- Implementing execution inside the coordination layer; Cluster only *coordinates* (MUST-4).
- Modifying Runtime Contract / `Manifest` / `Provider` / `Loader` / `Manager` / `core.Context`
  (ADR-014 MUST-4 — needs a new ADR).
- New identity system for Host/Node/Member — reuse `HostID` / `RuntimeID` / existing IDs (consistent
  with ADR-015 MUST-4).
- Implementing the scheduler / placement engine / membership gossip — this ADR is scope; code
  follows sign-off (Architecture First).
- Redefining the Trust Boundary (ADR-011 owns trust; Cluster may *route* by trust level, never
  change a verdict).

---

## 5. Relationship to the frozen system & Phase 8 siblings

ADR-016 is the second theme (Cluster Coordination) under the Phase 8 umbrella (ADR-014). Its
siblings are 8.1 Observability (accepted), 8.3 Enterprise Operations, 8.4 Governance — each its own
architecture ADR, each obeying ADR-014's freeze boundaries.

- **vs 8.1 Observability (ADR-015):** Cluster emits topology/health *events*; Observability consumes
  them. Cluster never builds its own monitoring — it composes the Read Model.
- **vs 8.3 Enterprise Operations / 8.4 Governance:** Those will *read* Cluster's membership and
  placement metadata to apply org policy and compliance scope. Cluster is the shared topology the
  later two build on, exactly as Observability is the shared signal base.

---

## 6. Phase 8 Route (authorization chain)

| Sub-phase | Deliverable | Status |
|---|---|---|
| 8.0 | Platform Operations Scope (ADR-014) | Accepted (R39) |
| 8.1 | Observability Architecture (ADR-015) | Accepted (R40) |
| 8.2 | Cluster Coordination Architecture (this ADR-016) | Accepted (R41) + **Implemented** (R45) |
| 8.3 | Enterprise Operations Architecture | Implementation in progress (R45→) |
| 8.4 | Governance / Policy Architecture | proposed |

Per GPT (Round 40): continue Architecture First — complete the 8.1–8.4 capability ADRs before any
Phase 8 implementation code. After 8.2 is accepted, the next is 8.3 (Enterprise Operations)
architecture, unless the operator chooses to begin 8.2 implementation.

---

## 7. Core Stability Statement (re-affirmed)

> **Core Stability Statement** (unchanged)
>
> Runtime Core is considered architecturally stable. Future features should preferentially be
> implemented as extensions built on top of the frozen Runtime Contract. Any change that modifies
> Runtime Contract semantics, lifecycle, or public invariants requires a new ADR.

8.2 re-affirms this from the coordination angle: Cluster Coordination is an *extension that arranges
existing Runtimes by reference and delegates execution to them* — it adds no Contract change and no
second execution path.

---

## 8. Sign-off (Round 41)

GPT review (Round 41) — **ADR-016 Architecture PASS** (by description; full source sign-off deferred
to implementation):

| Item | Verdict |
|---|---|
| MUST-1 Cluster does not execute directly (Runtime only) | ✅ PASS |
| MUST-2 Cluster does not own Host (Runtime Inventory owns) | ✅ PASS |
| MUST-3 Coordination metadata only (no execution protocol) | ✅ PASS |
| MUST-4 Coordinate, not implement Execution | ✅ PASS |
| MUST-5 Compose Runtime, don't duplicate (no second Runtime) | ✅ PASS |
| SHOULD-1 Ownership Matrix | ✅ applied (§2) |
| SHOULD-2 Scheduling = policy not executor; Membership owns no Host lifecycle | ✅ applied (§2) |
| Next phase | ✅ authorized Phase 8.3 — ADR-017 Enterprise Operations Architecture (spec-only) |

Direction chosen: **(A) Architecture First** — complete 8.3 and 8.4 capability ADRs before any
Phase 8 implementation code, so all four Platform Capabilities freeze their boundaries before code.

### 8.1 Round 45 — Implementation sign-off

GPT review (Round 45, by description of `internal/cluster`) — **Implementation PASS**:

| Item | Verdict |
|---|---|
| `internal/cluster` as Phase 8.2 implementation | ✅ PASS |
| MUST-1 no exec (Manager has no exec/SSH/command API; Placement only refs) | ✅ PASS |
| MUST-2 does not own Host (`HostRef` string; `hostregistry` import AST-forbidden) | ✅ PASS |
| MUST-3 metadata only (Member/Group/Label/State/Placement, no hardware/lifecycle) | ✅ PASS |
| MUST-4 Placement ≠ Execution (returns `[]HostRef`, not command) | ✅ PASS |
| MUST-5 compose, not replicate (AST guard on runtime/isolation/hostregistry) | ✅ PASS |
| ComputePlacement `[]HostRef` design | ✅ correct (host wires ref → Runtime existing interface → Execution) |
| Must-fix | none |
| SHOULD-1 Placement Explainability | ✅ applied (Round 45): `Placement.Reason` declarative note |
| SHOULD-2 Placement Version | ✅ applied (Round 45): `PlacementVersion "cluster/placement/v1"` |
| Next phase | ✅ authorized Phase 8.3 — `internal/enterprise` implementation (R46 sign-off) |

*Phase 8.2 Cluster Coordination Architecture — Platform Coordination over the frozen Runtime, not a second Runtime (Accepted R41; Implemented R45).*
