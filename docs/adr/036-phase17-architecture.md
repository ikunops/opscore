# ADR-036 — Phase 17.1: Management API / Policy Management Architecture

- **Status**: Draft (Round 74 — sign-off requested)
- **Date**: 2026-08-10
- **Companion to**: ADR-035 (Phase 17.0 Scope, R73-B CLOSED), ADR-033/034/032/031 (Phases 15/16),
  ADR-021 (Evolution Charter), ADR-010~020 (frozen bases)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme**: concrete architecture for the first deliberate write/control surface (Policy Management),
  mounted separately from the frozen `external/v1` read-only Public Contract.

---

## 0. Abstract

ADR-035 (Phase 17.0 Scope) is CLOSED (R73-B, modifications incorporated). This ADR-036 resolves every
acceptance item in ADR-035 §8 and specifies the concrete architecture for the Management API (Policy
Management write/control surface). No handler is implemented until this is signed (MUST-6).

---

## 1. Component layout

```
cmd/opscore-server  (Composition Root — unique, MUST-5/P17-7)
   │
   ├─ external/v1    (frozen read,  :8080)   ── reads  governancepolicy.Repository
   ├─ probes         (frozen,       :8081)   ── /healthz /readyz /versionz
   └─ management/v1  (NEW write,     :8082)   ── new package: internal/management
          │
          ├─ consumes  governancepolicy.Repository   (existing public interface; writes via it)   [P17-3/5]
          ├─ records   via storage.AuditStore.Append  (existing audit STORE, error-returning)      [P17-9/13]
          │              ^ revised R75: NOT core.AuditSink — that returns void / best-effort (§4.6.3)
          └─ MUST NOT import: governance.Engine, execution, plugin runtime, isolation, hostregistry [P17-4/10]
```

The `internal/management` package is the **only** new code. It is an HTTP handler plus the request
pipeline; it holds no persistence of its own (P17-3) and no decision logic (P17-4/10). It is a
*mutation client* of the same `governancepolicy.Repository` already used by the read surface — the
Repository remains the sole Policy owner.

---

## 2. Request pipeline (carries ADR-035 §3.2.1)

Every mutation request flows through, in order, and **no step may be skipped**:

```
HTTP (management/v1)
  → AuthN        X-Management-Token header → principal;        missing/invalid  ⇒ 401  (fail-closed)
  → AuthZ        principal must hold policy:manage;            absent           ⇒ 403  (fail-closed)
  → Validation   input shape + caller-supplied PolicyID + If-Match revision
  → Handler      maps verb → Repository lifecycle call (business logic only)
  → Audit INTENT AuditStore.Append(intent)      write fails ⇒ 503, mutation NOT attempted   [P17-13]
  → Repository   governancepolicy.CompareAndSave(rec, expectedRevision)  ⇒ 409 on mismatch  [P17-12]
  → Audit OUTCOME AuditStore.Append(outcome)                                                [P17-9]
  → 2xx / error envelope
```

There is **NO** path of the form `HTTP → Handler → Repository` that bypasses AuthN/AuthZ.

Two revisions versus the R74 draft of this pipeline, both forced by the code as it actually is
(§4.6.2, §4.6.3): the single trailing `auditor.Record(...)` — an API that does not exist — is replaced
by a durable **intent-before / outcome-after** pair through `storage.AuditStore.Append`, and the
Repository step is a compare-and-swap rather than a bare `Save`. `Idempotency-Key` is dropped from
Validation per R74 §5; identity is the caller-supplied `PolicyID`.

---

## 3. ADR-035 §8 acceptance answers

### 3.1 AuthN / AuthZ / Fail-closed (§8.1, P17-2 HARD MUST)
- **AuthN** — a shared-secret bearer token in header `X-Management-Token`, sourced from
  `OPCORE_MANAGEMENT_TOKEN` (env / config file, never compiled in). Absent or mismatched ⇒ `401`.
  (mTLS client-cert subject, or loopback-only bind, are noted as deployment alternatives; the
  *requirement* — fail-closed — is what is fixed.)
- **AuthZ** — a single capability `policy:manage`. The handler checks `principal.Has("policy:manage")`;
  absent ⇒ `403`.
- **Fail-closed** — default-deny. No configuration yields an open handler. Startup behaviour is frozen
  by R75 §5 in this wording:

  > **MUST-P17-14 — Management Surface Startup Fail-Closed.**
  > If the configured management authentication prerequisite is absent or invalid, the Management
  > surface MUST NOT bind or register. The server MUST fail closed rather than expose a writable
  > endpoint without effective AuthN/AuthZ.

  R75 confirmed the numbering and the promotion from SHOULD to MUST, with the reason stated sharply:
  *"`:8082` 没有 token 所以请求全部 401" is **not** the strongest available state; "`:8082` 根本不注册"
  is.* A registered-but-always-401 port still advertises a writable surface.

### 3.2 Revision conflict / atomicity (§8.2, P17-8 MUST; revised R75 — supersedes R74 draft)

> **Correction of a factual error in the R74 draft.** The R74 text asserted that the repository
> "performs one atomic file write (write-temp + rename)" and is "guarded by a `sync.Mutex`".
> Reading `internal/governancepolicy/repository.go` shows **neither is true today**: `write()` calls
> `os.WriteFile` (non-atomic, truncate-in-place) and there is **no lock anywhere** — `Save` and
> `setState` are both unguarded read-modify-write. The R74 draft described intended behaviour as if
> it were existing behaviour. That is corrected here; hardening the store is now explicit Phase 17
> work, not an assumption.

- **Conflict** — every mutating request carries `If-Match: <revision>`. The handler does **not**
  compare revisions itself (that would be exactly the `Get → check → Save` pattern MUST-P17-12
  forbids); it passes the expectation into the repository.
- **Primitive** — one new persistence primitive is added to `governancepolicy.Repository`:

  ```go
  // CompareAndSave atomically replaces the stored record for rec.PolicyID iff the
  // currently stored revision equals expectedRevision. expectedRevision == 0 means
  // "must not already exist". Returns ErrRevisionConflict on mismatch.
  CompareAndSave(rec PolicyRecord, expectedRevision int) (PolicyRecord, error)
  ```

#### 3.2.1 FROZEN — `CompareAndSave` is the sole Management mutation CAS primitive (R75 §2)

R75 accepted C-1 and froze the semantics. They are now architecture, not implementation latitude:

| Clause | Frozen semantics |
|---|---|
| CAS-1 | `expectedRevision == 0` ⇒ the target Policy MUST NOT already exist |
| CAS-2 | `expectedRevision > 0` ⇒ the currently stored revision MUST match **exactly** |
| CAS-3 | mismatch ⇒ `ErrRevisionConflict` (surfaced as `409`) |
| CAS-4 | on success the mutation **and** the revision increment are persisted in the **same** write |
| CAS-5 | `internal/management` MUST NOT reach create/update through revision-blind `Save` |
| CAS-6 | the revision comparison MUST NOT live in the HTTP handler |

- **Store hardening — architecture constraint, NOT an implementation optimization (R75 §2).** Without
  all five of these, `CompareAndSave` is a CAS in name only:
  1. a `sync.Mutex` inside the repository covering **every** read-modify-write path
     (`Save`, `CompareAndSave`, `setState`);
  2. writes go to a temporary file first;
  3. the temporary file is synced before it is published;
  4. publication is an atomic `rename`;
  5. the revision comparison stays inside the repository — never in the handler (= CAS-6).
- `Save` is retained for existing Phase 14 callers, but the Management boundary is forbidden to bypass
  CAS through it. Enforced by test, not convention (§3.6).
- No new storage engine is introduced (P17-6); the file store stays the single persistence owner.

#### 3.2.2 FROZEN — `CompareAndTransition` for lifecycle mutations (R76 §3)

**R76 found a real hole in the R75 revision of this ADR, and it was mine.** `CompareAndSave` closes
`create` and `update`, but §4 still mapped `activate` / `deactivate` / `archive` onto the *existing*
`Activate` / `Deactivate` / `Archive`, which take no expected revision. Three of the five management
mutations were therefore still revision-blind, and the only way to make them revision-aware from the
handler is `Get → check → transition` — the exact pattern MUST-P17-12 forbids. P17-8 was **not**
closed. Accepted without reservation.

The second primitive:

```go
// CompareAndTransition atomically moves policyID to targetStatus iff the currently
// stored revision equals expectedRevision and the transition is legal.
// Returns ErrRevisionConflict on revision mismatch, ErrIllegalTransition otherwise.
CompareAndTransition(policyID string, expectedRevision int, targetStatus PolicyStatus) (PolicyRecord, error)
```

| Clause | Frozen semantics (R76 §3, 1–7) |
|---|---|
| CT-1 | `expectedRevision` is compared **inside** the repository |
| CT-2 | mismatch ⇒ `ErrRevisionConflict` ⇒ `409` |
| CT-3 | status check + revision check + status mutation + revision bump are **one** protected read-modify-write. *Clarified at R77 §4:* the bump happens **only when a mutation actually occurs** — a CT-8 self-transition mutates nothing and therefore does not bump |
| CT-4 | Management MUST NOT call the revision-blind `Activate`/`Deactivate`/`Archive` |
| CT-5 | those three are retained unchanged for existing Phase 14 callers |
| CT-6 | the Management layer may use **only** the revision-aware primitives |
| CT-7 | the same store hardening (mutex + temp file + sync + atomic rename) applies |

Two further clauses are **added by this ADR, not by R76** — they resolve contradictions found while
applying CT-1…CT-7. Flagged as implementer-authored so that nothing is attributed to a signature that
was never given:

| Clause | Frozen semantics (added here; brought to R77 for ruling) |
|---|---|
| CT-8 | **Self-transition is a revision-checked no-op.** If the revision matches and the stored status already equals `targetStatus`, the call succeeds, returns the record **unchanged**, and **does not bump the revision** |
| CT-9 | **Error precedence is fixed:** the revision comparison runs **first**. A stale revision yields `ErrRevisionConflict` (`409`) even when the requested transition would also be illegal |

##### Why CT-8 exists — a contradiction in the R75 revision of this ADR, found here

§4 has always annotated `activate` with *"idempotent if Active"* and `archive` with *"idempotent if
Archived"*. CT-3 mandates a revision bump on every transition. **The two cannot both hold.** If a
repeated `activate` bumps the revision, the second call changes observable state and invalidates every
`If-Match` token the client holds — that is not idempotency, it is a silent version churn generator.
Three resolutions were considered:

| Option | Verdict |
|---|---|
| Self-transition bumps the revision anyway | ✗ destroys the idempotency §4 promises, and makes repeated retries (the normal failure-recovery path for a `5xx`) corrupt the client's revision view |
| Self-transition returns `ErrIllegalTransition`; the handler re-reads and returns `200` | ✗ puts lifecycle decision logic in the handler — **P17-4 violation**, the exact thing §3.2.2 was written to prevent |
| **CT-8 — repository returns success with the record unchanged** | ✓ idempotent, no churn, and the decision stays inside the Repository |

CT-8 keeps the revision check: a self-transition on a *stale* revision is still a `409`, because the
client's view of the record is wrong even though its intent is already satisfied.

##### CT-5 re-examined against the actual call graph

CT-5 retains the legacy trio "for existing Phase 14 callers". That phrasing was written before the
call graph was checked. It has now been checked, and there are **no production callers at all**:

```
repo.Activate / Deactivate / Archive   ← called only by internal/governancepolicy/lifecycle.go
governancepolicy.Activate / Archive    ← called only by internal/governancepolicy/guards_test.go
governancepolicy.Deactivate            ← no callers whatsoever
```

This matters, because CT-5 otherwise leaves a live hazard: a legacy transition changes `Status`
**without** bumping the revision, so a Management client holding `rev = 3` would still pass a
subsequent `CompareAndSave(rec, 3)` and overwrite the lifecycle change from a stale read — a lost
update that CAS cannot see. The grep above shows that path is unreachable today. Phase 17 therefore
declares the legacy trio **deprecated and closed to new callers**, enforced by the guard in §3.6
rather than by editing Phase 14 behaviour. If the hazard is ever to be removed structurally instead of
by convention, the legacy trio must bump the revision too — which *is* a Phase 14 behaviour change and
would need its own ruling. Not proposed here.

Resulting closed mutation graph — no half-CAS, half-legacy path:

```
Management
   ├── create / update              → CompareAndSave
   └── activate/deactivate/archive  → CompareAndTransition
                                          ↓
                                     Repository  →  PolicyStoreDir
```

##### Three code-verified facts that shape this contract

Stated here because R76 could not have known them, and because guessing is how the previous two
rounds went wrong:

1. **`setState` does NOT bump `Revision`.** It sets `Status`, optionally `ActivatedAt`, and
   `UpdatedAt`, then writes (`repository.go:175`). Consequence: if `CompareAndTransition` inherited
   that behaviour, **its CAS would be decorative** — two concurrent transitions both holding
   `expectedRevision = 3` would both pass the compare and both commit. CT-3 therefore *requires* the
   revision bump; it is load-bearing, not bookkeeping.
   **Declared divergence:** the new primitive bumps the revision; the legacy `Activate`/`Deactivate`/
   `Archive` keep their existing non-bumping behaviour for Phase 14 callers (CT-5). Two transition
   paths with different revision semantics therefore coexist on one store. That is deliberate, and it
   is the price of not editing Phase 14 behaviour.
2. **No transition-legality check exists anywhere today.** `setState` performs none, and the one layer
   above it — `internal/governancepolicy/lifecycle.go` — does not either: `Activate`/`Deactivate`/
   `Archive` there are three-line wrappers that call `repo.<verb>` and then `repo.Get`, with no
   precondition test between them. `Activate` on an already-Archived policy therefore succeeds
   silently today. So the state machine referenced by CT-3 is not being *reused*; it is being
   **created** by Phase 17. It is defined inside the repository, never in the handler (P17-4).
3. **`Deactivate` returns the record to `StatusDraft`** — there is no separate "inactive" state. The
   admitted machine is `Draft ⇄ Active`, plus `→ Archived` as terminal.

##### Why a second primitive, honestly

Not for the reason it first appears. A transition *could* be expressed as
`CompareAndSave(recWithNewStatus, rev)` and would still be safe against lost updates, because the
comparison happens inside the repository either way. The decisive reasons are different:

- **P17-4.** Expressing a transition as a full-record save means the handler authors the whole record,
  including `Status` — i.e. the lifecycle state machine moves into the management layer. That is
  decision logic at the write boundary, which P17-4 forbids.
- **Blast radius.** A full-record CAS lets a management client rewrite *any* field while nominally
  "activating". `CompareAndTransition` restricts the mutation to status + revision.

So R76's requirement is accepted on stronger grounds than revision-awareness alone.

See §4.6.2 for why this — and not handler-side composition — is the only construction that satisfies
MUST-P17-12.

### 3.3 Audit (§8.3, P17-9 MUST; revised R75 — supersedes R74 draft)

> **Correction of a second factual error in the R74 draft.** The R74 text referenced
> `auditor.Record(audit.Event{...})`. **No such API exists.** The real contract is
> `core.AuditSink.Emit(core.AuditEvent)` with the storage adapter in
> `internal/controlplane/audit`. The invented signature is withdrawn.

Reading the real abstraction produces a structural obstacle that must be stated plainly:

1. **`core.AuditSink.Emit` returns nothing.** Its documented contract is explicitly best-effort —
   *"A persist failure is logged but never breaks the caller's execution."* MUST-P17-13 requires
   mutation success to be **bound** to audit success. You cannot bind to a signal that does not
   exist. Routing management audit through `core.AuditSink` makes P17-13 unsatisfiable by
   construction.
2. **`core.AuditEvent` is execution-shaped.** It carries `OperationName`, `ExecutionID`,
   `CapabilityHash`, `Result`, and `StorageAuditSink.Emit` hard-codes `Action: "execute"`. Emitting a
   policy mutation through it would record a governance write **as an execution**. That is not a
   cosmetic mislabel: it corrupts the audit trail with precisely the Mutation⇄Execution conflation
   that P17-10 exists to prevent.

**Resolution — same store, different seam (not a second audit store).**
Management writes audit rows through `storage.AuditStore.Append(storage.AuditEvent) (…, error)` —
the *same* durable store `StorageAuditSink` already appends to — with `Action: "policy.<verb>"`.
`Append` **returns an error**, so mutation success can be bound to audit success. This satisfies
P17-13 without inventing a second audit store and without touching the frozen `core.AuditSink`
Runtime Contract.

**Ordering (fail-closed, no silent loss):**

```
audit INTENT (durable, error-checked)   ── failure ⇒ 503, mutation NOT attempted
        ↓
CompareAndSave                          ── the only mutation point
        ↓
audit OUTCOME (durable, error-checked)  ── failure ⇒ 500 + defined degraded state
```

#### 3.3.1 MUST-P17-13 restated (R75 §3) — Mutation Audit Durability / Causal Recordability

R75 accepted the seam but required the MUST be **renamed and reworded** so it can never be read as a
claim of cross-store atomicity. The binding text is:

> **MUST-P17-13 — Mutation Audit Durability / Causal Recordability.**
> A Policy mutation MUST NOT be attempted before a durable, error-checked audit **intent** has been
> recorded. The mutation outcome MUST subsequently produce a durable, error-checked audit
> **outcome**. Because the Policy Store and the Audit Store are independent persistence domains,
> **Phase 17 does not claim cross-store atomicity.**

This state is therefore **explicitly permitted**, not a bug:

```
INTENT   = durable
MUTATION = success
OUTCOME  = failed        ⇒ identifiable, reconciliation-needed degraded state
                           (NOT a rollback, NOT a transaction)
```

What Phase 17 does guarantee:

```
no durable intent   ⇒ mutation FORBIDDEN
durable intent      ⇒ mutation permitted
after mutation      ⇒ a durable outcome MUST be attempted
```

Consequences that are binding: a mutation can never be **silently** unaudited; the degraded state must
be identifiable and traceable; and the ADR MUST NOT claim rollback or transactional atomicity
anywhere.

#### 3.3.2 Required audit fields — and an honest gap (three of the seven do not exist)

R75 requires each audit record to carry at least: **PolicyID · expected/current Revision · action ·
actor/principal · correlation/request ID · timestamp · outcome/status** — *"so it forms a traceable
causal chain, not just a log line."* Checked against the real `storage.AuditEvent`
(`internal/storage/models.go:82`), which has
`ID, Timestamp, Actor, Operation, Action, Target, Result, Detail, CapabilityHash, ExecutionID,
SnapshotSchemaVersion`:

| R75 requirement | Real field | Status |
|---|---|---|
| PolicyID | `Target` | ✅ exists (documented as "operation-specific target") |
| action | `Action` | ✅ exists (vocabulary widened, §3.3.3) |
| actor / principal | `Actor` | ✅ exists |
| timestamp | `Timestamp` | ✅ exists |
| outcome / status | `Result` | ⚠️ exists, but documented `"success" \| "failure"` — has no value for *intent* |
| expected / current Revision | — | ❌ **no such field** |
| correlation / request ID | `ExecutionID`? | ❌ **not usable** — see below |

I am flagging this rather than quietly stuffing the missing values into `Detail`, because the last
round established that inventing an API is the failure mode to avoid.

- **`ExecutionID` MUST NOT be reused as the management request ID.** It is execution-shaped by name
  and by documented meaning ("correlates with the ExecutionRecord that drove it"). Populating it from
  an HTTP management request writes the Mutation⇄Execution conflation P17-10 forbids straight into the
  audit trail — the same defect that disqualified `core.AuditSink` in §4.6.3.
- **`internal/correlation` does not help.** It is a read-only view facade
  (`Correlate(ctx, Scope) (CorrelationView, error)`); it defines no request-ID mechanism.

**Proposal — two additive columns, not a `Detail` blob.** Extend `storage.AuditEvent` with
`Revision int` and `CorrelationID string`, via one versioned migration in
`internal/storage/sqlite/migrate.go`. Feasibility verified, not assumed:

1. **Precedent exists.** `capability_hash`, `execution_id` and `snapshot_schema_version` were all
   added to `audit_events` the same way; the migration runner already exists and is documented as
   idempotent additive DDL.
2. **Zero frozen-package edits.** The other `Audit().Append` callers live in
   `internal/plugin/runtime` (frozen) and `internal/controlplane/audit`. Both construct
   `storage.AuditEvent{...}` with **keyed** fields — provable from the fact that `go vet` passes,
   since vet rejects unkeyed composite literals of another package's struct. Adding fields is
   therefore source-compatible and requires no edit to any frozen file.
3. **Rejected alternative — encode into `Detail`.** It needs no schema change, but it buries
   governance-critical fields in a free-text blob and makes the causal chain unqueryable — which
   defeats the stated purpose of R75 §3 ("不是单纯打一条日志"). Rejected deliberately; overrule me if
   you disagree.

**Also a declared vocabulary change:** `Result` is documented `"success" | "failure"`. Intent rows
need a third value — proposed `"intent"` — so that intent and outcome are distinguishable by query.
Declared here, not performed silently.

#### 3.3.2.1 FROZEN — OQ-17.1-B approved, with Revision semantics (R76 §1)

R76 approved `AuditEvent.Revision int`, `AuditEvent.CorrelationID string` and `Result: "intent"`, and
confirmed that reusing `ExecutionID` is forbidden. It then required the `Revision` field's meaning be
pinned down, so that it is a causal chain rather than one ambiguous number:

| Row | `Result` | `Revision` carries |
|---|---|---|
| intent | `"intent"` | the **expected** revision (`0` for create) |
| outcome, committed | `"success"` | the **committed / resulting** revision |
| outcome, revision conflict | `"failure"` | the actual revision observed, when obtainable; the conflict is recorded as the failure reason |
| outcome, other failure | `"failure"` | best obtainable revision information; never silently omitted |

- **`CorrelationID` is the Management request correlation ID and is wholly independent of
  `ExecutionID`.** They are never cross-populated, in either direction.
- The chain that must be reconstructible from the audit table alone:

  ```
  CorrelationID → PolicyID (Target) → expected revision → mutation → resulting revision
  ```

This is what makes the intent/outcome pair *causally* recordable (MUST-P17-13, §3.3.1) rather than two
unrelated log lines that happen to be adjacent.

#### 3.3.3 FROZEN — `policy.*` audit Action vocabulary (R75 §4)

R75 accepted the widening but required it be contractualized rather than waved through as "it's a
`string` anyway":

- The admitted values are exactly `policy.create`, `policy.update`, `policy.activate`,
  `policy.deactivate`, `policy.archive`.
- This is an **Audit domain vocabulary expansion**. It does **not** change the Runtime Contract.
- `execute` and `plan` keep their existing execution semantics, untouched.
- A Policy mutation MUST NOT be recorded as `execute` or `plan`.
- Every Management action MUST use the `policy.*` namespace.

This is itself a P17-10 reinforcement: after this, mutation and execution are distinguishable in the
audit trail by a single indexed column.

### 3.4 Validation / Update semantics / Idempotency (§8.4; idempotency revised R75 per R74 §5)
- **Validation** — Policy Rule legality is checked in the handler against the existing
  `governancepolicy` model (the same validation the file-backed store already enforces).
  `Engine.Evaluate` is **NOT** invoked (P17-4).
- **Update = Draft-only** — `update` rejects an Active target with `422` (or `409`); to change an
  Active Policy the caller creates/edits a next Draft then `activate` (preserves Phase 14
  `PolicyID + Revision`).
- **Idempotency (store-derived; the `Idempotency-Key` header is REMOVED)** — identity is the
  caller-supplied `PolicyID`, which the Repository already owns, so no management-side idempotency
  state exists and behaviour survives restart and replication (§4.5.4, R74 §5):

  | Request | Store state | Result |
  |---|---|---|
  | `create` PolicyID=P1, payload A | absent | `201` — `CompareAndSave(rec, 0)` |
  | `create` PolicyID=P1, payload A (replay) | P1 exists, payload identical | `200` — existing record returned, **no second Policy** |
  | `create` PolicyID=P1, payload B | P1 exists, payload differs | **`409`** — never an implicit overwrite |
  | `activate` on Active / `archive` on Archived | terminal state already held | `200` no-op |

  The `409` row is R74's explicit addition: without it, "idempotency" silently degrades into
  overwrite-by-ID.

  **Clarified at 17.2 — who performs the content comparison.** The table above says *"payload
  identical ⇒ 200"* without naming the actor, and read carelessly it contradicts **CAS-1**
  (`expectedRevision == 0` ⇒ the target MUST NOT exist). Exactly one reading keeps every frozen clause
  intact, and it is the one implemented:

  ```
  CompareAndSave(rec, 0)
      ├── created            ──────────────► 201
      └── ErrRevisionConflict (already exists)
              └── handler performs a READ-ONLY Get and compares payloads
                      ├── identical  ─────► 200   (idempotent replay)
                      └── differs    ─────► 409
  ```

  This is **not** the forbidden `Get → check → Save`: there is no write on that branch. The mutation
  attempt already happened and was refused *inside* the repository, so no lost update is possible; the
  subsequent read only shapes the response. CAS-1 holds literally, CAS-6 holds (no revision comparison
  in the handler), MUST-P17-12 holds (no handler-side read-modify-**write**), and the frozen
  `CompareAndSave` signature is unchanged. The alternative — moving payload comparison inside the
  repository — would require a third return value to distinguish *created* from *already-identical*,
  i.e. a change to a frozen signature, which would need a new ruling for no behavioural gain.

### 3.5 Error contract (§8.5, P17-S3 SHOULD)
| Code | Meaning |
|---|---|
| 401 | missing / invalid AuthN token |
| 403 | authenticated but lacks `policy:manage` |
| 404 | PolicyID not found |
| 409 | `If-Match` revision stale (concurrent modification) |
| 422 | validation failure (bad input / update-on-Active) |

Consistent JSON envelope: `{ "error": { "code": "...", "message": "..." } }`.

### 3.6 Mechanical isolation (§8.6, P17-0/P17-1/P17-4/P17-10)
- **Surface isolation** — `management/v1` is registered on its own `:8082` bind in the Composition
  Root, **never** under the `:8080` `external/v1` mux. A build-time assertion (a test in
  `cmd/opscore-server`) fails the build if any management route is mounted on the external mux.
- **No execution bridge** — `internal/management` is covered by the existing AST forbidden-import
  guard (already banning `internal/plugin/runtime`, `internal/plugin/isolation`,
  `internal/controlplane/hostregistry`); the banned set is extended to include `internal/governance`
  (the `Engine` entry) so a `Management → Engine → Execution` import is a compile-time failure.
  `TestNoExecMethod` continues to apply.

  Per R75 §7, **the AST import guard and `TestNoExecMethod` are MUST-level architecture guards, not
  ordinary tests** — they may not be skipped, relaxed or deleted to make an implementation compile.
  The permitted dependency graph is closed:

  ```
  management
     ├── governancepolicy.Repository
     └── audit persistence            ← nothing else
  ```

  Calling `Engine.Evaluate` "just to validate a Policy" is explicitly forbidden (§4.5.2).
- **No CAS bypass** — a test asserts `internal/management` never calls **any** revision-blind
  repository method: `Save`, `Activate`, `Deactivate`, `Archive` (CAS-5/CAS-6 §3.2.1, CT-4/CT-6
  §3.2.2). Per R76 §2 this guard, the AST import guard and `TestNoExecMethod` are all **MUST-level and
  may not be downgraded to SHOULD**.

  **Widened here — the R76 formulation was not airtight.** Banning the four *methods* leaves a second
  door open: `internal/governancepolicy/lifecycle.go` exports revision-blind **package-level
  functions** that take a `Repository` and perform exactly the forbidden writes —
  `Create(repo, id, rules)` (`NextRevision` → `Save`), `Activate(repo, id)`, `Deactivate(repo, id)`,
  `Archive(repo, id)`. A handler importing `governancepolicy` — which it must, for the types — could
  call `governancepolicy.Activate(repo, id)` and bypass CAS entirely **without touching a single
  banned method name**. The guard is therefore defined over *both* forms:

  | Banned from `internal/management` | Form |
  |---|---|
  | `Save` / `Activate` / `Deactivate` / `Archive` | `Repository` methods |
  | `governancepolicy.Create` / `.Activate` / `.Deactivate` / `.Archive` | package-level functions (`lifecycle.go`) |
  | `NextRevision` followed by `Save` in the same function | handler-side composition, MUST-P17-12 |

  These functions are additionally declared **deprecated and closed to new callers** (§3.2.2, CT-5
  re-examination): they have no production callers today, and Phase 17 must not become their first.

---

## 4. Lifecycle verb → Repository mapping

> **Corrected R75.** The R74 draft mapped verbs onto `Create(...)` / `Update(...)` and claimed a 1:1
> fit with the existing contract. The real interface
> (`internal/governancepolicy/repository.go`) is
> `Save / Get / List / Archive / Activate / Deactivate / NextRevision` — there is **no** `Create`,
> no `Update`, and no `Latest`. The R74 table was wrong; this is the corrected mapping.

| HTTP verb | Repository call | Precondition |
|---|---|---|
| `POST /policies` (create) | `CompareAndSave(rec, 0)` | `0` = must not exist; caller supplies `PolicyID` |
| `PUT /policies/{id}` (update) | `CompareAndSave(rec, ifMatchRev)` | target is Draft; `If-Match` |
| `POST /policies/{id}/activate` | `CompareAndTransition(id, ifMatchRev, StatusActive)` | from `Draft`; `If-Match`; already `Active` ⇒ CT-8 no-op `200` |
| `POST /policies/{id}/deactivate` | `CompareAndTransition(id, ifMatchRev, StatusDraft)` | from `Active`; `If-Match`; already `Draft` ⇒ CT-8 no-op `200` |
| `POST /policies/{id}/archive` | `CompareAndTransition(id, ifMatchRev, StatusArchived)` | from `Draft` or `Active`; `If-Match`; already `Archived` ⇒ CT-8 no-op `200` |

Admitted state machine (created by Phase 17 — none exists today, §3.2.2 fact 2):

```
   Draft ⇄ Active            Draft → Archived
                             Active → Archived        Archived = terminal
```

Anything outside it — `Archived → Active`, `Archived → Draft` — is `ErrIllegalTransition` ⇒ `409`.
A stale `If-Match` outranks illegality (CT-9): revision is compared first.

> **Corrected R76.** The R75 revision of this table still routed the three lifecycle verbs to the
> revision-blind `Activate`/`Deactivate`/`Archive`, leaving three of five management mutations outside
> CAS and P17-8 open. R76 caught it. They now go through `CompareAndTransition` (§3.2.2).

**Two** methods are added — `CompareAndSave` (create/update) and `CompareAndTransition` (lifecycle) —
and every management mutation is revision-aware. No new lifecycle *state* is invented (P17-3/4); note
that `deactivate` returns the record to `Draft`, since no separate "inactive" state exists.
`Save`, `Activate`, `Deactivate` and `Archive` remain in place and behaviourally untouched, but
`internal/management` MUST NOT call any of them — a revision-blind write from the boundary is exactly
the lost-update hazard P17-8 forbids. Enforced as a test, not a convention (§3.6), and the ban covers
the `lifecycle.go` package-level functions of the same names, which are the non-obvious second door.

*(Wording corrected: earlier drafts said these were kept "for existing Phase 14 callers". The call
graph was then checked and there are none in production — only `guards_test.go`. They are kept because
removing them is a Phase 14 edit, not because anything depends on them. §3.2.2, CT-5 re-examination.)*

> **Required notice (R77 §5) — do not let a future maintainer mistake these for CAS.**
> `Repository.Activate` / `.Deactivate` / `.Archive` and the `lifecycle.go` package-level functions of
> the same names are **historical compatibility interfaces. They are NOT part of the Phase 17
> Management mutation contract and carry no revision semantics whatsoever.** They must never be
> described, documented or extended as revision-aware APIs. The Phase 17 mutation contract is exactly
> two methods: `CompareAndSave` and `CompareAndTransition`.

**Store-derived idempotency (closes OQ-17.1-A).** The same primitive delivers it, with no
management-side state:

| Case | Call | Result |
|---|---|---|
| first create | `CompareAndSave(P1{payload A}, 0)` | `201`, revision 1 |
| retry, identical semantics | `CompareAndSave(P1{payload A}, 0)` | exists ⇒ compare stored content ⇒ **`200`**, no second policy |
| same ID, different payload | `CompareAndSave(P1{payload B}, 0)` | exists, content differs ⇒ **`409 Conflict`** |

Restart-safe and replica-safe, because the Repository — not a process-local table — is the only
source of truth. The `Idempotency-Key` header is **dropped** from the request contract, not left
underspecified.

---

## 4.5 Implementer's rationale, deliberate deviations, and self-identified gaps

This section records the design reasoning of the implementer (not derived from R73), the places where
this ADR deliberately goes **beyond or against** what R73 asked for, and one gap the implementer found
while reviewing their own draft. It exists so that the sign-off is a review of reasoning, not of prose.

### 4.5.1 Rationale — why a shared-secret header instead of mTLS / OIDC / RBAC

Phase 17 is the project's **first deliberate write boundary**. The primary objective for a first write
boundary is not authentication strength; it is that **fail-closed behaviour be exhaustively testable**.

- mTLS pushes the AuthN decision into the TLS handshake. A `httptest` server cannot then cover
  "token absent ⇒ 401" without certificate fixtures, which would contaminate the currently clean
  ~44-package test tree.
- A capability **matrix** (RBAC) would be architecture paid for an imagined future: Phase 17 has
  exactly one writing principal.

The decision is therefore **replaceable, not extensible**: AuthN/AuthZ sit behind two interfaces
(`Authenticator`, `Authorizer`). Substituting mTLS in Phase 18+ is an implementation swap, not an
architecture change. This is an explicit bet that the boundary's *shape* is the durable part and the
*credential mechanism* is not.

### 4.5.2 Rationale — why the AST guard bans `internal/governance` wholesale

A narrower guard (ban only execution-related packages) was considered and rejected.

The realistic P17-10 leak is **not** a handler calling `Execute()` — that is caught by review. The
realistic leak is a handler calling `Engine.Evaluate()` "just to validate" a rule, at which point the
write path has silently acquired a dependency on evaluation results and the causal chain is joined.
A guard scoped to execution packages does not catch this.

Therefore the edge `Management → Engine` is made **non-existent at compile time**. The accepted cost:
validation cannot reuse the Engine's rule evaluation and must rely on the `governancepolicy` model's
own schema checks. The implementer considers this cost correct on its own merits — *write-time
validation* and *runtime evaluation* are different concerns, and sharing them would create latent
revision-semantics coupling.

### 4.5.3 Deliberate deviation — startup hard-fail exceeds what R73 required

R73 raised P17-2 to a HARD MUST on the grounds that a write boundary must default to deny. This ADR
goes further and is explicit about it: **if `OPCORE_MANAGEMENT_TOKEN` is unset, the `:8082` bind must
refuse to register and the process must fail to start** (§3.1), rather than starting and returning
401 per request.

Reason (which differs from R73's): `:8080` is an **unauthenticated read** surface. If AuthN on `:8082`
were merely "deny per request", a deployment that forgets the token would degrade into *a write port
that looks exactly as healthy as the read port*. A live, naked write port masquerading as a working
service is the worst available failure mode. Refusing to boot is strictly preferable to booting wrong.

### 4.5.4 Self-identified gap — idempotency key durability is unspecified

§3.4 states that `create` accepts an `Idempotency-Key` and that replay returns the original `201`.
**It does not say where the key⇒PolicyID association is stored.** This is a real hole:

- If the association is held in process memory, a restart (or a second replica) silently loses
  idempotency, and a client retry after restart creates a **duplicate Policy** — precisely the
  outcome the header exists to prevent.
- Persisting it, however, means introducing state that `internal/management` owns, which collides
  with P17-3 ("no persistence of its own").

The implementer's proposed resolution, offered for the reviewer to accept or overrule:
**derive idempotency from the store rather than from a side table.** `create` requires a
caller-supplied `PolicyID`; a replay therefore hits an existing `PolicyID` and returns the existing
Policy (`200`) instead of creating a second one. This keeps the Repository as the single source of
truth, requires no new state, and remains correct across restarts and replicas. The `Idempotency-Key`
header is then dropped rather than left underspecified.

This is flagged as **Open Question OQ-17.1-A** and must be resolved in the R74 verdict; an (A) Accept
that does not address it should be read as accepting the store-derived resolution above.

---

## 4.6 Disposition of R74 (B — Accept with modifications)

R74 returned **B**, explicitly withholding authorization for 17.2 pending four items. Disposition,
item by item, including the two places where this ADR does **not** simply comply.

### 4.6.1 Accepted without reservation
- **Repository contract conflict is real.** R74 is factually correct that the frozen contract has no
  `Create`/`Update`. Verified against `internal/governancepolicy/repository.go`. The R74 draft's
  mapping table was wrong and is corrected in §4. *(R74 additionally cited a `Latest` method; that
  one does not exist either — `Get` returns the latest. Noted because R74's preferred remedy was
  phrased in terms of it.)*
- **Startup hard-fail promoted to MUST** (R74 §2) — accepted, now MUST-P17-14.
- **Same PolicyID + different payload ⇒ 409** (R74 §5) — accepted; the rule was missing and is now
  in the §4 idempotency table.
- **MUST-P17-11 Repository Contract Integrity** — accepted. §3.2 declares the contract evolution
  explicitly rather than smuggling methods in during implementation.

### 4.6.2 Contested — R74 §4 "方案 1" is not implementable alongside R74 §7

R74 recommends **方案 1**: *"Management handler 自己组合现有 `Get / Latest / NextRevision / Save`"*.
R74 §7 then mandates: *"Revision check + mutation 必须是 Repository 内部原子操作 … 而不是由 Handler
自己做 check-then-save."*

Given the real interface, these two clauses are **mutually exclusive**. Composing
`Get → compare → Save` at the handler *is* handler-side check-then-save; there is no ordering of the
existing methods that performs the comparison inside the repository, because no existing method
accepts an expected revision. R74's own MUST-P17-12 therefore eliminates its own preferred remedy.
**方案 1 is rejected on R74's own grounds**, not on preference.

What is retained is R74's actual concern behind 方案 1 — *"HTTP verb ≠ Repository method; don't
reshape the Repository to match HTTP CRUD."* That concern is correct and is honoured:
`CompareAndSave` is **not** an HTTP-shaped verb. Compare-and-swap is a storage primitive. The HTTP
`create` and `update` verbs both collapse onto the *same* primitive, distinguished only by the
expected revision (`0` vs `n`). The Repository gains a persistence capability, not a REST surface.

This is deliberately the **minimum viable** contract evolution: one method, and it simultaneously
closes MUST-P17-12 (atomic CAS), OQ-17.1-A (store-derived idempotency), and the create/update
mapping hole. Adding `Create` and `Update` as R74 feared would have added two HTTP-shaped methods and
still not closed P17-12.

### 4.6.3 Contested — MUST-P17-13 cannot be met through the existing audit abstraction

R74 requires audit success semantics to be bound to mutation success, **and** forbids inventing a
second audit store. Against the real code those two constraints collide:
`core.AuditSink.Emit(core.AuditEvent)` returns nothing and is documented as best-effort by design.
There is no success signal to bind to.

§3.3 resolves this without violating either constraint: audit rows go to the **same** durable store
via `storage.AuditStore.Append`, which returns an error, rather than through the error-swallowing,
execution-shaped `core.AuditSink` adapter. No second store; a bindable signal; the frozen Runtime
Contract untouched.

A second, independent reason drives the same choice: `StorageAuditSink` hard-codes
`Action: "execute"`. Emitting policy mutations through it would label governance writes as
executions — recording, in the audit trail itself, exactly the Mutation⇄Execution conflation P17-10
exists to forbid.

### 4.6.4 Residual honesty

Atomicity across two independent stores is not achievable without a transaction manager, which is out
of scope (P17-6). §3.3 therefore guarantees the strictly weaker but *well-defined* property:
**no mutation without a prior durable audit intent**, hence no silent audit loss, with
intent-without-outcome as a detectable degraded state. If R75 requires true atomicity instead, that
is a scope change and needs its own ADR — it should not be waved through as an implementation
detail.

---

## 4.7 Disposition of R75 (B — narrow closure; all three corrections confirmed)

R75 confirmed all three Part-1 factual corrections, accepted C-1 outright, accepted C-2 and C-3 in
principle subject to precise wording, confirmed MUST-P17-14, and — importantly — **declined to require
a new ADR** for the two-store limitation, accepting it as a documented Phase 17 boundary instead.
Everything R75 asked for has been applied:

| R75 required item | Applied at | Nature of change |
|---|---|---|
| 1. Freeze `CompareAndSave(rec, expectedRevision)` as the **sole** Management mutation CAS primitive | §3.2.1 (CAS-1…CAS-6 + 5 hardening constraints) | frozen as architecture, not "implementation optimization" |
| 2. Rename/restate P17-13 → *Mutation Audit Durability / Causal Recordability*, disclaiming cross-store atomicity | §3.3.1 (binding text verbatim) | wording is now unable to imply a transaction |
| 3. Freeze the `policy.*` audit Action vocabulary as a contract | §3.3.3 | Audit-domain expansion; Runtime Contract untouched |
| 4. Register MUST-P17-14 (startup fail-closed, no writable surface without AuthN) | §3.1 (verbatim), §7.2 | SHOULD → MUST, numbering confirmed |
| AST guard + `TestNoExecMethod` are MUST-level, not ordinary tests | §3.6 | plus a new no-CAS-bypass guard |

**One item R75 asked for that could not be applied as stated — surfaced, not silently improvised.**
R75 §3 requires each audit record to carry seven fields. Three of them have no home in the real
`storage.AuditEvent`: **Revision**, **correlation/request ID**, and an **intent** value for `Result`.
`ExecutionID` is deliberately *not* reused for the request ID — doing so would write the
Mutation⇄Execution conflation into the audit trail, i.e. re-commit the very defect that disqualified
`core.AuditSink`. §3.3.2 states the gap, proposes two additive columns behind one versioned migration
(with the precedent and the zero-frozen-edit proof), and records the rejected `Detail`-blob
alternative. **This is a contract change and therefore needs an R76 ruling, not an implementer's
decision** — which is precisely the MUST-P17-11 discipline R74 imposed.

---

## 4.8 Disposition of R76 (B — one architectural modification; OQ-17.1-B approved)

R76 approved OQ-17.1-B, confirmed the four R75 items as PASS, and raised **one** blocker: the lifecycle
verbs were still revision-blind. Both required changes are applied.

| R76 required item | Applied at | Nature of change |
|---|---|---|
| 1. Approve OQ-17.1-B — add `Revision` + `CorrelationID`, admit `Result: "intent"`, **do not** reuse `ExecutionID`; freeze `intent = expected revision`, `outcome = committed revision` | §3.3.2.1 (FROZEN semantics table + causal chain) | additive audit-schema evolution, one versioned migration |
| 2. All Management mutations must be revision-aware; Repository must expose an internal atomic state-transition primitive; Management must not call revision-blind lifecycle methods | §3.2.2 (`CompareAndTransition`, CT-1…CT-7), §4 mapping table, §3.6 guard | second frozen primitive; the last three revision-blind paths closed |

**R76's finding is accepted without reservation, and it was a real defect of mine.** `CompareAndSave`
closed `create`/`update` and I stopped there; `activate`/`deactivate`/`archive` — three of five
mutations — were left on the revision-blind path, so P17-8 was open while the ADR claimed it closed.

### 4.8.1 Three things R76 did not ask for, found while applying it

Surfaced rather than silently decided, per MUST-P17-11. Two are contradictions inside this ADR; one is
a hole in the guard R76 itself specified.

| # | Finding | Resolution | Where |
|---|---|---|---|
| F-1 | **`CompareAndTransition` as specified could not be idempotent.** §4 has always promised *"idempotent if Active / if Archived"*, while CT-3 mandates a revision bump on every transition. A repeated `activate` would bump the revision and invalidate the client's `If-Match` — retry, the normal `5xx` recovery path, would corrupt the client's view | **CT-8** — self-transition is a revision-checked **no-op**: success, record unchanged, no bump. Rejected the alternative of returning `ErrIllegalTransition` and letting the handler re-read, because that relocates lifecycle logic into the handler (P17-4) | §3.2.2 CT-8 |
| F-2 | **Error precedence was undefined** when a call is *both* revision-stale *and* illegal | **CT-9** — revision compares first; stale ⇒ `409 ErrRevisionConflict`. A client with a stale view must refetch before its intent can even be evaluated | §3.2.2 CT-9 |
| F-3 | **The no-CAS-bypass guard as worded is not airtight.** Banning the four `Repository` *methods* misses `internal/governancepolicy/lifecycle.go`, which exports `Create`/`Activate`/`Deactivate`/`Archive` as **package-level functions taking a `Repository`**. A handler must import that package for its types, so `governancepolicy.Activate(repo, id)` would bypass CAS without naming a banned method | Guard redefined over both forms (methods *and* free functions), plus `NextRevision`→`Save` composition | §3.6 |

**Call-graph fact behind F-3 and CT-5.** The legacy lifecycle path has **no production callers**:
`repo.Activate/Deactivate/Archive` are reached only from `lifecycle.go`; the free functions
`Activate`/`Archive` only from `guards_test.go`; `Deactivate` from nowhere. So retaining them costs
nothing today — but it does leave a latent hazard, stated plainly rather than buried: a legacy
transition changes `Status` **without** bumping the revision, so a Management client holding `rev = 3`
would still pass a later `CompareAndSave(rec, 3)` and overwrite that lifecycle change from a stale
read. CAS cannot detect it. Unreachable today, closed by the guard, and structurally removable only by
making the legacy trio bump the revision — a Phase 14 behaviour change, **not** proposed here.

---

## 5. Decision requested — ✅ **SIGNED at R77 (A — PASS)**

> **Status: CLOSED.** R77 returned **A — ACCEPT / PASS** and authorized Phase 17.2 Implementation.
> CT-8, CT-9 and F-3 were each ruled correct; CT-3 was clarified (§3.2.2); the legacy-trio limitation
> was accepted with a mandatory documentation notice (§4). Ledger at §7.5. The request below is
> retained as the record of what was signed.

Sign off this Phase 17.1 Architecture ADR (ADR-036), **as revised per R76-B** (both mandated items
applied, §4.8; plus three implementer-found corrections, §4.8.1). Choose:

- **(A) Accept** — Phase 17 proceeds to 17.2 Implementation (handler + wiring + tests). No handler is
  written until this is signed.
- **(B) Accept with modifications** — list the specific architecture changes required.
- **(C) Reject** — state the blocker.

Confirm the three non-negotiable invariants are correctly enforced by this design:
1. **P17-0** — `external/v1` stays read-only; Management API is a separate surface (§3.6).
2. **P17-2** — fail-closed AuthN/AuthZ, no open path (§3.1, §2).
3. **P17-10** — no execution bridge; mutation does not enter `Evaluate`/`Execute` (§3.6, §1).

### 5.1 Historical — contested items ruled on at R75 (retained for the record)

*(All three were confirmed at R75; see §4.7. Retained because the reasoning explains why the ADR
reads as it does.)* R74 was accepted in substance, but three of its instructions could not be applied
as written because they contradicted the code as it actually exists.

| # | Contested item | R74 said | This ADR proposes | Where |
|---|---|---|---|---|
| C-1 | Atomic revision CAS | "Option 1: compose `Get`/`NextRevision`/`Save` in the handler" **and** MUST-P17-12 "CAS must be atomic inside the Repository; handler-side Get→check→Save is forbidden" | Reject Option 1 as self-contradictory under MUST-P17-12; add one new Repository primitive `CompareAndSave(rec, expectedRevision)` + harden the store (atomic temp-file rename + per-ID mutex) | §4.6.2, §3.2 |
| C-2 | Audit binding | MUST-P17-13 bound to a mutation, **and** "不要为了 Phase 17 临时发明第二套 Audit Store" | `auditor.Record` (as written in the R74 draft) does not exist. `core.AuditSink.Emit` returns `void` and is documented best-effort, so it *cannot* satisfy P17-13. Route management audit through `storage.AuditStore.Append(e) (AuditEvent, error)` — **the same existing store**, reached one layer lower than the error-swallowing sink adapter. No second store is introduced; the R74 prohibition is respected | §4.6.3, §3.3 |
| C-3 | Audit `Action` vocabulary | (not addressed) | `StorageAuditSink` hardcodes `Action: "execute"`. Management events need `policy.create` / `policy.update` / `policy.archive` / `policy.activate` / `policy.deactivate`. This widens an existing vocabulary — requires a ruling, not an implementation decision | §4.6.3 |

All three were resolved at R75: C-1 accepted and frozen (§3.2.1), C-2 accepted with mandated rewording
(§3.3.1), C-3 accepted and contractualized (§3.3.3). OQ-17.1-A was closed at R74; MUST-P17-14 was
confirmed at R75.

### 5.2 What is open for R77

**Nothing R76 mandated is outstanding** — both items are applied (§4.8), and OQ-17.1-B is closed by
R76's approval, its semantics frozen at §3.3.2.1. What R77 is being asked to rule on is **three
clauses this ADR added on its own authority** while applying R76. They are architecture, not
implementation detail, so they are brought up for signature rather than decided at the keyboard
(MUST-P17-11):

> **CT-8 — self-transition is a revision-checked no-op** (no revision bump). Without it,
> `CompareAndTransition` cannot honour the idempotency §4 promises, because CT-3's mandatory bump
> makes every repeated `activate` change observable state. The rejected alternative — return
> `ErrIllegalTransition` and have the handler re-read and answer `200` — was rejected because it puts
> lifecycle decision logic in the handler, violating P17-4.
>
> **CT-9 — revision compare precedes legality check.** A call that is both stale and illegal returns
> `409 ErrRevisionConflict`, not `ErrIllegalTransition`.
>
> **Guard widening (F-3)** — the no-CAS-bypass guard must ban `governancepolicy.Create` /
> `.Activate` / `.Deactivate` / `.Archive`, the **package-level functions** in `lifecycle.go`, in
> addition to the four `Repository` methods. Otherwise the ban is nominal: a handler must import that
> package for its types and could bypass CAS without naming a banned method.

Also asked, explicitly, so it is not signed by omission: **the latent lost-update hazard behind CT-5**
(§4.8.1). The legacy trio does not bump the revision, so a legacy transition is invisible to a later
`CompareAndSave`. Verified unreachable — the legacy path has no production callers — and closed by
convention plus guard. Removing it *structurally* means making the legacy trio bump the revision,
which is a Phase 14 behaviour change and is **not** proposed here. Confirm that this stays a
documented, guarded limitation.

**Honest limitation, restated so it is not signed off by accident** (accepted at R75 §6 as a
documented Phase 17 boundary, no new ADR required): the policy store and the audit store are
independent persistence domains with no transaction manager spanning them.
`audit INTENT → CompareAndSave → audit OUTCOME` is fail-closed and can leave an intent-without-outcome
record as a *detectable, reconciliation-needed* degraded state. This ADR claims **no** rollback and
**no** cross-store atomicity anywhere.

---

## 6. Phase 17 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 17.0 | Phase 17 Management API / Policy Management Scope (ADR-035) | **CLOSED (R73-B)** |
| 17.1 | Phase 17 Management API Architecture (ADR-036) | **CLOSED (R77-A)** — R74 = B → R75 = B (4 items) → R76 = B (1 blocker + OQ-17.1-B) → **R77 = A PASS** |
| 17.2 | Implementation (handler / wiring / tests + store hardening + audit migration) | **AUTHORIZED (R77)** — scope frozen at §7.5 |

Each step is authorized only after the previous is signed — same architecture-first discipline.

---

## 7. Sign-off ledger

### 7.1 Round 74 — verdict **B (Accept with modifications)**

| Item | Verdict | Round | Note |
|---|---|---|---|
| Phase 17.1 Architecture (ADR-036) | 🟨 **B** — accept w/ modifications | 74 | revised; re-signed at R75 |
| P17-0 / P17-1 separate surface (mechanical isolation) | ✅ accepted | 74 | no change required |
| P17-2 fail-closed AuthN/AuthZ | ✅ accepted | 74 | reinforced by MUST-P17-14 |
| P17-4 / P17-10 no execution bridge (AST guard) | ✅ accepted | 74 | §4.5.2 rationale accepted |
| P17-8 Revision conflict / atomicity | 🟥 **modified** | 74 | see C-1 / §4.6.2 — R74 remedy self-contradictory |
| P17-9 Audit entry (existing abstraction) | 🟥 **modified** | 74 | see C-2 / §4.6.3 — cited API does not exist |
| Repository `Create`/`Update` contract | 🟥 **must clarify first** | 74 | blocking item; resolved by `CompareAndSave` (§3.2) |
| PolicyID-derived idempotency (OQ-17.1-A) | ✅ accepted | 74 | + mandatory `409` on same ID / different payload |
| Startup hard-fail (§4.5.3) | ✅ accepted, raise to MUST | 74 | numbered MUST-P17-14 by this ADR |
| Multi-node / HA / Control Plane | ❌ excluded | 74 | out of scope, not designed for |
| P17-3 / P17-5 Repository sole owner, frozen pkgs unmodified | ✅ accepted | 74 | `CompareAndSave` is an *addition*, not a frozen-pkg edit |
| MUST-6 Architecture First (no handler before sign-off) | ✅ upheld | 74 | zero handler code written |

New MUSTs introduced by R74 (§9 最终修改清单) and folded into this ADR:

- **MUST-P17-11 — Repository Contract Integrity.** The Management API must not implicitly extend the
  Phase 14 Repository contract; any new Repository method must be an explicit contract evolution
  declared in this ADR. → §3.2 declares `CompareAndSave` explicitly. (§4.6.1)
- **MUST-P17-12 — Atomic Revision Mutation.** Revision compare + mutation + persistence must complete
  atomically *inside* the Repository; handler-side `Get → check → Save` is forbidden. → §3.2,
  and the reason R74's own 方案 1 is not implementable (§4.6.2).
- **MUST-P17-13 — *renamed at R75* → Mutation Audit Durability / Causal Recordability.** No mutation
  before a durable, error-checked audit intent; a durable, error-checked outcome must follow;
  **Phase 17 claims no cross-store atomicity.** → binding text at §3.3.1, seam at §3.3, honest field
  gap at §3.3.2.
- **MUST-P17-14 — Management Surface Startup Fail-Closed.** *Confirmed at R75 §5, numbering adopted.*
  If the configured management authentication prerequisite is absent or invalid, the Management
  surface MUST NOT bind or register; the server MUST fail closed rather than expose a writable
  endpoint without effective AuthN/AuthZ. (§3.1)

Explicitly ruled **out of scope** by R74 and not designed for here: multi-node / HA / Control Plane
concerns. Single-writer assumptions in §3.2 are legitimate only under that exclusion; if it is ever
lifted, `CompareAndSave` over a file store is insufficient and must be revisited.

### 7.2 Round 75 — verdict **B (Accept with modifications — narrow closure)**

| Item | Verdict | Round | Note |
|---|---|---|---|
| ADR-036 as revised (re-sign) | 🟨 **B** | 75 | four items to close; revised → R76 |
| Part 1 — all three factual corrections | ✅ **confirmed** | 75 | repo not atomic / no lock; no `Create`/`Update`/`Latest`; `core.AuditSink` unfit. R74 advice based on non-existent APIs is void |
| C-1 `CompareAndSave` + store hardening | ✅ **accepted & frozen** | 75 | CAS-1…CAS-6 + 5 hardening constraints, §3.2.1 |
| C-2 audit via `storage.AuditStore.Append` | 🟨 **accepted in principle** | 75 | must not be worded as atomic ⇒ P17-13 renamed, §3.3.1 |
| C-3 `policy.*` Action vocabulary | 🟨 **accepted, must be contractualized** | 75 | frozen at §3.3.3; `execute`/`plan` untouched |
| MUST-P17-14 numbering + wording | ✅ **confirmed** | 75 | verbatim text adopted at §3.1 |
| Two-store non-atomicity | ✅ **accepted as known Phase 17 limitation** | 75 | **no new ADR required**; must never be worded as rollback |
| P17-0 external/v1 read-only | 🔒 **PASS / MUST** | 75 | no external POST/PUT/DELETE, ever |
| P17-2 fail-closed AuthN/AuthZ | 🔒 **PASS / MUST** | 75 | no anonymous fallback |
| P17-10 no execution bridge | 🔒 **PASS / MUST** | 75 | AST guard + `TestNoExecMethod` are MUST-level, §3.6 |
| Authorize 17.2 | ⛔ **BLOCKED** | 75 | until the four items are closed and re-signed |

R75 signalled it *"倾向 R76 直接 PASS 进入 17.2"* provided the four items are closed and the
implementation obeys them. All four are applied (§4.7).

### 7.3 Round 76 — verdict **B (Accept with one architectural modification)**

| Item | Verdict | Round | Note |
|---|---|---|---|
| ADR-036 as revised per R75 (re-sign) | 🟨 **B** | 76 | one blocker; revised → R77 |
| §3.2.1 `CompareAndSave` frozen as sole CAS primitive | ✅ **PASS** | 76 | |
| §3.3.1 P17-13 restated (no cross-store atomicity claimed) | ✅ **PASS** | 76 | |
| §3.3.3 `policy.*` Action vocabulary frozen | ✅ **PASS** | 76 | |
| §3.1 MUST-P17-14 registered verbatim | ✅ **PASS** | 76 | |
| **OQ-17.1-B** — `Revision` + `CorrelationID` columns, `Result: "intent"` | ✅ **APPROVED** | 76 | `ExecutionID` reuse **rejected**; semantics frozen: intent = *expected* revision, outcome = *committed* revision (§3.3.2.1) |
| **Lifecycle mutations still revision-blind** | 🟥 **BLOCKER** | 76 | `activate`/`deactivate`/`archive` bypassed CAS ⇒ P17-8 not closed. Repository must expose an internal atomic transition primitive; Management must not call revision-blind lifecycle methods → `CompareAndTransition` (§3.2.2) |
| Authorize 17.2 Implementation | ⛔ **BLOCKED** | 76 | *"完成这两处后，我可以给 A — PASS，授权进入 17.2 Implementation。在这两处修改完成前，不授权写 handler。"* |

Both required changes are applied (§4.8). No handler code has been written — MUST-6 upheld through
four rounds.

### 7.4 Round 77 — verdict ✅ **A (ACCEPT / PASS)** — ADR-036 SIGNED

| Item | Verdict | Round | Note |
|---|---|---|---|
| **Phase 17.1 Architecture (ADR-036)** | ✅ **A — PASS** | 77 | signed; 17.2 unlocked |
| §3.2.2 `CompareAndTransition` (CT-1…CT-7) | ✅ PASS | 77 | |
| §3.3.2.1 OQ-17.1-B Revision/CorrelationID semantics | ✅ PASS | 77 | |
| §4 mapping — all five mutations revision-aware | ✅ PASS | 77 | |
| **CT-8** self-transition = revision-checked no-op | ✅ PASS | 77 | *"如果递增 Revision，就不再是真正的幂等操作"* — reasoning confirmed |
| **CT-9** revision compare precedes legality | ✅ PASS | 77 | fixed order: load → compare rev → `409` on mismatch → validate transition → mutate + bump |
| **F-3** guard widened to package-level helpers | ✅ PASS **"而且必须这样加强"** | 77 | frozen: *any* revision-blind lifecycle API, package-level helper, or `NextRevision→Save` chain counts as CAS bypass; enforced by AST/test guard, not code review |
| **CT-3 clarification** | ➕ added by R77 | 77 | bump only when a real mutation occurs; CT-8 no-op does not bump → §3.2.2 |
| CT-5 legacy trio limitation | ✅ accepted | 77 | + **mandatory notice**: legacy API is historical-compat, *not* part of the Phase 17 mutation contract → §4 |
| P17-0 / P17-2 / P17-10 | 🔒 PASS | 77 | three invariants re-confirmed |
| Two-store non-atomicity | ✅ accepted | 77 | *"这比虚构『原子事务』更正确"* — must remain queryable/reconciliation-detectable |

### 7.5 Phase 17.2 — authorized scope (frozen by R77)

**In scope:** `internal/management` handler + request pipeline · fail-closed AuthN/AuthZ ·
`CompareAndSave` · `CompareAndTransition` · repository hardening (mutex + temp write + fsync + atomic
rename) · CT-8 / CT-9 · audit `Revision` / `CorrelationID` / `intent` · `policy.*` Action vocabulary ·
audit migration · startup fail-closed · Composition-Root-only wiring · AST forbidden-import guard ·
No-Execution guard · No-CAS-bypass guard · unit / integration / wiring tests.

**Explicitly forbidden to slip in under 17.2:** any `external/v1` change · Runtime Contract change ·
Governance Engine change · Phase 14 legacy lifecycle semantics change · any new execution entry point ·
HA / consensus / orchestration · a new storage engine · a second Policy Store · a second Audit Store ·
any new control plane beyond the Management API.
