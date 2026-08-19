# ADR-046 — Phase 21: Operational Protection (Scope)

- **Status**: **ACCEPTED WITH MODIFICATIONS (R93 verdict B)**. Four scope-level
  clarifications applied per R93 sign-off: S-2 timeout ≠ guaranteed termination; S-3
  breaker distinguishes unavailable evidence from zero failures; S-4 single persistence
  owner + startup fail-closed; R21-6 protection rejects use dedicated `protection.*`
  audit vocabulary, not Policy mutation INTENT→CAS→OUTCOME. → authorises ADR-047
  Architecture. Phase 20 = *Causal Tracing* is **CLOSED** at the ADR level (R90 verdict B,
  R91 verdict A, R92 verdict A; commits `80963bd` / `01bd2c2` / `42221be` / `3c5f2ad`).
  Implementation code not yet committed for Phase 20 (per ADR-045 §8 wiring order — to be
  done under separate "land Phase 20" mandate, not in scope of this ADR).
  **No implementation until ADR-047 Architecture sign-off (R94).**
- **Date**: 2026-08-10
- **Companion to**: ADR-043/044/045 (Phase 20, ADR-level CLOSED, implementation pending),
  ADR-041/042 (Phase 19 Evidence Consumers, CLOSED), ADR-039/040 (Phase 18 Evidence Integrity,
  CLOSED), ADR-021 (three-tier discipline, frozen)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 21 — operational protection mechanisms that let the system self-defend
  without violating the frozen contracts.

---

## 0. Abstract

Phase 20 closed the *causal tracing* chain at the ADR level: the system can now reconstruct
*how* an execution unfolded. But reconstruction is forensic, not preventive. The system can
**observe** a runaway capability after the fact; it cannot **bound** one in real time.

Phase 21 proposes **Operational Protection** — a peripheral package `internal/protection` that
sits *between* the request boundary (where Phase 17.2 management intent lives) and the
execution engine (where frozen `core.Executor` lives), enforcing five mutually reinforcing
guards:

- **S-1** Per-capability rate limit (token bucket per `(CapabilityID, principal)`).
- **S-2** Per-execution timeout enforcement (context cancellation propagated to plugin).
- **S-3** Per-capability circuit breaker (opens on failure-rate threshold, half-opens on cooldown).
- **S-4** Bulk kill switch (operator-initiated, by capability tag or by principal).
- **S-5** Bounded concurrency per capability (semaphore — N concurrent executions max).

**Hard constraint**: Protection is a *gate*, never a *driver*. It observes state and either
*passes* or *rejects* requests. It does NOT modify the execution path inside the frozen engine.
A `Reject{reason}` from `internal/protection` is the same shape as any other pre-execution
gate (e.g. RBAC, governance policy) in that it blocks the request — but its **audit semantics
are distinct**: a protection reject is an *observation event* in the `protection.*` namespace,
NOT a Policy mutation `INTENT → CAS → OUTCOME` (R93-④). No policy or resource state is mutated;
the reject is recorded as a protection event with a dedicated action vocabulary.

> Phase 17: the system cannot lie about what it *did*.
> Phase 18: the system cannot lie about what it *sees*.
> Phase 19: the system lets you *read what it sees* — at scale.
> Phase 20: the system lets you *follow what it did* — as a chain.
> **Phase 21: the system can *bound what it does* — before it does too much.**

---

## 1. Positioning — why "protection" and not the other candidates

GPT's R89 sign-off listed three candidates for the next Major:

- **Dashboard UI** — Visualize Phase 17/18/19/20 evidence in a browser.
- **Protection** — Operational safeguards (rate limit, timeout, circuit breaker, kill switch).
- **Control Plane** — Distributed coordination across nodes.

I evaluated each against the discipline set in ADR-039 (Evidence Integrity), ADR-043 (single-
process posture), and the user's standing instruction to "have my own thinking" rather than
pick by popularity. My reasoning:

### 1.1 Control Plane: rejected on principle

Phase 20's R90 verdict explicitly rejected OpenTelemetry by establishing the single-process
posture:

> "OpsCore is a **single-process** system. There is no distributed RPC mesh, no cross-service
> propagation, no vendor-specific export pipeline." (ADR-043 §1)

A genuine Control Plane would contradict this posture. The `internal/controlplane/` package
already exists (audit/auth/hostregistry/inventory/server/sync), but it is *named* for the
control logic it centralizes within the single process — it is NOT a distributed control plane.
Adding distributed coordination now would re-open the architectural question R90 closed.

**Out of scope** for Phase 21. (If OpsCore ever becomes distributed, a future Scope ADR can
revisit — same discipline as the OTel punt.)

### 1.2 Dashboard UI: deferred to a later Major

The platform already exposes 11 `:8082` GET routes (Phase 19 + Phase 20). A read-only web UI
that consumes them is a real operational improvement. But:

- It introduces a new technology layer (HTML / JS / asset pipeline) — outside OpsCore's current
  single-binary deployment posture.
- It is *additive visualization*, not *bounded execution*. A dashboard that shows a runaway
  capability has already exhausted resources.
- It builds on Protection (S-4 bulk kill switch needs a UI surface to be useful; Phase 19's
  `/metrics` text surface is already enough for the kill switch's machine-readable form).

**Deferred** to Phase 22 (or later), after Protection makes the kill switch's effects observable
and well-tested.

### 1.3 Protection: the evidence-based choice

I read four pieces of code to validate the gap:

| Evidence | Location | What it shows |
|---|---|---|
| **P-1** `grep -n "rate\|limit\|timeout\|kill\|circuit" internal/plugin/runtime/manager.go` | empty | The runtime has no rate limit, no execution timeout, no circuit breaker, no kill switch in the executor path. |
| **P-2** `grep -n "context.WithCancel\|context.WithTimeout" internal/plugin/runtime/manager.go` | empty | Plugin executions run to natural completion — there is no context deadline. A plugin that hangs holds a goroutine indefinitely. |
| **P-3** `grep -rn "ErrConcurrentExecution\|circuit.breaker\|rate.limit\|deadline.exceeded" internal/` | no matches | The codebase has no protection-specific error sentinels; nothing maps to "rejected by protection". |
| **P-4** storage has `OperationStore.SetEnabled(name, bool)` | `internal/storage/memory.go:240` | There is a per-operation enable/disable, but it is *persistent state*, not a *live gate*. Changing it requires `Storage.Save` → sync to runtime; there is no in-memory fast path that an executor call site can check in microseconds. |

**Conclusion.** Today, a single misbehaving capability can:

- Run forever (no timeout).
- Run N times in parallel (no concurrency cap).
- Run 1000× in a second (no rate limit).
- Run repeatedly into the same failure (no circuit breaker).
- Remain enabled even after the operator flags it as dangerous (no bulk kill switch).

All five failures are *preventable* by a peripheral gate that the request boundary already
trusts (because Phase 17.2 made the boundary trusted for governance, and Phase 18 made the
boundary trustworthy for evidence).

---

## 2. Scope (S-1..S-5)

### S-1 — Per-capability rate limit

A token-bucket limiter keyed by `(CapabilityID, principal_hash)`. The principal hash is an
opaque, salted SHA-256 of the principal string — **never** logged in cleartext, never stored,
only used to bucket. Default bucket: 60 tokens / 60s, refill 60 / 60s (1 req/s steady, 60 burst).
Configurable per capability via an additive `Operation.Limit` field (storage v6 schema).

`Reject{Reason: "rate_limited", RetryAfter: 1s}` → 429 to client, audit `failure result="rate_limited"`.

### S-2 — Per-execution timeout

A deadline applied via `context.WithTimeout(parentCtx, Operation.Timeout)`. Default: 30s.
Configurable per capability (`Operation.Timeout` field, additive, default 30s).

**R93-①: Timeout is a cancellation/deadline boundary, not a guaranteed termination primitive.**

The architecture MUST distinguish:

```
deadline exceeded
      ↓
context cancellation signal propagated
```

from:

```
underlying execution actually terminated
```

Go cancellation is caller-side: `context.WithTimeout` fires the `Done()` channel, but
whether the downstream goroutine actually stops depends on **cooperative context
handling** by the plugin. Phase 6 established that Go cancellation can be caller-side
fail-closed without actually terminating work.

Therefore:

- For **in-process execution**: cancellation depends on the plugin observing
  `ctx.Done()`. The plugin contract has always required this — but the architecture
  must not *imply* hard termination.
- For **process-isolated helpers** (if any exist in future phases): actual process
  termination may be stronger, but that is a property of the execution backend, not
  of the protection gate.
- **Do not modify frozen executor signatures** (`core.Executor`, `plugin/runtime`)
  merely to manufacture hard cancellation. The timeout flows through the existing
  `context.Context` parameter that the frozen executor already accepts (Phase 9.1
  platformview boundary). No new executor signature.

`Reject{Reason: "timeout"}` → audit event `protection.timeout`. The plugin goroutine
sees `context.DeadlineExceeded` and is expected to clean up (per the existing plugin
contract). If it does not, the goroutine leaks — but the protection gate has already
rejected the request and the audit trail records the timeout.

### S-3 — Per-capability circuit breaker

Three-state breaker (Closed → Open → Half-Open → Closed) per `CapabilityID`. Opens after 5
consecutive failures in a 60s window. Half-opens after 30s cooldown: admits 1 request; if it
succeeds, transitions Closed; if it fails, transitions back to Open for another 30s.

The breaker is **observation-aware**: it reads from the existing audit stream
(`storage.AuditStore.ListByCorrelation` or `AuditQuery` — Phase 18/19 read paths) and counts
failures by `CapabilityID`. No new audit infra; it reuses Phase 18/19 evidence.

**R93-②: Audit-derived breaker state must distinguish unavailable evidence from zero failures.**

The breaker MUST NOT recreate the Phase 18 false-clean problem. Specifically:

```
audit readable
    → breaker may evaluate evidence
    → failure count = N (may be 0 if genuinely clean)

audit unreadable / query error / truncated
    → breaker state is UNKNOWN
    → protection decision on unknown state: FAIL CLOSED
```

An unavailable failure signal must NOT silently leave a repeatedly failing capability in
`Closed`. If the breaker cannot read the audit stream, it must treat its own state as
unknown and reject (fail closed) rather than assume zero failures.

Additionally, the breaker MUST define:

- **Query scope**: recent bounded history (not global). A bounded audit query cannot be
  treated as complete failure history — Phase 19 makes truncation visible. The breaker
  acknowledges truncation: if the audit query returns `truncated=true`, the failure count
  is a *lower bound*, not an exact count.
- **Window semantics**: the 60s window is a sliding window over the bounded query result.
  Failures older than the window do not count, but this is an *admission* decision, not a
  claim that older failures did not happen.

`Reject{Reason: "circuit_open"}` → 503 to client (signals "try later" — same family as
`evidence_unavailable` from Phase 18). `Reject{Reason: "breaker_unknown"}` → 503 (fail
closed on unreadable audit).

### S-4 — Bulk kill switch

Two flavors:
- **By capability tag** — set `Operation.Killed=true`; gates S-1/S-2/S-3 with a one-line check.
- **By principal** — set a `Principal.Killed=true` flag in a new `principal_state` table
  (storage v6 schema, additive, optional). When set, *all* capabilities for that principal
  return `Reject{Reason: "principal_killed"}` → 403.

The kill switch is **persistent** (survives restart) and **fast** (in-memory map, replicated
from storage at startup via the existing Phase 17.3 startup-scan discipline). It is the
"circuit breaker of last resort" — even if S-1/S-2/S-3 are misconfigured, the kill switch
guarantees an operator can stop damage in seconds.

**R93-③: Single persistence owner + startup fail-closed.**

There must be **exactly one** persistence owner for protection state. The architecture
MUST NOT allow:

```
Management  → own kill store
Protection  → another kill map
Plugin      → its own kill state
```

ADR-047 must freeze:

> **Protection owns Protection State persistence.** The in-memory map is only a runtime
> projection of that persistent state — not a competing authority.

The data flow is unidirectional:

```
persistent state (storage)
      ↓
bootstrap load (at startup)
      ↓
in-memory fast gate (runtime)
```

NOT:

```
runtime memory  ↔  database    (two competing authorities)
```

**Startup behavior** must be explicitly defined:

> If persistent kill-state cannot be loaded at startup (storage error, schema mismatch,
> corruption), **protection fails closed** — all capabilities are treated as killed until
> the operator explicitly clears the state. The system must NOT default to "all un-killed"
> when it cannot verify its own protection state.

Updates to kill-state at runtime flow through the management write surface (Phase 17.2
`:8082`), written to persistent storage first, then projected to the in-memory gate. The
in-memory gate never independently mutates persistent state.

### S-5 — Bounded concurrency per capability

A semaphore per `CapabilityID` (default 8 concurrent executions). Admittance checks before
S-1 token consumption (so a flood does not even consume tokens).

`Reject{Reason: "concurrency_exceeded"}` → 503 (Retry-After: 1s).

This is the cheapest guard against resource exhaustion: a runaway capability cannot hold
more than N goroutines, regardless of rate.

---

## 3. Iron laws (R21-0..R21-7)

- **R21-0** `external/v1` unchanged — read surface only.
- **R21-1** Kill switch state exposed via `:8082` `GET /management/v1/protection/kills` (read-only).
- **R21-2** **No new dependency** — stdlib only. No `golang.org/x/time/rate` (already vendored? no — keep zero new). Token bucket hand-rolled in ~30 LoC; circuit breaker hand-rolled in ~80 LoC.
- **R21-3** Frozen package set (`internal/platform`, `internal/governance`,
  `internal/plugin/runtime`, `internal/plugin/isolation`, `internal/controlplane/hostregistry`)
  remains at zero diff. **Protection sits between request boundary and executor; it does NOT
  touch either.**
- **R21-4** No mutation of execution semantics — `Reject` is the same shape as RBAC/policy denial.
- **R21-5** Context propagation only — `context.Context` carries the deadline (S-2); goroutine-
  local is forbidden.
- **R21-6** **Protection gates are auditable** — every `Reject` produces an audit **event**
  with a dedicated `protection.*` action vocabulary. **R93-④: Protection rejects are NOT
  Policy mutations.** A protection reject does not mutate policy, capability, or resource
  state — therefore it MUST NOT use the Policy mutation `INTENT → CAS → OUTCOME` semantics.
  Instead, it uses a dedicated observation/event vocabulary:

  ```
  protection.rate_limited      — S-1 token bucket exhausted
  protection.timeout           — S-2 deadline exceeded
  protection.circuit_open      — S-3 breaker Open state
  protection.breaker_unknown   — S-3 audit unreadable, fail closed
  protection.killed            — S-4 bulk kill switch active
  protection.principal_killed  — S-4 principal-level kill
  protection.concurrency_exceeded — S-5 semaphore full
  ```

  The essential invariant: **Protection rejects must be auditable without pretending that a
  policy/resource mutation occurred.** Operators can query Phase 18/19 audit by
  `action=protection.*` to see protection pressure over time. No silent rejection.
- **R21-7** **Bounded & honest** — `RejectedCount` exposed for each guard (`/metrics` Phase 19
  text surface extended). Follows Phase 18 discipline: counters are *exact*, not windowed.

---

## 4. Out of scope (explicitly)

- **Distributed control plane** — single-process posture maintained (R90 ruling preserved).
- **Distributed rate limit / cross-node circuit breaker** — out; would require cluster
  coordination that Phase 21 does not introduce.
- **Memory / CPU quotas per capability** — Phase 22+ candidate (requires `runtime/mem` polling
  or cgroup integration; too large for Phase 21).
- **UI for the kill switch** — Phase 22 (Dashboard-UI Major).
- **Mutation of plugin runtime behavior on reject** — out; plugins see `context.DeadlineExceeded`
  via existing context plumbing; they must already handle this (the contract has always required it).
- **Auto-tuning** — `Operation.Limit`, `Operation.Timeout`, breaker thresholds are *operator-set*
  values. No ML, no adaptive rate limiting. Discipline: the human chooses.

---

## 5. Sign-off discipline

Per ADR-021 three-tier discipline:

1. **R93 (COMPLETE — verdict B)** — Scope sign-off received with four modifications:
   - R93-①: S-2 timeout = cancellation signal, not guaranteed termination.
   - R93-②: S-3 breaker distinguishes unavailable evidence from zero failures; fail closed.
   - R93-③: S-4 single persistence owner; startup load failure is fail-closed.
   - R93-④: R21-6 protection rejects use `protection.*` event vocabulary, not Policy mutation
     INTENT→CAS→OUTCOME.
   
   All four modifications applied to this ADR. S-1 (rate limit) and S-5 (concurrency) accepted
   as-is. Default values accepted as deployment policy, not Runtime Contract semantics.
   Phase 20 sequencing rule confirmed: Phase 20 implementation must land before Phase 21
   consumes its trace data.

2. **R94 (next)** — Architecture sign-off on ADR-047 (concrete package surface, schema additions,
   wiring order, property tests). Must incorporate all four R93 modifications as binding
   architectural constraints.
3. **R95** — Implementation sign-off on ADR-048 (file inventory, mutation tests, exact LoC
   per commit).

ADR-021 three-tier discipline holds. No code touches the repo before R95 sign-off.

---

## 6. Companion ADRs

- ADR-045 (Phase 20 Implementation, ACCEPTED at R92) — implementation authorized but not yet
  committed; **must** be landed before or alongside Phase 21's first commit, to avoid two
  Majors in flight simultaneously. Phase 21 ADR-047/048 will reference the existing trace
  ring (when landed) for S-3 failure-rate counting.
- ADR-039 (Phase 18 Evidence Integrity) — Phase 21's S-3 reads from the existing audit stream;
  the truncation honesty and `evidence_unavailable` 503 mapping are inherited directly.
- ADR-041 (Phase 19 Evidence Consumers) — Phase 21's R21-7 extends `/metrics` text surface with
  protection counters; this is the only Phase 19 surface that grows.

---

## 7. R93 verdict and modifications applied

**R93 = B — ACCEPT WITH MODIFICATIONS.**

Phase 21 = Operational Protection is accepted as the right next Major. S-1 (rate limit) and
S-5 (concurrency) accepted as-is. Four scope-level modifications applied (R93-① through R93-④),
detailed in their respective sections above.

**Default values** (accepted as deployment policy, not Runtime Contract semantics):

```
rate:        60 / 60s
concurrency: 8
timeout:     30s
breaker:     5 failures / 60s
cooldown:    30s
```

ADR-047 must explicitly state: "Defaults are deployment policy, not Runtime Contract semantics.
Do not freeze these as immutable behavioral constants."

**Phase 20 sequencing** (confirmed): Phase 20 implementation (ADR-045) must land before Phase 21
consumes its trace data. No two unresolved Major implementations silently interleave.

**Next**: ADR-047 Architecture, incorporating all four R93 modifications as binding constraints.
No code before R94 sign-off.

Full text: this ADR (commit pending). Nothing implemented. ADR-021 three-tier discipline holds.