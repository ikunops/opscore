# ADR-027 — Phase 13.0: Post-Deployment Evolution Scope

- **Status**: Accepted (Round 61) — signed PASS; Phase 13 direction = **A (Read Surface Completeness)**; Governance policy storage explicitly **excluded** (future independent Major Evolution)
- **Date**: 2026-08-08
- **Companion to**: ADR-026 (Phase 12, CLOSED), ADR-025 (Phase 12.0, CLOSED), ADR-024 (Phase 11,
  CLOSED), ADR-022 (Phase 10, CLOSED), ADR-021 (Architecture Baseline, frozen), ADR-014~020 (Phases
  8~9, CLOSED), ADR-010/011/012/013 (the four frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 13 — Post-Deployment Evolution (the next Major Evolution beyond the frozen
  six-tier + deployment baseline)

---

## 0. Abstract

Phase 12 Deployment & Distribution is **CLOSED** (ADR-026, Round 60): the frozen six-tier system now
wires into a single runnable `opscore` process via `internal/harness` + `cmd/opscore-server`, mounting
`external/v1`. The architecture is complete, runnable, and frozen end-to-end:

```
Runtime Core → Plugin Ecosystem → Platform Operations → Platform Integration
            → Event Correlation → External Interface → Deployment & Distribution
```

Phase 12 also **honestly surfaced** two read-surface gaps (R60 verdict, GPT): `external/v1` currently
projects empty views for **Cluster** (ClusterID-oriented, no host-centric Reader API) and **Governance**
(stateless evaluator, no policy repository). Per GPT (R60): *"这不构成实现失败，但应明确：Phase 12 CLOSED
不等于声明 Cluster/Governance 已经具备完整外部数据投影。"* and *"若未来要求完整数据，需要分别走新的架构演进。"*

Phase 13 is the **next Major Evolution** beyond the deployment layer. Per ADR-021, any addition that
changes an external read surface, a deployment productionization contract, or (for Direction A) the
frozen capability data-ownership / model, is a **Major Evolution** — it requires this Scope ADR, then an
Architecture ADR, then implementation. **Phase 13 writes no implementation until the chosen
direction's architecture ADR is signed off.**

Per GPT (R60), the recommended first step is *not* to code, but to scope. This ADR presents three
candidate directions and requests a direction selection.

---

## 1. Phase 13 positioning — evolution beyond the frozen baseline, not a re-open of closed phases

| Phase | Nature | Adds |
|---|---|---|
| 8 | Platform Operations axis (CLOSED) | Observability / Cluster / Enterprise / Governance |
| 9 | Platform Integration (CLOSED) | `internal/platformview` read-only facade |
| 10 | Event Correlation (CLOSED) | `internal/correlation` cross-capability projection |
| 11 | External Interface (CLOSED) | `external/v1` read-only Public Contract |
| 12 | Deployment & Distribution (CLOSED) | composition root + server harness + single-node runnable process |
| **13** | **Post-Deployment Evolution** | closes read-surface gaps OR productionizes deployment OR documents the frozen system (choose one) |

Phase 13 sits *beyond* the frozen tiers. It must obey every existing freeze boundary (ADR-021) **plus
one new hard boundary**:

> **Phase 13 Boundary (MUST-0):** Phase 13 introduces **no change that breaks an existing frozen
> ownership boundary (R51 freeze), Runtime Contract, or Plugin Ecosystem contract**, and **does not
> turn a read-only concern or a productionization concern into a Control Plane or a new execution path
> into the Runtime Core**. Full stop. If a candidate direction requires modifying a frozen capability's
> *data ownership* or *domain model*, that must be surfaced in this Scope ADR as an explicit,
> separately-authorized change — never smuggled in under a "read surface" or "deployment" label.

---

## 2. Freeze boundaries (inherited + new)

- **MUST-1 — Runtime Contract remains frozen.** ADR-010/011/012 unchanged.
- **MUST-2 — Plugin Ecosystem remains frozen.** ADR-013 unchanged.
- **MUST-3 — Phase 8 capabilities remain closed.** `internal/observability`, `internal/cluster`,
  `internal/enterprise`, `internal/governance` are implemented and signed. Phase 13 may *observe* them
  read-only; **any addition to their data model / ownership requires explicit separate authorization
  within this ADR** (Direction A only).
- **MUST-4 — Platform Integration / Correlation / External remain closed.** `internal/platformview`,
  `internal/correlation`, `internal/external` are the only sanctioned read sources.
- **MUST-5 — Deployment layer (Phase 12) remains the deployment surface.** If Phase 13 extends
  deployment (Direction B), it must **stay within the Deployment-layer nature** defined in ADR-026
  (assembles, never becomes a capability or Control Plane); it must not undo the Phase 12 composition
  root or introduce execution.
- **MUST-6 — Architecture First.** The chosen direction's architecture ADR is signed before code.
- **MUST-0 (new, hard) — No frozen-ownership / Runtime-Contract break, no Control Plane / new
  execution path** (§1).

---

## 3. Candidate directions (choose one)

Phase 13 proposes three candidate directions. Each is a *scope sketch* only — detail lands in a
follow-up architecture ADR once GPT selects.

### 3.1 Direction A — Read Surface Completeness — **selected for Phase 13 (Round 61)**

**Goal.** Close the **Cluster** read-surface gap Phase 12 honestly surfaced: give `external/v1` a real
host-centric projection for **Cluster**, so the External Interface is no longer intentionally empty for
this surface. *(The Governance gap is **not** part of Phase 13 — see the split below.)*

**Scope split (GPT, Round 61).** Direction A decomposes into:
- **13.1 Cluster host-centric read projection** — ✅ **in scope for Phase 13**
- **Governance policy storage** — ❌ **excluded from Phase 13**. It changes storage ownership →
  persistence → lifecycle → mutation/versioning → governance evaluation input — i.e. not a mere read
  projection. It is deferred to a **future independent Major Evolution** (Scope ADR → Architecture ADR →
  Implementation), and must **not** ride in under "fill the external/v1 empty view".

**Shape (proposed).** Provide a read-only, host-centric projection on `internal/cluster` **without
breaking its ownership boundary**:
- Add a read-only `ClusterReader` projection (ClusterID scope → host view) that exposes *existing*
  Cluster-owned state — not a new capability, not a copy of the Host Registry. Per GPT (R60): *"可在不改语义下给
  cluster 加 read-only 成员查询（ADR-026 §3 授权）"*.

**MUST (A) — Phase 13 A iron laws (GPT Round 61, A1–A6).**
- **A1 — Read Projection Only.** Cluster's new capability is a *host-centric Read Model*, not a new
  Cluster inventory. Allowed: `Cluster → Members/Placement → HostRef → Read Projection`. Forbidden: copy
  Host, own Host lifecycle, own SSH/OS/CPU/Memory, duplicate HostRegistry.
- **A2 — Host Ownership unchanged.** Cluster may *reference* `HostRef` but never *own* Host. Final
  relationship must stay `HostRegistry ──owns──▶ Host ──▶ HostRef`; `Cluster ──references──▶ HostRef`.
  Never `Cluster ──owns──▶ Host{OS,CPU,Memory,SSH}`.
- **A3 — Placement semantics unchanged.** `Placement.Targets []HostRef`, `Placement.Reason`,
  `Placement.Version` remain the Cluster fact source. Phase 13 only projects existing Cluster-owned info
  into a host-centric read model; does **not** redefine the placement algorithm.
- **A4 — No execution path via the read surface.** Mechanically forbid `Run/Exec/Execute/Command/Apply/
  Schedule/Dispatch` and any Runtime Executor / SSH / lifecycle API. Keep AST Guard + TestNoExecMethod
  (not code-review-only).
- **A5 — External Contract does not read Cluster directly.** Keep the chain
  `Cluster → Cluster Reader/Projection → platformview → external/v1`. Never `external → cluster internals`.
  Preserves the R57/R60 frozen External ownership boundary.
- **A6 — Projection completeness is honest.** Fields not present in the frozen model return empty /
  omitted / unavailable — never derived or fabricated. Do **not** re-read OS/CPU/SSH HostRuntime just to
  fill HostView.

**SHOULD (A).**
- A-S1 Projections are consistent with the existing `external/v1` DTO shapes (no contract divergence).
- A-S2 Honest-empty behavior is removed only where real data now exists; surfaces without data remain
  explicit-empty (never fabricated).

**Out of scope (A).** No new capability, no write/mutate API, no change to observability/enterprise
(real-delegated) projections, no Governance *evaluation* or *policy storage* change. Governance policy
storage is deferred to a future independent Major Evolution.

---

### 3.2 Direction B — Deployment Productionization

**Goal.** Harden the Phase 12 scaffold for real single-node production: config files,
systemd/service packaging, graceful shutdown, health/readiness probes, logging/observability wiring.

**Shape.** Add operational artifacts around the existing `internal/harness` + `cmd/opscore-server`.
**Deployment layer only** — must not evolve into a Control Plane.

**MUST (B).**
- B-1 Productionization stays within the Phase 12 Deployment-layer nature (assembles, mounts, lifecycle).
- B-2 No new execution entry into the Runtime Core; no capability-semantics mutation.
- B-3 Health/readiness probes read only the frozen read models (no capability mutation).
- B-4 Graceful shutdown preserves Phase 12 idempotency guarantees (SHOULD-8).

**SHOULD (B).**
- B-S1 Config files are version-stamped and validated against `HarnessConfig` on load.
- B-S2 systemd/container unit declares single-node topology (multi-node seam remains inert).

**Out of scope (B).** No multi-node consensus/replication, no new capability, no Public API change.

---

### 3.3 Direction C — Documentation & Reference Deployment — **lowest risk**

**Goal.** Consolidate the now-frozen seven-layer system into a reference set: Architecture Reference,
Deployment Reference, API Consumer Guide, Extension Rules, ADR index.

**Shape.** Docs only — no code change, no new contract. Lowest-risk route to "shippable product
narrative".

**MUST (C).**
- C-1 Documentation reflects the frozen system exactly; introduces no normative change.
- C-2 No code, no new execution entry, no contract change.

**SHOULD (C).**
- C-S1 The ADR index records the full authorization chain (Phase 8 → 13) and the frozen-state manifest.

**Out of scope (C).** No implementation, no new capability, no API change.

---

## 4. Out of scope — explicitly forbidden for Phase 13

- Any change that breaks a frozen ownership boundary (R51), Runtime Contract, or Plugin Ecosystem
  contract (MUST-0/1/2).
- Modifying the signed implementations of Phase 8 capabilities beyond the read-only additions scoped
  above (MUST-3).
- Turning a read-only / productionization concern into a Control Plane or a new execution path
  (MUST-0/5).
- A write/mutate Public API (that is a distinct Major Evolution, not Phase 13).
- Multi-node consensus / replication engine (Direction B ships single-node only).
- Undoing the Phase 12 composition root.

---

## 5. Decision requested (Round 61)

Per GPT (Round 60) recommendation, **do not code yet** — the next step is this Scope ADR, then an
architecture ADR, then implementation. GPT prioritized **Direction A (Read Surface Completeness)** and
**Direction B (Deployment Productionization)** as the most valuable next Major Evolutions. Please sign
off this Scope ADR and select **one** direction for Phase 13:

- **(A) Read Surface Completeness** — close the Cluster/Governance empty-projection gaps honestly and
  read-only. *(recommended — highest architectural value; note Governance storage sub-part is a
  separately-authorized Major Evolution)*
- **(B) Deployment Productionization** — harden Phase 12 for single-node production (config, systemd,
  health, logging).
- **(C) Documentation & Reference Deployment** — consolidate the frozen system into reference docs
  (lowest risk).
- **(Other)** — a direction you specify.

On selection, the next round submits the chosen direction's **architecture ADR** (e.g. ADR-028 for A)
— **no implementation until that is signed.**

---

## 6. Phase 13 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 13.0 | Phase 13 Post-Deployment Evolution Scope (this ADR-027) | **Accepted R61 (signed PASS; Direction A)** |
| 13.1 | Chosen-direction Architecture ADR (ADR-028) | proposed — after 13.0 sign-off (R62) |
| 13.2 | Chosen-direction Implementation | proposed — after 13.1 sign-off (R63) |

Each step is authorized only after the previous is signed — same architecture-first discipline.

---

## 7. Sign-off placeholder (Round 61)

| Item | Verdict | Round |
|---|---|---|
| MUST-0 No frozen-ownership / Runtime-Contract break, no Control Plane / new execution path | ✅ PASS | 61 |
| MUST-1 Runtime Contract remains frozen | ✅ PASS | 61 |
| MUST-2 Plugin Ecosystem remains frozen | ✅ PASS | 61 |
| MUST-3 Phase 8 capabilities remain closed (Cluster read-only projection scoped; Governance storage excluded) | ✅ PASS | 61 |
| MUST-4 platformview/correlation/external remain the only read sources | ✅ PASS | 61 |
| MUST-5 Deployment layer (Phase 12) remains the deployment surface | ✅ PASS | 61 |
| MUST-6 Architecture First (architecture ADR signed before code) | ✅ PASS | 61 |
| Direction selected: **A — Read Surface Completeness** (Cluster host-centric projection; Governance policy storage excluded) | ✅ PASS | 61 |

*Phase 13.0 Post-Deployment Evolution Scope — **Accepted (Round 61, signed PASS)**. Phase 13 direction =
**A (Read Surface Completeness)**, scoped to Cluster host-centric read projection only; Governance policy
storage deferred to a future independent Major Evolution. Next: ADR-028 (13.1 Architecture), no
implementation until signed.*
