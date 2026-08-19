# ADR-030 — Phase 14.1: Governance Policy Persistence — Architecture

- **Status**: Accepted (Round 65, signed PASS) — **Phase 14.2 implemented (R66, gate green)**
- **Date**: 2026-08-08
- **Companion to**: ADR-029 (Phase 14.0 Scope, **Accepted R64 — Direction B selected**), ADR-028
  (Phase 13, CLOSED), ADR-026 (Phase 12, CLOSED), ADR-024 (Phase 11.1, CLOSED), ADR-022 (Phase 10,
  CLOSED), ADR-021 (Architecture Baseline, frozen), ADR-018 (Governance Capability, frozen),
  ADR-017 (Enterprise, frozen), ADR-014~020 (Phases 8~9, CLOSED)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 14 — Governance Policy Persistence (the long-deferred, independently-flagged
  Major Evolution R60/R61; authorized by ADR-029)

---

## 0. Abstract

ADR-029 (R64) signed **PASS** and selected **Direction B — Governance Policy Persistence**. GPT (R64)
explicitly:
- confirmed the ADR-029 freeze boundaries hold,
- upgraded four scope iron laws to **MUST-B6~B9**, and
- ruled **external/v1 is NOT modified** in Phase 14 (stays READ ONLY; a Policy management write
  surface, if ever, is a *separate future Management API Major Evolution*).

This ADR is the **architecture** for Direction B. It introduces a single new persisted-policy package
that **owns Policy persistence + lifecycle** while keeping `governance.Engine` a pure, stateless
evaluator (ADR-018 contract unchanged). It closes the Phase 12/13 honest-empty Governance read gap by
swapping the Phase 12 honest-empty `governanceAdapter` for a real projection backed by the repository —
**without touching `external/v1`, `platformview`'s delegation chain, or `governance.Engine`**.

```
Policy Definition  ──▶  PolicyRepository (owns persistence)  ──▶  Policy Lifecycle
                                                                          │
                                              (read projection, no State)  ▼
                          platformview.GovernanceReader  ◀──  GovernancePolicyReader
                                          │
                                          ▼
                          external/v1.GetGovernanceSummary  (UNCHANGED, READ ONLY)

Governance.Engine.Evaluate(policy, state) → Verdict   (UNCHANGED — pure evaluator)
   └── policy supplied by caller from Repository; state assembled by Runtime/Observability
```

---

## 1. Phase 14 positioning — a new data-ownership package, not a re-open of Governance

Phase 14 adds **net-new Policy data ownership** (a persisted, versioned Policy entity + its store +
its lifecycle). It is *orthogonal* to "project existing state read-only" (Phase 13) — it is the
policy **source** Governance has always lacked.

| Boundary | Status in Phase 14 |
|---|---|
| `governance.Engine` (ADR-018) | **UNCHANGED** — pure `Evaluate(policy, state) → Verdict`; owns no store (B-2/B-6) |
| `internal/enterprise` (ADR-017) | untouched — still owns *attachment* metadata; does NOT gain Policy ownership |
| `internal/platformview` | delegation chain unchanged; only its injected `GovernanceReader` gets a real impl |
| `internal/external` (external/v1) | **UNCHANGED** — READ ONLY; no Policy write/management surface |
| `internal/correlation` | unchanged; its `GovernanceReader` gets the same real impl |
| `internal/harness` | swaps honest-empty adapter for the real projection (composition only) |
| **NEW** `internal/governancepolicy` | the only Policy persistence + lifecycle owner (B-1/B-6) |

---

## 2. Freeze boundaries (inherited from ADR-029 + this ADR's acceptance)

- **MUST-0 — No frozen-ownership / Runtime-Contract break, no Control Plane / new execution path.**
- **MUST-1 — Runtime Contract frozen** (ADR-010/011/012).
- **MUST-2 — Plugin Ecosystem frozen** (ADR-013).
- **MUST-3 — Phase 8 capabilities closed**; the new package is *new* data ownership, separately
  authorized by ADR-029 — it does not modify `cluster`/`observability`/`enterprise`/`governance`
  models (B-1/B-5).
- **MUST-4 — Read sources unchanged**; the new package is a read *source* only for the read layers.
- **MUST-5 — Deployment layer unchanged** in nature.
- **MUST-6 — Architecture First**; no code until this ADR is signed (R66).

### Phase-14-specific acceptance (from ADR-029 MUST-B1~B9)

| ID | Law | How the architecture satisfies it |
|---|---|---|
| B-1 | Policy ownership does not leak into other frozen capabilities | `governancepolicy.Policy` is private to the new package; no `cluster`/`observability`/`enterprise`/`platformview` import of it; no model duplication |
| B-2 | `Evaluate` contract unchanged | `governance.Engine` is **not imported by the new package's read path**; `Evaluate` stays `(Policy, State) → Verdict` with no behavioral change |
| B-3 | No write path into `external/v1` | `external/v1` is NOT modified; no Policy write/management API added here |
| B-4 | Persistence governed by AST Guard + TestNoExecMethod | the new package forbids execution-path imports and mutating/execution verbs via mechanical guards |
| B-5 | No re-opening of R51/Phase-8 ownership | no change to HostRegistry→Host→HostRef or Cluster→HostRef |
| B-6 | Repository is the **sole** Policy persistence owner | `governance.Engine` holds no repository/db/cache (see §3.1); the new package owns all of it |
| B-7 | Lifecycle separated from Evaluation | lifecycle ops live in the repository/manager; `Evaluate` only consumes an already-determined Policy |
| B-8 | `PolicyID` + `PolicyRevision` explicit, no new global identity | `PolicyID` reuses the existing Governance policy identity; `Revision` is a version attribute; Audit/Verdict trace by `PolicyID+Revision` |
| B-9 | Persistence forms no execution bridge | Policy writes never trigger Execute/Run/Apply/Schedule/Dispatch/Rollback; boundary ends at `Verdict` |

---

## 3. Architecture

### 3.1 New package — `internal/governancepolicy` (the sole Policy persistence owner)

```
internal/governancepolicy/
├── model.go        # Policy entity (PolicyID + Revision + Status + Rules + lifecycle timestamps)
├── repository.go   # Repository interface (storage boundary, swappable store)
├── lifecycle.go    # Lifecycle manager (Create/Read/Update/Version/Activate/Deactivate/Archive)
├── filerepo.go     # default file-backed implementation (JSON, single-node)
├── reader.go       # GovernancePolicyReader: adapts Repository → platformview/correlation GovernanceReader
├── errors.go       # domain errors (NotFound, Conflict, Archived, etc.)
├── repository_test.go
├── reader_test.go
└── guards_test.go  # AST Guard + TestNoExecMethod
```

**model.go — `Policy` entity (new, persisted):**
```go
// Policy is the persisted policy entity owned by governancepolicy (B-1/B-6).
// PolicyID reuses the existing Governance policy identity (B-8) — it is NOT a new
// global identity. Revision is a version attribute of that identity.
type Policy struct {
    PolicyID  string            // existing identity (governance.Policy.PolicyID)
    Revision  int               // monotonically increasing version attribute (B-8)
    Status    PolicyStatus      // active | archived (B-7 lifecycle)
    Rules     []governance.Rule // reuses the frozen Rule type — NOT redefined
    CreatedAt time.Time
    UpdatedAt time.Time
    ArchivedAt *time.Time       // archive, never hard-delete (SHOULD-B-S2)
}
```
- `governance.Rule` (frozen type: `RuleID, Priority, Kind, Param`) is **reused**, never copied or
  redefined — preserving the `Evaluate` input contract (B-2).
- `PolicyStatus` is a small closed enum (`active`/`archived`).

**repository.go — `Repository` interface (storage boundary, B-4/B-6):**
```go
type Repository interface {
    Save(ctx, Policy) error                       // Create or Update (upsert by PolicyID+Revision)
    Get(ctx, policyID string, revision int) (Policy, error)
    Latest(ctx, policyID string) (Policy, error) // highest active revision
    List(ctx, Filter) ([]Policy, error)          // by status / rule-kind
    Archive(ctx, policyID string) error          // soft archive (SHOULD-B-S2)
    Activate(ctx, policyID string) error         // B-7 lifecycle
    Deactivate(ctx, policyID string) error
    NextRevision(ctx, policyID string) int       // B-8 versioning
}
```
- Store choice (file/sqlite/remote) is **swappable** behind this interface (SHOULD-B-S1); default
  implementation is file-backed (`filerepo.go`) for single-node. No store internals leak into the
  frozen evaluator.

**lifecycle.go — lifecycle manager (B-7):** owns Create/Read/Update/Version/Activate/Deactivate/
Archive. It is **data ownership + state transitions only** — it never calls `Evaluate`, never imports
`governance.Engine`, never triggers any Runtime action (B-2/B-9).

**reader.go — `GovernancePolicyReader` (read-source adapter):**
```go
// Satisfies BOTH platformview.GovernanceReader and correlation.GovernanceReader.
// Backed by the Repository; projects persisted Policy → read DTOs.
type Reader struct{ repo Repository }

func (r *Reader) QueryRules(ctx, policyID) ([]platformview.RuleView, error) {
    p, err := r.repo.Latest(ctx, policyID)        // real, non-empty projection
    if err != nil { return nil, nil }              // honest-empty on miss
    out := make([]platformview.RuleView, 0, len(p.Rules))
    for _, rule := range p.Rules {
        out = append(out, platformview.RuleView{
            RuleID: rule.RuleID, Kind: string(rule.Kind), Priority: rule.Priority,
            Meta: platformview.Meta{SourceCapability: "governancepolicy", SourceID: policyID},
        })
    }
    sort.Slice(out, func(i, j int) bool {          // deterministic (MUST-13.2 spirit)
        if out[i].Priority != out[j].Priority { return out[i].Priority > out[j].Priority }
        return out[i].RuleID < out[j].RuleID
    })
    return out, nil
}

func (r *Reader) QueryVerdict(ctx, policyID) (*platformview.VerdictView, error) {
    return nil, nil   // HONEST-EMPTY: a Verdict requires State, which the read layer has no
                      // access to. Evaluate(policy, state) is invoked by the Runtime/caller
                      // (B-2/B-7). The read surface never fabricates a verdict.
}

func (r *Reader) QueryPolicyRefs(ctx, scope correlation.Scope) ([]string, error) {
    ps, _ := r.repo.List(ctx, Filter{Status: Active})
    refs := make([]string, 0, len(ps))
    for _, p := range ps {                          // optional scope match; stable order
        if scope.Ref == "" || strings.Contains(p.PolicyID, scope.Ref) { refs = append(refs, p.PolicyID) }
    }
    sort.Strings(refs)
    return refs, nil
}
```

### 3.2 Harness wiring swap (composition only — no capability change)

`internal/harness/harness.go` today (R63 state) injects `governanceAdapter{e: cap.gov}` (honest-empty).
Phase 14.2 replaces it:
```go
// before: gov := &governanceAdapter{e: cap.gov}            // honest-empty
// after:
repo := governancepolicy.NewFileRepository(cap.policyStore) // single-node default
gov  := governancepolicy.NewReader(repo)                    // real projection
```
The `CapabilityBundle` gains one field `policyStore string` (path/config), populated from
`HarnessConfig` — **no new execution entry** (B-9). `cap.gov` (`governance.Engine`) remains for any
caller-side `Evaluate` use; the new package does not replace it.

### 3.3 What stays frozen (explicit non-changes)

- `internal/governance` — `Engine`, `Evaluate`, `Policy`, `Verdict`, `Rule` all unchanged (B-2).
- `internal/platformview` — `GetGovernanceSummary` already delegates to `readers.Governance`; only the
  injected impl changes. `GovernanceReader` interface unchanged.
- `internal/correlation` — `GovernanceReader.QueryPolicyRefs` signature unchanged.
- `internal/external` (external/v1) — **zero edits**; `GetGovernanceSummary` continues to call
  `platformview.GetGovernanceSummary` and stays READ ONLY (GPT R64 explicit).
- `cmd/opscore-server` — unchanged.

---

## 4. Acceptance mapping (how each law is verifiable)

| Law | Verification |
|---|---|
| B-1 ownership isolation | AST Guard: `governancepolicy` must NOT import `cluster`/`observability`/`enterprise`/`platformview`; `TestNoExecMethod` forbids execution verbs |
| B-2 Evaluate unchanged | `governance` package diff is empty; `governancepolicy` must NOT import `governance.Engine` in its read/lifecycle path |
| B-3 external/v1 untouched | `git diff internal/external` empty; ADR-030 §3.3 records it |
| B-4 guards | `guards_test.go` runs AST scan + `TestNoExecMethod` (forbidden verbs: Run/Exec/Invoke/Apply/Execute/Command/Emit/Dispatch/Rollback/Kill/Schedule/Mutate/Resolve/Replay/Remediate) |
| B-5 no R51 reopen | HostRegistry→Host→HostRef / Cluster→HostRef untouched (no diff in frozen packages) |
| B-6 sole owner | `governance.Engine` has no repository/db field; `governancepolicy` is the only package holding Policy persistence |
| B-7 lifecycle≠eval | `lifecycle.go` has no `Evaluate` call; `reader.QueryVerdict` returns honest-empty (no State) |
| B-8 PolicyID+Revision | `Policy.PolicyID` reuses existing identity; `Revision` is an int attribute; Audit trace uses `PolicyID+Revision` |
| B-9 no exec bridge | Policy writes in `lifecycle.go` have no Runtime-call side effects; `governancepolicy` forbids importing execution-path packages |

---

## 5. Mechanical guards (carried from Phase 8/13 discipline)

- **AST Guard** (per-package `_test.go`): parses the package's own imports; fails if it imports any
  execution-path package (`internal/plugin/runtime`, `internal/plugin/isolation`,
  `internal/controlplane/hostregistry`, `internal/cluster`, `internal/observability`,
  `internal/enterprise`) or reaches into frozen *implementation* internals.
- **`TestNoExecMethod`**: scans all method receivers in the package; fails on any method whose name is
  an execution/mutation verb (Run / Exec / Invoke / Apply / Execute / Command / Emit / Dispatch /
  Rollback / Kill / Schedule / Mutate / Resolve / Replay / Remediate). The package is data ownership +
  read projection, never capability logic.
- **Determinism**: `QueryRules` / `QueryPolicyRefs` stable-sort by a total order (MUST-13.2 spirit).

---

## 6. Integration points (exact, code-level)

| Surface | Today (R63) | Phase 14.2 | Frozen? |
|---|---|---|---|
| `platformview.GovernanceReader` | honest-empty `governanceAdapter` | `governancepolicy.Reader` (real `QueryRules`) | interface unchanged |
| `correlation.GovernanceReader` | honest-empty `governanceAdapter` | `governancepolicy.Reader` (real `QueryPolicyRefs`) | interface unchanged |
| `external/v1.GetGovernanceSummary` | delegates → empty | delegates → real Rules, empty Verdict | **method unchanged** |
| `governance.Engine.Evaluate` | pure | pure | **unchanged** |
| `HarnessConfig` | has capability bundles | + `PolicyStore` path | additive field only |

---

## 7. Roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 14.0 | Phase 14 Next Major Evolution Scope (ADR-029) | **signed PASS R64 — Direction B** |
| 14.1 | Governance Policy Persistence Architecture (this ADR-030) | **signed PASS R65** |
| 14.2 | Implementation: `internal/governancepolicy` + harness wiring swap | **implemented R66 (gate green)** |

---

## 8. Sign-off placeholder (Round 65)

| Item | Verdict | Round |
|---|---|---|
| MUST-0 No frozen-ownership / Runtime-Contract break, no Control Plane / new execution path | ✅ PASS | 65 |
| MUST-1 Runtime Contract remains frozen | ✅ PASS | 65 |
| MUST-2 Plugin Ecosystem remains frozen | ✅ PASS | 65 |
| MUST-3 Phase 8 capabilities remain closed (new package = separately-authorized data ownership) | ✅ PASS | 65 |
| MUST-4 Read sources unchanged (new package is read-source only) | ✅ PASS | 65 |
| MUST-5 Deployment layer unchanged in nature | ✅ PASS | 65 |
| MUST-6 Architecture First (implementation only after this ADR signed) | ✅ PASS | 65 |
| B-1 Policy ownership isolated from other frozen capabilities | ✅ PASS | 65 |
| B-2 `governance.Engine.Evaluate` contract unchanged | ✅ PASS | 65 |
| B-3 No write path into `external/v1` (external unmodified) | ✅ PASS | 65 |
| B-4 Persistence governed by AST Guard + TestNoExecMethod | ✅ PASS | 65 |
| B-5 No re-opening of R51/Phase-8 ownership | ✅ PASS | 65 |
| B-6 Repository is sole Policy persistence owner (Engine owns none) | ✅ PASS | 65 |
| B-7 Lifecycle separated from Evaluation | ✅ PASS | 65 |
| B-8 `PolicyID` + `PolicyRevision` explicit, no new global identity | ✅ PASS | 65 |
| B-9 Persistence forms no execution bridge | ✅ PASS | 65 |

*Phase 14.1 Governance Policy Persistence — Architecture ADR. **Phase 14.2 implemented (R66),
signed-off PASS, gate green.** (Accepted, Round 65).*

---

## 9. Implementation Notes (R66 — what was actually built)

**New package `internal/governancepolicy/` (662 LoC, 6 files):**
- `model.go` (59) — `PolicyRecord` entity: `PolicyID` (reuses existing identity, B-8) + `Revision int`
  + `PolicyStatus` enum (`active`/`archived`) + `Rules []governance.Rule` (reused, never redefined,
  B-2) + `CreatedAt`/`UpdatedAt`/`ArchivedAt *time.Time` lifecycle timestamps.
- `errors.go` (15) — `ErrNotFound` / `ErrInvalidID` / `ErrConflict` domain errors.
- `repository.go` (212) — `Repository` interface (Save / Get / Latest / List / Archive / Activate /
  Deactivate / NextRevision) **+ file-backed implementation** (`NewFileRepository`, JSON, single-node,
  `safeName` path-traversal guard, stable sort). NOTE: the file store lives inside `repository.go`
  rather than a separate `filerepo.go` — package boundary is the same, only the file split differs
  from §3.1's sketch.
- `lifecycle.go` (64) — `Create` / `Activate` / `Deactivate` / `Archive` operate on the Repository
  only; **no `Engine.Evaluate` call, no execution-path import** (B-2/B-7/B-9).
- `reader.go` (87) — `Reader` adapts `Repository` → `platformview.GovernanceReader` (`QueryRules`
  real projection, stable-sorted by `Priority desc, RuleID asc`; `QueryVerdict` honest-empty `nil`,
  Verdict needs State) + `correlation.GovernanceReader` (`QueryPolicyRefs` lists active PolicyIDs,
  stable-sorted). Imports only `internal/governance` (type reuse) + `platformview` + `correlation`.
- `guards_test.go` (225) — `TestNoExecMethod` (forbids execution verbs; **permits** persistence /
  lifecycle verbs Save/Activate/Archive/Create since the package legitimately owns them),
  `TestNoEngineEval` (no `NewEngine(` / `.Evaluate(` call — B-7), `TestRepositoryContract`,
  `TestReaderDeterministic`.

**Harness wiring swap (`internal/harness/`, 3 files, −11/+16 net):**
- `config.go` — added `PolicyStoreDir string` field + `DefaultPolicyStoreDir` const (additive config only).
- `harness.go` — `capabilities` gained `polRepo Repository`; `Build` constructs
  `governancepolicy.NewFileRepository(cfg.PolicyStoreDir)` and replaces the honest-empty
  `governanceAdapter` with `governancepolicy.NewReader(polRepo)`. `cap.gov` (`governance.Engine`)
  retained for caller-side `Evaluate`.
- `adapters.go` — removed dead `governanceAdapter` + `governance` import.

**Frozen packages — zero diff confirmed:** `git diff` on `internal/external`, `internal/platformview`,
`internal/correlation`, `internal/governance` is empty. `external/v1.GetGovernanceSummary` stays
READ ONLY (B-3, GPT R64 explicit).

**Gate:** `gofmt -l` clean; `go build ./...` ok; `go vet ./internal/governancepolicy/...
./internal/harness/...` ok; `go test ./...` → **EXIT 0, 0 FAIL, 50 packages green**.
