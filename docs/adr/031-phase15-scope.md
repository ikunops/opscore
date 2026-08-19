# ADR-031 — Phase 15.0: Deployment Productionization Scope

- **Status**: Accepted (Round 67, signed PASS) — **Direction A: Deployment Productionization selected — Phase 15.0 CLOSED**
- **Date**: 2026-08-08
- **Companion to**: ADR-030 (Phase 14.1, **Phase 14 CLOSED**), ADR-029 (Phase 14.0, CLOSED),
  ADR-028 (Phase 13.1, CLOSED), ADR-027 (Phase 13.0, CLOSED), ADR-026 (Phase 12, CLOSED),
  ADR-024 (Phase 11.1, CLOSED), ADR-022 (Phase 10, CLOSED), ADR-021 (Architecture Baseline,
  frozen), ADR-014~020 (Phases 8~9, CLOSED), ADR-010/011/012/013 (the four frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 15 — productionize the already-frozen Deployment surface (Phase 12) plus the
  real Cluster read-surface (Phase 13) and real Governance Policy persistence (Phase 14) that now sit
  behind it.

---

## 0. Abstract

**Phase 14 Governance Policy Persistence is CLOSED** (ADR-029 + ADR-030; R64/R65/R66 signed PASS;
R66 implementation gate-green). The frozen system now wires end-to-end with a real Cluster host-centric
projection (Phase 13) *and* a real Governance policy source + lifecycle (Phase 14), closing the Phase 12
honest-empty gaps:

```
Runtime Core → Plugin Ecosystem → Platform Operations → Platform Integration
            → Event Correlation → External Interface → Deployment & Distribution
            → Cluster Read Surface (Phase 13)
            → Governance Policy Persistence (Phase 14)
```

GPT (R66) explicitly directed: **do not keep extending new business capability; the highest-value next
step is Phase 15.0 — Deployment Productionization (Direction A from ADR-029 §3.1).** The already-frozen
capabilities (Deployment harness from Phase 12, plus the real read-surfaces and policy persistence added
in Phase 13/14) should first be validated as a *genuinely deployable, upgradable, observable,
recoverable single-node production runtime* before any further capability expansion.

This is the **selected-direction Scope ADR** for Phase 15 (analogous to how ADR-029 presented candidate
directions; here Direction A is chosen and refined into concrete scope + iron laws). Per ADR-021, Phase 15
writes **no implementation until the Phase 15.1 Architecture ADR (ADR-032) is signed off.**

**Phase 15 core thesis (GPT R66):** *Productionization ≠ new capability.* It may only touch
`config → harness → existing frozen capabilities → external/v1`. It **must not** become
`deployment → orchestration → execution`.

---

## 1. Phase 15 positioning — productionize the frozen surface, do not reopen it

| Phase | Nature | Adds |
|---|---|---|
| 8 | Platform Operations axis (CLOSED) | Observability / Cluster / Enterprise / Governance |
| 9 | Platform Integration (CLOSED) | `internal/platformview` read-only facade |
| 10 | Event Correlation (CLOSED) | `internal/correlation` cross-capability projection |
| 11 | External Interface (CLOSED) | `external/v1` read-only Public Contract |
| 12 | Deployment & Distribution (CLOSED) | composition root + server harness + single-node runnable process |
| 13 | Cluster Read Surface (CLOSED) | `internal/clusterprojection` host-centric read projection |
| 14 | Governance Policy Persistence (CLOSED) | `internal/governancepolicy` Policy ownership + persistence + lifecycle |
| **15** | **Deployment Productionization** | operational hardening *around* the frozen Deployment surface (config, service unit, health, logging, graceful shutdown, upgrade) — **no capability-semantics change** |

Phase 15 sits *around* the frozen tiers. It must obey every existing freeze boundary (ADR-021) **plus
one new hard boundary**:

> **Phase 15 Boundary (P15-0):** Phase 15 introduces **no change to any frozen capability's semantics,
> no change to the Runtime Contract, no new execution entry into the Runtime Core, and no Control Plane
> or orchestration layer.** It only *assembles, configures, observes, and operates* the existing
> Deployment surface (Phase 12 harness + `cmd/opscore-server`). Productionization artifacts (config
> files, service unit, probes, logging, upgrade packaging) are **operational scaffolding**, never new
> runtime behavior. Full stop. If a candidate item would require modifying a frozen capability's data
> model or adding an execution path, it is **out of scope** for Phase 15 and must be a separate Major
> Evolution.

---

## 2. Freeze boundaries (inherited + new)

- **MUST-1 — Runtime Contract remains frozen.** ADR-010/011/012 unchanged.
- **MUST-2 — Plugin Ecosystem remains frozen.** ADR-013 unchanged.
- **MUST-3 — Phase 8 capabilities remain closed.** `internal/observability`, `internal/cluster`,
  `internal/enterprise`, `internal/governance` are implemented and signed. Phase 15 may *observe* them
  read-only; it adds nothing to their data model or ownership.
- **MUST-4 — Platform Integration / Correlation / External / Cluster-projection / Governance-policy
  remain closed.** `internal/platformview`, `internal/correlation`, `internal/external`,
  `internal/clusterprojection`, `internal/governancepolicy` are the only sanctioned read/persistence
  sources. Phase 15 does not modify them.
- **MUST-5 — Deployment layer (Phase 12) remains the deployment surface.** Phase 15 extends it
  *operationally only* (assembles, mounts, lifecycle, observability) per ADR-026; it must not undo the
  Phase 12 composition root or introduce execution.
- **MUST-6 — Architecture First.** The Phase 15.1 architecture ADR is signed before code.
- **P15-0 (new, hard) — No frozen-semantics break, no Runtime-Contract break, no new execution entry,
  no Control Plane / orchestration (§1).**

---

## 3. Phase 15 scope — Deployment Productionization (Direction A, refined)

**Goal.** Harden the Phase 12 `internal/harness` + `cmd/opscore-server` single-node runtime into a
*production deployment baseline*: validated config, service-unit packaging, health/readiness probes,
graceful shutdown, structured logging, explicit config/secret boundary, fail-closed startup, version /
schema exposure, and a single-node upgrade path.

**Shape.** Operational artifacts **around** the existing Deployment surface. Deployment layer only — must
not evolve into a Control Plane and must not change any capability semantics.

### 3.1 Candidate scope items (proposed)

1. **Configuration file + schema validation.** A version-stamped config file loaded and validated against
   `HarnessConfig` on startup (extends ADR-029 A-S1). Unknown / deprecated keys fail-closed.
2. **Service unit (systemd / container).** A unit that declares *single-node* topology; the multi-node
   seam stays inert (extends ADR-029 A-S2).
3. **Health / readiness probes.** Read-only endpoints that report the frozen read models' availability
   (no capability mutation) (extends ADR-029 A-3).
4. **Graceful shutdown.** Preserves Phase 12 idempotency guarantees (SHOULD-8 of ADR-026); in-flight
   reads drain, persistence flushes (extends ADR-029 A-4).
5. **Structured logging + runtime config boundary.** Level/format wired through the existing harness
   Config without changing read contracts (extends ADR-029 A-S3).
6. **Persistence path management (`PolicyStoreDir`).** The Phase 14 (`DefaultPolicyStoreDir`) path is
   managed: validated on startup, created if absent with correct permissions, fail-closed if
   unwriteable.
7. **Data directory permissions.** Restrictive perms on the data/state directory; warn or fail if too
   open.
8. **Config / secret boundary.** Secrets are never embedded in the binary or committed config; config
   load validates against `HarnessConfig` and refuses secret-in-plaintext where a secret source is
   expected (extends ADR-029 A-5).
9. **Fail-closed startup / missing-dependency behavior.** If a required dependency (store dir, config,
   capability wiring) is missing or invalid, the process refuses to start (no partial/degraded silent
   run).
10. **Version / schema info exposure.** A read-only endpoint or log line exposing build version + ADR
    schema revision for operability/audit.
11. **Single-node upgrade path.** Documented, supported in-place upgrade of the single-node process
    (config migration is additive/validated; no multi-node replication).

### 3.2 MUST (Phase 15 iron laws)

- **P15-1 — Productionization stays within the Phase 12 Deployment-layer nature** (assembles, mounts,
  lifecycle, observability). No capability-semantics mutation.
- **P15-2 — No new execution entry into the Runtime Core;** no new endpoint that triggers
  Execute/Run/Apply/Schedule/Dispatch/Rollback. The Public Contract (`external/v1`) stays read-only.
- **P15-3 — Health/readiness probes read only the frozen read models** (no capability mutation, no
  write path).
- **P15-4 — Graceful shutdown preserves Phase 12 idempotency guarantees** (SHOULD-8 of ADR-026).
- **P15-5 — Config/secret boundary is explicit:** secrets never embedded in the binary or committed
  config; config load validates against `HarnessConfig` and fails-closed on invalid/missing required
  config.
- **P15-6 — Architecture First:** the Phase 15.1 architecture ADR is signed before any code.

### 3.3 SHOULD (Phase 15)

- **P15-S1** Config files are version-stamped and validated against `HarnessConfig` on load; unknown
  keys fail-closed.
- **P15-S2** Service unit declares single-node topology; multi-node seam remains inert.
- **P15-S3** Structured logging (level/format) wired through the existing harness Config without
  changing read contracts.
- **P15-S4** `PolicyStoreDir` (Phase 14) is created with restrictive permissions if absent and
  fail-closed if unwriteable.

### 3.4 Out of scope (Phase 15)

- No multi-node consensus / replication; the system remains single-node.
- No HA Control Plane, no orchestration layer.
- No new capability, no Public API change, no frozen-package modification.
- No Runtime Contract change (ADR-010/011/012).
- No change to `Governance.Evaluate` semantics or to `internal/governancepolicy` ownership (Phase 14
  closed).
- No write path into `external/v1`.

---

## 4. Out of scope — explicitly forbidden for Phase 15

- Any change that breaks a frozen ownership boundary (R51), Runtime Contract, or Plugin Ecosystem
  contract (P15-0/1/2).
- Modifying the signed implementations of Phase 8 capabilities, or the Phase 13/14 read/persistence
  packages (MUST-3/4).
- Turning a productionization concern into a Control Plane or a new execution path (P15-0/2).
- A write/mutate Public API.
- Multi-node consensus / replication engine; HA Control Plane; orchestration.
- Undoing the Phase 12 composition root.

---

## 5. Decision requested (Round 67)

Per GPT (R66) directive, **do not code yet** — the next step is this Scope ADR, then an architecture ADR
(ADR-032), then implementation. GPT selected **Direction A (Deployment Productionization)** as the Phase
15 Major Evolution (highest operational value before further capability expansion). Please sign off this
Scope ADR and confirm Phase 15 proceeds as **Direction A**:

- **(A) Deployment Productionization** — harden Phase 12 for single-node production (config, service
  unit, health, logging, graceful shutdown, upgrade). Lowest architectural risk; no capability-semantics
  change. *(GPT-selected for Phase 15.)*
- **(Other)** — a direction you specify.

On selection, the next round submits the Phase 15.1 **architecture ADR (ADR-032)** — **no implementation
until that is signed.**

---

## 6. Phase 15 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 15.0 | Phase 15 Deployment Productionization Scope (this ADR-031) | **signed PASS R67 — Phase 15.0 CLOSED** |
| 15.1 | Phase 15 Deployment Productionization Architecture (ADR-032) | proposed — after 15.0 sign-off (R68) |
| 15.2 | Phase 15 Deployment Productionization Implementation | proposed — after 15.1 sign-off (R69) |

Each step is authorized only after the previous is signed — same architecture-first discipline.

---

## 7. Sign-off placeholder (Round 67)

| Item | Verdict | Round |
|---|---|---|
| P15-0 No frozen-semantics / Runtime-Contract break, no new execution entry, no Control Plane / orchestration | ✅ PASS | 67 |
| MUST-1 Runtime Contract remains frozen | ✅ PASS | 67 |
| MUST-2 Plugin Ecosystem remains frozen | ✅ PASS | 67 |
| MUST-3 Phase 8 capabilities remain closed (no data-model/ownership addition) | ✅ PASS | 67 |
| MUST-4 platformview/correlation/external/clusterprojection/governancepolicy remain the only read/persistence sources | ✅ PASS | 67 |
| MUST-5 Deployment layer (Phase 12) remains the deployment surface | ✅ PASS | 67 |
| MUST-6 Architecture First (ADR-032 signed before code) | ✅ PASS | 67 |
| Direction selected: **A — Deployment Productionization** (P15-1~P15-6, external/v1 unmodified) | ✅ PASS | 67 |

*Phase 15.0 Deployment Productionization Scope — productionize the already-frozen Deployment surface
(Phase 12) plus the real read-surfaces (Phase 13) and policy persistence (Phase 14); no capability-
semantics change, no new execution entry, no Control Plane. No implementation until ADR-032 is signed.
**Phase 15.0 CLOSED (Round 67).***
