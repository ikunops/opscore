# ADR-019 — Phase 9.0: Post-Phase-8 Continuation Scope

- **Status**: Closed (Phase 9 CLOSED, Round 50)
- **Date**: 2026-08-07
- **Companion to**: ADR-014 (Phase 8 Platform Operations Scope — CLOSED), ADR-015/016/017/018
  (the four Phase 8 capability ADRs), ADR-010/011/012/013 (the three frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 9 — Post-Phase-8 Continuation

---

## 0. Abstract

Phase 8 **Platform Operations is CLOSED** (ADR-014 §10, Round 47). Four capability layers —
Observability, Cluster, Enterprise, Governance — are each architecture-frozen *and*
implementation-landed (`internal/observability`, `internal/cluster`, `internal/enterprise`,
`internal/governance`), with the two founding invariants proven end-to-end:

1. Composition over Modification (zero frozen-system change).
2. No New Execution Path (only `Existing Runtime Interface → Execution`).

Phase 9 is **not** a new architecture axis on par with Phase 8. It is the *continuation* phase:
turning the four closed capabilities into something consumable, correlatable, or documented — **without
re-opening any frozen boundary and without introducing a new execution path.**

Per GPT (Round 47) the first deliverable of Phase 9 is **this Scope ADR** — pick a direction
*before* any code, exactly the Architecture-First discipline Phase 7/8 proved. **Phase 9 writes
no implementation until the chosen direction's architecture ADR is signed off.**

---

## 1. Phase 9 positioning — continuation, not a new axis

| Phase | Nature | Adds |
|---|---|---|
| 7 | Ecosystem axis (frozen) | Plugin Ecosystem |
| 8 | Platform Operations axis (closed) | Observability / Cluster / Enterprise / Governance |
| **9** | **Continuation of 8** | consumes/correlates/documents the four closed capabilities |

Phase 9 operates *on top of* the four closed capabilities. It must obey every Phase 8 freeze
boundary (ADR-014 §2 MUST-1~5) **plus one new hard boundary**:

> **Phase 9 Boundary (MUST-0):** Phase 9 introduces **no new execution entry point**, **does not
> replace the Runtime API**, and **does not become a Control Plane**. Anything Phase 9 exposes is
> read-only over existing capability state, or correlation over existing event sources, or
> documentation. Full stop.

---

## 2. Freeze boundaries (inherited + new)

- **MUST-1 — Runtime Contract remains frozen.** ADR-010/011/012 unchanged.
- **MUST-2 — Plugin Ecosystem remains frozen.** ADR-013 unchanged.
- **MUST-3 — Phase 8 capabilities remain closed.** `internal/observability`, `internal/cluster`,
  `internal/enterprise`, `internal/governance` are implemented and signed; Phase 9 may *read*
  their state but must not modify their semantics or re-open their ADRs.
- **MUST-4 — No Execution-Contract change.** Any Runtime Contract change needs a new ADR, outside
  Phase 9.
- **MUST-5 — Architecture First.** The chosen direction's architecture ADR is signed before code.
- **MUST-0 (new, hard) — No new execution path / no Control Plane** (see §1).

---

## 3. Candidate directions (choose one)

Phase 9 proposes three candidate directions. Each is a *scope sketch* only — detail lands in a
follow-up architecture ADR once GPT selects.

### 3.1 Direction A — Platform Integration Layer (read-only aggregation)

**Goal.** Expose the four closed capabilities through one unified **read-only** API surface, so a
consumer (ops dashboard, CLI, another service) can query Observations / Cluster members /
Enterprise attachments / Governance verdicts without reaching into each package directly.

**Shape (proposed).** A thin facade package (e.g. `internal/platform` or a `readapi` module)
that *queries* the four capability packages and returns composed read models. It owns **no**
capability logic and **no** execution.

**MUST (A).**
- A-1 The layer is **read-only**: it queries existing state, it never mutates a capability.
- A-2 It does **not** add an execution entry; no `Execute`/`Run`/`Schedule` flows through it.
- A-3 It reuses each capability's existing public query API (e.g. `Collector.Query*`,
  `Manager.Members`, `Service.AttachmentsFor`, `Engine.Evaluate`); it invents no new capability.
- A-4 An AST guard forbids importing the frozen systems and forbids the facade becoming an
  executor (no `Run/Exec/Apply` methods).

**SHOULD (A).**
- A-S1 The facade returns *composed* read models (e.g. "verdict + its matched policy attachment +
  the observation that triggered evaluation") rather than raw per-package shapes.

**Out of scope (A).** No write/mutate API, no authn/authz engine, no event sourcing.

---

### 3.2 Direction B — Event Correlation Architecture

**Goal.** Build a cross-capability *correlation* model over the existing event sources, so an
operator can trace "this execution → this signature verdict → this governance verdict → this
cluster placement change → this observation" as one timeline.

**Event sources (all frozen, read-only).**
- `execution.ExecutionEvent` (Phase 6)
- `sandbox.Decision` (6.1 `Code`/`Allowed`/`Reason`)
- `manifest.SignatureResult` (Phase 5 verdict)
- `core.AuditEvent`
- `governance.Verdict` (Phase 8.4 output value object)
- `cluster` placement/change events (Phase 8.2, if emitted)

**Shape (proposed).** A `Correlator` that ingests the above (pushed or pulled) and builds a
correlation index keyed by `ExecutionID` / `RequestID` / `PluginID` / `AuditID` — the same ID
space the four capabilities already share. Read-only correlation; it **owns no event source** and
**modifies no event schema**.

**MUST (B).**
- B-1 Correlation only: builds views over existing events; emits no new events that other systems
  must consume as authoritative.
- B-2 Owns no event source: it reads `ExecutionEvent`/`AuditEvent`/`SignatureResult`/`Verdict`/
  `cluster` changes; it never appends to or alters them.
- B-3 Modifies no event schema: existing structs are consumed as-is; new correlation *index*
  structures are local to the correlator.
- B-4 AST guard forbids frozen-system import and any exec method.

**SHOULD (B).**
- B-S1 Correlator is deterministic: same input event set → same correlation index.

**Out of scope (B).** No new event bus, no replay authority, no alerting engine.

---

### 3.3 Direction C — Documentation & Reference Deployment

**Goal.** Capture the closed Phase 8 as durable artifacts: enterprise deployment topology,
multi-node operation guide, plugin ecosystem handbook, and governance (policy) examples.

**Shape.** Docs only — `docs/phase9/*` (topology, ops-runbook, ecosystem-handbook,
governance-examples). **No code.**

**MUST (C).** C-1 No code; C-2 Every doc references the authoritative ADR/package, not an
alternative spec.

**SHOULD (C).** C-S1 Include a worked governance example (policy attachment → evaluation →
verdict) end-to-end.

**Out of scope (C).** No new capability, no API.

---

## 4. Out of scope — explicitly forbidden for Phase 9

- Any new execution entry point / Control Plane (MUST-0).
- Modifying Runtime Contract / Plugin Ecosystem / any Phase 8 capability (MUST-1/2/3).
- Re-defining the Trust Boundary or Signature Policy (ADR-011 owns trust).
- Turning A or B into a writer/mutator of capability state or event sources.
- Any `.so` / WASM / Alternative Runtime Backend work (still deferred).

---

## 5. Decision requested (Round 48)

Please sign off this Scope ADR and select **one** direction for Phase 9:

- **(A) Platform Integration Layer** — unified read-only query facade over the four capabilities.
- **(B) Event Correlation Architecture** — correlation over frozen event sources.
- **(C) Documentation & Reference Deployment** — docs only, no code.
- **(Other)** — a direction you specify.

On selection, the next round submits the chosen direction's **architecture ADR** (e.g.
ADR-020 for A, ADR-020 for B, or a docs plan for C) — **no implementation until that is signed.**

---

## 6. Phase 9 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 9.0 | Phase 9 Continuation Scope (this ADR-019) | Accepted (R48) + **Phase 9 CLOSED (R50)** |
| 9.1 | Chosen-direction Architecture ADR (ADR-020) | Accepted (R49) |
| 9.2 | Platform Integration Implementation (`internal/platformview`) | **Implemented (R50, `e1c1195`)** |

Each step is authorized only after the previous is signed — same architecture-first discipline.

---

## 7. Sign-off placeholder (Round 48)

| Item | Verdict | Round |
|---|---|---|
| MUST-0 No new execution path / no Control Plane | ✅ PASS | 48 |
| MUST-1 Runtime Contract frozen | ✅ PASS | 48 |
| MUST-2 Plugin Ecosystem frozen | ✅ PASS | 48 |
| MUST-3 Phase 8 capabilities closed (read-only) | ✅ PASS | 48 |
| MUST-4 No Execution-Contract change | ✅ PASS | 48 |
| MUST-5 Architecture First | ✅ PASS | 48 |
| Direction selected (A/B/C/Other) | ✅ **(A) Platform Integration Layer** | 48 |

## 8. Phase 9 CLOSED (Round 50)

Per GPT (Round 50) verdict, Phase 9 is **CLOSED**:

- **Direction (A) Platform Integration Layer** implemented and signed. `internal/platformview`
  (read-only facade) landed in Round 50 — `gofmt`/`build`/`vet`/`test` gate green, **44 packages
  all `ok`, 0 FAIL** (`e1c1195`).
- The package-path adjustment `internal/platform` → `internal/platformview` (to avoid collision
  with the frozen Phase 2.x HostSnapshot resolver) is recognized as an **Architecture Preserving
  Refactoring** — path changed, architecture unchanged (GPT R50).
- All Phase 9 freeze boundaries (MUST-0~5) and SHOULDs (1/2/3) hold. Phase 9 introduced **no new
  execution path, no Control Plane, and no new Capability** — it completes the continuation chain
  `Runtime → Plugin Ecosystem → Platform Operations → Platform Integration`.

Phase 9 does not open any new architecture axis; it is the consumable cap of the four closed
capabilities. The four-tier system is now end-to-end closed and frozen (see ADR-021 Architecture
Baseline).

*Phase 9.0 Continuation Scope — consuming, correlating, or documenting the four closed Phase 8
capabilities, without re-opening any frozen boundary (Closed, Round 50).*
