# ADR-014 — Phase 8.0: Platform Operations Architecture Scope

- **Status**: Accepted
- **Date**: 2026-08-03
- **Companion to**: ADR-010 (Plugin Runtime Contract — frozen), ADR-011 (Trust Boundary / Signature Policy — frozen), ADR-012 (Runtime Core Stability — frozen), ADR-013 (Plugin Ecosystem Architecture — frozen)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 8 — Platform Operations

---

## 0. Abstract

Phase 7 closed the **Plugin Ecosystem Architecture** (ADR-013). Phase 8 opens a *new theme*,
**Platform Operations** — the operational capabilities a deployed OpsCore cluster needs, which
are orthogonal to both the frozen Runtime Core and the frozen Plugin Ecosystem.

Phase 8 does **not** redesign plugins. It adds a third axis of capability — *how the platform
operates, observes, scales, and governs itself* — while leaving the two frozen systems (Runtime
Contract ADR-010/011/012, Ecosystem ADR-013) entirely untouched.

The headline invariant of Phase 8:

> **Platform Operations is a new capability axis built ON TOP of the two frozen systems.
> It adds Observability / Cluster / Enterprise-Ops / Governance surfaces; it never modifies the
> Runtime Contract, the Trust Boundary, or the Plugin Ecosystem boundaries.**

Per GPT (Round 38) the first deliverable of Phase 8 is **this Architecture Scope ADR** — spec
first, implementation later. Phase 8 obeys the same discipline Phase 7 proved: **Architecture
First (MUST-5)** — no code until the scope, boundaries, and principles are signed off.

---

## 1. Theme — one topic, not four

GPT (Round 38) ruled: do **not** mix Operations / Observability / Enterprise into a soup. Phase 8
has a **single theme — Platform Operations** — and is sub-divided into focused sub-phases:

| Sub-phase | Topic | Scope (proposed) |
|---|---|---|
| **8.1** | Observability Architecture | Metrics, Logs, Traces, Audit Correlation |
| **8.2** | Cluster Architecture | Control Plane, Multi-node, Scheduler, Agent Topology |
| **8.3** | Enterprise Operations | HA, Backup, Disaster Recovery, Upgrade, Rolling Restart |
| **8.4** | Governance | Policy, Quota, Multi-tenancy, Compliance |

Each sub-phase is itself a **specification** (an architecture ADR or section) before any code.
8.1 is the natural first sub-phase because observability is the foundation every later
operational capability (cluster health, DR signals, compliance audit) depends on.

---

## 2. Freeze Boundaries (the non-negotiable)

These MUST hold for the entire Phase 8 arc. They are the prerequisites for opening a new theme
without re-opening the previous ones.

- **MUST-1 — Runtime Contract remains frozen.** ADR-010 (Manifest / Provider / Loader /
  Manager / `core.Context`), ADR-011 (Trust / Signature), ADR-012 (Core Stability) are
  unchanged by Phase 8. No new lifecycle stage, Contract field, or `Manager` interface change.
- **MUST-2 — Plugin Ecosystem remains frozen.** ADR-013 (SDK / Packaging / Tooling / Registry /
  Compatibility / OCI / Dist) is unchanged by Phase 8. Phase 8 does **not** redesign the plugin
  system; it may *consume* ecosystem metadata (e.g. read `PackageRef` for an audit view) but
  never modify `ecosystem/*`.
- **MUST-3 — New Platform Capability is allowed.** Observability, Scheduling, Cluster, and
  Policy are *new* `opscore` capabilities that did not exist in Phases 3–7. They live in their
  own packages (e.g. `internal/observability`, `internal/cluster`, `internal/governance`) and
  depend on, but do not modify, the frozen systems.
- **MUST-4 — Execution-Contract changes require a new ADR.** **No Phase 8 work may modify the
  Runtime Contract implicitly.** Any Runtime Contract change requires a dedicated ADR and is
  outside normal Phase 8 work — it must **not** be smuggled in as a "normal" Phase 8 sub-phase.
  Platform Operations observes and operates; it does not alter how a plugin executes.
- **MUST-5 — Architecture First.** Complete this Scope ADR (8.0) and each sub-phase's
  architecture before writing implementation. Do not start coding in 8.1 until 8.1's scope is
  signed off.

---

## 3. Scope — what Phase 8 may do

Platform Operations adds operational surfaces over the stable base:

- **Observability (8.1)** — emit/collect Metrics, Logs, Traces; correlate them with the existing
  audit trail (Phase 5 / ADR-011 signature verdicts, Phase 6.1 `Decision.Code`, Phase 6.3
  isolation events). Read-only by default; it observes, it does not execute.
- **Cluster (8.2)** — optional multi-node topology: a control plane that coordinates agents,
  a scheduler that places work, an agent topology that reports health. Pure orchestration;
  the Runtime Core on each node is unchanged.
- **Enterprise Operations (8.3)** — HA, Backup, Disaster Recovery, Upgrade, Rolling Restart.
  Operational resilience of the *platform*, not of plugin execution semantics.
- **Governance (8.4)** — Policy enforcement, Quota, Multi-tenancy, Compliance reporting.
  Operates on platform metadata; never re-defines the Trust Boundary (ADR-011 owns trust).

All of the above are **Platform Capabilities** layered above the frozen systems, consistent with
the ADR-012 §4 "peripheral-package additions" allowance.

---

## 4. Out of Scope — explicitly forbidden for Phase 8

- Modifying Runtime Core / Contract / `Manager` / `Manifest` / `Provider` / Execution Contract
  (MUST-1 / MUST-4 — those need a new ADR, not a Phase 8 sub-phase).
- Redesigning the Plugin Ecosystem (SDK / Packaging / Registry / Compatibility / OCI / Dist)
  (MUST-2 — ADR-013 is frozen).
- Re-defining the Trust Boundary or Signature Policy (ADR-011 owns trust; Phase 8 may *report*
  on it, never change it).
- Turning Observability / Cluster / Governance into an execution path — Platform Operations
  observes and operates; it never becomes a new way to *run* a plugin.
- Any `.so` / WASM / Alternative Runtime Backend work — still deferred (Round 32 / 37).

---

## 5. Relationship to the frozen system

```
Runtime Core  (ADR-010/011/012)  ── frozen, never touched by Phase 8
        ▲
        │  composition / observation only
Plugin Ecosystem (ADR-013)       ── frozen, never redesigned by Phase 8
        ▲
        │  composition / observation only
Platform Operations (ADR-014)    ── NEW axis: Observability / Cluster / Ent-Ops / Governance
```

Phase 8 consumes the *outputs* of the frozen systems (audit records, isolation events,
`PackageRef` metadata, capability snapshots) to operate and observe the platform. It adds
capability; it does not take ownership of anything frozen.

---

## 6. Phase 8 Roadmap (authorization chain)

| Sub-phase | Deliverable | Status |
|---|---|---|
| 8.0 | Platform Operations Architecture Scope (this ADR) | **Implemented** (R39, ADR-014) |
| 8.1 | Observability Architecture (Metrics / Logs / Traces / Audit Correlation) | **Implemented** (R44, `9ad8dd2`→`aa72032`, ADR-015 + `internal/observability`) |
| 8.2 | Cluster Architecture (Control Plane / Multi-node / Scheduler / Agent Topology) | **Implemented** (R45, `aa72032`→`7f99e42`, ADR-016 + `internal/cluster`) |
| 8.3 | Enterprise Operations (HA / Backup / DR / Upgrade / Rolling Restart) | **Implemented** (R46, `7f99e42`, ADR-017 + `internal/enterprise`) |
| 8.4 | Governance (Policy / Quota / Multi-tenancy / Compliance) | **Implemented** (R47, `bbe222a`, ADR-018 + `internal/governance`) |
| **Phase 8** | **Platform Operations — CLOSED** | **CLOSED** (R47, all four layers PASS) |

Each row is authorized only after the previous architecture is signed off — the same
architecture-first discipline Phase 7 used (SDK → … → Reference Distribution, one signed spec at
a time).

---

## 7. Core Stability Statement (re-affirmed)

> **Core Stability Statement** (unchanged from ADR-012 / 013)
>
> Runtime Core is considered architecturally stable. Future features should preferentially be
> implemented as extensions built on top of the frozen Runtime Contract. Any change that modifies
> Runtime Contract semantics, lifecycle, or public invariants requires a new ADR.

Phase 8 re-affirms this and extends it: the **Plugin Ecosystem** is now *also* a frozen base.
Platform Operations is the third stable layer — built on top of the first two, never modifying
either.

---

## 8. Platform Operations Principles (SHOULD, applied Round 39)

Two readability / guardrail principles GPT (Round 39) asked to be stated explicitly, so they are
not lost in the freeze-boundary prose. They hold for every Phase 8 sub-phase.

- **SHOULD-1 — Platform Operations orchestrates Runtime; it never replaces Runtime execution.**
  The only allowed call chain is `Platform → Runtime API → Execution → Executor`. The chain
  `Platform → SSH → Command` is **forbidden** — it would slowly duplicate the Runtime and break
  the freeze. Platform observes, schedules, and governs; it never becomes an executor.
- **SHOULD-2 — Observability is a Read Model, not a Command Model.** Metrics / Logs / Traces /
  Audit all *observe only*; they never execute. Declaring this up front (Round 39) keeps the 8.1
  boundary stable: the observability layer is a consumer of existing events, never an actor.

---

## 9. ADR-014 Sign-off (Round 39)

| Item | Verdict | Round |
|---|---|---|
| MUST-1 Runtime Contract frozen | PASS | 39 |
| MUST-2 Plugin Ecosystem (ADR-013) frozen | PASS | 39 |
| MUST-3 New Platform Capability allowed | PASS | 39 |
| MUST-4 Runtime Boundary requires new ADR (strengthened wording) | PASS | 39 |
| MUST-5 Architecture First | PASS | 39 |
| (SHOULD) Platform orchestrates, not executes + Observability read-only | applied | 39 |

**Round 39 Outcome: PASS.** GPT (Round 39) confirmed ADR-014 as the correct Phase 8.0 scope —
the third architecture axis (Platform Operations) sits *above* the two frozen systems, modifying
neither. MUST-4 wording was strengthened to "No Phase 8 work may modify the Runtime Contract
implicitly." The two SHOULD principles (§8) were added. **8.1 Observability Architecture is
authorized** and is specified in the companion **ADR-015** (one ADR per theme, per GPT's
"keep one ADR one topic" rule). The Phase 8 route is 8.0 → 8.1 → 8.2 → 8.3 → 8.4, each an
architecture ADR signed before any code.

*Phase 8.0 Platform Operations Architecture Scope — opening a new theme on top of two frozen systems (Accepted, Rounds 38–39).*

---

## 10. Phase 8 CLOSED (Round 47)

Per GPT (Round 47) final capability review, Phase 8 Platform Operations is **CLOSED** — all
four capability layers passed both architecture and implementation sign-off, and the two founding
principles held end-to-end.

### 10.1 Layer status (architecture + implementation)

| Capability | Architecture ADR | Impl Package | Status |
|---|---|---|---|
| Observability (Read Model) | ADR-015 | `internal/observability` | ✅ PASS (R44) |
| Cluster Coordination | ADR-016 | `internal/cluster` | ✅ PASS (R45) |
| Enterprise Operations (Policy Attachment) | ADR-017 | `internal/enterprise` | ✅ PASS (R46) |
| Governance (Policy Evaluation) | ADR-018 | `internal/governance` | ✅ PASS (R47) |

### 10.2 Founding principles — verified end-to-end

GPT (R47) confirmed all three Phase 8 invariants held across the full arc:

1. **Composition over Modification** — Phase 8 modified none of: Runtime Contract (ADR-010),
   Execution Contract, Plugin Manifest, Provider, Loader, `isolation` (ADR-011/012/013). Every
   new capability is a peripheral package composed *on top of* the frozen base.
2. **No New Execution Path** — no `Observability → Execute`, `Cluster → Execute`,
   `Enterprise → Execute`, or `Governance → Execute`. The *only* execution path remains
   `Existing Runtime Interface → Execution`.
3. **Capability Ownership is unambiguous** — no duplicated ownership:

   | Layer | Owns |
   |---|---|
   | Runtime Object | Runtime Core |
   | Plugin Lifecycle | Plugin Ecosystem |
   | Observation | Observability |
   | Placement Metadata | Cluster |
   | Policy Attachment | Enterprise |
   | Policy Evaluation | Governance |
   | Execution | Runtime |

### 10.3 The frozen seam, by structure

The split `Enterprise attaches, Governance evaluates, Runtime executes` is not a convention — it
is enforced by code structure:

- `Engine` (Governance) has **no exec method**; `Evaluate(policy, state) → Verdict` is a pure fn.
- `Verdict` is a **frozen value object** (no `Action`/`Command` field); allowed codes are
  `Allow / Deny / RequireApproval / MaintenanceBlocked`, explicitly *not* `RestartService /
  Rollback / Retry / KillProcess / ScheduleJob`.
- Each peripheral package carries an **AST guard** forbidding import of the frozen systems
  (`runtime`, `isolation`, `hostregistry`) and cross-package replication.

### 10.4 Hand-off to Phase 9

GPT (R47) authorized **(A) Phase 8 CLOSED → Phase 9 direction**, and instructed: **the next round
must first submit a Phase 9.0 Architecture Scope ADR, not code.** Three candidate directions were
proposed (see ADR-019):

- **A — Platform Integration Layer**: one unified *read-only* API over the four capabilities.
  No new execution entry, does not replace Runtime API, does not become a Control Plane.
- **B — Event Correlation Architecture**: cross-capability correlation over
  `ExecutionEvent / AuditEvent / SignatureEvent / GovernanceVerdict / ClusterChange`.
  Correlation only; owns no event source; modifies no event schema.
- **C — Documentation & Reference Deployment**: enterprise topology, multi-node ops guide,
  plugin ecosystem handbook, governance examples.

*Phase 8 Platform Operations — CLOSED (Round 47). The third architecture axis is complete:
four capabilities, architecture + implementation double-closed, zero frozen-system modification.*
