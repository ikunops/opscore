# ADR-021 — OpsCore v1 Architecture Baseline (Final Review)

- **Status**: Accepted (Round 51) — Final Architecture Review closed; **OpsCore v1 Architecture Baseline Frozen**
- **Date**: 2026-08-07
- **Companion to**: ADR-010/011/012 (Runtime Core), ADR-013 (Plugin Ecosystem), ADR-014~018
  (Platform Operations), ADR-019~020 (Platform Integration)
- **Author**: OpsCore Plugin Runtime Workstream
- **Nature**: Consolidation review — confirms the four-tier architecture is closed and frozen.
  **No new feature, no new package, no design change.**

---

## 0. Abstract

Rounds 1–50 built OpsCore in four architectural tiers, each frozen through an ADR with an
implementation that passed the `gofmt`/`build`/`vet`/`test` gate:

1. **Runtime Core** (frozen) — the execution engine and Runtime Contract.
2. **Plugin Ecosystem** (frozen) — plugin/package/isolation model layered on the Runtime Core.
3. **Platform Operations** (closed) — four capability layers (Observability / Cluster /
   Enterprise / Governance), each architecture-frozen *and* implementation-landed, proven under
   the two invariants *Composition over Modification* and *No New Execution Path*.
4. **Platform Integration** (closed) — a single read-only facade (`internal/platformview`) over
   the four capabilities.

ADR-021 is the **Final Architecture Review**. It does not add anything; it verifies the four tiers
are mutually consistent and formally declares the **OpsCore v1 Architecture Baseline Frozen**.

---

## 1. Four-tier inventory

| Tier | ADRs | Nature | Packages |
|---|---|---|---|
| Runtime Core | ADR-010, 011, 012 | **Frozen** | `internal/controlplane/hostregistry`, `internal/plugin/runtime`, `internal/plugin/isolation`, `core`, `execution`, `sandbox`, `manifest` |
| Plugin Ecosystem | ADR-013 | **Frozen** | plugin/package/isolation model (layered on Runtime Core) |
| Platform Operations | ADR-014 (scope, CLOSED), 015 (Observability), 016 (Cluster), 017 (Enterprise), 018 (Governance) | **Closed + Implemented** | `internal/observability`, `internal/cluster`, `internal/enterprise`, `internal/governance` |
| Platform Integration | ADR-019 (scope, CLOSED), 020 (Platform Integration) | **Closed + Implemented** | `internal/platformview` (read-only facade) |

Frozen packages are never modified by later phases. The five peripheral packages
(`observability`, `cluster`, `enterprise`, `governance`, `platformview`) are *capability / read*
layers that **read** frozen state and **execute nothing**.

> **Note — `internal/platform` collision.** `internal/platform` is the frozen Phase 2.x HostSnapshot
> resolver and is **not** part of the Phase 9 facade. The read facade lives in `internal/platformview`
> (Architecture Preserving Refactoring, ADR-020 §3, recognized R50).

---

## 2. Seven consistency checks

Each cross-tier axis is checked for conflict. All hold.

| # | Axis | Check | Result |
|---|---|---|---|
| 1 | **Ownership** | Each datum has exactly one owner; peripheral layers never take ownership. | ✅ No conflict — Observation→Observability, Membership/Placement→Cluster, Attachment→Enterprise, Verdict→Governance, Execution→Runtime |
| 2 | **Execution Path** | Exactly one execution entry (Runtime). No peripheral package opens a new one. | ✅ No conflict — peripheral layers are read-only; `Governance→Verdict→Existing Runtime Interface→Execution` |
| 3 | **Read Model** | Peripheral layers expose only composed read views, never mutate. | ✅ No conflict — `platformview` returns `platform/view/v1` value objects |
| 4 | **Coordination** | Cluster computes placement as a pure function; no execution. | ✅ No conflict — `Manager.ComputePlacement` returns `[]HostRef`, no side effect |
| 5 | **Policy** | Enterprise owns policy *attachments*; Governance owns *evaluation*. | ✅ No conflict — separate responsibilities, opaque `TargetRef` only |
| 6 | **Evaluation** | Governance is a deterministic `evaluate(policy, state) → Verdict` value object. | ✅ No conflict — pure function, stable contract, no exec |
| 7 | **Projection** | Platform Integration projects (not redefines) capability models. | ✅ No conflict — View composes references + read-only copies, no `PlatformEntity` |

---

## 3. Enforcement mechanism

The freeze is not advisory — it is mechanically enforced:

- **AST guards** in each peripheral package forbid importing the frozen systems
  (`runtime`, `isolation`, `hostregistry`) and forbid exec methods
  (`Run`/`Exec`/`Apply`/`Schedule`/`Emit`/`Execute`/`Dispatch`/`Rollback`/`Kill`/…).
- **`TestNoExecMethod`** in each peripheral package fails the build if an execution method is added.
- **Runtime Contract frozen** (ADR-010/011/012) — any change requires a new ADR, outside all
  closed phases.

These guards are exercised by the gate (`go build ./...`, `go vet ./...`, `go test ./...`) which
passed at **44 packages, 0 FAIL** (Round 50).

---

## 4. Decision (Round 51) — declare baseline frozen

On sign-off, the four tiers are declared the **OpsCore v1 Architecture Baseline**:

> **OpsCore v1 Architecture Baseline Frozen.**
> Runtime Core / Plugin Ecosystem / Platform Operations / Platform Integration — all four tiers
> closed and frozen. No new execution path, no new Capability, no Runtime Contract change without a
> new ADR. Evolution proceeds only via new, signed ADRs that honor the existing freeze boundaries.

This ADR introduces **no new functionality**; it is the closing review of the relay.

---

## 5. Sign-off placeholder (Round 51 — Final Review)

| Item | Verdict | Round |
|---|---|---|
| Runtime Core (ADR-010~012) frozen & consistent | ✅ PASS | 51 |
| Plugin Ecosystem (ADR-013) frozen & consistent | ✅ PASS | 51 |
| Platform Operations (ADR-014~018) closed & consistent | ✅ PASS | 51 |
| Platform Integration (ADR-019~020) closed & consistent | ✅ PASS | 51 |
| Seven consistency checks (Ownership / Execution Path / Read Model / Coordination / Policy / Evaluation / Projection) | ✅ PASS (all 7) | 51 |
| AST guard + `TestNoExecMethod` enforcement intact (gate green) | ✅ PASS | 51 |
| **OpsCore v1 Architecture Baseline Frozen** declared | ✅ CONFIRMED | 51 |

## 6. Appendix — Architecture Evolution Rules (Round 51, non-blocking)

Per GPT (R51), ADR-021 also serves as the **evolution charter** for the frozen baseline. Future
work proceeds in two tracks:

| Track | What | Process |
|---|---|---|
| **Minor Evolution** | New implementation, performance tuning, bug fix, new Adapter, new Exporter, docs, examples, test enhancement — **does not change a Frozen Contract** | Regular implementation iteration on the baseline; no new ADR required |
| **Major Evolution** | Any change to Runtime Contract / Capability Ownership / Execution Path / Plugin Contract / Policy Model / Cross-capability interaction / Public API Contract | **New ADR first**, re-review, then implement (ADR-first) |

**Compatibility Principle (Round 51).** New capability should **compose existing systems** rather
than duplicate them — the same discipline that kept all 51 rounds inside the freeze boundaries.

This keeps the architecture stable while allowing the implementation to keep evolving.

*ADR-021 — Final Architecture Review: confirms the four-tier OpsCore architecture is closed,
consistent, and frozen; declares the v1 Architecture Baseline; serves as the evolution charter
(Accepted, Round 51 — no new functionality).*
