// Package correlation implements Phase 10 Event Correlation Architecture — a read-only,
// cross-capability correlation layer that composes the four closed Phase 8 capabilities
// (Observability / Cluster / Enterprise / Governance) and internal/platformview into stable,
// explainable, scope-bounded Correlation Views.
//
// It is a PURE COMPOSITION EXTENSION over the frozen OpsCore v1 baseline (ADR-021). It adds no
// execution path, modifies no frozen contract, and owns nothing. See ADR-022 for the full spec.
//
// Invariants (ADR-022):
//   - MUST-0: inherits Phase 9 freeze; no new execution path, no Control Plane.
//   - MUST-1: read-only correlation; owns no data (Source -> Reader -> CorrelationView).
//   - MUST-2: reuses existing IDs; mints no CorrelationID / GlobalIncidentID / UnifiedEventID.
//   - MUST-3: no command surface; only Correlate / query / view.
//   - MUST-4: ownership preserved; correlation owns nothing (projection only).
//   - MUST-5: correlation != new model; CorrelationView is a projection of references.
//   - SHOULD-1: explainable correlation (every view carries Meta{SourceRefs, Reason, CorrelatedAt}).
//   - SHOULD-2: stable read contract under correlation/view/v1 (distinct from platform/view/v1).
//   - SHOULD-3: lazy aggregation; no cache / DB / background worker / event-queue ownership.
//   - SHOULD-4: correlation determinism; identical {events + state} -> identical CorrelationView
//     (inputs are sorted before projection to avoid map-iteration / time-order drift).
//   - SHOULD-5: correlation scope explicitness; every query declares Scope{Kind, Ref}; unbounded
//     correlation is forbidden (no global incident graph).
package correlation

import "time"

// ViewVersion is the stable read contract for all correlation views (SHOULD-2).
// Evolve by bumping to correlation/view/v2, never by editing the frozen capabilities.
const ViewVersion = "correlation/view/v1"

// Scope kinds (SHOULD-5). Every correlation query must declare one of these explicitly.
const (
	ScopeExecution = "execution" // bound by ExecutionID
	ScopePlugin    = "plugin"    // bound by PluginID
	ScopeHost      = "host"      // bound by HostRef
	ScopePolicy    = "policy"    // bound by PolicyID
)

// Meta carries explainability fields for every CorrelationView (SHOULD-1): each field can be
// traced back to the owning capabilities via SourceRefs.
type Meta struct {
	// SourceRefs lists the concrete existing IDs joined into this view (no new identity minted).
	SourceRefs []string `json:"sourceRefs"`
	// Reason explains why these events are correlated (human-readable, deterministic).
	Reason string `json:"reason"`
	// CorrelatedAt is captured at query time (SHOULD-3, fresh — never cached).
	CorrelatedAt time.Time `json:"correlatedAt"`
}

// Scope declares the correlation boundary (SHOULD-5). Unbounded correlation is forbidden.
type Scope struct {
	// Kind is one of ScopeExecution / ScopePlugin / ScopeHost / ScopePolicy.
	Kind string `json:"kind"`
	// Ref is the concrete existing id within that kind (ExecutionID / PluginID / HostRef / PolicyID).
	Ref string `json:"ref"`
}

// CorrelationView is a projection, never a domain entity (MUST-5). It composes references to the
// owning capabilities plus a single Meta (explainability). No CorrelatedExecution /
// OperationalIncident / UnifiedOperation type is defined.
type CorrelationView struct {
	Scope Scope `json:"scope"`

	// ExecutionRef is set only when Scope.Kind == execution; it reuses ExecutionID (MUST-2).
	ExecutionRef string `json:"executionRef,omitempty"`

	// Reference lists — each entry reuses an existing ID from its owning capability (MUST-2).
	ObservationRefs []string `json:"observationRefs,omitempty"` // ObsID list
	VerdictRefs     []string `json:"verdictRefs,omitempty"`     // PolicyID·RuleID list
	PlacementRefs   []string `json:"placementRefs,omitempty"`   // HostRef list
	PolicyRefs      []string `json:"policyRefs,omitempty"`      // AttachID list

	Meta Meta `json:"meta"`
}
