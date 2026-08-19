# ADR-023 — Phase 11.0: Post-Phase-10 External Interface Scope

- **Status**: Accepted (Round 55) — direction **A External Interface Architecture** selected; Phase 11.1 architecture ADR (ADR-024) submitted in Round 56
- **Date**: 2026-08-07
- **Companion to**: ADR-021 (Architecture Baseline, frozen), ADR-022 (Phase 10 Event Correlation, CLOSED),
  ADR-019/020 (Phase 9 Platform Integration, CLOSED), ADR-014~018 (Phase 8 capabilities, CLOSED),
  ADR-010/011/012/013 (the four frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 11 — External Interface (consumption boundary over the frozen baseline)

---

## 0. Abstract

Phase 10 **Event Correlation is CLOSED** (ADR-022, Round 54). The five-tier system is now
end-to-end closed and frozen:

```
Runtime Core → Plugin Ecosystem → Platform Operations → Platform Integration → Event Correlation
```

All five tiers are internal-only. There is **no stable external consumption boundary** yet — the
read models (`platformview`, `correlation`) exist but are not exposed through a defined Public
Contract (API/CLI/SDK), and there is no Authentication / Tenant boundary in front of them.

Phase 11 is the **External Interface** phase: it turns the frozen internal capabilities into
something a *consumer outside the process* (ops dashboard, CLI, SDK, integration adapter) can
safely read. Per the ADR-021 evolution charter, exposing a Public API is a **Major Evolution** —
it changes the Public Contract / Versioning / Authn / Tenant surface — so it requires this Scope
ADR, then an Architecture ADR, then implementation. **Phase 11 writes no implementation until the
chosen direction's architecture ADR is signed off.**

---

## 1. Phase 11 positioning — external boundary, not a new internal axis

| Phase | Nature | Adds |
|---|---|---|
| 8 | Platform Operations axis (closed) | Observability / Cluster / Enterprise / Governance |
| 9 | Platform Integration (closed) | `internal/platformview` read-only facade |
| 10 | Event Correlation (closed) | `internal/correlation` cross-capability projection |
| **11** | **External Interface** | stable Public Contract over the frozen read models |

Phase 11 sits *in front of* the frozen tiers. It must obey every existing freeze boundary
(ADR-021) **plus one new hard boundary**:

> **Phase 11 Boundary (MUST-0):** Phase 11 introduces **no new execution entry point into the
> Runtime Core**, **does not modify any frozen capability**, and **does not become a Control
> Plane**. Anything Phase 11 exposes is a *read-only projection* of existing read models
> (`platformview` / `correlation`); writes/mutations stay owned by the frozen capabilities. Full
> stop.

---

## 2. Freeze boundaries (inherited + new)

- **MUST-1 — Runtime Contract remains frozen.** ADR-010/011/012 unchanged.
- **MUST-2 — Plugin Ecosystem remains frozen.** ADR-013 unchanged.
- **MUST-3 — Phase 8 capabilities remain closed.** `internal/observability`, `internal/cluster`,
  `internal/enterprise`, `internal/governance` are implemented and signed; Phase 11 may *read*
  their state (via `platformview`/`correlation`) but must not modify them.
- **MUST-4 — Platform Integration / Correlation remain closed.** `internal/platformview`,
  `internal/correlation` are the only sanctioned read sources Phase 11 composes over.
- **MUST-5 — Architecture First.** The chosen direction's architecture ADR is signed before code.
- **MUST-0 (new, hard) — No new execution path / no Control Plane / no capability mutation** (§1).

---

## 3. Candidate directions (choose one)

Phase 11 proposes three candidate directions. Each is a *scope sketch* only — detail lands in a
follow-up architecture ADR once GPT selects.

### 3.1 Direction A — External Interface Architecture (Read API / CLI / SDK / Adapter) — **recommended**

**Goal.** Expose the frozen read models (`platformview`, `correlation`) through one stable,
versioned, authenticated **read-only** Public Contract — a thin HTTP/gRPC Read API, a CLI, an SDK,
and an Integration Adapter — so external consumers can consume OpsCore without reaching into any
internal package.

**Shape (proposed).** A new peripheral surface (e.g. `internal/api` or `cmd/opscore-cli`) that
*adapts* `platformview` / `correlation` query results into external DTOs under a frozen
`external/v1` read contract. It owns **no** capability logic, **no** execution, and **no** data.

**MUST (A).**
- A-1 The surface is **read-only**: it projects existing read models; it never mutates a
  capability or invokes an execution path.
- A-2 It adds **no execution entry**: no `Execute`/`Run`/`Schedule`/`Apply` flows through it.
- A-3 It reuses `platformview` / `correlation` existing query APIs; it invents no new capability.
- A-4 A Public Contract is frozen and versioned (`external/v1`); breaking changes bump the version.
- A-5 Authentication Boundary and Tenant Boundary are declared up front (even if v1 ships a
  single-tenant/no-auth stub, the seam must exist).
- A-6 An AST guard forbids importing frozen systems and forbids the surface becoming an executor.

**SHOULD (A).**
- A-S1 The API returns *composed* read DTOs consistent with `platform/view/v1` and
  `correlation/view/v1` (no divergence of the read contract).
- A-S2 CLI / SDK are generated from the same contract (single source of truth).

**Out of scope (A).** No write/mutate API, no authn/authz engine internals, no event sourcing,
no new execution path. (A write API would be a *separate* Major Evolution.)

---

### 3.2 Direction B — Deployment & Distribution Architecture

**Goal.** Make the frozen baseline production-deployable: single-node and multi-node deployment
topology, upgrade strategy, migration, backup/restore.

**Shape.** Deployment manifests, an `opscore` server harness (process wiring only — no capability
logic), upgrade/migration tooling. **Read-only operational surface**; does not alter capability
semantics.

**MUST (B).**
- B-1 Deployment wiring references frozen packages as-is; no re-implementation of capability logic.
- B-2 Upgrade/migration tooling is additive and reversible; never mutates a frozen contract.
- B-3 No new execution entry into the Runtime Core.

**SHOULD (B).**
- B-S1 Backup/restore covers only external state (DB/config), never the frozen in-process models.

**Out of scope (B).** No new capability, no Public API (that is Direction A).

---

### 3.3 Direction C — Documentation & Reference Architecture

**Goal.** Capture the closed five-tier system as durable artifacts: reference deployment, example
workflow, operator guide, extension guide.

**Shape.** Docs only — `docs/phase11/*` (reference-deployment, example-workflow, operator-guide,
extension-guide). **No code** (except perhaps doc-embedded diagrams).

**MUST (C).** C-1 No code; C-2 Every doc references the authoritative ADR/package, not an
alternative spec.

**SHOULD (C).** C-S1 Include a worked end-to-end example (execution → signature → governance
verdict → cluster placement → correlation view).

**Out of scope (C).** No new capability, no API.

---

## 4. Out of scope — explicitly forbidden for Phase 11

- Any new execution entry point into the Runtime Core / Control Plane (MUST-0).
- Modifying Runtime Contract / Plugin Ecosystem / any Phase 8 capability / `platformview` /
  `correlation` (MUST-1/2/3/4).
- Turning A into a writer/mutator of capability state or event sources.
- Any `.so` / WASM / Alternative Runtime Backend work (still deferred).
- A write/mutate Public API (that is a distinct Major Evolution, not Phase 11.0).

---

## 5. Decision requested (Round 55)

Per GPT (Round 54) recommendation, **Direction A (External Interface Architecture)** is preferred,
but the choice is yours. Please sign off this Scope ADR and select **one** direction for Phase 11:

- **(A) External Interface Architecture** — stable, versioned, read-only Public Contract (API/CLI/
  SDK/Adapter) over `platformview` + `correlation`. *(recommended)*
- **(B) Deployment & Distribution Architecture** — single/multi-node deploy, upgrade, migration,
  backup/restore.
- **(C) Documentation & Reference Architecture** — docs only, no code.
- **(Other)** — a direction you specify.

On selection, the next round submits the chosen direction's **architecture ADR** (e.g. ADR-024 for
A) — **no implementation until that is signed.**

---

## 6. Phase 11 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 11.0 | Phase 11 External Interface Scope (this ADR-023) | **Accepted (R55)** — direction A selected |
| 11.1 | Chosen-direction Architecture ADR (ADR-024) | **submitted R56 (pending sign-off)** |
| 11.2 | Chosen-direction Implementation (`internal/external`) | proposed — after 11.1 sign-off (R57) |

Each step is authorized only after the previous is signed — same architecture-first discipline.

---

## 7. Sign-off placeholder (Round 55)

| Item | Verdict | Round |
|---|---|---|
| MUST-0 No new execution path / no Control Plane / no capability mutation | ✅ PASS | 55 |
| MUST-1 Runtime Contract frozen | ✅ PASS | 55 |
| MUST-2 Plugin Ecosystem frozen | ✅ PASS | 55 |
| MUST-3 Phase 8 capabilities closed (read-only) | ✅ PASS | 55 |
| MUST-4 Platform Integration / Correlation closed (read-only source) | ✅ PASS | 55 |
| MUST-5 Architecture First | ✅ PASS | 55 |
| Direction selected (A/B/C/Other) | ✅ **A — External Interface Architecture** | 55 |

**GPT (R55) non-blocking suggestions folded into ADR-024:** SHOULD-1 External Contract Ownership
(DTO contract only — no domain/event/runtime model ownership); SHOULD-2 Public Contract Compatibility
(`external/v1` + `external/v2` versioning rules, no silent breaking change); SHOULD-3 Read Boundary
Enforcement (AST guard enhanced — forbid execution executor / plugin runtime / isolation / host
lifecycle imports).

*Phase 11.0 External Interface Scope — a stable, read-only consumption boundary over the frozen
five-tier baseline; no new execution path, no capability mutation. Direction A selected; authorized to
submit the Phase 11.1 architecture ADR (ADR-024) in Round 56. (Accepted, Round 55).*
