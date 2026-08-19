// Package cluster is the Phase 8.2 Capability (ADR-016): PLATFORM COORDINATION
// over the frozen Runtime, not a second Runtime. It is the second of the four
// Platform Operations capabilities to be implemented, and the first to carry
// real off-by-one risk — a coordination layer that drifts into owning hosts or
// executing commands is the classic Phase 8 failure mode.
//
// Design invariant (ADR-016 MUST-1..5):
//   - NO EXECUTION: it never runs a command, never opens an SSH/Command path.
//     Placement yields host references only; the host wires those references to
//     the Runtime's existing interface. Cluster has no exec method at all.
//   - DOES NOT OWN HOST: it holds a string HostRef — an opaque reference to a
//     host identity owned by controlplane/hostregistry. It never stores OS/CPU/
//     Memory/SSH of a host (that would be a second inventory).
//   - METADATA ONLY: it manages Membership / Group / Label / Placement metadata.
//     It does not manage host lifecycle, processes, services or packages.
//   - PLACEMENT != EXECUTION: ComputePlacement returns target host refs
//     ({"targetHosts":[...]}), never a command ({"command":"restart nginx"}).
//   - COMPOSED, NOT A REIMPLEMENTATION: it references the frozen systems by ID
//     and delegates execution to them. It must never import the Runtime engine
//     (internal/plugin/runtime) nor process isolation (internal/plugin/isolation)
//     nor re-own the host (internal/controlplane/hostregistry). The AST guard in
//     cluster_test.go enforces runtime/isolation/hostregistry exclusion.
//
// Architecture (ADR-016 §3):
//
//	Runtime Inventory ──HostID──▶ Cluster (references by ID)
//	                                     │ Membership/Group/Label/Placement
//	                                     ▼
//	                       ComputePlacement → []HostRef (no command)
//	                                     │
//	                  host wires HostRef → Runtime Existing Interface → Execution
//
// Cluster is pure metadata; it imports no frozen subsystem.
package cluster

// ClusterID identifies a coordination group (a logical cluster).
type ClusterID string

// HostRef is an OPAQUE reference to a host identity owned by
// controlplane/hostregistry. Cluster references it by string; it never owns,
// inspects, or stores the host's OS/CPU/Memory/SSH. This is MUST-2 enforced by
// shape: the only thing Cluster knows about a host is its ref.
type HostRef string

// MemberState is the lifecycle state Cluster is ALLOWED to own (ADR-016
// MUST-3): membership state only. It is deliberately disjoint from host
// lifecycle (provision/register/delete/discover) which stays in the Runtime
// Inventory — Cluster is a membership layer, not a host registry.
type MemberState string

const (
	// MemberJoining: the host is being admitted to the cluster.
	MemberJoining MemberState = "joining"
	// MemberActive: the host is a participating member.
	MemberActive MemberState = "active"
	// MemberLeaving: the host is being drained out of the cluster.
	MemberLeaving MemberState = "leaving"
	// MemberOffline: the host lost membership liveness (observed, not executed).
	MemberOffline MemberState = "offline"
)

// Member is Cluster's ONLY record about a host: a reference plus coordination
// metadata. The absence of any host-hardware/connection field is intentional
// (MUST-2/3) — re-adding those fields would turn this into a second inventory.
type Member struct {
	ClusterID ClusterID
	HostRef   HostRef
	Groups    []string          // logical groups for fan-out (Cluster owns grouping)
	Labels    map[string]string // free-form coordination metadata
	State     MemberState
}

// PlacementVersion identifies the schema of a Placement record. It is
// coordination metadata versioning only — NOT a Runtime Contract. It exists so
// that future consumers (rolling deployment, maintenance window, governance)
// reference a stable placement shape rather than re-interpreting an opaque
// artifact (ADR-016 Round 45 SHOULD-2).
const PlacementVersion = "cluster/placement/v1"

// Placement is the OUTPUT of coordination: a list of host references that
// satisfy a PlacementSpec. It contains NO command and NO execution intent —
// it is data the host later maps onto the Runtime's existing interface. This
// is MUST-4 (Placement ≠ Scheduling Execution) enforced by shape.
//
// Reason (ADR-016 Round 45 SHOULD-1) is explainability metadata: a
// human-readable note of why these targets were chosen (matched label,
// selected group, affinity rule). It MUST stay declarative — it must never be
// interpreted as an execution decision.
type Placement struct {
	Version string
	Targets []HostRef
	Reason  string
}

// PlacementSpec describes WHAT a placement must satisfy. It is pure selection
// criteria over member metadata — it never describes an action to perform.
type PlacementSpec struct {
	// RequireGroups: a member must belong to ALL listed groups.
	RequireGroups []string
	// RequireLabels: a member must carry ALL listed labels (exact value match).
	RequireLabels map[string]string
	// Affinity: soft preference — members whose labels match these values are
	// ordered first. Does not exclude non-matching members.
	Affinity map[string]string
	// Limit: maximum number of targets (0 = no limit, return all matches).
	Limit int
}
