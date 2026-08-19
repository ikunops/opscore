# ADR-026 — Phase 12.1: Deployment Architecture (Composition Root & Server Harness)

- **Status**: Accepted (R59) — Phase 12.1 Architecture ADR; implemented in Round 60 (12.2), Phase 12 CLOSED
- **Date**: 2026-08-07
- **Companion to**: ADR-025 (Phase 12.0 Scope — Accepted, R58, direction A), ADR-024 (Phase 11
  External Interface, CLOSED), ADR-022 (Phase 10, CLOSED), ADR-021 (Architecture Baseline, frozen),
  ADR-014~020 (Phases 8~9, CLOSED), ADR-010/011/012/013 (the four frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 12 — Deployment & Distribution (composition root + server harness over the frozen baseline)

---

## 0. Abstract

Per GPT (Round 58) ADR-025 is **Accepted** and direction **(A) Deployment Topology & Server Harness**
is selected. This ADR is the **architecture ADR for Phase 12.1** — the concrete design of a thin
**composition root + server harness** that wires the frozen six-tier system into one running process
and mounts the `external/v1` read contract.

This ADR is **spec only**. Per GPT (R58): *"R59: 授权提交 ADR-026 Phase 12.1 Deployment Architecture,
spec only, 不编码。"* No implementation lands until this ADR is signed; the implementation follows in
Phase 12.2 (Round 60) after sign-off.

The Harness **assembles**, it does not **become** a capability. The boundary GPT (R58) froze:

```
Deployment
   │
   ├── construct ─┐
   ├── configure  │
   ├── inject     ├────► Existing Capabilities   ← NOT "new orchestration / execution logic"
   └── mount      │
                  ▼
            platformview ─┐
            correlation  ├─ Existing Readers ─┐
            external/v1  ─┘                    │
                                              ▼
                                        Read API / CLI / SDK
```

Single-node is the normative deployment; multi-node is a **reserved seam** (shared external state),
never a distributed-coordination system.

---

## 1. Positioning — composition root + harness, not a new capability

Phase 12.1 sits *around* the frozen tiers as their **wiring + lifecycle owner**:

```
Config ──────────► ┌──────────────────────────┐
                   │ Deployment Harness        │
                   │  DI / Wiring              │
                   │  Lifecycle                │
                   │  Server Mount             │
                   └────────────┬──────────────┘
                               │
              ┌────────────────┼────────────────┐
              ▼                ▼                ▼
        platformview       correlation       external/v1
              │                │                │
              └──── Existing Readers (injected) ┘
```

The Harness is the **sole recommended composition root** (SHOULD-1). It:
- **constructs** the real capability `Readers` (from frozen capabilities' existing read query
  APIs, or read-only query methods added to them without changing semantics),
- **configures** them from `HarnessConfig`,
- **injects** them into `external.Server`,
- **mounts** the `external/v1` contract over a transport (HTTP).

It implements **no** capability logic, **no** execution entry, and **no** domain state of its own.

---

## 2. Freeze boundaries (inherited + this ADR)

**Inherited freezes (from ADR-025 / ADR-021 — unchanged):**
- Runtime Contract, Plugin Ecosystem, Phase 8 capabilities, Platform Integration / Correlation /
  External remain frozen and closed.

**Phase 12 hard boundary (MUST-0) + direction-A具体化 (MUST-1~5), signed by GPT R58:**

- **MUST-0 — No new execution path / no capability-semantics mutation / no Control Plane.** The
  Harness `construct/configure/inject/mount`s existing capabilities; it creates **no** new
  orchestration or execution logic. It must not wrap an Executor or act as a command surface.
- **MUST-1 — Harness assembles only; implements no capability logic.** It performs DI wiring only.
  It **forbids importing** the frozen execution path (`core/execution`, `plugin/runtime`,
  `plugin/isolation`, `controlplane/hostregistry`, `controlplane/server`, `builtin/*`, or any
  executor surface). It may import `platformview` / `correlation` / `external` **only to call their
  public query / construction APIs**.
- **MUST-2 — Owns no Runtime / Plugin / Platform capability; composes existing interfaces.** The
  Harness builds `platformview.Readers` / `correlation.Readers` from frozen capabilities' read APIs;
  it never re-implements or mutates them.
- **MUST-3 — Harness replicates no domain state.** It holds **no** local store / cache of Reader /
  Policy / Execution / Cluster state. Each request flows through the injected Readers to the frozen
  read facades.
- **MUST-4 — External Contract still owned by `external/v1`.** The Harness only **mounts**
  `external.Server`; it does not own, redefine, or evolve the `external/v1` DTO contract (that is
  Phase 11's domain, ADR-024).
- **MUST-5 — Single-/multi-node is topology seam only.** Single-node is the normative deployment.
  Multi-node may share external state (DB / config store) but introduces **no** distributed
  coordination (no leader election, no event replication, no cluster control plane, no cross-node
  execution coordination). Capability semantics and the Runtime Contract are unchanged.

**Folded non-blocking suggestions from GPT (R58), carried as SHOULD:**

- **SHOULD-1 — Composition Root explicit.** The Harness is the **sole** recommended composition root.
  `cmd` / `api` / `worker` must **not** each `new` their own Reader graph; all wiring flows through
  the Harness.
- **SHOULD-2 — Real Reader Wiring.** R57's nil-`Readers` facade construction is a **test/demo
  convenience, not production wiring**; the production Harness MUST inject real Reader
  implementations built from frozen capabilities.
- **SHOULD-3 — Single-node First.** Phase 12.1 ships single-node as the normative deployment; the
  multi-node seam is reserved but inert. Do **not** pre-design distributed state.
- **SHOULD-4 — Config / Capability decoupling.** `HarnessConfig` may set `listen address`,
  `enabled read surfaces`, `reader wiring`, `logging`, `storage`; it must **not** dynamically change
  Runtime / Policy semantics.
- **SHOULD-5 — Deployment Lifecycle only.** The Harness manages `construct → configure → start →
  serve → shutdown` — **process deployment lifecycle only**. It must not be confused with Execution /
  Plugin / Runtime lifecycle.
- **SHOULD-6 — Verifiable Wiring.** R60 adds mechanical tests: `TestProductionWiring`,
  `TestNoNilReader`, `TestExternalUsesInjectedReaders`, `TestHarnessNoExecutionMethod`,
  `TestHarnessNoFrozenOwnership` — so R57's demo wiring can never silently enter production.

---

## 3. Package structure (proposed — not implemented this round)

A new peripheral **assembly** package plus a thin server entrypoint, consistent with the
peripheral-package discipline (ADR-020 §3, ADR-022 §3, ADR-024 §3). The Harness is an *assembly*
package: it imports public facade / `external` types and `storage`/`config` only — **never** the
frozen execution path.

```
internal/harness/
    config.go       # HarnessConfig (SHOULD-4): ListenAddr, EnabledSurfaces, Storage, Logging, ReaderWiring
    harness.go      # Harness: Build Reader graph -> inject external.Server -> lifecycle
                    #          (construct/configure/start/serve/shutdown, SHOULD-5)
    server.go       # transport: HTTP mount of external.Server (serves external/v1)
    errors.go       # wiring errors only (no execution errors)
    harness_test.go # AST guard (MUST-1) + SHOULD-6 mechanical tests

cmd/opscore-server/        # R60: thin entrypoint; calls harness.Build(cfg).Serve(); no direct new Reader
    main.go
```

**Reader graph construction (MUST-2 / SHOULD-2).** The Harness builds the real `platformview.Readers`
and `correlation.Readers` from frozen capabilities' **existing read query APIs** (or read-only query
methods added to them without changing semantics — this is exposure of existing state, not new
capability logic, so it preserves MUST-0/1). The constructed facades are injected into
`external.Server`. **No nil `Readers` in production** (SHOULD-2).

**AST guard (MUST-1).** `internal/harness` forbids importing the frozen execution path
(`core/execution`, `plugin/runtime`, `plugin/isolation`, `controlplane/hostregistry`,
`controlplane/server`, `builtin/*`, any executor surface). It may import `platformview` /
`correlation` / `external` **only to call their public query / construction APIs**, and `storage` /
config for wiring. Its own test (`harness_test.go`) enforces `TestHarnessNoExecutionMethod` and
`TestHarnessNoFrozenOwnership` (SHOULD-6).

> **Note on existing `cmd/opscore`.** The current `cmd/opscore` is a *demo* server that directly
> imports the frozen execution path (`core/execution`, `plugin/runtime`, `builtin/*`). That violates
> Phase 12 MUST-0/1 and is **out of scope** for this ADR — it is neither modified nor reused as the
> Phase 12 harness. Phase 12 introduces the compliant `internal/harness` + `cmd/opscore-server`
> instead. (Refactoring or retiring the demo server is a separate, later decision.)

---

## 4. Interface sketches (not implementation)

```
// HarnessConfig — deployment configuration only (SHOULD-4).
// May set operational parameters; MUST NOT change Runtime/Policy semantics.
type HarnessConfig struct {
    ListenAddr      string            // e.g. ":8080"
    EnabledSurfaces []string          // e.g. ["external/v1"]
    ReaderWiring    ReaderWiringConfig // how to build real Readers from frozen capabilities
    Storage         StorageConfig      // DB / config store (external state only)
    Logging         LoggingConfig
}

// Harness — the sole composition root (SHOULD-1).
type Harness struct { /* no domain state — MUST-3 */ }

// Build constructs the real Reader graph, injects it into external.Server.
// Returns a ready-to-serve Harness. No execution entry created (MUST-0).
func Build(ctx context.Context, cfg HarnessConfig) (*Harness, error)

// Serve mounts the external/v1 transport and blocks serving reads.
func (h *Harness) Serve(ctx context.Context) error

// Shutdown is deployment-lifecycle only (SHOULD-5).
func (h *Harness) Shutdown(ctx context.Context) error
```

The transport (`server.go`) mounts `external.Server` over HTTP and serves `external/v1` read DTOs —
byte-identical to Phase 11's contract (ADR-024 MUST-4/6). The Harness adds **no** new resource or
method beyond what `external.Server` already exposes.

---

## 5. Stable read contract (MUST-4)

The read contract remains owned by Phase 11:

```
external/v1   // unchanged; the Harness only mounts it (ADR-024)
```

A consumer (ops dashboard, CLI, SDK, integration adapter) binds to `external/v1` exactly as before.
The Harness does not introduce a new contract version or a write surface.

---

## 6. Out of scope — explicitly forbidden for Phase 12.1

- Any `Execute` / `Run` / `Schedule` / `Apply` / `Mutate` (MUST-0 / MUST-1).
- Importing or re-implementing the frozen execution path inside the Harness (MUST-1).
- Owning / caching Reader / Policy / Execution / Cluster domain state (MUST-3).
- Redefining or evolving the `external/v1` DTO contract (MUST-4).
- Multi-node distributed coordination: leader election, event replication, cluster control plane,
  cross-node execution coordination (MUST-5 / SHOULD-3).
- Configuration that dynamically changes Runtime / Policy semantics (SHOULD-4).
- A new capability layer or Control Plane (MUST-0).
- Modifying the existing `cmd/opscore` demo server (out of scope, see §3 note).
- Any `.so` / WASM / Alternative Runtime Backend work (still deferred).

---

## 7. Phase 12 authorization chain

| Step | Deliverable | Status |
|---|---|---|
| 12.0 | Phase 12 Deployment & Distribution Scope (ADR-025) | Accepted (R58) — direction A |
| **12.1** | **Deployment Architecture (this ADR-026)** | **Accepted (R59) — implemented R60 (12.2)** |
| 12.2 | Deployment Implementation (`internal/harness` + `cmd/opscore-server`) | **Implemented (R60) — gate green** |

Phase 12.1 is signed before any code (Architecture First, ADR-025 MUST-5). Implementation lands in
12.2 only after GPT signs this ADR.

---

## 8. Sign-off placeholder (Round 59)

| Item | Verdict | Round |
|---|---|---|
| MUST-0 No new execution path / no capability-semantics mutation / no Control Plane | ✅ PASS | 59 |
| MUST-1 Harness assembles only; forbids frozen execution-path imports | ✅ PASS | 59 |
| MUST-2 Owns no capability; composes existing interfaces via Readers | ✅ PASS | 59 |
| MUST-3 Harness replicates no domain state (no store/cache) | ✅ PASS | 59 |
| MUST-4 External Contract (`external/v1`) still owned by Phase 11; Harness only mounts | ✅ PASS | 59 |
| MUST-5 Single-/multi-node is topology seam; no distributed coordination | ✅ PASS | 59 |
| SHOULD-1 Composition Root explicit (Harness sole root) | ✅ PASS | 59 |
| SHOULD-2 Real Reader Wiring (no nil-Readers in production) | ✅ PASS | 59 |
| SHOULD-3 Single-node First (multi-node seam inert) | ✅ PASS | 59 |
| SHOULD-4 Config / Capability decoupling | ✅ PASS | 59 |
| SHOULD-5 Deployment Lifecycle only | ✅ PASS | 59 |
| SHOULD-6 Verifiable Wiring (5 mechanical tests) | ✅ PASS | 59 |

GPT (R59) is requested to verify each boundary against this ADR text and authorize implementation of
`internal/harness` + `cmd/opscore-server` in Round 60 (Phase 12.2).

---

## 9. Implementation sign-off placeholder (Round 60)

| Item | Verdict | Round |
|---|---|---|
| `internal/harness` builds real Reader graph + injects `external.Server` (no nil) | ✅ PASS | 60 |
| AST guard forbids frozen execution-path imports (MUST-1) | ✅ PASS | 60 |
| `TestHarnessNoExecutionMethod` + `TestHarnessNoFrozenOwnership` pass (SHOULD-6) | ✅ PASS | 60 |
| `TestProductionWiring` + `TestNoNilReader` + `TestExternalUsesInjectedReaders` pass (SHOULD-6) | ✅ PASS | 60 |
| No domain state stored/cached in Harness (MUST-3) | ✅ PASS | 60 |
| Serves `external/v1` byte-identical to Phase 11 (MUST-4) | ✅ PASS | 60 |
| Single-node serves; multi-node seam inert (SHOULD-3) | ✅ PASS | 60 |
| `HarnessConfig` sets ops params only, no Runtime/Policy semantics change (SHOULD-4) | ✅ PASS | 60 |

*Phase 12.1 Deployment Architecture — one thin composition root that wires the frozen six-tier system
into a running process and mounts `external/v1`; assembles capabilities, becomes none. (Accepted, Round
59; implemented, Round 60 — Phase 12 CLOSED).*

