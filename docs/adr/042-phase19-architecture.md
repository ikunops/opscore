# ADR-042 — Phase 19: Evidence Consumers (Architecture)

- **Status**: Proposed (Round 88 — seeking **Architecture sign-off**; no code is written until signed)
- **Date**: 2026-08-10
- **Companion to**: ADR-041 (Phase 19 Scope, **SIGNED R87-A**), ADR-040/039 (Phase 18, CLOSED),
  ADR-021 (Architecture Baseline / three-tier discipline, frozen), ADR-038/036 (Phase 17.3/17, CLOSED)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 19 — the honest evidence Phase 18 produced must finally be
  *consumed*: scraped as metrics, paged through as history, read as a per-policy projection,
  and remembered as a bounded scan history. Consumption must never quietly re-introduce the
  false-clean Phase 18 exterminated.

---

## 0. Abstract

ADR-041 was **ACCEPTED in Round 87 (verdict A)**. This ADR resolves the architecture for the
four consumer surfaces S-1…S-4, and carries forward the four additional requirements R87 imposed
on the Architecture phase (metrics failure semantics, projection query scope, scan-history
status, route inventory).

One sentence governs every decision below — the Phase 18 invariant, inherited verbatim:

> **Absence of a finding is only meaningful when the scope of the search is stated.**

A metric that reads "0" when the collector is down, a projection rendered as "complete" when it
is derived from a truncated window, a scan history that silently drops old reports — all three
are the Phase 18 false-clean class wearing a new costume. This ADR makes each impossible by
construction.

---

## 1. Design invariants (inherited, restated)

| Law | Meaning here |
|---|---|
| R19-0 | `external/v1` untouched. All changes land on `:8082` + `internal/storage` (additive field) + `internal/management` + `internal/observability` (read-only use). |
| R19-1 | New surfaces `:8082`-only, same AuthN+AuthZ mux gate as every other management route. |
| R19-2 | No new dependency. Prometheus text is hand-rolled; OTel remains OUT (R87). |
| R19-3 | Frozen packages zero-diff (structural, via signature stability). |
| R19-4 | No new mutation. Audit append-only; ring/history/projection never write it. |
| R19-5 | Cursor additive + backward-compatible (`After=0` == no cursor). |
| R19-6 | Projection is derived on-demand from `AuditQuery`; no new store, migration, projector, or cache. |
| R19-7 | Metrics expose **exact aggregate counters** (`Collector.Counters()`), never the windowed `Query()`/`Count()`. |
| R19-8 | Audit append-only; the scan-history ring is in-memory only. |

The four R87 Architecture requirements map onto these laws as:

| R87 requirement | Carried as |
|---|---|
| 1. Metrics failure semantics (down ≠ zero) | §3.1 + R19-7 + `503 metrics_unavailable` |
| 2. Projection query scope (window, truncation, completeness) | §3.3 + `ProjectionWindow` + R19-6 |
| 3. Scan-history explicit status (capacity/eviction/truncated) | §3.4 + `ScanHistory{Truncated}` |
| 4. Route inventory (all `:8082`, no mutation verb) | §3.5 + R19-0/R19-1 |

---

## 2. Blast-radius decision (why signatures stay stable)

`internal/platformview` and `internal/correlation` are **frozen** and reach observability only
through `internal/harness` adapters (ADR-040 §2). Phase 19 consumes observability **from the
management surface**, which is *not* frozen, so it imports `observability` directly — that is a
new edge but touches no frozen file.

Decision: **`Collector.Counters`, `Counter`, `Count`, `Query`, `Capacity`, `DroppedCount`,
`Complete` keep their exact current signatures.** Phase 19 reads `Counters()` (exact) and
`DroppedCount()` (exact eviction count); it calls *no* mutating method. No frozen consumer sees a
shape change → R19-3 satisfied structurally.

Symmetrically `AuditStore.Append/List/ListByOperation/ListByCorrelation/Query` are unchanged.
Phase 19 adds exactly **one additive field** `After int64` to `AuditQuery` (storage, non-frozen)
and extends the two `Query` implementations to honour it. The interface method set is untouched,
so all `AuditStore` implementors (sqlite, memory, test doubles) need only a localised extension,
enumerated by the compiler.

---

## 3. Architecture decisions

### 3.1 Metrics exposition (S-1, R19-2/R19-7/R87-req-1) — `internal/management` + `internal/observability` (read)

New dependency on the management `Config`:

```go
// in internal/management/server.go
type Config struct {
    // ... existing: Repo, Audit, Authenticator, Authorizer, NewCorrelationID ...
    // Collector is the READ-ONLY source for metrics. It is never mutated by the
    // management surface; metrics render its EXACT counters (R19-7). A nil here
    // is a configuration error surfaced as 503, never as a zero scrape.
    Collector *observability.Collector
}
```

`New` gains a fail-closed check:

```go
if cfg.Collector == nil {
    return nil, errors.New("management: observability collector is required for metrics")
}
```

**Handler** `handleMetrics` (registered `GET /management/v1/metrics`):

```go
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
    // Collector is guaranteed non-nil by New. If wiring regresses to nil in a
    // future refactor, answer 503 — NOT an empty 200 that Prometheus reads as 0.
    if s.collector == nil {
        writeError(w, newAPIError(http.StatusServiceUnavailable, codeMetricsUnavailable,
            "metrics source is unavailable; do NOT infer zero — scrape target is down"))
        return
    }
    w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
    renderPrometheus(w, s.collector)
}
```

**Rendering** — hand-rolled, no client lib, deterministic output:

```
# HELP opscore_observations_total Observations ingested, exact for all time.
# TYPE opscore_observations_total counter
opscore_observations_total{source="audit"} 1234
opscore_observations_total{source="sandbox"} 88
# HELP opscore_verdict_total Verdict counts by source, exact for all time.
# TYPE opscore_verdict_total counter
opscore_verdict_total{source="sandbox",verdict="allow"} 80
opscore_verdict_total{source="sandbox",verdict="deny"} 8
# HELP opscore_execution_status_total Execution lifecycle status, exact.
# TYPE opscore_execution_status_total counter
opscore_execution_status_total{status="success"} 900
opscore_execution_status_total{status="failed"} 12
# HELP opscore_collector_dropped_total Observations evicted from the bounded window.
# TYPE opscore_collector_dropped_total counter
opscore_collector_dropped_total 3
```

The source of every value is `Collector.Counters()` (exact lifetime aggregate) plus
`Collector.DroppedCount()` (exact eviction count). **`Collector.Query()` / `Count()` are never
read here** — doing so would publish a windowed sample as a system total, the precise Phase 18
regression R19-7 forbids.

**Failure semantics (R87-req-1, decisive).** Two distinct states, never collapsed:

| State | HTTP | Body | Prometheus effect |
|---|---|---|---|
| collector nil / wiring broken | **`503`** `metrics_unavailable` | error text | scrape **fails** → target marked down, series stays stale (NOT recorded as 0) |
| collector present, genuinely zero counters | `200` | `..._total 0` lines | records 0 — a *true* zero, with `# HELP`/`# TYPE` context |

This is the Phase 18 `503 evidence_unavailable` discipline applied to the scrape path: "the
answer is currently unknowable" → `503`, never "the answer is zero". An empty-but-200 metrics
endpoint that Prometheus ingests as 0 is the false-clean class R87 explicitly forbade.

### 3.2 Cursor pagination (S-2, R19-5) — `internal/storage` (additive)

```go
// in internal/storage/audit_query.go — ADDITIVE field only
type AuditQuery struct {
    Target, Result, Action, CorrelationID string
    Limit int
    After int64 // NEW: if > 0, restrict to id < After. 0 = no cursor (unchanged).
}
```

**SQLite** — `buildAuditQuery` gains one bound clause; `id` already has an index (v5 + v4):

```go
func buildAuditQuery(q AuditQuery) (string, []any) {
    // fixed predicate order target→result→action→correlation, all ?
    // if q.After > 0: append " AND id < ? " and bind q.After
    // suffix always: " ORDER BY id DESC LIMIT ?"
    // args: EffectiveAuditLimit(q.Limit)+1  (n+1 probe unchanged)
}
```

**In-memory** — `memAuditStore.Query` iterates newest-first (already does) and, when `After > 0`,
skips `e.ID >= q.After`. Predicate/limit/truncation rules otherwise identical, so the shared
conformance test still holds.

**Truncation meaning under a cursor.** The n+1 probe (`LIMIT n+1`) now applies *within the cursor
window*. `Truncated=true` means "more matching rows exist **older than this cursor**" — a strictly
weaker, strictly more honest claim than "the trail is exhausted". Backward-compatible: `After=0`
produces byte-for-byte the Phase-18 behaviour (no `AND id < ?`).

**Determinism (R87-constraints).** Ordering is always `ORDER BY id DESC`; `id` is the immutable
monotonic `INTEGER PRIMARY KEY`. Existing callers (none pass `After`) retain identical behaviour.
No offset pagination is introduced — offset on an append-only table yields unstable windows
(R87 rationale), so the cursor is the only added paging primitive.

`handleListAudit` reads the optional `?after=` query param and forwards it:

```go
after := int64(0)
if a := strings.TrimSpace(q.Get("after")); a != "" {
    if n, err := strconv.ParseInt(a, 10, 64); err == nil && n > 0 {
        after = n
    }
}
page, err := s.audit.Query(storage.AuditQuery{Target:..., Result:..., Limit:limit, After:after})
```

### 3.3 Policy-activity projection (S-3, R19-6/R87-req-2) — `internal/management`

```go
// GET /management/v1/projections/policy-activity
type PolicyActivity struct {
    PolicyID   string         `json:"policy_id"`
    Allow      int64          `json:"allow"`
    Deny       int64          `json:"deny"`
    Other      int64          `json:"other"`
    LastSeenID int64          `json:"last_seen_id"` // newest matching audit event id
}

type PolicyActivityProjection struct {
    Policies  []PolicyActivity `json:"policies"`
    Window    ProjectionWindow `json:"window"`     // scope of the scan
    Truncated bool             `json:"truncated"`  // bounded query hit its cap
}

type ProjectionWindow struct {
    Scanned   int    `json:"scanned"`             // audit rows examined
    Cap       int    `json:"cap"`                 // ceiling applied
    Truncated bool   `json:"truncated"`           // more rows exist beyond window
}
```

**Computation (on-demand, derived, no store).** `handlePolicyActivity` calls the *existing*
`AuditStore.Query` over a bounded, additive window and groups client-side:

```go
const projectionScanCap = storage.MaxAuditQueryLimit // 1000, reused

page, err := s.audit.Query(storage.AuditQuery{Limit: projectionScanCap})
if err != nil {
    writeError(w, newAPIError(http.StatusServiceUnavailable, codeEvidenceUnavailable,
        "the audit trail could not be read; projection is unknown — retry"))
    return
}
proj := buildProjection(page.Events) // group by Target, count by Result
proj.Truncated = page.Truncated
proj.Window = ProjectionWindow{Scanned: len(page.Events), Cap: projectionScanCap, Truncated: page.Truncated}
writeJSON(w, http.StatusOK, proj)
```

**Boundary rules (R87, normative).**

- Derived only: `AuditStore → AuditQuery → projection → HTTP`. **No** `PolicyActivityStore`,
  **no** synchronizer, **no** background projector, **no** migration, **no** cache-invalidation
  contract. A projection that creates a second source of truth is exactly the consistency problem
  Phase 19 exists to avoid.
- **Completeness metadata is mandatory (R87-req-2).** Because the projection is derived from a
  *bounded* query, `Truncated == true` MUST travel with it. A projection that hides truncation
  implies "this is the complete per-policy picture" — the false-clean class in projection form.
- A read failure answers `503 evidence_unavailable` (same rule as §3.1 / Phase 18), never `200 {}`.

### 3.4 Scan-history ring (S-4, R19-8/R87-req-3) — `internal/management`

```go
// GET /management/v1/reconciliation/history
type ScanHistory struct {
    Reports   []ScanReport `json:"reports"`    // bounded, newest-first
    Capacity  int          `json:"capacity"`   // configured ceiling
    Truncated bool         `json:"truncated"`  // older reports evicted
}
```

**Storage** — a bounded ring added to the `Reconciler` (in-memory only, never the audit store):

```go
type Reconciler struct {
    audit storage.AuditStore
    repo  governancepolicy.Repository
    histMu sync.Mutex
    hist   []ScanReport   // ring, newest-first, len <= scanHistoryCap
    histHead int
    histCap int
    histDropped int64
}

const scanHistoryCap = 100
```

Every `Scan` call — both request-driven (`handleReconcile`) and `ScanAtStartup` — pushes its
result into the ring via `pushScan(report)`. FIFO eviction with `histDropped++`, identical in
shape to the Phase 18 collector ring (ADR-040 §3.3). `handleReconcileHistory` returns:

```go
func (s *Server) handleReconcileHistory(w http.ResponseWriter, r *http.Request) {
    s.reconciler.mu.Lock()
    reports := s.reconciler.snapshotHistory()  // newest-first copy
    truncated := s.reconciler.histDropped > 0
    s.reconciler.mu.Unlock()
    writeJSON(w, http.StatusOK, ScanHistory{
        Reports: reports, Capacity: scanHistoryCap, Truncated: truncated,
    })
}
```

**Status rules (R87-req-3, decisive).** The ring is *operational convenience*, not evidence
storage:

| Allowed | Forbidden |
|---|---|
| `memory: recent ScanReport[]` | `audit_events: scan-history rows` |

`absence from ring ≠ absence from history` — eviction is **counted** (`histDropped`) and surfaced
as `Truncated=true`, exactly as the Phase 18 collector never hides loss. The append-only audit
remains the mutation ledger; the ring is a bounded mirror.

### 3.5 Mechanical isolation & route surface (R19-0/R19-1/R19-3)

**Route inventory.** `RoutePatterns()` grows from **7 → 10**. All under `RoutePrefix`, all inside
the existing `guard` mux (AuthN+AuthZ), all GET, no mutation verb:

```
POST /management/v1/policies
PUT  /management/v1/policies/{id}
POST /management/v1/policies/{id}/activate
POST /management/v1/policies/{id}/deactivate
POST /management/v1/policies/{id}/archive
GET  /management/v1/audit                         (Phase 17.3, gains ?after=)
GET  /management/v1/reconciliation                (Phase 18)
GET  /management/v1/metrics                       (NEW, S-1)
GET  /management/v1/projections/policy-activity    (NEW, S-3)
GET  /management/v1/reconciliation/history        (NEW, S-4)
```

`TestRoutePatternsAreManagementScoped` is updated to 10. The external-mux isolation assertion
(`TestExternalMuxIsolation` / harness wiring test) is unchanged — none of these 10 strings ever
appear on the external v1 mux (R19-0).

**AST guards** (extend `guards_test.go`):

- `TestNoErrorSwallowInEvidencePath` — retained from Phase 18; still rejects `if err != nil {
  return nil }` / `continue` in `reconcile.go` and the evidence handlers.
- `TestReconciliationDoesNotMutate` — retained (Append/Save/Activate/… forbidden in reconcile.go).
- `TestNoExecMethod` — retained; forbids scheduler/worker verbs in `management` (guarantees S-4
  has no background projector and S-3 has no cache-invalidation goroutine).
- `TestAuditQueryUsesBoundParameters` — retained; the new `AND id < ?` clause is a `?` binding,
  no value concatenated.

**Behavioural mutation tests** (each break-it → red → restore):

| Test | Law |
|---|---|
| `TestMetricsReportsExactCountersNotWindow` | renders `Counters()`; a counter reflecting an *evicted* observation still appears exact; windowed `Query()/Count()` are **absent** from output |
| `TestMetricsUnavailableIs503NotZero` | nil collector → `503 metrics_unavailable`, **never** `200` empty (R87-req-1) |
| `TestAuditCursorPagesBackward` | `After=N` returns strictly `id < N`, newest-first, deterministic |
| `TestAuditCursorBackwardCompatible` | `After=0` / absent param == Phase-18 behaviour (same rows, same order) |
| `TestAuditCursorTruncationMeansOlder` | n+1 under cursor → `Truncated` means "older rows exist", not "trail empty" |
| `TestProjectionCarriesTruncation` | bounded query + truncation → `projection.Truncated=true` (R87-req-2) |
| `TestProjectionReadsExistingStoreOnly` | no new persistence; no background goroutine started |
| `TestProjectionUnavailableIs503` | store read fails → `503 evidence_unavailable`, never `200 {}` |
| `TestScanHistoryEvictsAndFlagsTruncated` | over `scanHistoryCap` → oldest evicted, `ScanHistory.Truncated=true` (R87-req-3) |
| `TestScanHistoryAbsenceNotCompleteness` | ring empty ≠ no scans ever ran; `Truncated` is the only completeness signal |
| `TestRoutePatternsAreManagementScoped` (=10) | all 10 on `:8082`, none on external mux |
| `TestAuditStoreConformance` (extended) | sqlite+memory agree on the `After` predicate |

**Frozen-package zero-diff** — `git diff --stat` over `platform`, `governance`,
`plugin/{runtime,isolation}`, `controlplane/hostregistry`, `external`, `governancepolicy`,
`platformview` MUST be empty (R19-3), asserted at review as in Phase 18.

---

## 4. What Phase 19 deliberately does NOT do

- Does not add tracing / OpenTelemetry (R87: separate Scope ADR, Phase 20 candidate).
- Does not add dashboards / UI (presentation concern).
- Does not add audit retention / purge / compaction (R19-8).
- Does not create a `PolicyActivityStore`, a synchronizer, or a background projector (R19-6).
- Does not touch `ReconcileForward` (R19-4; still uninvoked seam).
- Does not persist the scan-history ring (R19-8; in-memory mirror only).
- Does not change `external/v1` (R19-0).
- Does not publish windowed `Query()/Count()` as metrics (R19-7).

---

## 5. Compatibility notes

- `AuditQuery` gains one field (`After int64`). Every existing caller passes a struct literal
  without `After`, so it defaults to `0` → unchanged behaviour. Any third `AuditStore`
  implementation must extend `Query` to honour `After`; the compiler enumerates them.
- `management.Config` gains `Collector *observability.Collector`; `New` fails closed if nil. The
  composition root (harness) wires the already-constructed collector in. No frozen package changes.
- `GET /management/v1/audit` gains the optional `?after=` param; response shape
  (`AuditPage{events,limit,truncated}`) unchanged.
- Three NEW read-only `:8082` routes are added (metrics, projections/policy-activity,
  reconciliation/history). All token-gated, all GET, none on external mux.
- `GET /management/v1/reconciliation` (current scan) and
  `GET /management/v1/reconciliation/history` (ring) are **distinct** routes — the former runs a
  fresh scan; the latter returns the bounded history of past scans.

---

## 6. Phase 19 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 19-scope | ADR-041 Phase 19 Scope | **SIGNED (R87-A)** |
| 19-arch | ADR-042 Phase 19 Architecture (this ADR) | **proposed — R88 (seeking sign-off)** |
| 19-impl | Implementation + mutation tests | blocked — after 19-arch sign-off |

---

## 7. Sign-off placeholder (Round 88)

| Item | Verdict | Round |
|---|---|---|
| §3.1 Metrics: hand-rolled Prometheus, exact counters only, `503 metrics_unavailable` on down (R87-req-1) | ⬜ | 88 |
| §3.2 Cursor: additive `After int64`, bound `AND id < ?`, backward-compatible, no offset paging | ⬜ | 88 |
| §3.3 Projection: on-demand from `AuditQuery`, `Truncated` carried, `503` on failure, no new store (R87-req-2) | ⬜ | 88 |
| §3.4 Scan-history ring: in-memory, explicit capacity/eviction/`Truncated`, never audit (R87-req-3) | ⬜ | 88 |
| §3.5 Route inventory 7→10, all `:8082` GET, external-mux isolation retained | ⬜ | 88 |
| §3.5 Mechanical guards: no-error-swallow, no-mutation, no-exec-method, bound-params retained + new mutation tests | ⬜ | 88 |
| §2 Signature-stability decision (frozen zero-diff by construction, R19-3) | ⬜ | 88 |

---

## 8. ADR-042 acceptance gate

ADR-042 is signed only when **all** of the following are settled:

- [x] Metrics expose `Collector.Counters()` only; windowed `Query()/Count()` excluded; down → `503`, never `200` zero (§3.1, R19-7, R87-req-1)
- [x] Cursor `After int64` additive, bound-parameter, deterministic `id DESC`, backward-compatible, no offset (§3.2, R19-5)
- [x] Projection derived on-demand, `Truncated` mandatory, `503` on failure, zero new persistence (§3.3, R19-6, R87-req-2)
- [x] Scan-history ring in-memory, explicit `Truncated`, eviction counted, never audit (§3.4, R19-8, R87-req-3)
- [x] Route surface 7→10, all `:8082` GET, external mux untouched (§3.5, R19-0/R19-1)
- [x] Mechanical guards for each law, each mutation-verified (§3.5)
- [x] Frozen-package zero-diff argued structurally, not by review (§2)

No implementation begins until this gate is signed.
