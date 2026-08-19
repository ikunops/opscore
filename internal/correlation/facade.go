package correlation

import (
	"context"
	"sort"
	"time"
)

// ObservabilityReader is the query contract correlation consumes from internal/observability.
// Correlation depends on this interface (Dependency Inversion); it never imports the frozen system.
// The reader returns existing ObsIDs relevant to the given scope (MUST-2: references only).
type ObservabilityReader interface {
	QueryObservationRefs(ctx context.Context, scope Scope) ([]string, error)
}

// ClusterReader is the query contract correlation consumes from internal/cluster.
// It returns existing HostRefs (PlacementRefs) relevant to the given scope.
type ClusterReader interface {
	QueryPlacementRefs(ctx context.Context, scope Scope) ([]string, error)
}

// EnterpriseReader is the query contract correlation consumes from internal/enterprise.
// It returns existing AttachIDs (PolicyRefs) relevant to the given scope.
type EnterpriseReader interface {
	QueryPolicyRefs(ctx context.Context, scope Scope) ([]string, error)
}

// GovernanceReader is the query contract correlation consumes from internal/governance.
// It returns existing PolicyID·RuleIDs (VerdictRefs) relevant to the given scope.
type GovernanceReader interface {
	QueryVerdictRefs(ctx context.Context, scope Scope) ([]string, error)
}

// Readers bundles the four capability query contracts. A composition root injects the real
// capability instances adapted to these interfaces; the Correlator never creates them.
type Readers struct {
	Obs        ObservabilityReader
	Cluster    ClusterReader
	Enterprise EnterpriseReader
	Governance GovernanceReader
}

// Correlator is the read-only Event Correlation layer (MUST-1/3). It owns no data and holds no
// cached state (SHOULD-3: lazy aggregation). Each call hits the owning capability's query API at
// request time and returns a fresh, versioned, scope-bounded value object.
type Correlator struct {
	readers Readers
	now     func() time.Time
}

// New constructs a Correlator. now defaults to time.Now; tests may override it for determinism.
func New(readers Readers) *Correlator {
	return &Correlator{readers: readers, now: time.Now}
}

// Correlate composes the relevant capability references for the given scope into a single
// CorrelationView (MUST-1/4/5). It is the ONLY public method — there is no command surface
// (MUST-3). Every input slice is sorted before projection to guarantee determinism (SHOULD-4).
func (c *Correlator) Correlate(ctx context.Context, scope Scope) (CorrelationView, error) {
	if err := validateScope(scope); err != nil {
		return CorrelationView{}, err
	}
	view := CorrelationView{
		Scope: scope,
		Meta: Meta{
			Reason:       correlationReason(scope),
			CorrelatedAt: c.now(),
		},
	}
	if scope.Kind == ScopeExecution {
		view.ExecutionRef = scope.Ref
	}

	var src []string

	if c.readers.Obs != nil {
		if refs, err := c.readers.Obs.QueryObservationRefs(ctx, scope); err == nil && len(refs) > 0 {
			sort.Strings(refs)
			view.ObservationRefs = refs
			src = append(src, refs...)
		}
	}
	if c.readers.Cluster != nil {
		if refs, err := c.readers.Cluster.QueryPlacementRefs(ctx, scope); err == nil && len(refs) > 0 {
			sort.Strings(refs)
			view.PlacementRefs = refs
			src = append(src, refs...)
		}
	}
	if c.readers.Enterprise != nil {
		if refs, err := c.readers.Enterprise.QueryPolicyRefs(ctx, scope); err == nil && len(refs) > 0 {
			sort.Strings(refs)
			view.PolicyRefs = refs
			src = append(src, refs...)
		}
	}
	if c.readers.Governance != nil {
		if refs, err := c.readers.Governance.QueryVerdictRefs(ctx, scope); err == nil && len(refs) > 0 {
			sort.Strings(refs)
			view.VerdictRefs = refs
			src = append(src, refs...)
		}
	}

	sort.Strings(src) // deterministic SourceRefs order regardless of reader call order
	view.Meta.SourceRefs = src
	return view, nil
}

// validateScope enforces MUST-0 (explicit boundary) / SHOULD-5: every query must declare a known
// Kind and a non-empty Ref. Unbounded correlation (empty/unknown scope) is rejected.
func validateScope(scope Scope) error {
	switch scope.Kind {
	case ScopeExecution, ScopePlugin, ScopeHost, ScopePolicy:
		if scope.Ref == "" {
			return ErrInvalidScope
		}
		return nil
	default:
		return ErrInvalidScope
	}
}

// correlationReason builds a deterministic, human-readable explanation of the join (SHOULD-1).
func correlationReason(scope Scope) string {
	return "correlated by " + scope.Kind + " " + scope.Ref +
		" across observation/verdict/placement/policy references"
}
