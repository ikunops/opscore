# ADR-032 — Phase 15.1: Deployment Productionization — Architecture

- **Status**: Accepted (Round 68, signed **PASS**) — **Direction A: Deployment Productionization
  (Phase 15.0 Scope ADR-031 signed PASS R67 — Phase 15.0 CLOSED)**
- **Date**: 2026-08-09
- **Companion to**: ADR-031 (Phase 15.0 Scope, **Accepted R67 — Phase 15.0 CLOSED**), ADR-030 (Phase
  14.1, CLOSED), ADR-029 (Phase 14.0, CLOSED), ADR-028 (Phase 13.1, CLOSED), ADR-027 (Phase 13.0,
  CLOSED), ADR-026 (Phase 12, CLOSED), ADR-024 (Phase 11.1, CLOSED), ADR-022 (Phase 10, CLOSED),
  ADR-021 (Architecture Baseline, frozen), ADR-014~020 (Phases 8~9, CLOSED), ADR-010/011/012/013 (the
  four frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 15 — give the already-frozen Deployment surface (Phase 12) + real read-surfaces
  (13) + real Policy persistence (14) a *production-grade single-node runtime* (config, service unit,
  health, logging, graceful shutdown, upgrade) — **no capability-semantics change**.

---

## 0. Abstract

ADR-031 (R67) signed **PASS** and selected **Direction A — Deployment Productionization**. GPT (R67)
confirmed the freeze boundaries hold and authorized this Architecture ADR (ADR-032), explicitly listing
ten architecture-level freeze points that Phase 15.1 must harden (see §2.2).

This ADR is the **architecture** for Direction A. It introduces **operational scaffolding *around* the
existing Deployment surface** — `cmd/opscore-server` + `internal/harness` — and **touches no frozen
capability, no Runtime Contract, no `external/v1`, and adds no execution entry**. Phase 15.1 only:

- describes the deployment surface with a **versioned, validated config**;
- mounts the single-node process under a **service unit**;
- exposes **read-only health/readiness** that reflects frozen read models;
- defines a **graceful shutdown** that drains in-flight reads and flushes persistence;
- routes **structured logging + version/schema exposure** through the existing harness Config;
- manages the **`PolicyStoreDir`** lifecycle and **data/secret boundaries** fail-closed.

The composition root stays `cmd/opscore-server → internal/harness` — **no second production wiring
path** is introduced.

---

## 1. Phase 15 positioning — operational scaffolding around the frozen surface

Phase 15.1 adds **no data ownership and no new capability**. It is *orthogonal* to "project existing
state" (Phase 13) and "persist policy" (Phase 14) — it is the **operational hardening** those frozen
tiers have always lacked for a real single-node deployment.

| Boundary | Status in Phase 15.1 |
|---|---|
| `cmd/opscore-server` | extended operationally only (config load, signal handling, lifecycle) — still the sole composition root |
| `internal/harness` | gains config-validation / health / logging / shutdown lifecycle — **no capability wiring added** |
| `HarnessConfig` | additive config fields (version, paths, log level/format, probe bind) — **no semantic switch** |
| `internal/external` (external/v1) | **UNCHANGED** — READ ONLY; Phase 15.1 adds *no* endpoint to it |
| `internal/platformview` / `correlation` / `clusterprojection` / `governancepolicy` | **UNCHANGED** — their read models are what health probes observe |
| Phase 8 capabilities + `governance.Engine` | **UNCHANGED** — Phase 15.1 never calls `Evaluate` or any execution verb |
| `internal/governancepolicy` | **UNCHANGED** — `PolicyStoreDir` only selects its existing Repository location |

---

## 2. Freeze boundaries (inherited + R67 architecture freeze points)

### 2.1 Inherited (from ADR-031)

- **MUST-1 — Runtime Contract frozen** (ADR-010/011/012).
- **MUST-2 — Plugin Ecosystem frozen** (ADR-013).
- **MUST-3 — Phase 8 capabilities closed**; Phase 15.1 observes them read-only.
- **MUST-4 — Read / persistence sources unchanged**; phase 15.1 does not modify them.
- **MUST-5 — Deployment layer (Phase 12) remains the deployment surface** (assembles, mounts,
  lifecycle, observability — no execution).
- **MUST-6 — Architecture First**; no code until this ADR is signed (R69).
- **P15-0 (hard) — No frozen-semantics break, no Runtime-Contract break, no new execution entry, no
  Control Plane / orchestration.**

### 2.2 Phase 15.1 architecture freeze points (GPT R67 — must hold)

| ID | Law | How the architecture satisfies it |
|---|---|---|
| A-1 | **Composition Root 唯一化** | Only `cmd/opscore-server → internal/harness` assembles the process. No second production wiring path (no hidden `init()`, no alternate `main`). |
| A-2 | **配置只描述 Deployment** | `HarnessConfig` gains *operational* fields only (paths, log level/format, probe bind, store dir). It **cannot** alter Runtime / Policy / Plugin semantics. |
| A-3 | **Health/Readiness 只反映状态** | Probe handlers are **read-only observers** of frozen read models; they never trigger execution / repair / scheduling. |
| A-4 | **Graceful Shutdown 只处理进程生命周期** | Shutdown drains in-flight reads + flushes the existing `PolicyStoreDir` Repository; it is **never** translated into Execution Cancel / Apply / capability ops. |
| A-5 | **PolicyStoreDir 属部署配置** | It only selects the **existing** `governancepolicy.Repository` storage location; it changes **no** Policy lifecycle semantics (create/activate/archive unchanged). |
| A-6 | **Secret 只进入配置/运行环境边界** | Secrets enter via env / out-of-binary config; they form **no new Domain Entity or Capability**. |
| A-7 | **日志/版本信息只观测** | Structured logs + version/schema lines are **observability only**; they are never a new event-ownership or control channel. |
| A-8 | **单节点生产化优先** | No distributed coordination / leader election / HA state machine is introduced; the multi-node seam stays inert. |
| A-9 | **机械守卫继续存在** | AST import guard + `TestNoExecMethod` + wiring/lifecycle tests cover the new operational code. |
| A-10 | **External Contract 冻结** | `external/v1` does **not** evolve in Phase 15.1; any new Public API is a *separate future Major ADR*. |

---

## 3. Architecture

### 3.1 Configuration layer (operational, validated, fail-closed)

```
opscore.yaml  (version-stamped, validated against HarnessConfig on load)
   ├─ version: "1"                # schema stamp; unknown/deprecated keys → fail-closed
   ├─ server:
   │    ├─ listen: ":8080"        # existing external/v1 bind (unchanged contract)
   │    └─ probe:  ":8081"        # NEW operational bind for health/readiness ONLY
   ├─ log:
   │    ├─ level: "info"          # wired through harness Config (A-7, no semantics change)
   │    └─ format: "json"
   ├─ storage:
   │    └─ policyStoreDir: "/var/lib/opscore/policy"   # A-5: selects existing Repository location
   └─ (NO runtime/policy/plugin semantic switches)     # A-2
```

- A `config.Load(path)` validates the file against `HarnessConfig` (additive schema); on unknown key /
  missing required field / invalid value → **process refuses to start** (fail-closed, §3.6).
- The existing `HarnessConfig` gains additive fields only; the Go struct is backward-compatible.

### 3.2 Service unit (packaging artifact, single-node)

- A `systemd` unit (or container `Dockerfile`/manifest) that:
  - declares **single-node** topology (multi-node seam inert — A-8);
  - sets `ExecStart=cmd/opscore-server -config /etc/opscore/opscore.yaml`;
  - requests `Restart=on-failure` and a `TimeoutStopSec` aligned with graceful shutdown (§3.4);
  - runs as a non-root user with restrictive perms on the data dir (§3.5).
- This is **packaging**, not a new runtime path — composition root stays `cmd/opscore-server`.

### 3.3 Health / Readiness probes (read-only observers — A-3)

```
GET /healthz   → liveness:  process up + composition root built OK
GET /readyz    → readiness: frozen read models reachable
                   (delegates to existing platformview/correlation/governancepolicy read sources)
                 NO capability mutation, NO execution, NO repair, NO scheduling
```

- Probes are served on a **separate operational bind** (`probe:`), not on `external/v1` (A-10).
- A probe handler reads the *already-built* read models; if a read source is unavailable it reports
  `not ready` — it never attempts to fix, re-run, or trigger anything.

### 3.4 Graceful shutdown (process lifecycle only — A-4)

```
signal SIGTERM/SIGINT
   → harness.Stop():
       1. stop accepting NEW reads (external/v1 + probes return 503/locked)
       2. drain in-flight reads (wait bounded; Phase 12 idempotency preserved)
       3. flush + close the existing PolicyStoreDir Repository (no lifecycle *semantics* change)
       4. exit 0
```

- Shutdown is **process lifecycle**: it drains and flushes; it is **never** mapped to Execution
  Cancel / Apply / capability operations. No new execution entry (P15-0/A-4).

### 3.5 Persistence path + data / secret boundary (A-5, A-6)

- On startup, `PolicyStoreDir` is validated: created with **restrictive perms** if absent,
  **fail-closed** if unwriteable (P15-S4). It only tells the existing `governancepolicy.Repository`
  *where* to store — Policy create/activate/archive semantics are unchanged (A-5).
- Secrets (if any) enter via **env / out-of-binary config**; config load refuses secret-in-plaintext
  where a secret source is expected. Secrets form **no Domain Entity or Capability** (A-6).

### 3.6 Fail-closed startup (operational hardening)

- Required dependencies — config file, `PolicyStoreDir`, capability wiring — are checked at boot.
- Any missing/invalid dependency → log a precise error and **exit non-zero**; no partial/degraded
  silent run.

### 3.7 Version / schema exposure (observability only — A-7)

- A read-only `/versionz` (operational bind) or startup log line exposes build version + ADR schema
  revision. It is **observability only** — never an event-ownership or control channel.

### 3.8 What stays frozen (explicit non-changes)

- `cmd/opscore-server` — still the sole composition root (A-1); only gains config-load + signal +
  lifecycle glue.
- `internal/harness` — gains config-validation / health / logging / shutdown lifecycle; **no capability
  wiring added**.
- `internal/external` (external/v1) — **zero edits**; stays READ ONLY (A-10).
- `internal/platformview`, `internal/correlation`, `internal/clusterprojection`,
  `internal/governancepolicy` — **unchanged**; health probes merely observe their read models.
- `governance.Engine` / `Evaluate` — **never called** by Phase 15.1 operational code.

---

## 4. Acceptance mapping (how each law is verifiable)

| Law | Verification |
|---|---|
| A-1 Composition Root 唯一化 | `grep`/AST: only `cmd/opscore-server` calls `harness.Build`; no second `main`/hidden wiring |
| A-2 配置只描述 Deployment | `HarnessConfig` diff is additive operational fields; no Runtime/Policy/Plugin semantic field |
| A-3 Health 只反映状态 | probe handlers contain **no** execution/mutation verb; tests assert they only read |
| A-4 Shutdown 只处理生命周期 | `harness.Stop` has no `Evaluate`/Apply/Cancel call; tests assert drain+flush only |
| A-5 PolicyStoreDir 属部署配置 | `PolicyStoreDir` flows only into `NewFileRepository`'s path arg; no lifecycle call changed |
| A-6 Secret 不形成 Entity/Capability | no new package/type for secret; secret read via env/config boundary only |
| A-7 日志/版本只观测 | logging/version code has no write/trigger side effect; tests assert observability-only |
| A-8 单节点优先 | no leader-election / consensus / HA-state-machine import or code |
| A-9 机械守卫 | `TestNoExecMethod` + AST import guard + wiring/lifecycle tests cover new operational code |
| A-10 External 冻结 | `git diff internal/external` empty; no endpoint added to external/v1 |

---

## 5. Mechanical guards (carried from Phase 8/13/14 discipline)

- **AST import guard** (per new operational package `_test.go`): fails if it imports any execution-path
  package (`internal/plugin/runtime`, `internal/plugin/isolation`, `internal/controlplane/hostregistry`,
  `internal/cluster`, `internal/observability`, `internal/enterprise`) or reaches into frozen
  *implementation* internals.
- **`TestNoExecMethod`**: scans method receivers; fails on any execution/mutation verb (Run / Exec /
  Invoke / Apply / Execute / Command / Emit / Dispatch / Rollback / Kill / Schedule / Mutate / Resolve /
  Replay / Remediate). Operational code is config/lifecycle/observability only.
- **Wiring / lifecycle tests**: assert composition-root uniqueness (A-1), fail-closed startup (§3.6),
  probe read-only-ness (A-3), and shutdown drain+flush (A-4).

---

## 6. Integration points (exact, code-level)

| Surface | Today (R67) | Phase 15.1 | Frozen? |
|---|---|---|---|
| `cmd/opscore-server` | builds harness, serves external/v1 | + config-load + signal + lifecycle glue (sole root) | composition root unchanged (A-1) |
| `internal/harness.Build` | assembles capabilities | + config validation / probe mount / logging / Stop | no capability added |
| `HarnessConfig` | capability bundles | + operational fields (version/paths/log/probe/storeDir) | additive only (A-2) |
| `external/v1` endpoints | read-only | unchanged | **method unchanged** (A-10) |
| health/readiness | none | NEW `/healthz` `/readyz` on `probe:` bind | separate operational bind |
| graceful shutdown | Phase 12 best-effort | explicit drain + flush | idempotency preserved (A-4) |
| `governancepolicy.Repository` | file-backed at `PolicyStoreDir` | `PolicyStoreDir` validated/managed | lifecycle semantics unchanged (A-5) |

---

## 7. Roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 15.0 | Phase 15 Deployment Productionization Scope (ADR-031) | **signed PASS R67 — CLOSED** |
| 15.1 | Deployment Productionization Architecture (this ADR-032) | **signed PASS R68 — CLOSED** |
| 15.2 | Implementation (config / service unit / probes / logging / shutdown / storeDir mgmt) | **in progress — Round 69 (sign-off pending)** |

---

## 8. Sign-off placeholder (Round 68)

| Item | Verdict | Round |
|---|---|---|
| P15-0 No frozen-semantics / Runtime-Contract break, no new execution entry, no Control Plane / orchestration | ✅ PASS | 68 |
| MUST-1 Runtime Contract remains frozen | ✅ PASS | 68 |
| MUST-2 Plugin Ecosystem remains frozen | ✅ PASS | 68 |
| MUST-3 Phase 8 capabilities remain closed (observed read-only) | ✅ PASS | 68 |
| MUST-4 read/persistence sources unchanged | ✅ PASS | 68 |
| MUST-5 Deployment layer remains the deployment surface | ✅ PASS | 68 |
| MUST-6 Architecture First (implementation only after this ADR signed) | ✅ PASS | 68 |
| A-1 Composition Root 唯一化 | ✅ PASS | 68 |
| A-2 配置只描述 Deployment | ✅ PASS | 68 |
| A-3 Health/Readiness 只反映状态 | ✅ PASS | 68 |
| A-4 Graceful Shutdown 只处理进程生命周期 | ✅ PASS | 68 |
| A-5 PolicyStoreDir 属部署配置 | ✅ PASS | 68 |
| A-6 Secret 只进入配置/运行环境边界 | ✅ PASS | 68 |
| A-7 日志/版本信息只观测 | ✅ PASS | 68 |
| A-8 单节点生产化优先 | ✅ PASS | 68 |
| A-9 机械守卫继续存在 | ✅ PASS | 68 |
| A-10 External Contract 冻结 (external/v1 不演进) | ✅ PASS | 68 |

*Phase 15.1 Deployment Productionization — Architecture ADR. Signed PASS R68 (CLOSED 🔒). Implementation proceeds via R69 (sign-off pending).*
