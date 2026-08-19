# ADR-015 — Phase 8.1: Observability Architecture

- **Status**: Accepted (signed Round 40)
- **Date**: 2026-08-03
- **Theme**: Phase 8 — Platform Operations (ADR-014)
- **Companion to**: ADR-010 (Runtime Contract — frozen), ADR-011 (Trust — frozen), ADR-012 (Core Stability — frozen), ADR-013 (Plugin Ecosystem — frozen), ADR-014 (Platform Operations Scope — accepted)
- **Author**: OpsCore Plugin Runtime Workstream

---

## 0. Abstract

ADR-014 (Phase 8.0) defined Platform Operations as the **third architecture axis**, built on top
of the two frozen systems (Runtime Core + Plugin Ecosystem), modifying neither. This ADR (8.1)
specifies the **first Platform Operations capability — Observability** — as a *specification*
only. No implementation code is written until this architecture is signed off (Architecture
First, ADR-014 MUST-5).

Observability is the most natural first capability: a platform should first *know what is
happening* before it can manage itself (schedule, govern, recover). Its scope is a **unified
observation model** — Metrics, Logs, Traces, and Audit Correlation — that consumes the events
the frozen systems already emit.

The headline invariant of 8.1:

> **Observability is a Read Model, not a Command Model. It observes the frozen systems through
> their existing events and IDs; it never executes, never adds a new execution protocol, and
> never modifies the Runtime Contract.**

---

## 1. Scope — the unified observation model

| Signal | Source (existing system) | Purpose |
|---|---|---|
| **Metrics** | Runtime / Isolation / Plugin execution counters, latency histograms | Quantitative health (throughput, error rate, p99) |
| **Logs** | Structured logs from `Manager`, `isolation` helper, plugin stderr | Diagnostics, root-cause |
| **Traces** | Request / Execution spans (if a span ID already exists) | Causality across boundaries |
| **Audit Correlation** | Phase 5 signature verdicts, Phase 6.1 `Decision.Code`, Phase 6.3 isolation events | Tie *who/what ran* to *what happened* |

All four are **observation only**. They read from the existing event streams; they introduce no
new behavior into the Runtime or the Ecosystem.

---

## 2. Freeze Boundaries (MUST-1..5, from GPT Round 39)

- **MUST-1 — Observability is read-only.** It must not become an execution path. The observability
  layer can read and present; it can never issue an execution, a scheduling decision, or a control
  command. It is a *consumer*, not an *actor*.
- **MUST-2 — No Runtime Contract modification.** ADR-010/011/012 stay frozen. Observability adds
  no field, interface, or lifecycle stage to `Manifest` / `Provider` / `Loader` / `Manager` /
  `core.Context`.
- **MUST-3 — All data from existing systems; no new execution protocol.** Every signal originates
  from Audit / Execution / Plugin / Runtime / Isolation — the events those systems already emit.
  Observability defines **no new wire protocol** and adds **no new execution handshake**.
- **MUST-4 — Correlation via existing IDs only.** Join across signals using the IDs the systems
  already have: `RequestID`, `ExecutionID`, `AuditID`, `TraceID` (if present), `PluginID`.
  Observability introduces **no new identity system** — re-using existing IDs is what makes
  correlation honest and cheap.
- **MUST-5 — Composition, not intrusion.** The pipeline is
  `Execution → Event → Collector → Metrics/Logs/Traces → Dashboard`. The Runtime emits events;
  a *collector* (a new peripheral package, e.g. `internal/observability`) subscribes. Metrics code
  must **not** be injected into `Execution` / `Manager` / `isolation` — that would pollute the
  Runtime with monitoring logic and break the freeze (ADR-014 SHOULD-2).
- **SHOULD-1 — Event Schema Ownership stays with the producer.** Observability *consumes* events
  but does **not** own their semantics. An `Execution` event remains owned by the `Execution`
  system, not by `internal/observability`. The collector must never reshape an event's schema to
  suit a dashboard; if a needed field is missing, the fix belongs to the emitting system (behind
  its own ADR), never to the observer. This prevents monitoring convenience from quietly driving
  event-structure changes in the frozen systems.
- **SHOULD-2 — Dashboard is a Consumer, not a Capability.** Dashboards, visualization, Grafana,
  and UI are *read consumers* of the observation model — they are explicitly **out of scope** as
  architecture. The observability capability is the Event→Collector→Read Model pipeline; Grafana
  (or any renderer) may be swapped freely as long as it only reads. The capability must not be
  defined in terms of a specific UI.

---

## 3. Architecture — composition over instrumentation

```
Runtime Core (frozen)        Plugin Ecosystem (frozen)
   │ emits events               │ emits events
   ▼                            ▼
Isolation events           PackageRef / Registry events
Phase 6.1 Decision.Code    Phase 5 Signature verdicts
Phase 6.3 helper stderr
   │                            │
   └──────────┬─────────────────┘
              ▼
        Event Bus / Sink  (new, peripheral — internal/observability)
              │
              ▼
        Collector  (Metrics / Logs / Traces / Audit Correlation)
              │
              ▼
        Read Model  (dashboards, queries)  ──  READ ONLY, never executes
```

Key properties:

- **Peripheral package.** `internal/observability` depends on the frozen systems' *event outputs*;
  the frozen systems never import it. An AST guard forbids `internal/observability` from importing
  `internal/plugin/runtime` / `core` / `builtin` in a way that lets it call execution.
- **Event-driven, not call-injected.** The Runtime is unaware observability exists; it just emits
  events (as it already does for audit / decision / isolation). This preserves ADR-012's freeze.
- **Correlation without new IDs.** A query joins `ExecutionID → AuditID → PluginID → Decision.Code`
  using IDs already present — no schema migration of the frozen systems.

---

## 4. Out of Scope — explicitly forbidden for 8.1

- Modifying Runtime Contract / `Manifest` / `Provider` / `Loader` / `Manager` / `core.Context`
  (MUST-2 — needs a new ADR, ADR-014 MUST-4).
- New execution protocol / new wire handshake / new control command (MUST-3).
- Observability issuing execution, scheduling, or control commands (MUST-1 — it is a Read Model).
- New identity system for correlation (MUST-4 — reuse existing IDs).
- Injecting metrics/instrumentation code into `Execution` / `Manager` / `isolation` (MUST-5 —
  composition, not intrusion).
- Redefining the Trust Boundary (ADR-011 owns trust; 8.1 may *display* verdicts, never change them).
- Implementing the collector — this ADR is scope; code follows sign-off.
- **Dashboards / visualization / Grafana / UI as architecture** (SHOULD-2 — they are read
  *consumers* of the observation model, not part of the observability capability; they may be
  swapped freely as long as they only read).
- **Re-shaping event schemas to fit a dashboard** (SHOULD-1 — Event Schema Ownership stays with the
  producer; the observer consumes, it does not redefine what an `Execution`/`Isolation`/`Audit`
  event means).

---

## 5. Relationship to the frozen system & Phase 8 siblings

ADR-015 is one theme (Observability) under the Phase 8 umbrella (ADR-014). Its siblings are
8.2 Cluster Coordination, 8.3 Enterprise Operations, 8.4 Governance — each its own architecture
ADR, each obeying ADR-014's freeze boundaries. Observability is the *foundation* the others build
on: cluster health, DR signals, and compliance audit all consume the observation model 8.1 defines.

---

## 6. Phase 8 Route (authorization chain)

| Sub-phase | Deliverable | Status |
|---|---|---|
| 8.0 | Platform Operations Scope (ADR-014) | Accepted (R39) |
| 8.1 | Observability Architecture (this ADR-015) | Accepted (R40) · **Implemented `internal/observability` (R44 PASS)** |
| 8.2 | Cluster Coordination Architecture | Accepted (R41) · Implemented `internal/cluster` (R44→) |
| 8.3 | Enterprise Operations Architecture | proposed |
| 8.4 | Governance / Policy Architecture | proposed |

Per GPT (Round 39): architecture-first — all four sub-phase ADRs are signed before any Phase 8
implementation code is written. After 8.1 is accepted, the next is 8.2 (Cluster Coordination)
architecture, unless the operator chooses to begin 8.1 implementation.

---

## 7. Core Stability Statement (re-affirmed)

> **Core Stability Statement** (unchanged)
>
> Runtime Core is considered architecturally stable. Future features should preferentially be
> implemented as extensions built on top of the frozen Runtime Contract. Any change that modifies
> Runtime Contract semantics, lifecycle, or public invariants requires a new ADR.

8.1 re-affirms this from the observability angle: the observation model is an *extension that
reads existing events* — it adds no Contract change and no execution path.

---

## 8. Sign-off (Round 40)

GPT review (Round 40) — **ADR-015 Architecture PASS** (by description; full source sign-off deferred
to implementation):

| Item | Verdict |
|---|---|
| MUST-1 Read Model (no execution path) | ✅ PASS |
| MUST-2 Runtime Contract frozen | ✅ PASS |
| MUST-3 Existing event sources only (no new protocol) | ✅ PASS |
| MUST-4 Existing IDs only (no new identity) | ✅ PASS |
| MUST-5 Composition, not intrusion | ✅ PASS |
| SHOULD-1 Event Schema Ownership | ✅ applied (§2 / §4) |
| SHOULD-2 Dashboard is a Consumer, not a Capability | ✅ applied (§2 / §4) |
| Next phase | ✅ authorized Phase 8.2 — ADR-016 Cluster Coordination Architecture (spec-only) |

Direction chosen: **(A) Architecture First** — complete the 8.1–8.4 capability ADRs before any
Phase 8 implementation code, to keep the four capabilities consistent at the boundary level.

*Phase 8.1 Observability Architecture — the first Platform Operations capability, read-only by design (Accepted, Rounds 39→40).*

---

## 9. Implementation Sign-off (Round 44)

GPT review (Round 44) — **`internal/observability` Pilot Implementation PASS** (by description;
commit `9ad8dd2`, quality gate PASSED). The Pilot validated that a Phase 8 peripheral capability can
exist without modifying Runtime Core or intruding on the execution chain.

| Item | Verdict |
|---|---|
| ADR-015 MUST-1 Read Model (no execution path) | ✅ PASS |
| ADR-015 MUST-2 Runtime Contract frozen | ✅ PASS |
| ADR-015 MUST-3 Existing event sources only | ✅ PASS |
| ADR-015 MUST-4 Existing IDs only | ✅ PASS |
| ADR-015 MUST-5 Composition, not intrusion (AST guard) | ✅ PASS |
| Adapter (Producer → existing interface → Adapter → Collector) | ✅ recommended, kept |
| Blocking changes required | none |
| Next phase | ✅ authorized Phase 8.2 — `internal/cluster` implementation (per ADR-016 MUST-1..5) |

Round 44 SHOULDs (non-blocking, applied where noted):

- **SHOULD-1 — Observation Schema Version**: added `SchemaVersion` (const `"observability/v1"`)
  stamped on every `Observation` at ingest (model.go / collector.go / test). It is an
  *observability-local* read-model schema marker, explicitly NOT a Runtime Contract — downstream
  Dashboard / Exporter / Storage backends can branch on shape without guessing.
- **SHOULD-2 — Collector Backpressure Boundary**: future exporter/storage backends must not block the
  Runtime Producer. Frozen as a design rule; does not affect the current in-memory PASS and is
  deferred to the exporter phase.

Direction chosen: **(A) Implement 8.2 Cluster Coordination** — the second validation point for the
Phase 8 peripheral pattern, deliberately stricter (membership/group/label/placement only, no host
ownership, no execution).

*Phase 8.1 Observability — Reference Capability implemented and signed off (Rounds 39→44).*
