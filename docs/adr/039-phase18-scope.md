# ADR-039 — Phase 18: Evidence Integrity (Scope)

- **Status**: **SIGNED (Round 84 = A — Accept)**. Phase 18 = *Evidence Integrity* approved;
  ADR-040 Architecture drafting authorized. **No implementation until ADR-040 is signed.**
- **Date**: 2026-08-10
- **Companion to**: ADR-037/038 (Phase 17.3, **CLOSED** R80-A / R82-A / R83-A), ADR-021 (Architecture
  Baseline / three-tier discipline, frozen), ADR-015 (Observability Architecture, CLOSED),
  ADR-023/024 (Phase 11 External Interface, CLOSED)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 18 — the **read** side of the audit contract. Phase 17 made the write path
  auditable. Phase 18 makes reading that audit **honest**.

---

## 0. Abstract

Phase 17 is complete: `/management/v1` (`:8082`) implements INTENT → CAS → OUTCOME, is fail-closed,
physically isolated from the frozen `external/v1` read contract, has a strict-`409` replay guard, and
carries a read-only reconciliation observer.

The obvious next move is "more observability" — audit projections, metrics export, tracing. **This ADR
argues that would be premature**, because the evidence surfaces OpsCore already ships can report
**"all clear" while blind**. Adding dashboards on top of a read path that silently lies is how a
governance system loses its only real product: trustworthy evidence.

Phase 18 is therefore proposed as **Evidence Integrity**: make every read surface incapable of
producing a false-clean or a silent truncation. It adds **zero** new mutation, **zero** new surface
area beyond metadata fields, and stays strictly inside `observe > control`.

---

## 1. Positioning — why not "Phase 18 = Observability features"

Phase 17.3's own thesis was *"the trustworthiness of the write-audit itself"*. That thesis was only
half-delivered: the **write** side is now provably honest (three-phase protocol, no synthesis without
provable attribution, mechanically guarded). The **read** side, shipped in the same phase, is not.

A metrics/tracing phase would build new consumers on top of a producer that can silently under-report.
Every such consumer inherits the defect and makes it harder to fix. Integrity of the evidence pipeline
is a **prerequisite** to projecting it, not a follow-up to it.

> Phase 17: the system cannot lie about what it *did*.
> Phase 18: the system cannot lie about what it *sees*.

---

## 2. Evidence for the problem (code as of `85e7409`)

Four concrete findings in code shipped by Phase 17.3 / Phase 8.1. All are in **non-frozen** packages.

### 2.1 F-1 — `Scan` reports a false-clean on store failure

`internal/management/reconcile.go`:

```go
events, err := r.audit.List(scanCap)
if err != nil {
    return nil            // failure is indistinguishable from "healthy"
}
```

`handleReconcile` then converts `nil` → `[]` and answers **`200 OK []`**. An operator asking
"are there orphaned intents?" receives *"no"* when the correct answer is *"the audit store is
unreadable"*. `ScanAtStartup` logs `0 entries, 0 non-closed`, which reads as a clean bill of health.

The same swallow exists per-row: `ListByCorrelation` error → `continue`, so an intent silently
disappears from the report rather than being flagged as unexaminable.

This is the exact failure mode Phase 17.3 was chartered to prevent, reproduced on the read side.

### 2.2 F-2 — `Scan` truncates its evidence window silently

`scanCap = 1000` caps the scan at the newest 1000 audit rows. Beyond that, older orphaned intents are
never inspected and the report carries **no truncation signal**. "No findings" therefore does not mean
"no orphans"; it means "no orphans in an unstated window". A governance observer that cannot qualify
the scope of its own negative result is not evidence — it is reassurance.

### 2.3 F-3 — `GET /management/v1/audit` filters *after* limiting (misleading `limit` semantics)

`internal/management/server.go` `handleListAudit`: `audit.List(limit)` first, then in-memory
`policy` / `result` filtering. Consequences at **any** table size larger than `limit` (default 100):

- `?policy=X&limit=100` returns `[]` whenever X has no event among the newest 100 rows — even if X has
  hundreds of audit events. A false empty on the primary "investigate this policy" query.
- To any API consumer, `limit` means *rows returned*. Here it means *rows considered*. The filter
  cannot ever return more than `limit` **minus whatever it discarded**.

ADR-038 recorded this as **OQ-17.3-2, a future *performance* concern**. On re-reading the code I
disagree with that characterisation and record the disagreement here: it is a **correctness** defect
today, independent of table size, and scale only makes it more likely to be hit. Predicate pushdown is
the fix, but the reason is honesty, not throughput.

### 2.4 F-4 — `observability.Collector` is unbounded

`internal/observability/collector.go`: `c.obs = append(c.obs, o)` with no capacity, eviction, or
drop accounting. Every observed execution/sandbox/signature/audit event is retained for process
lifetime. In any long-running deployment this is monotonic memory growth, and — the part that matters
for this phase — there is no way for a reader to know whether the read model is complete.

---

## 3. Phase 18 scope — Evidence Integrity

### 3.1 Candidate scope items (proposed)

| # | Item | Fixes | Surface |
|---|---|---|---|
| S-1 | **No false-clean.** Evidence reads distinguish *"scanned, found nothing"* from *"could not scan"*. `Scan` returns `(report, error)`; `GET /reconciliation` maps a store failure to an explicit error status, never `200 []`. Startup log states failure as failure. | F-1 | `internal/management` |
| S-2 | **No silent truncation.** Every capped scan/query returns window metadata (`scanned`, `cap`, `truncated`) alongside findings, so a negative result is always qualified. Report envelope becomes `{window: {...}, entries: [...]}`. | F-2 | `internal/management` |
| S-3 | **Filter before limit.** Additive `AuditStore.Query(AuditQuery)` with predicate pushdown into SQLite (`WHERE target = ? AND result = ? ORDER BY id DESC LIMIT ?`); `limit` recovers its ordinary meaning. Index-only, **no schema migration**. | F-3 | `internal/storage`, `internal/storage/sqlite`, `internal/management` |
| S-4 | **Bounded read model.** `observability.Collector` gains an explicit capacity with FIFO eviction and a `Dropped()` accessor; aggregate counters stay exact (they are O(1) and lose nothing). Truncation becomes visible, never silent. | F-4 | `internal/observability` |

### 3.2 MUST (Phase 18 iron laws)

- **R18-0** `external/v1` Public Contract **UNCHANGED**. No new external route, no shape change.
- **R18-1** **No false-clean**: an evidence surface MUST NOT return a success-shaped empty result when
  its underlying read failed.
- **R18-2** **No silent truncation**: any capped read MUST return machine-readable window metadata.
- **R18-3** **No new mutation.** Phase 18 adds no write path, no verb, no state transition.
  `ReconcileForward` stays an uninvoked seam (**OQ-17.3-1 remains closed**; activating it needs its own
  Scope ADR).
- **R18-4** **No Control Plane / scheduler / worker / background loop.** On-demand and startup-pass only.
- **R18-5** **Frozen packages unmodified** (`platform`, `governance`, `plugin/{runtime,isolation}`,
  `controlplane/hostregistry`, `external`, `governancepolicy`, `platformview`) — `git diff` empty.
- **R18-6** **Additive storage only.** No schema migration; index creation permitted inside the existing
  migration chain, never in the v1 baseline.
- **R18-7** **No new dependency.** No Prometheus client, no OTel SDK, no metrics library.
- **R18-8** **Audit is append-only.** Phase 18 introduces no deletion, no retention job, no compaction.
  Destroying evidence is a governance decision requiring its own Scope ADR.

### 3.3 SHOULD (Phase 18)

- Report envelopes stay stable and parseable; changes to `GET /reconciliation` are versioned in the
  response body, not by a new route.
- Every new law gets a mutation test that is proven to fail before the fix lands (standing discipline).
- The truncation/failure semantics are identical across `Scan`, `GET /audit`, and the startup pass —
  one rule, three call sites.

### 3.4 Out of scope (Phase 18)

- Metrics **export** (`/metrics`, Prometheus text format, OTel) — deliberately deferred until the
  producer is honest. Natural Phase 19 candidate.
- Distributed tracing propagation.
- Audit projection / materialised views / dashboards.
- Audit retention, archival, deletion (see R18-8).
- Any `management/v1` write capability expansion.
- Activating `ReconcileForward` (OQ-17.3-1).
- `internal/plugin/runtime` gofmt drift (OQ-17.3-3) — frozen zero-diff still outranks formatting.

---

## 4. Decision requested (Round 84)

**(A) Accept** Phase 18 = *Evidence Integrity* with scope items S-1…S-4 and laws R18-0…R18-8 —
authorising a Phase 18 Architecture ADR (ADR-040), no code before it is signed.

**(B) Accept with modification** — e.g. narrow to S-1+S-2 (management-only, defer storage and
observability changes), or split S-4 into its own phase.

**(C) Reject the framing** — proceed instead with observability projection/metrics as the next Major,
treating F-1…F-4 as a defect-repair round (a "Phase 17.4") rather than a phase theme.

**(D) Other.**

Note the framing disagreement in §2.3 is deliberate and is put forward for adjudication: OQ-17.3-2 was
recorded as a performance item; this ADR classifies it as a correctness defect.

---

## 5. Phase 18 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 18-scope | ADR-039 Phase 18 Scope (this ADR) | **SIGNED (R84-A)** |
| 18-arch | ADR-040 Phase 18 Architecture | **authorized — drafting (R85 sign-off pending)** |
| 18-impl | Implementation + mutation tests | blocked — after 18-arch sign-off |

Each step authorized only after the previous is signed (ADR-021).

Revised global ordering accepted at R84:

```
Phase 17    write correctness / mutation audit integrity      CLOSED
Phase 17.3  reconciliation visibility                         CLOSED
Phase 18    evidence integrity → trustworthy observations     ← here
Phase 19    projections / metrics / tracing                   deferred
```

---

## 6. Sign-off record (Round 84)

| Item | Verdict | Round |
|---|---|---|
| Phase 18 Scope framing (Evidence Integrity before projection) | ✅ SIGNED (A) | 84 |
| §2.3 reclassification: OQ-17.3-2 is correctness, not performance | ✅ ACCEPTED — R83 classification formally revised | 84 |
| S-1 No false-clean | ✅ ACCEPT (mandatory) | 84 |
| S-2 No silent truncation | ✅ ACCEPT (metadata is semantic, not cosmetic) | 84 |
| S-3 Filter before limit (additive `AuditQuery`) | ✅ ACCEPT — promoted to contract correctness | 84 |
| S-4 Bounded observability collector | ✅ ACCEPT **with clarification** (see §7) | 84 |
| R18-0…R18-8 iron laws | ✅ ACCEPT (all) | 84 |

*Scope signed. ADR-040 Architecture authorized. No code before ADR-040 approval.*

---

## 7. R84 carry-forward into ADR-040

The Round 84 sign-off added one binding clarification and one set of required answers.

### 7.1 S-4 clarification (binding)

A bounded collector must not become **another** silent false-clean source. After eviction the read
model MUST NOT present itself as complete. ADR-040 must define an explicit completeness contract —
e.g. `Capacity()`, `DroppedCount()`, `Complete()` or equivalent — so any consumer can tell
"the read model saw everything" from "the read model is a bounded window".

### 7.2 Required ADR-040 answers

- **Audit query model** — `AuditQuery` shape; filtering semantics; `limit` semantics; ordering
  guarantee; and the distinction between *empty result* and *unavailable result*.
- **Reconciliation result contract** — three distinct states, never collapsed:
  `verified empty` / `unknown (scan failure)` / `truncated (incomplete scan)`.
- **Collector contract** — capacity, eviction semantics, dropped accounting, completeness semantics.
- **API-explosion avoidance** — one `Query(AuditQuery)` rather than `ListByPolicy` /
  `ListByResult` / `ListByPolicyAndResult`; existing `AuditStore` callers unchanged; additive only;
  no retention or deletion semantics anywhere.
