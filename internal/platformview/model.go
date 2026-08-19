// Package platformview implements Phase 9.1 Platform Integration Layer — a read-only facade that
// composes the four closed Phase 8 capabilities (Observability / Cluster / Enterprise / Governance)
// into stable, explainable read models.
//
// NOTE: the package path is internal/platformview (not internal/platform) because internal/platform
// is already occupied by the Phase 2.x HostSnapshot resolver (frozen, not modified by Phase 9.1).
// See ADR-020 §3 for the rationale. This keeps MUST-0 / ownership intact.
//
// Invariants (ADR-020):
//   - MUST-0: no new execution path, no Control Plane.
//   - MUST-1: read-only facade; owns no data.
//   - MUST-2: reuses existing IDs; mints no PlatformID / IntegrationID / CorrelationID.
//   - MUST-3: no command surface; only GET/query/view.
//   - MUST-4: ownership preserved; platform is a projection only.
//   - MUST-5: aggregation ≠ new model; Views are composed, never redefined.
//   - SHOULD-1: explainable (every View carries source / sourceID / collectedAt / relatedIDs).
//   - SHOULD-2: stable read contract under platform/view/v1.
//   - SHOULD-3: lazy aggregation; no stored state, no cache, no background sync.
package platformview

import "time"

// ViewVersion is the stable read contract for all composed views (SHOULD-2).
// Evolve by bumping to platform/view/v2, never by editing the frozen capabilities.
const ViewVersion = "platform/view/v1"

// Meta carries explainability fields for every composed view (SHOULD-1): each field can be traced
// back to its owning capability.
type Meta struct {
	SourceCapability string    `json:"sourceCapability"`
	SourceID         string    `json:"sourceID"`
	CollectedAt      time.Time `json:"collectedAt"`
	RelatedIDs       []string  `json:"relatedIDs,omitempty"`
}

// ObservationView is a read-only projection of an Observability Observation (MUST-5).
// Every field is copied from the owning capability; no new field is invented.
type ObservationView struct {
	ObsID       string `json:"obsId"`
	Kind        string `json:"kind"`
	TraceID     string `json:"traceId,omitempty"`
	ExecutionID string `json:"executionId,omitempty"`
	RequestID   string `json:"requestId,omitempty"`
	PluginID    string `json:"pluginId,omitempty"`
	AuditID     string `json:"auditId,omitempty"`
	Meta        Meta   `json:"meta"`
}

// PlacementView is a read-only projection of a Cluster Placement (MUST-5).
type PlacementView struct {
	Version string   `json:"version"`
	Targets []string `json:"targets"`
	Reason  string   `json:"reason,omitempty"`
	Meta    Meta     `json:"meta"`
}

// AttachmentView is a read-only projection of an Enterprise Policy Attachment (MUST-5).
type AttachmentView struct {
	AttachID   string `json:"attachId"`
	TargetKind string `json:"targetKind"`
	TargetRef  string `json:"targetRef"`
	Kind       string `json:"kind"`
	CreatedAt  string `json:"createdAt,omitempty"`
	Meta       Meta   `json:"meta"`
}

// RuleView is a read-only projection of a Governance Rule (MUST-5).
type RuleView struct {
	RuleID   string `json:"ruleId"`
	Kind     string `json:"kind"`
	Priority int    `json:"priority"`
	Meta     Meta   `json:"meta"`
}

// VerdictView is a read-only projection of a Governance Verdict (MUST-5).
type VerdictView struct {
	Code          string   `json:"code"`
	Reason        string   `json:"reason,omitempty"`
	PolicyID      string   `json:"policyId"`
	RuleID        string   `json:"ruleId,omitempty"`
	MatchedPolicy string   `json:"matchedPolicy,omitempty"`
	MatchedRule   string   `json:"matchedRule,omitempty"`
	Priority      int      `json:"priority"`
	Evidence      []string `json:"evidence,omitempty"`
	Meta          Meta     `json:"meta"`
}

// ExecutionOverview composes the four capabilities around a single execution (MUST-4/5).
type ExecutionOverview struct {
	ExecutionID string           `json:"executionId"`
	Observation *ObservationView `json:"observation,omitempty"`
	Placement   *PlacementView   `json:"placement,omitempty"`
	Attachments []AttachmentView `json:"attachments,omitempty"`
	Verdict     *VerdictView     `json:"verdict,omitempty"`
	Meta        Meta             `json:"meta"`
}

// HostPolicyStatus composes Enterprise attachments + Governance verdicts for a host (MUST-4/5).
type HostPolicyStatus struct {
	HostID      string           `json:"hostId"`
	Attachments []AttachmentView `json:"attachments,omitempty"`
	Verdicts    []VerdictView    `json:"verdicts,omitempty"`
	Meta        Meta             `json:"meta"`
}

// GovernanceSummary composes Governance rules + recent verdicts for a policy (MUST-4/5).
type GovernanceSummary struct {
	PolicyID       string        `json:"policyId"`
	MatchedRules   []RuleView    `json:"matchedRules,omitempty"`
	RecentVerdicts []VerdictView `json:"recentVerdicts,omitempty"`
	Meta           Meta          `json:"meta"`
}

// ClusterPlacementView composes Cluster membership + placement for a host (MUST-4/5).
type ClusterPlacementView struct {
	HostID    string         `json:"hostId"`
	Groups    []string       `json:"groups,omitempty"`
	Labels    []string       `json:"labels,omitempty"`
	Placement *PlacementView `json:"placement,omitempty"`
	Meta      Meta           `json:"meta"`
}
