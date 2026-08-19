# Phase 8.2 Cluster Coordination — Pilot Implementation Spec

> Peripheral capability package `internal/cluster` (ADR-016). Platform
> Coordination over the frozen Runtime — **not a second Runtime**.

## Scope (ADR-016 §1)

Cluster owns ONLY coordination metadata:

| Owns (Cluster)            | References by ID (frozen)        | Never owns / never does          |
|---------------------------|----------------------------------|----------------------------------|
| Membership (join/leave)   | `HostRef` → `hostregistry.Host`  | Host OS / CPU / Memory / SSH     |
| Group                     | —                                | Host lifecycle (provision/reg/delete) |
| Label                     | —                                | Process / Service / Package mgmt |
| Placement (target refs)   | `HostRef` list                   | Execute a command / open SSH      |

## Package layout

```
internal/cluster/
  model.go        Member / Placement / PlacementSpec / MemberState (pure metadata)
  cluster.go      Manager: Join/Leave/SetLabel/AddToGroup/ComputePlacement
  errors.go       metadata-layer errors (cluster/member not found)
  cluster_test.go AST guard + behavior
```

- **`Member`** carries `{ClusterID, HostRef, Groups, Labels, State}` only. The
  absence of any host-hardware/connection field is MUST-2/3 enforced by shape:
  re-adding those fields would turn it into a second inventory.
- **`HostRef`** is an opaque string reference to the host identity owned by
  `controlplane/hostregistry`. Cluster never imports that package (AST guard).
- **`ComputePlacement(spec)`** is a PURE function over member metadata returning
  `[]HostRef`. It emits `{"targetHosts":[...]}`, never `{"command":...}`
  (MUST-4). The host maps the refs onto the Runtime's existing execution
  interface — Cluster has no exec method at all (MUST-1).

## Frozen-system compliance (ADR-016 MUST-1..5)

| MUST | Mechanism |
|------|-----------|
| 1 — no execution | `Manager` exposes no exec/SSH/command method; placement returns refs only |
| 2 — does not own Host | `HostRef` is a string; `hostregistry` import forbidden by AST guard |
| 3 — metadata only | `Member` has no host-hardware/connection/lifecycle fields |
| 4 — placement ≠ execution | `Placement.Targets []HostRef`; no command field exists |
| 5 — composed, not reimplemented | AST guard forbids `runtime` / `isolation` / `hostregistry` |

## AST guard

`cluster_test.go::TestClusterDoesNotOwnOrExecute` inspects package source and
fails the build if `internal/cluster` imports `internal/plugin/runtime`,
`internal/plugin/isolation`, or `internal/controlplane/hostregistry`.

## Host wiring (reference, not part of this package)

```go
mgr := cluster.NewManager()
mgr.Join(cluster.ID("prod"), cluster.HostRef(host.Name), []string{"web"}, nil)
// ... later, coordinate:
placement := mgr.ComputePlacement(cluster.ID("prod"), cluster.PlacementSpec{
    RequireGroups: []string{"web"},
})
// host maps placement.Targets → Runtime existing interface → Execution.
// Cluster never performs the execution itself.
```

## Out-of-scope (ADR-016 §4, reaffirmed)

Second Runtime · second Inventory · new execution protocol · internal
implementation of scheduling · redefinition of the Trust Boundary · new identity
system · re-implementation of Observability.
