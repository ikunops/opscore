# ADR-035 — Phase 17.0: Management API / Policy Management Scope

- **Status**: Accepted (Round 73: **B — accept with modifications**; required boundary strengthening P17-2→MUST, P17-8/9/10 incorporated — per GPT instruction "modify ADR-035 then proceed to R74/ADR-036") — **Direction A: Management API / Policy Management selected — Phase 17.0 CLOSED (with modifications)**
- **Date**: 2026-08-09
- **Companion to**: ADR-033 (Phase 16.0, CLOSED), ADR-034 (Phase 16.1, CLOSED),
  ADR-032 (Phase 15.1, CLOSED), ADR-031 (Phase 15.0, CLOSED), ADR-021 (Architecture Baseline,
  frozen / Evolution Charter), ADR-014~020 (Phases 8~9, CLOSED), ADR-010/011/012/013 (the four
  frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 17 — introduce the first deliberate write/control boundary (Policy Management)
  as a surface SEPARATE from the frozen `external/v1` read-only Public Contract.

---

## 0. Abstract

**Phase 16 Documentation & Reference Deployment is CLOSED** (ADR-033 + ADR-034 signed PASS; R70/R71/R72;
R72 landing gate-green, commit `bf27fa8`, not pushed). The frozen system is now fully documented and
reference-deployable as a single-node production runtime with a read-only Public Contract.

GPT (R69, reaffirmed R72) offered two candidate directions for the next Major:
- **A — Management API / Policy Management** (new write/control boundary; must be its own separate Scope ADR and must NOT quietly extend `external/v1`).
- **B — Documentation & Reference Deployment** (stabilize & document the frozen surface).

Direction B was completed in Phase 16. Per the user decision in this round: **Direction A — Management
API / Policy Management is selected for Phase 17.** This is the first intentional write path in the
system; it therefore MUST be scoped before any code is written, under the ADR-021 three-tier
(Scope → Architecture → Implementation) discipline.

**Phase 17 core thesis:** introduce a dedicated, explicitly-gated **write/control surface** for Policy
management (create / modify / activate / deactivate / archive), mounted *separately* from the
frozen read-only `external/v1` contract, reusing the existing `governancepolicy.Repository` (file-backed)
for persistence. It is a new write boundary — but it is scoped, not smuggled in.

---

## 1. Phase 17 positioning — a NEW write/control boundary, separate from the frozen read contract

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
| 16 | Documentation & Reference Deployment (CLOSED) | docs + reference deployment artifacts (no runtime change) |
| **17** | **Management API / Policy Management** | a **new write/control surface** for Policy lifecycle, separate from `external/v1` |

Phase 17 is the deliberate counterpart to Phase 11/14: those established the **read** exposure and the
**persistence** of Policy; Phase 17 establishes the **write/control** exposure over that same
persistence, on its own surface, without touching the read contract.

> **Phase 17 Boundary (P17-0):** The frozen `external/v1` read-only Public Contract is **UNCHANGED**.
> No POST/PUT/DELETE is added to it; no new read path is added either. The Management API is a
> **distinct** HTTP surface (separate bind/port or distinct URL namespace such as `/management/v1`),
> so the public read contract stays pure. This is the single most important invariant of Phase 17.

---

## 2. Freeze boundaries (inherited + new)

- **MUST-1 — Runtime Contract remains frozen.** ADR-010/011/012 unchanged. No change to plugin runtime,
  isolation, controlplane, or harness lifecycle semantics.
- **MUST-2 — Plugin Ecosystem remains frozen.** ADR-013 unchanged.
- **MUST-3 — Phase 8 capabilities remain closed.** `internal/observability`, `internal/cluster`,
  `internal/enterprise`, `internal/governance` implemented and signed. Phase 17 does not add to their
  data model or ownership.
- **MUST-4 — Read/persistence sources remain the ones sanctioned.** `internal/platformview`,
  `internal/correlation`, `internal/external`, `internal/clusterprojection`, `internal/governancepolicy`
  are NOT modified. The Management API **consumes** `governancepolicy.Repository` through its existing
  public interface (same as the read surface does), it does not alter the package.
- **MUST-5 — Deployment layer (Phase 12) + Phase 15 hardening remain the deployment surface.** The
  Management API is wired in the **same unique Composition Root** (`cmd/opscore-server`); no second
  main/entrypoint is introduced (consistent with A-1).
- **MUST-6 — Architecture First.** The Phase 17.1 architecture ADR (ADR-036) is signed before any
  handler is implemented.
- **P16-0 (inherited)** — no frozen-semantics break, no Runtime-Contract break, no new execution entry
  into the Runtime Core, no Control Plane / orchestration.
- **P17-0 (new, hard)** — §1: `external/v1` read-only Public Contract unchanged; Management API is a
  separate surface.

---

## 3. Phase 17 scope — Management API / Policy Management (Direction A)

**Goal.** Provide operators a controlled, explicit way to create, modify, activate, deactivate, and
archive governance Policies without hand-editing files in `PolicyStoreDir` — while keeping the
read-only Public Contract (`external/v1`) untouched and keeping the decision engine (`Engine.Evaluate`)
out of the API path.

**Shape.** A new HTTP write/control surface backed by the **existing** `governancepolicy.Repository`.
No new storage engine, no new dependency.

### 3.1 Candidate scope items (proposed)

1. **Separate Management surface.** A distinct HTTP surface — own bind/port (default `:8082`) or a
   distinct URL namespace (`/management/v1`) — exposing write operations. Never mounted under
   `external/v1`.
2. **Policy write operations.** `create` (Draft), `update` (Draft only), `activate` (Draft→Active),
   `deactivate` (Active→Draft), `archive` (Active/Draft→Archived). These map 1:1 onto the existing
   `governancepolicy.Repository` lifecycle methods; they introduce no new lifecycle state.
3. **Persistence via existing store.** All writes go through `governancepolicy.Repository`, which writes
   to `PolicyStoreDir`. `PolicyStoreDir` remains the single source of truth; no duplicate store.
4. **Access gating.** The Management API is a *control* boundary and MUST have an explicit
   access-control story (not open to the world). The concrete mechanism is decided in ADR-036 (e.g.,
   shared-secret header, mTLS, or network-isolation + bind-to-loopback); the *requirement* is fixed here.
5. **Validation & error model.** Input validation, idempotency keys for create, and a consistent error
   envelope — specified in ADR-036.
6. **Composition-Root-only wiring.** `cmd/opscore-server` constructs the Management API handler and
   registers it on its own bind, reusing the same `governancepolicy.Repository` instance already used
   by the read/probe surfaces.

### 3.2 MUST (Phase 17 iron laws)

- **P17-1 — Management API is a SEPARATE surface from `external/v1`.** Distinct bind/port or distinct
  namespace. The frozen read contract is never given a write path.
- **P17-2 — Access control is a HARD MUST (fail-closed).** Every Management API mutation request MUST
  traverse an explicit **Authentication + Authorization** gate. Unauthenticated, failed-auth, or
  unauthorized requests MUST be rejected fail-closed (default-deny). There is **NO** open-by-default path
  of the form `HTTP → Management Handler → Repository`. The concrete mechanism (shared-secret header /
  mTLS / network-isolation + loopback bind) is decided in ADR-036; the *requirement* is fixed here as a
  Scope-level MUST. (Upgraded from "should have a story" to mandatory by R73-B.)
- **P17-3 — `PolicyStoreDir` remains the single source of truth.** The API writes only through
  `governancepolicy.Repository`; no second store, no cache that can diverge.
- **P17-4 — The API does NOT invoke `governance.Engine.Evaluate`.** It persists and transitions Policy
  state only. (Consistent with `governancepolicy.Create/Activate/Deactivate/Archive`, which were
  verified in R69/R71 to not call `Engine.Evaluate`.) Evaluation remains a Runtime-Core concern that
  reads Active policies; the Management API is not in that path.
- **P17-5 — All referenced frozen packages remain unmodified** (MUST-1~5); the API consumes
  `governancepolicy.Repository` as-is.
- **P17-6 — No new dependency.** Reuse `encoding/json` and the existing `governancepolicy` store; no
  new Go module / runtime library.
- **P17-7 — Composition Root remains unique** (MUST-5); the API is wired only in `cmd/opscore-server`.
- **P17-8 — Revision-aware mutation (MUST).** Every modifying operation MUST be based on an explicit
  `Revision`; unconditional overwrite of the latest Policy is forbidden. On concurrent modification the
  conflict MUST be returned explicitly (e.g. `409 Conflict`) and the caller re-reads. Mechanism (If-Match
  / explicit revision field) is decided in ADR-036; the invariant is fixed here. (New in R73-B.)
- **P17-9 — Policy mutation MUST enter the existing Audit system (MUST).** Each mutation MUST record who
  changed which Policy, which Revision, which lifecycle action, success/failure, and when. Audit is via
  the existing Audit abstraction — **NOT** a side-channel log. This MUST NOT modify the Runtime Contract
  or redesign the whole Audit system; if the existing Audit abstraction cannot fully express a Management
  mutation, that gap is surfaced in ADR-036 as a scoped sub-design, not smuggled into the execution
  Audit semantics. (New in R73-B.)
- **P17-10 — Mutation/Evaluation causal isolation is a Scope MUST.** `activate` / `deactivate` /
  `archive` transition Policy *state* only; they do **NOT** mean `Evaluate`, and they MUST NOT trigger
  `Execute` / `Schedule` / `Apply` / `Remediate` or any execution behavior. The data flow is
  `Management API → Repository → Policy state`, never `Management API → Repository → Evaluate →
  Execution`. (Upgrades the P17-4 prohibition into an explicit causal-isolation MUST; new in R73-B.)

#### 3.2.1 Mandatory request pipeline (R73-B: P17-2 upgrade)

Every mutation request MUST flow through, in order:

    HTTP request
      → Authentication      (who is the caller? fail-closed on missing/invalid)
      → Authorization       (does caller hold Policy-management permission? fail-closed)
      → Validation          (input + revision + idempotency checks)
      → Management Handler  (business logic only)
      → governancepolicy.Repository  (persistence / state transition)
      → Audit record        (P17-9)

There is **NO** path that skips Authentication/Authorization.

#### 3.2.2 Surface separation — data ownership ≠ HTTP contract ownership (R73-B: P17-0/1 emphasis)

`external/v1` and `management/v1` are **distinct HTTP surfaces**:

    :8080 → external/v1     (GET execution / host / policy / correlation)        [frozen, read-only]
    :8081 → probes          (/healthz /readyz /versionz)                        [frozen, read-only]
    :8082 → management/v1   (Policy create/update/activate/deactivate/archive)  [new, write/control]

They share **Policy data ownership** (the same `governancepolicy.Repository` → `PolicyStoreDir`),
**never** HTTP-contract ownership. The shared Repository is not a license to merge the two surfaces;
the two contracts stay separate at the network and routing level. (Independent bind/port preferred;
distinct namespace as API-versioning fallback — final exposure still deployment-config-driven.)

#### 3.2.3 Update semantics — Draft-only (R73-B guidance)

`create` = Draft; `update` = **Draft-only** (an Active Policy is NOT modified in place). To change an
Active Policy, the flow is:

    Active Revision N  →  create/edit next Draft  →  Revision N+1  →  activate

This preserves the Phase 14 `PolicyID + Revision` semantics and prevents silent in-place mutation of a
live policy.

#### 3.2.4 Dependency direction — no execution bridge (R73-B: P17-4/10 + mechanical guard)

    cmd/opscore-server  →  management  →  governancepolicy.Repository   ✅
    management  →  governance.Engine  →  execution                     ❌

The Management API MUST NOT import Runtime execution / `Executor` / Plugin runtime / `Isolation` / Host
registry execution path. The existing AST import-guard (forbidden imports of frozen execution paths)
continues to apply and is extended to cover the new `management` package.

### 3.3 SHOULD (Phase 17)

- **P17-S1** ~Auditable~ — **Upgraded to P17-9 (MUST)** in R73-B: mutation MUST enter the existing Audit
  system (see §3.2).
- **P17-S2** Create is idempotent (idempotency key or content-address) to survive relay/retry.
- **P17-S3** A clear error model distinguishes validation failure (`422`) / not-found (`404`) / conflict
  (`409`) / forbidden (`403`) / unauthorized (`401`).

### 3.4 Out of scope (Phase 17.0)

- Modifying `external/v1` (frozen read contract) — explicitly forbidden (P17-0/P17-1).
- Changing Policy evaluation semantics or calling `Engine.Evaluate` from the API (P17-4).
- Multi-node consensus / replication; remains single-node authoritative store (consistent with A-9).
- HA Control Plane, orchestration layer, leader election.
- New storage engine or new dependency (P17-6).
- Any change to the read surfaces or the read-only probe (`/healthz` `/readyz` `/versionz`).
- New capability ownership in Phase 8 packages; new Plugin Ecosystem contract.

---

## 4. Out of scope — explicitly forbidden for Phase 17

- Any write path added to `external/v1` (the frozen read-only Public Contract).
- Modifying the signed implementations of any frozen package, or the Phase 12/13/14 packages (MUST-4).
- A second main/entrypoint or a second Composition Root (MUST-5/P17-7).
- Multi-node consensus / replication engine; HA Control Plane; orchestration.
- Undoing the Phase 12 composition root or the Phase 15 operational hardening.

---

## 5. Decision requested (Round 73)

Per the ADR-021 discipline, **do not implement yet** — the next step is this Scope ADR, then an
architecture ADR (ADR-036), then implementation. The user selected **Direction A
(Management API / Policy Management)** as the Phase 17 Major Evolution (first deliberate write/control
boundary, scoped separately from the frozen read contract). Please sign off this Scope ADR and confirm
Phase 17 proceeds as **Direction A**:

- **(A) Management API / Policy Management** — a separate, access-gated write/control surface for Policy
  lifecycle, reusing `governancepolicy.Repository`, leaving `external/v1` read-only and `Engine.Evaluate`
  out of the API path. *(User-selected for Phase 17.)*
- **(Other)** — a direction you specify.

On selection, the next round submits the Phase 17.1 **architecture ADR (ADR-036)** — **no handler is
implemented until that is signed.**

---

## 6. Phase 17 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 17.0 | Phase 17 Management API / Policy Management Scope (this ADR-035) | **CLOSED (R73-B, modifications incorporated)** |
| 17.1 | Phase 17 Management API Architecture (ADR-036) | proposed — R74 (authorized by R73-B: "modify ADR-035 then proceed to R74/ADR-036") |
| 17.2 | Implementation (handler / wiring / tests) | proposed — after 17.1 sign-off (R75) |

Each step is authorized only after the previous is signed — same architecture-first discipline.

---

## 7. Sign-off placeholder (Round 73)

| Item | Verdict | Round |
|---|---|---|
| Phase 17 Direction A — Management API / Policy Management | ✅ PASS | 73 |
| Management API as independent Major | ✅ PASS | 73 |
| P17-0 `external/v1` read-only Public Contract UNCHANGED (no write path added) | 🔒 MUST · ✅ PASS | 73 |
| P17-1 Management API is a SEPARATE surface from `external/v1` | ✅ PASS | 73 |
| P17-3 `PolicyStoreDir` single source of truth (Repository sole owner) | ✅ PASS | 73 |
| P17-4 / P17-10 API does NOT invoke `Engine.Evaluate` (causal isolation) | ✅ PASS | 73 |
| MUST-1 Runtime Contract remains frozen | ✅ PASS | 73 |
| MUST-2 Plugin Ecosystem remains frozen | ✅ PASS | 73 |
| MUST-3 Phase 8 capabilities remain closed | ✅ PASS | 73 |
| MUST-4 frozen packages unmodified (API consumes Repository as-is) | ✅ PASS | 73 |
| MUST-5 Deployment layer + Phase 15 hardening (Composition Root unique) | ✅ PASS | 73 |
| MUST-6 Architecture First (ADR-036 signed before handler implemented) | ✅ PASS | 73 |
| P17-7 Composition Root remains unique | ✅ PASS | 73 |
| No new execution entry / no HA / Control Plane / orchestration | ✅ PASS | 73 |
| P17-2 Access Control — **upgraded to HARD MUST (fail-closed)** | ⚠️ → MUST (incorporated) | 73-B |
| P17-8 Revision-aware mutation — **new MUST** | ⚠️ → MUST (incorporated) | 73-B |
| P17-9 Management API MUST produce Audit — **new MUST** | ⚠️ → MUST (incorporated) | 73-B |
| P17-10 Mutation/Evaluation causal isolation — **upgraded to Scope MUST** | ⚠️ → MUST (incorporated) | 73-B |

*Phase 17.0 Management API / Policy Management Scope — R73 verdict **B: accept with modifications**.
Direction A confirmed as the next Major Evolution; the four required boundary strengthenings
(P17-2→HARD MUST, P17-8 Revision-aware mutation, P17-9 Audit, P17-10 causal isolation) are incorporated
above, plus the mandatory request pipeline (§3.2.1), surface separation (§3.2.2), Draft-only Update
semantics (§3.2.3), and dependency-direction / AST-guard discipline (§3.2.4). Per GPT instruction, after
this modification Phase 17.0 is CLOSED and the next round submits ADR-036 (R74). No handler is
implemented until ADR-036 is signed.
**Accepted (Round 73-B, modifications incorporated).***

---

## 8. R73 additional guidance — required answers for ADR-036 (Phase 17.1 Architecture)

Per the R73-B verdict, the following are **explicit acceptance items** the Phase 17.1 Architecture ADR
(ADR-036) MUST resolve. They carry the four strengthened boundaries (P17-2 / P17-8 / P17-9 / P17-10)
and the mandatory request pipeline (§3.2.1) into concrete architecture decisions.

### 8.1 Authentication & Authorization (carries P17-2 HARD MUST)
- **AuthN** — how is the request principal determined? (e.g. shared-secret header `X-Management-Token`,
  mTLS client-cert subject, or loopback identity under network isolation)
- **AuthZ** — what permission is required to mutate Policy, how is it represented, and where is it
  checked? (single `policy:manage` capability, or an admin role — concrete model required)
- **Fail-closed** — when AuthN/AuthZ is missing or fails, how is the request denied? (default-deny,
  explicit `401`/`403`; there is NO silent pass-through of the form `HTTP → Handler → Repository`)

### 8.2 Concurrency & Revision (carries P17-8 MUST)
- **Revision conflict** — how is concurrent modification detected (If-Match / explicit `revision` /
  compare-and-swap) and what is returned on conflict (`409 Conflict`, caller re-reads)?
- **Repository atomicity** — is `Save` + `Revision` bump atomic? what lock / transaction boundary
  guards it so two clients cannot both win (the R73 "Client A / Client B" hazard)?

### 8.3 Audit (carries P17-9 MUST)
- **Audit entry** — how does each mutation enter the *existing* Audit abstraction? what fields are
  recorded (who / which Policy / which Revision / lifecycle action / success-or-failure / timestamp)?
  MUST NOT be a side-channel log; MUST NOT modify the Runtime Contract or redesign the Audit system.

### 8.4 Validation & Semantics
- **Validation** — where is Policy Rule legality validated? (in the handler, against the existing
  `governancepolicy` model — NOT via `Engine.Evaluate`)
- **Update semantics** — why is `update` Draft-only, and how is in-place mutation of an Active Policy
  prevented? (preserves Phase 14 `PolicyID + Revision`)
- **Idempotency** — when is a repeated `Activate` / `Archive` / `Create` idempotent vs. a distinct
  operation? (e.g. idempotency key on create; activate-already-active = no-op)

### 8.5 Error contract (carries P17-S3 SHOULD)
- **Error model** — how are `401` / `403` / `404` / `409` / `422` distinguished, and what envelope
  carries them?

### 8.6 Mechanical isolation guarantees (carries P17-0 / P17-1 / P17-4 / P17-10)
- **Management surface isolation** — how is it *mechanically* proven the Management API cannot be
  mounted under `external/v1`? (separate bind/port `:8082` or `/management/v1` namespace + a
  compile/route assertion that fails the build if the two surfaces share a mount)
- **No execution bridge** — how is it *mechanically* proven a mutation does not enter
  `Evaluate`/`Execute`? (dependency-direction forbidden-import AST guard, extended to cover the new
  `management` package)

### 8.7 ADR-036 acceptance gate
ADR-036 is signed only when **all** of 8.1–8.6 have a concrete, reviewable answer and the mechanical
guards (8.6) are specified. No handler is implemented until then (MUST-6).

