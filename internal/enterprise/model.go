// Package enterprise is the Phase 8.3 Capability (ADR-017): ENTERPRISE
// OPERATIONS — a Policy Layer over the four frozen/accepted systems, not an
// Enterprise Runtime. It is the third of the four Platform Operations
// capabilities to be implemented.
//
// Design invariant (ADR-017 MUST-1..5):
//   - NO EXECUTION: it never runs a command and never opens its own SSH/Command
//     path. Every execution still enters the Runtime via its existing
//     interface; Enterprise only attaches policy to the IDs involved.
//   - DOES NOT OWN HOST/CLUSTER/PLUGIN/RUNTIME: it holds opaque string refs to
//     those identities (owned by their respective frozen systems) and attaches
//     policy to them. It never re-records membership, host hardware, plugin
//     binaries or runtime state.
//   - POLICY METADATA ONLY: it owns Approval / Maintenance Window / Change
//     Process / Tenant / Organization / RBAC Extension / Policy Attachment. None
//     is an execution protocol — it never describes HOW a plugin runs.
//   - CONSTRAINS, NOT IMPLEMENTS: it may gate a run by attaching policy, but the
//     run itself is always the Runtime's. It emits a PolicyAttachment (state),
//     never an action.
//   - COMPOSES, NEVER REPLICATES: it references the four systems by ID and
//     delegates evaluation to Governance (ADR-018). It must never import the
//     Runtime engine (internal/plugin/runtime), process isolation
//     (internal/plugin/isolation), host registry
//     (internal/controlplane/hostregistry), cluster (internal/cluster),
//     observability (internal/observability), or governance
//     (internal/governance). The AST guard in enterprise_test.go enforces this.
//
// The Enterprise / Governance split (ADR-017 SHOULD-1) is enforced by shape:
// Enterprise owns *attachment* (who/what this policy applies to, in org scope);
// it has NO evaluate method. Verdict production belongs to Governance.
//
// Architecture (ADR-017 §3):
//
//	Runtime / Ecosystem / Cluster / Observability (frozen or accepted)
//	                     │ existing IDs (PluginID, RuntimeID, Group, ExecutionID)
//	                     ▼
//	          Enterprise: attach Approval/Tenant/RBAC/Window/Change to ID
//	                     │ PolicyAttachment (state, not action)
//	                     ▼
//	          Governance (ADR-018): evaluates → Verdict
//	                     │
//	                     ▼
//	          Runtime execution interface (Enterprise constrains; Runtime runs)
//
// Enterprise is pure policy metadata; it imports no frozen subsystem.
package enterprise

import "time"

// TargetKind is the class of object a policy is attached to. These are the
// existing identity classes of the frozen/accepted systems — Enterprise adds no
// new identity system (ADR-017 §3, ADR-015 MUST-4).
type TargetKind string

const (
	// TargetPlugin: a plugin identity (PluginID, owned by the Ecosystem).
	TargetPlugin TargetKind = "plugin"
	// TargetRuntime: a runtime identity (RuntimeID, owned by Runtime Core).
	TargetRuntime TargetKind = "runtime"
	// TargetHost: a host identity (HostRef, owned by controlplane/hostregistry).
	TargetHost TargetKind = "host"
	// TargetCluster: a cluster identity (ClusterID, owned by Cluster).
	TargetCluster TargetKind = "cluster"
	// TargetExecution: an execution identity (ExecutionID, owned by Runtime Core).
	TargetExecution TargetKind = "execution"
)

// PolicyKind is the kind of organizational/policy metadata Enterprise owns
// (ADR-017 §1). None of these is an execution protocol.
type PolicyKind string

const (
	// PolicyApproval: a weighed action requires explicit approval.
	PolicyApproval PolicyKind = "approval"
	// PolicyMaintenanceWindow: a time window during which actions are gated.
	PolicyMaintenanceWindow PolicyKind = "maintenance-window"
	// PolicyTenantScope: an org/tenant/business-unit scope boundary.
	PolicyTenantScope PolicyKind = "tenant-scope"
	// PolicyRBAC: an RBAC extension rule attached to an ID.
	PolicyRBAC PolicyKind = "rbac"
	// PolicyChangeFreeze: a change-process freeze on a target.
	PolicyChangeFreeze PolicyKind = "change-freeze"
)

// PolicyAttachment is the OUTPUT of Enterprise: a piece of policy metadata
// bound to an existing ID. It is declarative state (ADR-017 SHOULD-2) — it
// records WHO/WHAT a policy applies to in org scope. It is NOT a verdict and
// contains NO action. Evaluation of these attachments is Governance's job
// (ADR-018).
type PolicyAttachment struct {
	// AttachID is an opaque, enterprise-local handle used only to key and
	// detach the attachment. It is NOT a new identity system — it never
	// replaces the TargetRef it is bound to.
	AttachID string
	// TargetKind/TargetRef together reference an existing identity owned by a
	// frozen/accepted system. Enterprise holds only this opaque ref.
	TargetKind TargetKind
	TargetRef  string
	// Kind is the policy category owned by Enterprise.
	Kind PolicyKind
	// Metadata is free-form policy detail (e.g. approver, window start/end,
	// tenant id, role). It carries no execution intent.
	Metadata map[string]string
	// CreatedAt is when the attachment was recorded (observability-grade
	// timestamp, not an execution event).
	CreatedAt time.Time
}
