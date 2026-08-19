# ADR-028 — Phase 13.1: Cluster Host-Centric Read Projection (Architecture)

- **Status**: Accepted (Round 62, signed PASS) — **Phase 13.2 implemented (R63)**; **Phase 13 CLOSED (R63)**
- **Date**: 2026-08-08
- **Companion to**: ADR-027 (Phase 13.0 Scope, **Accepted R61** — Direction A selected), ADR-026
  (Phase 12, CLOSED), ADR-024 (Phase 11.1, CLOSED), ADR-020 (Phase 9.1, CLOSED), ADR-016 (Cluster
  capability, CLOSED), ADR-021 (Architecture Baseline, frozen)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 13 — Read Surface Completeness (Direction A), scoped to **Cluster host-centric
  read projection only**; Governance policy storage excluded (future independent Major Evolution)

---

## 0. Abstract — the empty read surface, root-caused

Phase 12 (ADR-026, R60) wired the frozen six-tier system into a single runnable `opscore` process via
`internal/harness` + `cmd/opscore-server`, mounting `external/v1`. GPT (R60) honestly noted that
`external/v1` projects an **empty view** for the Cluster surface: `GetHost` returns a `ClusterPlacementView`
with no `Groups` / `Labels` / `Placement`.

Root cause (verified in code, not a defect):

- `external/v1.GetHost` already calls `platformview.GetClusterPlacementView` (facade.go:138), which
  already delegates to `f.readers.Cluster` — a `platformview.ClusterReader` (facade.go:15). The
  delegation chain is **correct and frozen**.
- Phase 12's `internal/harness/harness.go:68` injects `clusterAdapter{m: cap.cl}`, where `cap.cl` *is* a
  real `*cluster.Manager` — but the adapter **returns empty** for `QueryMemberGroups` /
  `QueryMemberLabels` / `QueryPlacement` (adapters.go:100-110). It was an intentionally "honest empty"
  stand-in because cluster is ClusterID-scoped and exposes no host-centric query (adapters.go:93-96).

Therefore the fix is **narrow and surgical**: replace the honest-empty `clusterAdapter` with a real
read-only projection that adapts the *already-present* `*cluster.Manager` into the `ClusterReader`
contract. **`external/v1`, `platformview.GetClusterPlacementView`, and the `ClusterReader` interface
require zero changes** — they already do the right thing once a real reader is injected.

This ADR is **spec only**. Per ADR-021 / ADR-027 MUST-6, no code lands until this architecture ADR is
signed (R63).

---

## 1. Architecture — who reads what, and the new projection seam

```
                ┌──────────────────────────────────────────────────────────┐
                │  internal/cluster  (frozen, Phase 8 capability — CLOSED)  │
                │  • NewManager() *Manager                                   │
                │  • (*Manager) Members(cid ClusterID) []Member              │
                │  • (*Manager) ComputePlacement(cid, spec) Placement        │
                │  • Member{HostRef, Groups, Labels, State}  ← metadata ONLY │
                └───────────────────────────────┬──────────────────────────┘
                                                 │ read-only, HostRef-only
                                                 ▼
                ┌──────────────────────────────────────────────────────────┐
                │  internal/clusterprojection  ★ NEW (Phase 13.2)            │
                │  • Reader adapts *cluster.Manager → host-centric queries  │
                │  • satisfies platformview.ClusterReader                    │
                │  • satisfies correlation.ClusterReader                     │
                │  • imports ONLY internal/cluster (metadata API)            │
                │  • MUST NOT import hostregistry / runtime / isolation      │
                └───────────────────────────────┬──────────────────────────┘
                          implements ▼                                          ▼ implements
   ┌──────────────────────────┐                              ┌──────────────────────────────┐
   │ platformview.ClusterReader│                              │ correlation.ClusterReader      │
   │ (facade.go:15)            │                              │ (QueryPlacementRefs)          │
   └────────────┬──────────────┘                              └──────────────────────────────┘
                │  injected as readers.Cluster
                ▼
   platformview.Facade.GetClusterPlacementView  (facade.go:138 — UNCHANGED)
                │
                ▼
   external/v1.GetHost  (mapHost merges ClusterPlacementView — UNCHANGED)
```

The chain preserves the R57/R60 frozen External ownership boundary (MUST-A5): `external` never reads
`cluster` directly; it reads the platformview facade, which reads `clusterprojection`, which reads
`cluster`.

---

## 2. New package — `internal/clusterprojection`

**Purpose.** A read-only adapter that turns `cluster`'s ClusterID-scoped, member-oriented metadata into
the host-centric `ClusterReader` contract that `platformview` and `correlation` already consume.

**Ownership / boundaries (MUST-A1/A2/A5).**
- Imports **only** `internal/cluster` (the sanctioned read source). It MUST NOT import
  `internal/controlplane/hostregistry`, `internal/plugin/runtime`, `internal/plugin/isolation`, or any
  Runtime Executor / SSH / lifecycle API.
- Holds a `*cluster.Manager` reference (passed by the harness composition root). It **references**
  `cluster.HostRef` (opaque string); it never copies, owns, or inspects the host's OS/CPU/Memory/SSH.
- Has no state of its own beyond the manager reference and a read-only cluster-ID index (see §4). It
  owns no data, no cache, no background sync (consistent with platformview SHOULD-3).

**Type.**
```go
package clusterprojection

import "github.com/YuDong999/opscore/internal/cluster"

// Reader adapts *cluster.Manager to the host-centric ClusterReader contract.
// It is a projection only — it adds no capability, no execution path, no model.
type Reader struct {
    m   *cluster.Manager
    ids []cluster.ClusterID // read-only index of known cluster IDs
}

// NewReader builds a projection over the given manager and the known cluster-ID set.
func NewReader(m *cluster.Manager, ids []cluster.ClusterID) *Reader
```

**Methods (implement `platformview.ClusterReader`).** All are pure, read-only scans over
`cluster.Members(cid)`; none mutate cluster state.

- `QueryMemberGroups(ctx, hostRef) ([]string, error)`
  Scan `r.ids`; for each `cid`, iterate `m.Members(cid)`; find the `Member` whose `HostRef == hostRef`;
  return its `Groups`. Honest-empty if no member matches (MUST-A6).
- `QueryMemberLabels(ctx, hostRef) ([]string, error)`
  Same scan; return `Member.Labels` as `"k=v"` strings (or the raw map view already defined by
  platformview — TBD at implementation, shape-preserving only). Honest-empty if unmatched.
- `QueryPlacement(ctx, hostRef) (*platformview.PlacementView, error)`
  Locate the cluster containing `hostRef` (same scan). Call `m.ComputePlacement(cid, <the cluster's last
  spec>)` **or** reuse the cluster's already-stored placement record — whichever the frozen `cluster`
  API exposes as read-only. Build `PlacementView{Version, Targets, Reason}` from the existing
  `cluster.Placement`; do **not** redefine placement semantics (MUST-A3). If the host is not a placement
  target, return `nil` (honest-empty), never fabricate targets.

**Methods (implement `correlation.ClusterReader`).**
- `QueryPlacementRefs(ctx, scope) ([]string, error)`
  Return the `HostRef`s of members within `scope` (used by `internal/correlation` for cross-capability
  fan-out). Pure read over `m.Members`.

> **MUST-A4 — no execution path.** `Reader` exposes only these query methods. It MUST NOT gain
> `Run/Exec/Execute/Command/Apply/Schedule/Dispatch` or any Runtime/SSH/lifecycle method. The package is
> covered by the Phase 8 AST Guard + `TestNoExecMethod` (§6) — not code-review-only.

---

## 3. Harness wiring change (the only behavioral change in Phase 13.2)

`internal/harness/harness.go:68` currently builds:
```go
cl := &clusterAdapter{m: cap.cl}   // honest-empty stand-in
```
Replace with the real projection:
```go
cl := clusterprojection.NewReader(cap.cl, knownClusterIDs)
```
where `knownClusterIDs` is the read-only cluster-ID set the harness tracks (or obtains per §4). The rest
of the wiring (`platformview.Readers{... Cluster: cl ...}` and `correlation.Readers{... Cluster: cl ...}`
at harness.go:72-73) is **unchanged** — the projection satisfies both `ClusterReader` interfaces the
existing `clusterAdapter` already satisfied.

**No change** to: `external/v1` (GetHost already merges the view), `platformview` (delegation correct),
`correlation` (interface unchanged), `cmd/opscore-server` (already imports harness), `internal/cluster`
model/ownership.

---

## 4. Frozen packages touched — explicit, minimal

| Package | Change in Phase 13.2 | Authorized by |
|---|---|---|
| `internal/clusterprojection` | **NEW** package | ADR-027 A1–A6 |
| `internal/harness` | swap `clusterAdapter` → `clusterprojection.NewReader` | ADR-027 MUST-3 (read-only wiring) |
| `internal/cluster` | **At most ONE** read-only addition: `(*Manager) Clusters() []ClusterID` (enumeration only) — **iff** the harness cannot otherwise supply `knownClusterIDs`. This is a read-only accessor, not a model/ownership change, consistent with A1/A2/A3 and MUST-3's read-only-addition carve-out. If the harness already tracks IDs, this addition is **not needed**. | ADR-027 MUST-3 + A2 |
| `external/v1` | **NONE** | MUST-A5 (boundary preserved) |
| `internal/platformview` | **NONE** (delegation already correct) | MUST-A5 |
| `internal/correlation` | **NONE** (interface already satisfied) | MUST-A4 |
| `cmd/opscore-server` | **NONE** | — |

The cluster-ID index: the projection needs the set of cluster IDs to scan. Two acceptable sources,
**either** suffices:
1. The harness already knows the IDs it created (it owns the `*cluster.Manager` lifecycle) → pass them
   in; `internal/cluster` is untouched.
2. Otherwise add the minimal read-only `Clusters()` accessor to `internal/cluster`.

This keeps Phase 13 strictly within the read-surface scope — no new capability, no model change.

---

## 5. Acceptance criteria — ADR-027 MUST-A1…A6 mapped

| ADR-027 iron law | How ADR-028 satisfies it |
|---|---|
| **A1 Read Projection Only** | `clusterprojection` is a host-centric Read Model; it reuses `Members`/`ComputePlacement`, never copies Host or duplicates HostRegistry. |
| **A2 Host Ownership unchanged** | Imports only `internal/cluster`; references `cluster.HostRef` opaquely; never imports `hostregistry`. `HostRegistry ─owns─▶ Host ─▶ HostRef`; `Cluster ─references─▶ HostRef`. |
| **A3 Placement semantics unchanged** | `QueryPlacement` returns a `PlacementView` built from the existing `cluster.Placement` (Version/Targets/Reason). No redefinition of the placement algorithm. |
| **A4 No execution path** | Only query methods; AST Guard + `TestNoExecMethod` forbid `Run/Exec/Execute/Command/Apply/Schedule/Dispatch` and Runtime/SSH/lifecycle APIs. |
| **A5 External does not read Cluster directly** | Chain preserved: `external → platformview → clusterprojection → cluster`. `external/v1` unchanged. |
| **A6 Honest completeness** | Unmatched hostRef / missing placement → empty/omitted/`nil`, never derived or fabricated. No re-read of OS/CPU/SSH HostRuntime. |
| **MUST-13.1 Projection computes no new fact** | `clusterprojection` only reads → filters → sorts → projects existing cluster metadata. It does **not** derive a new placement decision, new host state, or recompute membership. `QueryPlacement` reuses `cluster.ComputePlacement` (the cluster's own pure function) with an empty spec — surfacing existing active-membership state, not a new decision. |
| **MUST-13.2 Deterministic output** | Every result from unordered sources is stable-sorted before return: Cluster IDs, HostRefs, Groups, Labels, Placement targets. Identical input → identical View/JSON. |
| **MUST-13.3 Reader contract not reverse-polluted** | `clusterprojection` imports `platformview`/`correlation` **only for their value/interface types**; those packages do **not** import `clusterprojection`. Dependency flows one way: `cluster → clusterprojection → Reader interface → platformview/correlation`. No cycle. |
| **MUST-13.4 Phase 13 scope lock** | R63 touches **only** the Cluster read surface. Forbidden to smuggle in: Governance policy storage / policy persistence / HTTP API change / External DTO change / Runtime Contract mod / Cluster mutation API / HostRegistry ownership / Cache / DB / background sync / distributed coordination. Any of these requires its own ADR. |

---

## 6. Mechanical guardrails (carry from Phase 8 discipline)

- The new `internal/clusterprojection` package is subject to the **AST ownership guard** (forbids
  importing `internal/plugin/runtime`, `internal/plugin/isolation`,
  `internal/controlplane/hostregistry`) **and** a `TestNoExecMethod` that fails if any forbidden verb
  method (`Run/Exec/Invoke/Apply/Execute/Command/Emit/Dispatch/Rollback/Kill/Schedule/...`) appears.
- `go vet` / `go test ./...` must stay green across all ~44 packages; the pre-commit quality gate runs
  before commit (commit message single-quoted, **not pushed**).

---

## 7. Out of scope / forbidden (Phase 13.2)

- **Governance policy storage** — excluded entirely (future independent Major Evolution, ADR-027).
- Any change to the `external/v1` Public Contract, `platformview` delegation, or `correlation` interface.
- Any new capability, write/mutate API, or Control Plane.
- Copying Host, owning Host lifecycle, or re-adding OS/CPU/Memory/SSH fields to cluster (A1/A2).
- Redefining the placement algorithm (A3).
- Multi-node consensus / replication (out of Phase 13 scope per ADR-027).

---

## 8. Sign-off placeholder (Round 62)

| Item | Verdict | Round |
|---|---|---|
| MUST-A1 Read Projection Only (no Host copy / no HostRegistry duplicate) | ✅ PASS | 62 |
| MUST-A2 Host Ownership unchanged (references HostRef only; no hostregistry import) | ✅ PASS | 62 |
| MUST-A3 Placement semantics unchanged (reuses cluster.Placement) | ✅ PASS | 62 |
| MUST-A4 No execution path (AST Guard + TestNoExecMethod) | ✅ PASS | 62 |
| MUST-A5 External chain preserved (external → platformview → projection → cluster) | ✅ PASS | 62 |
| MUST-A6 Honest completeness (empty/omitted, never fabricated) | ✅ PASS | 62 |
| MUST-13.1 Projection computes no new fact | ✅ PASS | 63 |
| MUST-13.2 Deterministic output (stable sort) | ✅ PASS | 63 |
| MUST-13.3 Reader contract not reverse-polluted (no cycle) | ✅ PASS | 63 |
| MUST-13.4 Phase 13 scope lock (no smuggled Governance/Cache/etc.) | ✅ PASS | 63 |
| Frozen packages: external/v1, platformview, correlation, cluster model UNCHANGED (only 1 read-only `Clusters()` added) | ✅ PASS | 62 |
| Architecture First — implementation only after this ADR signed (R63) | ✅ PASS | 62 |

---

## 9. Phase 13 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 13.0 | Phase 13 Scope (ADR-027) | **Accepted R61 (signed PASS; Direction A)** |
| 13.1 | Cluster Host-Centric Read Projection Architecture (this ADR-028) | **Accepted R62 (signed PASS)** |
| 13.2 | Implementation: `internal/clusterprojection` + harness wiring swap + `cluster.Clusters()` read-only accessor | **Implemented R63 (signed-off PASS; gate green)** |

Each step is authorized only after the previous is signed — same architecture-first discipline as
Phases 8→12.

## 10. Implementation notes (R63)

Committed after sign-off (gate green: `go build/vet/test` all 44+ packages pass). Files:

- `internal/cluster/cluster.go` — added **one** read-only method `Clusters() []ClusterID` (sorted;
  returns existing IDs only; no side effect / store / cache / host ownership). This is the **only**
  change to the frozen `cluster` package (ADR-028 §4; GPT R62: allowed as last resort, forbidden to
  duplicate Cluster inventory).
- `internal/clusterprojection/reader.go` (**NEW**) — `Reader` adapts `*cluster.Manager` to
  `platformview.ClusterReader` + `correlation.ClusterReader`. Imports **only** `internal/cluster`
  (metadata API); references `cluster.HostRef` opaquely. Methods: `QueryMemberGroups`,
  `QueryMemberLabels`, `QueryPlacement` (reuses `cluster.ComputePlacement(cid, PlacementSpec{})` UNDER
  EMPTY CONSTRAINTS → projects the cluster's EXISTING active-membership state; the returned Reason is the
  cluster's own ComputePlacement explanation, NOT a decision by clusterprojection — MUST-13.1 no new
  fact; precisely "current active-membership placement projection under empty constraints", NOT
  "recomputing optimal placement for the host"), `QueryPlacementRefs` (correlation host-scoped co-member
  refs). All unordered sources stable-sorted (MUST-13.2).
- `internal/clusterprojection/reader_test.go` (**NEW**) — `TestProjection` (groups/labels/placement
  correctness + determinism + honest-empty for unknown host + correlation refs) and `TestNoExecMethod`
  (parses the package's `.go` files; fails on any receiver method whose name begins with a forbidden
  execution verb — MUST-A4 / MUST-13.4 mechanical guard).
- `internal/harness/harness.go` — `realReaders` now wires `clusterprojection.NewReader(cap.cl)` instead
  of the honest-empty `clusterAdapter`.
- `internal/harness/adapters.go` — removed the now-dead `clusterAdapter` (and its unused `cluster`
  import). `external/v1`, `platformview`, `correlation`, `cmd/opscore-server` are **untouched**.

Result: `external/v1.GetHost` now returns a real `ClusterPlacementView` (Groups/Labels/Placement) when
the host is a cluster member — closing the Phase 12 honest-empty gap for the Cluster surface, with zero
change to any frozen read contract. Governance remains honestly empty (deferred to a future independent
Major Evolution).

*Phase 13.1 Cluster Host-Centric Read Projection — Architecture ADR. **Phase 13 CLOSED (R63):
13.0/13.1/13.2 all Accepted-signed PASS; gate green.** QueryPlacement semantics precisely defined as
"current active-membership placement projection under empty constraints"; Reason is cluster's own
ComputePlacement explanation, not a clusterprojection decision (MUST-13.1). (Accepted Round 62; closed
Round 63).*
