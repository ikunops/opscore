# ADR-034 — Phase 16.1: Documentation & Reference Deployment — Architecture

- **Status**: Accepted (Round 71, signed PASS) — **Direction B: Documentation & Reference Deployment
  (Phase 16.0 Scope ADR-033 signed PASS R70 — Phase 16.0 CLOSED), Phase 16.1 Architecture CLOSED 🔒**
- **Date**: 2026-08-09
- **Companion to**: ADR-033 (Phase 16.0 Scope, **Accepted R70 — Phase 16.0 CLOSED**), ADR-032
  (Phase 15.1, CLOSED), ADR-031 (Phase 15.0, CLOSED), ADR-030 (Phase 14.1, CLOSED), ADR-029 (Phase
  14.0, CLOSED), ADR-028/027 (Phase 13, CLOSED), ADR-026 (Phase 12, CLOSED), ADR-024 (Phase 11.1,
  CLOSED), ADR-022 (Phase 10, CLOSED), ADR-021 (Architecture Baseline, frozen), ADR-014~020 (Phases
  8~9, CLOSED), ADR-010/011/012/013 (the four frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 16 — give the frozen 8-layer architecture a **documented, referenceable,
  drift-checked information architecture** — **no runtime code change**.

---

## 0. Abstract

ADR-033 (R70) signed **PASS** and selected **Direction B — Documentation & Reference Deployment**. GPT
(R70) confirmed the freeze boundaries hold and authorized this Architecture ADR (ADR-034), explicitly
listing five non-blocking SHOULD recommendations that Phase 16.1 must realize (see §3).

This ADR is the **architecture** for Direction B. Crucially, it is the **information architecture of the
documentation set** — *not* a code architecture. Phase 16.1 only:

- defines a **Single Source of Truth (SSOT) mapping** so README / architecture.md / deployment.md never
  drift from each other;
- specifies **versioned External read-contract documentation** (`external/v1`: version, read-only nature,
  breaking-change rules);
- requires **deployment reproducibility** — the Reference Deployment reproduces the Phase 15 single-node
  deployment, inventing no second architecture;
- produces an **Architecture Boundary Matrix** (layer → owner → allowed dep → forbidden dep → public
  surface) as the baseline for future Major Evolution;
- defines **drift-detection** as a *documentation/CI-only* mechanical check — **no new runtime
  capability** is introduced.

No code path, no new dependency, no `external/v1` change. Phase 16.2 (after this is signed) authors the
docs; this ADR only structures them.

---

## 1. Phase 16.1 positioning — information architecture around the frozen surface

Phase 16.1 adds **no runtime behavior and no data ownership**. It is *orthogonal* to every frozen tier —
it describes them. The deliverable is a coherent doc set + a boundary matrix + a drift check, all
descriptive/packaging in nature.

| Artifact | Source of truth it documents | Nature |
|---|---|---|
| `docs/architecture.md` | 8-layer closed loop (ADR-021 + `internal/...` packages) | descriptive |
| `docs/deployment.md` | `cmd/opscore-server` + `internal/harness` + `deploy/` (Phase 15) | descriptive / packaging |
| `docs/policy-lifecycle.md` | `internal/governancepolicy` (Phase 14) | descriptive |
| `docs/external-contract.md` | `external/v1` (Phase 11, frozen) | descriptive, versioned |
| `docs/operations.md` | evolution charter (ADR-021 §6) + iron laws + guard discipline | descriptive |
| `deploy/README` (+ upgrade note) | existing `deploy/` artifacts (Phase 15) | packaging reference |
| top-level `README` / index | all of the above | navigation |

None of these modify the code they describe. If authoring discovers a code/intent mismatch, it is recorded
as *fact / diff / future-Major-Evolution candidate* — never silently patched to make the doc "look nice".

---

## 2. Freeze boundaries (inherited + R70 reaffirmed)

### 2.1 Inherited (from ADR-033)

- **MUST-1 — Runtime Contract frozen** (ADR-010/011/012).
- **MUST-2 — Plugin Ecosystem frozen** (ADR-013).
- **MUST-3 — Phase 8 capabilities closed**; Phase 16.1 describes them read-only.
- **MUST-4 — Read / persistence sources unchanged**; Phase 16.1 does not modify them.
- **MUST-5 — Deployment layer (Phase 12) + Phase 15 hardening remain the deployment surface**.
- **MUST-6 — Architecture First**; no doc artifact until this ADR is signed (R72).
- **P15-0 (hard)** — no frozen-semantics break, no Runtime-Contract break, no new execution entry, no
  Control Plane / orchestration.
- **P16-0 (hard)** — no runtime-behavior change; deliverables are docs + reference deployment artifacts.
- **P16-1~P16-6** — descriptive/packaging nature, no new execution entry, no write/control API, frozen
  packages only described, Architecture First, zero new dependency.

### 2.2 R70 reaffirmed hard boundaries (must hold in Phase 16.1/16.2)

| ID | Law | How the architecture satisfies it |
|---|---|---|
| R70-B1 | Reference Deployment must **NOT** re-implement Deployment | `deploy/` only *describes, packages, demonstrates* the existing `opscore-server` deployment. No new startup path, sidecar orchestration, HA, leader election, or management controller is introduced. |
| R70-B2 | Documentation must **NOT** become a covert architecture-evolution entry | A doc/code mismatch is recorded as fact/diff/future-Major candidate, never silently fixed in frozen code. No "documentation-driven" capability expansion. |
| R70-A | Direction A (Management API) stays a separate Major | No `docs → reference deployment → management endpoint → Policy mutation` implicit chain. Policy write/control is explicitly out of scope. |

---

## 3. Documentation architecture (the five R70 SHOULD, realized)

### 3.1 SHOULD-1 — Documentation SSOT (Single Source of Truth)

Every key fact names exactly one authoritative source. The matrix below is the contract for Phase 16.2
authoring — if two docs would state the same fact, one links to the other's source, it does not
re-state it.

| Fact | Authoritative source (SSOT) | Documented in |
|---|---|---|
| 8-layer topology & ownership | `internal/...` packages + ADR-021 | `docs/architecture.md` |
| Deployment model / config schema | `cmd/opscore-server` + `internal/harness` + `HarnessConfig` | `docs/deployment.md` |
| Policy ownership / lifecycle states | `internal/governancepolicy` | `docs/policy-lifecycle.md` |
| External read contract (version, fields, stability) | `external/v1` (Phase 11, frozen) | `docs/external-contract.md` |
| Evolution charter / iron laws / guard discipline | ADR-021 §6 + ADR-033 | `docs/operations.md` |
| Service unit / container / upgrade | `deploy/` (Phase 15) | `deploy/README` + `docs/deployment.md` |

### 3.2 SHOULD-2 — Versioned Contract Documentation

`docs/external-contract.md` must state, and stay consistent with, the actual `external/v1` contract:

- **Contract version** (e.g., `v1`) stated explicitly; docs carry the same version string.
- **Read-only nature** asserted (no mutate endpoint exists or is permitted).
- **Breaking-change rules**: read-only additions are non-breaking; any field removal/semantics change
  requires a new contract version + a Scope ADR.
- A note that `external/v1` is frozen (ADR-011/012) — the doc describes, it does not extend.

### 3.3 SHOULD-3 — Deployment Reproducibility

The Reference Deployment (`deploy/` + `docs/deployment.md`) must let an operator reproduce the Phase 15
single-node production runtime from the documented steps:

- Start `opscore-server` with the versioned config (`deploy/opscore.json.example`).
- Run under the systemd unit (`deploy/opscore.service`) or the distroless container (`deploy/Dockerfile`).
- Health/readiness via `/healthz` `/readyz` `/versionz`; structured logging via slog.
- In-place single-node upgrade (config migration additive/validated; no multi-node replication).

No second deployment architecture, no HA/sidecar/orchestration variant, is introduced or implied.

### 3.4 SHOULD-4 — Architecture Boundary Matrix

The baseline for future Major Evolution. `layer → owner → allowed dependency → forbidden dependency →
public surface`:

| Layer | Owner package | Allowed dependency | Forbidden dependency | Public surface |
|---|---|---|---|---|
| Runtime Core | `core` (frozen) | Plugin Ecosystem | orchestration, external write | internal only |
| Plugin Ecosystem | `internal/plugin/*` | Runtime Core | control plane | runtime |
| Platform Operations | `internal/observability` `cluster` `enterprise` `governance` | read models | execution verbs | read-only |
| Platform Integration | `internal/platformview` | frozen read models | mutation | read facade |
| Event Correlation | `internal/correlation` | cross-capability read | write path | read |
| External Interface | `external/v1` | frozen read models | write/control endpoint | read-only HTTP |
| Deployment & Distribution | `internal/harness` + `cmd/opscore-server` | assemble / lifecycle | new exec entry | server + probe |
| Cluster Read Surface | `internal/clusterprojection` | read | mutation | read |
| Governance Policy Persistence | `internal/governancepolicy` | persist/read policy | `Evaluate` call from ops/doc layer | read via `external/v1` |

### 3.5 SHOULD-5 — Drift Detection (documentation / CI only, no runtime capability)

A *non-runtime* mechanical check that prevents doc/code drift. It is a documentation/CI concern, **not**
a new server feature:

- **Contract version check**: `docs/external-contract.md` version string matches the version exposed by
  `external/v1` (read from the frozen source, not re-implemented).
- **Deployment entrypoint check**: `docs/deployment.md` / `deploy/README` reference only
  `cmd/opscore-server` as the composition root (no second entrypoint named).
- **Layer-name check**: layer names in `docs/architecture.md` match ADR-021 + the actual package layout.

These checks may be run by a CI lint or a one-off script; **they introduce no new Go module, no new
runtime library, and no new server endpoint.** If a check needs tooling, it reuses existing Go test /
`go doc` / static inspection — it never adds a production code path.

---

## 4. Documentation deliverables (Phase 16.2 authoring scope)

Concretely, Phase 16.2 (after this ADR is signed) authors:

1. `docs/architecture.md` — 8-layer narrative + diagram, per §3.1/§3.4.
2. `docs/deployment.md` — single-node production baseline, per §3.3.
3. `docs/policy-lifecycle.md` — Policy ownership/lifecycle, read-only exposure only.
4. `docs/external-contract.md` — versioned read-only contract, per §3.2.
5. `docs/operations.md` — evolution charter, iron laws, guard discipline, relay/sign-off process.
6. `deploy/README` (+ upgrade note) — packaging reference for the existing artifacts.
7. top-level `README` / index — navigation + frozen-status + evolution rules statement.

All descriptive/packaging only; zero runtime behavior.

---

## 5. Out of scope / forbidden (reaffirmed from ADR-033)

- No Management API / Policy write/control boundary (Direction A — separate Major).
- No multi-node consensus / replication; remains single-node.
- No HA Control Plane, no orchestration layer, no sidecar.
- No new capability, no Public API change, no frozen-package modification.
- No Runtime Contract change (ADR-010/011/012).
- No change to `Governance.Evaluate` semantics or `internal/governancepolicy` ownership.
- No write path into `external/v1`.
- No new runtime dependency introduced for documentation/drift-detection purposes.

---

## 6. Phase 16 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 16.0 | Phase 16 Documentation & Reference Deployment Scope (ADR-033) | **signed PASS R70 — Phase 16.0 CLOSED** |
| 16.1 | Phase 16 Documentation & Reference Deployment Architecture (this ADR-034) | **signed PASS R71 — Phase 16.1 CLOSED** |
| 16.2 | Phase 16 Documentation & Reference Deployment Authoring | proposed — after 16.1 sign-off (R72) |

Each step is authorized only after the previous is signed — same architecture-first discipline.

---

## 7. Sign-off placeholder (Round 71)

| Item | Verdict | Round |
|---|---|---|
| P16-0 No runtime-behavior change, no new execution entry, no Control Plane / orchestration | ✅ PASS | 71 |
| MUST-1~6 Inherited freeze boundaries hold | ✅ PASS | 71 |
| P16-1~P16-6 Documentation stays descriptive/packaging, no write/control API, zero new dependency | ✅ PASS | 71 |
| R70-B1 Reference Deployment does NOT re-implement Deployment | ✅ PASS | 71 |
| R70-B2 Documentation does NOT become a covert architecture-evolution entry | ✅ PASS | 71 |
| R70-A Direction A (Management API) stays a separate Major | ✅ PASS | 71 |
| SHOULD-1 SSOT mapping defined (no cross-doc drift) | ✅ PASS | 71 |
| SHOULD-2 Versioned External read-contract documentation | ✅ PASS | 71 |
| SHOULD-3 Deployment Reproducibility (no second architecture) | ✅ PASS | 71 |
| SHOULD-4 Architecture Boundary Matrix produced | ✅ PASS | 71 |
| SHOULD-5 Drift Detection is documentation/CI-only, no runtime capability | ✅ PASS | 71 |

*Phase 16.1 Documentation & Reference Deployment Architecture — information architecture for the doc set
(SSOT, versioned contract, reproducibility, boundary matrix, drift detection); no runtime code change,
no new dependency, no `external/v1` change. No documentation artifact until ADR-034 is signed.
**Phase 16.1 CLOSED (Round 71).***

---

## 8. R71 additional guidance (for Phase 16.2 authoring, non-blocking)

GPT (R71) confirmed all freeze boundaries and the five SHOULD. Key execution-time rules for Phase 16.2:
- **Drift handling**: if a *code fact* ≠ *ADR/doc description*, **do NOT modify frozen code to make the doc pass**. Record it as `Fact → Drift → Impact → Future Major Candidate` and let a new Scope ADR decide evolution. Especially applies to the Boundary Matrix and `external/v1` docs.
- **Documentation must not invent endpoints**: `docs/external-contract.md` may only describe the *existing* `external/v1`; it must never describe a non-existent endpoint that makes the Public Contract look implemented.
- **Drift Detection stays doc/CI-only**: no new Go module, no new runtime library, no new endpoint, no new server; the Documentation Phase must not become a tooling/runtime Phase.
- **Boundary Matrix is the review baseline**: keep it; it becomes the baseline every future Major Evolution Scope ADR is reviewed against.
