# ADR-018 — Phase 8.4: Governance / Policy Architecture

- **Status**: Accepted (signed Round 43)
- **Date**: 2026-08-03
- **Theme**: Phase 8 — Platform Operations (ADR-014)
- **Companion to**: ADR-010 (Runtime Contract — frozen), ADR-011 (Trust — frozen), ADR-012 (Core Stability — frozen), ADR-013 (Plugin Ecosystem — frozen), ADR-014 (Platform Operations Scope — accepted), ADR-015 (Observability — accepted), ADR-016 (Cluster Coordination — accepted), ADR-017 (Enterprise Operations — accepted)
- **Author**: OpsCore Plugin Runtime Workstream

---

## 0. Abstract

ADR-014 (Phase 8.0) opened Platform Operations, and ADR-015/016/017 defined its first three
capabilities — Observability (read-only Read Model), Cluster Coordination (topology, not a second
Runtime), Enterprise Operations (policy ownership, not an Enterprise Runtime) — each as a
*specification only* (Architecture First, ADR-014 MUST-5). This ADR (8.4) specifies the **fourth and
final capability — Governance / Policy Evaluation** — also spec-only. No implementation code is written
until signed.

The framing (from GPT, Round 42), consistent with 8.1–8.3:

> **This is *Governance* — a Rule Evaluation Capability, not Policy Storage, and not an Enterprise
> Workflow.** It is the *rule engine / policy evaluation* substrate: it evaluates policy and produces a
> verdict. Policy *metadata* is owned by Enterprise (ADR-017); the *evaluation* of that policy is owned
> by Governance. It is deterministic, references existing IDs only, executes nothing, and is
> agnostic to transport / runtime / plugin.

The headline invariant of 8.4:

> **Governance is a deterministic Policy Evaluation engine. It consumes policy + state, emits a
> Verdict, and executes nothing. Same inputs always yield the same verdict.**

---

## 1. Scope — what Governance actually owns

| Concept | Belongs to | Meaning |
|---|---|---|
| **Policy Evaluation / Rule Engine** | Governance (this ADR) | Given policy + observed state, compute a verdict |
| **Conflict Resolution / Policy Priority / Exception / Override** | Governance | Deterministic resolution when policies disagree |
| **Verdict emission (allow / deny / require-approval / maintenance-blocked)** | Governance | The single output of the evaluation engine |
| **Policy *metadata* (Approval / Window / Change / Tenant / Org / RBAC / Attachment)** | Enterprise (ADR-017) | Governance *reads* these; it does not author or own them |
| **Runtime objects (Host / Plugin / Execution / Manifest)** | Frozen systems (ADR-010/013) | Governance only *references* their IDs; never owns them |
| **Observability signals / Cluster topology** | Frozen/accepted (ADR-015/016) | Governance may *consult*; never re-implements |

Governance adds **evaluation semantics**; it does not add policy storage (Enterprise), execution
behavior (Runtime), or observation (Observability). Where a verdict touches execution, it is *consulted*
by the Runtime — Governance never performs the run.

---

## 2. Freeze Boundaries (MUST-1..5, pre-frozen by GPT Round 42)

- **MUST-1 — Governance owns evaluation; Enterprise owns policy metadata.** Clean split, first stated
  as SHOULD-1 in ADR-017 and now frozen: **Enterprise attaches, Governance evaluates.** Approval /
  Maintenance / RBAC / Rolling policies live as metadata in Enterprise; the *verdict computation* that
  turns them into allow/deny lives only in Governance. Neither absorbs the other.
- **MUST-2 — Governance never executes commands; outputs Verdict only.** Like every Phase 8 capability,
  execution still enters the Runtime through its existing execution interface. Governance emits a
  *Verdict* (`Allow` / `Deny` / `RequireApproval` / `MaintenanceBlocked`); it never emits an *Action*
  (`Run` / `Retry` / `Rollback` / `Kill` / `Schedule`).
- **MUST-3 — Governance never owns Runtime objects; only references existing IDs.** It reads
  `PluginID` / `RuntimeID` / `Group` / `ExecutionID` / `Tenant` from the frozen systems (ADR-015
  MUST-4). It holds no copy of, and never re-records, their state.
- **MUST-4 — Governance is deterministic.** Same inputs ⇒ same verdict. No hidden state, no clock
  dependence, no side-effecting evaluation. This makes policy auditable and reproducible — a verdict
  computed yesterday must be reproducible from the same policy + state today.
- **MUST-5 — Governance is transport / runtime / plugin agnostic.** It evaluates policy only. It has no
  opinion about HTTP vs gRPC vs OCI vs SSH, about Go vs WASM plugins, about which Runtime backend
  executes. Decoupling it from every transport and every workload type is what keeps it in the frozen
  ecosystem's shadow rather than re-implementing it.

Two **SHOULD** refinements (from GPT, Round 43) — non-blocking, but written in to prevent the verdict
contract from drifting into a new Runtime Contract:

- **SHOULD-1 — Explainable Verdict.** A verdict returns not just `Allow`/`Deny`, but also
  `Reason` / `MatchedPolicy` / `MatchedRule` / `Priority` / `Evidence` (e.g.
  `{"verdict":"deny","reason":"maintenance window","matchedPolicy":"prod-maintenance","matchedRule":"window-01","priority":100}`).
  This lets Observability / Audit / Enterprise reference the verdict *directly* without re-interpreting
  it.
- **SHOULD-2 — Stable Verdict Contract.** The verdict is a **Value Object** — e.g.
  `struct { Code; Reason; PolicyID; RuleID }` — frozen from day one. It must never evolve
  `string → interface → Action`, because a mutable verdict type would itself become a new Runtime
  Contract. The contract is fixed so the four capabilities stay decoupled forever.

---

## 3. Architecture — deterministic evaluation over composed inputs

```
                 Policy metadata (Enterprise, ADR-017)
                          │  reads
                          ▼
   Observed State (Runtime/Ecosystem/Cluster events, read via Observability)
                          │  reads
                          ▼
                  ┌───────────────────────┐
                  │   Governance (8.4)     │   deterministic rule engine
                  │   evaluate(policy,     │   conflict resolution / priority
                  │            state)      │   exception / override
                  └───────────┬───────────┘
                              │ Verdict only (Allow / Deny / RequireApproval /
                              │            MaintenanceBlocked)
                              ▼
                  Runtime Existing Execution Interface
                              │
                              ▼
                          Execution
```

Key properties:

- **Evaluation, not storage.** Governance is the *function* `verdict = evaluate(policy, state)`. Policy
  lives in Enterprise; state comes from the frozen systems via Observability; the engine is pure and
  deterministic (MUST-4).
- **References IDs, owns nothing.** Every input is keyed by an ID the frozen systems already have
  (ADR-015 MUST-4). No new identity, no private object registry (MUST-3).
- **Composes the platform, replicates none.** Governance may *consult* Observability signals and
  *scope by* Enterprise policy / Cluster membership, but holds no copy of their behavior. An AST guard
  forbids Governance from importing `internal/plugin/runtime` / `internal/observability` /
  `internal/cluster` / `internal/enterprise` internals in a way that lets it re-implement their
  behavior.
- **No execution path.** The verdict is a *decision* the Runtime (or its caller) consults; Governance
  never performs the run. Same discipline as 8.1/8.2/8.3.

---

## 4. Out of Scope — explicitly forbidden for 8.4

- A *Policy Storage / Policy DB* ownership layer (belongs to Enterprise, ADR-017 — MUST-1).
- An *Enterprise Workflow* engine (approval routing, ticketing) — that is Enterprise scope, not
  evaluation (MUST-1).
- Executing commands or emitting actions (MUST-2 — re-enters the Runtime Boundary).
- Owning or re-recording Runtime objects / Host / Plugin / Execution (MUST-3).
- Non-deterministic evaluation (hidden state, clock dependence, side effects) — forbidden by MUST-4.
- Transport- or workload-specific evaluation logic (MUST-5 — must stay agnostic).
- Modifying Runtime Contract / `Manifest` / `Provider` / `Loader` / `Manager` / `core.Context`
  (ADR-014 MUST-4 — needs a new ADR).
- Redefining the Trust Boundary (ADR-011 owns trust; Governance may *scope* by trust level, never
  change a verdict).

---

## 5. Relationship to the frozen system & Phase 8 siblings

ADR-018 is the fourth and final theme (Governance) under the Phase 8 umbrella (ADR-014). The complete
four-layer Platform Operations model is now:

```
Platform Operations
 ├── Observability    — Read Model          (ADR-015, accepted)
 ├── Cluster          — Coordination         (ADR-016, accepted)
 ├── Enterprise       — Policy Ownership     (ADR-017, accepted)
 └── Governance       — Policy Evaluation    (this ADR-018)
```

Execution always remains:

```
Governance → Verdict → Runtime Existing Execution Interface → Execution
```

No new Execution Path.

- **vs 8.1 Observability (ADR-015):** Governance *consumes* the observation model as input state; it
  never rebuilds monitoring.
- **vs 8.2 Cluster Coordination (ADR-016):** Governance *scopes* verdicts by cluster membership/label;
  it never re-owns topology.
- **vs 8.3 Enterprise Operations (ADR-017):** Governance *evaluates* the policy metadata Enterprise
  *attaches*. Enterprise = who/what policy applies to (org scope + attachment); Governance = what the
  verdict is (evaluation + conflict resolution). Complementary, not overlapping.

---

## 6. Phase 8 Route (authorization chain — final capability)

| Sub-phase | Deliverable | Status |
|---|---|---|
| 8.0 | Platform Operations Scope (ADR-014) | Accepted (R39) |
| 8.1 | Observability Architecture (ADR-015) | Accepted (R40) |
| 8.2 | Cluster Coordination Architecture (ADR-016) | Accepted (R41) |
| 8.3 | Enterprise Operations Architecture (ADR-017) | Accepted (R42) |
| 8.4 | Governance / Policy Architecture (this ADR-018) | Accepted (R43) + **Implemented (R47 PASS, commit `bbe222a`)** |

Per GPT (Round 42): once 8.4 is accepted, all four Platform Capability architecture ADRs (8.1–8.4) are
frozen, and Phase 8 may then enter its unified implementation stage (writing `internal/observability`,
`internal/cluster`, `internal/enterprise`, `internal/governance` as peripheral packages that compose the
frozen systems by reference). Architecture First preserved throughout.

---

## 7. Core Stability Statement (re-affirmed)

> **Core Stability Statement** (unchanged)
>
> Runtime Core is considered architecturally stable. Future features should preferentially be
> implemented as extensions built on top of the frozen Runtime Contract. Any change that modifies
> Runtime Contract semantics, lifecycle, or public invariants requires a new ADR.

8.4 re-affirms this from the governance angle: Governance is a *deterministic evaluation extension that
composes the frozen systems by reference and emits a verdict* — it adds no Contract change and no
execution path.

---

## 8. Sign-off (Round 43)

- **Architecture verdict**: ✅ PASS (architecture layer)
- **MUST-1** Governance owns evaluation, Enterprise owns policy metadata — ✅ PASS
- **MUST-2** Governance never executes, outputs Verdict only — ✅ PASS
- **MUST-3** Governance never owns Runtime objects, references IDs only — ✅ PASS
- **MUST-4** Governance is deterministic (same inputs ⇒ same verdict) — ✅ PASS
- **MUST-5** Governance is transport/runtime/plugin agnostic — ✅ PASS
- **Applied SHOULD-1** Explainable Verdict (Reason / MatchedPolicy / MatchedRule / Priority / Evidence)
- **Applied SHOULD-2** Stable Verdict Contract (Value Object, frozen from day one)
- **Next step**: ✅ (B) implement `internal/observability` as the Pilot Capability — prove the peripheral-package pattern + AST guard before rolling out Cluster / Enterprise / Governance

*Phase 8.4 Governance / Policy Architecture — Rule Evaluation Capability (deterministic, references IDs, executes nothing), not Policy Storage, not an Enterprise Workflow (Accepted, Rounds 42→43).*

---

## 9. Implementation Sign-off (Round 47 — `internal/governance`)

**Verdict: ✅ PASS** (implementation layer). `internal/governance` committed in `bbe222a`
(quality gate PASSED, 43 packages green). All five MUSTs + two SHOULDs verified by construction:

- **MUST-1 Evaluate only:** `Engine` exposes `Evaluate(policy, state)` + `NewEngine()`. AST-guarded
  `TestNoExecMethod` bans `Run/Exec/Apply/Rollback/Schedule/Emit/Execute`. ✅
- **MUST-2 Verdict only:** Output is a `Verdict{Code}` with `Code ∈ {Allow, Deny, RequireApproval,
  MaintenanceBlocked}`. No Action/Command field exists. ✅
- **MUST-3 References IDs, owns nothing:** `State`/`Policy` keyed by existing IDs (`PluginID`,
  `RuntimeID`, `Group`, `ExecutionID`, `Tenant`) only; imports of the six frozen/accepted systems are
  AST-forbidden. ✅
- **MUST-4 Deterministic:** `sort.SliceStable` by (Priority desc, RuleID asc) yields a total order;
  `TestEvaluateDeterministic` re-runs 50× and compares verdicts field-by-field — identical every
  time. No hidden state / clock / side effects. ✅
- **MUST-5 Agnostic, stdlib, AST-guarded:** pure stdlib; `TestASTGuardForbiddenImports` forbids
  `runtime`/`isolation`/`hostregistry`/`cluster`/`observability`/`enterprise`. ✅
- **SHOULD-1 Explainable Verdict:** `TestExplainableVerdict` asserts every non-default verdict carries
  `Reason` / `MatchedPolicy` / `MatchedRule` / `Priority` / `Evidence` (evidence references only
  existing IDs). ✅
- **SHOULD-2 Stable Verdict Contract:** `Verdict` is a frozen Value Object (struct of plain data),
  asserted by `TestVerdictIsFrozenValueObject`. Shape fixed from day one; never `string → interface →
  Action`. ✅

**Evaluation model:** a `Policy` is an ordered set of tiny pure `Rule`s (kinds: `maintenance-window`,
`change-freeze`, `require-approval`, `tenant-scope`, `group-allow`). The engine sorts rules
deterministically, returns the first match's `VerdictCode`, and defaults to `Allow` when none match
(never blocks by omission). Complexity lives in how policies *compose* rules, not in each rule.

**Enterprise / Governance split (ADR-017 SHOULD-1, ADR-018 MUST-1) preserved:** Enterprise attaches
policy metadata to IDs; Governance evaluates policy + observed state and emits the verdict. Neither
absorbs the other. Execution path unchanged:
`Governance → Verdict → Runtime Existing Execution Interface → Execution`.

**Phase 8 double closure:** with 8.4 implemented, all four Platform Operations capabilities
(Observability / Cluster / Enterprise / Governance) are now both *architecturally frozen* (ADR-015/16/17/18
Accepted) and *implemented* as peripheral packages that compose the frozen systems by reference.

*Phase 8.4 internal/governance — Implemented & PASS (Round 47, commit `bbe222a`).*
