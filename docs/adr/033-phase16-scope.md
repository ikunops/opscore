# ADR-033 — Phase 16.0: Documentation & Reference Deployment Scope

- **Status**: Accepted (Round 70, signed PASS) — **Direction B: Documentation & Reference Deployment selected — Phase 16.0 CLOSED**
- **Date**: 2026-08-08
- **Companion to**: ADR-032 (Phase 15.1, **Phase 15 CLOSED**), ADR-031 (Phase 15.0, CLOSED),
  ADR-030 (Phase 14.1, CLOSED), ADR-029 (Phase 14.0, CLOSED), ADR-028 (Phase 13.1, CLOSED),
  ADR-027 (Phase 13.0, CLOSED), ADR-026 (Phase 12, CLOSED), ADR-024 (Phase 11.1, CLOSED),
  ADR-022 (Phase 10, CLOSED), ADR-021 (Architecture Baseline, frozen),
  ADR-014~020 (Phases 8~9, CLOSED), ADR-010/011/012/013 (the four frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 16 — stabilize and document the frozen 8-layer architecture as a reference
  deployment + contract surface (no runtime-behavior change).

---

## 0. Abstract

**Phase 15 Deployment Productionization is CLOSED** (ADR-031 + ADR-032 signed PASS; R67/R68/R69;
R69 implementation gate-green, commit `438b826`, not pushed). The frozen system now wires end-to-end as
a genuinely deployable, upgradable, observable, recoverable single-node production runtime:

```
Runtime Core → Plugin Ecosystem → Platform Operations → Platform Integration
            → Event Correlation → External Interface → Deployment & Distribution
            → Cluster Read Surface (Phase 13)
            → Governance Policy Persistence (Phase 14)
            → Operational hardening (Phase 15: config / service unit / probes / logging / shutdown / storeDir)
```

GPT (R69) directed: the next Major should continue the **Scope → Architecture → Implementation**
discipline and **NOT jump straight to coding**. Two candidate directions were offered:
- **A — Management API / Policy Management** (new write/control boundary; must be its own separate Scope ADR and must NOT quietly extend `external/v1`).
- **B — Documentation & Reference Deployment** (stabilize & document the frozen surface).

GPT expressed a preference for **B first** (if there is no clear new business problem). Per the user
decision in this round: **Direction B — Documentation & Reference Deployment is selected for Phase 16.**

This is the **selected-direction Scope ADR** for Phase 16 (analogous to how ADR-031 presented the
chosen direction for Phase 15). Per ADR-021, Phase 16 authors **no documentation artifact until the
Phase 16.1 Architecture ADR (ADR-034) is signed off.**

**Phase 16 core thesis (GPT R69):** *Stabilize, don't expand.* It produces documentation + reference
deployment artifacts that make the frozen 8-layer architecture, its deployment model, Policy lifecycle,
External read contract, and operational boundaries **explicit, discoverable, and referenceable** —
without changing any runtime behavior.

---

## 1. Phase 16 positioning — document & reference the frozen surface, do not reopen it

| Phase | Nature | Adds |
|---|---|---|
| 8 | Platform Operations axis (CLOSED) | Observability / Cluster / Enterprise / Governance |
| 9 | Platform Integration (CLOSED) | `internal/platformview` read-only facade |
| 10 | Event Correlation (CLOSED) | `internal/correlation` cross-capability projection |
| 11 | External Interface (CLOSED) | `external/v1` read-only Public Contract |
| 12 | Deployment & Distribution (CLOSED) | composition root + server harness + single-node process |
| 13 | Cluster Read Surface (CLOSED) | `internal/clusterprojection` host-centric read projection |
| 14 | Governance Policy Persistence (CLOSED) | `internal/governancepolicy` Policy ownership + persistence + lifecycle |
| 15 | Deployment Productionization (CLOSED) | config / service unit / probes / logging / shutdown / storeDir |
| **16** | **Documentation & Reference Deployment** | documentation + reference deployment artifacts *around* the frozen surface — **no runtime-behavior change** |

Phase 16 sits *around* the frozen tiers, like Phase 15, but even more conservative: it adds **no code
path at all** (unless a reference artifact genuinely requires a trivial, read-only, non-capability
helper that is itself documented and gated — and even then only by explicit sign-off). Its
deliverables are `docs/` content and `deploy/` reference artifacts.

> **Phase 16 Boundary (P16-0):** Phase 16 introduces **no change to any frozen capability's semantics,
> no change to the Runtime Contract, no new execution entry into the Runtime Core, no Control Plane or
> orchestration layer, and no new write/control API.** It only *documents and packages references for*
> the existing frozen architecture. All deliverables are `docs/` content and `deploy/` reference
> artifacts. If a candidate item would require modifying a frozen capability's data model, adding an
> execution path, or exposing a write API, it is **out of scope** for Phase 16 and belongs to Direction
> A (a separate Major Evolution with its own Scope ADR).

---

## 2. Freeze boundaries (inherited + new)

- **MUST-1 — Runtime Contract remains frozen.** ADR-010/011/012 unchanged.
- **MUST-2 — Plugin Ecosystem remains frozen.** ADR-013 unchanged.
- **MUST-3 — Phase 8 capabilities remain closed.** `internal/observability`, `internal/cluster`,
  `internal/enterprise`, `internal/governance` are implemented and signed. Phase 16 may *describe* them;
  it adds nothing to their data model or ownership.
- **MUST-4 — Platform Integration / Correlation / External / Cluster-projection / Governance-policy
  remain closed.** `internal/platformview`, `internal/correlation`, `internal/external`,
  `internal/clusterprojection`, `internal/governancepolicy` are the only sanctioned read/persistence
  sources. Phase 16 does not modify them.
- **MUST-5 — Deployment layer (Phase 12) + Phase 15 hardening remain the deployment surface.** Phase 16
  references them only; it must not undo the Phase 12 composition root or the Phase 15 operational
  scaffolding.
- **MUST-6 — Architecture First.** The Phase 16.1 architecture ADR is signed before any doc is authored.
- **P15-0 (inherited)** — no frozen-semantics break, no Runtime-Contract break, no new execution entry,
  no Control Plane / orchestration.
- **P16-0 (new, hard)** — §1: no runtime-behavior change; deliverables are docs + reference deployment
  artifacts only.

---

## 3. Phase 16 scope — Documentation & Reference Deployment (Direction B)

**Goal.** Make the frozen 8-layer architecture, its single-node deployment model, Policy lifecycle,
External read contract, and operational/evolution boundaries **explicit and referenceable**, so the
system can be operated, audited, and evolved safely without re-deriving intent from code.

**Shape.** Documentation + reference deployment artifacts around the existing surface. Zero new runtime
behavior.

### 3.1 Candidate scope items (proposed)

1. **Architecture reference (`docs/architecture.md`).** A single narrative + diagram of the 8-layer
   closed loop (Runtime Core → … → Governance Policy Persistence → operational hardening), naming the
   frozen packages, the read-only facades, and the Composition-Root-only wiring rule (A-1).
2. **Deployment reference (`docs/deployment.md`).** Document the Phase 15 single-node production
   baseline: config schema, service unit, probes (`/healthz` `/readyz` `/versionz`), structured logging,
   graceful shutdown, `PolicyStoreDir` management, and the in-place single-node upgrade path. Reference
   the existing `deploy/` artifacts.
3. **Policy lifecycle reference (`docs/policy-lifecycle.md`).** Document Policy ownership, persistence,
   lifecycle states, and the read-only `external/v1` exposure — without implying any write/control path
   (Direction A is explicitly deferred).
4. **External read contract reference (`docs/external-contract.md`).** Freeze-document `external/v1` as
   the read-only Public Contract: endpoints, payloads, stability guarantees, and the hard rule that it
   must never gain a write path.
5. **Operations & evolution boundaries (`docs/operations.md`).** Document the evolution charter
   (ADR-021 §6: Scope → Architecture → Implementation), the frozen-boundary iron laws, AST import guards
   + `TestNoExecMethod` discipline, and the relay/sign-off process used in this workstream.
6. **Reference deployment artifacts (`deploy/`).** Extend the existing `deploy/` (systemd, Dockerfile,
   example JSON) with a README and any missing reference (e.g., an upgrade note) — packaging references
   only, no new runtime behavior.
7. **Top-level README / index.** A concise entry point that points to the above references and states
   the project's frozen status and evolution rules.

### 3.2 MUST (Phase 16 iron laws)

- **P16-1 — Documentation stays within descriptive/packaging nature.** No capability-semantics
  mutation, no code-behavior change.
- **P16-2 — No new execution entry into the Runtime Core;** no new endpoint that triggers
  Execute/Run/Apply/Schedule/Dispatch/Rollback. The Public Contract (`external/v1`) stays read-only.
- **P16-3 — No write/control API** of any kind (this is the explicit differentiator from Direction A).
- **P16-4 — All referenced packages remain frozen** (MUST-1~5); docs describe, they do not modify.
- **P16-5 — Architecture First:** the Phase 16.1 architecture ADR (documentation information
  architecture) is signed before any doc is authored.
- **P16-6 — No new dependency** (no new Go module / no new runtime library) introduced for
  documentation purposes.

### 3.3 SHOULD (Phase 16)

- **P16-S1** Documentation is accurate to the frozen code (cross-checked against `internal/...`
  packages and `external/v1`).
- **P16-S2** Reference deployment artifacts are copy-paste runnable for a single node.
- **P16-S3** A clear index/README makes the references discoverable.

### 3.4 Out of scope (Phase 16)

- No Management API / Policy write/control boundary (Direction A — separate Major).
- No multi-node consensus / replication; remains single-node.
- No HA Control Plane, no orchestration layer.
- No new capability, no Public API change, no frozen-package modification.
- No Runtime Contract change (ADR-010/011/012).
- No change to `Governance.Evaluate` semantics or to `internal/governancepolicy` ownership (Phase 14
  closed).
- No write path into `external/v1`.

---

## 4. Out of scope — explicitly forbidden for Phase 16

- Any change that breaks a frozen ownership boundary (R51), Runtime Contract, or Plugin Ecosystem
  contract (P16-0/1/2).
- Modifying the signed implementations of any frozen package, or the Phase 12/13/14 packages
  (MUST-3/4).
- A write/mutate Public API or any new execution path (P16-2/3).
- Multi-node consensus / replication engine; HA Control Plane; orchestration.
- Undoing the Phase 12 composition root or the Phase 15 operational hardening.

---

## 5. Decision requested (Round 70)

Per GPT (R69) directive, **do not author docs yet** — the next step is this Scope ADR, then an
architecture ADR (ADR-034), then documentation authoring. The user selected **Direction B
(Documentation & Reference Deployment)** as the Phase 16 Major Evolution (stabilize & document the
frozen surface before any further capability expansion). Please sign off this Scope ADR and confirm
Phase 16 proceeds as **Direction B**:

- **(A) Documentation & Reference Deployment** — document + reference the frozen 8-layer architecture,
  deployment model, Policy lifecycle, External read contract, and operational boundaries; no
  runtime-behavior change. *(User-selected for Phase 16.)*
- **(Other)** — a direction you specify (e.g., switch to Direction A Management API, which would
  require its own separate Scope ADR).

On selection, the next round submits the Phase 16.1 **architecture ADR (ADR-034)** — **no
documentation artifact until that is signed.**

---

## 6. Phase 16 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 16.0 | Phase 16 Documentation & Reference Deployment Scope (this ADR-033) | **signed PASS R70 — Phase 16.0 CLOSED** |
| 16.1 | Phase 16 Documentation & Reference Deployment Architecture (ADR-034) | proposed — after 16.0 sign-off (R71) |
| 16.2 | Phase 16 Documentation & Reference Deployment Authoring | proposed — after 16.1 sign-off (R72) |

Each step is authorized only after the previous is signed — same architecture-first discipline.

---

## 7. Sign-off placeholder (Round 70)

| Item | Verdict | Round |
|---|---|---|
| P16-0 No frozen-semantics / Runtime-Contract break, no new execution entry, no Control Plane / orchestration, no runtime-behavior change | ✅ PASS | 70 |
| MUST-1 Runtime Contract remains frozen | ✅ PASS | 70 |
| MUST-2 Plugin Ecosystem remains frozen | ✅ PASS | 70 |
| MUST-3 Phase 8 capabilities remain closed (no data-model/ownership addition) | ✅ PASS | 70 |
| MUST-4 platformview/correlation/external/clusterprojection/governancepolicy remain the only read/persistence sources | ✅ PASS | 70 |
| MUST-5 Deployment layer (Phase 12) + Phase 15 hardening remain the deployment surface | ✅ PASS | 70 |
| MUST-6 Architecture First (ADR-034 signed before docs) | ✅ PASS | 70 |
| Direction selected: **B — Documentation & Reference Deployment** (P16-1~P16-6, external/v1 unmodified, no write API) | ✅ PASS | 70 |

*Phase 16.0 Documentation & Reference Deployment Scope — document & reference the frozen 8-layer
architecture, deployment model, Policy lifecycle, External read contract, and operational boundaries;
no runtime-behavior change, no new execution entry, no write/control API. No documentation artifact
until ADR-034 is signed.
**Phase 16.0 CLOSED (Round 70).***

---

## 8. R70 additional guidance (for ADR-034, non-blocking SHOULD)

GPT (R70) flagged five non-blocking recommendations for the Phase 16.1 Architecture ADR (ADR-034):
- **SHOULD-1 — Documentation SSOT:** every key fact must name a single authoritative source, avoiding drift between README / architecture.md / deployment.md.
- **SHOULD-2 — Versioned Contract Documentation:** `external/v1` docs must state version, read-only nature, and breaking-change rules, staying consistent with the actual contract.
- **SHOULD-3 — Deployment Reproducibility:** the Reference Deployment must reproduce the Phase 15 single-node deployment as documented, inventing no second deployment architecture.
- **SHOULD-4 — Architecture Boundary Matrix:** produce a `layer → owner → allowed dependency → forbidden dependency → public surface` matrix as the baseline for future Major Evolution.
- **SHOULD-5 — Drift Detection:** consider mechanical checks (contract version, deployment entrypoint, layer names) to prevent doc/code drift — without introducing new runtime capability.

Two hard boundaries reaffirmed by GPT: (1) Reference Deployment must not re-implement Deployment (no new startup path, sidecar orchestration, HA, leader election, or management controller); (2) Documentation must not become a covert architecture-evolution entry — if docs find code/intent mismatch, record it as fact/diff/future-Major candidate, never silently modify frozen code to make docs 'look nice'. Direction A (Management API) stays a separate Major: no `docs → reference deployment → management endpoint → Policy mutation` implicit chain.
