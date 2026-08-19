# ADR-025 — Phase 12.0: Deployment & Distribution Scope

- **Status**: Accepted (Round 58) — direction **A Deployment Topology & Server Harness** selected; Phase 12.1 architecture ADR (ADR-026) submitted in Round 59
- **Date**: 2026-08-07
- **Companion to**: ADR-021 (Architecture Baseline, frozen), ADR-022 (Phase 10, CLOSED),
  ADR-024 (Phase 11, CLOSED), ADR-019/020 (Phase 9, CLOSED), ADR-014~018 (Phase 8, CLOSED),
  ADR-010/011/012/013 (the four frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 12 — Deployment & Distribution (delivery / runtime layer over the frozen baseline)

---

## 0. Abstract

Phase 11 **External Interface is CLOSED** (ADR-024, Round 57). The six-tier system is now
end-to-end closed and frozen:

```
Runtime Core → Plugin Ecosystem → Platform Operations → Platform Integration
            → Event Correlation → External Interface (read-only Public Contract)
```

The architecture is complete, but it is **not yet reliably *runnable* in production**: there is no
defined deployment topology (single- / multi-node), no server harness that wires the frozen
packages into a running process, no upgrade / migration / rollback path, no backup / restore
contract, and no configuration / secret-distribution boundary. The capability chain can *do*
everything (execute / observe / correlate / govern / expose read-only) — what is missing is *how to
operate it reliably*.

Phase 12 is the **Deployment & Distribution** phase: it makes the frozen baseline reliably
deployable and operable. Per the ADR-021 evolution charter, adding a deployment / operational layer
that defines topology, process wiring, upgrade strategy, and config/secret boundaries is a **Major
Evolution** — it changes the operational contract, runtime topology, and (potentially) the
secret/configuration surface — so it requires this Scope ADR, then an Architecture ADR, then
implementation. **Phase 12 writes no implementation until the chosen direction's architecture ADR is
signed off.**

---

## 1. Phase 12 positioning — delivery/runtime layer, not a new internal axis

| Phase | Nature | Adds |
|---|---|---|
| 8 | Platform Operations axis (closed) | Observability / Cluster / Enterprise / Governance |
| 9 | Platform Integration (closed) | `internal/platformview` read-only facade |
| 10 | Event Correlation (closed) | `internal/correlation` cross-capability projection |
| 11 | External Interface (closed) | `external/v1` read-only Public Contract |
| **12** | **Deployment & Distribution** | runtime topology + process wiring + upgrade/migration + backup/restore + config/secret boundary |

Phase 12 sits *around* the frozen tiers. It must obey every existing freeze boundary (ADR-021) **plus
one new hard boundary**:

> **Phase 12 Boundary (MUST-0):** Phase 12 introduces **no new execution entry point into the
> Runtime Core**, **does not modify any frozen capability's semantics or contract**, and **does not
> become a Control Plane**. A server harness / deployment tool may *wire* frozen packages into a
> running process and *operate* external state (DB / config / secrets), but it invents **no**
> capability logic, **no** execution path, and **no** mutation of frozen in-process models. Full
> stop.

---

## 2. Freeze boundaries (inherited + new)

- **MUST-1 — Runtime Contract remains frozen.** ADR-010/011/012 unchanged.
- **MUST-2 — Plugin Ecosystem remains frozen.** ADR-013 unchanged.
- **MUST-3 — Phase 8 capabilities remain closed.** `internal/observability`, `internal/cluster`,
  `internal/enterprise`, `internal/governance` are implemented and signed; Phase 12 may run/wire
  them but must not modify them.
- **MUST-4 — Platform Integration / Correlation / External remain closed.** `internal/platformview`,
  `internal/correlation`, `internal/external` are the only sanctioned read sources Phase 12 observes.
- **MUST-5 — Architecture First.** The chosen direction's architecture ADR is signed before code.
- **MUST-0 (new, hard) — No new execution path / no capability-semantics mutation / no Control
  Plane** (§1).

---

## 3. Candidate directions (choose one)

Phase 12 proposes three candidate directions. Each is a *scope sketch* only — detail lands in a
follow-up architecture ADR once GPT selects.

### 3.1 Direction A — Deployment Topology & Server Harness — **recommended**

**Goal.** Define the runtime topology (single-node, and the seam for multi-node) and ship a server
harness that wires the frozen packages into one running `opscore` process — injecting the real
capability `Readers` (production composition root), mounting the `external/v1` contract, and binding
configuration. This closes the R57-open gap: the CLI currently builds facades with nil `Readers`.

**Shape (proposed).** A thin harness package (e.g. `internal/harness` or `cmd/opscore-server`) that
*assembles* frozen packages via dependency injection — no capability logic, no new execution entry.
It builds `platformview.Readers` / `correlation.Readers` from the real capability query APIs,
constructs `external.Server`, and serves the `external/v1` contract. Multi-node is a *seam* (shared
DB / config store), not a re-implementation.

**MUST (A).**
- A-1 The harness references frozen packages as-is; it implements **no** capability logic.
- A-2 It injects the real `Readers` (production composition root) — closing the R57 nil-`Readers` stub.
- A-3 It adds **no execution entry**: no `Execute`/`Run`/`Schedule`/`Apply` flows through it.
- A-4 Topology is declared (single-node now; multi-node seam via shared external state).
- A-5 An AST guard forbids the harness becoming an executor or importing frozen *implementation*
  internals (it may import public facade / `Readers` types only).

**SHOULD (A).**
- A-S1 The served contract is byte-identical to `external/v1` (no divergence from Phase 11).
- A-S2 Health / readiness probes read only the frozen read models (no capability mutation).

**Out of scope (A).** No new capability, no write API, no multi-node consensus/replication engine
(that is a separate, later Major Evolution).

---

### 3.2 Direction B — Upgrade / Migration / Rollback

**Goal.** Define an additive, reversible upgrade & migration path for the external state (DB schema
/ config) across OpsCore versions, with a rollback guarantee.

**Shape.** Migration tooling + version metadata. **Operational only**; never mutates a frozen
in-process contract or capability semantics.

**MUST (B).**
- B-1 Migration tooling is additive and reversible; never mutates a frozen contract.
- B-2 Never alters capability semantics — only external (DB / config) state.
- B-3 No new execution entry into the Runtime Core.

**SHOULD (B).**
- B-S1 Each migration is idempotent and forward/backward checkable.

**Out of scope (B).** No new capability, no Public API change (that is Phase 11's domain).

---

### 3.3 Direction C — Backup / Restore / Config & Secret Distribution

**Goal.** Define a backup/restore contract for external state (DB / config), plus a configuration /
secret-distribution boundary (where secrets live, how they are injected, never in frozen models).

**Shape.** Backup/restore tooling + a config/secret boundary spec. **Reads/writes external state
only.**

**MUST (C).**
- C-1 Backup/restore covers only external state (DB / config / secrets), never frozen in-process models.
- C-2 Secret boundary is declared; secrets are never embedded in frozen capability code.
- C-3 No new execution entry into the Runtime Core.

**SHOULD (C).**
- C-S1 Config distribution is version-stamped and validated against the frozen contract on load.

**Out of scope (C).** No new capability, no topology definition (that is Direction A).

---

## 4. Out of scope — explicitly forbidden for Phase 12

- Any new execution entry point into the Runtime Core / Control Plane (MUST-0).
- Modifying Runtime Contract / Plugin Ecosystem / any Phase 8 capability / `platformview` /
  `correlation` / `external` (MUST-1/2/3/4).
- Turning the harness into a capability implementer or a writer of capability state.
- Any `.so` / WASM / Alternative Runtime Backend work (still deferred).
- A write/mutate Public API (that is a distinct Major Evolution, not Phase 12).
- Multi-node consensus / replication engine (Direction A ships only the seam, not the engine).

---

## 5. Decision requested (Round 58)

Per GPT (Round 57) recommendation, **Direction A (Deployment Topology & Server Harness)** is
preferred — it directly closes the R57-open production composition root (nil-`Readers`) gap and makes
the frozen baseline actually runnable — but the choice is yours. Please sign off this Scope ADR and
select **one** direction for Phase 12:

- **(A) Deployment Topology & Server Harness** — runtime topology + thin process-wiring harness that
  injects real `Readers` and serves `external/v1`. *(recommended)*
- **(B) Upgrade / Migration / Rollback** — additive, reversible external-state migration tooling.
- **(C) Backup / Restore / Config & Secret Distribution** — external-state backup + config/secret boundary.
- **(Other)** — a direction you specify.

On selection, the next round submits the chosen direction's **architecture ADR** (e.g. ADR-026 for A)
— **no implementation until that is signed.**

---

## 6. Phase 12 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 12.0 | Phase 12 Deployment & Distribution Scope (this ADR-025) | **Accepted (R58)** — direction A |
| 12.1 | Chosen-direction Architecture ADR (ADR-026) | **submitted R59 (pending sign-off)** |
| 12.2 | Chosen-direction Implementation (`internal/harness` + `cmd/opscore-server`) | proposed — after 12.1 sign-off (R60) |

Each step is authorized only after the previous is signed — same architecture-first discipline.

---

## 7. Sign-off placeholder (Round 58)

| Item | Verdict | Round |
|---|---|---|
| MUST-0 No new execution path / no capability-semantics mutation / no Control Plane | ✅ PASS | 58 |
| MUST-1 Harness assembles only; implements no capability logic | ✅ PASS | 58 |
| MUST-2 Owns no Runtime / Plugin / Platform capability; composes existing interfaces | ✅ PASS | 58 |
| MUST-3 Harness replicates no Reader / Policy / Execution / Cluster domain state | ✅ PASS | 58 |
| MUST-4 External Contract still owned by `external/v1`; Deployment only mounts it | ✅ PASS | 58 |
| MUST-5 Single-/multi-node is topology seam; no capability-semantics change | ✅ PASS | 58 |
| Direction selected (A/B/C/Other) | ✅ **A — Deployment Topology & Server Harness** | 58 |

**GPT (R58) non-blocking suggestions folded into ADR-026:** SHOULD-1 Composition Root explicit
(Harness is the sole recommended composition root — no `cmd`/`api`/`worker` each `new Reader`);
SHOULD-2 Real Reader Wiring (R57 nil-`Readers` is test/demo convenience, not production wiring);
SHOULD-3 Single-node First (single-node = normative deployment, multi-node = reserved seam, no
distributed coordination); SHOULD-4 Config/Capability decoupling (config may set listen address /
enabled surfaces / reader wiring / logging / storage, never change Runtime/Policy semantics);
SHOULD-5 Deployment Lifecycle only (construct/configure/start/serve/shutdown — process lifecycle,
not Execution/Plugin/Runtime lifecycle); SHOULD-6 Verifiable Wiring (mechanical tests
TestProductionWiring / TestNoNilReader / TestExternalUsesInjectedReaders / TestHarnessNoExecutionMethod
/ TestHarnessNoFrozenOwnership).

*Phase 12.0 Deployment & Distribution Scope — a reliable runtime/delivery layer around the frozen
six-tier baseline; no new execution path, no capability-semantics mutation. Direction A selected;
authorized to submit the Phase 12.1 architecture ADR (ADR-026) in Round 59. (Accepted, Round 58).*

