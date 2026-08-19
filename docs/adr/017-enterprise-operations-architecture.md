# ADR-017 — Phase 8.3: Enterprise Operations Architecture

- **Status**: Accepted (signed Round 42)
- **Date**: 2026-08-03
- **Theme**: Phase 8 — Platform Operations (ADR-014)
- **Companion to**: ADR-010 (Runtime Contract — frozen), ADR-011 (Trust — frozen), ADR-012 (Core Stability — frozen), ADR-013 (Plugin Ecosystem — frozen), ADR-014 (Platform Operations Scope — accepted), ADR-015 (Observability — accepted), ADR-016 (Cluster Coordination — accepted)
- **Author**: OpsCore Plugin Runtime Workstream

---

## 0. Abstract

ADR-014 (Phase 8.0) opened Platform Operations, and ADR-015/016 defined its first two capabilities
— Observability (read-only) and Cluster Coordination (topology, not a second Runtime) — each as a
*specification only* (Architecture First, ADR-014 MUST-5). This ADR (8.3) specifies the **third
capability — Enterprise Operations** — also spec-only. No implementation code is written until signed.

The framing (from GPT, Round 41), consistent with 8.1/8.2:

> **This is *Enterprise Operations* — an Enterprise Capability, not an *Enterprise Runtime*.** It is
> the policy / governance / organizational layer: approvals, change process, tenants, RBAC extension,
> maintenance windows. It constrains and orchestrates the frozen systems through their existing
> interfaces; it never executes a plugin itself and never becomes a fourth Runtime.

The headline invariant of 8.3:

> **Enterprise Operations is a Policy Layer, not an Execution Layer. It composes Runtime + Ecosystem +
> Cluster + Observability; it replicates none of them, and it executes nothing.**

---

## 1. Scope — what Enterprise Operations actually owns

| Concept | Belongs to | Meaning |
|---|---|---|
| **Approval / Change Process** | Enterprise Operations (this ADR) | Gating *whether* a weighed action is permitted under org policy |
| **Maintenance Window** | Enterprise Operations | Scheduling-policy scope (Cluster owns placement; Enterprise owns *when* windows open) |
| **Tenant / Organization / Business Unit** | Enterprise Operations | Org-scoped policy attachment boundaries |
| **RBAC Extension / Policy Attachment** | Enterprise Operations | Extra policy metadata attached to existing IDs |
| **Execution / Runtime / Plugin / Cluster topology** | Frozen systems (ADR-010/013/014/016) | Enterprise only *reads* their state and *attaches* policy; never owns them |

Enterprise Operations adds **policy and organizational metadata**; it does not add execution behavior.
Where a decision touches execution, it delegates to the Runtime (or constrains via policy the Runtime
already honors).

---

## 2. Freeze Boundaries (MUST-1..5, from GPT Round 41)

- **MUST-1 — Enterprise does not execute commands.** Like 8.1/8.2, every execution still enters the
  Runtime through its existing execution interface. Enterprise may *call* that interface; it never
  opens its own execution path (no SSH/shell/Command of its own).
- **MUST-2 — Enterprise does not own Cluster.** Cluster Coordination (ADR-016) stays the owner of
  Membership / Group / Label / Placement. Enterprise may *attach* tenant/approval policy to a cluster
  or group, but it does not re-record membership or placement.
- **MUST-3 — Enterprise owns policy metadata, not execution.** Its responsibility set is
  `Approval` / `Maintenance Window` / `Change Process` / `Tenant` / `Organization` / `RBAC Extension`
  / `Policy Attachment`. None of these is an execution protocol; Enterprise never describes *how a
  plugin runs*.
- **MUST-4 — Enterprise may constrain Execution, cannot implement/replace Execution.** Enterprise can
  *gate* a run (policy verdict: allow/deny under tenant + approval + window), but the run itself is
  always the Runtime's. Constraining ≠ implementing.
- **MUST-5 — Enterprise is a Policy Layer, not an Execution Layer; it composes, never replicates.**
  Enterprise is built *on top of* Runtime + Ecosystem + Cluster + Observability via their existing
  interfaces and event outputs. It must not re-implement `Manager` / `Loader` / `Provider` /
  `Manifest` / `isolation` (Runtime), the plugin lifecycle (Ecosystem), membership/placement (Cluster),
  or the observation model (Observability). Duplicating any of the four frozen/accepted capabilities
  inside Enterprise is the cardinal sin of 8.3.

Two **SHOULD** refinements (from GPT, Round 42) — non-blocking, but written in to prevent future
boundary drift:

- **SHOULD-1 — Policy Evaluation Ownership.** Enterprise owns *policy attachment*, but does **not**
  own *policy evaluation semantics*. Concretely: **Enterprise attaches, Governance evaluates.** If
  Approval / Maintenance / RBAC / Rolling Policies were all absorbed into Enterprise, Governance
  (ADR-018) would lose its reason to exist. This boundary is frozen early so the 8.3/8.4 split stays
  clean: Enterprise = *who/what this policy applies to* (org scope + attachment); Governance = *what
  the verdict is* (evaluation + conflict resolution).
- **SHOULD-2 — Verdict is Declarative (State, not Action).** Enterprise outputs *states* —
  `Allow` / `Deny` / `RequireApproval` / `MaintenanceBlocked` — never *actions* — `Run this` /
  `Retry` / `Rollback` / `Kill` / `Schedule`. Enterprise declares state; the Runtime performs actions.
  This makes Enterprise's output structurally incapable of colliding with Runtime's execution role.

---

## 3. Architecture — policy over the composed platform

```
Runtime Core (frozen)          Plugin Ecosystem (frozen)       Cluster (ADR-016, accepted)
   │ emits events                 │ emits events                  │ membership / placement
   │                               │                               │
   └──────────────┬────────────────┴──────────────┬───────────────┘
                  ▼                                 ▼
        Observability (ADR-015, accepted)    Enterprise Operations (this ADR)
           │ reads signals                     │ Approval / Tenant / RBAC / Window / Change
           │                                   │ attaches policy to existing IDs (PluginID,
           │                                   │   RuntimeID, Group, ExecutionID)
           ▼                                   ▼
        Read Model  ◄──── policy verdict (allow/deny) ────►  Runtime execution interface
        (dashboards)                                  (Enterprise constrains; Runtime executes)
```

Key properties:

- **Policy attached to existing IDs.** A tenant/approval/role is *bound to* `PluginID` / `RuntimeID`
  / `Group` / `ExecutionID` — reusing the IDs the frozen systems already have (ADR-015 MUST-4). No
  new identity system.
- **Composes the four, replicates none.** Enterprise may *consult* Observability signals and *scope
  by* Cluster membership, but it holds no copy of their state. An AST guard forbids Enterprise from
  importing `internal/plugin/runtime` / `internal/observability` / `internal/cluster` internals in a
  way that lets it re-implement their behavior.
- **Gating, not executing.** The policy verdict is a *decision* the Runtime (or its caller) consults;
  Enterprise never performs the run. This matches the Read-Model discipline of 8.1 and the
  Coordinate-don't-execute discipline of 8.2.

---

## 4. Out of Scope — explicitly forbidden for 8.3

- An *Enterprise Runtime* / fourth execution engine that runs plugins itself (MUST-1, MUST-5 —
  re-enters the Runtime Boundary, needs a new ADR).
- Owning or re-recording Cluster membership / placement (MUST-2 — belongs to ADR-016).
- New execution protocol / new execution handshake / describing *how* a plugin runs (MUST-3).
- Implementing execution inside the policy layer; Enterprise only *constrains* (MUST-4).
- Modifying Runtime Contract / `Manifest` / `Provider` / `Loader` / `Manager` / `core.Context`
  (ADR-014 MUST-4 — needs a new ADR).
- New identity system for tenant/org/member — reuse existing IDs (MUST-1/4, ADR-015).
- Re-implementing Observability (signals) or Cluster (topology) inside Enterprise (MUST-5).
- Redefining the Trust Boundary (ADR-011 owns trust; Enterprise may *scope* by trust level, never
  change a verdict).

---

## 5. Relationship to the frozen system & Phase 8 siblings

ADR-017 is the third theme (Enterprise Operations) under the Phase 8 umbrella (ADR-014). Its siblings
are 8.1 Observability (accepted), 8.2 Cluster Coordination (accepted), 8.4 Governance — each its own
architecture ADR, each obeying ADR-014's freeze boundaries.

- **vs 8.1 Observability (ADR-015):** Enterprise *reads* the observation model to make policy
  decisions; it never rebuilds monitoring.
- **vs 8.2 Cluster Coordination (ADR-016):** Enterprise *attaches* tenant/approval policy to groups
  and members; it never re-owns membership/placement.
- **vs 8.4 Governance:** Governance is the *rule engine / policy evaluation* substrate; Enterprise
  Operations is the *org-capability* that carries approval/tenant/RBAC/change-process. 8.4 will
  define how policy is authored, stored, and evaluated; 8.3 defines the org scope those policies
  attach to. They are complementary, not overlapping.

---

## 6. Phase 8 Route (authorization chain)

| Sub-phase | Deliverable | Status |
|---|---|---|
| 8.0 | Platform Operations Scope (ADR-014) | Accepted (R39) |
| 8.1 | Observability Architecture (ADR-015) | Accepted (R40) |
| 8.2 | Cluster Coordination Architecture (ADR-016) | Accepted (R41) + Implemented (R45) |
| 8.3 | Enterprise Operations Architecture (this ADR-017) | Accepted (R42) + **Implemented (R46 PASS, commit `7f99e42`)** |
| 8.4 | Governance / Policy Architecture (ADR-018) | Accepted (R43) + **Implementation in progress (R47→)** |

Per GPT (Round 41): continue Architecture First — complete 8.3 and 8.4 capability ADRs before any
Phase 8 implementation code. After 8.3 is accepted, the next is 8.4 (Governance) architecture, unless
the operator chooses to begin 8.3 implementation.

---

## 7. Core Stability Statement (re-affirmed)

> **Core Stability Statement** (unchanged)
>
> Runtime Core is considered architecturally stable. Future features should preferentially be
> implemented as extensions built on top of the frozen Runtime Contract. Any change that modifies
> Runtime Contract semantics, lifecycle, or public invariants requires a new ADR.

8.3 re-affirms this from the enterprise angle: Enterprise Operations is a *policy/organizational
extension that composes the frozen systems and constrains execution via policy* — it adds no Contract
change and no execution path.

---

## 8. Sign-off (Round 42)

- **Architecture verdict**: ✅ PASS (architecture layer)
- **MUST-1** Enterprise does not execute commands — ✅ PASS
- **MUST-2** Enterprise does not own Cluster — ✅ PASS
- **MUST-3** Enterprise owns only policy metadata — ✅ PASS
- **MUST-4** Enterprise may constrain, cannot implement Execution — ✅ PASS
- **MUST-5** Enterprise is Policy Layer, replicates none of the four systems — ✅ PASS
- **Applied SHOULD-1** Policy Evaluation Ownership (Enterprise attaches, Governance evaluates)
- **Applied SHOULD-2** Verdict is Declarative (State not Action)
- **Next step**: ✅ (A) sign ADR-017 + continue ADR-018 Governance/Policy Architecture (spec)

*Phase 8.3 Enterprise Operations Architecture — Enterprise Capability (policy layer), not an Enterprise Runtime (Accepted, Rounds 41→42).*

---

## 9. Implementation Sign-off (Round 46 — `internal/enterprise`)

**Verdict: ✅ PASS** (implementation layer). `internal/enterprise` committed in `7f99e42`
(quality gate PASSED, 42 packages green). All five MUSTs verified by construction:

- **MUST-1** No execution: `Service` exposes `Attach/Detach/AttachmentsFor/All` only; AST-guarded
  `TestNoExecMethod` bans `Run/Exec/Invoke/Execute/Command/Evaluate`. Attach produces a
  `PolicyAttachment` (state), never an action. ✅
- **MUST-2** No ownership of Host/Cluster/Plugin/Runtime: `TargetRef` is an opaque string; imports of
  `hostregistry`/`cluster`/`runtime`/`isolation`/`observability`/`governance` are AST-forbidden. ✅
- **MUST-3** Policy metadata only: `PolicyAttachment.Metadata` is free-form org detail (window,
  approver, tenant), never an execution protocol. ✅
- **MUST-4** Constrains, not implements: attach yields state; verdict production belongs to Governance
  (no `Evaluate` method). ✅
- **MUST-5** Composition, not replication: AST guard forbids the six frozen/accepted systems. ✅

**Architecture confirmations from GPT (R46):**
- `Enterprise → PolicyAttachment → TargetRef` (opaque ref) is the correct direction; Enterprise owns
  *who/what a policy applies to*, not Host/Runtime/Plugin/Execution internals.
- Enterprise / Governance split is the most important 8.3 boundary: Enterprise has no `Evaluate()`,
  which prevents the Policy Layer from silently inflating into a Control Plane.
- AST guard (Composition over modification) aligns with referencing abstract IDs, not capability impls.
- `Metadata` must stay descriptive (window/approval), never an execution instruction (command/retry).

**Round 46 SHOULD recommendations (non-blocking — do not block 8.3):**
- **SHOULD-A (Attachment Schema Version):** add `const AttachmentSchemaVersion = "enterprise/attachment/v1"`
  for long-lived policy-metadata evolution. *Not yet applied — tracked for a future round.*
- **SHOULD-B (Attachment Immutable History):** model `PolicyAttachmentEvent` (ATTACHED/UPDATED/DETACHED)
  for audit / Governance-Explain / compliance. *Not yet applied — tracked for a future round.*

Both SHOULDs are recorded but intentionally deferred: 8.3 closes PASS on the MUST contract; the
SHOULDs are forward-looking metadata-evolution hooks, not required for the Implemented sign-off.

*Phase 8.3 internal/enterprise — Implemented & PASS (Round 46, commit `7f99e42`).*
