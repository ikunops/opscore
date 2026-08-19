# ADR-029 — Phase 14.0: Next Major Evolution Scope

- **Status**: Accepted (Round 64, signed PASS) — **Direction B: Governance Policy Persistence selected**
- **Date**: 2026-08-08
- **Companion to**: ADR-028 (Phase 13.1, **Phase 13 CLOSED**), ADR-027 (Phase 13.0, CLOSED), ADR-026
  (Phase 12, CLOSED), ADR-024 (Phase 11.1, CLOSED), ADR-022 (Phase 10, CLOSED), ADR-021 (Architecture
  Baseline, frozen), ADR-014~020 (Phases 8~9, CLOSED), ADR-010/011/012/013 (the four frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 14 — the next Major Evolution beyond the frozen seven-layer + deployment
  baseline (and beyond Phase 13's Cluster read-surface closure)

---

## 0. Abstract

**Phase 13 Read Surface Completeness is CLOSED** (ADR-027 + ADR-028; R61/R62/R63 signed PASS; R63
implementation gate-green). The frozen system now wires end-to-end with a real Cluster host-centric
projection closing the Phase 12 honest-empty gap:

```
Runtime Core → Plugin Ecosystem → Platform Operations → Platform Integration
            → Event Correlation → External Interface → Deployment & Distribution
            → Cluster Read Surface (Phase 13)
```

GPT (R63) explicitly advised: **pause coding new capabilities; the next step is a Phase 14.0 Scope ADR**,
not more read-surface patching — continuing to pile features risks re-entering the "extend the model
just to fill an API" loop. GPT identified two candidate Major Evolutions and **recommended B first**:

- **A — Deployment Productionization**: lift the already-existing `cmd/opscore-server` + `internal/harness`
  from "runnable deployment" to "production deployment baseline" (config, systemd/service,
  health/readiness, graceful shutdown, logging, config/secret boundary, upgrade/install packaging) —
  **without changing capability semantics**.
- **B — Governance Policy Persistence** *(GPT recommended)*: the long-deferred independent Major
  Evolution (R60/R61). Gives Governance a real policy **source** and **lifecycle**:
  `Policy → Repository/Persistence → Lifecycle → Governance.Evaluate`. Involves genuine Policy Ownership
  + Persistence + Lifecycle — higher architectural value, but higher risk, so it must be separately
  scoped here.

Per ADR-021, any change that alters an external read surface, a deployment productionization contract, or
(especially for Direction B) a frozen capability's data-ownership / model is a **Major Evolution** — it
requires this Scope ADR, then an Architecture ADR, then implementation. **Phase 14 writes no
implementation until the chosen direction's architecture ADR is signed off.**

Per GPT (R63), the recommended first step is *not* to code, but to scope. This ADR presents the candidate
directions and requests a selection.

---

## 1. Phase 14 positioning — evolution beyond the frozen baseline, not a re-open of closed phases

| Phase | Nature | Adds |
|---|---|---|
| 8 | Platform Operations axis (CLOSED) | Observability / Cluster / Enterprise / Governance |
| 9 | Platform Integration (CLOSED) | `internal/platformview` read-only facade |
| 10 | Event Correlation (CLOSED) | `internal/correlation` cross-capability projection |
| 11 | External Interface (CLOSED) | `external/v1` read-only Public Contract |
| 12 | Deployment & Distribution (CLOSED) | composition root + server harness + single-node runnable process |
| 13 | Cluster Read Surface (CLOSED) | `internal/clusterprojection` host-centric read projection |
| **14** | **Next Major Evolution** | productionize deployment (A) OR give Governance real policy persistence (B) — choose one |

Phase 14 sits *beyond* the frozen tiers. It must obey every existing freeze boundary (ADR-021) **plus
one new hard boundary**:

> **Phase 14 Boundary (MUST-0):** Phase 14 introduces **no change that breaks an existing frozen
> ownership boundary (R51 freeze), Runtime Contract, or Plugin Ecosystem contract**, and **does not
> turn a productionization concern or a policy-persistence concern into a Control Plane or a new
> execution path into the Runtime Core**. Full stop. If a candidate direction requires modifying a
> frozen capability's *data ownership* or *domain model* (Direction B, by nature), that must be surfaced
> in this Scope ADR as an explicit, separately-authorized change — never smuggled in under a
> "productionization" or "fill the empty view" label.

---

## 2. Freeze boundaries (inherited + new)

- **MUST-1 — Runtime Contract remains frozen.** ADR-010/011/012 unchanged.
- **MUST-2 — Plugin Ecosystem remains frozen.** ADR-013 unchanged.
- **MUST-3 — Phase 8 capabilities remain closed.** `internal/observability`, `internal/cluster`,
  `internal/enterprise`, `internal/governance` are implemented and signed. Phase 14 may *observe* them
  read-only; **any addition to their data model / ownership requires explicit separate authorization
  within this ADR** (Direction B only).
- **MUST-4 — Platform Integration / Correlation / External / Cluster-projection remain closed.** `internal/platformview`,
  `internal/correlation`, `internal/external`, `internal/clusterprojection` are the only sanctioned read
  sources.
- **MUST-5 — Deployment layer (Phase 12) remains the deployment surface.** If Phase 14 extends
  deployment (Direction A), it must **stay within the Deployment-layer nature** defined in ADR-026
  (assembles, never becomes a capability or Control Plane); it must not undo the Phase 12 composition
  root or introduce execution.
- **MUST-6 — Architecture First.** The chosen direction's architecture ADR is signed before code.
- **MUST-0 (new, hard) — No frozen-ownership / Runtime-Contract break, no Control Plane / new
  execution path** (§1).

---

## 3. Candidate directions (choose one)

Phase 14 proposes two candidate directions (plus a lowest-risk documentation fallback). Each is a *scope
sketch* only — detail lands in a follow-up architecture ADR once GPT selects.

### 3.1 Direction A — Deployment Productionization

**Goal.** Harden the Phase 12 scaffold for real single-node production: config files, systemd/service
packaging, graceful shutdown, health/readiness probes, logging/observability wiring, config/secret
boundary, upgrade/install packaging.

**Shape.** Add operational artifacts **around** the existing `internal/harness` + `cmd/opscore-server`.
**Deployment layer only** — must not evolve into a Control Plane, and must not change any capability
semantics.

**MUST (A).**
- A-1 Productionization stays within the Phase 12 Deployment-layer nature (assembles, mounts, lifecycle).
- A-2 No new execution entry into the Runtime Core; no capability-semantics mutation.
- A-3 Health/readiness probes read only the frozen read models (no capability mutation).
- A-4 Graceful shutdown preserves Phase 12 idempotency guarantees (SHOULD-8 of ADR-026).
- A-5 Config/secret boundary is explicit: secrets are never embedded in the binary or committed config;
  config load validates against `HarnessConfig`.

**SHOULD (A).**
- A-S1 Config files are version-stamped and validated against `HarnessConfig` on load.
- A-S2 systemd/container unit declares single-node topology (multi-node seam remains inert).
- A-S3 Structured logging (level/format) is wired through the existing harness Config without changing
  read contracts.

**Out of scope (A).** No multi-node consensus/replication, no new capability, no Public API change, no
frozen-package modification.

---

### 3.2 Direction B — Governance Policy Persistence — **GPT-recommended for Phase 14**

**Goal.** Close the long-deferred, independently-flagged Major Evolution (R60/R61): give Governance a
real **policy source** and **lifecycle** so `Governance.Evaluate` operates over persisted, versioned
policies rather than an empty stateless inventory.

```
Policy  →  Repository / Persistence  →  Lifecycle (create/read/update/archive)
                                                   ↓
                                          Governance.Evaluate (unchanged signature)
```

**Why it is its own Major Evolution (not a Phase 13 read-surface patch).** It introduces genuine
**Policy Ownership** (policy is now a first-class persisted entity, not an evaluated input), **Persistence**
(a new storage surface), and **Lifecycle** (create/read/update/archive/version). That is orthogonal to
"project existing state read-only" — it is net-new data ownership. R60/R61 deliberately deferred it;
Phase 14 is the authorized vehicle.

**Shape (proposed).**
- A new persisted policy entity owned by Governance (or a dedicated `internal/governancepolicy` package
  that Governance reads), with a Repository boundary (storage behind an interface — never leaking store
  internals into the frozen `internal/governance` evaluator).
- Lifecycle operations are management-plane operations, **strictly separated** from the read-only
  `external/v1` Public Contract. By default Phase 14 does **not** add policy-write to `external/v1`; if a
  write/management surface is desired, it is separately authorized within this ADR (and is a distinct
  concern from the read Contract).
- `Governance.Evaluate` keeps its **stateless evaluator contract** — it consumes policies from the
  repository instead of from nothing; the evaluation *logic* is unchanged.

**MUST (B) — Phase 14 B iron laws.**
- **B-1 — Policy Ownership does not leak into other frozen capabilities.** Policy is a Governance-owned
  persisted entity; it must NOT be injected into `cluster`/`observability`/`enterprise`/`platformview`
  ownership, and must NOT copy or duplicate their models.
- **B-2 — Evaluate contract unchanged.** `Governance.Evaluate` stays a pure stateless function over its
  inputs; only the *source* of those inputs changes (repository vs. none). No behavioral change to
  evaluation semantics.
- **B-3 — No write path into `external/v1`.** The Public Contract stays read-only. Any policy
  management/write surface is a separate, explicitly-authorized sub-concern (not smuggled into the read
  Contract).
- **B-4 — Persistence is a new storage surface, governed by AST Guard + TestNoExecMethod.** The
  repository has no execution path into the Runtime Core; it is data ownership, not capability logic.
- **B-5 — No re-opening of R51/Phase-8 ownership.** Policy persistence does not alter the
  HostRegistry→Host→HostRef or Cluster→HostRef relationships.
- **B-6 — Repository is the sole persistence owner of Policy (GPT R64 upgrade).** The new
  `PolicyRepository` owns Policy persistence. `governance.Engine` owns **no** database, file, cache,
  or repository — it stays a pure evaluator. Forbidden shape: `Engine ── db/store/repository`.
- **B-7 — Policy Lifecycle is separated from Evaluation (GPT R64 upgrade).** The repository owns
  Create/Read/Update/Delete/Version/Activate/Deactivate (lifecycle). `Evaluate(policy, state) →
  Verdict` only accepts an already-determined Policy and State. Forbidden shape:
  `Evaluate ── Load policy ── mutate policy ── activate policy`.
- **B-8 — Policy Version / Revision must be explicit (GPT R64 upgrade).** Persistence introduces a
  stable `PolicyID` + `PolicyRevision`/`Version`. `PolicyID` stays the **existing** Policy identity
  (reused, not reinvented); Revision is a version *attribute* of it — not a new "Global Policy
  Identity". Audit/Verdict traceability references `PolicyID + Revision`, never a freshly-minted
  `EvaluationPolicyID`.
- **B-9 — Persistence forms no execution bridge (GPT R64 upgrade).** Policy management writes must
  NOT trigger Execute / Run / Apply / Schedule / Dispatch / Rollback. Correct boundary:
  `Policy Management → Persist Policy → Governance Evaluate → Verdict → [END]`. Runtime remains the
  sole execution owner.

**external/v1 is NOT modified in Phase 14 (GPT R64 explicit verdict).** It stays `READ ONLY`. Even if
a Policy management write surface is eventually needed, it is a **separate, future Management API
Major Evolution** — never smuggled into the read Contract, and never into Phase 14's scope.

**SHOULD (B).**
- B-S1 Repository is behind an interface; store choice (file/sqlite/remote) is swappable without
  touching the evaluator.
- B-S2 Policy lifecycle is versioned and auditable (archive, not hard-delete).
- B-S3 Reads from the repository are deterministic and consistent with the existing `external/v1`
  Governance *read* DTO shapes (which remain honest-empty or real, never fabricated).

**Out of scope (B).** No change to `Evaluate` semantics; no write to `external/v1` unless separately
authorized; no multi-node replication of policy store; no new capability beyond policy persistence +
lifecycle; no frozen-package ownership change beyond Governance's own new persisted entity.

---

### 3.3 Direction C — Documentation & Reference Deployment (lowest risk, fallback)

**Goal.** Consolidate the now-frozen eight-layer system (through Phase 13) into a reference set:
Architecture Reference, Deployment Reference, API Consumer Guide, Extension Rules, ADR index.

**Shape.** Docs only — no code change, no new contract. Lowest-risk route to a complete "shippable
product narrative".

**MUST (C).**
- C-1 Documentation reflects the frozen system exactly; introduces no normative change.
- C-2 No code, no new execution entry, no contract change.

**Out of scope (C).** No implementation, no new capability, no API change.

---

## 4. Out of scope — explicitly forbidden for Phase 14

- Any change that breaks a frozen ownership boundary (R51), Runtime Contract, or Plugin Ecosystem
  contract (MUST-0/1/2).
- Modifying the signed implementations of Phase 8 capabilities beyond the explicitly-authorized additions
  scoped above (MUST-3).
- Turning a productionization / policy-persistence concern into a Control Plane or a new execution path
  (MUST-0/5).
- A write/mutate Public API (that is a distinct, separately-authorized concern, not the default Phase 14
  scope).
- Multi-node consensus / replication engine (both directions ship single-node; policy store is
  single-node).
- Undoing the Phase 12 composition root.

---

## 5. Decision requested (Round 64)

Per GPT (R63) recommendation, **do not code yet** — the next step is this Scope ADR, then an
architecture ADR, then implementation. GPT recommended **Direction B (Governance Policy Persistence)** as
the highest-value next Major Evolution, with A (Deployment Productionization) as the lower-risk
alternative. Please sign off this Scope ADR and select **one** direction for Phase 14:

- **(A) Deployment Productionization** — harden Phase 12 for single-node production (config, systemd,
  health, logging, graceful shutdown). Lowest architectural risk.
- **(B) Governance Policy Persistence** — give Governance a real policy source + lifecycle (Policy →
  Repository → Lifecycle → Evaluate). *(GPT recommended — highest architectural value; genuinely new
  data ownership, so higher risk and must stay strictly separated from the read-only `external/v1`.)*
- **(C) Documentation & Reference Deployment** — consolidate the frozen system into reference docs
  (lowest risk).
- **(Other)** — a direction you specify.

On selection, the next round submits the chosen direction's **architecture ADR** (e.g. ADR-030 for the
chosen direction) — **no implementation until that is signed.**

---

## 6. Phase 14 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 14.0 | Phase 14 Next Major Evolution Scope (this ADR-029) | **signed PASS R64 — Direction B selected** |
| 14.1 | Chosen-direction Architecture ADR (ADR-030) | proposed — after 14.0 sign-off (R65) |
| 14.2 | Chosen-direction Implementation | proposed — after 14.1 sign-off (R66) |

Each step is authorized only after the previous is signed — same architecture-first discipline.

---

## 7. Sign-off placeholder (Round 64)

| Item | Verdict | Round |
|---|---|---|
| MUST-0 No frozen-ownership / Runtime-Contract break, no Control Plane / new execution path | ✅ PASS | 64 |
| MUST-1 Runtime Contract remains frozen | ✅ PASS | 64 |
| MUST-2 Plugin Ecosystem remains frozen | ✅ PASS | 64 |
| MUST-3 Phase 8 capabilities remain closed (read-only observation; separately-authorized additions scoped) | ✅ PASS | 64 |
| MUST-4 platformview/correlation/external/clusterprojection remain the only read sources | ✅ PASS | 64 |
| MUST-5 Deployment layer (Phase 12) remains the deployment surface | ✅ PASS | 64 |
| MUST-6 Architecture First (architecture ADR signed before code) | ✅ PASS | 64 |
| Direction selected: **B — Governance Policy Persistence** (MUST-B1~B9, external/v1 unmodified) | ✅ PASS | 64 |

*Phase 14.0 Next Major Evolution Scope — the next Major Evolution beyond the frozen seven-layer +
deployment + Cluster-read-surface baseline; no implementation until the chosen direction's architecture
ADR (ADR-030) is signed. (Draft, Round 64).*
