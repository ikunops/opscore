package platformview

import (
	"context"
	"time"
)

// ObservabilityReader is the query contract platformview consumes from internal/observability.
// Platformview depends on this interface (Dependency Inversion); it never imports the frozen system.
type ObservabilityReader interface {
	QueryObservations(ctx context.Context, executionID string) ([]ObservationView, error)
}

// ClusterReader is the query contract platformview consumes from internal/cluster.
type ClusterReader interface {
	QueryPlacement(ctx context.Context, hostRef string) (*PlacementView, error)
	QueryMemberGroups(ctx context.Context, hostRef string) ([]string, error)
	QueryMemberLabels(ctx context.Context, hostRef string) ([]string, error)
}

// EnterpriseReader is the query contract platformview consumes from internal/enterprise.
type EnterpriseReader interface {
	QueryAttachments(ctx context.Context, targetRef string) ([]AttachmentView, error)
}

// GovernanceReader is the query contract platformview consumes from internal/governance.
type GovernanceReader interface {
	QueryVerdict(ctx context.Context, policyID string) (*VerdictView, error)
	QueryRules(ctx context.Context, policyID string) ([]RuleView, error)
}

// Readers bundles the four capability query contracts. A composition root injects the real
// capability instances adapted to these interfaces; the facade never creates them.
type Readers struct {
	Obs        ObservabilityReader
	Cluster    ClusterReader
	Enterprise EnterpriseReader
	Governance GovernanceReader
}

// Facade is the read-only Platform Integration Layer (MUST-1/3). It owns no data and holds no
// cached state (SHOULD-3: lazy aggregation). Each call hits the owning capability's query API at
// request time and returns a fresh, versioned value object.
type Facade struct {
	readers Readers
	now     func() time.Time
}

// New constructs a Facade. now defaults to time.Now; tests may override it for determinism.
func New(readers Readers) *Facade {
	return &Facade{readers: readers, now: time.Now}
}

func (f *Facade) meta(sourceCapability, sourceID string, related ...string) Meta {
	return Meta{
		SourceCapability: sourceCapability,
		SourceID:         sourceID,
		CollectedAt:      f.now(),
		RelatedIDs:       related,
	}
}

// GetExecutionOverview composes Observation + Placement + Attachments + Verdict for one execution.
func (f *Facade) GetExecutionOverview(ctx context.Context, executionID string) (ExecutionOverview, error) {
	if executionID == "" {
		return ExecutionOverview{}, ErrMissingID
	}
	ov := ExecutionOverview{
		ExecutionID: executionID,
		Meta:        f.meta("platform", executionID, executionID),
	}
	if f.readers.Obs != nil {
		if obs, err := f.readers.Obs.QueryObservations(ctx, executionID); err == nil && len(obs) > 0 {
			ov.Observation = &obs[0]
		}
	}
	if f.readers.Cluster != nil {
		if p, err := f.readers.Cluster.QueryPlacement(ctx, executionID); err == nil && p != nil {
			ov.Placement = p
		}
	}
	if f.readers.Enterprise != nil {
		if a, err := f.readers.Enterprise.QueryAttachments(ctx, executionID); err == nil {
			ov.Attachments = a
		}
	}
	if f.readers.Governance != nil {
		if v, err := f.readers.Governance.QueryVerdict(ctx, executionID); err == nil && v != nil {
			ov.Verdict = v
		}
	}
	return ov, nil
}

// GetHostPolicyStatus composes Enterprise attachments + Governance verdicts for a host.
func (f *Facade) GetHostPolicyStatus(ctx context.Context, hostRef string) (HostPolicyStatus, error) {
	if hostRef == "" {
		return HostPolicyStatus{}, ErrMissingID
	}
	st := HostPolicyStatus{
		HostID: hostRef,
		Meta:   f.meta("platform", hostRef, hostRef),
	}
	if f.readers.Enterprise != nil {
		if a, err := f.readers.Enterprise.QueryAttachments(ctx, hostRef); err == nil {
			st.Attachments = a
		}
	}
	if f.readers.Governance != nil {
		if v, err := f.readers.Governance.QueryVerdict(ctx, hostRef); err == nil && v != nil {
			st.Verdicts = []VerdictView{*v}
		}
	}
	return st, nil
}

// GetGovernanceSummary composes Governance rules + recent verdicts for a policy.
func (f *Facade) GetGovernanceSummary(ctx context.Context, policyID string) (GovernanceSummary, error) {
	if policyID == "" {
		return GovernanceSummary{}, ErrMissingID
	}
	sum := GovernanceSummary{
		PolicyID: policyID,
		Meta:     f.meta("platform", policyID, policyID),
	}
	if f.readers.Governance != nil {
		r, err := f.readers.Governance.QueryRules(ctx, policyID)
		if err != nil {
			return GovernanceSummary{}, err
		}
		sum.MatchedRules = r
		if v, err := f.readers.Governance.QueryVerdict(ctx, policyID); err == nil && v != nil {
			sum.RecentVerdicts = []VerdictView{*v}
		}
	}
	return sum, nil
}

// GetClusterPlacementView composes Cluster membership + placement for a host.
func (f *Facade) GetClusterPlacementView(ctx context.Context, hostRef string) (ClusterPlacementView, error) {
	if hostRef == "" {
		return ClusterPlacementView{}, ErrMissingID
	}
	pv := ClusterPlacementView{
		HostID: hostRef,
		Meta:   f.meta("platform", hostRef, hostRef),
	}
	if f.readers.Cluster != nil {
		if g, err := f.readers.Cluster.QueryMemberGroups(ctx, hostRef); err == nil {
			pv.Groups = g
		}
		if l, err := f.readers.Cluster.QueryMemberLabels(ctx, hostRef); err == nil {
			pv.Labels = l
		}
		if p, err := f.readers.Cluster.QueryPlacement(ctx, hostRef); err == nil && p != nil {
			pv.Placement = p
		}
	}
	return pv, nil
}
