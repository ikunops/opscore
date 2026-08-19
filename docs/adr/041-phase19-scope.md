# ADR-041 — Phase 19: Evidence Consumers (Scope)

- **Status**: **DRAFT — seeking Round 87 sign-off**. Phase 18 = *Evidence Integrity* is **CLOSED**
  (R84-A / R85-A / R86-A). This ADR proposes the consumer layer that Phase 18 earned.
  **No implementation until ADR-042 (Architecture) is signed.**
- **Date**: 2026-08-10
- **Companion to**: ADR-037/038 (Phase 17.3, CLOSED), ADR-039/040 (Phase 18, CLOSED),
  ADR-021 (three-tier discipline, frozen), ADR-015 (Observability Architecture, CLOSED)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 19 — the **consumer** side of the now-honest audit contract.
  Phase 18 made the evidence *readable without lying*. Phase 19 makes it *usable*.

---

## 0. Abstract

Phase 18 is complete. Every read surface is now incapable of a false-clean or a silent truncation:
`AuditStore.Query` pushes predicates into SQL and reports its window; the bounded `Collector`
carries an **exact** `Counter()`/`Counters()` plus a `Complete()` completeness bit; reconciliation
returns three honest states (`verified` / `truncated` / `unknown`) and fails with `503
evidence_unavailable` instead of `200 []`.

The honest evidence now *exists* — but **nothing consumes it**. Phase 19 is therefore proposed as
**Evidence Consumers**: expose the trustworthy evidence as (a) a metrics surface, (b) cursor-based
pagination over the audit, and (c) a lightweight policy-activity projection. It adds **zero** new
mutation, **zero** new dependency, and stays strictly inside `observe`.

> Phase 17: the system cannot lie about what it *did*.
> Phase 18: the system cannot lie about what it *sees*.
> Phase 19: the system lets you *read what it sees* — at scale, without re-inventing the lie.

---

## 1. Positioning — why "consumers" and not "more features"

The originally-proposed "Phase 18 = Observability / Read Model" was deliberately **downgraded** at
R84 to *Evidence Integrity* (ADR-039), because building consumers on a producer that can silently
under-report merely multiplies the defect. That precondition is now satisfied: the producer and the
reader are honest. The consumer layer is the **dependent** phase, not an alternative — it could not
have come before Phase 18, and now it should not be deferred further.

Two temptations must be resisted in scoping:

1. **The tracing trap.** "projections / metrics / *tracing*" was GPT's loose label for Phase 19.
   Distributed tracing == OpenTelemetry == a **new dependency** and a cross-cutting propagation
   concern that does not belong in a read-model phase. This ADR **scopes tracing OUT** (§3.4) and
   puts the call forward for adjudication, exactly as ADR-039 put the OQ-17.3-2 re-classification
   forward. If tracing is wanted, it earns its own Scope ADR.
2. **The dashboard trap.** Dashboards are a *presentation* concern. Phase 19 builds the API/data
   layer (metrics exposition + projections); a UI is explicitly out of scope.

---

## 2. Evidence for the gap (code as of `978ba75`)

Phase 18 fixed *correctness*. Phase 19 fixes the *capability gap* that correctness exposed.

### 2.1 E-1 — the aggregate/sample split is inert

`observability.Collector` (Phase 18) computes **exact** lifetime counters (`Counter()` /
`Counters()`) and a `Complete()` completeness bit — but **nothing reads them**. The most valuable
integrity guarantee Phase 18 shipped has no consumer. A metrics surface is the natural, and only
honest, reader of those counters.

### 2.2 E-2 — no way to page *back* through history

`AuditStore.Query` (Phase 18) supports predicates + a `Truncated` window signal, but **no cursor**.
Consumers that must walk a large audit history can only re-pull from the newest edge; the Phase 18
deferral ("`truncated` is a signal, not a paging contract") was explicitly left to Phase 19. Without
a cursor, "show me policy X's activity from last week" is unanswerable without re-scanning the whole
table.

### 2.3 E-3 — no per-policy read model

Operators investigating a policy must reconstruct its status by hand from the raw audit stream. The
Phase 17.3 `ScanReport` is **point-in-time** (one scan, three states); there is no *historical* or
*per-policy* projection. The deferred "audit projection / materialised view" (ADR-039 §3.4) remains
unbuilt.

### 2.4 E-4 — the management surface cannot be scraped

There is still no metrics endpoint on `:8082`. The original "more observability" goal is met only at
the data layer (Phase 18); the scrapeable surface it implies is absent. Standard observability
tooling cannot pull OpsCore's runtime truth.

---

## 3. Phase 19 scope — Evidence Consumers

### 3.1 Candidate scope items (proposed)

| # | Item | Fixes | Surface |
|---|---|---|---|
| S-1 | **Metrics exposition (exact counters).** Read-only `GET /management/v1/metrics` rendering `Collector.Counters()` as **hand-rolled Prometheus text** (no client lib). Reports the EXACT counters, never the windowed `Count()`. | E-1, E-4 | `internal/observability`, `internal/management` (`:8082`) |
| S-2 | **Cursor pagination for the audit query.** Additive `After int64` to `AuditQuery`; `buildAuditQuery` appends `AND id < ?` when set, reusing the existing `id` index. `limit` keeps its ordinary meaning; `Truncated` now means "more exists before this window". Client pages with the last event `id`. | E-2 | `internal/storage`, `internal/storage/sqlite`, `internal/management` |
| S-3 | **Policy-activity projection (read model).** Read-only `GET /management/v1/projections/policy-activity` summarising per-policy audit activity (recent events, intent/outcome counts, orphan flag) **computed on-demand from `AuditQuery`** — no new store, no schema migration. | E-3 | `internal/management` |
| S-4 | **Reconciliation history ring.** Bounded in-memory ring of recent `ScanReport`s (status + window + timestamp), exposed read-only; lets operators see trend (verified→truncated→unknown). In-memory only — never writes the append-only audit. | E-3 (historical) | `internal/management` |

### 3.2 MUST (Phase 19 iron laws)

- **R19-0** `external/v1` Public Contract **UNCHANGED** (inherit R18-0). No new external route, no shape change.
- **R19-1** New surfaces (`/metrics`, `/projections/*`) live on `:8082` only, token-gated, behind the
  existing management AuthN/AuthZ. Never `external/v1`.
- **R19-2** **No new dependency.** Prometheus text is hand-rolled (key-value lines); no Prometheus
  client, no OTel SDK. Tracing deferred (§3.4). New deps require their own Scope ADR.
- **R19-3** **Frozen packages unmodified** (`platform`, `governance`, `plugin/{runtime,isolation}`,
  `controlplane/hostregistry`, `external`, `governancepolicy`, `platformview`) — `git diff` empty.
  Phase 18's collector signatures stay; new endpoints reach the collector via the existing harness
  adapter (signature stability = R19-3 by construction).
- **R19-4** **No new mutation.** Phase 19 adds no write path, no verb, no state transition. Every
  item is a *reader* of existing audit/collector state. `ReconcileForward` stays uninvoked (OQ-17.3-1
  remains closed — its own Scope ADR if ever activated).
- **R19-5** **Cursor is additive & backward-compatible.** `limit`-only callers are unchanged; `After`
  is optional. `Truncated` is preserved (cursor + limit together express "more before this window").
- **R19-6** **Additive storage only.** S-3/S-4 compute on-demand from `AuditQuery` — no schema
  migration, no new table. Index creation permitted only within the existing migration chain, never
  the v1 baseline.
- **R19-7** **Metrics report EXACT counters, never the window.** `GET /metrics` MUST source
  `Collector.Counters()` (lifetime truth). Reporting the windowed `Count()` as "total" would be a
  Phase 18 regression — a false-clean *via metrics*. The aggregate/sample split is load-bearing here.
- **R19-8** **Audit is append-only** (inherit R18-8). S-4's scan-history ring is in-memory only and
  never appends to audit. No retention, deletion, or compaction.

### 3.3 SHOULD (Phase 19)

- New management routes bump `RoutePatterns()`; `TestRoutePatternsAreManagementScoped` is updated to
  the new count (currently 7 → 9 for `/metrics` + `/projections/policy-activity`; S-2 adds no route).
- Every new law gets a mutation test proven to fail before the fix lands (standing discipline).
- Prometheus exposition text is valid for a standard scraper (correct `HELP`/`TYPE`, stable metric
  names); metric names are prefixed `opscore_` to avoid collisions.
- Cursor pagination and the metrics surface share one truncation/completeness vocabulary with
  Phase 18 — one rule, every call site.

### 3.4 Out of scope (Phase 19)

- **Distributed tracing / OpenTelemetry propagation** — new dependency + cross-cutting scope; deferred
  to its own Scope ADR. (Put forward for adjudication in §4, mirroring ADR-039 §2.3.)
- Dashboards / UI / Grafana — presentation concern; Phase 19 is the data/API layer.
- Audit retention, archival, deletion (R19-8).
- `external/v1` changes (R19-0).
- Activating `ReconcileForward` (OQ-17.3-1).
- New storage / schema migration for projections (R19-6 — compute on-demand).
- `internal/plugin/runtime` gofmt drift — frozen zero-diff outranks formatting.

---

## 4. Decision requested (Round 87)

**(A) Accept** Phase 19 = *Evidence Consumers* with scope items S-1…S-4 and laws R19-0…R19-8 —
authorising a Phase 19 Architecture ADR (ADR-042), no code before it is signed.

**(B) Accept with modification** — e.g. drop S-4 (scan-history ring) as premature, or merge S-3
into S-1's surface.

**(C) Reject** — e.g. insist tracing/OTel belongs here (would require R19-2 to fall, need a
dependency justification), or restart as a defect round.

**(D) Other.**

Note the tracing call is deliberate and put forward for adjudication: "projections / metrics /
*tracing*" was the loose Phase 19 label; this ADR scopes tracing out as a dependency trap, exactly
as ADR-039 scoped "observability features" out until the producer was honest.

---

## 5. Phase 19 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 19-scope | ADR-041 Phase 19 Scope (this ADR) | **DRAFT — R87 sign-off pending** |
| 19-arch | ADR-042 Phase 19 Architecture | authorized after R87 — drafting (R88 pending) |
| 19-impl | Implementation + mutation tests | blocked — after 19-arch sign-off |

Each step authorized only after the previous is signed (ADR-021).

Global ordering (frozen at R84, reaffirmed R86):

```
Phase 17    write correctness / mutation audit integrity      CLOSED
Phase 17.3  reconciliation visibility                         CLOSED
Phase 18    evidence integrity → trustworthy observations     CLOSED
Phase 19    evidence consumers → readable at scale            ← here
```

---

## 6. Sign-off record (Round 87)

| Item | Verdict | Round |
|---|---|---|
| Phase 19 Scope framing (Consumers after Integrity) | ⏳ pending | 87 |
| S-1 Metrics (exact counters, hand-rolled) | ⏳ pending | 87 |
| S-2 Cursor pagination (additive) | ⏳ pending | 87 |
| S-3 Policy-activity projection (on-demand) | ⏳ pending | 87 |
| S-4 Scan-history ring (in-memory) | ⏳ pending | 87 |
| R19-0…R19-8 iron laws | ⏳ pending | 87 |
| Tracing scoped OUT (adjudication) | ⏳ pending | 87 |

*Scope unsigned. ADR-042 Architecture authorized only after R87 approval.*

---

## 7. Carry-forward into ADR-042 (to be filled at R88)

The Round 87 sign-off may add binding clarifications and required answers. Anticipated questions for
ADR-042 to answer:

- **Metrics contract** — exact Prometheus text shape; which counters are exposed; how `Complete()` is
  surfaced (a `opscore_collector_complete 0|1` gauge?);命名 stability.
- **Cursor contract** — `After` semantics on `AuditQuery`; interaction with `Truncated`; whether the
  client uses the last event `id` or an explicit `NextCursor` field (prefer reusing `id`, additive).
- **Projection contract** — `policy-activity` response shape; freshness (on-demand = live); whether it
  reuses `ScanReport`'s three-state vocabulary per policy.
- **API-explosion avoidance** — S-1/S-3 are two new `:8082` routes; existing callers unchanged;
  additive only; no retention/deletion semantics.
