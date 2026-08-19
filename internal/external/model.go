// Package external implements Phase 11.1 External Interface Architecture — a stable, versioned,
// read-only Public Contract (external/v1) that exposes internal/platformview and internal/correlation
// to external consumers (Read API / CLI / SDK / Integration Adapter).
//
// It is a PURE CONSUMPTION BOUNDARY over the frozen OpsCore v1 baseline (ADR-021/023/024). It adds
// no execution path, modifies no frozen contract, and owns nothing — it owns the DTO contract only
// (MUST-4). See ADR-024 for the full spec.
//
// Invariants (ADR-024):
//   - MUST-0: no new execution entry; does not become a Control Plane; never wraps an Executor.
//   - MUST-1: read-only contract; reads platformview/correlation only.
//   - MUST-2: reuses existing IDs; mints no ExternalID / APITokenID-as-entity.
//   - MUST-3: no command surface; only GET/query/view (Get* methods).
//   - MUST-4: Contract Ownership — owns the DTO contract, not the Domain/Event/Runtime Model.
//   - MUST-5: external/v1 DTO composes facades; it never redefines core models.
//   - MUST-6: frozen, versioned contract under external/v1.
//   - SHOULD-1: explainable DTO (ViewVersion + sourceRefs + correlatedAt provenance).
//   - SHOULD-2: Public Contract Compatibility (v1+v2 coexist; no silent breaking change).
//   - SHOULD-3: Read Boundary Enforcement (AST guard forbids frozen/executor imports).
package external

// ContractVersion is the frozen, versioned read contract for all external DTOs (MUST-6).
// Evolve by bumping to external/v2, never by editing external/v1 (SHOULD-2).
const ContractVersion = "external/v1"

// DTOViewMeta carries provenance so a consumer can trace each field back to its owning facade
// (SHOULD-1). It mirrors platformview.Meta / correlation.Meta explainability.
type DTOViewMeta struct {
	ViewVersion  string   `json:"viewVersion"` // "external/v1"
	SourceView   string   `json:"sourceView"`  // "platform/view/v1" | "correlation/view/v1"
	SourceRefs   []string `json:"sourceRefs,omitempty"`
	CorrelatedAt string   `json:"correlatedAt,omitempty"`
}

// ObservationDTO is a read-only projection of platformview.ObservationView (MUST-5).
type ObservationDTO struct {
	ObsID       string `json:"obsId"`
	Kind        string `json:"kind"`
	TraceID     string `json:"traceId,omitempty"`
	ExecutionID string `json:"executionId,omitempty"`
	RequestID   string `json:"requestId,omitempty"`
	PluginID    string `json:"pluginId,omitempty"`
	AuditID     string `json:"auditId,omitempty"`
}

// PlacementDTO is a read-only projection of platformview.PlacementView (MUST-5).
type PlacementDTO struct {
	Version string   `json:"version"`
	Targets []string `json:"targets"`
	Reason  string   `json:"reason,omitempty"`
}

// AttachmentDTO is a read-only projection of platformview.AttachmentView (MUST-5).
type AttachmentDTO struct {
	AttachID   string `json:"attachId"`
	TargetKind string `json:"targetKind"`
	TargetRef  string `json:"targetRef"`
	Kind       string `json:"kind"`
	CreatedAt  string `json:"createdAt,omitempty"`
}

// RuleDTO is a read-only projection of platformview.RuleView (MUST-5).
type RuleDTO struct {
	RuleID   string `json:"ruleId"`
	Kind     string `json:"kind"`
	Priority int    `json:"priority"`
}

// VerdictDTO is a read-only projection of platformview.VerdictView (MUST-5).
type VerdictDTO struct {
	Code          string   `json:"code"`
	Reason        string   `json:"reason,omitempty"`
	PolicyID      string   `json:"policyId"`
	RuleID        string   `json:"ruleId,omitempty"`
	MatchedPolicy string   `json:"matchedPolicy,omitempty"`
	MatchedRule   string   `json:"matchedRule,omitempty"`
	Priority      int      `json:"priority"`
	Evidence      []string `json:"evidence,omitempty"`
}

// ExecutionView is a read-only DTO projection of platformview.ExecutionOverview (MUST-4/5).
type ExecutionView struct {
	ExecutionID string          `json:"executionId"`
	Observation *ObservationDTO `json:"observation,omitempty"`
	Placement   *PlacementDTO   `json:"placement,omitempty"`
	Attachments []AttachmentDTO `json:"attachments,omitempty"`
	Verdict     *VerdictDTO     `json:"verdict,omitempty"`
	Meta        DTOViewMeta     `json:"meta"`
}

// ScopeDTO declares the correlation boundary passed by a consumer (MUST-5 analogue: explicit).
type ScopeDTO struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// CorrelationView is a read-only DTO projection of correlation.CorrelationView (MUST-4/5).
type CorrelationView struct {
	Scope           ScopeDTO    `json:"scope"`
	ExecutionRef    string      `json:"executionRef,omitempty"`
	ObservationRefs []string    `json:"observationRefs,omitempty"`
	VerdictRefs     []string    `json:"verdictRefs,omitempty"`
	PlacementRefs   []string    `json:"placementRefs,omitempty"`
	PolicyRefs      []string    `json:"policyRefs,omitempty"`
	Reason          string      `json:"reason,omitempty"`
	CorrelatedAt    string      `json:"correlatedAt,omitempty"`
	Meta            DTOViewMeta `json:"meta"`
}

// HostView is a read-only DTO projection of platformview.HostPolicyStatus (MUST-4/5).
type HostView struct {
	HostID      string          `json:"hostId"`
	Groups      []string        `json:"groups,omitempty"`
	Labels      []string        `json:"labels,omitempty"`
	Attachments []AttachmentDTO `json:"attachments,omitempty"`
	Verdicts    []VerdictDTO    `json:"verdicts,omitempty"`
	Meta        DTOViewMeta     `json:"meta"`
}

// PolicyView is a read-only DTO projection of platformview.GovernanceSummary (MUST-4/5).
type PolicyView struct {
	PolicyID       string       `json:"policyId"`
	MatchedRules   []RuleDTO    `json:"matchedRules,omitempty"`
	RecentVerdicts []VerdictDTO `json:"recentVerdicts,omitempty"`
	Meta           DTOViewMeta  `json:"meta"`
}
